package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file covers four interlocking behaviors on the same endpoint:
//
//   - Revision pinning: a durable caller pins the exact show.action
//     revision to execute (TRACK-F-resting-mode.md §F4); the outer
//     journal, the nested FPP child journal, the audit, the response, a
//     replay, and a post-crash reconciliation all name the SAME pinned
//     revision even after a newer one is activated.
//   - Startup reconciliation must consult a stranded invocation's own
//     confirmed FPP child rather than overwriting it with a guess.
//   - A commands-row persist failure after a successful dispatch must
//     not tell the synchronous caller a different story than a
//     concurrent replay or a later restart.
//   - Periodic reconciliation must retry while the process stays up,
//     without racing a genuinely in-flight request.

// --- Revision pinning acceptance ---

// TestActionInvokeRevisionPinningAcceptance covers the target scenario:
// queue revision 1 targeting adapter A, activate revision 2 targeting B,
// invoke the queued revision, observe only A called, and confirm every
// surface — outer journal, child journal, audit, response, replay, and
// post-restart reconciliation — identifies revision 1.
func TestActionInvokeRevisionPinningAcceptance(t *testing.T) {
	srvA, fppA := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	srvB, fppB := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{
		{InstanceID: "player-a", Endpoint: srvA.URL},
		{InstanceID: "player-b", Endpoint: srvB.URL},
	}
	setup.obs.setObs(nil)

	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, setup.svc, admin.ID)
	deps := setup.deps()
	deps.Config = setup.st
	api := New(deps, Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 20 * time.Millisecond,
	})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "toggle", `{
		"show": "halloween-2026", "label": "Toggle", "safetyClass": "stop",
		"target": {"integration": "fpp", "instanceId": "player-a", "primitive": "stopPlaylist", "params": {}}
	}`)
	mustPutAction(t, api, token, "toggle", `{
		"show": "halloween-2026", "label": "Toggle", "safetyClass": "stop",
		"target": {"integration": "fpp", "instanceId": "player-b", "primitive": "stopPlaylist", "params": {}}
	}`)

	// Revision 2 is now active; pin the request to revision 1.
	req := newJSONRequest(t, http.MethodPost, "/api/v1/actions/toggle/invocations",
		`{"idempotencyKey":"pin-key-1","requestedRevision":1}`, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if fppA.hitCount() != 1 {
		t.Errorf("adapter A hits = %d, want exactly 1", fppA.hitCount())
	}
	if fppB.hitCount() != 0 {
		t.Errorf("adapter B hits = %d, want 0 (the pinned revision must never reach the currently-active target)", fppB.hitCount())
	}

	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	if rev, _ := result["revision"].(float64); int64(rev) != 1 {
		t.Errorf("response revision = %v, want 1", result["revision"])
	}
	cmdID, _ := result["id"].(string)
	if cmdID == "" {
		t.Fatalf("response carried no command id; body: %s", body)
	}

	// Outer journal.
	outer, err := setup.st.GetCommand(context.Background(), cmdID)
	if err != nil {
		t.Fatalf("get outer command: %v", err)
	}
	if outer.RequestedRevision != "1" {
		t.Errorf("outer requested_revision = %q, want \"1\"", outer.RequestedRevision)
	}

	// Child journal (the nested FPP dispatch).
	child, err := setup.st.GetCommandByIdempotencyKey(context.Background(), actionInvokeFPPChildIdempotencyKeyPrefix+cmdID)
	if err != nil {
		t.Fatalf("get child command: %v", err)
	}
	if child.RequestedRevision != "1" {
		t.Errorf("child requested_revision = %q, want \"1\"", child.RequestedRevision)
	}
	if child.TargetID != "player-a" {
		t.Errorf("child target = %q, want player-a", child.TargetID)
	}

	// Audit: the dispatch entry's own params name the revision.
	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var sawDispatchRevision bool
	for _, e := range entries {
		if e.CommandID == cmdID && e.Kind == identity.AuditDispatch {
			if rev, _ := e.Params["revision"].(float64); int64(rev) == 1 {
				sawDispatchRevision = true
			}
		}
	}
	if !sawDispatchRevision {
		t.Error("no dispatch audit entry named revision 1")
	}

	// Replay: the same idempotency key, still revision 1.
	req2 := newJSONRequest(t, http.MethodPost, "/api/v1/actions/toggle/invocations",
		`{"idempotencyKey":"pin-key-1"}`, map[string]string{"Authorization": "Bearer " + token})
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	result2 := m2["result"].(map[string]any)
	if rev, _ := result2["revision"].(float64); int64(rev) != 1 {
		t.Errorf("replay revision = %v, want 1", result2["revision"])
	}
	if fppA.hitCount() != 1 || fppB.hitCount() != 0 {
		t.Errorf("replay must not re-dispatch: A=%d B=%d", fppA.hitCount(), fppB.hitCount())
	}

	// Restart recovery: strand a SECOND pinned invocation (own idempotency
	// key), close and reopen the store, and run the exact production
	// startup order (FPP children before the outer sweep — coordinator.go).
	strandOuter, err := setup.st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "stranded-pin-outer", IdempotencyKey: "pin-key-2", Action: "action.invoke:fpp",
		TargetKind: actionInvokeTargetKind, TargetID: "toggle", RequestedRevision: "1",
		IssuerPrincipalID: admin.ID, IssuerPrincipalName: admin.Name,
		ConfirmationMethod: "evidence", State: "pending", OutcomeReason: actionInvokePendingOutcomeReason,
	})
	if err != nil {
		t.Fatalf("insert stranded outer: %v", err)
	}
	dispatchedAt := testNow.Add(-time.Minute)
	dispatchedState := "dispatched"
	if err := setup.st.UpdateCommandOutcome(context.Background(), strandOuter.ID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("mark outer dispatched: %v", err)
	}
	strandChild, err := setup.st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "stranded-pin-child", IdempotencyKey: actionInvokeFPPChildIdempotencyKeyPrefix + strandOuter.ID,
		Action: "fpp.stop_playlist", TargetKind: "fpp", TargetID: "player-a", RequestedRevision: "1",
		IssuerPrincipalID: admin.ID, IssuerPrincipalName: admin.Name,
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("insert stranded child: %v", err)
	}
	if err := setup.st.UpdateCommandOutcome(context.Background(), strandChild.ID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("mark child dispatched: %v", err)
	}

	reopened := closeAndReopenStore(t, setup)
	deps2 := setup.deps()
	deps2.Commands, deps2.Config, deps2.Identity, deps2.Observations, deps2.FPP = reopened.st, reopened.st, reopened.svc, setup.obs, setup.fppLister

	if _, err := ReconcileStrandedFPPCommands(context.Background(), deps2, fixedClock(testNow), testLogger()); err != nil {
		t.Fatalf("ReconcileStrandedFPPCommands: %v", err)
	}
	if _, err := ReconcileStrandedActionInvocations(context.Background(), deps2, fixedClock(testNow), testLogger()); err != nil {
		t.Fatalf("ReconcileStrandedActionInvocations: %v", err)
	}

	afterOuter, err := reopened.st.GetCommand(context.Background(), strandOuter.ID)
	if err != nil {
		t.Fatalf("get outer after reconcile: %v", err)
	}
	if afterOuter.RequestedRevision != "1" {
		t.Errorf("post-reconcile outer requested_revision = %q, want \"1\"", afterOuter.RequestedRevision)
	}
	if afterOuter.State != "resolved" {
		t.Errorf("post-reconcile outer state = %q, want resolved", afterOuter.State)
	}
}

// --- helpers ---

// closeAndReopenStore closes setup's store and identity data, then opens
// a FRESH *store.Store/identity.Service against the SAME on-disk
// directory — a real coordinator restart, not a simulation, for tests
// that must prove behavior survives a process boundary.
type reopenedStore struct {
	st  *store.Store
	svc identity.Service
}

func closeAndReopenStore(t *testing.T, setup *fppCommandTestSetup) reopenedStore {
	t.Helper()
	if err := setup.st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	st, err := store.Open(context.Background(), setup.storeDir, nil, store.WithClock(fixedClock(testNow)))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, fixedClock(testNow), filepath.Join(filepath.Dir(setup.storeDir), "data"), identity.WithLogger(testLogger()))
	return reopenedStore{st: st, svc: svc}
}

// --- Reconciliation consults a confirmed FPP child ---

// TestReconcileConsultsConfirmedFPPChildRatherThanOverwriting proves
// a stranded outer row whose nested FPP child resolves
// CONFIRMED (via ReconcileStrandedFPPCommands' own re-evaluated Confirm
// predicate, run first, matching coordinator.go's production order) must
// reconstruct a confirmed outer result, never an unconditional guess.
func TestReconcileConsultsConfirmedFPPChildRatherThanOverwriting(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: "http://unused.invalid"}}
	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)

	outer, err := setup.st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "outer-confirmed-child", IdempotencyKey: "outer-key-confirmed", Action: "action.invoke:fpp",
		TargetKind: actionInvokeTargetKind, TargetID: "stop-now",
		IssuerPrincipalID: admin.ID, IssuerPrincipalName: admin.Name,
		ConfirmationMethod: "evidence", State: "pending", OutcomeReason: actionInvokePendingOutcomeReason,
	})
	if err != nil {
		t.Fatalf("insert outer: %v", err)
	}
	dispatchedAt := testNow.Add(-time.Minute)
	dispatchedState := "dispatched"
	if err := setup.st.UpdateCommandOutcome(context.Background(), outer.ID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("mark outer dispatched: %v", err)
	}

	child, err := setup.st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "child-confirmed", IdempotencyKey: actionInvokeFPPChildIdempotencyKeyPrefix + outer.ID,
		Action: "fpp.stop_playlist", TargetKind: "fpp", TargetID: "bench-fpp",
		IssuerPrincipalID: admin.ID, IssuerPrincipalName: admin.Name,
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("insert child: %v", err)
	}
	if err := setup.st.UpdateCommandOutcome(context.Background(), child.ID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("mark child dispatched: %v", err)
	}

	// stopPlaylist's own Confirm predicate: "idle" evidence collected
	// after dispatchedAt confirms it.
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	deps := setup.deps()
	if _, err := ReconcileStrandedFPPCommands(context.Background(), deps, fixedClock(testNow), testLogger()); err != nil {
		t.Fatalf("ReconcileStrandedFPPCommands: %v", err)
	}
	resolvedChild, err := setup.st.GetCommand(context.Background(), child.ID)
	if err != nil {
		t.Fatalf("get child: %v", err)
	}
	if resolvedChild.State != "resolved" {
		t.Fatalf("child state = %q, want resolved (fixture setup is wrong if this fails)", resolvedChild.State)
	}

	if _, err := ReconcileStrandedActionInvocations(context.Background(), deps, fixedClock(testNow), testLogger()); err != nil {
		t.Fatalf("ReconcileStrandedActionInvocations: %v", err)
	}
	resolvedOuter, err := setup.st.GetCommand(context.Background(), outer.ID)
	if err != nil {
		t.Fatalf("get outer: %v", err)
	}
	var payload actionInvokeResultPayload
	_ = json.Unmarshal([]byte(resolvedOuter.ResultJSON), &payload)
	if payload.Outcome != outcomeWordConfirmed {
		t.Errorf("outer outcome = %q, want %q (a confirmed child must never be overwritten with a guess)", payload.Outcome, outcomeWordConfirmed)
	}
	if resolvedOuter.OutcomeReason == "" {
		t.Error("outer outcome reason is empty, want it to name the child command")
	}
}

// --- A commands-row persist failure tells one story ---

// installFailCommandsUpdateTrigger fails every UPDATE against the
// commands table (a real SQLite trigger, matching installFailAuditTrigger's
// own precedent one file over — CLAUDE.md's own standing "duplication
// found the bug" lesson came from two independently-written decoders, and
// a mock here would not exercise the same failure the production driver
// hits).
func installFailCommandsUpdateTrigger(t *testing.T, storeDir string) {
	t.Helper()
	dbPath := filepath.Join(storeDir, "showmesh.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw connection to %q: %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()

	_, err = raw.ExecContext(context.Background(), `
		CREATE TRIGGER fail_commands_update BEFORE UPDATE ON commands
		WHEN NEW.state = 'resolved'
		BEGIN SELECT RAISE(ABORT, 'injected commands-resolve failure'); END;
	`)
	if err != nil {
		t.Fatalf("install fail_commands_update trigger: %v", err)
	}
}

// TestActionInvokeOutcomePersistFailureRespondsPendingConsistently proves
// that when the final commands-row write (marking the row "resolved")
// fails, THIS caller's own synchronous response must
// report the SAME "pending" story a concurrent replay would read from
// the still-pending row — never a "resolved" claim the row itself
// contradicts.
func TestActionInvokeOutcomePersistFailureRespondsPendingConsistently(t *testing.T) {
	fppSrv, fppFake := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs(nil)

	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, setup.svc, admin.ID)
	deps := setup.deps()
	deps.Config = setup.st
	api := New(deps, Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 20 * time.Millisecond,
	})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "stop-now", validShowActionFPPStopBody)

	// The interim (dispatch-attribution) write happens BEFORE this
	// trigger fires (state stays "pending" for that write), so only the
	// FINAL resolve write — state='resolved' — is the one this proves.
	installFailCommandsUpdateTrigger(t, setup.storeDir)

	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("stop-now", "persist-fail-key", token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the outward effect still ran); body: %s", resp.StatusCode, body)
	}
	if fppFake.hitCount() != 1 {
		t.Errorf("fpp hits = %d, want exactly 1 (dispatch itself must still happen)", fppFake.hitCount())
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	if result["state"] != "pending" {
		t.Fatalf("state = %v, want \"pending\" (the row was never durably marked resolved); body: %s", result["state"], body)
	}
	if result["outcome"] != nil {
		t.Errorf("outcome = %v, want null (a pending result must not carry one)", result["outcome"])
	}
	if reason, _ := result["outcomeReason"].(string); reason == "" {
		t.Error("outcomeReason is empty, want a stated reason")
	}

	// The row itself must agree: still "pending".
	cmdID, _ := result["id"].(string)
	row, err := setup.st.GetCommand(context.Background(), cmdID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if row.State != "pending" {
		t.Errorf("stored row state = %q, want \"pending\" (matching the response — one dispatch, one story)", row.State)
	}
}

// --- Periodic reconciliation retries while the process stays up,
// without racing a genuinely in-flight request ---

// TestActionInvokeReconcileMinAgeProtectsLiveRequests proves the safety
// rail a periodic sweep needs that a one-shot startup sweep does not: a
// row too young to be provably stranded (it could still be a live
// request's own in-flight dispatch) is left untouched.
func TestActionInvokeReconcileMinAgeProtectsLiveRequests(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.Commands = st

	young, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "young-row", IdempotencyKey: "young-key", Action: "action.invoke:fpp",
		TargetKind: actionInvokeTargetKind, TargetID: "start-main",
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("insert young row: %v", err)
	}

	n, err := reconcileStrandedActionInvocations(context.Background(), deps, fixedClock(testNow), testLogger(), actionInvokeReconcileMinAge)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Fatalf("resolved = %d, want 0 (a row this young could still be a live request)", n)
	}
	after, err := st.GetCommand(context.Background(), young.ID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if after.State != "pending" {
		t.Errorf("state = %q, want untouched (\"pending\")", after.State)
	}

	// The SAME row, once old enough, is fair game.
	old := fixedClock(testNow.Add(actionInvokeReconcileMinAge + time.Second))
	n2, err := reconcileStrandedActionInvocations(context.Background(), deps, old, testLogger(), actionInvokeReconcileMinAge)
	if err != nil {
		t.Fatalf("reconcile (aged): %v", err)
	}
	if n2 != 1 {
		t.Fatalf("resolved = %d, want 1 once the row is old enough", n2)
	}
}

// TestRunActionInvokeReconciliationLoopRetriesUntilResolved proves the
// loop keeps retrying — it does not give up after one failed or empty
// pass — for as long as the process
// (here, the test's own context) stays up.
func TestRunActionInvokeReconciliationLoopRetriesUntilResolved(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.Commands = st

	_, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "loop-row", IdempotencyKey: "loop-key", Action: "action.invoke:fpp",
		TargetKind: actionInvokeTargetKind, TargetID: "start-main",
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("insert row: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		runActionInvokeReconciliationLoop(ctx, deps, fixedClock(testNow.Add(actionInvokeReconcileMinAge+time.Minute)), testLogger(), 20*time.Millisecond)
		close(done)
	}()
	<-done

	row, err := st.GetCommand(context.Background(), "loop-row")
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if row.State != "resolved" {
		t.Errorf("state = %q, want \"resolved\" (the periodic loop must have retried and resolved it)", row.State)
	}
}

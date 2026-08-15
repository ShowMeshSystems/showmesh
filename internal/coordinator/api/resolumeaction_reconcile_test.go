package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Review fix 1's own proof, the Resolume-action sibling of
// fppcommand_reconcile_test.go: a Resolume command row a prior process
// left dispatched-but-unresolved must be resolved with a stated state and
// reason, never left blank forever — and, unlike FPP, this coordinator has
// no way to re-evaluate a Resolume action's own confirming evidence
// retroactively (resolumeaction_reconcile.go's own top comment), so every
// stranded row resolves "unconfirmed", not sometimes-confirmed.

// strandResolumeCommand mirrors fppcommand_reconcile_test.go's own
// strandCommand exactly, narrowed to this seam's fixed target — a row
// inserted and left with resolved_at NULL, never going through the live
// handler at all, simulating what a killed or crashed process leaves
// behind. paramsJSON may be "" (canonicalized to "{}" on replay, matching
// resolveResolumeActionReplay's own rule) for a test that never replays
// this row over HTTP.
func strandResolumeCommand(t *testing.T, st *store.Store, id, action, paramsJSON string, dispatchedAt *time.Time) store.CommandRecord {
	t.Helper()
	rec, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: id, IdempotencyKey: "key-" + id, Action: action, ParamsJSON: paramsJSON,
		TargetKind: resolumeActionTargetKind, TargetID: resolumeActionTargetID,
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("strandResolumeCommand: insert: %v", err)
	}
	dispatchedState := "dispatched"
	if err := st.UpdateCommandOutcome(context.Background(), rec.ID, store.CommandOutcomeUpdate{
		DispatchedAt: dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("strandResolumeCommand: mark dispatched: %v", err)
	}
	rec, err = st.GetCommand(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("strandResolumeCommand: re-read: %v", err)
	}
	return rec
}

func TestReconcileStrandedResolumeActionsResolvesUnconfirmedWithAStatedReason(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	dispatchedAt := testNow.Add(-time.Minute)
	strand := strandResolumeCommand(t, setup.st, "stranded-resolume-1", "resolume.blackout", "", &dispatchedAt)

	resolved, err := ReconcileStrandedResolumeActions(context.Background(), setup.deps(), fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedResolumeActions: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved = %d, want 1", resolved)
	}

	rec, err := setup.st.GetCommand(context.Background(), strand.ID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if rec.State != "resolved" {
		t.Errorf("state = %q, want \"resolved\"", rec.State)
	}
	if rec.ResolvedAt == nil {
		t.Fatal("resolved_at is nil, want it set")
	}
	if rec.OutcomeState != string(observation.StateNotCollected) {
		t.Errorf("outcome_state = %q, want %q — NEVER blank (this is the whole point of this fix)", rec.OutcomeState, observation.StateNotCollected)
	}
	if rec.OutcomeReason == "" {
		t.Error("outcome_reason is empty, want a stated reason (ADR-020)")
	}

	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var outcomeCount int
	for _, e := range entries {
		if e.CommandID == strand.ID && e.Kind == identity.AuditOutcome {
			outcomeCount++
			if e.Outcome != string(ResolumeOutcomeUnconfirmed) {
				t.Errorf("outcome audit entry Outcome = %q, want %q", e.Outcome, ResolumeOutcomeUnconfirmed)
			}
			if e.OutcomeState != string(observation.StateNotCollected) {
				t.Errorf("outcome audit entry OutcomeState = %q, want %q", e.OutcomeState, observation.StateNotCollected)
			}
		}
	}
	if outcomeCount != 1 {
		t.Errorf("outcome audit entries = %d, want exactly 1", outcomeCount)
	}
}

// TestReconcileStrandedResolumeActionsMakesTheReplayRaceUnreachablePermanently
// is this fix's own sharpest test: replaying the idempotency key BEFORE
// reconciliation runs reproduces the accepted, narrow "" race
// (resolveResolumeActionReplay's own doc comment); replaying it AFTER
// reconciliation runs must never see that shape again.
func TestReconcileStrandedResolumeActionsMakesTheReplayRaceUnreachablePermanently(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	// No DispatchedAt at all — the shape a crash between the audit write
	// and calling Dispatch leaves behind (resolumeaction.go's own
	// handleDispatchResolumeAction never writes DispatchedAt until AFTER
	// Dispatch returns).
	strandResolumeCommand(t, setup.st, "stranded-resolume-2", "resolume.launch_clip", `{"id":"clip-1"}`, nil)

	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	// Before reconciliation: replaying the stranded key reproduces the
	// accepted blank race.
	req1 := newResolumeActionRequest(t, resolumeActionBody("launchClip", "key-stranded-resolume-2", `{"id":"clip-1"}`), token)
	resp1, body1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("pre-reconciliation replay status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}
	m1 := decodeMap(t, body1)
	result1, _ := m1["result"].(map[string]any)
	if result1["outcome"] != "" {
		t.Fatalf("pre-reconciliation outcome = %v, want \"\" (fixture setup is wrong if this fails)", result1["outcome"])
	}

	if _, err := ReconcileStrandedResolumeActions(context.Background(), setup.deps(), fixedClock(testNow), testLogger()); err != nil {
		t.Fatalf("ReconcileStrandedResolumeActions: %v", err)
	}

	// After reconciliation: the SAME key now replays a resolved,
	// non-blank outcome — this row can never again answer "".
	req2 := newResolumeActionRequest(t, resolumeActionBody("launchClip", "key-stranded-resolume-2", `{"id":"clip-1"}`), token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("post-reconciliation replay status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	result2, _ := m2["result"].(map[string]any)
	if result2["outcome"] != string(ResolumeOutcomeUnconfirmed) {
		t.Errorf("post-reconciliation outcome = %v, want %q", result2["outcome"], ResolumeOutcomeUnconfirmed)
	}
	if result2["outcomeReason"] == "" || result2["outcomeReason"] == nil {
		t.Error("post-reconciliation outcomeReason is empty, want a stated reason")
	}
}

// TestReconcileStrandedResolumeActionsSkipsFPPRows proves the TargetKind
// filter is real: an FPP row left unresolved must be untouched by this
// sweep (it is ReconcileStrandedFPPCommands' own job).
func TestReconcileStrandedResolumeActionsSkipsFPPRows(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	dispatchedAt := testNow.Add(-time.Minute)
	rec, err := setup.st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "stranded-fpp-1", IdempotencyKey: "key-stranded-fpp-1", Action: "fpp.stop_playlist",
		TargetKind: "fpp", TargetID: "bench-fpp",
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("insert fpp command: %v", err)
	}
	dispatchedState := "dispatched"
	if err := setup.st.UpdateCommandOutcome(context.Background(), rec.ID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}

	resolved, err := ReconcileStrandedResolumeActions(context.Background(), setup.deps(), fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedResolumeActions: %v", err)
	}
	if resolved != 0 {
		t.Fatalf("resolved = %d, want 0 (an fpp-target row is not this sweep's job)", resolved)
	}

	after, err := setup.st.GetCommand(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if after.State != "dispatched" {
		t.Errorf("state = %q, want it untouched (\"dispatched\")", after.State)
	}
}

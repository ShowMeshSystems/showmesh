package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is A1's own proof: an action-invocation command row a prior
// process left dispatched-but-unresolved must be resolved with a stated
// state and reason, never left blank forever — mirroring
// resolumeaction_reconcile_test.go's identical shape for a third command
// family.

func strandActionInvokeCommand(t *testing.T, st *store.Store, id, actionID string, dispatchedAt *time.Time) store.CommandRecord {
	t.Helper()
	rec, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: id, IdempotencyKey: "key-" + id, Action: "action.invoke:fpp",
		TargetKind: actionInvokeTargetKind, TargetID: actionID,
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("strandActionInvokeCommand: insert: %v", err)
	}
	dispatchedState := "dispatched"
	if err := st.UpdateCommandOutcome(context.Background(), rec.ID, store.CommandOutcomeUpdate{
		DispatchedAt: dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("strandActionInvokeCommand: mark dispatched: %v", err)
	}
	rec, err = st.GetCommand(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("strandActionInvokeCommand: re-read: %v", err)
	}
	return rec
}

func TestReconcileStrandedActionInvocationsResolvesUnconfirmedWithAStatedReason(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.Commands = st

	dispatchedAt := testNow.Add(-time.Minute)
	strand := strandActionInvokeCommand(t, st, "stranded-invoke-1", "start-main", &dispatchedAt)

	resolved, err := ReconcileStrandedActionInvocations(context.Background(), deps, fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedActionInvocations: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved = %d, want 1", resolved)
	}

	rec, err := st.GetCommand(context.Background(), strand.ID)
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
		t.Errorf("outcome_state = %q, want %q — NEVER blank", rec.OutcomeState, observation.StateNotCollected)
	}
	if rec.OutcomeReason == "" {
		t.Error("outcome_reason is empty, want a stated reason (ADR-020)")
	}

	entries, err := svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var outcomeCount int
	for _, e := range entries {
		if e.CommandID == strand.ID && e.Kind == identity.AuditOutcome {
			outcomeCount++
			if e.Outcome != outcomeWordUnconfirmed {
				t.Errorf("outcome audit entry Outcome = %q, want %q", e.Outcome, outcomeWordUnconfirmed)
			}
		}
	}
	if outcomeCount != 1 {
		t.Errorf("outcome audit entries = %d, want exactly 1", outcomeCount)
	}
}

// TestReconcileStrandedActionInvocationsMakesTheReplayRaceUnreachablePermanently
// is this fix's own sharpest test: replaying a stranded row's idempotency
// key BEFORE reconciliation runs reproduces the accepted, narrow ""
// race; replaying it AFTER reconciliation runs must never see that shape
// again — the openapi text's "narrow race" claim is only true once this
// sweep exists.
func TestReconcileStrandedActionInvocationsMakesTheReplayRaceUnreachablePermanently(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.Commands = st
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "start-main", validShowActionFPPBody)

	// No DispatchedAt at all — the shape a crash between the audit write
	// and dispatch leaves behind.
	rec, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "stranded-invoke-2", IdempotencyKey: "stranded-invoke-key-2", Action: "action.invoke:fpp",
		TargetKind: actionInvokeTargetKind, TargetID: "start-main",
		IssuerPrincipalID: admin.ID, IssuerPrincipalName: admin.Name,
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("insert stranded command: %v", err)
	}
	_ = rec

	req1 := invokeActionRequest("start-main", "stranded-invoke-key-2", token)
	resp1, body1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != 200 {
		t.Fatalf("pre-reconciliation replay status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}
	m1 := decodeMap(t, body1)
	result1, _ := m1["result"].(map[string]any)
	if result1["state"] != "pending" {
		t.Fatalf("pre-reconciliation state = %v, want \"pending\" (fixture setup is wrong if this fails)", result1["state"])
	}
	if result1["outcome"] != nil {
		t.Fatalf("pre-reconciliation outcome = %v, want null (a pending result must not carry one)", result1["outcome"])
	}
	if reason, _ := result1["outcomeReason"].(string); reason == "" {
		t.Fatalf("pre-reconciliation outcomeReason is empty, want a stated reason even while pending")
	}

	if _, err := ReconcileStrandedActionInvocations(context.Background(), deps, fixedClock(testNow), testLogger()); err != nil {
		t.Fatalf("ReconcileStrandedActionInvocations: %v", err)
	}

	req2 := invokeActionRequest("start-main", "stranded-invoke-key-2", token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != 200 {
		t.Fatalf("post-reconciliation replay status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	result2, _ := m2["result"].(map[string]any)
	if result2["outcome"] != outcomeWordUnconfirmed {
		t.Errorf("post-reconciliation outcome = %v, want %q", result2["outcome"], outcomeWordUnconfirmed)
	}
	if result2["outcomeReason"] == "" || result2["outcomeReason"] == nil {
		t.Error("post-reconciliation outcomeReason is empty, want a stated reason")
	}
}

// TestReconcileStrandedActionInvocationsSkipsOtherKinds proves the
// TargetKind filter is real: an fpp/resolume-target row left unresolved
// must be untouched by this sweep.
func TestReconcileStrandedActionInvocationsSkipsOtherKinds(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.Commands = st

	dispatchedAt := testNow.Add(-time.Minute)
	rec, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "stranded-fpp-invoke-1", IdempotencyKey: "key-stranded-fpp-invoke-1", Action: "fpp.stop_playlist",
		TargetKind: "fpp", TargetID: "bench-fpp",
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("insert fpp command: %v", err)
	}
	dispatchedState := "dispatched"
	if err := st.UpdateCommandOutcome(context.Background(), rec.ID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}

	resolved, err := ReconcileStrandedActionInvocations(context.Background(), deps, fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedActionInvocations: %v", err)
	}
	if resolved != 0 {
		t.Fatalf("resolved = %d, want 0 (an fpp-target row is not this sweep's job)", resolved)
	}

	after, err := st.GetCommand(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if after.State != "dispatched" {
		t.Errorf("state = %q, want it untouched (\"dispatched\")", after.State)
	}
}

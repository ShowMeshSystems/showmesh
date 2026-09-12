package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the resync-request timeout sweep's own test suite
// (resyncrequest_reconcile.go): a node that never answers an
// asset.inventory.request must have that row resolved "failed" once it
// has sat "dispatched" for resyncRequestTimeout, while a row still young
// enough to be a live in-flight request is left untouched.

func strandResyncCommand(t *testing.T, st *store.Store, id, nodeID string, dispatchedAt time.Time) store.CommandRecord {
	t.Helper()
	rec, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: id, IdempotencyKey: "key-" + id, Action: assetsync.ResyncCommandAction,
		TargetKind: assetsync.ResyncCommandTargetKind, TargetID: nodeID,
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("strandResyncCommand: insert: %v", err)
	}
	dispatchedState := "dispatched"
	if err := st.UpdateCommandOutcome(context.Background(), rec.ID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("strandResyncCommand: mark dispatched: %v", err)
	}
	rec, err = st.GetCommand(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("strandResyncCommand: re-read: %v", err)
	}
	return rec
}

func TestReconcileTimedOutResyncRequestsFailsAnOldRowAndLeavesAYoungOne(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.Commands = st

	oldDispatchedAt := testNow.Add(-resyncRequestTimeout - time.Second)
	old := strandResyncCommand(t, st, "resync-old", "render-01", oldDispatchedAt)

	youngDispatchedAt := testNow.Add(-resyncRequestTimeout + time.Second)
	young := strandResyncCommand(t, st, "resync-young", "render-02", youngDispatchedAt)

	resolved, err := reconcileTimedOutResyncRequests(context.Background(), deps, fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("reconcileTimedOutResyncRequests: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved = %d, want 1", resolved)
	}

	oldRec, err := st.GetCommand(context.Background(), old.ID)
	if err != nil {
		t.Fatalf("get old command: %v", err)
	}
	if oldRec.State != "failed" {
		t.Errorf("old row state = %q, want %q", oldRec.State, "failed")
	}
	if oldRec.ResolvedAt == nil {
		t.Error("old row resolved_at is nil, want it set")
	}
	if oldRec.OutcomeState != mqttproto.OutcomeFailed {
		t.Errorf("old row outcome_state = %q, want %q", oldRec.OutcomeState, mqttproto.OutcomeFailed)
	}
	if oldRec.OutcomeReason == "" {
		t.Error("old row outcome_reason is empty, want a stated timeout reason")
	}

	youngRec, err := st.GetCommand(context.Background(), young.ID)
	if err != nil {
		t.Fatalf("get young command: %v", err)
	}
	if youngRec.State != "dispatched" {
		t.Errorf("young row state = %q, want it left untouched at %q", youngRec.State, "dispatched")
	}
	if youngRec.ResolvedAt != nil {
		t.Error("young row resolved_at is set, want it left unresolved")
	}
}

func TestReconcileTimedOutResyncRequestsIgnoresOtherCommandFamiliesAndAlreadyResolvedRows(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.Commands = st

	oldDispatchedAt := testNow.Add(-resyncRequestTimeout - time.Second)

	// A different command family, dispatched just as long ago: this sweep
	// must not touch it.
	other, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "other-1", IdempotencyKey: "key-other-1", Action: "asset.fetch",
		TargetKind: assetsync.ResyncCommandTargetKind, TargetID: "render-01",
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("insert other command: %v", err)
	}
	dispatchedState := "dispatched"
	if err := st.UpdateCommandOutcome(context.Background(), other.ID, store.CommandOutcomeUpdate{
		DispatchedAt: &oldDispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("mark other command dispatched: %v", err)
	}

	// An old resync row already resolved (e.g. confirmed by a fresh
	// report): must not be re-resolved.
	already := strandResyncCommand(t, st, "resync-already-resolved", "render-02", oldDispatchedAt)
	resolvedAt := testNow.Add(-time.Second)
	resolvedState := "resolved"
	if err := st.UpdateCommandOutcome(context.Background(), already.ID, store.CommandOutcomeUpdate{
		ResolvedAt: &resolvedAt, State: &resolvedState,
		OutcomeState: strPtr(mqttproto.OutcomeConfirmed), OutcomeReason: strPtr("already confirmed"),
	}); err != nil {
		t.Fatalf("resolve already-resolved command: %v", err)
	}

	resolved, err := reconcileTimedOutResyncRequests(context.Background(), deps, fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("reconcileTimedOutResyncRequests: %v", err)
	}
	if resolved != 0 {
		t.Fatalf("resolved = %d, want 0", resolved)
	}

	otherRec, err := st.GetCommand(context.Background(), other.ID)
	if err != nil {
		t.Fatalf("get other command: %v", err)
	}
	if otherRec.State != "dispatched" {
		t.Errorf("other command family state = %q, want it untouched at %q", otherRec.State, "dispatched")
	}

	alreadyRec, err := st.GetCommand(context.Background(), already.ID)
	if err != nil {
		t.Fatalf("get already-resolved command: %v", err)
	}
	if alreadyRec.OutcomeState != mqttproto.OutcomeConfirmed || alreadyRec.OutcomeReason != "already confirmed" {
		t.Errorf("already-resolved command = %+v, want its own resolution untouched", alreadyRec)
	}
}

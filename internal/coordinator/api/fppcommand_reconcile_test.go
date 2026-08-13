package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Step 7 seam C review defect 5's own proof: a command left
// state='dispatched' with resolved_at NULL — the shape a coordinator
// restart (or, before this review pass, defect 4's own bug) leaves behind
// — must be resolved with a stated state and reason, never left blank
// forever. Reuses [newFPPCommandTestSetup] from fppcommand_handler_test.go
// (same package): a real store.Store and a real identity.Service, exactly
// like every other test in this file's family.

// strandCommand simulates what a killed or crashed process leaves behind:
// a commands row inserted and marked dispatched, with resolved_at still
// NULL — never going through the live handler at all, because the whole
// point of this sweep is to resolve rows no live request is watching.
func strandCommand(t *testing.T, st *store.Store, id, action, targetID string, dispatchedAt *time.Time) store.CommandRecord {
	t.Helper()
	rec, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: id, IdempotencyKey: "key-" + id, Action: action,
		TargetKind: "fpp", TargetID: targetID,
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("strandCommand: insert: %v", err)
	}
	dispatchedState := "dispatched"
	if err := st.UpdateCommandOutcome(context.Background(), rec.ID, store.CommandOutcomeUpdate{
		DispatchedAt: dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("strandCommand: mark dispatched: %v", err)
	}
	rec, err = st.GetCommand(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("strandCommand: re-read: %v", err)
	}
	return rec
}

func TestReconcileStrandedFPPCommandsResolvesConfirmedRow(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	dispatchedAt := testNow.Add(-time.Minute)
	strand := strandCommand(t, setup.st, "stranded-1", auditActionFPPStopPlaylist, "bench-fpp", &dispatchedAt)

	// Evidence collected AFTER the stranded dispatch — a real poll landed
	// after the crash, which a live process would have picked up itself
	// had it still been running.
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "idle", testNow, testNow),
	})

	resolved, err := ReconcileStrandedFPPCommands(context.Background(), setup.deps(), fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedFPPCommands: %v", err)
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
	if rec.OutcomeState != string(observation.StateCurrent) {
		t.Errorf("outcome_state = %q, want %q — NEVER blank (this is the whole point of this fix)", rec.OutcomeState, observation.StateCurrent)
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
		}
	}
	if outcomeCount != 1 {
		t.Errorf("outcome audit entries = %d, want exactly 1", outcomeCount)
	}
}

// TestReconcileStrandedFPPCommandsNeverLeavesBlankStateOrReason is this
// defect's own sharpest test: with NO evidence at all for the target, the
// pre-fix behavior was outcome_state="" and outcome_reason="" FOREVER —
// this proves that shape is now unreachable.
func TestReconcileStrandedFPPCommandsNeverLeavesBlankStateOrReason(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	dispatchedAt := testNow.Add(-time.Minute)
	strand := strandCommand(t, setup.st, "stranded-2", auditActionFPPStopPlaylist, "bench-fpp", &dispatchedAt)
	// No observations seeded at all.

	resolved, err := ReconcileStrandedFPPCommands(context.Background(), setup.deps(), fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedFPPCommands: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved = %d, want 1", resolved)
	}

	rec, err := setup.st.GetCommand(context.Background(), strand.ID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if rec.State != "resolved" {
		t.Errorf("state = %q, want \"resolved\" — NEVER stuck at \"dispatched\" forever", rec.State)
	}
	if rec.OutcomeState == "" {
		t.Error("outcome_state is empty — this is EXACTLY the pre-fix defect: permanent blankness indistinguishable " +
			"from the narrow accepted replay race")
	}
	if rec.OutcomeReason == "" {
		t.Error("outcome_reason is empty — same defect")
	}
}

// TestReconcileStrandedFPPCommandsIgnoresResolvedRows proves the sweep is
// scoped to resolved_at IS NULL, never touching (or double-auditing) a
// command that already finished normally.
func TestReconcileStrandedFPPCommandsIgnoresResolvedRows(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	rec, err := setup.st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "already-resolved", IdempotencyKey: "key-resolved", Action: auditActionFPPStopPlaylist,
		TargetKind: "fpp", TargetID: "bench-fpp",
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	resolvedState, outcomeState, outcomeReason := "resolved", string(observation.StateCurrent), ""
	if err := setup.st.UpdateCommandOutcome(context.Background(), rec.ID, store.CommandOutcomeUpdate{
		ResolvedAt: &testNow, State: &resolvedState, OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	}); err != nil {
		t.Fatalf("mark resolved: %v", err)
	}

	resolved, err := ReconcileStrandedFPPCommands(context.Background(), setup.deps(), fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedFPPCommands: %v", err)
	}
	if resolved != 0 {
		t.Errorf("resolved = %d, want 0 (an already-resolved command must not be touched)", resolved)
	}
}

// TestReconcileStrandedFPPCommandsSkipsUnknownActions proves the sweep
// does not guess at a stranded command whose action it does not know how
// to re-evaluate evidence for — it leaves such a row alone rather than
// fabricating a verdict, per this codebase's standing "never fabricate"
// rule.
func TestReconcileStrandedFPPCommandsSkipsUnknownActions(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	dispatchedAt := testNow.Add(-time.Minute)
	strandCommand(t, setup.st, "stranded-unknown", "some.other.action", "bench-fpp", &dispatchedAt)

	resolved, err := ReconcileStrandedFPPCommands(context.Background(), setup.deps(), fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedFPPCommands: %v", err)
	}
	if resolved != 0 {
		t.Errorf("resolved = %d, want 0 (an unrecognized action must be left alone, not guessed at)", resolved)
	}
}

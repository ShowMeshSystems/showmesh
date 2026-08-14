package api

import (
	"context"
	"strings"
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

// TestReconcileStrandedFPPCommandsNeverConfirmsAnUndispatchedCommand is
// Finding 2's own regression proof (Step 8 review), reproducing exactly
// the shape a process crash BETWEEN the AuditedWrite commit and
// primitive.Dispatch leaves behind: a commands row inserted with
// state="pending" and dispatched_at left NULL — nothing was ever sent to
// FPP. Evidence exists (fpp.status="playing", matching what
// fpp.start_playlist's own DesiredState would ask for) collected well
// after CreatedAt — standing in for FPP's OWN scheduler doing something
// unrelated in that window. The original code fell back to CreatedAt as
// a notBefore fence and evaluated the primitive's ordinary Confirm
// predicate against it, which could — and, proved live, did — report
// "confirmed" for a command that was never dispatched, attributing FPP's
// own activity to the operator's principal (ADR-001, ADR-003). This row
// must resolve unconfirmed unconditionally, regardless of what the
// evidence reads.
func TestReconcileStrandedFPPCommandsNeverConfirmsAnUndispatchedCommand(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	rec, err := setup.st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "never-dispatched", IdempotencyKey: "key-never-dispatched", Action: auditActionFPPStopPlaylist,
		TargetKind: "fpp", TargetID: "bench-fpp",
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
		// DispatchedAt and ResolvedAt both left NULL — exactly the row a
		// crash between the audited write and the dispatch call leaves.
	})
	if err != nil {
		t.Fatalf("insert command: %v", err)
	}
	if rec.DispatchedAt != nil {
		t.Fatalf("test setup: DispatchedAt = %v, want nil (this test requires the never-dispatched shape)", rec.DispatchedAt)
	}

	// Evidence that would satisfy stopPlaylist's own confirmation
	// predicate (fpp.status == idle), collected well after CreatedAt —
	// standing in for FPP's own unrelated activity, which must NOT be
	// credited to a command that was never sent.
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "idle", testNow.Add(2*time.Second), testNow.Add(2*time.Second)),
	})

	resolved, err := ReconcileStrandedFPPCommands(context.Background(), setup.deps(), fixedClock(testNow.Add(5*time.Second)), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedFPPCommands: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved = %d, want 1", resolved)
	}

	got, err := setup.st.GetCommand(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if got.State != "resolved" {
		t.Errorf("state = %q, want \"resolved\"", got.State)
	}
	if strings.Contains(string(got.ResultJSON), `"confirmed"`) {
		t.Fatalf("result = %q — a command whose dispatch was NEVER ATTEMPTED must never resolve confirmed, no matter "+
			"what evidence exists since CreatedAt (Finding 2: this is FPP's own activity, not this command's effect)", got.ResultJSON)
	}
	if got.OutcomeReason == "" || !strings.Contains(got.OutcomeReason, "never attempted") {
		t.Errorf("outcome_reason = %q, want it to say dispatch was never attempted", got.OutcomeReason)
	}

	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	for _, e := range entries {
		if e.CommandID == rec.ID && e.Outcome == "confirmed" {
			t.Errorf("audit entry for %s records outcome=confirmed for a command that was never dispatched", rec.ID)
		}
	}
}

// TestReconcileStrandedFPPCommandsResolvesUnknownActionsUnconfirmed is
// Finding 11's own regression proof (Step 8 review): a stranded command
// whose action this coordinator does not recognize used to `continue`
// with no log line and no state change, leaving the row "pending" with
// outcome_state and outcome_reason permanently blank — the exact
// "stranded, blank forever" shape this whole file exists to close,
// reintroduced for this one case. It does not GUESS at the command's
// actual evidence (there is no primitive to evaluate it with), but it
// also must not leave the row silently stuck: it resolves unconfirmed
// with a stated reason naming the unrecognized action, per ADR-020
// ("absent evidence is stated, never omitted"). This test previously
// asserted the opposite (resolved == 0, row left untouched); that
// assertion is Finding 11 itself and has been corrected here rather than
// left as a stale expectation of the pre-fix behavior.
func TestReconcileStrandedFPPCommandsResolvesUnknownActionsUnconfirmed(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	dispatchedAt := testNow.Add(-time.Minute)
	strand := strandCommand(t, setup.st, "stranded-unknown", "some.other.action", "bench-fpp", &dispatchedAt)

	resolved, err := ReconcileStrandedFPPCommands(context.Background(), setup.deps(), fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedFPPCommands: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved = %d, want 1 (an unrecognized action must be resolved unconfirmed, never left pending forever)", resolved)
	}

	rec, err := setup.st.GetCommand(context.Background(), strand.ID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if rec.State != "resolved" {
		t.Errorf("state = %q, want \"resolved\" — NEVER stuck at \"dispatched\" forever", rec.State)
	}
	if rec.OutcomeState == "" {
		t.Error("outcome_state is empty — this is exactly Finding 11: permanent blankness for an action this " +
			"coordinator does not recognize")
	}
	if rec.OutcomeReason == "" || !strings.Contains(rec.OutcomeReason, "some.other.action") {
		t.Errorf("outcome_reason = %q, want it to name the unrecognized action", rec.OutcomeReason)
	}
}

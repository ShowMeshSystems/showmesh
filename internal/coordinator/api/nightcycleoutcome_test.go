package api

import (
	"context"
	"log/slog"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F night-cycle-outcomes tests: the loop's own existing evidence and
// commands, instrumented to open and close night_cycle_outcomes rows,
// never changing which transition fires.

// TestNightAdvanceTransitionToShow_EnteringLiveOpensCycleOutcome proves the
// moment a show is committed and the session enters live, a cycle outcome
// row is opened for the session's current cycle.
func TestNightAdvanceTransitionToShow_EnteringLiveOpensCycleOutcome(t *testing.T) {
	now0 := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	h, st, rec, gotArgs, now := setupTransitionToShowTest(t, []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, now0),
		playlistNameObservation("player-01", "halloween-resting", now0),
	})

	h.nightAdvanceTransitionToShow(context.Background(), now, rec)

	if len(*gotArgs) < 1 {
		t.Fatalf("Start Playlist args = %v, want a dispatch (setup did not reach live)", *gotArgs)
	}
	got := mustGetCurrentSession(t, st)
	if got.State != nightStateLive {
		t.Fatalf("state = %q, want %q", got.State, nightStateLive)
	}

	rows, err := st.ListNightCycleOutcomes(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("ListNightCycleOutcomes: %v", err)
	}
	if len(rows) != 1 || rows[0].Cycle != got.Cycle || rows[0].EndedAt != nil {
		t.Fatalf("ListNightCycleOutcomes = %+v, want one open row for cycle %d", rows, got.Cycle)
	}
}

// TestNightAdvanceLive_CompletionClosesCycleOutcomeAsCompleted proves rule
// 4's own evidence-based commit closes the open cycle outcome as completed,
// with no reason (nothing to explain about a normal finish).
func TestNightAdvanceLive_CompletionClosesCycleOutcomeAsCompleted(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(20 * time.Second)
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now),
		playlistNameObservation("player-01", "", now),
	}}
	h, st := nightLoopTestHandlers(t, func() time.Time { return now }, obs)
	anchor := liveSessionAnchor("player-01", "halloween-show", "halloween-show.fseq", dispatchedAt)
	mustCreateLiveSession(t, st, dispatchedAt, anchor)
	if err := st.OpenNightCycleOutcome(context.Background(), "sess-1", 0, dispatchedAt); err != nil {
		t.Fatalf("seed open cycle outcome: %v", err)
	}

	h.nightAdvanceLive(context.Background(), now, mustGetCurrentSession(t, st))

	rows, err := st.ListNightCycleOutcomes(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("ListNightCycleOutcomes: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListNightCycleOutcomes = %+v, want exactly one row", rows)
	}
	rec := rows[0]
	if rec.EndedAt == nil || !rec.EndedAt.Equal(now) {
		t.Errorf("EndedAt = %v, want %v", rec.EndedAt, now)
	}
	if rec.Outcome != store.NightCycleOutcomeCompleted {
		t.Errorf("Outcome = %q, want %q", rec.Outcome, store.NightCycleOutcomeCompleted)
	}
	if rec.Reason != "" {
		t.Errorf("Reason = %q, want empty for a normal completion", rec.Reason)
	}
}

// TestNightAdvanceLive_DegradePastDeadlineClosesCycleOutcomeAsInterrupted
// proves that when live cannot confirm the show ended and degrades past
// [nightAdvanceLiveDeadline], the open cycle outcome closes as interrupted
// with an operator-readable reason, even though the session's own State
// never leaves live (nightDegradeSession never transitions state).
func TestNightAdvanceLive_DegradePastDeadlineClosesCycleOutcomeAsInterrupted(t *testing.T) {
	withNightAdvanceLiveDeadline(t, time.Minute)
	dispatchedAt := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(2 * time.Minute)
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", "playing", now),
		playlistNameObservation("player-01", "halloween-resting", now),
	}}
	h, st := nightLoopTestHandlers(t, func() time.Time { return now }, obs)
	anchor := liveSessionAnchor("player-01", "halloween-show", "halloween-show.fseq", dispatchedAt)
	mustCreateLiveSession(t, st, dispatchedAt, anchor)
	if err := st.OpenNightCycleOutcome(context.Background(), "sess-1", 0, dispatchedAt); err != nil {
		t.Fatalf("seed open cycle outcome: %v", err)
	}

	h.nightAdvanceLive(context.Background(), now, mustGetCurrentSession(t, st))

	got := mustGetCurrentSession(t, st)
	if !got.Degraded {
		t.Fatalf("session not degraded; test setup did not reach the deadline path")
	}
	if got.State != nightStateLive {
		t.Fatalf("state = %q, want still %q (degrade never transitions state)", got.State, nightStateLive)
	}

	rows, err := st.ListNightCycleOutcomes(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("ListNightCycleOutcomes: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListNightCycleOutcomes = %+v, want exactly one row", rows)
	}
	rec := rows[0]
	if rec.EndedAt == nil {
		t.Fatal("EndedAt is nil, want the cycle closed")
	}
	if rec.Outcome != store.NightCycleOutcomeInterrupted {
		t.Errorf("Outcome = %q, want %q", rec.Outcome, store.NightCycleOutcomeInterrupted)
	}
	if rec.Reason == "" {
		t.Error("Reason is empty, want an operator-readable explanation")
	}
}

// TestNightPowerDownPresentationApply_ForcedFromLiveDescribesCycleOutcomeAsStopped
// proves an emergency-stop-forced power-down that stops a live show
// describes its open cycle outcome as data for the runner to close, as
// stopped: the operator did this, not the player or the coordinator.
// nightPowerDownPresentationApply is a decide function
// (decidewriteboundary_test.go), so it must never write to tx itself; it
// hands the close back through nightCommandOutcome.closeCycleOutcome for
// nightRunExempt to apply, proven end to end by
// TestNightPowerDownPresentationCommand_ForcedFromLiveClosesCycleOutcomeAsStopped.
func TestNightPowerDownPresentationApply_ForcedFromLiveDescribesCycleOutcomeAsStopped(t *testing.T) {
	h, st := nightLoopTestHandlers(t, func() time.Time { return testNow }, &fakeObservationLister{})
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateLive, StateEnteredAt: testNow, Cycle: 1,
	}
	if err := st.CreateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("create night session: %v", err)
	}

	current := mustGetCurrentSession(t, st)
	var out nightCommandOutcome
	err := st.InTx(context.Background(), func(ctx context.Context, tx *store.Tx) error {
		var derr error
		out, _, derr = h.nightPowerDownPresentationApply(ctx, tx, &current, testNow, nil, nightShutdownForced)
		return derr
	})
	if err != nil {
		t.Fatalf("nightPowerDownPresentationApply: %v", err)
	}

	if out.closeCycleOutcome == nil {
		t.Fatal("closeCycleOutcome = nil, want a close for the interrupted-from-live cycle")
	}
	if out.closeCycleOutcome.sessionID != "sess-1" || out.closeCycleOutcome.cycle != 1 ||
		out.closeCycleOutcome.outcome != store.NightCycleOutcomeStopped || out.closeCycleOutcome.reason == "" {
		t.Errorf("closeCycleOutcome = %+v, want session sess-1 cycle 1 stopped with a reason", out.closeCycleOutcome)
	}
}

// TestNightPowerDownPresentationCommand_ForcedFromLiveClosesCycleOutcomeAsStopped
// proves the full command path (decide plus nightRunExempt's own apply of
// closeCycleOutcome) actually closes the stored row, not just describes it.
func TestNightPowerDownPresentationCommand_ForcedFromLiveClosesCycleOutcomeAsStopped(t *testing.T) {
	h, st := nightLoopTestHandlers(t, func() time.Time { return testNow }, &fakeObservationLister{})
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateLive, StateEnteredAt: testNow, Cycle: 1,
	}
	if err := st.CreateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	if err := st.OpenNightCycleOutcome(context.Background(), "sess-1", 1, testNow); err != nil {
		t.Fatalf("seed open cycle outcome: %v", err)
	}

	var attributionDegraded bool
	_, _, err := h.nightRunExempt(context.Background(), testNow, nightCommandPowerDownPresentation, identity.AuditEntry{}, &attributionDegraded,
		func(ctx context.Context, tx *store.Tx, cur *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightPowerDownPresentationApply(ctx, tx, cur, testNow, nil, nightShutdownForced)
		})
	if err != nil {
		t.Fatalf("nightRunExempt: %v", err)
	}

	rows, err := st.ListNightCycleOutcomes(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("ListNightCycleOutcomes: %v", err)
	}
	if len(rows) != 1 || rows[0].EndedAt == nil || rows[0].Outcome != store.NightCycleOutcomeStopped {
		t.Fatalf("ListNightCycleOutcomes = %+v, want one row closed as stopped", rows)
	}
}

// TestNightEndSessionDecide_FromLiveDescribesCycleOutcomeAsStopped proves
// the operator's end-session recovery decide function describes an open
// cycle to close as stopped, as data, when it abandons a session still in
// live. nightEndSessionDecide takes no *store.Tx precisely because it must
// never write to one (decidewriteboundary_test.go);
// TestNightEndSessionCommand_FromLiveClosesCycleOutcomeAsStopped proves the
// close actually lands through nightRunExempt.
func TestNightEndSessionDecide_FromLiveDescribesCycleOutcomeAsStopped(t *testing.T) {
	current := &store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateLive, StateEnteredAt: testNow, Cycle: 2,
	}

	out := (&handlers{}).nightEndSessionDecide(testNow, current)

	if out.closeCycleOutcome == nil {
		t.Fatal("closeCycleOutcome = nil, want a close for the abandoned live cycle")
	}
	if out.closeCycleOutcome.sessionID != "sess-1" || out.closeCycleOutcome.cycle != 2 ||
		out.closeCycleOutcome.outcome != store.NightCycleOutcomeStopped || out.closeCycleOutcome.reason == "" {
		t.Errorf("closeCycleOutcome = %+v, want session sess-1 cycle 2 stopped with a reason", out.closeCycleOutcome)
	}
}

// TestNightEndSessionCommand_FromLiveClosesCycleOutcomeAsStopped proves the
// full command path (decide plus nightRunExempt's own apply of
// closeCycleOutcome) actually closes the stored row.
func TestNightEndSessionCommand_FromLiveClosesCycleOutcomeAsStopped(t *testing.T) {
	h, st := nightLoopTestHandlers(t, func() time.Time { return testNow }, &fakeObservationLister{})
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateLive, StateEnteredAt: testNow, Cycle: 2,
	}
	if err := st.CreateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	if err := st.OpenNightCycleOutcome(context.Background(), "sess-1", 2, testNow); err != nil {
		t.Fatalf("seed open cycle outcome: %v", err)
	}

	var attributionDegraded bool
	_, _, err := h.nightRunExempt(context.Background(), testNow, nightCommandEndSession, identity.AuditEntry{}, &attributionDegraded,
		func(ctx context.Context, tx *store.Tx, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightEndSessionDecide(testNow, current), nil, nil
		})
	if err != nil {
		t.Fatalf("nightRunExempt: %v", err)
	}

	rows, err := st.ListNightCycleOutcomes(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("ListNightCycleOutcomes: %v", err)
	}
	if len(rows) != 1 || rows[0].EndedAt == nil || rows[0].Outcome != store.NightCycleOutcomeStopped {
		t.Fatalf("ListNightCycleOutcomes = %+v, want one row closed as stopped", rows)
	}
}

// TestReconcileOpenNightCycleOutcomesOnStartup_ClosesAsInterrupted proves a
// cycle outcome row left open by a prior process closes as interrupted the
// next time this coordinator starts, before it serves any request.
func TestReconcileOpenNightCycleOutcomesOnStartup_ClosesAsInterrupted(t *testing.T) {
	_, st := nightLoopTestHandlers(t, func() time.Time { return testNow }, &fakeObservationLister{})
	if err := st.OpenNightCycleOutcome(context.Background(), "sess-stale", 1, testNow.Add(-time.Hour)); err != nil {
		t.Fatalf("seed open cycle outcome: %v", err)
	}

	deps := Dependencies{NightSessions: st}.withDefaults()
	if err := reconcileOpenNightCycleOutcomesOnStartup(context.Background(), deps, testNow, slog.Default()); err != nil {
		t.Fatalf("reconcileOpenNightCycleOutcomesOnStartup: %v", err)
	}

	rows, err := st.ListNightCycleOutcomes(context.Background(), "sess-stale")
	if err != nil {
		t.Fatalf("ListNightCycleOutcomes: %v", err)
	}
	if len(rows) != 1 || rows[0].EndedAt == nil || rows[0].Outcome != store.NightCycleOutcomeInterrupted {
		t.Fatalf("ListNightCycleOutcomes = %+v, want one row closed as interrupted", rows)
	}

	open, err := st.ListOpenNightCycleOutcomes(context.Background())
	if err != nil {
		t.Fatalf("ListOpenNightCycleOutcomes: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("ListOpenNightCycleOutcomes = %+v, want none open after reconciliation", open)
	}
}

// TestMapNightFinishedCycles_ReportsClosedRowsOnlyOldestFirst proves the
// API response lists every closed cycle for the session, oldest first, and
// leaves out the still-open current cycle: it has not ended yet, so it
// stays represented only by NightSessionState.cycle.
func TestMapNightFinishedCycles_ReportsClosedRowsOnlyOldestFirst(t *testing.T) {
	h, st := nightLoopTestHandlers(t, func() time.Time { return testNow }, &fakeObservationLister{})
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateLive, StateEnteredAt: testNow, Cycle: 2,
	}
	if err := st.CreateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	if err := st.OpenNightCycleOutcome(context.Background(), "sess-1", 0, testNow.Add(-2*time.Hour)); err != nil {
		t.Fatalf("seed cycle 0: %v", err)
	}
	if err := st.CloseNightCycleOutcome(context.Background(), "sess-1", 0, testNow.Add(-time.Hour), store.NightCycleOutcomeCompleted, ""); err != nil {
		t.Fatalf("close cycle 0: %v", err)
	}
	if err := st.OpenNightCycleOutcome(context.Background(), "sess-1", 1, testNow.Add(-time.Hour)); err != nil {
		t.Fatalf("seed cycle 1: %v", err)
	}
	if err := st.CloseNightCycleOutcome(context.Background(), "sess-1", 1, testNow.Add(-30*time.Minute), store.NightCycleOutcomeStopped, "The operator stopped the show before it finished."); err != nil {
		t.Fatalf("close cycle 1: %v", err)
	}
	if err := st.OpenNightCycleOutcome(context.Background(), "sess-1", 2, testNow); err != nil {
		t.Fatalf("seed still-open cycle 2: %v", err)
	}

	got := mapNightFinishedCycles(context.Background(), h.deps, rec)

	if len(got) != 2 {
		t.Fatalf("mapNightFinishedCycles = %+v, want 2 entries (cycle 2 is still open)", got)
	}
	if got[0].Cycle != 0 || got[0].Outcome != store.NightCycleOutcomeCompleted || got[0].Reason != "" {
		t.Errorf("entry 0 = %+v, want cycle 0 completed with no reason", got[0])
	}
	if got[1].Cycle != 1 || got[1].Outcome != store.NightCycleOutcomeStopped || got[1].Reason == "" {
		t.Errorf("entry 1 = %+v, want cycle 1 stopped with a reason", got[1])
	}
}

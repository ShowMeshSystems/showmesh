package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// testMacroRun builds a minimal, valid two-step MacroRunRecord/
// []MacroRunStepRecord pair for id/idempotencyKey/macroObjectID, so each
// test below only has to state what actually varies for its own scenario.
// SafetyClass/LocalFallbackClass are both required (createMacroRun rejects
// an empty one: STEP-9-SPEC.md §5.3/§5.4's write-time requirement, applied
// here to a run's pinned copy too), so this helper always supplies
// realistic values from each field's own closed vocabulary rather than a
// placeholder string, matching STEP-9-SPEC.md §2.5/§5.4's actual day-0
// shape: a coordinator-required FPP step with no safety class of its own.
// OutcomeState/OutcomeReason are set to [MacroRunStepOutcomeStatePending]/
// [MacroRunStepOutcomeReasonPending] — createMacroRun requires both
// non-empty since STEP-9-SPEC review finding 8, exactly the two fields a
// step must never round-trip blank while unresolved.
func testMacroRun(id, idempotencyKey, macroObjectID string) (MacroRunRecord, []MacroRunStepRecord) {
	run := MacroRunRecord{
		ID: id, MacroObjectID: macroObjectID, MacroRevision: 1, Show: "halloween-2026",
		Trigger: "api", IssuerPrincipalID: "p-1", IssuerPrincipalName: "operator",
		IdempotencyKey: idempotencyKey, State: "running",
	}
	steps := []MacroRunStepRecord{
		{StepIndex: 0, StepID: "projectors", ActionObjectID: "projectors-on", ActionRevision: 1, Integration: "fpp",
			SafetyClass: "none", LocalFallbackClass: "coordinator-required", State: "pending",
			OutcomeState: MacroRunStepOutcomeStatePending, OutcomeReason: MacroRunStepOutcomeReasonPending},
		{StepIndex: 1, StepID: "start", ActionObjectID: "start-main-show", ActionRevision: 1, Integration: "fpp",
			SafetyClass: "none", LocalFallbackClass: "coordinator-required", State: "pending",
			OutcomeState: MacroRunStepOutcomeStatePending, OutcomeReason: MacroRunStepOutcomeReasonPending},
	}
	return run, steps
}

// TestCreateMacroRunDuplicateIdempotencyKeyReturnsDuplicateError proves the
// storage half of ADR-031's "idempotency keys are still required and still
// work": a second CreateMacroRun with an already-used idempotency_key never
// creates a second run (or any of its steps) and returns a
// *DuplicateMacroRunError carrying the pre-existing run, never a bare
// generic error a caller could not distinguish from any other failure —
// mirroring TestInsertCommandDuplicateIdempotencyKeyReturnsDuplicateError
// (commands_test.go) for macro runs.
func TestCreateMacroRunDuplicateIdempotencyKeyReturnsDuplicateError(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-1", "idem-1", "macro-a")
	first, _, err := st.CreateMacroRun(ctx, run, steps)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	dupRun, dupSteps := testMacroRun("run-2", "idem-1", "macro-a")
	_, _, err = st.CreateMacroRun(ctx, dupRun, dupSteps)
	if err == nil {
		t.Fatalf("second create with the same idempotency key succeeded, want a duplicate error")
	}
	if !errors.Is(err, ErrMacroRunIdempotencyKeyExists) {
		t.Fatalf("error = %v, want it to wrap ErrMacroRunIdempotencyKeyExists", err)
	}
	var dup *DuplicateMacroRunError
	if !errors.As(err, &dup) {
		t.Fatalf("error = %v, want errors.As to find a *DuplicateMacroRunError", err)
	}
	if dup.Existing.ID != first.ID {
		t.Errorf("DuplicateMacroRunError.Existing.ID = %q, want %q (the original run)", dup.Existing.ID, first.ID)
	}

	all, err := st.ListMacroRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list macro runs: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(all) = %d, want exactly 1 run after a duplicate create attempt", len(all))
	}

	// The rejected create must not have left orphaned steps for run-2 behind
	// either — confirmed directly against the table, not just against
	// ListMacroRuns, since a partial-step-insert bug would not show up in a
	// run-row count at all.
	var stepCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM macro_run_steps WHERE run_id = 'run-2'`).Scan(&stepCount); err != nil {
		t.Fatalf("count steps for run-2: %v", err)
	}
	if stepCount != 0 {
		t.Errorf("macro_run_steps has %d rows for the rejected run-2, want 0", stepCount)
	}
}

// TestCreateMacroRunReusedIDReturnsDuplicateIDErrorNotIdempotencyError is
// STEP-9-SPEC review Minor finding 1: a reused macro_runs.id (the table's
// PRIMARY KEY) with a DIFFERENT, otherwise-unused idempotency_key must be
// reported as *DuplicateMacroRunIDError, never routed through the
// idempotency-key machinery. Before this fix, isUniqueConstraintErr's
// fallback branch treated ANY "UNIQUE constraint failed" on this INSERT as
// an idempotency-key collision and unconditionally re-read the row by
// idempotency_key — for this scenario, no row carries the SECOND
// submission's idempotency key, so that re-read would have failed and
// surfaced the misleading "idempotency key %q already exists but could not
// be re-read: ... macro run not found", naming a key that was never
// actually duplicated.
func TestCreateMacroRunReusedIDReturnsDuplicateIDErrorNotIdempotencyError(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first, firstSteps := testMacroRun("run-reused-id", "idem-reused-id-1", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, first, firstSteps); err != nil {
		t.Fatalf("create first run: %v", err)
	}
	// Finish the first run so ADR-031 decision 6's overlap guard (a
	// DIFFERENT check, on macro_object_id + state=='running') does not fire
	// first and mask the id collision this test is actually proving.
	if err := st.FinishMacroRun(ctx, "run-reused-id", MacroRunFinishUpdate{
		FinishedAt: st.now(), Completed: true, Confirmed: true,
	}); err != nil {
		t.Fatalf("finish first run: %v", err)
	}

	// Same id, but a DIFFERENT idempotency key that has never been used —
	// the only thing colliding is the PRIMARY KEY.
	second, secondSteps := testMacroRun("run-reused-id", "idem-reused-id-2", "macro-a")
	_, _, err := st.CreateMacroRun(ctx, second, secondSteps)
	if err == nil {
		t.Fatalf("create with a reused id succeeded, want an error")
	}
	if !errors.Is(err, ErrMacroRunIDExists) {
		t.Fatalf("error = %v, want it to wrap ErrMacroRunIDExists", err)
	}
	if errors.Is(err, ErrMacroRunIdempotencyKeyExists) {
		t.Fatalf("error = %v, must NOT also read as an idempotency-key collision: the idempotency key in this scenario was never duplicated", err)
	}
	var dupID *DuplicateMacroRunIDError
	if !errors.As(err, &dupID) {
		t.Fatalf("error = %v, want errors.As to find a *DuplicateMacroRunIDError", err)
	}
	if dupID.ID != "run-reused-id" {
		t.Errorf("DuplicateMacroRunIDError.ID = %q, want %q", dupID.ID, "run-reused-id")
	}

	// The rejected second submission's own idempotency key must not have
	// been consumed by anything — a caller retrying with a FRESH id and the
	// same key should still be free to use it. Checked directly: no run
	// exists under "idem-reused-id-2" at all.
	if _, err := st.GetMacroRunByIdempotencyKey(ctx, "idem-reused-id-2"); !errors.Is(err, ErrMacroRunNotFound) {
		t.Errorf("GetMacroRunByIdempotencyKey(idem-reused-id-2) error = %v, want ErrMacroRunNotFound (the rejected insert must not have left this key claimed)", err)
	}
}

// TestCreateMacroRunDoesNotMutateCallerStepsSlice is STEP-9-SPEC review
// Minor finding 2: CreateMacroRun used to write run.ID and clear
// DispatchedAt/ResolvedAt directly onto the caller's own steps slice — Go
// slices share their backing array across a function call — so a caller
// that kept its input slice around after a successful create observed this
// package's own bookkeeping layered onto data it still thought it owned.
// Proved by capturing the input slice's RunID BEFORE the call (still "",
// since the caller never set it) and confirming it is STILL "" after,
// while the RETURNED slice does carry the run id.
func TestCreateMacroRunDoesNotMutateCallerStepsSlice(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-no-mutate", "idem-no-mutate", "macro-a")
	for _, in := range steps {
		if in.RunID != "" {
			t.Fatalf("test setup invariant broken: input step RunID = %q, want empty before the call", in.RunID)
		}
	}

	_, recSteps, err := st.CreateMacroRun(ctx, run, steps)
	if err != nil {
		t.Fatalf("create macro run: %v", err)
	}

	for i, s := range steps {
		if s.RunID != "" {
			t.Errorf("input steps[%d].RunID = %q after CreateMacroRun, want it left untouched at \"\" (the caller's own slice must not be mutated)", i, s.RunID)
		}
	}
	for i, s := range recSteps {
		if s.RunID != "run-no-mutate" {
			t.Errorf("returned recSteps[%d].RunID = %q, want %q", i, s.RunID, "run-no-mutate")
		}
	}
}

// TestGetMacroRunStepsComeBackInIndexOrder proves GetMacroRun returns a
// run's steps in step_index order regardless of insertion order — an
// operator or the executor reading a run must see step 0 before step 1
// before step 2, since STEP-9-SPEC.md §5.4 requires step id to be "stable"
// and this ordering is what makes "which step stopped it" (ADR-031
// decision 2) legible at all.
func TestGetMacroRunStepsComeBackInIndexOrder(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run := MacroRunRecord{
		ID: "run-order", MacroObjectID: "macro-a", MacroRevision: 1, Show: "halloween-2026",
		Trigger: "api", IdempotencyKey: "idem-order", State: "running",
	}
	// Deliberately built out of index order, so a query with no ORDER BY
	// (or one relying on insertion/rowid order) would fail this test.
	steps := []MacroRunStepRecord{
		{StepIndex: 2, StepID: "third", ActionObjectID: "a3", ActionRevision: 1, Integration: "fpp",
			SafetyClass: "none", LocalFallbackClass: "coordinator-required", State: "pending",
			OutcomeState: MacroRunStepOutcomeStatePending, OutcomeReason: MacroRunStepOutcomeReasonPending},
		{StepIndex: 0, StepID: "first", ActionObjectID: "a1", ActionRevision: 1, Integration: "fpp",
			SafetyClass: "stop", LocalFallbackClass: "none", State: "pending",
			OutcomeState: MacroRunStepOutcomeStatePending, OutcomeReason: MacroRunStepOutcomeReasonPending},
		{StepIndex: 1, StepID: "second", ActionObjectID: "a2", ActionRevision: 1, Integration: "mqtt",
			SafetyClass: "none", LocalFallbackClass: "coordinator-required", State: "pending",
			OutcomeState: MacroRunStepOutcomeStatePending, OutcomeReason: MacroRunStepOutcomeReasonPending},
	}
	if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
		t.Fatalf("create macro run: %v", err)
	}

	_, gotSteps, err := st.GetMacroRun(ctx, "run-order")
	if err != nil {
		t.Fatalf("get macro run: %v", err)
	}
	if len(gotSteps) != 3 {
		t.Fatalf("len(gotSteps) = %d, want 3", len(gotSteps))
	}
	wantOrder := []string{"first", "second", "third"}
	for i, want := range wantOrder {
		if gotSteps[i].StepID != want {
			t.Errorf("gotSteps[%d].StepID = %q, want %q (index order, not insertion order)", i, gotSteps[i].StepID, want)
		}
		if gotSteps[i].StepIndex != i {
			t.Errorf("gotSteps[%d].StepIndex = %d, want %d", i, gotSteps[i].StepIndex, i)
		}
	}
}

// TestMacroRunCompletedConfirmedUnknownUntilFinishedThenSurviveRoundTrip is
// this task's core acceptance requirement: completed/confirmed must round
// trip as genuinely unknown (nil) while a run is in flight, never as a
// false standing in for "not decided yet", and then must round trip as the
// exact booleans FinishMacroRun set, including the "completed but not
// confirmed" combination ADR-031 decision 3 calls out by name (a
// structurally unconfirmable step: "completed: true, confirmed: false...
// every single time it runs correctly").
func TestMacroRunCompletedConfirmedUnknownUntilFinishedThenSurviveRoundTrip(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-unknown", "idem-unknown", "macro-a")
	created, _, err := st.CreateMacroRun(ctx, run, steps)
	if err != nil {
		t.Fatalf("create macro run: %v", err)
	}
	if created.Completed != nil {
		t.Errorf("created.Completed = %v, want nil (unknown) on a freshly created running run", *created.Completed)
	}
	if created.Confirmed != nil {
		t.Errorf("created.Confirmed = %v, want nil (unknown) on a freshly created running run", *created.Confirmed)
	}

	gotRun, _, err := st.GetMacroRun(ctx, "run-unknown")
	if err != nil {
		t.Fatalf("get macro run before finish: %v", err)
	}
	if gotRun.Completed != nil {
		t.Fatalf("gotRun.Completed = %v, want nil (unknown) before FinishMacroRun — a NOT NULL DEFAULT would manufacture a false 'not completed' claim", *gotRun.Completed)
	}
	if gotRun.Confirmed != nil {
		t.Fatalf("gotRun.Confirmed = %v, want nil (unknown) before FinishMacroRun", *gotRun.Confirmed)
	}

	finishedAt := st.now()
	if err := st.FinishMacroRun(ctx, "run-unknown", MacroRunFinishUpdate{
		FinishedAt: finishedAt, Completed: true, Confirmed: false,
		Reason: "the mqtt step declared no expected response",
	}); err != nil {
		t.Fatalf("finish macro run: %v", err)
	}

	gotRun, _, err = st.GetMacroRun(ctx, "run-unknown")
	if err != nil {
		t.Fatalf("get macro run after finish: %v", err)
	}
	if gotRun.State != "finished" {
		t.Errorf("State = %q, want finished", gotRun.State)
	}
	if gotRun.Completed == nil || *gotRun.Completed != true {
		t.Fatalf("Completed = %v, want a known true", gotRun.Completed)
	}
	if gotRun.Confirmed == nil || *gotRun.Confirmed != false {
		t.Fatalf("Confirmed = %v, want a known false (completed-but-not-confirmed must survive, not collapse to nil or to Completed's value)", gotRun.Confirmed)
	}
	if gotRun.FinishedAt == nil || !gotRun.FinishedAt.Equal(finishedAt) {
		t.Errorf("FinishedAt = %v, want %v", gotRun.FinishedAt, finishedAt)
	}
	if gotRun.Reason == "" {
		t.Errorf("Reason is empty, want the finish reason preserved")
	}
}

// TestFinishMacroRunCompletedFalseConfirmedTrueDoesNotCollapse is
// TestMacroRunCompletedConfirmedUnknownUntilFinishedThenSurviveRoundTrip's
// sibling for the opposite combination — a run that aborted partway
// through but whose already-dispatched steps DID all confirm before the
// abort — proving the two columns are stored and read completely
// independently rather than one being derived from the other in either
// direction.
func TestFinishMacroRunCompletedFalseConfirmedTrueDoesNotCollapse(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-abort", "idem-abort", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
		t.Fatalf("create macro run: %v", err)
	}

	if err := st.FinishMacroRun(ctx, "run-abort", MacroRunFinishUpdate{
		FinishedAt: st.now(), Completed: false, Confirmed: true,
		Reason: "step 'start' failed",
	}); err != nil {
		t.Fatalf("finish macro run: %v", err)
	}

	gotRun, _, err := st.GetMacroRun(ctx, "run-abort")
	if err != nil {
		t.Fatalf("get macro run: %v", err)
	}
	if gotRun.Completed == nil || *gotRun.Completed != false {
		t.Fatalf("Completed = %v, want a known false", gotRun.Completed)
	}
	if gotRun.Confirmed == nil || *gotRun.Confirmed != true {
		t.Fatalf("Confirmed = %v, want a known true (must not be forced false just because Completed is false)", gotRun.Confirmed)
	}
}

// TestFinishMacroRunNotFound proves FinishMacroRun reports a distinguishable
// error rather than silently succeeding against a nonexistent run.
func TestFinishMacroRunNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	err := st.FinishMacroRun(context.Background(), "does-not-exist", MacroRunFinishUpdate{FinishedAt: st.now()})
	if !errors.Is(err, ErrMacroRunNotFound) {
		t.Errorf("error = %v, want ErrMacroRunNotFound", err)
	}
}

// TestListRunningMacroRunsExcludesFinished proves ADR-031 decision 4's
// startup-reconciler primitive returns only runs still state=="running":
// a finished run — regardless of how it finished — must never appear, or
// a reconciler using this query would re-"finish" an already-finished run
// and could stamp a false interrupted-by-restart reason over a genuine
// outcome.
func TestListRunningMacroRunsExcludesFinished(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	runA, stepsA := testMacroRun("run-running", "idem-running", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, runA, stepsA); err != nil {
		t.Fatalf("create running run: %v", err)
	}

	runB, stepsB := testMacroRun("run-finished", "idem-finished", "macro-b")
	if _, _, err := st.CreateMacroRun(ctx, runB, stepsB); err != nil {
		t.Fatalf("create soon-to-finish run: %v", err)
	}
	if err := st.FinishMacroRun(ctx, "run-finished", MacroRunFinishUpdate{
		FinishedAt: st.now(), Completed: true, Confirmed: true,
	}); err != nil {
		t.Fatalf("finish run-finished: %v", err)
	}

	running, err := st.ListRunningMacroRuns(ctx)
	if err != nil {
		t.Fatalf("list running macro runs: %v", err)
	}
	if len(running) != 1 {
		t.Fatalf("len(running) = %d, want exactly 1", len(running))
	}
	if running[0].ID != "run-running" {
		t.Errorf("running[0].ID = %q, want %q", running[0].ID, "run-running")
	}
	for _, r := range running {
		if r.ID == "run-finished" {
			t.Fatalf("ListRunningMacroRuns returned the finished run %q", r.ID)
		}
	}
}

// TestCreateMacroRunRefusesOverlappingRunOfSameMacro is ADR-031 decision
// 6's storage-level proof: a second, DIFFERENT idempotency key for a macro
// that already has a run in state=="running" is refused with a
// *MacroRunAlreadyInFlightError naming that run, and no second row (or its
// steps) is created.
func TestCreateMacroRunRefusesOverlappingRunOfSameMacro(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first, firstSteps := testMacroRun("run-first", "idem-first", "macro-a")
	created, _, err := st.CreateMacroRun(ctx, first, firstSteps)
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}

	second, secondSteps := testMacroRun("run-second", "idem-second", "macro-a")
	_, _, err = st.CreateMacroRun(ctx, second, secondSteps)
	if err == nil {
		t.Fatalf("second submission of an already-running macro succeeded, want a refusal")
	}
	if !errors.Is(err, ErrMacroRunAlreadyInFlight) {
		t.Fatalf("error = %v, want it to wrap ErrMacroRunAlreadyInFlight", err)
	}
	var inFlight *MacroRunAlreadyInFlightError
	if !errors.As(err, &inFlight) {
		t.Fatalf("error = %v, want errors.As to find a *MacroRunAlreadyInFlightError", err)
	}
	if inFlight.InFlight.ID != created.ID {
		t.Errorf("InFlight.ID = %q, want %q (the run actually in flight)", inFlight.InFlight.ID, created.ID)
	}

	all, err := st.ListMacroRuns(ctx, "macro-a", 0)
	if err != nil {
		t.Fatalf("list macro runs: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(all) = %d, want exactly 1 (the refused submission must not have been created)", len(all))
	}
}

// TestCreateMacroRunOverlapGuardDoesNotBlockDifferentMacros proves the
// overlap guard is scoped per macro_object_id, not a global "one run at a
// time" rule: two DIFFERENT macros must both be able to run concurrently.
func TestCreateMacroRunOverlapGuardDoesNotBlockDifferentMacros(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	runA, stepsA := testMacroRun("run-a", "idem-a", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, runA, stepsA); err != nil {
		t.Fatalf("create macro-a run: %v", err)
	}

	runB, stepsB := testMacroRun("run-b", "idem-b", "macro-b")
	if _, _, err := st.CreateMacroRun(ctx, runB, stepsB); err != nil {
		t.Fatalf("create macro-b run: %v, want it to succeed (different macro)", err)
	}
}

// TestCreateMacroRunSameIdempotencyKeyReturnsExistingRunEvenWhileRunning
// proves the ordering STEP-9-SPEC.md §4 requires generalized to this
// store's own overlap guard: a legitimate retry of an already-accepted
// submission (the SAME idempotency_key, for the SAME macro, while that
// exact run is still state=="running") must return the existing run via
// *DuplicateMacroRunError, never be refused as "already in flight" against
// itself — see migrations.go's schemaV7 doc comment and
// MacroRunAlreadyInFlightError's doc comment for why idempotency lookup
// runs first.
func TestCreateMacroRunSameIdempotencyKeyReturnsExistingRunEvenWhileRunning(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-retry", "idem-retry", "macro-a")
	first, _, err := st.CreateMacroRun(ctx, run, steps)
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}

	// Simulate a client retry: same ID, same idempotency key, same macro —
	// the run is still "running" (never finished in this test).
	retryRun, retrySteps := testMacroRun("run-retry", "idem-retry", "macro-a")
	_, _, err = st.CreateMacroRun(ctx, retryRun, retrySteps)
	if err == nil {
		t.Fatalf("retry succeeded as a fresh create, want a duplicate error")
	}
	var dup *DuplicateMacroRunError
	if !errors.As(err, &dup) {
		t.Fatalf("error = %v, want *DuplicateMacroRunError (never *MacroRunAlreadyInFlightError) for a same-key retry against a still-running run", err)
	}
	if dup.Existing.ID != first.ID {
		t.Errorf("Existing.ID = %q, want %q", dup.Existing.ID, first.ID)
	}
}

// TestFindRunningMacroRunNotFound proves FindRunningMacroRun reports
// ErrMacroRunNotFound, not a zero-value success, when no run of the named
// macro is running.
func TestFindRunningMacroRunNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.FindRunningMacroRun(context.Background(), "macro-nonexistent")
	if !errors.Is(err, ErrMacroRunNotFound) {
		t.Errorf("error = %v, want ErrMacroRunNotFound", err)
	}
}

// TestGetMacroRunByIdempotencyKeyNotFound mirrors
// TestGetCommandByIdempotencyKey-shaped not-found coverage in commands.go's
// suite (see GetCommandByIdempotencyKey's own contract), applied here.
func TestGetMacroRunByIdempotencyKeyNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.GetMacroRunByIdempotencyKey(context.Background(), "no-such-key")
	if !errors.Is(err, ErrMacroRunNotFound) {
		t.Errorf("error = %v, want ErrMacroRunNotFound", err)
	}
}

// TestUpdateMacroRunStepOutcomeOnlyTouchesGivenFields mirrors
// TestUpdateCommandOutcomeOnlyTouchesGivenFields (commands_test.go)
// exactly, applied to a macro run step: setting only State/DispatchedAt
// must leave Outcome/OutcomeState/OutcomeReason/ResolvedAt/CommandID
// exactly as they were.
func TestUpdateMacroRunStepOutcomeOnlyTouchesGivenFields(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-step-update", "idem-step-update", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
		t.Fatalf("create macro run: %v", err)
	}

	dispatchedAt := st.now()
	dispatchedState := "dispatched"
	if err := st.UpdateMacroRunStepOutcome(ctx, "run-step-update", 0, MacroRunStepOutcomeUpdate{
		State: &dispatchedState, DispatchedAt: &dispatchedAt,
	}); err != nil {
		t.Fatalf("update step outcome (dispatch): %v", err)
	}

	_, gotSteps, err := st.GetMacroRun(ctx, "run-step-update")
	if err != nil {
		t.Fatalf("get macro run: %v", err)
	}
	step0 := gotSteps[0]
	if step0.State != "dispatched" {
		t.Errorf("State = %q, want dispatched", step0.State)
	}
	if step0.DispatchedAt == nil || !step0.DispatchedAt.Equal(dispatchedAt) {
		t.Fatalf("DispatchedAt = %v, want %v", step0.DispatchedAt, dispatchedAt)
	}
	if step0.ResolvedAt != nil {
		t.Errorf("ResolvedAt = %v, want nil (not part of this update)", step0.ResolvedAt)
	}
	if step0.Outcome != "" {
		t.Errorf("Outcome = %q, want unchanged empty string", step0.Outcome)
	}
	if step0.CommandID != nil {
		t.Errorf("CommandID = %v, want unchanged nil", step0.CommandID)
	}

	// Step 1 must be entirely untouched by an update scoped to step 0.
	step1 := gotSteps[1]
	if step1.State != "pending" {
		t.Errorf("step 1 State = %q, want unchanged pending", step1.State)
	}

	// Now resolve step 0, including its command_id — proves CommandID's
	// nil-means-untouched / non-nil-means-set contract (including that ""
	// is a settable, real value distinct from "leave alone").
	resolvedAt := st.now()
	confirmedOutcome := "confirmed"
	confirmedState := "healthy"
	cmdID := "cmd-123"
	if err := st.UpdateMacroRunStepOutcome(ctx, "run-step-update", 0, MacroRunStepOutcomeUpdate{
		ResolvedAt: &resolvedAt, Outcome: &confirmedOutcome, OutcomeState: &confirmedState, CommandID: &cmdID,
	}); err != nil {
		t.Fatalf("update step outcome (resolve): %v", err)
	}
	_, gotSteps, err = st.GetMacroRun(ctx, "run-step-update")
	if err != nil {
		t.Fatalf("get macro run after resolve: %v", err)
	}
	step0 = gotSteps[0]
	if step0.State != "dispatched" {
		t.Errorf("State = %q, want it left as dispatched by the resolve-only update", step0.State)
	}
	if step0.Outcome != "confirmed" {
		t.Errorf("Outcome = %q, want confirmed", step0.Outcome)
	}
	if step0.CommandID == nil || *step0.CommandID != "cmd-123" {
		t.Fatalf("CommandID = %v, want a pointer to \"cmd-123\"", step0.CommandID)
	}
}

// TestUpdateMacroRunStepOutcomeNotFound proves a nonexistent (run, step)
// pair reports ErrMacroRunStepNotFound rather than silently succeeding.
func TestUpdateMacroRunStepOutcomeNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	dispatchedState := "dispatched"
	err := st.UpdateMacroRunStepOutcome(context.Background(), "does-not-exist", 0, MacroRunStepOutcomeUpdate{State: &dispatchedState})
	if !errors.Is(err, ErrMacroRunStepNotFound) {
		t.Errorf("error = %v, want ErrMacroRunStepNotFound", err)
	}
}

// TestSetMacroRunAttributionDegradedIsIdempotentAndSurvivesReadWhileRunning
// proves ADR-031 decision 5's live-surfacing requirement: attribution
// degradation is readable through GetMacroRun/ListMacroRuns before the run
// finishes, not only after, and setting it twice is a harmless no-op
// rather than an error.
func TestSetMacroRunAttributionDegradedIsIdempotentAndSurvivesReadWhileRunning(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-degraded", "idem-degraded", "macro-a")
	created, _, err := st.CreateMacroRun(ctx, run, steps)
	if err != nil {
		t.Fatalf("create macro run: %v", err)
	}
	if created.AttributionDegraded {
		t.Fatalf("AttributionDegraded = true on creation, want false")
	}

	if err := st.SetMacroRunAttributionDegraded(ctx, "run-degraded"); err != nil {
		t.Fatalf("set attribution degraded: %v", err)
	}
	if err := st.SetMacroRunAttributionDegraded(ctx, "run-degraded"); err != nil {
		t.Fatalf("set attribution degraded (second call): %v", err)
	}

	gotRun, _, err := st.GetMacroRun(ctx, "run-degraded")
	if err != nil {
		t.Fatalf("get macro run: %v", err)
	}
	if !gotRun.AttributionDegraded {
		t.Errorf("AttributionDegraded = false while the run is still running, want true (must be surfaced live, not only at finish)")
	}
	if gotRun.State != "running" {
		t.Fatalf("test setup invariant broken: State = %q, want running", gotRun.State)
	}
}

// TestSetMacroRunAttributionDegradedNotFound proves a nonexistent run
// reports ErrMacroRunNotFound.
func TestSetMacroRunAttributionDegradedNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	err := st.SetMacroRunAttributionDegraded(context.Background(), "does-not-exist")
	if !errors.Is(err, ErrMacroRunNotFound) {
		t.Errorf("error = %v, want ErrMacroRunNotFound", err)
	}
}

// TestListMacroRunsFilteredByMacroObjectID proves the optional
// macroObjectID filter actually filters, and that an empty filter returns
// every run across every macro.
func TestListMacroRunsFilteredByMacroObjectID(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	runA, stepsA := testMacroRun("run-a", "idem-a", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, runA, stepsA); err != nil {
		t.Fatalf("create run-a: %v", err)
	}
	runB, stepsB := testMacroRun("run-b", "idem-b", "macro-b")
	if _, _, err := st.CreateMacroRun(ctx, runB, stepsB); err != nil {
		t.Fatalf("create run-b: %v", err)
	}

	onlyA, err := st.ListMacroRuns(ctx, "macro-a", 0)
	if err != nil {
		t.Fatalf("list macro-a runs: %v", err)
	}
	if len(onlyA) != 1 || onlyA[0].ID != "run-a" {
		t.Fatalf("onlyA = %+v, want exactly run-a", onlyA)
	}

	all, err := st.ListMacroRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list all runs: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len(all) = %d, want 2", len(all))
	}
}

// TestListMacroRunsNewestFirst proves the documented ordering.
func TestListMacroRunsNewestFirst(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		id := idFor(i)
		run, steps := testMacroRun("run-"+id, "idem-"+id, "macro-a-"+id)
		if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
		clock.advance(time.Hour)
	}

	got, err := st.ListMacroRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list macro runs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	if got[0].ID != "run-cmd-race-c" || got[2].ID != "run-cmd-race-a" {
		t.Errorf("order = [%s, %s, %s], want newest (c) first, oldest (a) last", got[0].ID, got[1].ID, got[2].ID)
	}
}

// TestPruneMacroRunsByRowCountCascadesToSteps mirrors
// TestPruneCommandsByRowCount (commands_test.go) exactly, plus proves the
// part specific to this table: pruning a macro_runs row must take its
// macro_run_steps rows with it (ON DELETE CASCADE, schemaV7), not leave
// them behind as orphans.
func TestPruneMacroRunsByRowCountCascadesToSteps(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxMacroRunRows(2), WithMaxMacroRunAge(0))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		id := idFor(i)
		run, steps := testMacroRun("run-"+id, "idem-"+id, "macro-"+id)
		if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
		clock.advance(2 * time.Hour)
	}

	got, err := st.ListMacroRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list macro runs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (maxMacroRunRows=2 keeps only the newest two — a prune pass must actually have fired)", len(got))
	}

	var totalSteps int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM macro_run_steps`).Scan(&totalSteps); err != nil {
		t.Fatalf("count macro_run_steps: %v", err)
	}
	// Each surviving run has 2 steps (see testMacroRun); a pruned run's
	// steps must be gone too, not orphaned.
	if totalSteps != 4 {
		t.Errorf("macro_run_steps row count = %d, want 4 (2 surviving runs x 2 steps each — the pruned run's steps must have been cascade-deleted, not orphaned)", totalSteps)
	}
}

// TestPruneMacroRunsByAge mirrors TestPruneCommandsByAge exactly.
func TestPruneMacroRunsByAge(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxMacroRunAge(24*time.Hour), WithMaxMacroRunRows(1_000_000))
	ctx := context.Background()

	oldRun, oldSteps := testMacroRun("run-old", "idem-old", "macro-old")
	if _, _, err := st.CreateMacroRun(ctx, oldRun, oldSteps); err != nil {
		t.Fatalf("create old run: %v", err)
	}
	clock.advance(48 * time.Hour)
	newRun, newSteps := testMacroRun("run-new", "idem-new", "macro-new")
	if _, _, err := st.CreateMacroRun(ctx, newRun, newSteps); err != nil {
		t.Fatalf("create new run: %v", err)
	}

	got, err := st.ListMacroRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list macro runs: %v", err)
	}
	if len(got) != 1 || got[0].ID != "run-new" {
		t.Fatalf("got = %+v, want only the run younger than maxMacroRunAge", got)
	}

	var oldStepCount int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM macro_run_steps WHERE run_id = 'run-old'`).Scan(&oldStepCount); err != nil {
		t.Fatalf("count run-old steps: %v", err)
	}
	if oldStepCount != 0 {
		t.Errorf("run-old still has %d step rows after age-based pruning, want 0 (cascade)", oldStepCount)
	}
}

// TestGetMacroRunNotFound mirrors TestGetNodeNotFound/TestGetCommandByIdempotencyKey-shaped coverage.
func TestGetMacroRunNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, _, err := st.GetMacroRun(context.Background(), "does-not-exist")
	if !errors.Is(err, ErrMacroRunNotFound) {
		t.Errorf("error = %v, want ErrMacroRunNotFound", err)
	}
}

// TestCreateMacroRunStepRequiresSafetyClassAndLocalFallbackClass proves
// STEP-9-SPEC.md §2.5/§5.4's write-time requirement holds at the point this
// package actually persists a run's pinned per-step copies: a step with an
// empty SafetyClass or an empty LocalFallbackClass is rejected, and the
// rejected create leaves no run or step row behind: an unlabelled step
// silently defaulting to "" here would be exactly the "reads as absent"
// shape STEP-9-SPEC.md §6.1 warns against for a DIFFERENT field, applied to
// these two.
func TestCreateMacroRunStepRequiresSafetyClassAndLocalFallbackClass(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-missing-safety", "idem-missing-safety", "macro-a")
	steps[0].SafetyClass = ""
	if _, _, err := st.CreateMacroRun(ctx, run, steps); err == nil {
		t.Fatalf("create with an empty SafetyClass succeeded, want an error")
	} else if !strings.Contains(err.Error(), "SafetyClass") {
		t.Fatalf("error = %v, want it to name SafetyClass", err)
	}

	run2, steps2 := testMacroRun("run-missing-fallback", "idem-missing-fallback", "macro-a")
	steps2[0].LocalFallbackClass = ""
	if _, _, err := st.CreateMacroRun(ctx, run2, steps2); err == nil {
		t.Fatalf("create with an empty LocalFallbackClass succeeded, want an error")
	} else if !strings.Contains(err.Error(), "LocalFallbackClass") {
		t.Fatalf("error = %v, want it to name LocalFallbackClass", err)
	}

	all, err := st.ListMacroRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list macro runs: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("len(all) = %d, want 0 (both rejected creates must leave no row behind)", len(all))
	}
}

// TestCreateMacroRunStepRequiresOutcomeStateAndOutcomeReason is
// STEP-9-SPEC review finding 8's write-time requirement, proved the same
// way its SafetyClass/LocalFallbackClass sibling above is: a step with an
// empty OutcomeState or an empty OutcomeReason is rejected at creation, and
// the rejected create leaves no run or step row behind. Before this fix,
// createMacroRun validated every other required per-step field and left
// these two to round-trip as "" — indistinguishable, on read, from a step
// that resolved to a genuinely empty state — which is finding 8's own
// "nothing enforced it" defect, and this test breaks that behavior by
// deleting the validation clause and confirming the create then succeeds
// with a step OutcomeState/OutcomeReason permanently blank.
func TestCreateMacroRunStepRequiresOutcomeStateAndOutcomeReason(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-missing-outcome-state", "idem-missing-outcome-state", "macro-a")
	steps[0].OutcomeState = ""
	if _, _, err := st.CreateMacroRun(ctx, run, steps); err == nil {
		t.Fatalf("create with an empty OutcomeState succeeded, want an error")
	} else if !strings.Contains(err.Error(), "OutcomeState") {
		t.Fatalf("error = %v, want it to name OutcomeState", err)
	}

	run2, steps2 := testMacroRun("run-missing-outcome-reason", "idem-missing-outcome-reason", "macro-a")
	steps2[0].OutcomeReason = ""
	if _, _, err := st.CreateMacroRun(ctx, run2, steps2); err == nil {
		t.Fatalf("create with an empty OutcomeReason succeeded, want an error")
	} else if !strings.Contains(err.Error(), "OutcomeReason") {
		t.Fatalf("error = %v, want it to name OutcomeReason", err)
	}

	all, err := st.ListMacroRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list macro runs: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("len(all) = %d, want 0 (both rejected creates must leave no row behind)", len(all))
	}
}

// TestCreateMacroRunSameIdempotencyKeyDifferentMacroReturnsMacroMismatch is
// STEP-9-SPEC.md §6.2's first NEW outcome, corrected 2026-08-14: before
// this fix, this exact scenario returned a *DuplicateMacroRunError (a false
// "replay"), silently handing the caller someone else's run for a
// different macro instead of the distinct 409 §6.2 requires.
func TestCreateMacroRunSameIdempotencyKeyDifferentMacroReturnsMacroMismatch(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first, firstSteps := testMacroRun("run-mm-1", "idem-mm", "macro-a")
	created, _, err := st.CreateMacroRun(ctx, first, firstSteps)
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}

	second, secondSteps := testMacroRun("run-mm-2", "idem-mm", "macro-b")
	_, _, err = st.CreateMacroRun(ctx, second, secondSteps)
	if err == nil {
		t.Fatalf("create against a different macro with the same key succeeded, want a macro-mismatch conflict")
	}
	if !errors.Is(err, ErrMacroRunIdempotencyMacroMismatch) {
		t.Fatalf("error = %v, want it to wrap ErrMacroRunIdempotencyMacroMismatch", err)
	}
	if errors.Is(err, ErrMacroRunIdempotencyKeyExists) {
		t.Fatalf("error = %v, must NOT also read as a true replay: a macro mismatch is a distinct outcome from ErrMacroRunIdempotencyKeyExists", err)
	}
	var mm *MacroRunIdempotencyMacroMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("error = %v, want errors.As to find a *MacroRunIdempotencyMacroMismatchError", err)
	}
	if mm.Existing.ID != created.ID {
		t.Errorf("Existing.ID = %q, want %q (the original run)", mm.Existing.ID, created.ID)
	}
	if mm.RequestedMacroObjectID != "macro-b" {
		t.Errorf("RequestedMacroObjectID = %q, want %q", mm.RequestedMacroObjectID, "macro-b")
	}

	all, err := st.ListMacroRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list macro runs: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(all) = %d, want exactly 1 (the conflicting submission must not have been created)", len(all))
	}
}

// TestCreateMacroRunSameIdempotencyKeySameMacroDifferentRevisionReturnsRevisionMismatch
// is §6.2's SECOND new outcome, distinguished from both a true replay and a
// macro mismatch: the operator edited the macro between two submissions
// that reused one idempotency key, and the caller asked for two genuinely
// different things under it.
func TestCreateMacroRunSameIdempotencyKeySameMacroDifferentRevisionReturnsRevisionMismatch(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first, firstSteps := testMacroRun("run-rm-1", "idem-rm", "macro-a")
	created, _, err := st.CreateMacroRun(ctx, first, firstSteps)
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}

	second, secondSteps := testMacroRun("run-rm-2", "idem-rm", "macro-a")
	second.MacroRevision = 2 // the macro was edited between the two submissions
	_, _, err = st.CreateMacroRun(ctx, second, secondSteps)
	if err == nil {
		t.Fatalf("create against an edited macro revision with the same key succeeded, want a revision conflict")
	}
	if !errors.Is(err, ErrMacroRunIdempotencyRevisionMismatch) {
		t.Fatalf("error = %v, want it to wrap ErrMacroRunIdempotencyRevisionMismatch", err)
	}
	if errors.Is(err, ErrMacroRunIdempotencyKeyExists) || errors.Is(err, ErrMacroRunIdempotencyMacroMismatch) {
		t.Fatalf("error = %v, must be its own distinct outcome, not a true replay or a macro mismatch", err)
	}
	var rm *MacroRunIdempotencyRevisionMismatchError
	if !errors.As(err, &rm) {
		t.Fatalf("error = %v, want errors.As to find a *MacroRunIdempotencyRevisionMismatchError", err)
	}
	if rm.Existing.ID != created.ID {
		t.Errorf("Existing.ID = %q, want %q", rm.Existing.ID, created.ID)
	}
	if rm.RequestedMacroRevision != 2 {
		t.Errorf("RequestedMacroRevision = %d, want 2", rm.RequestedMacroRevision)
	}

	all, err := st.ListMacroRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list macro runs: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(all) = %d, want exactly 1", len(all))
	}
}

// TestSetMacroRunStepAttributionDegradedIsIdempotentAndIndependentOfRunLevel
// proves ADR-031 decision 5's per-step half: flipping one step's own
// AttributionDegraded is idempotent, does not touch a different step's flag
// on the same run, and does NOT implicitly also flip the run-level flag:
// [Store.SetMacroRunAttributionDegraded] is a SEPARATE call the executor
// (Wave 2) is expected to make in the same transaction, not something this
// method does on the caller's behalf; see this method's own doc comment.
func TestSetMacroRunStepAttributionDegradedIsIdempotentAndIndependentOfRunLevel(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-step-degraded", "idem-step-degraded", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
		t.Fatalf("create macro run: %v", err)
	}

	if err := st.SetMacroRunStepAttributionDegraded(ctx, "run-step-degraded", 0); err != nil {
		t.Fatalf("set step 0 attribution degraded: %v", err)
	}
	if err := st.SetMacroRunStepAttributionDegraded(ctx, "run-step-degraded", 0); err != nil {
		t.Fatalf("set step 0 attribution degraded (second call): %v", err)
	}

	gotRun, gotSteps, err := st.GetMacroRun(ctx, "run-step-degraded")
	if err != nil {
		t.Fatalf("get macro run: %v", err)
	}
	if !gotSteps[0].AttributionDegraded {
		t.Errorf("step 0 AttributionDegraded = false, want true")
	}
	if gotSteps[1].AttributionDegraded {
		t.Errorf("step 1 AttributionDegraded = true, want false (setting step 0 must not touch step 1)")
	}
	if gotRun.AttributionDegraded {
		t.Errorf("run-level AttributionDegraded = true, want false (setting a step's flag must not implicitly flip the run's)")
	}
}

// TestSetMacroRunStepAttributionDegradedNotFound proves a nonexistent
// (run, step) pair reports ErrMacroRunStepNotFound rather than silently
// succeeding.
func TestSetMacroRunStepAttributionDegradedNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	err := st.SetMacroRunStepAttributionDegraded(context.Background(), "does-not-exist", 0)
	if !errors.Is(err, ErrMacroRunStepNotFound) {
		t.Errorf("error = %v, want ErrMacroRunStepNotFound", err)
	}
}

// TestResolveMacroRunStepCommandDistinguishesNoneAvailableAndNotRetained is
// STEP-9-SPEC.md §6.1's dangling-by-design commands.id reference, proved
// through all three of [CommandDetailState]'s cases in the order a real
// step actually passes through them: never dispatched, dispatched with a
// live command row, and dispatched with the command row since pruned by
// retention. The third case is simulated directly against the commands
// table, exactly what retention.go's pruneCommands does for real, and
// exactly why macro_run_steps.command_id deliberately carries no foreign
// key (migrations.go's schemaV7 doc comment): never as though the step
// had never dispatched, which is CommandDetailNone's job, not this one's.
func TestResolveMacroRunStepCommandDistinguishesNoneAvailableAndNotRetained(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-cmd-detail", "idem-cmd-detail", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
		t.Fatalf("create macro run: %v", err)
	}

	_, gotSteps, err := st.GetMacroRun(ctx, "run-cmd-detail")
	if err != nil {
		t.Fatalf("get macro run: %v", err)
	}
	state, _, err := st.ResolveMacroRunStepCommand(ctx, gotSteps[0])
	if err != nil {
		t.Fatalf("resolve step 0 command (never dispatched): %v", err)
	}
	if state != CommandDetailNone {
		t.Fatalf("state = %q, want %q (never dispatched)", state, CommandDetailNone)
	}

	insertedCmd, err := st.InsertCommand(ctx, CommandRecord{
		ID: "cmd-detail-1", IdempotencyKey: "cmd-detail-1-key", Action: "startPlaylist",
		TargetKind: "fpp", TargetID: "fpp-main", State: "dispatched",
	})
	if err != nil {
		t.Fatalf("insert command: %v", err)
	}
	cmdID := insertedCmd.ID
	if err := st.UpdateMacroRunStepOutcome(ctx, "run-cmd-detail", 0, MacroRunStepOutcomeUpdate{CommandID: &cmdID}); err != nil {
		t.Fatalf("attach command_id to step 0: %v", err)
	}
	_, gotSteps, err = st.GetMacroRun(ctx, "run-cmd-detail")
	if err != nil {
		t.Fatalf("get macro run after dispatch: %v", err)
	}
	state, gotCmd, err := st.ResolveMacroRunStepCommand(ctx, gotSteps[0])
	if err != nil {
		t.Fatalf("resolve step 0 command (dispatched, command still live): %v", err)
	}
	if state != CommandDetailAvailable {
		t.Fatalf("state = %q, want %q", state, CommandDetailAvailable)
	}
	if gotCmd.ID != "cmd-detail-1" {
		t.Errorf("gotCmd.ID = %q, want cmd-detail-1", gotCmd.ID)
	}

	// Simulate retention pruning the commands row out from under the step:
	// this is exactly what pruneCommands (retention.go) does for real; the
	// step's own command_id is untouched, since there is no FK to cascade.
	if _, err := st.db.ExecContext(ctx, `DELETE FROM commands WHERE id = 'cmd-detail-1'`); err != nil {
		t.Fatalf("simulate the command row being pruned: %v", err)
	}
	state, notRetained, err := st.ResolveMacroRunStepCommand(ctx, gotSteps[0])
	if err != nil {
		t.Fatalf("resolve step 0 command (dispatched, command pruned): %v", err)
	}
	if state != CommandDetailNotRetained {
		t.Fatalf("state = %q, want %q (the step DID dispatch; only the command detail is gone, must not read as CommandDetailNone)", state, CommandDetailNotRetained)
	}
	if notRetained.ID != "" {
		t.Errorf("CommandRecord.ID = %q, want the zero value for CommandDetailNotRetained (the caller must read state, not this record, as authoritative)", notRetained.ID)
	}
}

// TestFormatParseMacroRunCallerIntentRoundTrip proves
// [FormatMacroRunCallerIntent]/[ParseMacroRunCallerIntent] round trip
// exactly, including a macroObjectID that itself contains "@": the case
// that proves parsing splits on the LAST "@", not the first.
func TestFormatParseMacroRunCallerIntentRoundTrip(t *testing.T) {
	cases := []struct {
		macroObjectID string
		revision      int64
	}{
		{"start-main-show", 1},
		{"start-main-show", 42},
		{"weird@object@id", 7},
	}
	for _, c := range cases {
		formatted := FormatMacroRunCallerIntent(c.macroObjectID, c.revision)
		gotID, gotRev, ok := ParseMacroRunCallerIntent(formatted)
		if !ok {
			t.Fatalf("ParseMacroRunCallerIntent(%q) ok = false, want true", formatted)
		}
		if gotID != c.macroObjectID || gotRev != c.revision {
			t.Errorf("ParseMacroRunCallerIntent(%q) = (%q, %d), want (%q, %d)",
				formatted, gotID, gotRev, c.macroObjectID, c.revision)
		}
	}
}

// TestParseMacroRunCallerIntentRejectsNonMacroValues proves the
// distinguishability STEP-9-SPEC.md §6.1 actually asks for: an
// operator-issued command's untouched default ("") and any value a macro
// dispatch never produced must both report ok == false, never a
// misleadingly "parsed" zero-valued macro id and revision.
func TestParseMacroRunCallerIntentRejectsNonMacroValues(t *testing.T) {
	for _, s := range []string{"", "7", "not-a-macro-revision", "macro:no-at-sign"} {
		if _, _, ok := ParseMacroRunCallerIntent(s); ok {
			t.Errorf("ParseMacroRunCallerIntent(%q) ok = true, want false (not a macro-issued value)", s)
		}
	}
}

// TestListRunningMacroRunsThenFinishMatchesReconciliationShape proves the
// THREE primitives ADR-031 decision 4's startup reconciler needs, this
// package's own [Store.ListRunningMacroRuns] to find a run stranded by a
// prior process, [Store.ResolveUnresolvedMacroRunSteps] to close out that
// run's own still-unresolved STEPS, then [Store.FinishMacroRun] to close
// the run itself completed:false with a reason, actually compose the way
// STEP-9-SPEC.md §6.5 requires ("called at startup... per §2.4"), mirroring
// [api.ReconcileStrandedFPPCommands]'s own ListUnresolvedCommands-then-
// UpdateCommandOutcome shape one level up, at the run rather than the
// command. The reconciler itself is Wave 2's (internal/coordinator/macro)
// to write; this test only proves the storage half it will be built on.
//
// Extended 2026-08-14 per STEP-9-SPEC review finding 8: the version of this
// test before the fix proved ONLY the run row reconciles correctly and
// never read a single macro_run_steps row back — the test's own name
// invokes "reconciliation shape" while asserting nothing about what that
// shape actually has to reconcile. Before the fix, this test would have
// passed even with steps 3/4/5 of a longer run permanently blank, which is
// exactly finding 8's failure scenario; it does not pass that way now,
// because a second step (index 1) is deliberately left unresolved past the
// simulated restart and the assertions below fail if it stays blank.
func TestListRunningMacroRunsThenFinishMatchesReconciliationShape(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-stranded", "idem-stranded", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
		t.Fatalf("create macro run: %v", err)
	}

	// Step 0 resolved BEFORE the coordinator restarted; step 1 did not —
	// this is finding 8's own failure scenario ("coordinator restarts at
	// step 3 of 5... steps 3, 4 and 5 keep outcome='' forever"), scaled down
	// to this test's two-step run: one step already resolved, one stranded.
	resolvedAt := st.now()
	confirmedOutcome, confirmedState := "confirmed", "current"
	if err := st.UpdateMacroRunStepOutcome(ctx, "run-stranded", 0, MacroRunStepOutcomeUpdate{
		ResolvedAt: &resolvedAt, Outcome: &confirmedOutcome, OutcomeState: &confirmedState,
	}); err != nil {
		t.Fatalf("resolve step 0 before the simulated restart: %v", err)
	}

	stranded, err := st.ListRunningMacroRuns(ctx)
	if err != nil {
		t.Fatalf("list running macro runs: %v", err)
	}
	if len(stranded) != 1 || stranded[0].ID != "run-stranded" {
		t.Fatalf("stranded = %+v, want exactly run-stranded", stranded)
	}

	for _, r := range stranded {
		unresolvedSteps, err := st.ListUnresolvedMacroRunSteps(ctx, r.ID)
		if err != nil {
			t.Fatalf("list unresolved macro run steps for %q: %v", r.ID, err)
		}
		if len(unresolvedSteps) != 1 || unresolvedSteps[0].StepIndex != 1 {
			t.Fatalf("unresolvedSteps = %+v, want exactly step 1 (step 0 already resolved before the simulated restart)", unresolvedSteps)
		}

		n, err := st.ResolveUnresolvedMacroRunSteps(ctx, r.ID, st.now(), "resolved", "unconfirmed",
			MacroRunStepOutcomeStatePending, "the coordinator restarted before this step could be dispatched or resolved")
		if err != nil {
			t.Fatalf("resolve unresolved macro run steps for %q: %v", r.ID, err)
		}
		if n != 1 {
			t.Fatalf("resolved %d steps for %q, want 1", n, r.ID)
		}

		if err := st.FinishMacroRun(ctx, r.ID, MacroRunFinishUpdate{
			FinishedAt: st.now(), Completed: false, Confirmed: false,
			Reason: "the coordinator restarted while this run was in flight",
		}); err != nil {
			t.Fatalf("finish stranded run %q: %v", r.ID, err)
		}
	}

	stillRunning, err := st.ListRunningMacroRuns(ctx)
	if err != nil {
		t.Fatalf("list running macro runs after reconciliation: %v", err)
	}
	if len(stillRunning) != 0 {
		t.Fatalf("stillRunning = %+v, want none left running after reconciliation", stillRunning)
	}

	gotRun, gotSteps, err := st.GetMacroRun(ctx, "run-stranded")
	if err != nil {
		t.Fatalf("get macro run: %v", err)
	}
	if gotRun.Completed == nil || *gotRun.Completed {
		t.Fatalf("Completed = %v, want a known false", gotRun.Completed)
	}
	if gotRun.Reason == "" {
		t.Errorf("Reason is empty, want the restart reason preserved")
	}

	// THE ASSERTION FINDING 8 SAYS WAS MISSING: read the steps back and
	// prove neither one renders blank, which is what "reconciliation shape"
	// actually has to mean for this table.
	if len(gotSteps) != 2 {
		t.Fatalf("len(gotSteps) = %d, want 2", len(gotSteps))
	}
	for _, step := range gotSteps {
		if step.Outcome == "" {
			t.Errorf("step %d Outcome is empty after reconciliation, want a stated outcome", step.StepIndex)
		}
		if step.OutcomeState == "" {
			t.Errorf("step %d OutcomeState is empty after reconciliation, want a stated state (finding 8's own failure mode)", step.StepIndex)
		}
		if step.OutcomeReason == "" {
			t.Errorf("step %d OutcomeReason is empty after reconciliation, want a stated reason (finding 8's own failure mode)", step.StepIndex)
		}
		if step.ResolvedAt == nil {
			t.Errorf("step %d ResolvedAt is nil after reconciliation, want it resolved", step.StepIndex)
		}
	}
	if gotSteps[0].Outcome != "confirmed" {
		t.Errorf("step 0 Outcome = %q, want it left as confirmed by the pre-restart resolution (reconciliation must not overwrite an already-resolved step)", gotSteps[0].Outcome)
	}
	if gotSteps[1].Outcome != "unconfirmed" {
		t.Errorf("step 1 Outcome = %q, want unconfirmed (the value the reconciliation pass supplied)", gotSteps[1].Outcome)
	}
	if gotSteps[1].OutcomeReason == MacroRunStepOutcomeReasonPending {
		t.Errorf("step 1 OutcomeReason is still the creation-time placeholder, want the reconciliation's own reason")
	}
}

// TestResolveUnresolvedMacroRunStepsOnlyTouchesUnresolvedSteps proves the
// bulk resolve is scoped to resolved_at IS NULL, not every step of the run:
// an already-resolved step must survive with its own outcome untouched, and
// a run with nothing left unresolved reports 0 resolved rather than an
// error.
func TestResolveUnresolvedMacroRunStepsOnlyTouchesUnresolvedSteps(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-bulk-resolve", "idem-bulk-resolve", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
		t.Fatalf("create macro run: %v", err)
	}

	// Resolve both steps up front, then prove a second call is a no-op.
	n, err := st.ResolveUnresolvedMacroRunSteps(ctx, "run-bulk-resolve", st.now(), "resolved", "unconfirmed",
		MacroRunStepOutcomeStatePending, "the coordinator restarted before this step could be dispatched or resolved")
	if err != nil {
		t.Fatalf("resolve unresolved macro run steps: %v", err)
	}
	if n != 2 {
		t.Fatalf("resolved %d steps, want 2", n)
	}

	n, err = st.ResolveUnresolvedMacroRunSteps(ctx, "run-bulk-resolve", st.now(), "resolved", "unconfirmed",
		MacroRunStepOutcomeStatePending, "should not be reachable: nothing left unresolved")
	if err != nil {
		t.Fatalf("resolve unresolved macro run steps (second call): %v", err)
	}
	if n != 0 {
		t.Fatalf("resolved %d steps on a run with nothing left unresolved, want 0 (a harmless no-op, not an error)", n)
	}

	_, gotSteps, err := st.GetMacroRun(ctx, "run-bulk-resolve")
	if err != nil {
		t.Fatalf("get macro run: %v", err)
	}
	for _, step := range gotSteps {
		if step.OutcomeReason != "the coordinator restarted before this step could be dispatched or resolved" {
			t.Errorf("step %d OutcomeReason = %q, want it from the FIRST resolve call only (the second call's reason must never have been reachable)", step.StepIndex, step.OutcomeReason)
		}
	}
}

// TestResolveUnresolvedMacroRunStepsRequiresAllFourFields proves the bulk
// resolve rejects a blank state/outcome/outcomeState/outcomeReason rather
// than silently writing one — a caller resolving a stranded step with a
// blank outcome_state or outcome_reason would otherwise reopen finding 8
// through this exact method.
func TestResolveUnresolvedMacroRunStepsRequiresAllFourFields(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-bulk-resolve-validate", "idem-bulk-resolve-validate", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
		t.Fatalf("create macro run: %v", err)
	}

	cases := []struct {
		name                                        string
		state, outcome, outcomeState, outcomeReason string
	}{
		{"empty state", "", "unconfirmed", "not_collected", "reason"},
		{"empty outcome", "resolved", "", "not_collected", "reason"},
		{"empty outcomeState", "resolved", "unconfirmed", "", "reason"},
		{"empty outcomeReason", "resolved", "unconfirmed", "not_collected", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := st.ResolveUnresolvedMacroRunSteps(ctx, "run-bulk-resolve-validate", st.now(),
				c.state, c.outcome, c.outcomeState, c.outcomeReason); err == nil {
				t.Fatalf("resolve with %s succeeded, want an error", c.name)
			}
		})
	}

	// None of the rejected calls may have touched the steps: still exactly
	// the creation-time pending sentinel on both.
	_, gotSteps, err := st.GetMacroRun(ctx, "run-bulk-resolve-validate")
	if err != nil {
		t.Fatalf("get macro run: %v", err)
	}
	for _, step := range gotSteps {
		if step.OutcomeState != MacroRunStepOutcomeStatePending {
			t.Errorf("step %d OutcomeState = %q, want it untouched at the creation-time pending sentinel", step.StepIndex, step.OutcomeState)
		}
		if step.ResolvedAt != nil {
			t.Errorf("step %d ResolvedAt = %v, want nil (untouched by any rejected resolve call)", step.StepIndex, step.ResolvedAt)
		}
	}
}

// TestListUnresolvedMacroRunStepsScopedToOneRun proves the run_id filter
// actually filters: a second run's unresolved steps must never appear in
// the first run's list.
func TestListUnresolvedMacroRunStepsScopedToOneRun(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	runA, stepsA := testMacroRun("run-unresolved-a", "idem-unresolved-a", "macro-a")
	if _, _, err := st.CreateMacroRun(ctx, runA, stepsA); err != nil {
		t.Fatalf("create run A: %v", err)
	}
	runB, stepsB := testMacroRun("run-unresolved-b", "idem-unresolved-b", "macro-b")
	if _, _, err := st.CreateMacroRun(ctx, runB, stepsB); err != nil {
		t.Fatalf("create run B: %v", err)
	}

	gotA, err := st.ListUnresolvedMacroRunSteps(ctx, "run-unresolved-a")
	if err != nil {
		t.Fatalf("list unresolved steps for run A: %v", err)
	}
	if len(gotA) != 2 {
		t.Fatalf("len(gotA) = %d, want 2 (both of run A's own steps, none of run B's)", len(gotA))
	}
	for _, step := range gotA {
		if step.RunID != "run-unresolved-a" {
			t.Errorf("step.RunID = %q, want run-unresolved-a", step.RunID)
		}
	}
}

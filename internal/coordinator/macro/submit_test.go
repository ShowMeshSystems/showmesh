package macro

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func TestSubmitRunUnknownMacroIs404Problem(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	_, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "does-not-exist", IdempotencyKey: "key-1", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil {
		t.Fatalf("unexpected internal error: %v", err)
	}
	if problem == nil {
		t.Fatalf("expected a problem for an unknown macro, got none")
	}
	if problem.Status != 404 {
		t.Fatalf("status = %d, want 404", problem.Status)
	}
	e.wg.Wait()
}

func TestSubmitRunEmptyIdempotencyKeyIsProblem(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	_, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil {
		t.Fatalf("unexpected internal error: %v", err)
	}
	if problem == nil || problem.Status != 400 {
		t.Fatalf("expected a 400 problem for an empty idempotency key, got %+v (err=%v)", problem, err)
	}
}

func TestSubmitRunReplayReturnsSameRunNotNew(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	req := api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "key-1", Trigger: "api", Issuer: testIssuer()}

	first, problem, err := e.SubmitRun(context.Background(), req)
	if err != nil || problem != nil {
		t.Fatalf("first submit: problem=%+v err=%v", problem, err)
	}
	if first.Replay {
		t.Fatalf("first submit reported Replay=true")
	}

	second, problem, err := e.SubmitRun(context.Background(), req)
	if err != nil || problem != nil {
		t.Fatalf("second submit (replay): problem=%+v err=%v", problem, err)
	}
	if !second.Replay {
		t.Fatalf("second submit with the same idempotency key did not report Replay=true")
	}
	if second.Run.ID != first.Run.ID {
		t.Fatalf("replay returned a different run id: first=%s second=%s", first.Run.ID, second.Run.ID)
	}

	e.wg.Wait()
}

func TestSubmitRunSameKeyDifferentMacroIsDistinctConflict(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))
	putMacro(t, st, "m2", testMacroPayload(testStep("s1", "a1")))

	if _, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "shared-key", Trigger: "api", Issuer: testIssuer(),
	}); err != nil || problem != nil {
		t.Fatalf("first submit: problem=%+v err=%v", problem, err)
	}

	_, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m2", IdempotencyKey: "shared-key", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil {
		t.Fatalf("unexpected internal error: %v", err)
	}
	if problem == nil || problem.Status != 409 {
		t.Fatalf("expected a 409 problem for a key reused against a different macro, got %+v", problem)
	}
	if problem.Type != ProblemTypeMacroRunIdempotencyMacroConflict {
		t.Fatalf("problem.Type = %q, want %q", problem.Type, ProblemTypeMacroRunIdempotencyMacroConflict)
	}
	e.wg.Wait()
}

func TestSubmitRunSameMacroDifferentRevisionIsDistinctConflict(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	if _, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "shared-key", Trigger: "api", Issuer: testIssuer(),
	}); err != nil || problem != nil {
		t.Fatalf("first submit: problem=%+v err=%v", problem, err)
	}

	// Edit the macro (a new revision) between the two submissions.
	payloadJSON, err := config.EncodeShowMacroPayload(testMacroPayload(testStep("s1", "a1"), testStep("s2", "a1")))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowMacroConfigKind, ObjectID: "m1", Revision: 2, PayloadJSON: payloadJSON,
		CreatedByPrincipalID: "test", CreatedByPrincipalName: "test", Source: "api",
	}); err != nil {
		t.Fatalf("create revision 2: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowMacroConfigKind, "m1", 2); err != nil {
		t.Fatalf("activate revision 2: %v", err)
	}

	_, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "shared-key", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil {
		t.Fatalf("unexpected internal error: %v", err)
	}
	if problem == nil || problem.Status != 409 {
		t.Fatalf("expected a 409 problem for a key reused against an edited macro, got %+v", problem)
	}
	if problem.Type != ProblemTypeMacroRunIdempotencyRevisionConflict {
		t.Fatalf("problem.Type = %q, want %q", problem.Type, ProblemTypeMacroRunIdempotencyRevisionConflict)
	}
	e.wg.Wait()
}

func TestSubmitRunOverlapGuardRefusesSecondRunOfSameMacro(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	// A dispatcher that blocks until released, so the first run's ONE step
	// stays dispatching (and the run stays state=="running") long enough
	// for a second submission to observe it before the background
	// goroutine can finish the run out from under this test.
	release := make(chan struct{})
	dispatch := &fakeDispatcher{dispatchFn: func(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
		<-release
		return api.FPPCommandOutcome{
			CommandID: "cmd-1", Outcome: "confirmed", OutcomeState: "current", OutcomeReason: "ok",
			DispatchedAt: ptrTime(time.Now()), ResolvedAt: ptrTime(time.Now()),
		}, nil, nil
	}}
	e, _ := newTestExecutor(t, st, svc, dispatch, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	first, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "key-a", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil || problem != nil {
		close(release)
		t.Fatalf("first submit: problem=%+v err=%v", problem, err)
	}

	_, problem, err = e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "key-b", Trigger: "api", Issuer: testIssuer(),
	})
	close(release)
	if err != nil {
		t.Fatalf("unexpected internal error: %v", err)
	}
	if problem == nil || problem.Status != 409 {
		t.Fatalf("expected a 409 overlap problem, got %+v", problem)
	}
	if problem.Type != ProblemTypeMacroRunAlreadyInFlight {
		t.Fatalf("problem.Type = %q, want %q", problem.Type, ProblemTypeMacroRunAlreadyInFlight)
	}
	if want := first.Run.ID; want == "" {
		t.Fatalf("first run has no id")
	}
	e.wg.Wait()
}

// TestSubmitRunIdempotencyRunsBeforeOverlapGuard is the shared contract's
// item 6 verification: a legitimate replay of an in-flight run must return
// that run, never the overlap 409, even though a second, different
// submission of the SAME macro at the SAME moment would correctly get the
// 409.
//
// The required break-test was performed directly against this test:
// conflictResult's *store.DuplicateMacroRunError branch (submit.go) was
// temporarily gated `if false && errors.As(err, &dup)`, so a true replay
// fell through to conflictResult's final `fmt.Errorf("... called on a
// non-conflict error ...")` instead of being recognized. This test then
// failed exactly as expected ("unexpected internal error on replay: macro:
// conflictResult called on a non-conflict error: store: macro run with
// idempotency key ... already exists"), confirming the test actually
// detects the regression. The change was reverted and the test re-run to
// confirm it passes again. See this builder's own report for the exact
// diff and both observed outputs.
func TestSubmitRunIdempotencyRunsBeforeOverlapGuard(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	req := api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "same-key", Trigger: "api", Issuer: testIssuer()}

	first, problem, err := e.SubmitRun(context.Background(), req)
	if err != nil || problem != nil {
		t.Fatalf("first submit: problem=%+v err=%v", problem, err)
	}

	// Replay of the SAME key while the run may still legitimately be
	// "running": must return the existing run, not the overlap 409.
	replay, problem, err := e.SubmitRun(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected internal error on replay: %v", err)
	}
	if problem != nil {
		t.Fatalf("a legitimate replay of an in-flight run was refused with a problem instead of returning the run: %+v", problem)
	}
	if !replay.Replay || replay.Run.ID != first.Run.ID {
		t.Fatalf("replay = %+v, want Replay=true and Run.ID=%s", replay, first.Run.ID)
	}
	e.wg.Wait()
}

// TestSubmitRunAuditUnavailableAtSubmissionProceedsForANonExemptRun is the
// REVERSED form of a test that used to assert the opposite, and the
// reversal is an owner decision (2026-08-14): a macro run never withholds a
// command because the audit store is down, whatever its steps' safety
// classes.
//
// The test it replaces asserted a 503 and an empty store. That behaviour
// was measured against a real unwritable audit_log during the Step 9 wave 3
// acceptance run, on a [stop, start] macro, and what it actually did was
// refuse the STOP because the macro also contained a start. Fail-closed
// protects an operator from an unaccountable actor; here it protected
// nobody and cost the show its stop.
//
// startPlaylist is deliberately still the step under test, because it is
// NOT one of ADR-024 decision 11's three exempt classes: if the exemption
// were still what decided this, this test would fail.
func TestSubmitRunAuditUnavailableAtSubmissionProceedsForANonExemptRun(t *testing.T) {
	st, svc, storeDir := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", config.ShowSafetyClassNone, map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	installFailAuditTrigger(t, storeDir)

	result, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "key-1", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil {
		t.Fatalf("unexpected internal error: %v", err)
	}
	if problem != nil {
		t.Fatalf("expected the run to proceed degraded, got problem=%+v", problem)
	}
	// Degraded attribution is the cost, and it must be recorded rather
	// than silently swallowed: a run nobody can attribute is still a run
	// the operator has to be told about.
	if !result.Run.AttributionDegraded {
		t.Fatal("run proceeded with an unwritable audit store but did not record AttributionDegraded")
	}
	e.wg.Wait()
}

func TestSubmitRunAuditUnavailableAtSubmissionProceedsWhenAllStepsExempt(t *testing.T) {
	st, svc, storeDir := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	// stopPlaylist IS exempt (ADR-024 decision 11).
	putAction(t, st, "a1", fppAction("fpp-main", "stopPlaylist", config.ShowSafetyClassStop, nil))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	installFailAuditTrigger(t, storeDir)

	result, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "key-1", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil || problem != nil {
		t.Fatalf("expected the run to proceed degraded, got problem=%+v err=%v", problem, err)
	}
	if !result.Run.AttributionDegraded {
		t.Fatalf("run created with an unwritable audit store and every step exempt did not record AttributionDegraded")
	}
	e.wg.Wait()
}

func TestSubmitRunPersistsRevisionsAtSubmissionTime(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	result, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "key-1", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil || problem != nil {
		t.Fatalf("submit: problem=%+v err=%v", problem, err)
	}
	if result.Run.MacroRevision != 1 {
		t.Fatalf("MacroRevision = %d, want 1", result.Run.MacroRevision)
	}
	if len(result.Steps) != 1 || result.Steps[0].ActionRevision != 1 {
		t.Fatalf("steps = %+v, want one step at ActionRevision 1", result.Steps)
	}
	e.wg.Wait()
}

func TestGetRunUnknownIDWrapsSentinel(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	_, err := e.GetRun(context.Background(), "does-not-exist")
	if !errors.Is(err, api.ErrMacroRunNotFound) {
		t.Fatalf("GetRun on an unknown id: err = %v, want wrapped api.ErrMacroRunNotFound", err)
	}
}

// setupTwoShowRuns submits one run for each of two macros belonging to
// different shows ("halloween-2026" and "christmas-2026"), and returns the
// executor those runs live in.
func setupTwoShowRuns(t *testing.T) *Executor {
	t.Helper()
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m-halloween", config.ShowMacroPayload{
		Show: "halloween-2026", Label: "spooky macro", Steps: []config.ShowMacroStep{testStep("s1", "a1")},
	})
	putMacro(t, st, "m-christmas", config.ShowMacroPayload{
		Show: "christmas-2026", Label: "jolly macro", Steps: []config.ShowMacroStep{testStep("s1", "a1")},
	})

	if _, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m-halloween", IdempotencyKey: "key-halloween", Trigger: "api", Issuer: testIssuer(),
	}); err != nil || problem != nil {
		t.Fatalf("submit halloween run: problem=%+v err=%v", problem, err)
	}
	if _, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m-christmas", IdempotencyKey: "key-christmas", Trigger: "api", Issuer: testIssuer(),
	}); err != nil || problem != nil {
		t.Fatalf("submit christmas run: problem=%+v err=%v", problem, err)
	}
	e.wg.Wait()
	return e
}

// TestListRunsFiltersByShow proves GET /macro-runs?show= actually narrows
// the result instead of silently returning every show's runs.
//
// Broken and confirmed to fail: changed the `if f.Show != "" && r.Show !=
// f.Show { continue }` line in ListRuns to a no-op and reran — this test's
// "runs = 2, want 1" assertion failed as expected (both the halloween and
// christmas run came back for a filter naming only halloween-2026).
// Restored afterward.
func TestListRunsFiltersByShow(t *testing.T) {
	e := setupTwoShowRuns(t)

	runs, err := e.ListRuns(context.Background(), api.MacroRunFilter{Show: "halloween-2026"})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if runs[0].Show != "halloween-2026" {
		t.Fatalf("runs[0].Show = %q, want halloween-2026", runs[0].Show)
	}
}

// TestListRunsEmptyShowReturnsEverything proves an empty/absent show
// narrows nothing, unchanged from before this filter existed.
func TestListRunsEmptyShowReturnsEverything(t *testing.T) {
	e := setupTwoShowRuns(t)

	runs, err := e.ListRuns(context.Background(), api.MacroRunFilter{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
}

// TestListRunsShowMatchingNothingReturnsEmptyList proves a show naming no
// run is a legitimate empty answer, not an error.
func TestListRunsShowMatchingNothingReturnsEmptyList(t *testing.T) {
	e := setupTwoShowRuns(t)

	runs, err := e.ListRuns(context.Background(), api.MacroRunFilter{Show: "no-such-show"})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %d, want 0", len(runs))
	}
}

// TestListRunsCombinesShowAndState proves show and state narrow together,
// including the "running" branch which is served by a different store call
// ([Store.ListRunningMacroRuns], via [Executor.listRunningRuns]) than
// "finished"/no-state is. The dispatch fake blocks on release so the run
// stays state=="running" until this test lets it finish.
func TestListRunsCombinesShowAndState(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)

	release := make(chan struct{})
	dispatch := &fakeDispatcher{dispatchFn: func(_ context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
		<-release
		now := time.Now()
		return api.FPPCommandOutcome{
			CommandID: "cmd-" + in.IdempotencyKey, Action: in.Action, InstanceID: in.InstanceID, Params: in.Params,
			Outcome: "confirmed", OutcomeState: "current", OutcomeReason: "test evidence confirmed",
			DispatchedAt: ptrTime(now), ResolvedAt: ptrTime(now),
		}, nil, nil
	}}
	e, _ := newTestExecutor(t, st, svc, dispatch, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m-halloween", config.ShowMacroPayload{
		Show: "halloween-2026", Label: "spooky macro", Steps: []config.ShowMacroStep{testStep("s1", "a1")},
	})

	if _, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m-halloween", IdempotencyKey: "key-halloween", Trigger: "api", Issuer: testIssuer(),
	}); err != nil || problem != nil {
		t.Fatalf("submit halloween run: problem=%+v err=%v", problem, err)
	}

	// The step's dispatch is blocked on release, so this run is still
	// state=="running" — the filter combination must find it there.
	runs, err := e.ListRuns(context.Background(), api.MacroRunFilter{Show: "halloween-2026", State: "running"})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Show != "halloween-2026" || runs[0].State != "running" {
		t.Fatalf("runs = %+v, want exactly one running halloween-2026 run", runs)
	}

	// A show id that does not own the running run must not match it, even
	// combined with the right state.
	runs, err = e.ListRuns(context.Background(), api.MacroRunFilter{Show: "christmas-2026", State: "running"})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %d, want 0 for a show with no running runs", len(runs))
	}

	close(release)
	e.wg.Wait()

	runs, err = e.ListRuns(context.Background(), api.MacroRunFilter{Show: "halloween-2026", State: "finished"})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].State != "finished" {
		t.Fatalf("runs = %+v, want exactly one finished halloween-2026 run once the block is released", runs)
	}
}

package macro

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// storeRunRecord builds the minimal store.MacroRunRecord
// TestReconcileFinishesStrandedRunsNotCompleted needs to create a
// "running" row directly, bypassing SubmitRun, to simulate a coordinator
// that crashed before ever executing a run it had already accepted.
func storeRunRecord(id, macroObjectID string, macroRevision int64) store.MacroRunRecord {
	return store.MacroRunRecord{
		ID: id, MacroObjectID: macroObjectID, MacroRevision: macroRevision, Show: "test-show",
		Trigger: "api", IssuerPrincipalID: "p1", IssuerPrincipalName: "tester",
		IdempotencyKey: "stranded-key-" + id, State: "running",
	}
}

func submitAndWait(t *testing.T, e *Executor, req api.MacroSubmitRequest) api.MacroRunResult {
	t.Helper()
	result, problem, err := e.SubmitRun(context.Background(), req)
	if err != nil || problem != nil {
		t.Fatalf("submit: problem=%+v err=%v", problem, err)
	}
	e.wg.Wait()
	got, err := e.GetRun(context.Background(), result.Run.ID)
	if err != nil {
		t.Fatalf("get run after execution: %v", err)
	}
	return got
}

// TestUnconfirmedStepDoesNotAbortByDefault is acceptance criterion 14 /
// this task's own required break-test 1: an unconfirmed step neither
// aborts the run nor sets completed=false, but DOES set confirmed=false
// with a reason.
func TestUnconfirmedStepDoesNotAbortByDefault(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	dispatch := &fakeDispatcher{dispatchFn: func(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
		now := time.Now()
		return api.FPPCommandOutcome{
			CommandID: "cmd-1", Outcome: "unconfirmed", OutcomeState: "unknown_age", OutcomeReason: "no evidence arrived",
			DispatchedAt: ptrTime(now), ResolvedAt: ptrTime(now),
		}, nil, nil
	}}
	e, _ := newTestExecutor(t, st, svc, dispatch, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putAction(t, st, "a2", fppAction("fpp-main", "setVolume", "none", map[string]any{"volume": int64(50)}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1"), testStep("s2", "a2")))

	run := submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if run.Run.Completed == nil || !*run.Run.Completed {
		t.Fatalf("Completed = %v, want true (an unconfirmed step must not abort by default)", run.Run.Completed)
	}
	if run.Run.Confirmed == nil || *run.Run.Confirmed {
		t.Fatalf("Confirmed = %v, want false", run.Run.Confirmed)
	}
	if run.Run.Reason == "" {
		t.Fatalf("Reason is empty; an unconfirmed step must name itself")
	}
	if dispatch.callCount() != 2 {
		t.Fatalf("dispatch called %d times, want 2 (both steps must run since the run did not abort)", dispatch.callCount())
	}
	for i, st := range run.Steps {
		if st.Outcome != outcomeUnconfirmed {
			t.Fatalf("step %d outcome = %q, want %q", i, st.Outcome, outcomeUnconfirmed)
		}
	}
}

// TestOnUnconfirmedAbortDoesAbort is the companion to the previous test:
// with onUnconfirmed:"abort" on the step, the identical unconfirmed
// dispatch DOES abort the run.
func TestOnUnconfirmedAbortDoesAbort(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	dispatch := &fakeDispatcher{dispatchFn: func(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
		now := time.Now()
		return api.FPPCommandOutcome{
			CommandID: "cmd-1", Outcome: "unconfirmed", OutcomeState: "unknown_age", OutcomeReason: "no evidence arrived",
			DispatchedAt: ptrTime(now), ResolvedAt: ptrTime(now),
		}, nil, nil
	}}
	e, _ := newTestExecutor(t, st, svc, dispatch, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putAction(t, st, "a2", fppAction("fpp-main", "setVolume", "none", map[string]any{"volume": int64(50)}))

	step1 := testStep("s1", "a1")
	step1.OnUnconfirmed = config.ShowMacroOnUnconfirmedAbort
	putMacro(t, st, "m1", testMacroPayload(step1, testStep("s2", "a2")))

	run := submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if run.Run.Completed == nil || *run.Run.Completed {
		t.Fatalf("Completed = %v, want false (onUnconfirmed:abort must abort)", run.Run.Completed)
	}
	if dispatch.callCount() != 1 {
		t.Fatalf("dispatch called %d times, want 1 (step 2 must be skipped)", dispatch.callCount())
	}
	if run.Steps[1].Outcome != outcomeSkipped {
		t.Fatalf("step 2 outcome = %q, want %q", run.Steps[1].Outcome, outcomeSkipped)
	}
}

// TestFailedStepAbortsByDefault is acceptance criterion 2.
func TestFailedStepAbortsByDefault(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	dispatch := &fakeDispatcher{dispatchFn: func(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
		p := invalidParameterProblem("refused for test")
		return api.FPPCommandOutcome{}, &p, nil
	}}
	e, _ := newTestExecutor(t, st, svc, dispatch, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putAction(t, st, "a2", fppAction("fpp-main", "setVolume", "none", map[string]any{"volume": int64(50)}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1"), testStep("s2", "a2")))

	run := submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if run.Run.Completed == nil || *run.Run.Completed {
		t.Fatalf("Completed = %v, want false", run.Run.Completed)
	}
	if run.Steps[0].Outcome != outcomeFailed {
		t.Fatalf("step 1 outcome = %q, want %q", run.Steps[0].Outcome, outcomeFailed)
	}
	if run.Steps[1].Outcome != outcomeSkipped {
		t.Fatalf("step 2 outcome = %q, want %q (never attempted)", run.Steps[1].Outcome, outcomeSkipped)
	}
	if dispatch.callCount() != 1 {
		t.Fatalf("dispatch called %d times, want 1", dispatch.callCount())
	}
}

// TestOnFailureContinueDoesNotAbort is acceptance criterion 3.
func TestOnFailureContinueDoesNotAbort(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	callN := 0
	dispatch := &fakeDispatcher{dispatchFn: func(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
		callN++
		if callN == 1 {
			p := invalidParameterProblem("refused for test")
			return api.FPPCommandOutcome{}, &p, nil
		}
		now := time.Now()
		return api.FPPCommandOutcome{
			CommandID: "cmd-2", Outcome: "confirmed", OutcomeState: "current", OutcomeReason: "ok",
			DispatchedAt: ptrTime(now), ResolvedAt: ptrTime(now),
		}, nil, nil
	}}
	e, _ := newTestExecutor(t, st, svc, dispatch, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putAction(t, st, "a2", fppAction("fpp-main", "setVolume", "none", map[string]any{"volume": int64(50)}))

	step1 := testStep("s1", "a1")
	step1.OnFailure = config.ShowMacroOnFailureContinue
	putMacro(t, st, "m1", testMacroPayload(step1, testStep("s2", "a2")))

	run := submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if run.Run.Completed == nil || !*run.Run.Completed {
		t.Fatalf("Completed = %v, want true (onFailure:continue must not abort)", run.Run.Completed)
	}
	if run.Run.Confirmed == nil || *run.Run.Confirmed {
		t.Fatalf("Confirmed = %v, want false (a failed step must still mark the run not confirmed)", run.Run.Confirmed)
	}
	if run.Run.Reason == "" {
		t.Fatalf("Reason is empty")
	}
	if run.Steps[1].Outcome != outcomeConfirmed {
		t.Fatalf("step 2 outcome = %q, want %q (must still run)", run.Steps[1].Outcome, outcomeConfirmed)
	}
	if dispatch.callCount() != 2 {
		t.Fatalf("dispatch called %d times, want 2", dispatch.callCount())
	}
}

// TestCompletedAndConfirmedAreSeparateFacts is ADR-031 decision 3: a
// structurally unconfirmable MQTT step (expect.kind:none) reports
// completed:true, confirmed:false EVERY time it runs correctly.
func TestCompletedAndConfirmedAreSeparateFacts(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", mqttAction("home-automation", "none", config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNone}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	run := submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if run.Run.Completed == nil || !*run.Run.Completed {
		t.Fatalf("Completed = %v, want true", run.Run.Completed)
	}
	if run.Run.Confirmed == nil || *run.Run.Confirmed {
		t.Fatalf("Confirmed = %v, want false (expect.kind:none is structurally unconfirmable)", run.Run.Confirmed)
	}
	if run.Steps[0].Outcome != outcomeUnconfirmable {
		t.Fatalf("step outcome = %q, want %q", run.Steps[0].Outcome, outcomeUnconfirmable)
	}
}

func TestReconcileFinishesStrandedRunsNotCompleted(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1"), testStep("s2", "a1")))

	// Build a "running" run directly against the store, bypassing
	// SubmitRun, to simulate a coordinator that crashed mid-run: nothing
	// ever executed this run's steps.
	rm, err := e.resolveMacro(context.Background(), "m1")
	if err != nil {
		t.Fatalf("resolveMacro: %v", err)
	}
	steps := buildStepRecords(rm)
	run, _, err := e.store.CreateMacroRun(context.Background(), storeRunRecord("stranded-1", "m1", rm.Revision), steps)
	if err != nil {
		t.Fatalf("create stranded run: %v", err)
	}

	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, steps2, err := e.store.GetMacroRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get reconciled run: %v", err)
	}
	if got.State != "finished" {
		t.Fatalf("State = %q, want finished", got.State)
	}
	if got.Completed == nil || *got.Completed {
		t.Fatalf("Completed = %v, want false", got.Completed)
	}
	if got.Reason == "" {
		t.Fatalf("Reason is empty")
	}
	// A run whose steps never ran produced no evidence, so it is not
	// confirmed. The first version of the reconciler skipped unresolved
	// steps and started from true, so a coordinator that restarted one
	// second after accepting a run finished it with every step reading
	// skipped and the run itself reading confirmed. ADR-031 decision 3
	// defines confirmed as every step having produced post-dispatch
	// evidence, and none of these produced any.
	if got.Confirmed == nil || *got.Confirmed {
		t.Fatalf("Confirmed = %v, want false: no step of this run ever resolved, so nothing was confirmed", got.Confirmed)
	}
	for i, s := range steps2 {
		if s.ResolvedAt == nil {
			t.Fatalf("step %d has no ResolvedAt after reconciliation", i)
		}
		if s.Outcome == "" {
			t.Fatalf("step %d has an empty Outcome after reconciliation (should never be left blank)", i)
		}
	}

	// No remaining running run for this macro.
	if _, err := e.store.FindRunningMacroRun(context.Background(), "m1"); err == nil {
		t.Fatalf("a running run still exists for m1 after reconciliation")
	}

	// A second Reconcile call is a no-op, not an error.
	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
}

package macro

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
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

// TestFailedStepDoesNotAbortByDefault is acceptance criterion 2, REVERSED
// 2026-08-14 by owner decision: a macro run always runs every step, and a
// failure is recorded rather than allowed to suppress the rest of the
// sequence. See config.ShowMacroOnFailureDefault's own doc comment.
//
// The property this test still guards is the one that mattered all along
// and is now MORE important, not less: the difference between a step that
// was tried and failed and a step that was never attempted must stay
// visible. Nothing reads "skipped" here any more, because nothing is
// skipped; step 2 dispatches on its own merits after step 1 failed.
func TestFailedStepDoesNotAbortByDefault(t *testing.T) {
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

	// completed stays false: the run did not do what it was asked to do,
	// and saying otherwise because it reached the end would be the
	// "reports success regardless" failure ADR-029 names by hand.
	if run.Run.Completed == nil || *run.Run.Completed {
		t.Fatalf("Completed = %v, want false", run.Run.Completed)
	}
	if run.Run.Reason == "" {
		t.Fatal("Reason is empty; a run that failed a step must name it")
	}
	if run.Steps[0].Outcome != outcomeFailed {
		t.Fatalf("step 1 outcome = %q, want %q", run.Steps[0].Outcome, outcomeFailed)
	}
	if run.Steps[1].Outcome == outcomeSkipped {
		t.Fatalf("step 2 outcome = %q; a failed step must no longer suppress the steps after it", run.Steps[1].Outcome)
	}
	if dispatch.callCount() != 2 {
		t.Fatalf("dispatch called %d times, want 2 (every step runs)", dispatch.callCount())
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

	// completed is false because a step FAILED, which is independent of
	// whether the run continued past it: onFailure decides the sequence,
	// completed reports the outcome. Before 2026-08-14 these were the same
	// flag, which is why this assertion used to read "want true".
	if run.Run.Completed == nil || *run.Run.Completed {
		t.Fatalf("Completed = %v, want false (a failed step is not a completed run, even when the run continues)", run.Run.Completed)
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

// TestReconcileDoesNotRecordADispatchedStepAsSkipped is this task's own
// required break-test: a real coordinator was SIGKILLed partway through a
// four-step macro. Step 0 had already resolved before the kill. Step 1 had
// DISPATCHED — its command reached FPP and changed the volume to 42 — but
// the process died before the dispatch call returned, so
// macro_run_steps.dispatched_at/command_id for step 1 were never written.
// Steps 2 and 3 never started at all.
//
// This reproduces that shape directly against the store (bypassing
// SubmitRun/executeRun, exactly like TestReconcileFinishesStrandedRunsNotCompleted
// above): a "running" run with all-pending steps, plus a commands row
// inserted and resolved under step 1's own deterministic idempotency key —
// api.ReconcileStrandedFPPCommands' own job, simulated here since this
// package does not import that function — to stand in for "the command-side
// reconciler already ran and found this command stranded."
//
// Before the fix, Reconcile blanket-marked every unresolved step "skipped",
// including step 1, discarding the fact that it had dispatched and erasing
// its command_id. After the fix, step 1 must read resolved with its
// command's own recorded outcome, never skipped.
func TestReconcileDoesNotRecordADispatchedStepAsSkipped(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putAction(t, st, "a2", fppAction("fpp-main", "setVolume", "none", map[string]any{"volume": int64(42)}))
	putAction(t, st, "a3", fppAction("fpp-main", "pausePlaylist", "none", map[string]any{}))
	putAction(t, st, "a4", fppAction("fpp-main", "resumePlaylist", "none", map[string]any{}))
	putMacro(t, st, "m1", testMacroPayload(
		testStep("s1", "a1"), testStep("s2", "a2"), testStep("s3", "a3"), testStep("s4", "a4"),
	))

	rm, err := e.resolveMacro(context.Background(), "m1")
	if err != nil {
		t.Fatalf("resolveMacro: %v", err)
	}
	steps := buildStepRecords(rm)
	run, _, err := e.store.CreateMacroRun(context.Background(), storeRunRecord("stranded-dispatched", "m1", rm.Revision), steps)
	if err != nil {
		t.Fatalf("create stranded run: %v", err)
	}

	// Step 0 (s1) resolved normally before the kill.
	state := stepStateResolved
	outcome := outcomeConfirmed
	outcomeState := "current"
	outcomeReason := "confirmed before the restart"
	resolvedAt := time.Now()
	if err := st.UpdateMacroRunStepOutcome(context.Background(), run.ID, 0, store.MacroRunStepOutcomeUpdate{
		State: &state, DispatchedAt: &resolvedAt, ResolvedAt: &resolvedAt,
		Outcome: &outcome, OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	}); err != nil {
		t.Fatalf("resolve step 0: %v", err)
	}

	// Step 1 (s2, setVolume) DISPATCHED — its command reached FPP — but
	// never resolved before the kill, and macro_run_steps was never
	// touched for it: it is still exactly as CreateMacroRun left it
	// (state=pending, dispatched_at=NULL, command_id=NULL).
	cmdID := "cmd-stranded-1"
	cmdKey := stepIdempotencyKey(run.ID, 1)
	dispatchedAt := time.Now().Add(-10 * time.Second)
	cmdResolvedAt := time.Now().Add(-5 * time.Second)
	if _, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: cmdID, IdempotencyKey: cmdKey, Action: "fpp.set_volume", TargetKind: "fpp", TargetID: "fpp-main",
		ParamsJSON: `{"volume":42}`, IssuerPrincipalID: "p1", IssuerPrincipalName: "tester", State: "dispatched",
	}); err != nil {
		t.Fatalf("insert stranded command: %v", err)
	}
	// Simulate api.ReconcileStrandedFPPCommands having already resolved
	// this command — the real reason text it writes, verbatim, since the
	// fix must propagate it rather than replace it.
	cmdOutcomeReason := "resolved by startup reconciliation, not by this command's own original request (see this coordinator's " +
		"own log for why: a restart or an abandoned connection left it dispatched but never resolved): no fpp.volume reading has " +
		"arrived since this command was dispatched; the most recent evidence predates dispatch, it cannot confirm this command"
	resultJSON := `{"outcome":"unconfirmed"}`
	cmdOutcomeState := "unknown_age"
	resolvedState := "resolved"
	if err := st.UpdateCommandOutcome(context.Background(), cmdID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, ResolvedAt: &cmdResolvedAt, State: &resolvedState,
		ResultJSON: &resultJSON, OutcomeState: &cmdOutcomeState, OutcomeReason: &cmdOutcomeReason,
	}); err != nil {
		t.Fatalf("resolve stranded command: %v", err)
	}

	// Steps 2 and 3 (s3, s4) never started at all: left exactly as
	// CreateMacroRun wrote them.

	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	_, gotSteps, err := e.store.GetMacroRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get reconciled run: %v", err)
	}

	s1 := gotSteps[1]
	if s1.State == stepStateSkipped || s1.Outcome == outcomeSkipped {
		t.Fatalf("step 1 (which DISPATCHED — its command changed FPP's volume to 42) was recorded as skipped: %+v", s1)
	}
	if s1.CommandID == nil || *s1.CommandID != cmdID {
		t.Fatalf("step 1 CommandID = %v, want %q — the dispatched command must not be discarded", s1.CommandID, cmdID)
	}
	if s1.DispatchedAt == nil {
		t.Fatalf("step 1 DispatchedAt is nil, want the command's own dispatch time — this step DID dispatch")
	}
	if s1.Outcome != outcomeUnconfirmed {
		t.Fatalf("step 1 Outcome = %q, want %q (the command's own resolved outcome)", s1.Outcome, outcomeUnconfirmed)
	}
	if s1.OutcomeReason == "" || s1.OutcomeReason == "the coordinator restarted before this step was dispatched or resolved" {
		t.Fatalf("step 1 OutcomeReason = %q, must reflect the command's own outcome, not the generic never-dispatched reason", s1.OutcomeReason)
	}

	// Steps 2 and 3 genuinely never dispatched (no command row exists for
	// either), so they must still read skipped.
	for _, idx := range []int{2, 3} {
		s := gotSteps[idx]
		if s.Outcome != outcomeSkipped {
			t.Fatalf("step %d Outcome = %q, want %q (no command row exists for it)", idx, s.Outcome, outcomeSkipped)
		}
	}
}

// TestReconcileMQTTStepMidFlightIsNotSkipped is this task's MQTT
// requirement: an MQTT step has no commands-table row the way an FPP step
// does, so the one durable trace a dispatch-in-progress step leaves behind
// is its own DISPATCH audit entry (step_mqtt.go's dispatchMQTTStep writes
// it before ever calling Publish). A step with that entry present dispatched
// — this coordinator cannot tell whether the publish itself reached the
// broker or a response arrived before the crash, so it must read
// unconfirmed with a reason saying exactly that, never "skipped". A step
// with no such entry genuinely never started and must still read skipped.
func TestReconcileMQTTStepMidFlightIsNotSkipped(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})

	expect := config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean}
	putAction(t, st, "a1", mqttAction("home-automation", "none", expect))
	putAction(t, st, "a2", mqttAction("home-automation", "none", expect))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1"), testStep("s2", "a2")))

	rm, err := e.resolveMacro(context.Background(), "m1")
	if err != nil {
		t.Fatalf("resolveMacro: %v", err)
	}
	steps := buildStepRecords(rm)
	run, _, err := e.store.CreateMacroRun(context.Background(), storeRunRecord("stranded-mqtt", "m1", rm.Revision), steps)
	if err != nil {
		t.Fatalf("create stranded run: %v", err)
	}

	// Step 0's dispatch began before the kill: its own DISPATCH audit
	// entry was written (mirroring dispatchMQTTStep's own write, step_mqtt.go),
	// but nothing else about it — whether Publish ever ran, whether a
	// response arrived — was ever recorded, because the process died
	// before dispatchMQTTStep returned and macro_run_steps was never
	// touched for it.
	key := stepIdempotencyKey(run.ID, 0)
	if err := svc.WriteAudit(context.Background(), identity.AuditEntry{
		Timestamp: time.Now(), PrincipalID: "p1", PrincipalName: "tester",
		Action: "mqtt.publish", Target: "home-automation:test/publish", IdempotencyKey: key,
		Kind: identity.AuditDispatch,
	}); err != nil {
		t.Fatalf("write dispatch audit entry: %v", err)
	}

	// Step 1 never started: no audit entry under its own key.

	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	_, gotSteps, err := e.store.GetMacroRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get reconciled run: %v", err)
	}

	s0 := gotSteps[0]
	if s0.Outcome == outcomeSkipped {
		t.Fatalf("step 0 (whose dispatch audit entry proves it started) was recorded as skipped: %+v", s0)
	}
	if s0.DispatchedAt == nil {
		t.Fatalf("step 0 DispatchedAt is nil, want the audit entry's own recorded time")
	}
	if s0.Outcome != outcomeUnconfirmed {
		t.Fatalf("step 0 Outcome = %q, want %q", s0.Outcome, outcomeUnconfirmed)
	}
	if s0.OutcomeReason == "" || s0.OutcomeReason == "the coordinator restarted before this step was dispatched or resolved" {
		t.Fatalf("step 0 OutcomeReason = %q, must state the genuine uncertainty, not the generic never-dispatched reason", s0.OutcomeReason)
	}

	s1 := gotSteps[1]
	if s1.Outcome != outcomeSkipped {
		t.Fatalf("step 1 Outcome = %q, want %q (no dispatch audit entry exists for it)", s1.Outcome, outcomeSkipped)
	}
}

// TestRunExecutesWithAShowThatDoesNotExist is E7-3's own ADR-009 proof at
// the run layer: a macro's "show" naming no configured show object must
// still run — existence is a write-time gate on the config package's own
// Decode functions, never re-checked here. testMacroPayload's "test-show"
// is never created as a show config object anywhere in this package's test
// harness, so every other test in this file already exercises this path
// implicitly; this test names the property directly.
func TestRunExecutesWithAShowThatDoesNotExist(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	dispatch := &fakeDispatcher{dispatchFn: func(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
		now := time.Now()
		return api.FPPCommandOutcome{
			CommandID: "cmd-1", Outcome: "confirmed", OutcomeState: "confirmed", OutcomeReason: "observed state moved",
			DispatchedAt: ptrTime(now), ResolvedAt: ptrTime(now),
		}, nil, nil
	}}
	e, _ := newTestExecutor(t, st, svc, dispatch, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	run := submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k-no-show", Trigger: "api", Issuer: testIssuer()})
	if run.Run.Completed == nil || !*run.Run.Completed {
		t.Fatalf("Completed = %v, want true", run.Run.Completed)
	}
	if dispatch.callCount() != 1 {
		t.Fatalf("dispatch called %d times, want 1 (a nonexistent show must not block the run)", dispatch.callCount())
	}
}

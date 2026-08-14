package macro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// TestPerStepAuditExemptionMQTT proves this package's OWN per-step audit
// exemption logic (dispatchMQTTStep, step_mqtt.go) against a REAL
// unwritable audit_log (installFailAuditTrigger — the same SQLite trigger
// internal/coordinator/api/config_test.go uses for the identical class of
// proof, never a mock).
//
// This is the MQTT integration's version of this proof rather than FPP's:
// an FPP step's per-step exemption lives entirely inside the real
// [api.FPPCommandDispatcher] seam this package was handed (see
// TestPerStepAuditExemptionRealDispatchSeam below for that one, which is
// what actually proves the FPP case — a fake fppDispatcher cannot exercise
// it, since the fake never consults identity.Service at all). The MQTT
// integration has no equivalent pre-built seam (step_mqtt.go's own top
// comment: "there is no pre-existing in-process dispatch seam that already
// writes an audit entry on this package's behalf"), so this package's own
// dispatchMQTTStep IS the code the per-step rule lives in for that
// integration, and this test is what proves it rather than merely
// asserting it against a fake.
//
// A run of [exempt-mqtt-stop, non-exempt-mqtt-start] with the audit store
// unwritable from before dispatch: the stop step still publishes
// (degraded attribution), the start step is refused before it ever
// publishes, and the run aborts with a reason naming the audit store.
func TestPerStepAuditExemptionMQTT(t *testing.T) {
	st, svc, storeDir := newTestStoreAndIdentity(t, time.Now)

	var published []string
	brokers := &fakeBrokers{publishFn: func(ctx context.Context, id, topic string, qos byte, retain bool, payload []byte) error {
		published = append(published, topic)
		return nil
	}}
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, brokers)

	// "stop" is exempt (ADR-024 decision 11); "none" is not.
	stopExpect := config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNone}
	startExpect := config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNone}
	putAction(t, st, "a-stop", mqttAction("home-automation", config.ShowSafetyClassStop, stopExpect))
	putAction(t, st, "a-start", mqttAction("home-automation", config.ShowSafetyClassNone, startExpect))
	putMacro(t, st, "m1", testMacroPayload(testStep("stop", "a-stop"), testStep("start", "a-start")))

	// Resolve and create the run exactly as SubmitRun does, but call
	// executeRun SYNCHRONOUSLY (in this goroutine, not backgrounded)
	// rather than going through SubmitRun's own "go e.executeRun(...)":
	// this makes "the audit store becomes unwritable strictly between
	// submission and the run's first step" a deterministic ordering this
	// test controls directly, rather than a race between this test
	// goroutine and a background one with nothing to synchronize on
	// between them.
	ctx := context.Background()
	rm, err := e.resolveMacro(ctx, "m1")
	if err != nil {
		t.Fatalf("resolveMacro: %v", err)
	}
	steps := buildStepRecords(rm)
	classes := stepSafetyClasses(steps)
	if api.FPPCommandAllStepsExempt(classes...) {
		t.Fatalf("test setup error: this run must NOT be all-exempt, or it would be refused at submission rather than reaching the mid-run rule this test exercises")
	}
	run, createdSteps, err := e.store.CreateMacroRun(ctx, storeRunRecord("mqtt-exempt-run", "m1", rm.Revision), steps)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	installFailAuditTrigger(t, storeDir)

	e.wg.Add(1)
	e.executeRun(ctx, rm, run, createdSteps, testIssuer())

	got, err := e.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}

	if got.Run.Completed == nil || *got.Run.Completed {
		t.Fatalf("Completed = %v, want false (the start step must have been refused and aborted the run)", got.Run.Completed)
	}
	if got.Run.Reason == "" {
		t.Fatalf("Reason is empty; must name the audit store")
	}
	if !got.Steps[0].AttributionDegraded {
		t.Fatalf("stop step (exempt) AttributionDegraded = false, want true — it should still have published, degraded")
	}
	if got.Steps[1].Outcome != outcomeFailed {
		t.Fatalf("start step (not exempt) outcome = %q, want %q", got.Steps[1].Outcome, outcomeFailed)
	}
	if len(published) != 1 {
		t.Fatalf("published to %d topics, want exactly 1 (the exempt stop step only; the start step must never publish)", len(published))
	}
}

// The required break-test for the per-step (never run-wide) audit
// exemption rule was performed directly against this file's own
// TestPerStepAuditExemptionMQTT, not left as a standing skipped test:
// step_mqtt.go's `exempt := api.FPPCommandDecision11Exempt(safetyClass)`
// was temporarily replaced with a hardcoded `exempt := true`, simulating
// the run-wide "any step exempt" reading ADR-031 decision 5's rewrite
// removed. TestPerStepAuditExemptionMQTT then failed exactly as expected
// ("Completed = true, want false" — the non-exempt start step was let
// through unaccountably instead of being refused), confirming the test
// actually detects the defect it is named for. The change was then
// reverted and the test re-run to confirm it passes again. See this
// builder's own report for the exact diff and both observed outputs.

// TestPerStepAuditExemptionRealDispatchSeam is the same scenario as above
// but through the REAL api.FPPCommandDispatcher, so the audit-write
// failure this test relies on is the one dispatchFPPCommand's own step 5
// actually hits (fppCommandAuditUnavailableProblem, 503-shaped), never
// this package's own fake standing in for it. This is what makes the
// per-step exemption a proof about the SEAM this package was handed,
// not only about this package's own control flow.
func TestPerStepAuditExemptionRealDispatchSeam(t *testing.T) {
	st, svc, storeDir := newTestStoreAndIdentity(t, time.Now)

	// A stub that accepts every command, rather than the closed port an
	// earlier version of this test pointed at. The closed port made the
	// stop step fail at the transport, and a transport failure is a
	// FAILED step (step_fpp.go, on api.FPPCommandOutcome.DispatchFailed),
	// which aborts the run under ADR-031 decision 2's default and skips
	// the start step this test exists to assert on. That would have made
	// the test pass or fail on the wrong axis entirely: the audit
	// exemption is about whether a step is REFUSED, never about whether
	// its host answered.
	fppStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"OK"}`))
	}))
	t.Cleanup(fppStub.Close)

	fppView := &fakeFPPLister{views: []api.FPPInstanceView{{InstanceID: "fpp-main", Endpoint: fppStub.URL}}}
	realDispatch := api.NewFPPCommandDispatcher(api.Dependencies{
		FPP:      fppView,
		Commands: st,
		Identity: svc,
	}, api.Options{
		Logger: testLogger(),
		// The stop step will never confirm here: nothing feeds this
		// store an observation. Keep that wait short, since this test
		// is about the audit exemption and not about confirmation.
		FPPCommandConfirmDeadline: 50 * time.Millisecond,
		FPPCommandPollInterval:    10 * time.Millisecond,
	})

	e, _ := newTestExecutor(t, st, svc, realDispatch, &fakeBrokers{})

	putAction(t, st, "a-stop", fppAction("fpp-main", "stopPlaylist", config.ShowSafetyClassStop, nil))
	putAction(t, st, "a-start", fppAction("fpp-main", "startPlaylist", config.ShowSafetyClassNone, map[string]any{"playlist": "Main"}))
	putMacro(t, st, "m1", testMacroPayload(testStep("stop", "a-stop"), testStep("start", "a-start")))

	// As in TestPerStepAuditExemptionMQTT: create the run, THEN install
	// the trigger, THEN execute synchronously (never through SubmitRun's
	// own "go e.executeRun(...)"), so "audit healthy at submission,
	// broken strictly before the first step" is this test's own
	// deterministic ordering rather than a race against a background
	// goroutine — observed directly: without this, the real dispatch
	// seam against a closed-port FPP endpoint resolves fast enough that
	// the background goroutine's own dispatch of the stop step routinely
	// wins the race against this test's own trigger-install connection
	// setup, making the assertion below flaky rather than reliable.
	ctx := context.Background()
	rm, err := e.resolveMacro(ctx, "m1")
	if err != nil {
		t.Fatalf("resolveMacro: %v", err)
	}
	steps := buildStepRecords(rm)
	run, createdSteps, err := e.store.CreateMacroRun(ctx, storeRunRecord("real-seam-run", "m1", rm.Revision), steps)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	installFailAuditTrigger(t, storeDir)

	e.wg.Add(1)
	e.executeRun(ctx, rm, run, createdSteps, testIssuer())

	got, err := e.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Run.Completed == nil || *got.Run.Completed {
		t.Fatalf("Completed = %v, want false", got.Run.Completed)
	}
	if got.Steps[0].AttributionDegraded == false {
		t.Fatalf("stop step AttributionDegraded = false, want true (it should still have dispatched, degraded)")
	}
	if got.Steps[0].Outcome == outcomeFailed {
		t.Fatalf("stop step outcome = %q, want anything but failed: the exempt step must still dispatch when the audit store is unwritable", got.Steps[0].Outcome)
	}
	if got.Steps[1].Outcome != outcomeFailed {
		t.Fatalf("start step outcome = %q, want %q", got.Steps[1].Outcome, outcomeFailed)
	}
}

// fakeFPPLister is the one double api.NewFPPCommandDispatcher needs beyond
// the real store/identity.Service this test already has (Commands and
// Identity are satisfied directly by *store.Store and identity.Service —
// interfaces.go's own "no adapter needed" precedent). Everything else in
// api.Dependencies defaults ([Dependencies.withDefaults]).

type fakeFPPLister struct {
	views []api.FPPInstanceView
}

func (f *fakeFPPLister) ListInstances(ctx context.Context) ([]api.FPPInstanceView, error) {
	return f.views, nil
}

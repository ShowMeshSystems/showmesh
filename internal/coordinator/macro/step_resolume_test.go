package macro

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// fakeResolumeActions is this package's own resolumeActionDispatcher fake,
// mirroring fakeDispatcher/fakeBrokers (testing_test.go) one file over.
type fakeResolumeActions struct {
	mu    sync.Mutex
	calls []string // action names, in dispatch order

	dispatchFn func(ctx context.Context, action string, params map[string]any, now time.Time) (api.ResolumeActionResult, error)
}

func (f *fakeResolumeActions) Dispatch(ctx context.Context, action string, params map[string]any, now time.Time) (api.ResolumeActionResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, action)
	f.mu.Unlock()
	if f.dispatchFn != nil {
		return f.dispatchFn(ctx, action, params, now)
	}
	t := time.Now()
	return api.ResolumeActionResult{
		Outcome: api.ResolumeOutcomeConfirmed, Reason: "test evidence, collected after dispatch, confirmed the clip connected",
		Dispatched: true, DispatchedAt: &t, ResolvedAt: &t,
	}, nil
}

func (f *fakeResolumeActions) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// resolumeAction builds the smallest valid show.action payload this
// package's own decode expects for a resolume target, mirroring
// fppAction/mqttAction (testing_test.go).
func resolumeAction(action string, ref map[string]any, safetyClass string) config.ShowActionPayload {
	return config.ShowActionPayload{
		Show: "test-show", Label: "test resolume action", SafetyClass: safetyClass,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationResolume,
			Action:      action, Ref: ref,
		},
	}
}

// newTestExecutorWithResolume is newTestExecutor plus a resolumeActionDispatcher.
// Setting Executor.resolumeActions directly (this file is package macro,
// not macro_test) avoids growing every existing newTestExecutor call site
// with a fifth argument nothing but this file's own tests need.
func newTestExecutorWithResolume(t *testing.T, dispatch fppDispatcher, brokers mqttRegistry, resolumeActions resolumeActionDispatcher) (*Executor, *store.Store, string) {
	t.Helper()
	st, svc, storeDir := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, dispatch, brokers)
	e.resolumeActions = resolumeActions
	return e, st, storeDir
}

// TestMacroRunDispatchesResolumeStepThroughSameDispatcher is acceptance
// criterion 6: a macro run dispatches a Resolume step through the same
// [api.ResolumeActionDispatcher] the HTTP handler uses, and the step's
// outcome carries D-3's own outcome vocabulary — confirmed when the fake
// reports the clip connected on evidence post-dating dispatch, and
// unconfirmable when it does not.
//
// Broken and confirmed to fail: changed dispatchResolumeStep to call
// e.dispatch.Dispatch (the FPP seam) instead of e.resolumeActions.Dispatch —
// this test's fake.callCount() assertion failed (0 calls reached the
// resolume fake), confirming the assertion is load-bearing on the dispatch
// path rather than on the outcome shape alone. Restored afterward.
func TestMacroRunDispatchesResolumeStepThroughSameDispatcher(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		fake := &fakeResolumeActions{}
		e, st, _ := newTestExecutorWithResolume(t, &fakeDispatcher{}, &fakeBrokers{}, fake)
		putAction(t, st, "a1", resolumeAction(config.ShowActionResolumeLaunchClip,
			map[string]any{"clip": "Whole House 1", "deck": "Main"}, config.ShowSafetyClassNone))
		putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

		got := submitAndWait(t, e, api.MacroSubmitRequest{
			MacroObjectID: "m1", IdempotencyKey: "key-confirmed", Trigger: "api", Issuer: testIssuer(),
		})
		if fake.callCount() != 1 {
			t.Fatalf("resolume dispatcher called %d times, want 1", fake.callCount())
		}
		if fake.calls[0] != config.ShowActionResolumeLaunchClip {
			t.Fatalf("dispatched action = %q, want %q", fake.calls[0], config.ShowActionResolumeLaunchClip)
		}
		if got.Steps[0].Outcome != outcomeConfirmed {
			t.Fatalf("step outcome = %q, want %q: %+v", got.Steps[0].Outcome, outcomeConfirmed, got.Steps[0])
		}
		if got.Run.Confirmed == nil || !*got.Run.Confirmed {
			t.Fatalf("run Confirmed = %v, want true", got.Run.Confirmed)
		}
	})

	t.Run("unconfirmable", func(t *testing.T) {
		fake := &fakeResolumeActions{dispatchFn: func(ctx context.Context, action string, params map[string]any, now time.Time) (api.ResolumeActionResult, error) {
			d := time.Now()
			return api.ResolumeActionResult{
				Outcome:      api.ResolumeOutcomeUnconfirmable,
				Reason:       "the pre-dispatch baseline could not be read, so post-dispatch evidence cannot distinguish this action's effect from the state it found",
				Dispatched:   true,
				DispatchedAt: &d,
			}, nil
		}}
		e, st, _ := newTestExecutorWithResolume(t, &fakeDispatcher{}, &fakeBrokers{}, fake)
		putAction(t, st, "a1", resolumeAction(config.ShowActionResolumeClearLayer,
			map[string]any{"layer": "Whole House 1"}, config.ShowSafetyClassBlackout))
		putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

		got := submitAndWait(t, e, api.MacroSubmitRequest{
			MacroObjectID: "m1", IdempotencyKey: "key-unconfirmable", Trigger: "api", Issuer: testIssuer(),
		})
		if got.Steps[0].Outcome != outcomeUnconfirmable {
			t.Fatalf("step outcome = %q, want %q: %+v", got.Steps[0].Outcome, outcomeUnconfirmable, got.Steps[0])
		}
		if got.Run.Confirmed == nil || *got.Run.Confirmed {
			t.Fatalf("run Confirmed = %v, want false", got.Run.Confirmed)
		}
	})
}

// TestMacroRunResolumeStepRefusedAtRunTimeStillRunsRemainingSteps is
// acceptance criterion 7: a composition re-uploaded so a stored action's
// clip name no longer resolves produces a refused step at run time naming
// the clip, and the run still runs its remaining steps (ADR-035). This
// package dispatches through [api.ResolumeActionDispatcher] unchanged
// (TRACK-D-SEAM-C-MACRO-SPEC.md section 3 rule 2): the fake here stands in
// for that dispatcher reporting the SAME refusal a real one would produce
// after a rename, so this test proves this package's own obligation — the
// run keeps going — rather than re-proving name resolution itself, which
// internal/coordinator/collector/resolume's own tests already cover.
func TestMacroRunResolumeStepRefusedAtRunTimeStillRunsRemainingSteps(t *testing.T) {
	fake := &fakeResolumeActions{dispatchFn: func(ctx context.Context, action string, params map[string]any, now time.Time) (api.ResolumeActionResult, error) {
		if action == config.ShowActionResolumeLaunchClip {
			return api.ResolumeActionResult{
				Outcome:    api.ResolumeOutcomeRefused,
				Reason:     `the clip named "Whole House 1" is not in the current composition`,
				Dispatched: false,
			}, nil
		}
		d := time.Now()
		return api.ResolumeActionResult{
			Outcome: api.ResolumeOutcomeConfirmed, Reason: "confirmed",
			Dispatched: true, DispatchedAt: &d, ResolvedAt: &d,
		}, nil
	}}
	e, st, _ := newTestExecutorWithResolume(t, &fakeDispatcher{}, &fakeBrokers{}, fake)

	putAction(t, st, "a-stale", resolumeAction(config.ShowActionResolumeLaunchClip,
		map[string]any{"clip": "Whole House 1", "deck": "Main"}, config.ShowSafetyClassNone))
	putAction(t, st, "a-after", resolumeAction(config.ShowActionResolumeSelectDeck,
		map[string]any{"deck": "Main"}, config.ShowSafetyClassNone))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a-stale"), testStep("s2", "a-after")))

	got := submitAndWait(t, e, api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "key-stale", Trigger: "api", Issuer: testIssuer(),
	})

	if got.Steps[0].Outcome != outcomeFailed {
		t.Fatalf("step 0 outcome = %q, want %q (a refusal maps to this package's own failed outcome)", got.Steps[0].Outcome, outcomeFailed)
	}
	if got.Steps[0].OutcomeReason == "" {
		t.Fatal("step 0 OutcomeReason is empty; the refusal must name the clip")
	}
	if !strings.Contains(got.Steps[0].OutcomeReason, "Whole House 1") {
		t.Fatalf("step 0 OutcomeReason = %q, want it to name %q", got.Steps[0].OutcomeReason, "Whole House 1")
	}
	// ADR-035: a run always runs every step. Step 1 must have dispatched
	// (fake called twice), not be skipped.
	if fake.callCount() != 2 {
		t.Fatalf("resolume dispatcher called %d times, want 2 (the run must not abort after step 0's refusal)", fake.callCount())
	}
	if got.Steps[1].Outcome != outcomeConfirmed {
		t.Fatalf("step 1 outcome = %q, want %q: the run must still run its remaining steps", got.Steps[1].Outcome, outcomeConfirmed)
	}
	if got.Run.Completed == nil || *got.Run.Completed {
		t.Fatalf("run Completed = %v, want false (step 0 failed)", got.Run.Completed)
	}
}

// TestMacroRunResolumeBlackoutRunsWithAuditStoreUnwritable is acceptance
// criterion 8: a macro whose only step is a Resolume blackout runs to
// completion with the audit store unwritable, and the step dispatches.
//
// Broken and confirmed to fail: this builder temporarily made
// dispatchResolumeStep return outcomeFailed whenever WriteAudit failed
// (reintroducing the fail-closed inversion ADR-035 exists to forbid) —
// this test's fake.callCount() assertion dropped to 0 and the step outcome
// assertion failed, confirming both are load-bearing. Restored afterward.
func TestMacroRunResolumeBlackoutRunsWithAuditStoreUnwritable(t *testing.T) {
	fake := &fakeResolumeActions{}
	e, st, storeDir := newTestExecutorWithResolume(t, &fakeDispatcher{}, &fakeBrokers{}, fake)

	putAction(t, st, "a-blackout", resolumeAction(config.ShowActionResolumeBlackout, map[string]any{}, config.ShowSafetyClassBlackout))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a-blackout")))

	rm, err := e.resolveMacro(context.Background(), "m1")
	if err != nil {
		t.Fatalf("resolveMacro: %v", err)
	}
	steps := buildStepRecords(rm)
	run, createdSteps, err := e.store.CreateMacroRun(context.Background(), storeRunRecord("blackout-audit-down", "m1", rm.Revision), steps)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	installFailAuditTrigger(t, storeDir)

	e.wg.Add(1)
	e.executeRun(context.Background(), rm, run, createdSteps, testIssuer())

	got, err := e.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if fake.callCount() != 1 {
		t.Fatalf("resolume dispatcher called %d times, want 1: blackout must dispatch even though the audit store is unwritable", fake.callCount())
	}
	if got.Steps[0].Outcome == outcomeFailed {
		t.Fatalf("step outcome = %q, want anything but failed: blackout must never be refused for want of an audit write", got.Steps[0].Outcome)
	}
	if !got.Steps[0].AttributionDegraded {
		t.Fatal("step AttributionDegraded = false, want true: it dispatched without a durable audit entry")
	}
	if !got.Run.AttributionDegraded {
		t.Fatal("run AttributionDegraded = false, want true")
	}
}

// TestDispatchStepDefaultArmStillBehavesForUnrecognizedIntegration is
// acceptance criterion 13: the default arm of run.go's dispatchStep
// integration switch still behaves as before for an unrecognized
// integration, after adding the resolume case alongside it.
func TestDispatchStepDefaultArmStillBehavesForUnrecognizedIntegration(t *testing.T) {
	e, _, _ := newTestExecutorWithResolume(t, &fakeDispatcher{}, &fakeBrokers{}, &fakeResolumeActions{})
	action := resolvedAction{
		ObjectID: "a-future",
		Payload: config.ShowActionPayload{
			Show: "test-show", Label: "future integration", SafetyClass: config.ShowSafetyClassNone,
			Target: config.ShowActionTarget{Integration: "osc"},
		},
	}
	res := e.dispatchStep(context.Background(), store.MacroRunRecord{}, store.MacroRunStepRecord{}, action, testIssuer())
	if res.outcome != outcomeFailed {
		t.Fatalf("outcome = %q, want %q", res.outcome, outcomeFailed)
	}
	if res.outcomeState != "unknown_integration" {
		t.Fatalf("outcomeState = %q, want %q", res.outcomeState, "unknown_integration")
	}
	if !strings.Contains(res.outcomeReason, "osc") {
		t.Fatalf("outcomeReason = %q, want it to name the unrecognized integration %q", res.outcomeReason, "osc")
	}
}

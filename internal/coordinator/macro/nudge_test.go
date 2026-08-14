package macro

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// nudgeDelayBudget is this test's own tolerance for "dispatch was not
// delayed" — generous enough to absorb ordinary scheduling jitter and a
// slow CI runner, tiny next to the 2-second limiter window this test
// deliberately sets NextNudgeAt to. If dispatch were waiting on the
// reservation, the observed interval would be on the order of seconds,
// not milliseconds; this budget exists only to reject a real regression,
// not to pin an exact number.
const nudgeDelayBudget = 200 * time.Millisecond

// TestDispatchNotDelayedByNudgeReservation is acceptance criterion 18 and
// this task's own required break-test 7 (see this file's own
// TestBreak_ documentation below for what was changed to prove it fails).
//
// A fake fppDispatcher whose NextNudgeAt reports a reservation 2 seconds
// in the future (as if the collector's post-nudge rate limit had just been
// hit by an earlier step) must not change how quickly dispatchFPPStep
// calls Dispatch for the NEXT step. This measures the interval between
// "the executor decides to dispatch a step" (immediately before calling
// dispatchStep) and "the command left" (Dispatch's own entry), across a
// four-step same-host macro — STEP-9-SPEC.md section 6.3's own acceptance
// criterion 18, restated as a runnable test rather than left as a comment.
func TestDispatchNotDelayedByNudgeReservation(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)

	var dispatchedAt []time.Time
	dispatch := &fakeDispatcher{
		nextNudgeAt: time.Now().Add(2 * time.Second),
		nextNudgeOK: true,
		dispatchFn: func(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
			dispatchedAt = append(dispatchedAt, time.Now())
			now := time.Now()
			return api.FPPCommandOutcome{
				CommandID: "cmd-" + in.IdempotencyKey, Outcome: "confirmed", OutcomeState: "current", OutcomeReason: "ok",
				DispatchedAt: ptrTime(now), ResolvedAt: ptrTime(now),
			}, nil, nil
		},
	}
	e, _ := newTestExecutor(t, st, svc, dispatch, &fakeBrokers{})

	putAction(t, st, "a1", fppAction("fpp-main", "startPlaylist", "none", map[string]any{"playlist": "Main"}))
	putAction(t, st, "a2", fppAction("fpp-main", "setVolume", "none", map[string]any{"volume": int64(50)}))
	putAction(t, st, "a3", fppAction("fpp-main", "nextPlaylistItem", "none", nil))
	putAction(t, st, "a4", fppAction("fpp-main", "pausePlaylist", "none", nil))
	putMacro(t, st, "m1", testMacroPayload(
		testStep("s1", "a1"), testStep("s2", "a2"), testStep("s3", "a3"), testStep("s4", "a4")))

	decidedAt := time.Now()
	submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if len(dispatchedAt) != 4 {
		t.Fatalf("dispatched %d steps, want 4", len(dispatchedAt))
	}

	if gap := dispatchedAt[0].Sub(decidedAt); gap > nudgeDelayBudget {
		t.Fatalf("first dispatch took %v after submission was decided, want under %v (a NextNudgeAt reservation must never delay dispatch)", gap, nudgeDelayBudget)
	}
	for i := 1; i < len(dispatchedAt); i++ {
		if gap := dispatchedAt[i].Sub(dispatchedAt[i-1]); gap > nudgeDelayBudget {
			t.Fatalf("step %d dispatched %v after step %d, want under %v — dispatch must not grow with the limiter window (2s) NextNudgeAt reported",
				i, gap, i-1, nudgeDelayBudget)
		}
	}
}

// The required break-test for this rule was performed directly against
// TestDispatchNotDelayedByNudgeReservation above, not left as a standing
// skipped test: step_fpp.go's dispatchFPPStep temporarily gained
// `if at, ok := e.dispatch.NextNudgeAt(target.InstanceID); ok { time.Sleep(time.Until(at)) }`
// immediately before the `Dispatch` call. With NextNudgeAt reporting a
// reservation 2 seconds out, the test failed exactly as expected ("first
// dispatch took 1.9987785s ... want under 200ms"), confirming the test
// actually catches a dispatch-delaying regression. The change was then
// reverted and the test re-run to confirm it passes again. See this
// builder's own report for the exact diff and both observed outputs.

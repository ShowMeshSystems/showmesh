package macro

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// TestFPPTransportFailureIsFailedNotUnconfirmed pins the distinction
// ADR-031 decision 2 rests on, at the one place the dispatch seam cannot
// express it.
//
// api.FPPCommandOutcome.Outcome is only ever "confirmed" or "unconfirmed",
// so a powered-off host arrives looking exactly like a command FPP
// accepted and whose effect no evidence reached us about. Those go onto
// two different policy axes: a host that never answered is a failure of
// the show, and it stops the run; a missing observation is a gap in our
// own evidence pipeline, and it must never stop a show.
//
// Before this branch existed, a macro against a dead host dispatched
// nothing and the run reported completed: true, which is the exact
// opposite of what "every step dispatched" means.
func TestFPPTransportFailureIsFailedNotUnconfirmed(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	putAction(t, st, "a-stop", fppAction("fpp-main", "stopPlaylist", "stop", nil))
	putAction(t, st, "a-pause", fppAction("fpp-main", "pausePlaylist", "none", nil))
	putMacro(t, st, "m", testMacroPayload(testStep("stop", "a-stop"), testStep("pause", "a-pause")))

	fd := &fakeDispatcher{dispatchFn: func(_ context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
		now := time.Now()
		return api.FPPCommandOutcome{
			CommandID: "cmd-" + in.IdempotencyKey, Action: in.Action, InstanceID: in.InstanceID,
			Outcome:        "unconfirmed",
			OutcomeState:   "collection_failed",
			OutcomeReason:  `dispatching to FPP failed: fppcommand: dispatching "Stop Now": Post "http://10.0.0.9/api/command": dial tcp: connect: connection refused`,
			DispatchedAt:   ptrTime(now),
			ResolvedAt:     ptrTime(now),
			DispatchFailed: true,
		}, nil, nil
	}}

	e, _ := newTestExecutor(t, st, svc, fd, nil)
	res, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m", IdempotencyKey: "k", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil || problem != nil {
		t.Fatalf("SubmitRun() = problem %v, err %v, want neither", problem, err)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	got, err := e.GetRun(context.Background(), res.Run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if got.Steps[0].Outcome != outcomeFailed {
		t.Errorf("step 0 outcome = %q, want %q: the request never reached the host", got.Steps[0].Outcome, outcomeFailed)
	}
	if got.Run.Completed == nil || *got.Run.Completed {
		t.Errorf("run Completed = %v, want false: nothing was dispatched, so \"every step dispatched\" is false", got.Run.Completed)
	}
	if got.Steps[1].Outcome != outcomeSkipped {
		t.Errorf("step 1 outcome = %q, want %q: a failed step aborts the remainder by default", got.Steps[1].Outcome, outcomeSkipped)
	}
}

// TestFPPTransportFailureReasonCarriesNoRawGoError checks the operator's
// side of the same branch. The seam's own reason on this path interpolates
// the raw Go error, which carries a package path, an HTTP verb and the
// instance's internal URL. What the operator reads and what a maintainer
// needs are different documents.
func TestFPPTransportFailureReasonCarriesNoRawGoError(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	putAction(t, st, "a-stop", fppAction("fpp-main", "stopPlaylist", "stop", nil))
	putMacro(t, st, "m", testMacroPayload(testStep("stop", "a-stop")))

	fd := &fakeDispatcher{dispatchFn: func(_ context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
		now := time.Now()
		return api.FPPCommandOutcome{
			CommandID: "c1", Action: in.Action, InstanceID: in.InstanceID,
			Outcome:        "unconfirmed",
			OutcomeState:   "collection_failed",
			OutcomeReason:  `dispatching to FPP failed: fppcommand: dispatching "Stop Now": Post "http://10.0.0.9/api/command": dial tcp: connect: connection refused`,
			DispatchedAt:   ptrTime(now),
			ResolvedAt:     ptrTime(now),
			DispatchFailed: true,
		}, nil, nil
	}}

	e, _ := newTestExecutor(t, st, svc, fd, nil)
	res, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m", IdempotencyKey: "k", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil || problem != nil {
		t.Fatalf("SubmitRun() = problem %v, err %v, want neither", problem, err)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	got, err := e.GetRun(context.Background(), res.Run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}

	for _, forbidden := range []string{"fppcommand:", "dial tcp", "http://", "Post "} {
		if strings.Contains(got.Steps[0].OutcomeReason, forbidden) {
			t.Errorf("step reason %q contains %q, which belongs in the log and not in front of an operator", got.Steps[0].OutcomeReason, forbidden)
		}
		if strings.Contains(got.Run.Reason, forbidden) {
			t.Errorf("run reason %q contains %q, which belongs in the log and not in front of an operator", got.Run.Reason, forbidden)
		}
	}
	if !strings.Contains(got.Steps[0].OutcomeReason, "fpp-main") {
		t.Errorf("step reason %q does not name the instance the operator needs to go look at", got.Steps[0].OutcomeReason)
	}
}

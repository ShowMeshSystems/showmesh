package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/weathertrigger"
)

// triggerLoop builds the loop the coordinator runs, on the harness's store.
func (r *resumeHarness) triggerLoop() *WeatherDelayTriggerLoop {
	deps := r.h.deps
	deps.WeatherDelayTrigger = r.st
	return NewWeatherDelayTriggerLoop(deps, Options{Clock: func() time.Time { return r.now }, Logger: testLogger()}, NewWeatherDelayNWSStatus())
}

// askAndPassDeadline raises one question through l and moves the clock past
// its deadline, leaving it for the loop to settle.
func (r *resumeHarness) askAndPassDeadline(l *WeatherDelayTriggerLoop) {
	r.t.Helper()
	ev := weathertrigger.TriggerEvent{Source: "nws", Kind: weathertrigger.KindWarning, EventType: "Tornado Warning"}
	_, pending, message, err := l.h.weatherDelayReceiveTrigger(context.Background(), r.now, ev, weatherDelayTriggerSystemAuthContext(ev.Source), "")
	if err != nil || pending == nil {
		r.t.Fatalf("weatherDelayReceiveTrigger: pending %v, message %q, err %v; want a new question", pending, message, err)
	}
	r.now = pending.Deadline.Add(time.Second)
}

func (r *resumeHarness) pendingDecision() bool {
	r.t.Helper()
	_, ok, err := r.st.GetPendingWeatherDelayDecision(context.Background())
	if err != nil {
		r.t.Fatalf("GetPendingWeatherDelayDecision: %v", err)
	}
	return ok
}

// An unanswered question is settled by the loop's own handlers, which must
// be able to stop an FPP player and wait for it to confirm.
func TestWeatherDelayTriggerLoopStartsADelayAtTheDeadline(t *testing.T) {
	r := newResumeHarness(t)
	l := r.triggerLoop()
	r.askAndPassDeadline(l)

	l.reconcileDeadline(context.Background(), r.now)

	if got := r.state(); !got.Active || got.StartedBy != "nws" {
		t.Fatalf("state = %+v, want an active delay started by nws", got)
	}
	if got := r.stopCommands(); got != 1 {
		t.Fatalf("FPP stop commands = %d, want 1", got)
	}
	if r.pendingDecision() {
		t.Fatal("the question is still pending after its deadline ran")
	}
}

// The start path answers a repeated command key from its stored result
// without sending anything, so each trigger start must carry a key of its own.
func TestWeatherDelayTriggerLoopSendsItsStopsEveryTime(t *testing.T) {
	r := newResumeHarness(t)
	l := r.triggerLoop()

	r.askAndPassDeadline(l)
	l.reconcileDeadline(context.Background(), r.now)
	if got := r.stopCommands(); got != 1 {
		t.Fatalf("FPP stop commands after the first trigger start = %d, want 1", got)
	}
	r.resume()
	if got := r.state(); got.Active {
		t.Fatalf("state after resume = %+v, want no delay", got)
	}

	r.askAndPassDeadline(l)
	l.reconcileDeadline(context.Background(), r.now)
	if got := r.state(); !got.Active {
		t.Fatalf("state = %+v, want the second delay active", got)
	}
	if got := r.stopCommands(); got != 2 {
		t.Fatalf("FPP stop commands after the second trigger start = %d, want 2: the second start's stop was not sent", got)
	}
}

// By the time a trigger start reaches a player, the delay is saved and the
// question is gone, so a coordinator that dies there restarts into the delay
// and never asks or starts again.
func TestWeatherDelayTriggerStartIsSavedBeforeAnyPlayerIsStopped(t *testing.T) {
	r := newResumeHarness(t)
	l := r.triggerLoop()
	r.askAndPassDeadline(l)

	var savedAtStop, pendingAtStop bool
	r.onStop = func() {
		savedAtStop, pendingAtStop = r.state().Active, r.pendingDecision()
	}
	l.reconcileDeadline(context.Background(), r.now)
	if !savedAtStop || pendingAtStop {
		t.Fatalf("when the stop reached the player: delay saved %v, question pending %v; want saved and not pending", savedAtStop, pendingAtStop)
	}

	restarted := r.triggerLoop()
	restarted.reconcileDeadline(context.Background(), r.now)
	if got := r.stopCommands(); got != 1 {
		t.Fatalf("FPP stop commands after a restarted loop's pass = %d, want 1: the restart started the delay again", got)
	}
	if got := r.state(); !got.Active || got.Revision != 1 {
		t.Fatalf("state after a restarted loop's pass = %+v, want the same delay still active", got)
	}
}

// A cue's show action aimed at an FPP player is sent by the cue loop's own
// handlers, which must be able to wait for the player to confirm.
func TestCueActivationLoopHandlersCanConfirmAnFPPCommand(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, 200, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	l := NewCueActivationLoop(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 50 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	if _, problem, err := l.h.dispatchFPPCommand(context.Background(), testNow, FPPCommandInput{
		InstanceID: "bench-fpp", Action: "stopPlaylist", IdempotencyKey: "cue-loop-confirm",
		Issuer: FPPCommandIssuer{PrincipalID: "system:test"},
	}); problem != nil || err != nil {
		t.Fatalf("dispatch: problem %+v, err %v", problem, err)
	}
}

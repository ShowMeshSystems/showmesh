package pipeline

import (
	"context"
	"testing"
	"time"
)

func testSpec(surfaceID string) Spec {
	return Spec{
		SurfaceID: surfaceID,
		Stages: []Stage{
			{Label: "source", Elements: []Element{{Factory: "fakesrc"}}},
			{Label: "sink", Elements: []Element{{Factory: "fakesink"}}},
		},
	}
}

// shutdownSupervisor registers sup.Shutdown as test cleanup, so every
// runner's control-loop goroutine actually exits at the end of each test —
// without this, a goroutine left running past its test's return can read
// [restartPolicy] fields or fake-starter state concurrently with a LATER
// test mutating them, which is exactly the data race this file's tests hit
// before restartPolicy was moved off shared package-level vars.
func shutdownSupervisor(t *testing.T, sup *Supervisor) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})
}

// TestSupervisorApplyReachesRunning proves the ordinary path: Apply starts
// the fake process, which reports its running marker, and Snapshot reflects
// StateRunning.
func TestSupervisorApplyReachesRunning(t *testing.T) {
	clock := newFakeClock(time.Now())
	fs := &fakeStarter{}
	sup := NewSupervisor(clock.Now, fs.Start, testLogger{})
	shutdownSupervisor(t, sup)

	if err := sup.Apply(testSpec("s1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snap, ok := sup.AwaitState(ctx, "s1", []State{StateRunning}, time.Time{}, 5*time.Millisecond)
	if !ok {
		t.Fatalf("did not observe StateRunning; last snapshot: %+v", snap)
	}
	if fs.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1 (proves Apply actually invoked the injected starter)", fs.callCount())
	}
}

// TestSupervisorClearStopsAndDoesNotRestart proves a commanded Clear does
// not trigger the crash-restart path — the seam's core "distinguish a
// crash from a commanded stop" requirement. Mutation check: removing
// stopCurrent's synchronous drain (making Clear's kill visible to the
// crash-handling branch instead) makes this test fail, confirmed by hand.
func TestSupervisorClearStopsAndDoesNotRestart(t *testing.T) {
	clock := newFakeClock(time.Now())
	fs := &fakeStarter{}
	sup := NewSupervisor(clock.Now, fs.Start, testLogger{})
	shutdownSupervisor(t, sup)

	if err := sup.Apply(testSpec("s1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, ok := sup.AwaitState(ctx, "s1", []State{StateRunning}, time.Time{}, 5*time.Millisecond); !ok {
		t.Fatalf("never reached running before clearing")
	}

	if err := sup.Clear("s1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	snap, ok := sup.AwaitState(ctx2, "s1", []State{StateStopped}, time.Time{}, 5*time.Millisecond)
	if !ok {
		t.Fatalf("did not observe StateStopped after Clear; last snapshot: %+v", snap)
	}

	// Give the (absent) restart path a chance to have fired, if it were
	// going to.
	time.Sleep(50 * time.Millisecond)
	callsAfter := fs.callCount()
	if callsAfter != 1 {
		t.Fatalf("callCount after Clear = %d, want 1 (a commanded stop must never trigger a restart)", callsAfter)
	}
	snap, _ = sup.Snapshot("s1")
	if snap.State != StateStopped {
		t.Fatalf("state after settling = %q, want %q", snap.State, StateStopped)
	}
}

// TestSupervisorCrashTriggersRestart proves the opposite of the Clear test:
// an unrequested exit IS treated as a crash and does trigger a restart.
func TestSupervisorCrashTriggersRestart(t *testing.T) {
	clock := newFakeClock(time.Now())
	var procs []*fakeProcess
	fs := &fakeStarter{}
	sup := NewSupervisor(clock.Now, func(ctx context.Context, path string, args []string, onRunningMarker func()) (ProcessHandle, error) {
		h, err := fs.Start(ctx, path, args, onRunningMarker)
		procs = append(procs, h.(*fakeProcess))
		return h, err
	}, testLogger{})
	shutdownSupervisor(t, sup)

	if err := sup.Apply(testSpec("s1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, ok := sup.AwaitState(ctx, "s1", []State{StateRunning}, time.Time{}, 5*time.Millisecond); !ok {
		t.Fatalf("never reached running")
	}

	// Advance the clock past the fast-failure window so this crash is
	// treated as a normal restart, not a fast failure, and simulate a
	// crash by exiting the underlying fake process directly (never
	// through Supervisor.Clear).
	clock.Advance(defaultRestartPolicy.fastFailureWindow * 2)
	code := 1
	procs[0].exitNow(ExitResult{ExitCode: &code, StderrTail: "boom"})

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if _, ok := sup.AwaitState(ctx2, "s1", []State{StateRestarting}, time.Time{}, 5*time.Millisecond); !ok {
		t.Fatalf("crash did not transition to StateRestarting")
	}

	snap, _ := sup.Snapshot("s1")
	if snap.RestartCount != 1 {
		t.Fatalf("RestartCount = %d, want 1", snap.RestartCount)
	}
	if snap.LastExitCode == nil || *snap.LastExitCode != 1 {
		t.Fatalf("LastExitCode = %v, want 1", snap.LastExitCode)
	}
}

// fastTestPolicy shrinks every restartPolicy duration so backoff- and
// lockout-driven tests run in milliseconds instead of seconds, without
// touching any package-level state (see newSupervisorWithPolicy's doc
// comment on why this replaced mutating shared vars).
var fastTestPolicy = restartPolicy{
	fastFailureWindow:          3 * time.Second, // left real-sized; tests control "fast" via the fake clock instead
	maxConsecutiveFastFailures: 5,
	initialBackoff:             5 * time.Millisecond,
	maxBackoff:                 20 * time.Millisecond,
}

// TestSupervisorFastFailureLockout proves the seam's named acceptance
// criterion: repeated immediate crashes stop being retried after
// maxConsecutiveFastFailures and report StateFailed instead of looping
// forever. Mutation check: with maxConsecutiveFastFailures raised
// arbitrarily high (or the lockout check removed), this test fails because
// the callCount bound below is violated — confirmed by hand during
// development.
func TestSupervisorFastFailureLockout(t *testing.T) {
	clock := newFakeClock(time.Now())
	fs := &fakeStarter{}
	fs.onStart = func(p *fakeProcess) {
		// Simulate an instant crash: exit before ever calling
		// onRunningMarker, on its own goroutine so Start can return first
		// (matching how a real process's exit is observed asynchronously).
		code := 1
		go p.exitNow(ExitResult{ExitCode: &code, StderrTail: "immediate crash"})
	}
	sup := newSupervisorWithPolicy(clock.Now, fs.Start, testLogger{}, fastTestPolicy)
	shutdownSupervisor(t, sup)

	if err := sup.Apply(testSpec("s1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snap, ok := sup.AwaitState(ctx, "s1", []State{StateFailed}, time.Time{}, 10*time.Millisecond)
	if !ok {
		t.Fatalf("never reached StateFailed; last snapshot: %+v", snap)
	}
	if snap.ConsecutiveFailures < fastTestPolicy.maxConsecutiveFastFailures {
		t.Fatalf("ConsecutiveFailures = %d, want >= %d", snap.ConsecutiveFailures, fastTestPolicy.maxConsecutiveFastFailures)
	}

	// Give any further (wrongly persisting) restart loop a chance to run;
	// the call count must stay bounded rather than climbing forever.
	time.Sleep(200 * time.Millisecond)
	callsAtFailure := fs.callCount()
	time.Sleep(200 * time.Millisecond)
	if fs.callCount() != callsAtFailure {
		t.Fatalf("callCount grew from %d to %d after entering StateFailed: restart loop did not stop", callsAtFailure, fs.callCount())
	}

	// An explicit restart clears the lockout and tries again.
	fs.onStart = nil // let the next attempt reach StateRunning
	if err := sup.Restart("s1"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if _, ok := sup.AwaitState(ctx2, "s1", []State{StateRunning}, time.Time{}, 5*time.Millisecond); !ok {
		t.Fatalf("explicit Restart after lockout did not reach StateRunning")
	}
}

// TestSupervisorUnsupportedWhenGstLaunchMissing proves an absent binary
// degrades to StateUnsupported rather than erroring or looping, and that it
// does so without ever invoking the process starter.
func TestSupervisorUnsupportedWhenGstLaunchMissing(t *testing.T) {
	oldLook := lookPathFunc
	oldEnv := lookupEnvFunc
	defer func() { lookPathFunc = oldLook; lookupEnvFunc = oldEnv }()
	lookPathFunc = func(string) (string, error) { return "", errNotFoundForTest }
	lookupEnvFunc = func(string) (string, bool) { return "", false }

	clock := newFakeClock(time.Now())
	fs := &fakeStarter{}
	sup := NewSupervisor(clock.Now, fs.Start, testLogger{})
	shutdownSupervisor(t, sup)

	if err := sup.Apply(testSpec("s1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, ok := sup.AwaitState(ctx, "s1", []State{StateUnsupported}, time.Time{}, 5*time.Millisecond); !ok {
		t.Fatalf("did not observe StateUnsupported when gst-launch is absent")
	}
	if fs.callCount() != 0 {
		t.Fatalf("callCount = %d, want 0: an absent binary must never invoke the process starter", fs.callCount())
	}
}

type notFoundErr struct{}

func (notFoundErr) Error() string { return "executable file not found in $PATH" }

var errNotFoundForTest = notFoundErr{}

// TestSupervisorConfirmationEvidencePostDatesDispatch proves the exact
// requirement named in the seam spec: AwaitState only reports found=true
// for a snapshot whose ObservedAt is at or after the dispatch time it was
// given, never off a stale pre-dispatch reading. Mutation check: reverting
// [runner.Snapshot] to stamp ObservedAt from the current clock read (rather
// than returning the stored evidence time) makes this test fail — that
// exact regression was caught by hand while writing this test, before this
// fix was made.
func TestSupervisorConfirmationEvidencePostDatesDispatch(t *testing.T) {
	clock := newFakeClock(time.Now())
	fs := &fakeStarter{}
	sup := NewSupervisor(clock.Now, fs.Start, testLogger{})
	shutdownSupervisor(t, sup)

	// First apply: reaches StateRunning immediately (stale, "old" evidence).
	if err := sup.Apply(testSpec("s1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	staleSnap, ok := sup.AwaitState(ctx, "s1", []State{StateRunning}, time.Time{}, 5*time.Millisecond)
	if !ok {
		t.Fatalf("setup: never reached initial running state")
	}

	// dispatchTime is set to strictly AFTER the stale evidence, with NOTHING
	// yet done to produce fresh evidence: the runner is still sitting in
	// exactly the same StateRunning it was in before, with the SAME
	// ObservedAt from before dispatchTime.
	clock.Advance(time.Hour)
	dispatchTime := clock.Now()

	// The state matches (StateRunning) but the evidence backing it predates
	// dispatchTime, so a bounded poll must time out with found=false rather
	// than accepting the stale match — this is the exact shape of the
	// project's own prior defect (a command "confirmed" 179 microseconds
	// after its own dispatch off a pre-dispatch reading).
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer shortCancel()
	if snap, found := sup.AwaitState(shortCtx, "s1", []State{StateRunning}, dispatchTime, 5*time.Millisecond); found {
		t.Fatalf("AwaitState reported found=true using stale evidence dated %s (before dispatch %s); snapshot: %+v",
			staleSnap.ObservedAt, dispatchTime, snap)
	}

	// Now produce fresh evidence — the equivalent of the pipeline actually
	// reaching PLAYING after a real restart — and confirm the same kind of
	// call succeeds once genuinely fresh evidence exists.
	clock.Advance(time.Second)
	fs.onStart = nil // subsequent starts report running immediately
	if err := sup.Restart("s1"); err != nil {
		t.Fatalf("second Restart: %v", err)
	}
	freshCtx, freshCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer freshCancel()
	freshDispatch := clock.Now()
	snap, found := sup.AwaitState(freshCtx, "s1", []State{StateRunning}, freshDispatch, 5*time.Millisecond)
	if !found {
		t.Fatalf("did not observe fresh running evidence after the second restart")
	}
	if snap.ObservedAt.Before(freshDispatch) {
		t.Fatalf("ObservedAt %s is before dispatch %s: evidence does not post-date dispatch", snap.ObservedAt, freshDispatch)
	}
}

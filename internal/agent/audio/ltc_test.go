package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

func waitForLTCState(t *testing.T, g *LTCGenerator, want LTCGeneratorState) LTCGeneratorSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := g.Snapshot()
		if snap.State == want {
			return snap
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %q, last snapshot: %+v", want, g.Snapshot())
	return LTCGeneratorSnapshot{}
}

// TestStartReachesRunningOnHeartbeat proves the basic happy path: a
// process that emits a heartbeat is reported Running, with the timecode
// from that heartbeat and the frame rate it was started with.
func TestStartReachesRunningOnHeartbeat(t *testing.T) {
	alwaysResolvableLTCGen(t)
	starter := &fakeLTCStarter{
		onStart: func(p *fakeLTCProcess, onHeartbeat func(pkgaudio.LTCTimecode)) {
			go onHeartbeat(pkgaudio.LTCTimecode("00:00:01:00"))
		},
	}
	clock := newFakeLTCClock(time.Unix(1000, 0))
	g := newTestLTCGenerator(clock, starter, fastLTCPolicy())
	defer g.Shutdown(context.Background())

	g.Start(LTCGeneratorSpec{FrameRate: pkgaudio.LTCFrameRate30, StartOffset: "00:00:00:00"})

	snap := waitForLTCState(t, g, LTCGeneratorRunning)
	if !snap.TimecodeKnown || snap.Timecode != "00:00:01:00" {
		t.Errorf("snapshot = %+v, want TimecodeKnown with 00:00:01:00", snap)
	}
	if !snap.FrameRateKnown || snap.FrameRate != pkgaudio.LTCFrameRate30 {
		t.Errorf("snapshot = %+v, want FrameRateKnown 30", snap)
	}
}

// TestNeverStartedReportsStoppedNotRunning proves liveness is never
// assumed: a generator that has never had Start called reports Stopped,
// never Running, and carries no frame rate or timecode evidence.
func TestNeverStartedReportsStoppedNotRunning(t *testing.T) {
	starter := &fakeLTCStarter{}
	clock := newFakeLTCClock(time.Unix(1000, 0))
	g := newTestLTCGenerator(clock, starter, fastLTCPolicy())
	defer g.Shutdown(context.Background())

	snap := g.Snapshot()
	if snap.State != LTCGeneratorStopped {
		t.Errorf("state = %q, want stopped", snap.State)
	}
	if snap.FrameRateKnown || snap.TimecodeKnown {
		t.Errorf("snapshot = %+v, want no frame rate or timecode evidence", snap)
	}
	if starter.callCount() != 0 {
		t.Errorf("starter called %d times before Start, want 0", starter.callCount())
	}
}

// TestHeartbeatLossIsDetectedWithoutProcessExiting is ruling 4's central
// mutation-visible proof: a process that never exits and never emits a
// heartbeat must NOT be reported Running just because it is alive. This
// is the exact case a naive "state = process alive ? running : dead"
// implementation gets wrong — mutating this supervisor to infer Running
// from process liveness alone (deleting the heartbeat-timeout branch, or
// having Start itself set Running) makes this test fail.
func TestHeartbeatLossIsDetectedWithoutProcessExiting(t *testing.T) {
	alwaysResolvableLTCGen(t)
	var live *fakeLTCProcess
	starter := &fakeLTCStarter{
		onStart: func(p *fakeLTCProcess, onHeartbeat func(pkgaudio.LTCTimecode)) {
			live = p // no heartbeat ever fired, and this process never exits on its own
		},
	}
	clock := newFakeLTCClock(time.Unix(1000, 0))
	policy := fastLTCPolicy()
	policy.maxConsecutiveFastFailures = 1000 // isolate heartbeat-loss detection from the lockout
	g := newTestLTCGenerator(clock, starter, policy)
	defer g.Shutdown(context.Background())

	g.Start(LTCGeneratorSpec{FrameRate: pkgaudio.LTCFrameRate25, StartOffset: "00:00:00:00"})

	// The process is alive (never exited) the whole time; only the
	// missing heartbeat should move state off Starting.
	waitForLTCState(t, g, LTCGeneratorRestarting)
	if live == nil {
		t.Fatal("starter.onStart was never invoked")
	}
}

// TestConsecutiveHeartbeatlessFailuresLockOutAsFailed proves the bounded
// restart policy: repeated attempts that never produce a heartbeat reach
// Failed rather than restarting forever.
func TestConsecutiveHeartbeatlessFailuresLockOutAsFailed(t *testing.T) {
	alwaysResolvableLTCGen(t)
	var procs []*fakeLTCProcess
	starter := &fakeLTCStarter{
		onStart: func(p *fakeLTCProcess, onHeartbeat func(pkgaudio.LTCTimecode)) {
			procs = append(procs, p)
			go p.exitNow(LTCExitResult{SawHeartbeat: false})
		},
	}
	clock := newFakeLTCClock(time.Unix(1000, 0))
	g := newTestLTCGenerator(clock, starter, fastLTCPolicy())
	defer g.Shutdown(context.Background())

	g.Start(LTCGeneratorSpec{FrameRate: pkgaudio.LTCFrameRate24, StartOffset: "00:00:00:00"})

	snap := waitForLTCState(t, g, LTCGeneratorFailed)
	if snap.ConsecutiveFailures < 3 {
		t.Errorf("ConsecutiveFailures = %d, want at least 3 (the lockout threshold)", snap.ConsecutiveFailures)
	}
}

// TestAHeartbeatBeforeACrashResetsTheLockoutCounter mirrors pipeline's
// identical rule one level down: an attempt that proved itself alive
// (SawHeartbeat) resets the consecutive-failure counter even though it
// eventually exited, so a pipeline that runs for a while between crashes
// never reaches the lockout the way one that never starts does.
func TestAHeartbeatBeforeACrashResetsTheLockoutCounter(t *testing.T) {
	alwaysResolvableLTCGen(t)
	starter := &fakeLTCStarter{
		onStart: func(p *fakeLTCProcess, onHeartbeat func(pkgaudio.LTCTimecode)) {
			go p.exitNow(LTCExitResult{SawHeartbeat: true})
		},
	}
	clock := newFakeLTCClock(time.Unix(1000, 0))
	g := newTestLTCGenerator(clock, starter, fastLTCPolicy())
	defer g.Shutdown(context.Background())

	g.Start(LTCGeneratorSpec{FrameRate: pkgaudio.LTCFrameRate24, StartOffset: "00:00:00:00"})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		snap := g.Snapshot()
		if snap.State == LTCGeneratorFailed {
			t.Fatalf("reached failed lockout despite every attempt proving a heartbeat: %+v", snap)
		}
		if snap.ConsecutiveFailures != 0 {
			t.Fatalf("ConsecutiveFailures = %d, want 0 (every attempt saw a heartbeat)", snap.ConsecutiveFailures)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestContinuousHeartbeatsNeverExpireTheWatchdog verifies that a
// generator that keeps heartbeating faster than heartbeatTimeout is
// never killed and restarted: the watchdog timer must reset on every
// heartbeat rather than run once from process start on a fixed cadence.
func TestContinuousHeartbeatsNeverExpireTheWatchdog(t *testing.T) {
	alwaysResolvableLTCGen(t)
	stopHeartbeats := make(chan struct{})
	starter := &fakeLTCStarter{
		onStart: func(p *fakeLTCProcess, onHeartbeat func(pkgaudio.LTCTimecode)) {
			go func() {
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-tick.C:
						onHeartbeat(pkgaudio.LTCTimecode("00:00:01:00"))
					case <-stopHeartbeats:
						return
					}
				}
			}()
		},
	}
	clock := newFakeLTCClock(time.Unix(1000, 0))
	// A wide margin (10ms heartbeats against a 150ms timeout) so this test
	// stays deterministic under -race and a loaded test machine, where
	// scheduling jitter is measured in tens of milliseconds — the property
	// under test is "the deadline resets on every heartbeat," not "the
	// scheduler is fast."
	policy := fastLTCPolicy()
	policy.heartbeatTimeout = 150 * time.Millisecond
	g := newTestLTCGenerator(clock, starter, policy)
	defer g.Shutdown(context.Background())
	defer close(stopHeartbeats)

	g.Start(LTCGeneratorSpec{FrameRate: pkgaudio.LTCFrameRate30, StartOffset: "00:00:00:00"})
	waitForLTCState(t, g, LTCGeneratorRunning)

	// Heartbeats arrive every 10ms against a 150ms timeout: run well past
	// several timeout windows and confirm the process is never restarted.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		snap := g.Snapshot()
		if snap.State != LTCGeneratorRunning {
			t.Fatalf("state = %q while heartbeats keep arriving, want Running the whole time: %+v", snap.State, snap)
		}
		if snap.RestartCount != 0 {
			t.Fatalf("RestartCount = %d, want 0: a continuously-heartbeating generator must never be restarted", snap.RestartCount)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if starter.callCount() != 1 {
		t.Errorf("starter called %d times, want exactly 1 (no restart)", starter.callCount())
	}
}

// TestStopReportsStoppedAndKillsTheProcess proves an operator-issued Stop
// reaches Stopped and the underlying process is killed, matching
// pipeline.Supervisor.Clear's identical contract.
func TestStopReportsStoppedAndKillsTheProcess(t *testing.T) {
	alwaysResolvableLTCGen(t)
	var live *fakeLTCProcess
	starter := &fakeLTCStarter{
		onStart: func(p *fakeLTCProcess, onHeartbeat func(pkgaudio.LTCTimecode)) {
			live = p
			go onHeartbeat(pkgaudio.LTCTimecode("00:00:00:00"))
		},
	}
	clock := newFakeLTCClock(time.Unix(1000, 0))
	g := newTestLTCGenerator(clock, starter, fastLTCPolicy())
	defer g.Shutdown(context.Background())

	g.Start(LTCGeneratorSpec{FrameRate: pkgaudio.LTCFrameRate30, StartOffset: "00:00:00:00"})
	waitForLTCState(t, g, LTCGeneratorRunning)

	g.Stop()
	snap := waitForLTCState(t, g, LTCGeneratorStopped)
	if snap.TimecodeKnown {
		t.Errorf("stopped snapshot still carries TimecodeKnown: %+v", snap)
	}
	if live == nil {
		t.Fatal("no process was ever started")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		live.mu.Lock()
		killed := live.killed
		live.mu.Unlock()
		if killed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("process was never killed by Stop")
}

// TestUnresolvableBinaryReportsUnsupportedNotFailed proves ruling 6: a
// missing generator binary degrades to Unsupported (never crashes the
// agent, never Failed as if it were a runtime malfunction).
func TestUnresolvableBinaryReportsUnsupportedNotFailed(t *testing.T) {
	prevLook, prevEnv := ltcLookPathFunc, ltcLookupEnvFunc
	ltcLookupEnvFunc = func(string) (string, bool) { return "", false }
	ltcLookPathFunc = func(string) (string, error) { return "", context.DeadlineExceeded }
	defer func() { ltcLookPathFunc, ltcLookupEnvFunc = prevLook, prevEnv }()

	starter := &fakeLTCStarter{}
	clock := newFakeLTCClock(time.Unix(1000, 0))
	g := newTestLTCGenerator(clock, starter, fastLTCPolicy())
	defer g.Shutdown(context.Background())

	g.Start(LTCGeneratorSpec{FrameRate: pkgaudio.LTCFrameRate30, StartOffset: "00:00:00:00"})

	snap := waitForLTCState(t, g, LTCGeneratorUnsupported)
	if snap.Reason == "" {
		t.Error("unsupported snapshot carries no reason")
	}
	if starter.callCount() != 0 {
		t.Errorf("starter was called %d times for an unresolvable binary, want 0", starter.callCount())
	}
}

// TestStartPassesFrameRateAndOffsetToArgv proves the offset/rate actually
// reach the generator process's own argv, not just the reported snapshot.
func TestStartPassesFrameRateAndOffsetToArgv(t *testing.T) {
	alwaysResolvableLTCGen(t)
	starter := &fakeLTCStarter{}
	clock := newFakeLTCClock(time.Unix(1000, 0))
	g := newTestLTCGenerator(clock, starter, fastLTCPolicy())
	defer g.Shutdown(context.Background())

	g.Start(LTCGeneratorSpec{FrameRate: pkgaudio.LTCFrameRate2997, StartOffset: "01:00:00:00", SampleRate: 44100})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && starter.callCount() == 0 {
		time.Sleep(time.Millisecond)
	}
	argv := starter.lastArgv()
	found := map[string]bool{"29.97": false, "01:00:00:00": false, "44100": false}
	for _, a := range argv {
		if _, ok := found[a]; ok {
			found[a] = true
		}
	}
	for want, ok := range found {
		if !ok {
			t.Errorf("argv %v does not contain %q", argv, want)
		}
	}
}

// TestResolveLTCStartOffsetPrefersSessionOverride proves the precedence
// rule: a session's own override wins over audio.settings' default
// whenever present.
func TestResolveLTCStartOffsetPrefersSessionOverride(t *testing.T) {
	override := pkgaudio.LTCTimecode("01:00:00:00")
	got := ResolveLTCStartOffset(&override, pkgaudio.LTCTimecode("00:00:00:00"))
	if got != override {
		t.Errorf("ResolveLTCStartOffset() = %q, want the session override %q", got, override)
	}
}

// TestResolveLTCStartOffsetFallsBackToSettingsDefault proves the other
// half: no session override falls back to audio.settings' default,
// never an implicit 00:00:00:00 the caller did not actually configure.
func TestResolveLTCStartOffsetFallsBackToSettingsDefault(t *testing.T) {
	def := pkgaudio.LTCTimecode("01:00:00:00")
	got := ResolveLTCStartOffset(nil, def)
	if got != def {
		t.Errorf("ResolveLTCStartOffset() = %q, want the settings default %q", got, def)
	}
}

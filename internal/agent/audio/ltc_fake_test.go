package audio

import (
	"context"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// fakeLTCProcess is an [LTCProcessHandle] whose exit is entirely
// test-controlled — matching internal/agent/pipeline's fakeProcess
// convention exactly, for the same reason (restart/backoff/heartbeat
// tests run in milliseconds and never depend on an ltcgen binary
// existing on the test machine).
type fakeLTCProcess struct {
	mu     sync.Mutex
	exitCh chan LTCExitResult
	killed bool
}

func newFakeLTCProcess() *fakeLTCProcess {
	return &fakeLTCProcess{exitCh: make(chan LTCExitResult, 1)}
}

func (p *fakeLTCProcess) Wait() LTCExitResult { return <-p.exitCh }

func (p *fakeLTCProcess) Kill() error {
	p.mu.Lock()
	already := p.killed
	p.killed = true
	p.mu.Unlock()
	if already {
		return nil
	}
	select {
	case p.exitCh <- LTCExitResult{Signaled: true}:
	default:
	}
	return nil
}

func (p *fakeLTCProcess) Pid() int { return 1 }

func (p *fakeLTCProcess) exitNow(res LTCExitResult) {
	p.exitCh <- res
}

// fakeLTCStarter is an [LTCProcessStarter] handing back caller-controlled
// [fakeLTCProcess] instances and recording every call, so a test can
// prove the supervisor's own call site actually invokes the injected
// starter — this seam's own version of the same proof
// internal/agent/pipeline's fakeStarter exists for.
type fakeLTCStarter struct {
	mu    sync.Mutex
	calls []fakeLTCStartCall

	// onStart, when set, runs synchronously for every Start call and may
	// kill the returned process immediately or fire a heartbeat itself —
	// giving a test full control over one attempt's behaviour, matching
	// fakeStarter.onStart's identical role one package over.
	onStart func(p *fakeLTCProcess, onHeartbeat func(pkgaudio.LTCTimecode))
}

type fakeLTCStartCall struct {
	path string
	argv []string
}

func (f *fakeLTCStarter) Start(_ context.Context, path string, args []string, onHeartbeat func(pkgaudio.LTCTimecode)) (LTCProcessHandle, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeLTCStartCall{path: path, argv: append([]string(nil), args...)})
	f.mu.Unlock()

	p := newFakeLTCProcess()
	if f.onStart != nil {
		f.onStart(p, onHeartbeat)
	}
	return p, nil
}

func (f *fakeLTCStarter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeLTCStarter) lastArgv() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1].argv
}

// alwaysResolvableLTCGen points [ResolveLTCGen] at a fixed, always-present
// path for the duration of one test, matching the "SHOWMESH_LTCGEN
// override" real behaviour without touching PATH — restored by the
// returned func.
func alwaysResolvableLTCGen(t interface{ Cleanup(func()) }) {
	prevLook, prevEnv := ltcLookPathFunc, ltcLookupEnvFunc
	ltcLookupEnvFunc = func(key string) (string, bool) {
		if key == envLTCGenOverride {
			return "/fake/showmesh-ltcgen", true
		}
		return "", false
	}
	t.Cleanup(func() {
		ltcLookPathFunc, ltcLookupEnvFunc = prevLook, prevEnv
	})
}

func newTestLTCGenerator(clock *fakeLTCClock, starter *fakeLTCStarter, policy ltcGeneratorRestartPolicy) *LTCGenerator {
	return newLTCGeneratorWithPolicy(clock.Now, starter.Start, policy)
}

// fastLTCPolicy shrinks every timing knob so a test exercising restart or
// heartbeat-loss behaviour runs in milliseconds.
func fastLTCPolicy() ltcGeneratorRestartPolicy {
	return ltcGeneratorRestartPolicy{
		maxConsecutiveFastFailures: 3,
		initialBackoff:             5 * time.Millisecond,
		maxBackoff:                 20 * time.Millisecond,
		heartbeatTimeout:           30 * time.Millisecond,
	}
}

// fakeLTCClock is an injectable clock — this file's own copy of
// pipeline's fakeClock, since Snapshot's ObservedAt/Since need a
// deterministic value to assert against.
type fakeLTCClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeLTCClock(start time.Time) *fakeLTCClock {
	return &fakeLTCClock{now: start}
}

func (c *fakeLTCClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

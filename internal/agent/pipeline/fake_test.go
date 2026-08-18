package pipeline

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// testLogger discards everything; tests assert on Supervisor state, not on
// log lines.
type testLogger struct{}

func (testLogger) Info(string, ...any)  {}
func (testLogger) Warn(string, ...any)  {}
func (testLogger) Error(string, ...any) {}

// fakeProcess is a [ProcessHandle] whose exit is entirely test-controlled:
// nothing is actually spawned, so restart/backoff policy tests run in
// milliseconds and never depend on gst-launch-1.0 being installed.
type fakeProcess struct {
	mu         sync.Mutex
	exitCh     chan ExitResult
	killed     bool
	killFunc   func()
	stdinBytes []byte
	stdinFail  bool
}

func newFakeProcess() *fakeProcess {
	return &fakeProcess{exitCh: make(chan ExitResult, 1)}
}

func (p *fakeProcess) Wait() ExitResult { return <-p.exitCh }

// Kill matches a real process's Kill: it must cause Wait (and thus
// stopCurrent's blocking <-exitCh) to unblock, exactly like a real SIGKILL
// causes a real process's Wait to return. By default it delivers a
// Signaled exit; killFunc, when set, runs instead and is responsible for
// eventually delivering an exit itself.
func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	already := p.killed
	p.killed = true
	fn := p.killFunc
	p.mu.Unlock()
	if already {
		return nil
	}
	if fn != nil {
		fn()
		return nil
	}
	// Non-blocking: if this process already delivered its own exit (e.g. a
	// crash raced this Kill) the buffered channel already holds a value,
	// and a second send here must not block forever.
	select {
	case p.exitCh <- ExitResult{Signaled: true}:
	default:
	}
	return nil
}

func (p *fakeProcess) Pid() int { return 1 }

// Stdin returns p itself as an io.Writer: writes are recorded (for a test
// to assert against) unless stdinFail is set, in which case every write
// errors — simulating a closed pipe / dead process without spawning
// anything real.
func (p *fakeProcess) Stdin() (io.Writer, error) { return p, nil }

func (p *fakeProcess) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdinFail {
		return 0, errors.New("fakeProcess: simulated stdin write failure")
	}
	p.stdinBytes = append(p.stdinBytes, b...)
	return len(b), nil
}

func (p *fakeProcess) stdinSnapshot() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]byte, len(p.stdinBytes))
	copy(out, p.stdinBytes)
	return out
}

func (p *fakeProcess) setStdinFail(fail bool) {
	p.mu.Lock()
	p.stdinFail = fail
	p.mu.Unlock()
}

// exitNow delivers res as this process's exit outcome.
func (p *fakeProcess) exitNow(res ExitResult) {
	p.exitCh <- res
}

// fakeStarter is a [ProcessStarter] recording every call and handing back
// caller-controlled [fakeProcess] instances, proving (per this seam's
// injectable-seam requirement) that the supervisor's call site actually
// invokes the starter it was given.
type fakeStarter struct {
	mu    sync.Mutex
	calls []fakeStartCall

	// onStart, when set, is called synchronously for every Start
	// invocation and may kill the returned process immediately (via
	// process.Kill()), simulating an instant crash — used for the
	// fast-failure lockout test. It also receives onRunningMarker directly,
	// so a test can hold onto it and fire it later, on its own goroutine,
	// after the runner has already moved on (F10) — something the fake's
	// default behaviour cannot exercise at all.
	onStart func(process *fakeProcess, onRunningMarker func())
}

type fakeStartCall struct {
	path string
	argv []string
}

func (f *fakeStarter) Start(_ context.Context, path string, args []string, onRunningMarker func()) (ProcessHandle, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeStartCall{path: path, argv: append([]string(nil), args...)})
	f.mu.Unlock()

	p := newFakeProcess()
	if f.onStart != nil {
		f.onStart(p, onRunningMarker)
	} else if onRunningMarker != nil {
		// Default behaviour: simulate reaching PLAYING almost immediately,
		// like a healthy pipeline, unless the test wants finer control. On
		// its own goroutine, matching startRealProcess: onRunningMarker
		// there fires from watchStdout, asynchronously, strictly after
		// Start has already returned. Calling it synchronously here (as
		// this fake used to) cannot produce the ordering a real process
		// produces — where the runner's own loop can process an exit
		// before a still-in-flight marker call is scheduled — which is
		// exactly the race F10 exists to guard.
		go onRunningMarker()
	}
	return p, nil
}

func (f *fakeStarter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeClock is an injectable, manually-advanced clock matching this
// codebase's now-func convention, so restart-policy tests never depend on
// real wall-clock timing.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

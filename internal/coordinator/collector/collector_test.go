package collector

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// testLogger discards output: these tests assert behavior, not log
// content, and a discarded logger keeps `go test -v` readable.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// countingCollector reports how many times Poll has been called, and lets
// a test block a call until it chooses to release it (to test overlap
// prevention).
type countingCollector struct {
	id    string
	calls atomic.Int64

	mu       sync.Mutex
	blocking bool
	release  chan struct{}

	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

func (c *countingCollector) ID() string { return c.id }

func (c *countingCollector) Poll(ctx context.Context) ([]observation.Observation, bool) {
	n := c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	for {
		old := c.maxInFlight.Load()
		if n <= old || c.maxInFlight.CompareAndSwap(old, n) {
			break
		}
	}

	c.calls.Add(1)

	c.mu.Lock()
	blocking := c.blocking
	release := c.release
	c.mu.Unlock()
	if blocking {
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	return nil, true
}

// fakeSink records every delivery it receives.
type fakeSink struct {
	mu   sync.Mutex
	recs [][]observation.Observation
	// completes parallels recs: completes[i] is the complete flag delivered
	// alongside recs[i].
	completes []bool
}

func (f *fakeSink) RecordObservations(ctx context.Context, obs []observation.Observation, complete bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, obs)
	f.completes = append(f.completes, complete)
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recs)
}

func TestRunnerPollsImmediatelyOnStart(t *testing.T) {
	c := &countingCollector{id: "test"}
	sink := &fakeSink{}
	r := NewRunner(sink, testLogger())
	r.Add(c, time.Hour) // long enough that only the immediate poll matters

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for c.calls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatalf("Poll was not called within 2s of Run starting")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s of cancellation")
	}
}

// TestRunnerPollsOnCadence proves Run polls its collector repeatedly, not
// only once.
//
// Step 3 review finding 4.8: this used to sleep a fixed 150ms at a 20ms
// cadence and then assert at least 3 calls happened — a wall-clock ratio
// with only a little slack, which fails identically on a loaded CI runner
// (too slow to reach 3 calls in 150ms) and on an actually-broken cadence
// (stopped polling after the first call), reading as flake either way. The
// fix waits for a call count to be reached with a generous bound instead of
// asserting a count reached within a fixed sleep: it never fails merely for
// being slow, and it still fails (by timing out) if polling genuinely never
// repeats.
func TestRunnerPollsOnCadence(t *testing.T) {
	c := &countingCollector{id: "test"}
	sink := &fakeSink{}
	r := NewRunner(sink, testLogger())
	r.Add(c, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	const wantCalls = 5
	deadline := time.After(5 * time.Second)
waitLoop:
	for {
		select {
		case <-deadline:
			t.Fatalf("Poll called only %d times within 5s at a 5ms cadence, want at least %d", c.calls.Load(), wantCalls)
		default:
			if c.calls.Load() >= wantCalls {
				break waitLoop
			}
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s of cancellation")
	}
}

func TestRunnerNeverOverlapsPollsForTheSameCollector(t *testing.T) {
	// A Poll call that takes longer than the configured interval must not
	// cause a second, concurrent Poll call for the same collector: Run's
	// self-paced timer (started only after Poll returns) is what the fpp
	// package's "never two in-flight requests to the same instance"
	// requirement leans on instead of its own bookkeeping. This test
	// proves that property generically, once, here.
	c := &countingCollector{id: "test"}
	c.mu.Lock()
	c.blocking = true
	c.release = make(chan struct{})
	c.mu.Unlock()

	sink := &fakeSink{}
	r := NewRunner(sink, testLogger())
	r.Add(c, time.Millisecond) // interval far shorter than how long Poll blocks

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	// Let the first (blocked) Poll call establish itself, and give the
	// runner ample opportunity to (incorrectly) start a second one before
	// the first returns.
	time.Sleep(100 * time.Millisecond)

	close(c.release)
	cancel()
	<-done

	if got := c.maxInFlight.Load(); got > 1 {
		t.Errorf("max concurrent Poll calls for one collector = %d, want 1 (no overlap)", got)
	}
}

func TestRunnerStopsCleanlyOnContextCancelWithNoLeakedGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()

	c1 := &countingCollector{id: "one"}
	c2 := &countingCollector{id: "two"}
	sink := &fakeSink{}
	r := NewRunner(sink, testLogger())
	r.Add(c1, 5*time.Millisecond)
	r.Add(c2, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s of cancellation")
	}

	// Give any genuinely-leaked goroutine a moment it would need anyway
	// (e.g. GC-driven finalizer timing) before asserting; this is not a
	// sleep to paper over a race, it is slack for goroutine-count
	// bookkeeping that is inherently a little noisy.
	deadline := time.After(2 * time.Second)
	for {
		if runtime.NumGoroutine() <= baseline+1 { // +1 slack for test scheduling noise
			return
		}
		select {
		case <-deadline:
			t.Fatalf("goroutine count = %d after Run returned, want close to baseline %d (leak)", runtime.NumGoroutine(), baseline)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestRunnerDeliversEachPollToSink(t *testing.T) {
	res := observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}
	want, err := observation.Measured(res, observation.SignalID("fpp.reachable"), true, time.Now())
	if err != nil {
		t.Fatalf("observation.Measured() error = %v", err)
	}

	c := &staticCollector{id: "test", obs: []observation.Observation{want}}
	sink := &fakeSink{}
	r := NewRunner(sink, testLogger())
	r.Add(c, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for sink.count() < 1 {
		select {
		case <-deadline:
			t.Fatalf("sink received no delivery within 2s")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	<-done

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.recs) == 0 || len(sink.recs[0]) != 1 {
		t.Fatalf("sink.recs = %+v, want exactly one delivery of one observation", sink.recs)
	}
	if sink.recs[0][0].Signal != want.Signal {
		t.Errorf("delivered signal = %q, want %q", sink.recs[0][0].Signal, want.Signal)
	}
}

func TestAddPanicsOnNonPositiveInterval(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("Add did not panic on a zero interval")
		}
	}()
	r := NewRunner(&fakeSink{}, testLogger())
	r.Add(&countingCollector{id: "test"}, 0)
}

// staticCollector always returns the same fixed observations.
type staticCollector struct {
	id  string
	obs []observation.Observation
}

func (s *staticCollector) ID() string { return s.id }
func (s *staticCollector) Poll(ctx context.Context) ([]observation.Observation, bool) {
	return s.obs, true
}

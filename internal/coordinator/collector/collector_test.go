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

// --- Runner.Nudge: the owner's 2026-08-13 post-dispatch poll nudge. These
// tests exercise the mechanism this package now offers generically
// (internal/coordinator/api/fppcommand_handler.go is the one production
// caller, via internal/coordinator/apiwiring.go's fppRunnerNudger); none of
// them know or care that the real caller is a command dispatch — Nudge's
// whole contract is that it means the same thing regardless of caller. ---

// waitForCalls blocks until c.calls reaches at least want, or fails t after
// a generous deadline — the identical wait shape
// TestRunnerPollsImmediatelyOnStart and TestRunnerPollsOnCadence above
// already hand-roll once each; factored out here because the nudge tests
// below add three more call sites for it.
func waitForCalls(t *testing.T, c *countingCollector, want int64) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if c.calls.Load() >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Poll called only %d times within 2s, want at least %d", c.calls.Load(), want)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// TestRunnerNudgeTriggersPollForItsOwnCollectorOnly proves Nudge is scoped
// per id: nudging collector "one" produces an extra poll for "one" and
// none at all for "two", even though both share the same Runner and both
// are registered with an interval long enough that neither would poll
// again on its own before this test's own deadline.
func TestRunnerNudgeTriggersPollForItsOwnCollectorOnly(t *testing.T) {
	c1 := &countingCollector{id: "one"}
	c2 := &countingCollector{id: "two"}
	sink := &fakeSink{}
	r := NewRunner(sink, testLogger())
	r.Add(c1, time.Hour)
	r.Add(c2, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitForCalls(t, c1, 1) // Run's own immediate first poll, for both.
	waitForCalls(t, c2, 1)

	if !r.Nudge("one") {
		t.Fatalf(`Nudge("one") = false, want true`)
	}
	waitForCalls(t, c1, 2)

	// Give a wrongly-broad nudge implementation time to (incorrectly) poll
	// "two" as well before asserting it never did.
	time.Sleep(100 * time.Millisecond)
	if got := c2.calls.Load(); got != 1 {
		t.Errorf(`collector "two" was polled %d times after Nudge("one"), want 1 (a nudge for one instance must never affect another)`, got)
	}
}

// TestRunnerNudgeUnknownIDIsSuppressedNotAnError proves Nudge for an id no
// Add call ever registered returns false rather than panicking or blocking
// — the exact "unknown nudge falls back to ordinary behavior" contract
// [FPPPollNudger]'s doc comment (internal/coordinator/api) names, proven
// here at the one place that fact is actually implemented.
func TestRunnerNudgeUnknownIDIsSuppressedNotAnError(t *testing.T) {
	r := NewRunner(&fakeSink{}, testLogger())
	r.Add(&countingCollector{id: "known"}, time.Hour)

	if r.Nudge("unknown") {
		t.Errorf(`Nudge("unknown") = true, want false — no collector was ever registered under that id`)
	}
}

// TestRunnerNudgeRateLimitedPerID is this task's own required proof: a
// second nudge for the SAME id inside [DefaultNudgeMinInterval] (here,
// [WithNudgeMinInterval]'s override) is suppressed, produces no extra
// poll, and — critically — is not an error: the collector's own scheduled
// cadence remains exactly what would confirm a command whose own nudge got
// suppressed. [WithNudgeClock] decouples the RATE LIMIT's clock from the
// real wall-clock timers driving each collector's own poll loop (already
// proven correct, with no fake clock at all, by TestRunnerPollsOnCadence
// and friends above), so the rate-limit boundary itself can be asserted
// deterministically rather than raced against a real 2-second sleep.
func TestRunnerNudgeRateLimitedPerID(t *testing.T) {
	c := &countingCollector{id: "test"}
	sink := &fakeSink{}

	var clockMu sync.Mutex
	fakeNow := time.Unix(1_700_000_000, 0)
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return fakeNow
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		fakeNow = fakeNow.Add(d)
	}

	r := NewRunner(sink, testLogger(), WithNudgeMinInterval(2*time.Second), WithNudgeClock(clock))
	r.Add(c, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitForCalls(t, c, 1) // Run's own immediate first poll.

	if !r.Nudge("test") {
		t.Fatalf("first Nudge = false, want true (no prior nudge to rate-limit against)")
	}
	waitForCalls(t, c, 2)

	// Fake clock has not advanced: still inside the 2s window.
	if r.Nudge("test") {
		t.Errorf("second Nudge inside the rate-limit window = true, want false (suppressed)")
	}
	time.Sleep(50 * time.Millisecond) // let a wrongly-accepted nudge fire, if it would
	if got := c.calls.Load(); got != 2 {
		t.Errorf("Poll called %d times after a rate-limited nudge, want exactly 2 (no extra poll; the command that "+
			"requested it must still confirm off the collector's own scheduled cadence)", got)
	}

	// Advance past the window: a nudge must be accepted again.
	advance(3 * time.Second)
	if !r.Nudge("test") {
		t.Errorf("Nudge after the rate-limit window elapsed = false, want true")
	}
	waitForCalls(t, c, 3)
}

// TestRunnerNudgeReturnsImmediatelyWhileCollectorPollIsInFlight proves
// design constraint 2 (the nudge must never let a dispatch path hang
// unboundedly): Nudge does not wait for the collector's own in-flight Poll
// call — the exact call a nudge is meant to make happen SOONER, which is
// also the one call a naive implementation might be tempted to wait on.
func TestRunnerNudgeReturnsImmediatelyWhileCollectorPollIsInFlight(t *testing.T) {
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

	// Let the first (blocked) Poll call establish itself.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	r.Nudge("test")
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Nudge took %v while a Poll call was in flight, want it to return immediately (never block on network I/O or on Poll completing)", elapsed)
	}

	close(c.release)
	cancel()
	<-done
}

// TestRunnerAddAfterRunStartsPolling covers the half of the 2026-08-14
// dynamic-registry change that a naive reading would call the easy one and
// that is actually the dangerous one to get wrong.
//
// Add used to be documented as "must be called before Run", and adding
// afterwards silently did nothing. Silently is the problem: nothing
// errored, nothing logged, and the endpoint appeared in the API while no
// poll ever happened, so every command dispatched to it would confirm
// nothing and read as broken hardware rather than as configuration that
// had not landed.
func TestRunnerAddAfterRunStartsPolling(t *testing.T) {
	early := &countingCollector{id: "early"}
	late := &countingCollector{id: "late"}
	r := NewRunner(&fakeSink{}, testLogger())
	r.Add(early, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	waitForCalls(t, early, 1)

	r.Add(late, time.Hour)
	waitForCalls(t, late, 1)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancellation; a collector added after Run must still be waited for")
	}
}

// TestRunnerRemoveStopsPollingAndNudging proves the other half: a removed
// collector stops being polled, and stops being a valid nudge target,
// while its neighbours are undisturbed.
//
// The neighbour assertion is the point. Reconciliation calls Remove for
// one endpoint while the rest of the fleet is mid-show, so "removing one
// collector quietly disturbed the others" is exactly the defect worth a
// test rather than a code comment.
func TestRunnerRemoveStopsPollingAndNudging(t *testing.T) {
	doomed := &countingCollector{id: "doomed"}
	survivor := &countingCollector{id: "survivor"}
	r := NewRunner(&fakeSink{}, testLogger(), WithNudgeMinInterval(0))
	r.Add(doomed, 5*time.Millisecond)
	r.Add(survivor, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	waitForCalls(t, doomed, 2)
	waitForCalls(t, survivor, 2)

	if !r.Remove("doomed") {
		t.Fatal("Remove(\"doomed\") = false, want true: it was registered")
	}
	if r.Remove("doomed") {
		t.Fatal("Remove(\"doomed\") = true on the second call, want false: it is already gone")
	}
	if r.Nudge("doomed") {
		t.Fatal("Nudge(\"doomed\") = true after removal, want false: a removed collector is not a nudge target")
	}

	// Let the poll loop notice cancellation, then hold the count still.
	time.Sleep(50 * time.Millisecond)
	stopped := doomed.calls.Load()
	time.Sleep(80 * time.Millisecond) // many poll intervals
	if got := doomed.calls.Load(); got != stopped {
		t.Fatalf("removed collector polled %d more times after Remove returned", got-stopped)
	}

	// The neighbour is untouched: still polling, still nudgeable.
	waitForCalls(t, survivor, stopped+2)
	if !r.Nudge("survivor") {
		t.Fatal("Nudge(\"survivor\") = false; removing one collector must not disturb another")
	}

	ids := r.IDs()
	if len(ids) != 1 || ids[0] != "survivor" {
		t.Fatalf("IDs() = %v, want exactly [survivor]", ids)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancellation")
	}
}

package api

import (
	"context"
	"sync"
	"testing"
	"time"
)

// This file is ADR-024 decision 8's missing coverage: loginLimiter shipped
// with zero tests of its own (fastLoginOptions, in auth_test.go, was
// declared for exactly this file and never called until now). A review
// pass confirmed by mutation that the whole api package's test suite
// passed unmodified against each of six independently broken versions of
// this type: a genuine per-principal lockout, keying on principal name
// instead of source, never applying the delay at all, never counting
// failures, removing decay, and removing the cap. Every test below is
// named for the property it exists to catch, and was confirmed to fail
// against the corresponding broken behavior before being restored — see
// this task's report for the mutation-by-mutation results.
//
// Every test in this file drives *loginLimiter directly (this package's
// own type, not the HTTP layer) with a fake clock and, where a test needs
// to observe delay() actually sleeping, a non-blocking recording stub in
// place of l.sleep — CLAUDE.md's "no real sleeps, timers, or wall-clock
// reads in assertions" applies to this package's own tests exactly as
// much as to anyone else's, and loginLimiter's now/sleep seams exist for
// precisely this reason (see newLoginLimiter's doc comment).

// fakeLoginClock is this file's own tiny injectable clock, matching the
// pattern this package's other test files (e.g. session_test.go's
// fixedClock) already use, except mutable — several tests here need to
// advance time between calls without a real sleep.
type fakeLoginClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeLoginClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeLoginClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recordingSleep is a loginLimiter.sleep replacement that never actually
// blocks: it records the requested duration and returns immediately. Used
// by every test in this file that calls delay() — a production loginLimiter
// built by newLoginLimiter installs a real time.NewTimer-based sleep; this
// stub is what makes this file's tests fast and deterministic regardless
// of how large a duration delay() computes.
type recordingSleep struct {
	mu   sync.Mutex
	last time.Duration
	n    int
}

func (r *recordingSleep) fn(ctx context.Context, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = d
	r.n++
}

func (r *recordingSleep) calls() (n int, last time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n, r.last
}

func newTestLoginLimiter(clock *fakeLoginClock, concurrency int, queueWait, perFailureDelay, maxDelay time.Duration) (*loginLimiter, *recordingSleep) {
	l := newLoginLimiter(concurrency, queueWait, perFailureDelay, maxDelay, clock.now)
	rec := &recordingSleep{}
	l.sleep = rec.fn
	return l, rec
}

// --- property: delay escalates per FAILURE, from zero, linear in count, and caps ---

func TestLoginLimiterDelayEscalatesWithFailuresAndCaps(t *testing.T) {
	clock := &fakeLoginClock{t: mustLoginTime(t, "2026-01-01T00:00:00Z")}
	l, _ := newTestLoginLimiter(clock, 4, time.Second, 100*time.Millisecond, 300*time.Millisecond)

	if d := l.currentDelay("src-a"); d != 0 {
		t.Fatalf("currentDelay before any failure = %v, want 0", d)
	}

	l.recordFailure("src-a")
	if d := l.currentDelay("src-a"); d != 100*time.Millisecond {
		t.Errorf("currentDelay after 1 failure = %v, want 100ms", d)
	}

	l.recordFailure("src-a")
	if d := l.currentDelay("src-a"); d != 200*time.Millisecond {
		t.Errorf("currentDelay after 2 failures = %v, want 200ms", d)
	}

	// A third failure would compute to 300ms exactly at the cap; a fourth
	// must not exceed it — this is the "removing the cap" mutation's
	// target.
	l.recordFailure("src-a")
	l.recordFailure("src-a")
	if d := l.currentDelay("src-a"); d != 300*time.Millisecond {
		t.Errorf("currentDelay after 4 failures = %v, want capped at 300ms", d)
	}
}

// --- property: the delay is never counted or applied against anything a caller did not fail from ---

func TestLoginLimiterDelayIsPerSourceNeverGlobal(t *testing.T) {
	clock := &fakeLoginClock{t: mustLoginTime(t, "2026-01-01T00:00:00Z")}
	l, _ := newTestLoginLimiter(clock, 4, time.Second, 100*time.Millisecond, time.Second)

	l.recordFailure("attacker")
	l.recordFailure("attacker")
	l.recordFailure("attacker")

	if d := l.currentDelay("innocent-source"); d != 0 {
		t.Errorf("currentDelay for a source that never failed = %v, want 0 (one source's failures must never slow a different source)", d)
	}
}

// --- property: a source's own successful login clears ONLY that source's history ---

func TestLoginLimiterRecordSuccessClearsOnlyItsOwnSource(t *testing.T) {
	clock := &fakeLoginClock{t: mustLoginTime(t, "2026-01-01T00:00:00Z")}
	l, _ := newTestLoginLimiter(clock, 4, time.Second, 100*time.Millisecond, time.Second)

	l.recordFailure("src-a")
	l.recordFailure("src-a")
	l.recordFailure("src-b")

	l.recordSuccess("src-a")

	if d := l.currentDelay("src-a"); d != 0 {
		t.Errorf("currentDelay for src-a after its own success = %v, want 0", d)
	}
	if d := l.currentDelay("src-b"); d != 100*time.Millisecond {
		t.Errorf("currentDelay for src-b after src-a's success = %v, want 100ms (unaffected — a success on one source must never reset a different source's history)", d)
	}
}

// --- property: failure history decays after decayAfter, not sooner and not never ---

func TestLoginLimiterFailureHistoryDecaysAfterInactivity(t *testing.T) {
	clock := &fakeLoginClock{t: mustLoginTime(t, "2026-01-01T00:00:00Z")}
	l, _ := newTestLoginLimiter(clock, 4, time.Second, 100*time.Millisecond, time.Second)
	l.decayAfter = time.Minute // shrink from the production 5-minute value; no real sleep either way

	l.recordFailure("src-a")
	l.recordFailure("src-a")
	if d := l.currentDelay("src-a"); d != 200*time.Millisecond {
		t.Fatalf("currentDelay before decay = %v, want 200ms", d)
	}

	clock.advance(30 * time.Second) // well within decayAfter
	if d := l.currentDelay("src-a"); d != 200*time.Millisecond {
		t.Errorf("currentDelay at 30s (< decayAfter) = %v, want still 200ms (decayed too early)", d)
	}

	clock.advance(31 * time.Second) // now past decayAfter (61s total)
	if d := l.currentDelay("src-a"); d != 0 {
		t.Errorf("currentDelay past decayAfter = %v, want 0 (a source that stopped failing must not be penalized forever)", d)
	}
}

// --- property: delay() actually sleeps for the computed duration, never a silent no-op ---

func TestLoginLimiterDelayActuallySleepsForTheComputedDuration(t *testing.T) {
	clock := &fakeLoginClock{t: mustLoginTime(t, "2026-01-01T00:00:00Z")}
	l, rec := newTestLoginLimiter(clock, 4, time.Second, 50*time.Millisecond, time.Second)

	// No failure history yet: delay() must not call sleep at all (a 0
	// duration is "nothing to wait for", not "wait for zero").
	l.delay(context.Background(), "src-a")
	if n, _ := rec.calls(); n != 0 {
		t.Errorf("sleep called %d times before any failure, want 0", n)
	}

	l.recordFailure("src-a")
	l.recordFailure("src-a")
	l.delay(context.Background(), "src-a")
	if n, last := rec.calls(); n != 1 || last != 100*time.Millisecond {
		t.Errorf("sleep call = (n=%d, last=%v), want (n=1, last=100ms) — the delay must actually be requested, not skipped", n, last)
	}
}

// --- property: a queued attempt beyond the concurrency bound is rejected, not held indefinitely ---

func TestLoginLimiterAcquireRejectsAfterQueueWaitWhenFull(t *testing.T) {
	clock := &fakeLoginClock{t: mustLoginTime(t, "2026-01-01T00:00:00Z")}
	l, _ := newTestLoginLimiter(clock, 1, 10*time.Millisecond, time.Millisecond, time.Millisecond)

	if !l.acquire(context.Background()) {
		t.Fatalf("first acquire on an empty limiter failed, want success")
	}
	defer l.release()

	if l.acquire(context.Background()) {
		t.Errorf("second acquire succeeded while the sole slot was held, want it to queue and then fail")
		l.release()
	}
}

func mustLoginTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return parsed.UTC()
}

package api

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

// loginLimiter bounds ADR-024 decision 8's login cost, without EVER
// locking out a principal: nothing here is keyed on a principal name,
// only on the request's own source address, so a correct password from a
// given source is never refused for having failed before from that same
// source. Two independent mechanisms, both required by decision 8:
//
//   - A concurrency bound on argon2id verification ([loginLimiter.acquire]/
//     [loginLimiter.release]), so unbounded concurrent login attempts
//     cannot deny the write surface to everyone else on an appliance also
//     running the broker, the UI, and the collectors. A queued attempt
//     that would exceed the bound is rejected outright after
//     queueWait — never held indefinitely.
//   - A per-source increasing delay ([loginLimiter.delay]) applied BEFORE
//     the caller attempts verification, so each additional failure from
//     one source slows every subsequent attempt from it. This is latency,
//     never a refusal: a correct password from a source with a long
//     failure history still succeeds, just slower — the property decision
//     8's whole design is shaped around ("neither mechanism can ever put a
//     principal into a state where the correct password is refused").
//
// The delay MUST run before [loginLimiter.acquire], never after — every
// caller in this package (session.go, bootstrap.go) is required to call
// [loginLimiter.delay] first and only then [loginLimiter.acquire]. This is
// load-bearing, not stylistic, and was itself the subject of a review
// finding, reproduced against the real binary: acquiring the slot first
// and delaying while holding it means a slot is occupied for
// delay-plus-verify time instead of just verify time, so a handful of
// concurrent requests from one already-slowed source can fill every slot
// with sleeping holders and starve a DIFFERENT source's correct-password
// login into the queue timeout — a 429 for a source that did nothing
// wrong, which is exactly the operator lockout this decision exists to
// rule out. Delaying outside the semaphore bounds what a slot is ever held
// for to argon2id verification alone, regardless of any source's history,
// which is what actually makes "a correct password from a source with a
// long failure history still succeeds, just slower" true rather than
// aspirational — see TestCorrectPasswordFromThrottledSourceStillSucceedsWhileAnotherSourceQueues
// in auth_test.go for the regression guard.
//
// Both bounds are SHOWMESH HYPOTHESES: sized to keep a Pi-class
// coordinator responsive to everything else while making sustained
// guessing arbitrarily expensive, not derived from any measurement.
type loginLimiter struct {
	sem       chan struct{}
	queueWait time.Duration

	perFailureDelay time.Duration
	maxDelay        time.Duration
	decayAfter      time.Duration

	now func() time.Time
	// sleep is overridden in tests to a non-blocking stub, so this
	// package's tests never actually wait out a real delay (CLAUDE.md: "no
	// real sleeps, timers, or wall-clock reads in assertions"). Production
	// code (newLoginLimiter's default) uses a real timer respecting ctx
	// cancellation, so a login request's own deadline/client disconnect
	// still unblocks a queued or delayed attempt rather than leaking it.
	sleep func(ctx context.Context, d time.Duration)

	mu       sync.Mutex
	failures map[string]sourceFailureState
}

type sourceFailureState struct {
	count    int
	lastSeen time.Time
}

// newLoginLimiter builds a [loginLimiter]. now is the same clock seam
// every other injectable-time type in this package uses.
func newLoginLimiter(concurrency int, queueWait, perFailureDelay, maxDelay time.Duration, now func() time.Time) *loginLimiter {
	if concurrency <= 0 {
		concurrency = 1
	}
	return &loginLimiter{
		sem:             make(chan struct{}, concurrency),
		queueWait:       queueWait,
		perFailureDelay: perFailureDelay,
		maxDelay:        maxDelay,
		decayAfter:      5 * time.Minute,
		now:             now,
		sleep: func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
			case <-ctx.Done():
			}
		},
		failures: make(map[string]sourceFailureState),
	}
}

// acquire blocks until a concurrency slot is free, up to queueWait, and
// reports whether it got one. Every true return MUST be paired with
// exactly one [loginLimiter.release].
func (l *loginLimiter) acquire(ctx context.Context) bool {
	select {
	case l.sem <- struct{}{}:
		return true
	default:
	}

	timer := time.NewTimer(l.queueWait)
	defer timer.Stop()
	select {
	case l.sem <- struct{}{}:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (l *loginLimiter) release() { <-l.sem }

// delay sleeps source's currently accumulated per-source delay before the
// caller attempts verification — see this type's doc comment.
func (l *loginLimiter) delay(ctx context.Context, source string) {
	if d := l.currentDelay(source); d > 0 {
		l.sleep(ctx, d)
	}
}

// loginDelayAppliedKey marks a request context whose per-source delay has
// already been paid, so [loginLimiter.delayOnce] never charges the same
// request twice.
type loginDelayAppliedKey struct{}

// delayOnce applies source's accumulated delay unless this request has
// already paid it, and returns a context recording that it now has.
//
// One request must never be charged the delay twice, because two
// independent places apply it: withIdentity delays EVERY request whose
// credential fails to resolve (auth.go), and POST /api/v1/session and
// POST /api/v1/bootstrap delay again before verifying a password. A
// sign-in carrying a stale showmesh_session cookie — the ordinary case
// under ADR-024 decision 5, where the cookie rides along until sign-in or
// its 90-day expiry — hits both, and maxDelay twice plus queueWait
// exceeds net/http.Server's WriteTimeout (httpapi.NewServer). The server
// then closes the connection with no response at all, so the operator
// sees a proxy 502 on the sign-in form instead of being signed in, with
// nothing in the coordinator log to explain it. Reproduced against the
// running stack: a POST /api/v1/session carrying an invalid cookie
// returned 502 after exactly 10s, the WriteTimeout to the millisecond.
//
// Charging once per request keeps the throttle's actual property — each
// additional failure from a source slows that source's next attempt —
// while bounding one request's total added latency to maxDelay.
func (l *loginLimiter) delayOnce(ctx context.Context, source string) context.Context {
	if applied, _ := ctx.Value(loginDelayAppliedKey{}).(bool); applied {
		return ctx
	}
	l.delay(ctx, source)
	return context.WithValue(ctx, loginDelayAppliedKey{}, true)
}

// currentDelay computes, without mutating, how long source's next attempt
// should be delayed: linear in its recent failure count, capped at
// maxDelay, and reset to zero once decayAfter has passed since the last
// failure — a source that stops failing is not penalized forever.
func (l *loginLimiter) currentDelay(source string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.failures[source]
	if !ok {
		return 0
	}
	if l.now().Sub(st.lastSeen) > l.decayAfter {
		delete(l.failures, source)
		return 0
	}
	d := time.Duration(st.count) * l.perFailureDelay
	if d > l.maxDelay {
		d = l.maxDelay
	}
	return d
}

func (l *loginLimiter) recordFailure(source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.failures[source]
	st.count++
	st.lastSeen = l.now()
	l.failures[source] = st
}

// recordSuccess clears source's failure history — a correct password
// resets the delay for its own source going forward, matching decision
// 8's "neither can strand the operator" property: a source is only ever
// slowed by ITS OWN recent failures, never by another source's, and never
// permanently.
func (l *loginLimiter) recordSuccess(source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, source)
}

// loginSource derives a per-source key from r's remote address: the host
// portion only (via net.SplitHostPort), so a client retrying from a new
// ephemeral source port is not treated as an unpenalized new source on
// every attempt. Falls back to the raw RemoteAddr string when it does not
// parse as host:port (e.g. a test's httptest.NewRequest default, or a
// unix socket) rather than failing the request over a bookkeeping detail.
func loginSource(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

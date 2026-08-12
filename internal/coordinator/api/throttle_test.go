package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// TestWithIdentityThrottlesCredentialInURLPerSource closes review finding
// 11's first smaller item: the credential-in-url rejection in auth.go's
// withIdentity used to write an audit entry on every single matching
// request, completely unauthenticated and with no bound at all — unlike
// POST /api/v1/session and POST /api/v1/bootstrap, this path is reachable
// on ANY route, so a source repeating it could grow the append-only audit
// table without limit at full request rate. This drives withIdentity
// directly (a whitebox unit, not through the full API) against a real
// loginLimiter with a recording sleep stub, so the two properties that
// matter are asserted with no timing race at all: a rejection measurably
// raises that source's currentDelay (recordFailure ran), and a second
// offense from the same source actually requests a sleep (delay ran) —
// see loginlimiter_test.go's TestLoginLimiterDelayActuallySleepsForTheComputedDuration
// for the identical property proven against the limiter alone; this test
// is what proves withIdentity is actually wired to it.
func TestWithIdentityThrottlesCredentialInURLPerSource(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	clock := &fakeLoginClock{t: testNow}
	limiter, rec := newTestLoginLimiter(clock, 4, time.Second, 50*time.Millisecond, time.Second)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := withIdentity(svc, limiter, testLogger(), fixedClock(testNow), false)(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes?tok="+identity.TokenPrefix+"leaked", nil)
	req.RemoteAddr = "198.51.100.10:1234"
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Result().StatusCode)
	}

	source := loginSource(req)
	if d := limiter.currentDelay(source); d == 0 {
		t.Errorf("currentDelay(%q) after one credential-in-url rejection = 0, want > 0 — the rejection must feed the per-source throttle", source)
	}

	// A second offense from the same source must actually request a
	// sleep: delay() only calls loginLimiter.sleep when currentDelay > 0,
	// which the first offense above just made true for this source.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/nodes?tok="+identity.TokenPrefix+"leaked2", nil)
	req2.RemoteAddr = "198.51.100.10:1234"
	rr2 := httptest.NewRecorder()
	mw.ServeHTTP(rr2, req2)
	if n, _ := rec.calls(); n == 0 {
		t.Errorf("loginLimiter.sleep was never invoked for a second credential-in-url offense from an already-penalized source")
	}
}

// TestWithIdentityThrottlesCredentialResolveFailurePerSource is Fix 1 from
// this task's report: resolveCredential's failure branch in withIdentity
// wrote an audit entry on EVERY request, on every route — including every
// open read, which is most of this API's traffic by design — with no
// bound at all. Unlike the credential-in-url path directly above (which
// requires a caller to deliberately misuse the URL), this branch is hit by
// something as ordinary as a stale showmesh_session cookie left over from
// an unrelated local stack: ADR-024 decision 5 scopes cookies by host and
// ignores port, so the cookie rides along on every request until sign-in
// or its 90-day expiry, and the orchestrator reproduced exactly this by
// accident against the running stack (six credential.resolve rows from a
// few seconds of ordinary page loads).
//
// This test drives the identical malformed-Authorization-header path
// (bearerToken rejects a non-Bearer scheme before any identity.Service
// call is even made, so no test identity service setup is needed) and
// checks the SAME two structural properties
// TestWithIdentityThrottlesCredentialInURLPerSource does: a rejection
// measurably raises the source's currentDelay (recordFailure ran), and a
// second offense from the same source actually requests a sleep (delay
// ran) — both asserted against the limiter's own bookkeeping, never a
// wall-clock measurement, per LESSONS.md's rule against racing a
// scheduler.
func TestWithIdentityThrottlesCredentialResolveFailurePerSource(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	clock := &fakeLoginClock{t: testNow}
	limiter, rec := newTestLoginLimiter(clock, 4, time.Second, 50*time.Millisecond, time.Second)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := withIdentity(svc, limiter, testLogger(), fixedClock(testNow), false)(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)
	req.RemoteAddr = "198.51.100.20:1234"
	req.Header.Set("Authorization", "Basic bm90LWEtYmVhcmVyLXRva2Vu")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	// The malformed Authorization header does not itself reject the
	// request — resolveCredential resolves ac.ok=false and withIdentity
	// still calls next, per this file's doc comment on authContext's
	// deliberate two-state design (ADR-024 decision 6).
	if rr.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a resolution failure must not itself block the request; only the audit write is throttled)", rr.Result().StatusCode)
	}

	source := loginSource(req)
	if d := limiter.currentDelay(source); d == 0 {
		t.Errorf("currentDelay(%q) after one credential-resolution-failure = 0, want > 0 — the failure must feed the per-source throttle", source)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)
	req2.RemoteAddr = "198.51.100.20:1234"
	req2.Header.Set("Authorization", "Basic bm90LWEtYmVhcmVyLXRva2Vu")
	rr2 := httptest.NewRecorder()
	mw.ServeHTTP(rr2, req2)
	if n, _ := rec.calls(); n == 0 {
		t.Errorf("loginLimiter.sleep was never invoked for a second credential-resolution offense from an already-penalized source")
	}
}

// TestWithIdentityNilLimiterDoesNotPanic proves the nil-safety this
// package's tests rely on elsewhere (every test in this file except the
// one above builds its API through [New], which always constructs a real
// loginLimiter — this is the one caller of withIdentity outside that path,
// and the only one that can pass nil).
func TestWithIdentityNilLimiterDoesNotPanic(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := withIdentity(svc, nil, testLogger(), fixedClock(testNow), false)(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes?tok="+identity.TokenPrefix+"leaked", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Result().StatusCode)
	}
}

// This file is the HTTP-level companion to loginlimiter_test.go's whitebox
// unit tests: it drives POST /api/v1/session through a real API and a real
// identity.Service (never a fake), proving ADR-024 decision 8's per-source
// throttle behaves correctly at the layer that actually calls it — the
// exact seam a review finding caught completely untested (fastLoginOptions
// was declared and never called before this file existed).
//
// Both tests below share one harness (throttleHarness) that builds up a
// source's per-source delay to its cap with real, but deliberately tiny,
// argon2id-cost failed logins, then races a THIRD request against a
// request still inside its own delay window — a structural race, not a
// tuned one: the racing request's own delay/queueWait bounds are set
// (loginRaceStagger below) at least 10x smaller than the throttled
// source's capped delay, so which goroutine the Go scheduler happens to
// run first is irrelevant to the outcome as long as the stagger elapses
// before the throttled request's delay does — see loginRaceStagger's own
// comment for the exact margin.

// loginRaceDelay/loginRaceQueueWait/loginRaceStagger size
// throttleHarness's timing so the two tests in this file need real (but
// small) sleeps rather than a fixed wall-clock read in an assertion —
// CLAUDE.md's "no real sleeps... in assertions" is honored by never
// asserting on how long anything took, only on which HTTP status came
// back, with generous one-directional margins rather than a tuned race.
const (
	// loginRaceDelay is the throttled source's capped per-source delay
	// (LoginPerSourceDelay == LoginMaxDelay, so a single failure already
	// reaches the cap and every subsequent request from that source sleeps
	// for exactly this long before ever contending for a slot). Sized well
	// above a realistic worst-case argon2id verification time (decision 1's
	// 64 MiB/time-2 parameters, which run meaningfully slower than usual
	// under `go test -race`'s memory-access instrumentation and slower
	// still on a loaded CI runner) — see loginRaceQueueWait's comment for
	// why that matters: an earlier, tighter version of this constant made
	// this file's own tests flaky under -race for exactly that reason.
	loginRaceDelay = 3 * time.Second

	// loginRaceQueueWait is how long a request queues for a concurrency
	// slot before giving up. It does NOT need to exceed a real argon2id
	// verification's duration — under the fix, the racing request always
	// finds the sole slot free (the throttled source is still sleeping
	// OUTSIDE the semaphore, per the delay-before-acquire ordering) and so
	// never queues at all, regardless of how long its own verification
	// takes. It only needs to be comfortably shorter than loginRaceDelay,
	// so a request that DOES end up genuinely waiting behind a throttled
	// source's held slot (the old, buggy ordering this file's tests exist
	// to catch) times out well before that source's delay would have
	// elapsed anyway.
	loginRaceQueueWait = time.Second

	// loginRaceStagger is how long this file's harness waits after firing
	// the throttled source's second request before firing the racing
	// third request — long enough that the throttled request has
	// certainly been scheduled and entered its delay/acquire sequence
	// (microseconds, in practice, for a goroutine that does nothing before
	// that), short enough (well under a third of loginRaceDelay) that it
	// is still deep inside loginRaceDelay's sleep no matter how loaded the
	// machine running this test is.
	loginRaceStagger = loginRaceDelay / 15

	// loginRaceCollectTimeout bounds how long this file's tests wait to
	// collect the throttled goroutine's own eventual result: generously
	// above loginRaceDelay plus a realistic argon2id verification, never a
	// tuned value — see loginRaceDelay's own comment on why that headroom
	// matters under -race.
	loginRaceCollectTimeout = 15 * time.Second
)

// throttleHarness builds an API with one principal and a concurrency
// bound of 1 (so a single held slot is enough to exhaust it), sends one
// failed login from throttledSource to push its delay to loginRaceDelay,
// then fires a second request from throttledSource (racingRequestBody,
// always the correct password — proving ADR-024 decision 8's "a correct
// password... still succeeds, just slower" even from the source under
// test) and, loginRaceStagger later, a THIRD request from raceSource with
// raceName/racePassword. It returns both the throttled second request's
// eventual status (collected async, generous bound) and the racing third
// request's status.
func throttleHarness(t *testing.T, raceSource, raceName, racePassword string) (throttledStatus, raceStatus int) {
	t.Helper()
	svc := newTestIdentityService(t, fixedClock(testNow))
	mustCreatePrincipal(t, svc, "victim", identity.RoleOperator)

	api := New(authTestDeps(svc), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		LoginConcurrency: 1, LoginQueueWait: loginRaceQueueWait,
		LoginPerSourceDelay: loginRaceDelay, LoginMaxDelay: loginRaceDelay,
	})

	const throttledSource = "198.51.100.7:5000"

	// One failure from throttledSource is already enough to reach the cap
	// (LoginPerSourceDelay == LoginMaxDelay == loginRaceDelay).
	failReq := newJSONRequest(t, http.MethodPost, "/api/v1/session",
		`{"name":"victim","password":"wrong","deviceLabel":"x"}`, map[string]string{"Sec-Fetch-Site": "same-origin"})
	failReq.RemoteAddr = throttledSource
	failResp, failBody := doRawRequest(t, api.Handler, failReq)
	if failResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("setup: failed login status = %d, want 401; body: %s", failResp.StatusCode, failBody)
	}

	throttledDone := make(chan int, 1)
	go func() {
		req := newJSONRequest(t, http.MethodPost, "/api/v1/session",
			`{"name":"victim","password":"`+testPassword+`","deviceLabel":"x"}`, map[string]string{"Sec-Fetch-Site": "same-origin"})
		req.RemoteAddr = throttledSource
		resp, _ := doRawRequest(t, api.Handler, req)
		throttledDone <- resp.StatusCode
	}()

	time.Sleep(loginRaceStagger)

	raceReq := newJSONRequest(t, http.MethodPost, "/api/v1/session",
		`{"name":"`+raceName+`","password":"`+racePassword+`","deviceLabel":"x"}`, map[string]string{"Sec-Fetch-Site": "same-origin"})
	raceReq.RemoteAddr = raceSource
	raceResp, _ := doRawRequest(t, api.Handler, raceReq)
	raceStatus = raceResp.StatusCode

	select {
	case throttledStatus = <-throttledDone:
	case <-time.After(loginRaceCollectTimeout):
		t.Fatalf("timed out waiting for the throttled source's own correct-password login to complete")
	}
	return throttledStatus, raceStatus
}

// TestCorrectPasswordFromThrottledSourceStillSucceedsWhileAnotherSourceQueues
// is finding 2's own regression guard, cited by name from loginlimiter.go's
// doc comment. It proves BOTH halves of ADR-024 decision 8 that a review
// finding, reproduced against the real binary, showed the shipped code
// violated: a correct password from a throttled source is never refused
// (only slowed — throttledStatus below), and — the actual bug — a
// DIFFERENT source is never made to pay for another source's throttling
// (raceStatus below must be 200, not 429). Before the delay-before-acquire
// reordering fix, the throttled source's own request held the sole
// concurrency slot for its entire sleep, so the racing different-source
// request queued for loginRaceQueueWait and was rejected — this test fails
// against that ordering and passes against the fix (confirmed by
// reverting the reordering locally; see this task's report).
func TestCorrectPasswordFromThrottledSourceStillSucceedsWhileAnotherSourceQueues(t *testing.T) {
	throttledStatus, raceStatus := throttleHarness(t, "203.0.113.9:4000", "victim", testPassword)

	if throttledStatus != http.StatusOK {
		t.Errorf("throttled source's own correct-password login: status = %d, want 200 (a throttled source's correct password must still succeed, ADR-024 decision 8)", throttledStatus)
	}
	if raceStatus != http.StatusOK {
		t.Errorf("a DIFFERENT source's correct-password login while another source was throttled: status = %d, want 200 — one source's throttling must never delay or refuse a different source (this is the exact operator-lockout shape decision 8 exists to prevent)", raceStatus)
	}
}

// TestLoginThrottleAppliesAcrossDifferentPrincipalsFromTheSameSource is the
// converse property, proving the throttle is keyed on SOURCE, never on
// principal identity: after one principal ("victim") fails a login from a
// given address, a DIFFERENT principal ("bystander") attempting a login
// from the SAME address must inherit the identical accumulated delay —
// not get a fresh, unthrottled budget merely for naming someone else. If
// this package's per-source throttle were ever keyed on the request's
// principal name instead of loginSource(r) (the mutation a review pass
// confirmed the existing suite never caught), bystander would present as
// a brand-new, never-failed key.
//
// This drives handlers.handleCreateSession directly (a whitebox call, not
// through the full HTTP stack New builds) against a real loginLimiter —
// the same technique TestWithIdentityThrottlesCredentialInURLPerSource
// uses — specifically so this test can check the property with NO real
// sleep, NO concurrency race, and NO wall-clock duration read in an
// assertion (this package's testing standard forbids all three): victim's
// failure must raise currentDelay(loginSource(...)) for the shared
// address, and bystander's OWN request — a different principal name, the
// identical address — must observe that SAME raised delay, checked
// directly against the limiter rather than inferred from how long a
// request took or from a queued-request race.
func TestLoginThrottleAppliesAcrossDifferentPrincipalsFromTheSameSource(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	mustCreatePrincipal(t, svc, "victim", identity.RoleOperator)
	mustCreatePrincipal(t, svc, "bystander", identity.RoleOperator)

	clock := &fakeLoginClock{t: testNow}
	limiter, _ := newTestLoginLimiter(clock, 4, time.Second, 50*time.Millisecond, time.Second)
	h := &handlers{deps: authTestDeps(svc), clock: fixedClock(testNow), logger: testLogger(), loginLimiter: limiter}

	const sharedSource = "198.51.100.7:5000"

	failReq := newJSONRequest(t, http.MethodPost, "/api/v1/session",
		`{"name":"victim","password":"wrong","deviceLabel":"x"}`, map[string]string{"Sec-Fetch-Site": "same-origin"})
	failReq.RemoteAddr = sharedSource
	rec := httptest.NewRecorder()
	h.handleCreateSession(rec, failReq)
	if rec.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("setup: victim's failed login status = %d, want 401", rec.Result().StatusCode)
	}

	if d := limiter.currentDelay(loginSource(failReq)); d == 0 {
		t.Fatalf("currentDelay for the shared source after victim's failure = 0, want > 0 — this test's own setup did not throttle the source, so the assertion below would prove nothing")
	}

	// bystander's request never needs to actually run: loginSource is a
	// pure function of RemoteAddr, and currentDelay is a pure function of
	// what the limiter has recorded for that source string — the property
	// under test is exactly "these two agree", which this checks directly.
	byReq := newJSONRequest(t, http.MethodPost, "/api/v1/session",
		`{"name":"bystander","password":"`+testPassword+`","deviceLabel":"x"}`, map[string]string{"Sec-Fetch-Site": "same-origin"})
	byReq.RemoteAddr = sharedSource
	if d := limiter.currentDelay(loginSource(byReq)); d == 0 {
		t.Errorf("currentDelay for bystander's own source = 0, want > 0 (inherited from victim's failure) — the throttle must be keyed on source, not on which principal name is being attempted")
	}
}

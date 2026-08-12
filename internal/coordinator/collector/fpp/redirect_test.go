package fpp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// --- Step 5 review finding 1: GET-only is not read-only ---------------------

// redirectRecorder is a throwaway server that stands in for "the confused
// deputy's target" — a completely different host this Collector must never
// actually reach, even though a real net/http client would happily follow a
// 3xx there by default. Every one of this file's tests asserts hits stays
// at 0 for the whole test, which is a stronger claim than "the response
// body looks like a failure": it proves the second host was never
// contacted at all, not merely that its response was discarded.
type redirectRecorder struct {
	hits atomic.Int32
	srv  *httptest.Server
}

func newRedirectRecorder(t *testing.T) *redirectRecorder {
	t.Helper()
	r := &redirectRecorder{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// redirectTargetPath is the exact live command shape section 0 of the Step
// 5 contract names: a real FPP invokes commands over GET at
// /api/command/... , so a redirect landing there is not a benign
// mis-route, it is a command invocation on whatever answers at the
// redirected host.
const redirectTargetPath = "/api/command/Start%20Playlist/Christmas?repeat=1"

// TestRedirectsNeverFollowedOnAnyPolledEndpoint is the tripwire test the
// Step 5 review asked for by name: every endpoint Poll fetches
// (/api/fppd/status, /api/fppd/multiSyncSystems, /api/fppd/ports,
// /api/system/info) is made to 302 to redirectRecorder's URL, and the
// recorder must never be touched — not once, across a full Poll call — no
// matter which of the four endpoints the redirect came from.
//
// Before trusting this test, fpp.go's withDefaults was reverted to not
// force CheckRedirect at all (i.e. Go's default "follow up to 10
// redirects") and confirmed to make this test fail with the recorder
// actually hit once per redirected endpoint; see this package's Step 5
// review-fix report for that verification.
func TestRedirectsNeverFollowedOnAnyPolledEndpoint(t *testing.T) {
	recorder := newRedirectRecorder(t)
	target := recorder.srv.URL + redirectTargetPath

	srv := newFPPServer()
	for _, path := range []string{
		"/api/fppd/status", "/api/fppd/multiSyncSystems", "/api/fppd/ports", "/api/system/info",
	} {
		srv.serveRedirect(path, target)
	}
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now)})
	obs, _ := c.Poll(context.Background())

	if got := recorder.hits.Load(); got != 0 {
		t.Fatalf("redirect target was contacted %d time(s); this Collector must NEVER follow a redirect, to any host, for any endpoint", got)
	}

	// The 3xx must surface as an honest collection_failed, not a silent
	// retry and not a fabricated success — contract section 3's "read-only
	// means read-only" and Step 5 review finding 1's explicit requirement.
	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.reachable Absence = %q, want collection_failed for a 3xx response that was correctly not followed", reachable.Absence)
	}
	if !contains(reachable.Reason, "302") {
		t.Errorf("fpp.reachable Reason = %q, want it to mention the 3xx status actually received", reachable.Reason)
	}

	msSig := findSignal(t, obs, SignalMultiSyncSystems)
	if msSig.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.multisync.systems Absence = %q, want collection_failed", msSig.Absence)
	}
	for _, sig := range portsFailureSignals {
		got := findSignal(t, obs, sig)
		if got.Absence != observation.StateCollectionFailed {
			t.Errorf("signal %q Absence = %q, want collection_failed", sig, got.Absence)
		}
	}
	for _, sig := range systemInfoStaticSignals {
		got := findSignal(t, obs, sig)
		if got.Absence != observation.StateCollectionFailed {
			t.Errorf("signal %q Absence = %q, want collection_failed", sig, got.Absence)
		}
	}
}

// TestRedirectsNeverFollowedWithCallerSuppliedPermissiveClient is the
// "caller-supplied client" half the review demanded: Options.HTTPClient
// lets a caller hand this package their own *http.Client, and this proves
// the guarantee holds even when that client's own CheckRedirect is
// explicitly permissive (the shape a client shared for some other purpose
// might already have) — not merely when Options.HTTPClient is left nil.
//
// Before trusting this test, withDefaults was changed to only force
// CheckRedirect when o.HTTPClient == nil (i.e. leaving a caller-supplied
// client's own CheckRedirect untouched) and confirmed to make this test
// fail, with the recorder actually hit; see the Step 5 review-fix report.
func TestRedirectsNeverFollowedWithCallerSuppliedPermissiveClient(t *testing.T) {
	recorder := newRedirectRecorder(t)
	target := recorder.srv.URL + redirectTargetPath

	srv := newFPPServer()
	srv.serveRedirect("/api/fppd/status", target)
	srv.serveStatus("/api/fppd/multiSyncSystems", http.StatusServiceUnavailable)
	ts := srv.start(t)

	// A caller-supplied client with an explicit, permissive CheckRedirect
	// — "always follow" — the exact opposite of what this package needs,
	// supplied the way a shared client built for some other caller's
	// purpose plausibly could be.
	followAlways := func(*http.Request, []*http.Request) error { return nil }
	permissive := &http.Client{CheckRedirect: followAlways}

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now), HTTPClient: permissive})
	obs, _ := c.Poll(context.Background())

	if got := recorder.hits.Load(); got != 0 {
		t.Fatalf("redirect target was contacted %d time(s) despite a caller-supplied client; this package's guarantee must hold regardless of what a caller's *http.Client was configured with", got)
	}

	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.reachable Absence = %q, want collection_failed", reachable.Absence)
	}

	// The original caller's client must not have been mutated in place —
	// withDefaults takes a defensive copy (Options.HTTPClient's doc
	// comment) so a client shared across multiple fpp.New calls, or reused
	// by the caller for something else entirely, is not silently altered
	// by this package as a side effect.
	if permissive.CheckRedirect == nil {
		t.Errorf("the caller's original *http.Client.CheckRedirect was set to nil; it must never be mutated, only copied from")
	}
	if err := permissive.CheckRedirect(nil, nil); err != nil {
		t.Errorf("the caller's original *http.Client.CheckRedirect no longer behaves as the caller configured it (err = %v), want nil (unchanged, permissive) — this package must copy, never mutate", err)
	}
}

// TestRedirectsNeverFollowedWithCallerSuppliedNilCheckRedirectClient covers
// the other caller-supplied shape: a *http.Client with CheckRedirect left
// at its zero value (nil), which for a raw http.Client means "follow up to
// 10 redirects" — Go's own default, not a permissive choice the caller
// made deliberately. This must be refused exactly the same as the nil
// Options.HTTPClient case.
func TestRedirectsNeverFollowedWithCallerSuppliedNilCheckRedirectClient(t *testing.T) {
	recorder := newRedirectRecorder(t)
	target := recorder.srv.URL + redirectTargetPath

	srv := newFPPServer()
	srv.serveRedirect("/api/fppd/status", target)
	srv.serveStatus("/api/fppd/multiSyncSystems", http.StatusServiceUnavailable)
	ts := srv.start(t)

	bareClient := &http.Client{} // CheckRedirect left nil: Go's own default-follow behavior

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now), HTTPClient: bareClient})
	c.Poll(context.Background())

	if got := recorder.hits.Load(); got != 0 {
		t.Fatalf("redirect target was contacted %d time(s) with a bare *http.Client (nil CheckRedirect, Go's own default-follow behavior); this package must override that default", got)
	}
}

// --- Step 5 review finding 9: base URL path/query/fragment are unvalidated -

// TestNewRejectsBaseURLWithPath, ...WithQuery, ...WithFragment prove
// fpp.New refuses a configured base URL carrying anything beyond
// scheme+host: fetch builds every request path via plain string
// concatenation (c.baseURL+path), so a base URL like
// http://host/api/command/Start%20Playlist?arg= turns into
// "GET /api/command/Start Playlist?arg=/api/fppd/status" for every request
// this Collector ever makes — lower severity than finding 1 (this needs a
// config-file mistake, not merely a malicious FPP response), but closing
// it means this package's GET-only guarantee no longer depends on every
// requested path happening to still land under /api/ by coincidence.
func TestNewRejectsBaseURLWithPath(t *testing.T) {
	if _, err := New("player-01", "http://10.0.1.20/api/command/Start%20Playlist", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a base URL carrying a path")
	}
}

func TestNewRejectsBaseURLWithQuery(t *testing.T) {
	if _, err := New("player-01", "http://10.0.1.20?arg=1", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a base URL carrying a query")
	}
}

func TestNewRejectsBaseURLWithFragment(t *testing.T) {
	if _, err := New("player-01", "http://10.0.1.20#frag", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a base URL carrying a fragment")
	}
}

// TestNewAcceptsBaseURLWithBareTrailingSlash is the non-regression check:
// a base URL of exactly "http://host/" (Path == "/", the ordinary shape a
// browser address bar or a careless copy-paste produces) must still be
// accepted — only a REAL path beyond that bare slash is rejected.
func TestNewAcceptsBaseURLWithBareTrailingSlash(t *testing.T) {
	if _, err := New("player-01", "http://10.0.1.20/", Options{}); err != nil {
		t.Fatalf("New() error = %v, want a bare trailing slash to be accepted", err)
	}
}

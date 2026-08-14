package resolume

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// --- Required test 1: a redirect is refused, never followed -----------------
//
// Mirrors internal/coordinator/collector/fpp/redirect_test.go's own
// TestRedirectsNeverFollowedOnAnyPolledEndpoint. D-1 issues only GETs, but
// this package's own doc comment states the reason this defence still
// applies: Resolume's REST API serves destructive POSTs and DELETEs on
// the same host, so a coordinator that silently followed a 3xx from
// /product could be turned into a confused deputy against one of those.

// redirectRecorder is a throwaway server standing in for "the confused
// deputy's target" — a completely different host this package must never
// actually reach. Asserting hits stays at 0 proves the second host was
// never contacted at all, not merely that its response was discarded.
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

// redirectTargetPath stands in for a destructive Resolume operation on
// the redirected-to host — e.g. a disconnect-all a coordinator must never
// be tricked into issuing.
const redirectTargetPath = "/api/v1/composition/disconnect-all"

// TestClientNeverFollowsARedirect proves the defence at the transport
// layer: a 302 from /product must never be followed, and must surface as
// a *StatusError naming the 3xx, exactly like any other non-2xx response.
func TestClientNeverFollowsARedirect(t *testing.T) {
	recorder := newRedirectRecorder(t)
	target := recorder.srv.URL + redirectTargetPath

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = c.Product(context.Background())
	if err == nil {
		t.Fatalf("Product() error = nil, want a collection failure for a 302 that must not be followed")
	}
	if got := recorder.hits.Load(); got != 0 {
		t.Fatalf("redirect target was contacted %d time(s); this Client must NEVER follow a redirect", got)
	}
	if got := ClassifyError(err); got != "http status 302" {
		t.Errorf("ClassifyError(%v) = %q, want it to name the 302 status", err, got)
	}
}

// TestCollectorRedirectBecomesCollectionFailedNeverFollowed is the
// required test at the collector layer: a redirect from /product must
// become a collection_failed observation for BOTH signals, and the
// redirect target must never be contacted.
//
// Before trusting this test, refuseRedirects was temporarily removed from
// NewClient (leaving Go's zero-value "follow up to 10 redirects"
// default) and this test was re-run: it failed with the recorder actually
// hit once, and Product() returning nil error with the recorder's 200 OK
// silently accepted as if it had come from Resolume itself. Restored
// afterward.
func TestCollectorRedirectBecomesCollectionFailedNeverFollowed(t *testing.T) {
	recorder := newRedirectRecorder(t)
	target := recorder.srv.URL + redirectTargetPath

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()

	now := time.Now()
	c, err := New("resolume-main", srv.URL, Options{Now: fixedClock(&now)})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Errorf("Poll() complete = false, want true (see Poll's doc comment: there is no backoff in this seam)")
	}
	if got := recorder.hits.Load(); got != 0 {
		t.Fatalf("redirect target was contacted %d time(s); Poll must NEVER follow a redirect", got)
	}

	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("resolume.reachable Absence = %q, want collection_failed for a 3xx that was correctly not followed", reachable.Absence)
	}
	if !contains(reachable.Reason, "302") {
		t.Errorf("resolume.reachable Reason = %q, want it to mention the 3xx status actually received", reachable.Reason)
	}

	product := findSignal(t, obs, SignalProduct)
	if product.Absence != observation.StateCollectionFailed {
		t.Errorf("resolume.product Absence = %q, want collection_failed", product.Absence)
	}

	// StateAt must never read healthy/current off a redirect that was
	// correctly refused — the exact CLAUDE.md rule this test's name is a
	// claim about.
	if got := reachable.StateAt(now); got != observation.StateCollectionFailed {
		t.Errorf("resolume.reachable.StateAt(now) = %q, want collection_failed, never a value and never healthy", got)
	}
}

// TestClientRefusesRedirectWithCallerSuppliedPermissiveClient proves the
// guarantee holds even when a caller hands NewClient an *http.Client whose
// own CheckRedirect is already permissive — the shape a client shared for
// some other purpose might already have.
func TestClientRefusesRedirectWithCallerSuppliedPermissiveClient(t *testing.T) {
	recorder := newRedirectRecorder(t)
	target := recorder.srv.URL + redirectTargetPath

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()

	followAlways := func(*http.Request, []*http.Request) error { return nil }
	permissive := &http.Client{CheckRedirect: followAlways}

	c, err := NewClient(srv.URL, ClientOptions{HTTPClient: permissive})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = c.Product(context.Background())
	if err == nil {
		t.Fatalf("Product() error = nil, want a collection failure")
	}
	if got := recorder.hits.Load(); got != 0 {
		t.Fatalf("redirect target was contacted %d time(s) despite a caller-supplied permissive client", got)
	}

	// The caller's original client must not have been mutated in place.
	if permissive.CheckRedirect == nil {
		t.Errorf("the caller's original *http.Client.CheckRedirect was set to nil; it must never be mutated, only copied from")
	}
	if err := permissive.CheckRedirect(nil, nil); err != nil {
		t.Errorf("the caller's original *http.Client.CheckRedirect no longer behaves as configured (err = %v); NewClient must copy, never mutate", err)
	}
}

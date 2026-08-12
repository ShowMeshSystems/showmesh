package fppcommand

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// --- New: base URL validation, mirroring internal/coordinator/collector/fpp's
// own rules (independently reproduced — see this package's doc comment
// and client.go's [New] doc comment for why that duplication is
// deliberate) ---

func TestNewRejectsBaseURLWithUserinfo(t *testing.T) {
	if _, err := New("http://user:pass@10.0.1.20", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a base URL carrying userinfo")
	}
}

func TestNewRejectsBaseURLWithPath(t *testing.T) {
	if _, err := New("http://10.0.1.20/api/command/Start%20Playlist", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a base URL carrying a path")
	}
}

func TestNewRejectsBaseURLWithQuery(t *testing.T) {
	if _, err := New("http://10.0.1.20?arg=1", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a base URL carrying a query")
	}
}

func TestNewRejectsBaseURLWithFragment(t *testing.T) {
	if _, err := New("http://10.0.1.20#frag", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a base URL carrying a fragment")
	}
}

func TestNewRejectsNonHTTPScheme(t *testing.T) {
	if _, err := New("ftp://10.0.1.20", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a non-http(s) scheme")
	}
}

func TestNewRejectsMissingHost(t *testing.T) {
	if _, err := New("http://", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a missing host")
	}
}

func TestNewAcceptsBareTrailingSlash(t *testing.T) {
	if _, err := New("http://10.0.1.20/", Options{}); err != nil {
		t.Fatalf("New() error = %v, want a bare trailing slash to be accepted", err)
	}
}

// --- Invoke: request shape ---

// TestInvokeIssuesGETAgainstEscapedCommandPath proves the exact request
// shape a real FPP command endpoint expects: GET, no body, the command
// name path-escaped (a space becomes %20) under /api/command/.
func TestInvokeIssuesGETAgainstEscapedCommandPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Stopped"))
	}))
	defer srv.Close()

	c, err := New(srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	out, err := c.StopPlaylist(context.Background())
	if err != nil {
		t.Fatalf("StopPlaylist() error = %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/api/command/Stop%20Now" {
		t.Errorf("path = %q, want /api/command/Stop%%20Now", gotPath)
	}
	if out.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", out.StatusCode)
	}
	if out.Body != "Stopped" {
		t.Errorf("Body = %q, want %q", out.Body, "Stopped")
	}
}

// TestInvokeNon2xxIsAnError proves a non-2xx (FPP refusing or erroring on
// a command, e.g. an unrecognized command name) is reported as an error
// carrying the status, never a silent success.
func TestInvokeNon2xxIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
	}))
	defer srv.Close()

	c, err := New(srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = c.StopPlaylist(context.Background())
	if err == nil {
		t.Fatalf("StopPlaylist() error = nil, want an error for a 404 response")
	}
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %v, want it to wrap *httpStatusError", err)
	}
	if statusErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", statusErr.StatusCode)
	}
}

// TestInvokeUnreachableIsAnError proves a transport-level failure (nothing
// listening) is a plain error, never confused with an *httpStatusError —
// the two are how internal/coordinator/api's handler tells "FPP answered
// and refused" apart from "FPP could not be reached at all."
func TestInvokeUnreachableIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close() // now nothing is listening at url

	c, err := New(url, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = c.StopPlaylist(context.Background())
	if err == nil {
		t.Fatalf("StopPlaylist() error = nil, want an error for an unreachable instance")
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		t.Fatalf("error = %v, want a transport failure, not an *httpStatusError (nothing ever answered)", err)
	}
}

// --- Redirect refusal: the property this package exists to prove for
// the write half of Step 5's "GET-only is not read-only" lesson ---

// redirectRecorder mirrors internal/coordinator/collector/fpp's own
// redirectRecorder (redirect_test.go), reproduced here rather than shared
// for the identical reason [refuseRedirects] is its own copy: this
// package's guarantee must be provable without depending on that
// package's test helpers continuing to exist or behave identically.
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

// TestRedirectNeverFollowed is the sharper version of the collector's own
// redirect test: here, a followed redirect would not merely mis-read a
// status document, it would dispatch a SECOND command — FPP's own — on
// whatever host and path the Location header names. The recorder must
// never be touched, not once.
func TestRedirectNeverFollowed(t *testing.T) {
	recorder := newRedirectRecorder(t)
	target := recorder.srv.URL + "/api/command/Start%20Playlist/Christmas?repeat=1"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()

	c, err := New(srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = c.StopPlaylist(context.Background())
	if err == nil {
		t.Fatalf("StopPlaylist() error = nil, want an error for a 302 response that was correctly not followed")
	}
	if got := recorder.hits.Load(); got != 0 {
		t.Fatalf("redirect target was contacted %d time(s); this Client must NEVER follow a redirect, to any host, for any command", got)
	}
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusFound {
		t.Errorf("error = %v, want it to name the 302 status actually received", err)
	}
}

// TestRedirectNeverFollowedWithCallerSuppliedPermissiveClient is the
// caller-supplied-client half, mirroring the collector's own review-found
// case: a caller's *http.Client may already carry an explicitly
// permissive CheckRedirect (built for some unrelated purpose), and this
// package's guarantee must hold regardless.
func TestRedirectNeverFollowedWithCallerSuppliedPermissiveClient(t *testing.T) {
	recorder := newRedirectRecorder(t)
	target := recorder.srv.URL + "/api/command/Start%20Playlist"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()

	followAlways := func(*http.Request, []*http.Request) error { return nil }
	permissive := &http.Client{CheckRedirect: followAlways}

	c, err := New(srv.URL, Options{HTTPClient: permissive})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := c.StopPlaylist(context.Background()); err == nil {
		t.Fatalf("StopPlaylist() error = nil, want an error for a 302 response")
	}
	if got := recorder.hits.Load(); got != 0 {
		t.Fatalf("redirect target was contacted %d time(s) despite a caller-supplied permissive client", got)
	}
	// The caller's original client must not have been mutated in place.
	if err := permissive.CheckRedirect(nil, nil); err != nil {
		t.Errorf("the caller's original *http.Client.CheckRedirect no longer behaves as configured (err = %v); must be copied, never mutated", err)
	}
}

// TestRedirectNeverFollowedWithBareClient covers a caller-supplied
// *http.Client with CheckRedirect left nil — Go's own "follow up to 10
// redirects" default, not a deliberate choice.
func TestRedirectNeverFollowedWithBareClient(t *testing.T) {
	recorder := newRedirectRecorder(t)
	target := recorder.srv.URL + "/api/command/Start%20Playlist"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()

	c, err := New(srv.URL, Options{HTTPClient: &http.Client{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := c.StopPlaylist(context.Background()); err == nil {
		t.Fatalf("StopPlaylist() error = nil, want an error for a 302 response")
	}
	if got := recorder.hits.Load(); got != 0 {
		t.Fatalf("redirect target was contacted %d time(s) with a bare *http.Client", got)
	}
}

// --- Invoke never issues anything but GET ---

// TestInvokeNeverSendsNonGET is this package's own version of the
// collector's "GET-only" guarantee, restated for a package that exists
// specifically to dispatch a command: even so, every request THIS package
// itself constructs is a GET (matching FPP's own command invocation
// mechanism, which has no other verb) — proven directly rather than
// assumed from reading Invoke's one http.NewRequestWithContext call site.
func TestInvokeNeverSendsNonGET(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := c.Invoke(context.Background(), "Some Command"); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	for _, m := range methods {
		if m != http.MethodGet {
			t.Errorf("observed method %q, want only GET", m)
		}
	}
	if len(methods) == 0 {
		t.Fatalf("no request observed at all")
	}
}

// TestInvokeEscapesArbitraryCommandNames is a narrower check than the
// StopPlaylist-specific path test above: any command name reaches FPP
// correctly escaped, not just the one this step's endpoint uses.
func TestInvokeEscapesArbitraryCommandNames(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := c.Invoke(context.Background(), "All Lights Off"); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if !strings.HasSuffix(gotPath, "/api/command/All%20Lights%20Off") {
		t.Errorf("path = %q, want it to end with /api/command/All%%20Lights%%20Off", gotPath)
	}
}

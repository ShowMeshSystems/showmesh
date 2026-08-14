package fppcommand

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

// decodedCommandRequest mirrors commandRequest's wire shape for test-side
// decoding, kept separate from the production type so a test failure
// here cannot be masked by a bug in commandRequest itself.
type decodedCommandRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// TestInvokeIssuesPOSTWithJSONBody proves the exact request shape a real
// FPP command endpoint expects for the zero-argument case: POST, JSON
// content type, a body decoding to {"command":"Stop Now","args":[]} —
// asserted on the decoded JSON, never the raw bytes, since field order is
// not part of the contract.
func TestInvokeIssuesPOSTWithJSONBody(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	var gotBody decodedCommandRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		gotContentType = r.Header.Get("Content-Type")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decoding request body %q: %v", raw, err)
		}
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
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/command" {
		t.Errorf("path = %q, want /api/command", gotPath)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	want := decodedCommandRequest{Command: "Stop Now", Args: []string{}}
	if gotBody.Command != want.Command || len(gotBody.Args) != 0 {
		t.Errorf("decoded body = %+v, want %+v", gotBody, want)
	}
	if gotBody.Args == nil {
		t.Errorf("decoded args = nil, want a present empty array (args must never be omitted or null)")
	}
	if out.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", out.StatusCode)
	}
	if out.Body != "Stopped" {
		t.Errorf("Body = %q, want %q", out.Body, "Stopped")
	}
}

// TestInvokeArgsNeverNullOrAbsent proves the args key is always present
// as a JSON array, even for a nil args slice, and is never encoded as
// JSON null — capture section 1.4 measured FPP rejecting an absent args
// key, a null args, and an empty args identically (500) for a command
// with required arguments, so this package must never emit the first two
// for any command.
func TestInvokeArgsNeverNullOrAbsent(t *testing.T) {
	var raw json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decoding request body %q: %v", body, err)
		}
		argsRaw, ok := decoded["args"]
		if !ok {
			t.Fatalf("decoded body %q has no \"args\" key at all", body)
		}
		raw = argsRaw
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := c.Invoke(context.Background(), "Pause Playlist", nil); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if string(raw) == "null" {
		t.Fatalf("args encoded as JSON null for a nil []string; must encode as []")
	}
	if string(raw) != "[]" {
		t.Errorf("args = %s, want []", raw)
	}
}

// TestInvokeNon2xxIsAnError proves a non-2xx (FPP refusing or erroring on
// a command, e.g. an unrecognized command name) is reported as an error
// carrying the status, never a silent success.
func TestInvokeNon2xxIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("No Command: Stop Playlist"))
	}))
	defer srv.Close()

	c, err := New(srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = c.StopPlaylist(context.Background())
	if err == nil {
		t.Fatalf("StopPlaylist() error = nil, want an error for a 500 response")
	}
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %v, want it to wrap *httpStatusError", err)
	}
	if statusErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", statusErr.StatusCode)
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
// the write half of Step 5's "GET-only is not read-only" lesson, sharper
// still now that the outbound method is POST ---

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
	target := recorder.srv.URL + "/api/command"

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
	target := recorder.srv.URL + "/api/command"

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
	target := recorder.srv.URL + "/api/command"

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

// --- Invoke never issues anything but POST ---

// TestInvokeNeverSendsNonPOST is this package's own version of the
// collector's "GET-only" guarantee, restated for the POST form this
// package now uses: every request THIS package itself constructs is a
// POST — proven directly rather than assumed from reading Invoke's one
// http.NewRequestWithContext call site.
func TestInvokeNeverSendsNonPOST(t *testing.T) {
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
	if _, err := c.Invoke(context.Background(), "Some Command", nil); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	for _, m := range methods {
		if m != http.MethodPost {
			t.Errorf("observed method %q, want only POST", m)
		}
	}
	if len(methods) == 0 {
		t.Fatalf("no request observed at all")
	}
}

// TestInvokeSendsArbitraryCommandNamesAndArgsInBody is a narrower check
// than the StopPlaylist-specific test above: any command name and any
// args slice reach FPP correctly encoded in the JSON body, not just the
// one this step's first endpoint uses. In particular a value containing
// "/" — unreachable through the old GET-path form (capture section 1.3)
// — must round-trip untouched through the POST body.
func TestInvokeSendsArbitraryCommandNamesAndArgsInBody(t *testing.T) {
	var gotBody decodedCommandRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decoding request body %q: %v", raw, err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := c.Invoke(context.Background(), "Start Playlist", []string{"foo/bar", "true", "false"}); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if gotBody.Command != "Start Playlist" {
		t.Errorf("command = %q, want %q", gotBody.Command, "Start Playlist")
	}
	wantArgs := []string{"foo/bar", "true", "false"}
	if len(gotBody.Args) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", gotBody.Args, wantArgs)
	}
	for i, want := range wantArgs {
		if gotBody.Args[i] != want {
			t.Errorf("args[%d] = %q, want %q", i, gotBody.Args[i], want)
		}
	}
}

// --- Typed helpers: exact command name and args array per primitive ---

// commandCapture is a shared test server that decodes and records every
// dispatched command, and never rejects — used by the typed-helper table
// test below to assert exactly what each method sends.
type commandCapture struct {
	srv  *httptest.Server
	got  decodedCommandRequest
	body string
}

func newCommandCapture(t *testing.T, respond string) *commandCapture {
	t.Helper()
	cap := &commandCapture{body: respond}
	cap.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		if err := json.Unmarshal(raw, &cap.got); err != nil {
			t.Fatalf("decoding request body %q: %v", raw, err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respond))
	}))
	t.Cleanup(cap.srv.Close)
	return cap
}

func TestTypedHelpersSendExactCommandAndArgs(t *testing.T) {
	tests := []struct {
		name        string
		call        func(c *Client) (Outcome, error)
		wantCommand string
		wantArgs    []string
	}{
		{
			name:        "StopPlaylist",
			call:        func(c *Client) (Outcome, error) { return c.StopPlaylist(context.Background()) },
			wantCommand: "Stop Now",
			wantArgs:    []string{},
		},
		{
			name: "StartPlaylist",
			call: func(c *Client) (Outcome, error) {
				return c.StartPlaylist(context.Background(), "showmesh-test", true, false)
			},
			wantCommand: "Start Playlist",
			wantArgs:    []string{"showmesh-test", "true", "false"},
		},
		{
			name: "StartPlaylist all false",
			call: func(c *Client) (Outcome, error) {
				return c.StartPlaylist(context.Background(), "showmesh-test", false, true)
			},
			wantCommand: "Start Playlist",
			wantArgs:    []string{"showmesh-test", "false", "true"},
		},
		{
			name: "StopPlaylistGracefully",
			call: func(c *Client) (Outcome, error) {
				return c.StopPlaylistGracefully(context.Background(), true)
			},
			wantCommand: "Stop Gracefully",
			wantArgs:    []string{"true"},
		},
		{
			name: "StopPlaylistGracefully false",
			call: func(c *Client) (Outcome, error) {
				return c.StopPlaylistGracefully(context.Background(), false)
			},
			wantCommand: "Stop Gracefully",
			wantArgs:    []string{"false"},
		},
		{
			name:        "PausePlaylist",
			call:        func(c *Client) (Outcome, error) { return c.PausePlaylist(context.Background()) },
			wantCommand: "Pause Playlist",
			wantArgs:    []string{},
		},
		{
			name:        "ResumePlaylist",
			call:        func(c *Client) (Outcome, error) { return c.ResumePlaylist(context.Background()) },
			wantCommand: "Resume Playlist",
			wantArgs:    []string{},
		},
		{
			name:        "NextPlaylistItem",
			call:        func(c *Client) (Outcome, error) { return c.NextPlaylistItem(context.Background()) },
			wantCommand: "Next Playlist Item",
			wantArgs:    []string{},
		},
		{
			name:        "PrevPlaylistItem",
			call:        func(c *Client) (Outcome, error) { return c.PrevPlaylistItem(context.Background()) },
			wantCommand: "Prev Playlist Item",
			wantArgs:    []string{},
		},
		{
			name:        "SetVolume",
			call:        func(c *Client) (Outcome, error) { return c.SetVolume(context.Background(), 55) },
			wantCommand: "Volume Set",
			wantArgs:    []string{"55"},
		},
		{
			name:        "SetVolume zero",
			call:        func(c *Client) (Outcome, error) { return c.SetVolume(context.Background(), 0) },
			wantCommand: "Volume Set",
			wantArgs:    []string{"0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cap := newCommandCapture(t, "OK")
			c, err := New(cap.srv.URL, Options{})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if _, err := tt.call(c); err != nil {
				t.Fatalf("call error = %v", err)
			}
			if cap.got.Command != tt.wantCommand {
				t.Errorf("command = %q, want %q", cap.got.Command, tt.wantCommand)
			}
			if len(cap.got.Args) != len(tt.wantArgs) {
				t.Fatalf("args = %v, want %v", cap.got.Args, tt.wantArgs)
			}
			for i, want := range tt.wantArgs {
				if cap.got.Args[i] != want {
					t.Errorf("args[%d] = %q, want %q", i, cap.got.Args[i], want)
				}
			}
		})
	}
}

// TestStartPlaylistNeverSendsScheduleProtected proves the fourth FPP
// argument, scheduleProtected, is never sent — ADR-001 makes FPP the
// authoritative scheduler, and sending this argument would be ShowMesh
// overriding FPP's own schedule.
func TestStartPlaylistNeverSendsScheduleProtected(t *testing.T) {
	cap := newCommandCapture(t, "Playlist Starting")
	c, err := New(cap.srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := c.StartPlaylist(context.Background(), "showmesh-test", false, false); err != nil {
		t.Fatalf("StartPlaylist() error = %v", err)
	}
	if len(cap.got.Args) != 3 {
		t.Fatalf("args = %v, want exactly 3 arguments (no scheduleProtected)", cap.got.Args)
	}
}

// --- Validation: playlist name ---

// noRequestServer fails the test if it ever receives a request — used by
// every reject case below to prove a validation failure returns without
// dispatching anything.
func noRequestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request reached the server: %s %s — a rejected validation must never dispatch", r.Method, r.URL)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestValidatePlaylistName(t *testing.T) {
	longName := strings.Repeat("a", 256)
	maxName := strings.Repeat("a", 250)

	tests := []struct {
		name    string
		input   string
		wantErr error // nil means accept
	}{
		{name: "accepts a plain name", input: "showmesh-test", wantErr: nil},
		{name: "accepts a name at exactly 250 bytes", input: maxName, wantErr: nil},
		{name: "rejects empty", input: "", wantErr: ErrPlaylistNameRequired},
		{name: "rejects leading whitespace", input: " showmesh-test", wantErr: ErrPlaylistNameWhitespace},
		{name: "rejects trailing whitespace", input: "showmesh-test ", wantErr: ErrPlaylistNameWhitespace},
		{name: "rejects a name over 250 bytes", input: longName, wantErr: ErrPlaylistNameTooLong},
		{name: "rejects a NUL byte", input: "showmesh\x00test", wantErr: ErrPlaylistNameControlChar},
		{name: "rejects a newline", input: "showmesh\ntest", wantErr: ErrPlaylistNameControlChar},
		{name: "rejects DEL", input: "showmesh\x7ftest", wantErr: ErrPlaylistNameControlChar},
		{name: "rejects a forward slash", input: "foo/bar", wantErr: ErrPlaylistNameTraversal},
		{name: "rejects a backslash", input: "foo\\bar", wantErr: ErrPlaylistNameTraversal},
		{name: "rejects a traversal substring", input: "foo..bar", wantErr: ErrPlaylistNameTraversal},
		{name: "rejects a leading traversal", input: "../../etc/passwd", wantErr: ErrPlaylistNameTraversal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePlaylistName(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidatePlaylistName(%q) error = %v, want nil", tt.input, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidatePlaylistName(%q) error = nil, want %v", tt.input, tt.wantErr)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidatePlaylistName(%q) error = %v, want errors.Is match for %v", tt.input, err, tt.wantErr)
			}
			var valErr *ValidationError
			if !errors.As(err, &valErr) {
				t.Fatalf("ValidatePlaylistName(%q) error = %v, want an *ValidationError", tt.input, err)
			}
			if valErr.Field != "name" {
				t.Errorf("ValidationError.Field = %q, want %q", valErr.Field, "name")
			}
		})
	}
}

// TestStartPlaylistRejectsInvalidNameWithoutDispatching proves the typed
// helper validates before it ever builds a request.
func TestStartPlaylistRejectsInvalidNameWithoutDispatching(t *testing.T) {
	srv := noRequestServer(t)
	c, err := New(srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = c.StartPlaylist(context.Background(), "foo/bar", false, false)
	if err == nil {
		t.Fatalf("StartPlaylist() error = nil, want a validation error for a name containing \"/\"")
	}
	if !errors.Is(err, ErrPlaylistNameTraversal) {
		t.Fatalf("error = %v, want errors.Is match for ErrPlaylistNameTraversal", err)
	}
}

// --- Validation: volume ---

func TestValidateVolume(t *testing.T) {
	tests := []struct {
		name    string
		input   int
		wantErr error
	}{
		{name: "accepts 0", input: 0, wantErr: nil},
		{name: "accepts 100", input: 100, wantErr: nil},
		{name: "accepts a middle value", input: 55, wantErr: nil},
		{name: "rejects negative", input: -1, wantErr: ErrVolumeOutOfRange},
		{name: "rejects over 100", input: 101, wantErr: ErrVolumeOutOfRange},
		{name: "rejects the FPP clamp-target value", input: 999, wantErr: ErrVolumeOutOfRange},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVolume(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateVolume(%d) error = %v, want nil", tt.input, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateVolume(%d) error = %v, want errors.Is match for %v", tt.input, err, tt.wantErr)
			}
			var valErr *ValidationError
			if !errors.As(err, &valErr) {
				t.Fatalf("ValidateVolume(%d) error = %v, want a *ValidationError", tt.input, err)
			}
			if valErr.Field != "volume" {
				t.Errorf("ValidationError.Field = %q, want %q", valErr.Field, "volume")
			}
		})
	}
}

// TestSetVolumeRejectsOutOfRangeWithoutDispatching proves the typed
// helper validates before it ever builds a request — capture section 1.5
// is the reason this matters: FPP would silently clamp or coerce rather
// than reject, so a request that reached FPP with a bad volume would
// "succeed" from FPP's perspective.
func TestSetVolumeRejectsOutOfRangeWithoutDispatching(t *testing.T) {
	srv := noRequestServer(t)
	c, err := New(srv.URL, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = c.SetVolume(context.Background(), 999)
	if err == nil {
		t.Fatalf("SetVolume(999) error = nil, want a validation error")
	}
	if !errors.Is(err, ErrVolumeOutOfRange) {
		t.Fatalf("error = %v, want errors.Is match for ErrVolumeOutOfRange", err)
	}
}

// --- encodeBool: the exact two strings FPP's own parser recognizes ---

func TestEncodeBoolProducesExactStrings(t *testing.T) {
	if got := encodeBool(true); got != "true" {
		t.Errorf("encodeBool(true) = %q, want %q", got, "true")
	}
	if got := encodeBool(false); got != "false" {
		t.Errorf("encodeBool(false) = %q, want %q", got, "false")
	}
}

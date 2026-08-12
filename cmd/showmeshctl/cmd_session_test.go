package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCmdSessionUnauthenticatedIsNotAnError pins the contract this command
// exists to prove: GET /api/v1/session with no credential is a 200 body
// reporting authenticated=false, never a 401 — openapi.yaml's
// SessionResponse: "being signed out is a persistent, readable state, not
// an error a caller must catch to learn." This must exit 0, not
// exitUnauthorized.
func TestCmdSessionUnauthenticatedIsNotAnError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","authenticated":false,"principal":null,
			"session":null,"credentialForm":null,"scopes":[],"scopesState":"not_applicable","bootstrapRequired":false}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSession([]string{"--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Authenticated: no") {
		t.Errorf("stdout = %q, want it to say not authenticated", stdout.String())
	}
}

// TestCmdSessionBootstrapRequiredBanner pins ADR-024 decision 9's "loud
// and persistent" unclaimed-bootstrap signal reaching this CLI too, not
// only the browser UI: bootstrapRequired is computed and returned
// regardless of whether this particular request authenticated.
func TestCmdSessionBootstrapRequiredBanner(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","authenticated":false,"principal":null,
			"session":null,"credentialForm":null,"scopes":[],"scopesState":"not_applicable","bootstrapRequired":true}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSession([]string{"--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "REQUIRED") {
		t.Errorf("stdout = %q, want a loud bootstrap-required banner", stdout.String())
	}
}

// TestCmdSessionAuthenticatedShowsPrincipalRoleAndScopes is the load-
// bearing test for ADR-024 decision 1's promise: a human at a terminal,
// holding a bearer token, can see who they authenticated as and what they
// can do. If this rendered nothing usable, the promise would be true only
// in the ADR text.
func TestCmdSessionAuthenticatedShowsPrincipalRoleAndScopes(t *testing.T) {
	var gotAuthHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","authenticated":true,
			"principal":{"id":"p-1","name":"eric","kind":"human","role":"operator"},
			"session":null,"credentialForm":"token","scopes":["node:read","fpp:read","show:macro:run"],
			"scopesState":"current","bootstrapRequired":false}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSession([]string{"--server", ts.URL, "--token", "smsh_test123"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotAuthHeader != "Bearer smsh_test123" {
		t.Errorf("request Authorization header = %q, want the token to have been sent", gotAuthHeader)
	}
	out := stdout.String()
	for _, want := range []string{"eric", "human", "operator", "node:read", "fpp:read", "show:macro:run", "token"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
}

// TestCmdSessionScopesStateUnknownIsRenderedLoudly pins ADR-024 decision
// 12 end to end through this command: an "unknown" scopesState must not
// look identical to "current" in the text output an operator reads.
func TestCmdSessionScopesStateUnknownIsRenderedLoudly(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","authenticated":true,
			"principal":{"id":"p-1","name":"eric","kind":"human","role":"operator"},
			"session":null,"credentialForm":"token","scopes":[],
			"scopesState":"unknown","bootstrapRequired":false}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSession([]string{"--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	// Deliberately not just "contains UNKNOWN": the wire value itself is
	// the string "unknown", which upper-cases to "UNKNOWN" whether or not
	// scopesStateGlyph does any work at all — a bare pass-through would
	// pass that assertion too. Require the warning language ADR-024
	// decision 12 is actually about ("never as permissive"), which only
	// appears if the glyph function is doing its job.
	if !strings.Contains(stdout.String(), "never as permissive") {
		t.Errorf("stdout = %q, want scopesState=unknown to carry decision 12's explicit warning, not just an upper-cased pass-through of the wire value", stdout.String())
	}
}

func TestCmdSessionRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdSession([]string{"unexpected"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

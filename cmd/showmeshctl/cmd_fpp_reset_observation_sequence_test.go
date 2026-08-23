package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file tests "fpp reset-observation-sequence" (TRACK-H-H2-SPEC.md
// §5.1), following cmd_discovery_test.go's undeclare --confirm pattern:
// refusal without --confirm must never reach the coordinator, and a
// confirmed call must pin the DELETE method and path.

func TestCmdFPPResetObservationSequenceRefusesWithoutConfirmFlag(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"reset-observation-sequence", "--server", ts.URL, "u1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if called {
		t.Errorf("the coordinator was called without --confirm ever being passed")
	}
	if !strings.Contains(stderr.String(), "--confirm") {
		t.Errorf("stderr = %q, want it to name --confirm", stderr.String())
	}
}

func TestCmdFPPResetObservationSequenceWithConfirmSucceeds(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"reset-observation-sequence", "--server", ts.URL, "--confirm", "u1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	wantPath := "/api/v1/integrations/fpp/playlist-entry-observations/u1"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if !strings.Contains(stdout.String(), "u1") {
		t.Errorf("output missing confirmation:\n%s", stdout.String())
	}
}

func TestCmdFPPResetObservationSequenceRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"reset-observation-sequence", "--server", "http://example.invalid", "--confirm"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a missing instance-id argument", code)
	}
}

func TestCmdFPPResetObservationSequenceServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"https://showmesh.dev/problems/forbidden","title":"Forbidden",
			"status":403,"detail":"missing fpp:command scope","serverTime":"2026-08-16T21:00:00Z"}`))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"reset-observation-sequence", "--server", ts.URL, "--confirm", "u1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code == exitOK {
		t.Fatalf("exit code = %d, want a non-zero exit for a 403", code)
	}
}

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

// TestCmdNightPrepareSiteAppliedExitsOK proves the write shape (POST the
// exact path, decode the 202 body) and that a successful command exits 0.
func TestCmdNightPrepareSiteAppliedExitsOK(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","command":{"command":"prepare-site","outcome":"applied"},
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"preparing",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":0,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"","showCommitted":false,
			"readiness":{"state":"unknown","reason":"no readiness result recorded","sameEpoch":false,"fresh":false,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},"transition":{"state":"not_available","reason":""},
			"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"prepare-site", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/night/commands/prepare-site" {
		t.Errorf("path = %q, want /api/v1/night/commands/prepare-site", gotPath)
	}
	if !strings.Contains(stdout.String(), "applied") {
		t.Errorf("stdout = %q, want it to report the outcome", stdout.String())
	}
}

func nightProblemServer(status int, problemType string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"type":%q,"title":"refused","status":%d,"detail":"refused","serverTime":"2026-08-18T22:00:00Z"}`, problemType, status)
	}))
}

// TestCmdNightStartNotReadyExitsExitNightNotReady proves the
// night-not-ready problem type maps to exit code 26.
func TestCmdNightStartNotReadyExitsExitNightNotReady(t *testing.T) {
	ts := nightProblemServer(http.StatusConflict, problemNightNotReady)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"start", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitNightNotReady {
		t.Fatalf("exit code = %d, want exitNightNotReady (26); stderr=%s", code, stderr.String())
	}
}

// TestCmdNightStartStateRejectedExitsExitNightStateRejected proves the
// night-state-rejected problem type maps to exit code 27, distinctly from
// exitNightNotReady.
func TestCmdNightStartStateRejectedExitsExitNightStateRejected(t *testing.T) {
	ts := nightProblemServer(http.StatusConflict, problemNightStateRejected)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"start", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitNightStateRejected {
		t.Fatalf("exit code = %d, want exitNightStateRejected (27); stderr=%s", code, stderr.String())
	}
}

// TestCmdNightPreshowAmbiguousExitsExitNightAmbiguous proves the
// night-ambiguous problem type maps to exit code 28, distinctly from the
// other two.
func TestCmdNightPreshowAmbiguousExitsExitNightAmbiguous(t *testing.T) {
	ts := nightProblemServer(http.StatusConflict, problemNightAmbiguous)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"preshow", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitNightAmbiguous {
		t.Fatalf("exit code = %d, want exitNightAmbiguous (28); stderr=%s", code, stderr.String())
	}
}

// TestCmdNightStatusIsAnOpenRead proves "night status" issues a GET (never
// a write) and succeeds without a --token.
func TestCmdNightStatusIsAnOpenRead(t *testing.T) {
	var gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","session":{"id":"","configObjectId":"","configRevision":0,
			"state":"inactive","stateEnteredAt":"2026-08-18T22:00:00Z","cycle":0,"finalShowRequested":false,
			"finalShowRequestedAt":null,"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"",
			"armedShowId":"","showCommitted":false,"readiness":{"state":"unknown","reason":"no session","sameEpoch":false,
			"fresh":false,"checks":[]},"powerPhase":{"state":"unknown","reason":""},
			"transition":{"state":"not_available","reason":""},"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if !strings.Contains(stdout.String(), "inactive") {
		t.Errorf("stdout = %q, want it to report state=inactive", stdout.String())
	}
}

// TestCmdNightEndSessionSendsExpectedPath proves "night end-session" POSTs
// the expected path — the one command reachable against a degraded
// session (finding 1).
func TestCmdNightEndSessionSendsExpectedPath(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","command":{"command":"end-session","outcome":"applied","attributionDegraded":false},
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"stopped",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":0,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":true,"admissionClosedAt":"2026-08-18T22:00:00Z","shutdownIntent":"","armedShowId":"","showCommitted":false,
			"readiness":{"state":"unknown","reason":"no readiness result recorded","sameEpoch":false,"fresh":false,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},"transition":{"state":"not_available","reason":""},
			"degraded":true,"degradedReason":"ambiguous restart","attributionDegraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"end-session", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/night/commands/end-session" {
		t.Errorf("path = %q, want /api/v1/night/commands/end-session", gotPath)
	}
	if !strings.Contains(stdout.String(), "DEGRADED") {
		t.Errorf("stdout = %q, want it to report the still-degraded flag", stdout.String())
	}
}

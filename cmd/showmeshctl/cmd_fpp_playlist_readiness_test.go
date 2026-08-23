package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file tests the read-only "fpp playlist-readiness" subcommand
// (TRACK-H-H2-SPEC.md §6, §7), following
// cmd_fpp_playlist_definition_test.go's exact pattern: each drives a real
// httptest.Server and pins the request path, method, and flags.

func TestCmdFPPPlaylistReadinessReady(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","playlistId":"playlist-1","ready":true}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-readiness", "--server", ts.URL, "playlist-1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	wantPath := "/api/v1/integrations/fpp/playlists/playlist-1/readiness"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if !strings.Contains(stdout.String(), "Ready:    true") {
		t.Errorf("stdout = %q, want it to report Ready: true", stdout.String())
	}
}

func TestCmdFPPPlaylistReadinessNotReady(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","playlistId":"playlist-1","ready":false,
			"failingCondition":"definition-missing","reason":"no playlist definition is stored"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-readiness", "--server", ts.URL, "playlist-1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "definition-missing") {
		t.Errorf("stdout = %q, want it to name the failing condition", stdout.String())
	}
}

func TestCmdFPPPlaylistReadinessJSONOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","playlistId":"playlist-1","ready":true}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-readiness", "--server", ts.URL, "--output", "json", "playlist-1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"playlistId"`) {
		t.Errorf("stdout = %q, want raw JSON with a playlistId key", stdout.String())
	}
}

func TestCmdFPPPlaylistReadinessRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-readiness", "--server", "http://example.invalid"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a missing playlist-id argument", code)
	}
}

func TestCmdFPPPlaylistReadinessNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Not found",
			"status":404,"detail":"no such playlist","serverTime":"2026-08-16T21:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-readiness", "--server", ts.URL, "playlist-1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code == exitOK {
		t.Fatalf("exit code = %d, want a non-zero exit for a 404", code)
	}
}

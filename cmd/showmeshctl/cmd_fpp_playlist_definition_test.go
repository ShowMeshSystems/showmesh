package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file tests the read-only "fpp playlist-definitions" subcommands
// (FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3.6, TRACK-H-H2-SPEC.md §4 step
// 2, §7), following cmd_playlist_test.go's exact pattern: each drives a
// real httptest.Server.

func TestCmdFPPPlaylistDefinitionsList(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","definitions":[
			{"instanceUuid":"u1","playlistName":"Halloween Main","playlistHash":"`+strings.Repeat("a", 64)+`",
			 "capturedAt":"2026-08-16T20:00:00Z","receivedAt":"2026-08-16T20:00:01Z","entryCount":3,"referenced":true}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-definitions", "list", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/integrations/fpp/playlist-definitions" {
		t.Errorf("path = %q, want /api/v1/integrations/fpp/playlist-definitions", gotPath)
	}
	if !strings.Contains(stdout.String(), "Halloween Main") {
		t.Errorf("stdout = %q, want it to name the playlist", stdout.String())
	}
	if !strings.Contains(stdout.String(), "true") {
		t.Errorf("stdout = %q, want it to show referenced=true", stdout.String())
	}
}

func TestCmdFPPPlaylistDefinitionsListJSONOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","definitions":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-definitions", "list", "--server", ts.URL, "--output", "json"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"definitions"`) {
		t.Errorf("stdout = %q, want raw JSON with a definitions key", stdout.String())
	}
}

func TestCmdFPPPlaylistDefinitionGet(t *testing.T) {
	var gotPath string
	hash := strings.Repeat("a", 64)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","instanceUuid":"u1","playlistName":"Halloween Main",
			"playlistHash":"`+hash+`","definition":{"name":"Halloween Main"},
			"capturedAt":"2026-08-16T20:00:00Z","receivedAt":"2026-08-16T20:00:01Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-definitions", "get", "--server", ts.URL, "u1", hash}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	wantPath := "/api/v1/integrations/fpp/playlist-definitions/u1/" + hash
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if !strings.Contains(stdout.String(), "Halloween Main") {
		t.Errorf("stdout = %q, want it to name the playlist", stdout.String())
	}
}

func TestCmdFPPPlaylistDefinitionGetNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Not found",
			"status":404,"detail":"no stored playlist definition","serverTime":"2026-08-16T21:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-definitions", "get", "--server", ts.URL, "u1", strings.Repeat("a", 64)}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code == exitOK {
		t.Fatalf("exit code = %d, want a non-zero exit for a 404", code)
	}
}

func TestCmdFPPPlaylistDefinitionEntries(t *testing.T) {
	var gotPath string
	hash := strings.Repeat("b", 64)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","instanceUuid":"u1","playlistHash":"`+hash+`",
			"entries":[
				{"section":"mainPlaylist","position":0,"type":"sequence","sequenceName":"trackf-resting.fseq","mediaName":""}
			]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-definitions", "entries", "--server", ts.URL, "u1", hash}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	wantPath := "/api/v1/integrations/fpp/playlist-definitions/u1/" + hash + "/entries"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if !strings.Contains(stdout.String(), "trackf-resting.fseq") {
		t.Errorf("stdout = %q, want it to name the sequence file", stdout.String())
	}
	if !strings.Contains(stdout.String(), "mainPlaylist") {
		t.Errorf("stdout = %q, want it to name the section", stdout.String())
	}
}

func TestCmdFPPPlaylistDefinitionsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-definitions"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
	if !strings.Contains(stderr.String(), "playlist-definitions") {
		t.Errorf("stderr = %q, want usage text", stderr.String())
	}
}

func TestCmdFPPPlaylistDefinitionsUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-definitions", "bogus"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
}

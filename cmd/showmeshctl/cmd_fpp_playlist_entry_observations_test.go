package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file tests the read-only "fpp playlist-entry-observations"
// subcommands (FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1, TRACK-H-H2-SPEC.md
// §5, §7), following cmd_fpp_playlist_definition_test.go's exact pattern:
// each drives a real httptest.Server and pins the request path, method,
// and flags.

func TestCmdFPPPlaylistEntryObservationsList(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","observations":[
			{"instanceUuid":"u1","schemaVersion":1,"sequence":3,"playlistName":"Main","playlistHash":"`+strings.Repeat("a", 64)+`",
			 "section":"mainPlaylist","position":0,"entryKey":"`+strings.Repeat("b", 64)+`","action":"playing",
			 "observedAt":"2026-08-16T20:59:00Z","receivedAt":"2026-08-16T20:59:01Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-entry-observations", "list", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/api/v1/integrations/fpp/playlist-entry-observations" {
		t.Errorf("path = %q, want /api/v1/integrations/fpp/playlist-entry-observations", gotPath)
	}
	if !strings.Contains(stdout.String(), "u1") {
		t.Errorf("stdout = %q, want it to name the instance", stdout.String())
	}
}

func TestCmdFPPPlaylistEntryObservationsListJSONOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","observations":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-entry-observations", "list", "--server", ts.URL, "--output", "json"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"observations"`) {
		t.Errorf("stdout = %q, want raw JSON with an observations key", stdout.String())
	}
}

func TestCmdFPPPlaylistEntryReconciliation(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","instanceUuid":"u1","outcome":"resolved",
			"reason":"the observation names exactly one Playlist, entry, and Cue",
			"playlistId":"playlist-1","playlistRevision":1,"entryId":"entry-1","cueId":"cue-1","cueRevision":1,
			"definitionAvailable":true}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-entry-observations", "reconciliation", "--server", ts.URL, "u1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	wantPath := "/api/v1/integrations/fpp/playlist-entry-observations/u1/reconciliation"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if !strings.Contains(stdout.String(), "resolved") {
		t.Errorf("stdout = %q, want it to name the outcome", stdout.String())
	}
	if !strings.Contains(stdout.String(), "cue-1") {
		t.Errorf("stdout = %q, want it to name the resolved cue", stdout.String())
	}
}

func TestCmdFPPPlaylistEntryReconciliationRendersOperatorInstruction(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","instanceUuid":"u1","outcome":"stale-import",
			"reason":"the observation's playlistHash differs from the binding's",
			"playlistId":"playlist-1","playlistRevision":1,"definitionAvailable":false,
			"operatorInstruction":"Restart FPP, or re-import the playlist so the coordinator's binding and FPP agree."}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-entry-observations", "reconciliation", "--server", ts.URL, "u1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Restart FPP, or re-import the playlist so the coordinator's binding and FPP agree.") {
		t.Errorf("stdout = %q, want it to print the operator instruction", stdout.String())
	}
}

func TestCmdFPPPlaylistEntryReconciliationOmitsOperatorInstructionLineWhenAbsent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","instanceUuid":"u1","outcome":"resolved",
			"reason":"the observation names exactly one Playlist, entry, and Cue",
			"playlistId":"playlist-1","playlistRevision":1,"entryId":"entry-1","cueId":"cue-1","cueRevision":1,
			"definitionAvailable":true}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-entry-observations", "reconciliation", "--server", ts.URL, "u1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Instruction:") {
		t.Errorf("stdout = %q, want no Instruction line for a resolved (non-mismatched) outcome", stdout.String())
	}
}

func TestCmdFPPPlaylistEntryObservationsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-entry-observations"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
	if !strings.Contains(stderr.String(), "playlist-entry-observations") {
		t.Errorf("stderr = %q, want usage text", stderr.String())
	}
}

func TestCmdFPPPlaylistEntryObservationsUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-entry-observations", "bogus"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdFPPPlaylistEntryReconciliationRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"playlist-entry-observations", "reconciliation", "--server", "http://example.invalid"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a missing instance-id argument", code)
	}
}

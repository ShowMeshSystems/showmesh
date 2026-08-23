package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file tests Track H seam H1's "playlist" subcommands, following
// cmd_cue_test.go's exact pattern: each drives a real httptest.Server.

func TestCmdPlaylistListPassesShowFilter(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.playlist","objects":[
			{"id":"main","label":"Main show","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{"list", "--server", ts.URL, "--show", "halloween-2026"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.playlist" {
		t.Errorf("path = %q, want /api/v1/config/show.playlist", gotPath)
	}
	if gotQuery != "show=halloween-2026" {
		t.Errorf("query = %q, want show=halloween-2026", gotQuery)
	}
	if !strings.Contains(stdout.String(), "main") {
		t.Errorf("stdout = %q, want it to name the playlist id", stdout.String())
	}
}

func TestCmdPlaylistGet(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.playlist","id":"main","revision":1,
			"payload":{"show":"halloween-2026","name":"Main show","runner":"showmesh-audio",
				"entries":[{"id":"e1","cue":"thriller"}]},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{"get", "--server", ts.URL, "main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.playlist/main" {
		t.Errorf("path = %q, want /api/v1/config/show.playlist/main", gotPath)
	}
	if !strings.Contains(stdout.String(), "Main show") {
		t.Errorf("stdout = %q, want it to name the playlist", stdout.String())
	}
}

func TestCmdPlaylistSetSendsFullPayloadFPP(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.playlist","id":"main","revision":1,
			"payload":{"show":"halloween-2026","name":"Main show","runner":"fpp","mismatchPolicy":"hold",
				"fpp":{"instanceUuid":"u","playlistName":"p","playlistHash":"`+strings.Repeat("a", 64)+`"},
				"entries":[{"id":"e1","cue":"thriller","fpp":{"section":"main","position":0}}]},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--name", "Main show", "--runner", "fpp",
		"--mismatch-policy", "hold",
		"--fpp-json", `{"instanceUuid":"u","playlistName":"p","playlistHash":"` + strings.Repeat("a", 64) + `"}`,
		"--entries-json", `[{"id":"e1","cue":"thriller","fpp":{"section":"main","position":0}}]`,
		"main",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/show.playlist/main" {
		t.Errorf("path = %q, want /api/v1/config/show.playlist/main", gotPath)
	}

	var decoded configShowPlaylist
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if decoded.Show != "halloween-2026" || decoded.Name != "Main show" || decoded.Runner != "fpp" {
		t.Errorf("decoded = %+v, want show=halloween-2026 name=Main show runner=fpp", decoded)
	}
	if len(decoded.FPP) == 0 {
		t.Errorf("expected fpp binding on the request body, got none")
	}
	if !strings.Contains(string(decoded.Entries), `"cue":"thriller"`) {
		t.Errorf("entries = %s, want it to carry the thriller cue reference", decoded.Entries)
	}
}

func TestCmdPlaylistSetMissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{"set", "--server", "http://unused", "main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--entries-json") {
		t.Errorf("stderr = %q, want it to name --entries-json as missing", stderr.String())
	}
}

func TestCmdPlaylistSetRejectsInvalidEntriesJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{
		"set", "--server", "http://unused",
		"--show", "halloween-2026", "--name", "Main show", "--runner", "showmesh-audio",
		"--entries-json", "not-json",
		"main",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

// TestCmdPlaylistSetRejectsInvalidFPPJSON matches
// TestCmdPlaylistSetRejectsInvalidEntriesJSON, for --fpp-json.
func TestCmdPlaylistSetRejectsInvalidFPPJSON(t *testing.T) {
	requested := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--name", "Main show", "--runner", "fpp",
		"--fpp-json", "not-json",
		"--entries-json", `[{"id":"e1","cue":"thriller","fpp":{"section":"main","position":0}}]`,
		"main",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if requested {
		t.Errorf("a request was sent despite invalid --fpp-json")
	}
}

// TestCmdPlaylistSetRejectsInvalidShowmeshAudioJSON is the
// --showmesh-audio-json twin.
func TestCmdPlaylistSetRejectsInvalidShowmeshAudioJSON(t *testing.T) {
	requested := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--name", "Audio only", "--runner", "showmesh-audio",
		"--showmesh-audio-json", "not-json",
		"--entries-json", `[{"id":"e1","cue":"thriller"}]`,
		"audio-only",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if requested {
		t.Errorf("a request was sent despite invalid --showmesh-audio-json")
	}
}

// TestCmdPlaylistUnknownSubcommandIsUsageError matches
// TestCmdSurfaceUnknownSubcommandIsUsageError (cmd_surface_test.go).
func TestCmdPlaylistUnknownSubcommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{"bogus"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdPlaylistRevisions(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.playlist","revisions":[
			{"revision":1,"createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api","createdAt":"2026-08-16T20:00:00Z","active":true}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{"revisions", "--server", ts.URL, "main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.playlist/main/revisions" {
		t.Errorf("path = %q, want /api/v1/config/show.playlist/main/revisions", gotPath)
	}
}

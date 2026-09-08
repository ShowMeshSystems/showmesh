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

// This file tests the "media-playlist" subcommands, following
// cmd_playlist_test.go's exact pattern one kind over: each drives a real
// httptest.Server.

func TestCmdMediaPlaylistListPassesShowFilter(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"media.playlist","objects":[
			{"id":"porch","label":"Porch bed","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMediaPlaylist([]string{"list", "--server", ts.URL, "--show", "halloween-2026"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/media.playlist" {
		t.Errorf("path = %q, want /api/v1/config/media.playlist", gotPath)
	}
	if gotQuery != "show=halloween-2026" {
		t.Errorf("query = %q, want show=halloween-2026", gotQuery)
	}
	if !strings.Contains(stdout.String(), "porch") {
		t.Errorf("stdout = %q, want it to name the media playlist id", stdout.String())
	}
}

func TestCmdMediaPlaylistGet(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"media.playlist","id":"porch","revision":1,
			"payload":{"label":"Porch bed","show":"halloween-2026",
				"items":[{"kind":"asset","show":"halloween-2026","sequence":"seq1","target":"node1"}],
				"repeat":"none","resume":"resume","itemTransition":"sequential","maxGainDb":0},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMediaPlaylist([]string{"get", "--server", ts.URL, "porch"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/media.playlist/porch" {
		t.Errorf("path = %q, want /api/v1/config/media.playlist/porch", gotPath)
	}
	if !strings.Contains(stdout.String(), "Porch bed") {
		t.Errorf("stdout = %q, want it to name the media playlist label", stdout.String())
	}
}

func TestCmdMediaPlaylistSetSendsFullPayload(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"media.playlist","id":"porch","revision":1,
			"payload":{"label":"Porch bed","show":"halloween-2026",
				"items":[{"kind":"asset","show":"halloween-2026","sequence":"seq1","target":"node1"}],
				"repeat":"none","resume":"resume","itemTransition":"sequential","maxGainDb":0},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMediaPlaylist([]string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--label", "Porch bed",
		"--items-json", `[{"kind":"asset","show":"halloween-2026","sequence":"seq1","target":"node1"}]`,
		"--resume", "resume", "--item-transition", "sequential", "--max-gain-db", "0",
		"porch",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/media.playlist/porch" {
		t.Errorf("path = %q, want /api/v1/config/media.playlist/porch", gotPath)
	}

	var decoded configMediaPlaylist
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if decoded.Show != "halloween-2026" || decoded.Label != "Porch bed" || decoded.Resume != "resume" {
		t.Errorf("decoded = %+v, want show=halloween-2026 label=Porch bed resume=resume", decoded)
	}
	if !strings.Contains(string(decoded.Items), `"sequence":"seq1"`) {
		t.Errorf("items = %s, want it to carry the seq1 asset reference", decoded.Items)
	}
	if decoded.CrossfadeMs != nil || decoded.FadeOutMs != nil || decoded.FadeInMs != nil {
		t.Errorf("decoded optional ints = %+v, want all nil when their flags were never given", decoded)
	}
}

// TestCmdMediaPlaylistSetSendsOptionalInts proves crossfadeMs/fadeOutMs/
// fadeInMs reach the wire when their flags are given, matching a 0 value
// distinctly from "not given" (this file's own doc comment).
func TestCmdMediaPlaylistSetSendsOptionalInts(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"media.playlist","id":"porch","revision":1,
			"payload":{"label":"Porch bed","show":"halloween-2026",
				"items":[{"kind":"asset","show":"halloween-2026","sequence":"seq1","target":"node1"}],
				"repeat":"none","resume":"resume","itemTransition":"crossfade","crossfadeMs":500,
				"maxGainDb":-6,"fadeOutMs":1000,"fadeInMs":1000},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMediaPlaylist([]string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--label", "Porch bed",
		"--items-json", `[{"kind":"asset","show":"halloween-2026","sequence":"seq1","target":"node1"}]`,
		"--resume", "resume", "--item-transition", "crossfade", "--crossfade-ms", "500",
		"--max-gain-db", "-6", "--fade-out-ms", "1000", "--fade-in-ms", "1000",
		"porch",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	var decoded configMediaPlaylist
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if decoded.CrossfadeMs == nil || *decoded.CrossfadeMs != 500 {
		t.Errorf("crossfadeMs = %v, want 500", decoded.CrossfadeMs)
	}
	if decoded.MaxGainDb != -6 {
		t.Errorf("maxGainDb = %v, want -6", decoded.MaxGainDb)
	}
	if decoded.FadeOutMs == nil || *decoded.FadeOutMs != 1000 {
		t.Errorf("fadeOutMs = %v, want 1000", decoded.FadeOutMs)
	}
	if decoded.FadeInMs == nil || *decoded.FadeInMs != 1000 {
		t.Errorf("fadeInMs = %v, want 1000", decoded.FadeInMs)
	}
}

func TestCmdMediaPlaylistSetMissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdMediaPlaylist([]string{"set", "--server", "http://unused", "porch"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--items-json") {
		t.Errorf("stderr = %q, want it to name --items-json as missing", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--max-gain-db") {
		t.Errorf("stderr = %q, want it to name --max-gain-db as missing", stderr.String())
	}
}

func TestCmdMediaPlaylistSetRejectsInvalidItemsJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdMediaPlaylist([]string{
		"set", "--server", "http://unused",
		"--show", "halloween-2026", "--label", "Porch bed",
		"--items-json", "not-json",
		"--resume", "resume", "--item-transition", "sequential", "--max-gain-db", "0",
		"porch",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

func TestCmdMediaPlaylistUnknownSubcommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdMediaPlaylist([]string{"bogus"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdMediaPlaylistRevisions(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"media.playlist","revisions":[
			{"revision":1,"createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api","createdAt":"2026-08-16T20:00:00Z","active":true}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMediaPlaylist([]string{"revisions", "--server", ts.URL, "porch"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/media.playlist/porch/revisions" {
		t.Errorf("path = %q, want /api/v1/config/media.playlist/porch/revisions", gotPath)
	}
}

// TestCmdMediaPlaylistDeleteRequiresConfirm proves --confirm is checked
// locally before any request is sent, matching every other per-object
// config kind's delete verb.
func TestCmdMediaPlaylistDeleteRequiresConfirm(t *testing.T) {
	requested := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMediaPlaylist([]string{"delete", "--server", ts.URL, "porch"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if requested {
		t.Errorf("a request was sent despite missing --confirm")
	}
}

func TestCmdMediaPlaylistDelete(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMediaPlaylist([]string{"delete", "--server", ts.URL, "--confirm", "porch"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/api/v1/config/media.playlist/porch" {
		t.Errorf("path = %q, want /api/v1/config/media.playlist/porch", gotPath)
	}
	if !strings.Contains(string(gotBody), `"confirm":true`) {
		t.Errorf("body = %s, want confirm:true", gotBody)
	}
	if !strings.Contains(stdout.String(), "porch") {
		t.Errorf("stdout = %q, want it to name the deleted id", stdout.String())
	}
}

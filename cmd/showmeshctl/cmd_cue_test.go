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

// This file tests Track H seam H1's "cue" subcommands, following
// cmd_surface_test.go's exact pattern: each drives a real
// httptest.Server.

func TestCmdCueListPassesShowFilter(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.cue","objects":[
			{"id":"thriller","label":"Thriller","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{"list", "--server", ts.URL, "--show", "halloween-2026"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.cue" {
		t.Errorf("path = %q, want /api/v1/config/show.cue", gotPath)
	}
	if gotQuery != "show=halloween-2026" {
		t.Errorf("query = %q, want show=halloween-2026", gotQuery)
	}
	if !strings.Contains(stdout.String(), "thriller") {
		t.Errorf("stdout = %q, want it to name the cue id", stdout.String())
	}
}

func TestCmdCueGet(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.cue","id":"thriller","revision":1,
			"payload":{"show":"halloween-2026","name":"Thriller","outputs":{"render":{"sequence":"thriller"}}},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{"get", "--server", ts.URL, "thriller"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.cue/thriller" {
		t.Errorf("path = %q, want /api/v1/config/show.cue/thriller", gotPath)
	}
	if !strings.Contains(stdout.String(), "Thriller") {
		t.Errorf("stdout = %q, want it to name the cue", stdout.String())
	}
}

func TestCmdCueSetSendsFullPayload(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.cue","id":"thriller","revision":1,
			"payload":{"show":"halloween-2026","name":"Thriller","outputs":{"render":{"sequence":"thriller"}}},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--name", "Thriller",
		"--outputs-json", `{"render":{"sequence":"thriller"}}`,
		"thriller",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/show.cue/thriller" {
		t.Errorf("path = %q, want /api/v1/config/show.cue/thriller", gotPath)
	}

	var decoded configShowCue
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if decoded.Show != "halloween-2026" || decoded.Name != "Thriller" {
		t.Errorf("decoded = %+v, want show=halloween-2026 name=Thriller", decoded)
	}
	if !strings.Contains(string(decoded.Outputs), `"sequence":"thriller"`) {
		t.Errorf("outputs = %s, want it to carry the render sequence", decoded.Outputs)
	}
}

func TestCmdCueSetMissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{"set", "--server", "http://unused", "thriller"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--show") {
		t.Errorf("stderr = %q, want it to name --show as missing", stderr.String())
	}
}

func TestCmdCueSetRejectsInvalidOutputsJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{
		"set", "--server", "http://unused",
		"--show", "halloween-2026", "--name", "Thriller", "--outputs-json", "not-json",
		"thriller",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

func TestCmdCueRevisions(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.cue","revisions":[
			{"revision":1,"createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api","createdAt":"2026-08-16T20:00:00Z","active":true}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{"revisions", "--server", ts.URL, "thriller"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.cue/thriller/revisions" {
		t.Errorf("path = %q, want /api/v1/config/show.cue/thriller/revisions", gotPath)
	}
}

// TestCmdCueUnknownSubcommandIsUsageError matches
// TestCmdSurfaceUnknownSubcommandIsUsageError (cmd_surface_test.go).
func TestCmdCueUnknownSubcommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{"bogus"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

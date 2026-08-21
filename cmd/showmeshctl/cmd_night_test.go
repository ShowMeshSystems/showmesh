package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// This file tests Track F seam F1's "night" subcommand. Each drives a
// real httptest.Server, matching cmd_show_test.go's own established
// pattern one seam over: the request this program actually issues
// (method, path, body) is what is under test, not a mock of the client.

const nightSessionSampleJSON = `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session","id":"halloween-main","revision":1,
	"payload":{
		"show":"halloween-2026","label":"Halloween main loop",
		"showPlaylist":{"fppInstanceId":"player-01","playlist":"halloween-show"},
		"resting":{
			"fppInstanceId":"player-01","playlist":"halloween-resting","endOfNightPlaylist":"halloween-resting",
			"timelineAsset":{"show":"halloween-2026","sequence":"resting-loop","target":"player-01"},
			"endOfNightRepeat":true
		},
		"enterShow":{"cues":[{"name":"lighting-fade","role":"lighting","action":"lighting-fade-out","offsetMs":-20000,"barrier":true,"onFailure":"continue"}],"blackoutHoldMs":6000},
		"enterResting":{"cues":[],"blackoutAfterShowMs":6000}
	},
	"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`

func TestCmdNightListRendersObjects(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session","objects":[
			{"id":"halloween-main","label":"Halloween main loop","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"list", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/night.session" {
		t.Errorf("path = %q, want /api/v1/config/night.session", gotPath)
	}
	if !strings.Contains(stdout.String(), "halloween-main") {
		t.Errorf("stdout = %q, want it to name the session id", stdout.String())
	}
}

func TestCmdNightGetRendersDetail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nightSessionSampleJSON)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"get", "--server", ts.URL, "halloween-main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Halloween main loop") || !strings.Contains(out, "lighting-fade") {
		t.Errorf("stdout missing expected detail; got: %s", out)
	}
}

func TestCmdNightSetSendsFullReplacementBody(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nightSessionSampleJSON)
	}))
	defer ts.Close()

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	payload := `{"show":"halloween-2026","label":"x"}`
	go func() {
		_, _ = w.Write([]byte(payload))
		_ = w.Close()
	}()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"set", "--server", ts.URL, "halloween-main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/night.session/halloween-main" {
		t.Errorf("path = %q, want /api/v1/config/night.session/halloween-main", gotPath)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body was not valid JSON: %v; body=%s", err, gotBody)
	}
	if sent["label"] != "x" {
		t.Errorf("request body did not carry the stdin payload verbatim: %s", gotBody)
	}
}

func TestCmdNightActiveDeactivateSendsEmptySession(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session.active","id":"default","revision":2,
			"payload":{"session":""},"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"deactivate", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body was not valid JSON: %v; body=%s", err, gotBody)
	}
	if sent["session"] != "" {
		t.Errorf("expected an explicit empty session on the wire, got %v (body: %s)", sent["session"], gotBody)
	}
	if !strings.Contains(stdout.String(), "none") {
		t.Errorf("expected the detail view to render the cleared pointer; got: %s", stdout.String())
	}
}

func TestCmdNightActivateSendsSessionID(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session.active","id":"default","revision":1,
			"payload":{"session":"halloween-main"},"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"activate", "--server", ts.URL, "halloween-main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body was not valid JSON: %v; body=%s", err, gotBody)
	}
	if sent["session"] != "halloween-main" {
		t.Errorf("expected session halloween-main on the wire, got %v (body: %s)", sent["session"], gotBody)
	}
}

func TestCmdNightRevisionFetchesSpecificRevision(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nightSessionSampleJSON)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"revision", "--server", ts.URL, "halloween-main", "1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/night.session/halloween-main/revisions/1" {
		t.Errorf("path = %q, want .../revisions/1", gotPath)
	}
}

func TestCmdNightRevisionRejectsNonPositiveArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"revision", "--server", "http://example.invalid", "halloween-main", "0"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

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

// This file tests ADR-033's "show mode" subcommands, following
// cmd_cue_test.go's exact pattern: each drives a real httptest.Server.

const showModeProgramResponse = `{"serverTime":"2026-08-23T21:00:00Z","kind":"show.mode","revision":0,
	"payload":{"mode":"program"},"updatedAt":"2026-08-23T21:00:00Z",
	"createdByPrincipalId":null,"createdByPrincipalName":null,"source":"default",
	"resolumeWebSocketEffect":"program mode: the Resolume WebSocket wake-up channel is held OPEN."}`

// A bare "show mode" reads: that is the operation ADR-033 decision 3 is
// about, and the one an operator performs most.
func TestCmdShowModeBareReadsCurrentMode(t *testing.T) {
	var gotPath, gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, showModeProgramResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"mode", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-23T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/config/show.mode" {
		t.Errorf("request = %s %s, want GET /api/v1/config/show.mode", gotMethod, gotPath)
	}
	if !strings.Contains(stdout.String(), "show mode: program") {
		t.Errorf("stdout = %q, want it to name the mode", stdout.String())
	}
	// ADR-033 decision 3: the behaviour caused by the mode names the mode
	// as its reason, and a CLI operator reads it here.
	if !strings.Contains(stdout.String(), "program mode:") {
		t.Errorf("stdout = %q, want it to carry the mode's stated effect", stdout.String())
	}
}

// A show.cue edit saved mid-show is staged, invisible to every node until
// the show restarts: an operator working through showmeshctl alone must
// be able to see that, not just a UI viewer, per
// this repository's API-first / CLI-parity rule. This is a regression
// test for the coordinator/CLI response-parity gate's own finding: the
// wire field existed before this test was written, but showmeshctl's
// struct had no matching field, so the CLI silently dropped it.
func TestCmdShowModeGetPrintsAStagedCueActivationPin(t *testing.T) {
	const pinnedResponse = `{"serverTime":"2026-08-23T21:00:00Z","kind":"show.mode","revision":4,
		"payload":{"mode":"show"},"updatedAt":"2026-08-23T21:00:00Z",
		"createdByPrincipalId":"p-1","createdByPrincipalName":"admin-1","source":"api",
		"resolumeWebSocketEffect":"show mode: the Resolume WebSocket wake-up channel is held CLOSED.",
		"cueActivationPin":{"pinned":true,"show":"show-1","generation":2,
		"pinnedAt":"2026-08-23T20:30:00Z","effect":"show mode: this coordinator is holding the cue authorization identity it captured for the show and generation named above."}}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, pinnedResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"mode", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-23T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "STAGED") {
		t.Errorf("stdout = %q, want it to say STAGED for a held cue-activation pin", out)
	}
	if !strings.Contains(out, "show-1") || !strings.Contains(out, "generation 2") {
		t.Errorf("stdout = %q, want it to name the pinned show (show-1) and generation (2)", out)
	}
	if !strings.Contains(out, "will NOT reach any node") {
		t.Errorf("stdout = %q, want it to state the concrete operator-facing consequence, not just the word staged", out)
	}
}

// Nothing ever written is reported as the built-in default, never as an
// error and never as an empty value.
func TestCmdShowModeGetReportsTheBuiltInDefault(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, showModeProgramResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"mode", "get", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-23T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "revision 0") || !strings.Contains(out, "source default") {
		t.Errorf("stdout = %q, want it to report revision 0 and source default", out)
	}
	if !strings.Contains(out, "built-in default") {
		t.Errorf("stdout = %q, want it to say nothing has ever been written", out)
	}
}

func TestCmdShowModeSetSendsTheModePayload(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-23T21:00:00Z","kind":"show.mode","revision":4,
			"payload":{"mode":"show"},"updatedAt":"2026-08-23T21:00:00Z",
			"createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api",
			"resolumeWebSocketEffect":"show mode: the Resolume WebSocket wake-up channel is held CLOSED."}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"mode", "set", "--server", ts.URL, "show"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-23T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/config/show.mode" {
		t.Errorf("request = %s %s, want PUT /api/v1/config/show.mode", gotMethod, gotPath)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body %s is not JSON: %v", gotBody, err)
	}
	if sent["mode"] != "show" {
		t.Errorf("request body = %s, want mode show", gotBody)
	}
	if len(sent) != 1 {
		t.Errorf("request body = %s, want exactly the mode key", gotBody)
	}
	if !strings.Contains(stderr.String(), "revision 4 is now active") {
		t.Errorf("stderr = %q, want it to report the new revision", stderr.String())
	}
}

// A value outside the closed enum is refused locally with a usage error
// naming both members, and no request is made.
func TestCmdShowModeSetRefusesANonMemberWithoutCallingTheAPI(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()

	for _, bad := range []string{"unknown", "setup", "SHOW"} {
		var stdout, stderr bytes.Buffer
		code := cmdShow([]string{"mode", "set", "--server", ts.URL, bad}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-23T21:00:00Z")))
		if code != exitUsage {
			t.Fatalf("%q: exit code = %d, want exitUsage; stderr=%s", bad, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "program or show") {
			t.Errorf("%q: stderr = %q, want it to name both members", bad, stderr.String())
		}
	}
	if called {
		t.Error("a rejected mode value still reached the coordinator")
	}
}

func TestCmdShowModeSetRequiresExactlyOneArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"mode", "set"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-23T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdShowModeRevisions(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-23T21:00:00Z","kind":"show.mode","revisions":[
			{"revision":2,"createdAt":"2026-08-23T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api","note":"","active":true},
			{"revision":1,"createdAt":"2026-08-23T19:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api","note":"","active":false}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"mode", "revisions", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-23T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.mode/revisions" {
		t.Errorf("path = %q, want /api/v1/config/show.mode/revisions", gotPath)
	}
}

func TestCmdShowModeJSONOutputCarriesTheWholeResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, showModeProgramResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"mode", "--server", ts.URL, "--output", "json"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-23T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout %s is not JSON: %v", stdout.String(), err)
	}
	payload, _ := got["payload"].(map[string]any)
	if payload["mode"] != "program" {
		t.Errorf("payload.mode = %v, want program", payload["mode"])
	}
}

func TestCmdShowModeUnknownSubcommandIsAUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"mode", "toggle"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-23T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
	if !strings.Contains(stderr.String(), "toggle") {
		t.Errorf("stderr = %q, want it to name the unknown subcommand", stderr.String())
	}
}

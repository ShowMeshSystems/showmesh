package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCmdMacroListRendersObjects(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.macro","objects":[
			{"id":"begin-set","label":"Begin set","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-14T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"list", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/config/show.macro" {
		t.Errorf("method/path = %s %s, want GET /api/v1/config/show.macro", gotMethod, gotPath)
	}
	if !strings.Contains(stdout.String(), "begin-set") {
		t.Errorf("stdout = %q, want it to name the macro id", stdout.String())
	}
}

func TestCmdMacroListEmptyDoesNotPrintBlank(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.macro","objects":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"list", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no show.macro objects") {
		t.Errorf("stdout = %q, want an explicit empty-list message rather than a blank table", stdout.String())
	}
}

func TestCmdMacroShowRendersSteps(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.macro","id":"begin-set","revision":2,
			"payload":{"show":"halloween-2026","label":"Begin set","description":"","steps":[
				{"id":"projectors","action":"projectors-on","onFailure":"abort","onUnconfirmed":"continue",
				 "localFallback":{"class":"coordinator-required","reason":"projector power is MQTT-only"}}
			]},
			"updatedAt":"2026-08-14T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"show", "--server", ts.URL, "begin-set"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.macro/begin-set" {
		t.Errorf("path = %q, want /api/v1/config/show.macro/begin-set", gotPath)
	}
	out := stdout.String()
	for _, want := range []string{"projectors", "projectors-on", "coordinator-required"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
}

func TestCmdMacroShowRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"show"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

// TestCmdMacroRunSubmitsAndReportsAcceptedWithoutFollow proves the default
// (no --follow) behavior: a 202 is printed and this command returns
// immediately with exitOK, without any request to /macro-runs/{runId} —
// STEP-9-SPEC.md section 2.1's "the submitting client learns the outcome
// by watching, not by waiting" applied to this CLI's default posture.
func TestCmdMacroRunSubmitsAndReportsAcceptedWithoutFollow(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	var runRequestCount int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/macro-runs/") {
			runRequestCount++
		}
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","replay":false,"run":{
			"id":"run-1","macroObjectId":"begin-set","macroRevision":1,"show":"halloween-2026","trigger":"cli",
			"issuerPrincipalId":"p1","issuerPrincipalName":"admin","createdAt":"2026-08-14T21:00:00Z",
			"finishedAt":null,"state":"running","completed":null,"confirmed":null,"reason":"",
			"attributionDegraded":false,"steps":[]}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"run", "--server", ts.URL, "begin-set"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/macros/begin-set/runs" {
		t.Errorf("method/path = %s %s, want POST /api/v1/macros/begin-set/runs", gotMethod, gotPath)
	}
	if gotBody["trigger"] != "cli" {
		t.Errorf("request body trigger = %v, want \"cli\"", gotBody["trigger"])
	}
	key, _ := gotBody["idempotencyKey"].(string)
	if key == "" {
		t.Errorf("request body idempotencyKey is empty, want a minted value")
	}
	if !strings.Contains(stdout.String(), "run-1") {
		t.Errorf("stdout = %q, want it to print the accepted run id", stdout.String())
	}
	if !strings.Contains(stdout.String(), "accepted") {
		t.Errorf("stdout = %q, want it to say the run was accepted (not that it finished)", stdout.String())
	}
	if runRequestCount != 0 {
		t.Errorf("this command made %d request(s) to /macro-runs/{runId} without --follow; want 0 (never waits by default)", runRequestCount)
	}
}

// TestCmdMacroRunReplayNotesOriginalRun proves the idempotency-replay case
// is surfaced to the operator rather than silently indistinguishable from
// a fresh submission.
func TestCmdMacroRunReplayNotesOriginalRun(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","replay":true,"run":{
			"id":"run-original","macroObjectId":"begin-set","macroRevision":1,"show":"halloween-2026","trigger":"cli",
			"issuerPrincipalId":"p1","issuerPrincipalName":"admin","createdAt":"2026-08-14T21:00:00Z",
			"finishedAt":null,"state":"running","completed":null,"confirmed":null,"reason":"",
			"attributionDegraded":false,"steps":[]}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"run", "--server", ts.URL, "begin-set"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "run-original") || !strings.Contains(stderr.String(), "already used") {
		t.Errorf("stderr = %q, want it to note the replay and name the original run id", stderr.String())
	}
}

// TestCmdMacroRunOverlapRefusalMapsToExitConflict proves ADR-031 decision
// 6's 409 (a second run of a macro already running) is reported as this
// program's conflict exit code, not a generic failure.
func TestCmdMacroRunOverlapRefusalMapsToExitConflict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/macro-run-already-in-flight",
			"title":"Macro run refused: another run of this macro is already in flight","status":409,
			"detail":"macro \"begin-set\" already has a run in progress (run run-existing)","conflictingRunId":"run-existing"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"run", "--server", ts.URL, "begin-set"}, &stdout, &stderr, time.Now)
	if code != exitConflict {
		t.Fatalf("exit code = %d, want exitConflict (%d); stderr=%s", code, exitConflict, stderr.String())
	}
	if !strings.Contains(stderr.String(), "run-existing") {
		t.Errorf("stderr = %q, want it to name the in-flight run", stderr.String())
	}
}

// TestCmdMacroRunForbiddenMapsToExitForbidden proves a 403 (missing
// show:macro:run) is distinguishable from a 401 in this program's own exit
// code convention, matching every other write subcommand's established
// posture.
func TestCmdMacroRunForbiddenMapsToExitForbidden(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/forbidden","title":"Forbidden","status":403,
			"detail":"missing required scope show:macro:run"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"run", "--server", ts.URL, "--token", "smsh_viewer", "begin-set"}, &stdout, &stderr, time.Now)
	if code != exitForbidden {
		t.Fatalf("exit code = %d, want exitForbidden (%d); stderr=%s", code, exitForbidden, stderr.String())
	}
	if !strings.Contains(stderr.String(), "show:macro:run") {
		t.Errorf("stderr = %q, want it to name the missing scope", stderr.String())
	}
}

func TestCmdMacroRunTooSmallTimeoutIsRaisedAndNoted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","replay":false,"run":{
			"id":"run-1","macroObjectId":"begin-set","macroRevision":1,"show":"halloween-2026","trigger":"cli",
			"issuerPrincipalId":"p1","issuerPrincipalName":"admin","createdAt":"2026-08-14T21:00:00Z",
			"finishedAt":null,"state":"running","completed":null,"confirmed":null,"reason":"",
			"attributionDegraded":false,"steps":[]}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"run", "--server", ts.URL, "--timeout", "1ms", "begin-set"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "1ms") || !strings.Contains(stderr.String(), minMacroClientTimeout.String()) {
		t.Errorf("stderr = %q, want a note naming both the requested and floor timeout values", stderr.String())
	}
}

// TestCmdMacroPutSendsTheFileContents mirrors TestCmdActionPutSendsTheFileContents
// (cmd_action_test.go): "macro put" PUTs the file's own bytes unmodified,
// against the show.macro route.
func TestCmdMacroPutSendsTheFileContents(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.macro","id":"begin-set","revision":2,
			"payload":{"show":"halloween-2026","label":"Begin set","description":"","steps":[
				{"id":"projectors","action":"projectors-on","onFailure":"abort","onUnconfirmed":"continue",
				 "localFallback":{"class":"coordinator-required","reason":"projector power is MQTT-only"}}
			]},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "macro.json")
	payload := `{"show":"halloween-2026","label":"Begin set","steps":[{"id":"projectors","action":"projectors-on","localFallback":{"class":"coordinator-required","reason":"projector power is MQTT-only"}}]}`
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"put", "--file", file, "--server", ts.URL, "--token", "t", "begin-set"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/show.macro/begin-set" {
		t.Errorf("path = %q, want /api/v1/config/show.macro/begin-set", gotPath)
	}
	if strings.TrimSpace(string(gotBody)) != payload {
		t.Errorf("body = %s, want the file's own contents unmodified: %s", gotBody, payload)
	}
	if !strings.Contains(stdout.String(), "begin-set") || !strings.Contains(stdout.String(), "projectors") {
		t.Errorf("stdout = %q, want the written macro's definition rendered back", stdout.String())
	}
	if !strings.Contains(stderr.String(), "revision 2 is now active") {
		t.Errorf("stderr = %q, want a note that revision 2 is now active", stderr.String())
	}
}

// TestCmdMacroPutRejectsInvalidJSON mirrors TestCmdActionPutRejectsInvalidJSON:
// a malformed payload is refused client-side before any request is sent.
func TestCmdMacroPutRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(file, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"put", "--file", file, "--server", "http://unused.invalid", "begin-set"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

// TestCmdMacroPutRequiresExactlyOneArg mirrors TestCmdActionPutRequiresExactlyOneArg.
func TestCmdMacroPutRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"put"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

// TestCmdMacroPutServerRejectionIsNotSwallowed proves a server-side
// validation refusal (a step referencing an unknown action) surfaces as
// the server's own problem response rather than being reported as a
// client-side success, and that no revision note is printed for a
// refused write.
func TestCmdMacroPutServerRejectionIsNotSwallowed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/show-config-field-unknown-reference","title":"Invalid show configuration","status":400,
			"detail":"steps[0].action: no show.action object with id \"does-not-exist\" has an active revision"}`)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "macro.json")
	payload := `{"show":"halloween-2026","label":"Begin set","steps":[{"id":"s1","action":"does-not-exist","localFallback":{"class":"coordinator-required","reason":"n/a"}}]}`
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdMacro([]string{"put", "--file", file, "--server", ts.URL, "--token", "t", "begin-set"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage (%d, the unrecognized-problem-type/400 fallback); stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "does-not-exist") {
		t.Errorf("stderr = %q, want the server's own refusal detail surfaced", stderr.String())
	}
	if strings.Contains(stderr.String(), "is now active") {
		t.Errorf("stderr = %q, want no revision-active note for a refused write", stderr.String())
	}
}

// TestCmdMacroDispatchesPut proves "showmeshctl macro put" is reachable
// through the real top-level dispatcher (run, main.go), not only by
// calling cmdMacroPut directly — TestCmdResolumeDispatchesStatus's own
// reasoning (ADR-030's "a capability nothing calls is not shipped").
func TestCmdMacroDispatchesPut(t *testing.T) {
	var gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.macro","id":"begin-set","revision":1,
			"payload":{"show":"halloween-2026","label":"Begin set","description":"","steps":[]},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "macro.json")
	if err := os.WriteFile(file, []byte(`{"show":"halloween-2026","label":"Begin set","steps":[]}`), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"macro", "put", "--file", file, "--server", ts.URL, "--token", "t", "begin-set"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
}

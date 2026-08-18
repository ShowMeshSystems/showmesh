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

func TestCmdActionListRendersObjects(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.action","objects":[
			{"id":"projectors-on","label":"Projectors on","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-14T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"list", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.action" {
		t.Errorf("path = %q, want /api/v1/config/show.action", gotPath)
	}
	if !strings.Contains(stdout.String(), "projectors-on") {
		t.Errorf("stdout = %q, want it to name the action id", stdout.String())
	}
}

func TestCmdActionShowRendersFPPTarget(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.action","id":"stop-main","revision":1,
			"payload":{"show":"halloween-2026","label":"Stop main show","description":"","safetyClass":"stop",
				"target":{"integration":"fpp","instanceId":"fpp-main","primitive":"stopPlaylist"}},
			"updatedAt":"2026-08-14T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"show", "--server", ts.URL, "stop-main"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.action/stop-main" {
		t.Errorf("path = %q, want /api/v1/config/show.action/stop-main", gotPath)
	}
	out := stdout.String()
	for _, want := range []string{"stop", "fpp-main", "stopPlaylist"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
}

func TestCmdActionShowRendersMQTTTarget(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.action","id":"projectors-on","revision":1,
			"payload":{"show":"halloween-2026","label":"Projectors on","description":"","safetyClass":"none",
				"target":{"integration":"mqtt","broker":"home-automation",
					"publish":{"topic":"home/projectors/set","payload":"ON","qos":1,"retain":false},
					"expect":{"kind":"boolean","topic":"home/projectors/state","deadlineSeconds":30}}},
			"updatedAt":"2026-08-14T20:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"show", "--server", ts.URL, "projectors-on"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"home-automation", "home/projectors/set", "home/projectors/state", "boolean"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "no principal recorded") {
		t.Errorf("stdout = %q, want a null creator rendered explicitly, not blank", out)
	}
}

func TestCmdActionShowRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"show"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

// TestCmdActionShowRendersResolumeTarget proves "action show" renders a
// resolume target's Action/Ref fields (TRACK-D-SEAM-C-MACRO-SPEC.md
// section 4) the same way it already renders fpp and mqtt targets.
func TestCmdActionShowRendersResolumeTarget(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.action","id":"launch-main","revision":1,
			"payload":{"show":"halloween-2026","label":"Launch main clip","description":"","safetyClass":"none",
				"target":{"integration":"resolume","action":"launchClip","ref":{"clip":"Whole House 1","deck":"Main"}}},
			"updatedAt":"2026-08-14T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"show", "--server", ts.URL, "launch-main"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"launchClip", "Whole House 1", "Main"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	// ADR-037: no Resolume object id ever appears.
	if strings.Contains(out, `"id"`) {
		t.Errorf("stdout leaked a raw \"id\" key: %s", out)
	}
}

// TestCmdActionPutSendsTheFileContents proves "action put --file" issues a
// real PUT carrying the file's JSON contents, unmodified, to
// /api/v1/config/show.action/{id} — the write path ADR-030 requires this
// program to drive for every authoring capability, including a Resolume
// target.
func TestCmdActionPutSendsTheFileContents(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.action","id":"launch-main","revision":1,
			"payload":{"show":"halloween-2026","label":"Launch main clip","description":"","safetyClass":"none",
				"target":{"integration":"resolume","action":"launchClip","ref":{"clip":"Whole House 1","deck":"Main"}}},
			"updatedAt":"2026-08-14T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "action.json")
	payload := `{"show":"halloween-2026","label":"Launch main clip","safetyClass":"none","target":{"integration":"resolume","action":"launchClip","ref":{"clip":"Whole House 1","deck":"Main"}}}`
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"put", "--file", file, "--server", ts.URL, "--token", "t", "launch-main"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/show.action/launch-main" {
		t.Errorf("path = %q, want /api/v1/config/show.action/launch-main", gotPath)
	}
	if strings.TrimSpace(string(gotBody)) != payload {
		t.Errorf("body = %s, want the file's own contents unmodified: %s", gotBody, payload)
	}
}

func TestCmdActionPutRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(file, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"put", "--file", file, "--server", "http://unused.invalid", "launch-main"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

func TestCmdActionPutRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"put"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

// TestCmdActionListPassesShowFilter is E7-3 deliverable 4's CLI half,
// matching TestCmdSurfaceListPassesShowFilter's shape one file over.
func TestCmdActionListPassesShowFilter(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T21:00:00Z","kind":"show.action","objects":[
			{"id":"projectors-on","label":"Projectors on","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-18T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"list", "--server", ts.URL, "--show", "halloween-2026"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.action" {
		t.Errorf("path = %q, want /api/v1/config/show.action", gotPath)
	}
	if gotQuery != "show=halloween-2026" {
		t.Errorf("query = %q, want show=halloween-2026", gotQuery)
	}
}

// TestCmdActionListWithoutShowSendsNoQuery proves the flag's absence sends
// no query string at all, never an empty "show=" value.
func TestCmdActionListWithoutShowSendsNoQuery(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T21:00:00Z","kind":"show.action","objects":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"list", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty", gotQuery)
	}
}

// --- Track E seam E7-2: "action check" ---

func TestCmdActionCheckOneIDExitsOKWhenOK(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T21:00:00Z","binding":{"actionId":"start-main","label":"Start","show":"halloween-2026","state":"ok","reason":"resolves"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"check", "--server", ts.URL, "start-main"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/actions/start-main/binding" {
		t.Errorf("path = %q, want /api/v1/actions/start-main/binding", gotPath)
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Errorf("stdout = %q, want it to name the state", stdout.String())
	}
}

// TestCmdActionCheckExitsBrokenForABrokenBinding is the exit-code half of
// the acceptance criterion this seam names explicitly: "check reports it
// naming the name, exits 29".
func TestCmdActionCheckExitsBrokenForABrokenBinding(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T21:00:00Z","binding":{"actionId":"start-main","label":"Start","show":"halloween-2026","state":"broken","reason":"instance \"player-01\" is not a configured FPP endpoint"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"check", "--server", ts.URL, "start-main"}, &stdout, &stderr, time.Now)
	if code != exitActionBindingBroken {
		t.Fatalf("exit code = %d, want exitActionBindingBroken (29); stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "player-01") {
		t.Errorf("stdout = %q, want it to name the missing instance", stdout.String())
	}
}

// TestCmdActionCheckUnknownNeverExitsBroken is the seam's own explicit
// warning: "unknown must NOT exit 29".
func TestCmdActionCheckUnknownNeverExitsBroken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T21:00:00Z","binding":{"actionId":"launch-main","label":"Launch","show":"halloween-2026","state":"unknown","reason":"no resolume composition has ever been uploaded"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"check", "--server", ts.URL, "launch-main"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (unknown must never exit 29); stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestCmdActionCheckWithNoIDListsWithShowFilter(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T21:00:00Z","bindings":[
			{"actionId":"start-main","label":"Start","show":"halloween-2026","state":"broken","reason":"gone"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"check", "--show", "halloween-2026", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitActionBindingBroken {
		t.Fatalf("exit code = %d, want exitActionBindingBroken; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/actions/bindings" {
		t.Errorf("path = %q, want /api/v1/actions/bindings", gotPath)
	}
	if gotQuery != "show=halloween-2026" {
		t.Errorf("query = %q, want show=halloween-2026", gotQuery)
	}
}

// --- Track E seam E7-1: "action invoke" ---

func TestCmdActionInvokeConfirmedExitsOK(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = decodeJSONBody(t, r, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T21:00:00Z","result":{
			"id":"cmd-1","idempotencyKey":"k","actionId":"blackout-now","label":"Blackout","replay":false,
			"outcome":"confirmed","outcomeReason":"went dark","attributionDegraded":false,
			"dispatchedAt":"2026-08-18T21:00:00Z","resolvedAt":"2026-08-18T21:00:01Z"
		}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"invoke", "--server", ts.URL, "blackout-now"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/actions/blackout-now/invocations" {
		t.Errorf("path = %q, want /api/v1/actions/blackout-now/invocations", gotPath)
	}
	if _, ok := gotBody["idempotencyKey"].(string); !ok || gotBody["idempotencyKey"] == "" {
		t.Errorf("request body idempotencyKey = %v, want a non-empty string", gotBody["idempotencyKey"])
	}
	if len(gotBody) != 1 {
		t.Errorf("request body = %v, want ONLY idempotencyKey (no protocol parameters, ADR-029 decision 3)", gotBody)
	}
	if !strings.Contains(stdout.String(), "confirmed") {
		t.Errorf("stdout = %q, want it to lead with the outcome word", stdout.String())
	}
}

func TestCmdActionInvokeUnconfirmableExitsDistinctFromConfirmed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T21:00:00Z","result":{
			"id":"cmd-2","idempotencyKey":"k","actionId":"relay-on","replay":false,
			"outcome":"unconfirmable","outcomeReason":"this action declares no expected response","attributionDegraded":false,
			"dispatchedAt":"2026-08-18T21:00:00Z","resolvedAt":"2026-08-18T21:00:01Z"
		}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"invoke", "--server", ts.URL, "relay-on"}, &stdout, &stderr, time.Now)
	if code != exitActionUnconfirmable {
		t.Fatalf("exit code = %d, want exitActionUnconfirmable (11); stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "unconfirmable:") {
		t.Errorf("stdout = %q, want it to lead with the distinct word \"unconfirmable\", not \"confirmed\"", stdout.String())
	}
}

func TestCmdActionInvokeRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"invoke"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
}

// decodeJSONBody reads and JSON-decodes r's body into v, failing t on
// error — a small local helper since this file's other tests never need
// to inspect a REQUEST body.
func decodeJSONBody(t *testing.T, r *http.Request, v any) error {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return json.Unmarshal(raw, v)
}

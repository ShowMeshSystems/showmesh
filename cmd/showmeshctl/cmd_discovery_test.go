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

// This file tests BUILD-PLAN Step 7 seam B's three write subcommands:
// discover, declare, undeclare. Each drives a real httptest.Server, exactly
// like this package's other cmd_*_test.go files, so the request this
// program actually issues (method, path, body) is what is under test, not
// a mock of the client.

func TestCmdDiscoverPrintsProposals(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z",
			"run":{"id":"run-1","startedAt":"2026-08-10T21:00:00Z","finishedAt":"2026-08-10T21:00:01Z",
			       "complete":true,"reason":null,"foundCount":2,
			       "initiatedByPrincipalId":"p1","initiatedByPrincipalName":"admin-1"},
			"proposals":[{"nodeId":"shed-01","source":"node"},{"nodeId":"player-01","source":"fpp"}]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdDiscover([]string{"--server", ts.URL, "--token", "t"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/discovery/runs" {
		t.Errorf("path = %q, want /api/v1/discovery/runs", gotPath)
	}

	out := stdout.String()
	if !strings.Contains(out, "shed-01") || !strings.Contains(out, "player-01") {
		t.Errorf("output missing proposals:\n%s", out)
	}
	if !strings.Contains(out, "showmeshctl declare") {
		t.Errorf("output does not point at `showmeshctl declare` for promoting a proposal:\n%s", out)
	}
}

func TestCmdDiscoverNoProposalsStatesHonestLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z",
			"run":{"id":"run-1","startedAt":"2026-08-10T21:00:00Z","finishedAt":"2026-08-10T21:00:01Z",
			       "complete":true,"reason":null,"foundCount":0,
			       "initiatedByPrincipalId":"p1","initiatedByPrincipalName":"admin-1"},
			"proposals":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdDiscover([]string{"--server", ts.URL, "--token", "t"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	// B1's honest consequence: this program must not imply discovery finds
	// equipment it structurally cannot.
	if !strings.Contains(out, "never talked to ShowMesh") {
		t.Errorf("output does not state the no-active-probing limitation:\n%s", out)
	}
}

func TestCmdDiscoverForbiddenNamesScope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/forbidden","title":"Forbidden","status":403,"detail":"this principal does not hold the required scope: config:write","serverTime":"2026-08-10T21:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdDiscover([]string{"--server", ts.URL, "--token", "t"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitForbidden {
		t.Fatalf("exit code = %d, want exitForbidden (%d); stderr=%s", code, exitForbidden, stderr.String())
	}
	if !strings.Contains(stderr.String(), "config:write") {
		t.Errorf("stderr = %q, want it to name the missing scope", stderr.String())
	}
}

func TestCmdDeclareSendsLabelAndNotes(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody declareNodeRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","declaration":{
			"declared":true,"label":"Shed controller","notes":"north yard","declaredAt":"2026-08-10T21:00:00Z",
			"declaredByPrincipalId":"p1","declaredByPrincipalName":"admin-1",
			"discoveryState":"unknown","discoveryReason":"no discovery run history is available",
			"lastDiscoveryRunId":null,"lastDiscoveredAt":null}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdDeclare([]string{"--server", ts.URL, "--token", "t", "--label", "Shed controller", "--notes", "north yard", "shed-01"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/nodes/shed-01/declaration" {
		t.Errorf("path = %q, want /api/v1/nodes/shed-01/declaration", gotPath)
	}
	if gotBody.Label != "Shed controller" || gotBody.Notes != "north yard" {
		t.Errorf("request body = %+v, want label/notes forwarded", gotBody)
	}
	if !strings.Contains(stdout.String(), "Shed controller") {
		t.Errorf("output missing declared label:\n%s", stdout.String())
	}
}

func TestCmdUndeclareRefusesWithoutConfirmFlag(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdUndeclare([]string{"--server", ts.URL, "--token", "t", "shed-01"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if called {
		t.Errorf("the coordinator was called without --confirm ever being passed")
	}
	if !strings.Contains(stderr.String(), "--confirm") {
		t.Errorf("stderr = %q, want it to name --confirm", stderr.String())
	}
}

func TestCmdUndeclareWithConfirmSucceeds(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody deleteNodeDeclarationRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdUndeclare([]string{"--server", ts.URL, "--token", "t", "--confirm", "shed-01"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/api/v1/nodes/shed-01/declaration" {
		t.Errorf("path = %q, want /api/v1/nodes/shed-01/declaration", gotPath)
	}
	if !gotBody.Confirm {
		t.Errorf("request body confirm = %v, want true", gotBody.Confirm)
	}
	if !strings.Contains(stdout.String(), "shed-01") {
		t.Errorf("output missing confirmation:\n%s", stdout.String())
	}
}

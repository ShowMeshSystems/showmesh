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

// This file tests Track E seam E1/E2's "show" and "show active"/"show
// activate" subcommands. Each drives a real httptest.Server, exactly like
// this package's other cmd_*_test.go files, so the request this program
// actually issues (method, path, body) is what is under test, not a mock
// of the client.

func TestCmdShowListRendersObjects(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show","objects":[
			{"id":"halloween-2026","label":"Halloween 2026","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"list", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show" {
		t.Errorf("path = %q, want /api/v1/config/show", gotPath)
	}
	if !strings.Contains(stdout.String(), "halloween-2026") {
		t.Errorf("stdout = %q, want it to name the show id", stdout.String())
	}
}

func TestCmdShowGetRendersDetail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show","id":"halloween-2026","revision":2,
			"payload":{"name":"Halloween 2026","notes":"the good one"},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"get", "--server", ts.URL, "halloween-2026"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Halloween 2026", "the good one", "admin"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
}

// TestCmdShowSetAlwaysSendsBothFieldsFullReplacement is
// TRACK-E-SESSION-SPEC.md section 2.1's own CLI-layer proof: "show set"
// never read-modify-writes. Only --name is given here (no --notes at
// all); the request body must still carry an explicit "notes":"" rather
// than omitting the key, because config.DecodeShowPayload's "absent"
// path and "explicit empty string" path both mean the same thing server
// side, but a CLIENT that omitted the key entirely on every call where
// the operator did not repeat --notes would be indistinguishable, from
// reading this test alone, from one that carried the previous value
// forward by NOT sending the key — the request must prove the field is
// always sent, not rely on the coincidence that absent and empty decode
// identically on this particular server.
//
// Broken and confirmed to fail: changed cmdShowSet to use fs.Visit
// (mirroring declare's partial-update shape, cmd_discovery.go) so an
// unset --notes omitted the key entirely — this test's JSON-body
// assertion failed, the decoded body had no "notes" key at all. Restored
// afterward.
func TestCmdShowSetAlwaysSendsBothFieldsFullReplacement(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show","id":"halloween-2026","revision":1,
			"payload":{"name":"Halloween 2026","notes":""},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"set", "--server", ts.URL, "--name", "Halloween 2026", "halloween-2026"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/show/halloween-2026" {
		t.Errorf("path = %q, want /api/v1/config/show/halloween-2026", gotPath)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	notesRaw, ok := decoded["notes"]
	if !ok {
		t.Fatalf("request body has no \"notes\" key at all; a full replacement must always send it: %s", gotBody)
	}
	if string(notesRaw) != `""` {
		t.Errorf("notes = %s, want an explicit empty string", notesRaw)
	}
}

func TestCmdShowSetRequiresName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"set", "halloween-2026"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdShowActiveNotFoundReportsExitNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Resource not found","status":404,"detail":"no show.active object with id \"active\" has an active revision; PUT one to create it","serverTime":"2026-08-16T21:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"active", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitNotFound {
		t.Errorf("exit code = %d, want exitNotFound; stderr=%s", code, stderr.String())
	}
}

func TestCmdShowActivatePUTsShowField(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.active","id":"active","revision":1,
			"payload":{"show":"halloween-2026"},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"activate", "--server", ts.URL, "halloween-2026"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/show.active" {
		t.Errorf("path = %q, want /api/v1/config/show.active (no id in the path — it's a singleton)", gotPath)
	}
	if !strings.Contains(string(gotBody), `"show":"halloween-2026"`) {
		t.Errorf("request body = %s, want it to carry show=halloween-2026", gotBody)
	}
	if !strings.Contains(stdout.String(), "halloween-2026") {
		t.Errorf("stdout = %q, want it to name the now-active show", stdout.String())
	}
}

func TestCmdShowActivateRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"activate"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdShowUnknownSubcommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"bogus"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

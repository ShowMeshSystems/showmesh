package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This file is Track D seam E's own showmeshctl test suite: "resolume
// status", over GET /resolume/instances and GET /resolume/instances/{id}.

// TestCmdResolumeStatusListPrintsInstance is acceptance criterion 11's
// positive case: an instance is configured, and its health, composition,
// and observations are all rendered.
func TestCmdResolumeStatusListPrintsInstance(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resolume/instances" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","instances":[
			{"instanceId":"resolume","health":"healthy",
			 "observations":[
			   {"signal":"resolume.reachable","value":true,"unit":null,"state":"current","reason":null,"observedAt":"2026-08-10T20:59:55Z","collectedAt":"2026-08-10T20:59:55Z","source":"resolume-rest","quality":"derived","validForSeconds":30},
			   {"signal":"resolume.composition.name","value":null,"unit":null,"state":"unsupported","reason":"this Arena build does not expose this value without reading the full composition, which this system never does","observedAt":null,"collectedAt":"2026-08-10T20:59:55Z","source":"resolume-survey","quality":"direct","validForSeconds":null}
			 ],
			 "composition":{"name":"Holiday Test Show","revision":3,"activatedAt":"2026-08-10T18:00:00Z"}}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeStatus([]string{"--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"resolume", "Holiday Test Show", "resolume.reachable", "UNSUPPORTED"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestCmdResolumeStatusListUnconfiguredPrintsPlainStatementAndExitsOK is
// acceptance criterion 11's negative case: an unconfigured coordinator
// prints a plain statement and exits 0 — a fact about the deployment, not
// an error, mints no new exit code.
func TestCmdResolumeStatusListUnconfiguredPrintsPlainStatementAndExitsOK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","instances":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeStatus([]string{"--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (an unconfigured coordinator is a fact, not an error); stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(strings.ToLower(out), "no resolume instance is configured") {
		t.Errorf("output = %q, want a plain statement that no Resolume instance is configured", out)
	}
}

// TestCmdResolumeStatusOnePrintsInstance drives the single-instance route.
func TestCmdResolumeStatusOnePrintsInstance(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resolume/instances/resolume" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","instance":
			{"instanceId":"resolume","health":"unknown","observations":[],"composition":null}
		}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeStatus([]string{"--server", ts.URL, "resolume"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "resolume") {
		t.Errorf("output missing instance id:\n%s", stdout.String())
	}
}

// TestCmdResolumeStatusOneNotFound proves the ordinary problem/exit-code
// path applies here exactly as it does for `fpp <id>`/`node <id>`.
func TestCmdResolumeStatusOneNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Resource not found","status":404,"detail":"no Resolume instance with id \"bogus\" is configured"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeStatus([]string{"--server", ts.URL, "bogus"}, &stdout, &stderr, time.Now)
	if code != exitNotFound {
		t.Errorf("exit code = %d, want exitNotFound (%d); stderr=%s", code, exitNotFound, stderr.String())
	}
}

// TestCmdResolumeStatusJSONOutput proves --output json round-trips the raw
// decoded response rather than the text table.
func TestCmdResolumeStatusJSONOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","instances":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeStatus([]string{"--server", ts.URL, "--output", "json"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"instances"`) {
		t.Errorf("json output = %q, want it to contain an \"instances\" key", stdout.String())
	}
}

// TestCmdResolumeStatusRejectsTooManyArguments mirrors cmdFPP's identical
// argument-count guard.
func TestCmdResolumeStatusRejectsTooManyArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdResolumeStatus([]string{"one", "two"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

// TestCmdResolumeDispatchesStatus proves "showmeshctl resolume status" is
// reachable through the real top-level dispatcher (cmdResolume), not only
// by calling cmdResolumeStatus directly — ADR-030's own lesson (CLAUDE.md's
// "Step 6's own lesson": a capability nothing calls is not shipped).
func TestCmdResolumeDispatchesStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","instances":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{"resolume", "status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
}

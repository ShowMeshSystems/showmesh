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

func TestCmdRunShowConfirmedExitsOK(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:05:00Z","run":{
			"id":"run-1","macroObjectId":"begin-set","macroRevision":1,"show":"halloween-2026","trigger":"cli",
			"issuerPrincipalId":"p1","issuerPrincipalName":"admin","createdAt":"2026-08-14T21:00:00Z",
			"finishedAt":"2026-08-14T21:04:00Z","state":"finished","completed":true,"confirmed":true,"reason":"",
			"attributionDegraded":false,"steps":[
				{"stepIndex":0,"stepId":"projectors","actionObjectId":"projectors-on","actionRevision":1,
				 "integration":"fpp","safetyClass":"none","localFallbackClass":"coordinator-required",
				 "state":"resolved","dispatchedAt":"2026-08-14T21:00:01Z","resolvedAt":"2026-08-14T21:00:02Z",
				 "outcome":"confirmed","outcomeState":"current","outcomeReason":"","attributionDegraded":false,
				 "command":{"state":"none","reason":"no command dispatched for this test fixture"}}
			]}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRun([]string{"show", "--server", ts.URL, "run-1"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/macro-runs/run-1" {
		t.Errorf("path = %q, want /api/v1/macro-runs/run-1", gotPath)
	}
	if !strings.Contains(stdout.String(), "run-1") {
		t.Errorf("stdout missing run id:\n%s", stdout.String())
	}
}

// TestCmdRunShowAbortedExitsNonZeroAndNamesReason proves an aborted run
// (completed=false) exits with exitMacroRunAborted and surfaces the run's
// own reason, matching "completed" and "confirmed" being reported as
// separate facts.
func TestCmdRunShowAbortedExitsNonZeroAndNamesReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:05:00Z","run":{
			"id":"run-2","macroObjectId":"begin-set","macroRevision":1,"show":"halloween-2026","trigger":"cli",
			"issuerPrincipalId":"p1","issuerPrincipalName":"admin","createdAt":"2026-08-14T21:00:00Z",
			"finishedAt":"2026-08-14T21:04:00Z","state":"finished","completed":false,"confirmed":false,
			"reason":"step \"start\" failed: coordinator refused the command","attributionDegraded":false,"steps":[]}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRun([]string{"show", "--server", ts.URL, "run-2"}, &stdout, &stderr, time.Now)
	if code != exitMacroRunAborted {
		t.Fatalf("exit code = %d, want exitMacroRunAborted (%d); stdout=%s", code, exitMacroRunAborted, stdout.String())
	}
	if !strings.Contains(stdout.String(), "coordinator refused the command") {
		t.Errorf("stdout = %q, want it to carry the run's own reason", stdout.String())
	}
}

// TestCmdRunShowStillRunningIsNotReportedAsFailure proves reading a
// still-in-flight run (no --follow) is not itself treated as a failure:
// this program genuinely does not know the eventual outcome and must not
// guess either way.
func TestCmdRunShowStillRunningIsNotReportedAsFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:05:00Z","run":{
			"id":"run-3","macroObjectId":"begin-set","macroRevision":1,"show":"halloween-2026","trigger":"cli",
			"issuerPrincipalId":"p1","issuerPrincipalName":"admin","createdAt":"2026-08-14T21:00:00Z",
			"finishedAt":null,"state":"running","completed":null,"confirmed":null,"reason":"","attributionDegraded":false,"steps":[]}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRun([]string{"show", "--server", ts.URL, "run-3"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (a running run is not a failure); stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestCmdRunShowNotFoundExitsExitNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Resource not found","status":404,"detail":"no such run"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRun([]string{"show", "--server", ts.URL, "nope"}, &stdout, &stderr, time.Now)
	if code != exitNotFound {
		t.Fatalf("exit code = %d, want exitNotFound (%d); stderr=%s", code, exitNotFound, stderr.String())
	}
}

func TestCmdRunListAppliesQueryFilters(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:05:00Z","runs":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRun([]string{"list", "--server", ts.URL, "--macro", "begin-set", "--show", "halloween-2026", "--state", "finished", "--limit", "10"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"macroId=begin-set", "show=halloween-2026", "state=finished", "limit=10"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query = %q, want it to contain %q", gotQuery, want)
		}
	}
}

func TestCmdRunListOmitsShowQueryParamWhenFlagNotGiven(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:05:00Z","runs":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRun([]string{"list", "--server", ts.URL, "--macro", "begin-set", "--state", "finished", "--limit", "10"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if strings.Contains(gotQuery, "show=") {
		t.Errorf("query = %q, want no show query parameter when --show is not given", gotQuery)
	}
}

func TestCmdRunListRejectsInvalidState(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdRun([]string{"list", "--state", "bogus"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage for an invalid --state", code)
	}
}

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCmdAudioAlignmentRunStartHitsNodeScopedPath(t *testing.T) {
	var gotPath, gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","run":{
			"id":"run-1","nodeId":"node-a","startedAt":"2026-08-18T22:00:00Z","stoppedAt":null,
			"startedBy":"admin-1","stoppedBy":null,"stopReason":null}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioAlignmentRunStart([]string{"--server", ts.URL, "--node", "node-a"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/nodes/node-a/audio/alignment-runs" {
		t.Fatalf("%s %s, want POST /api/v1/nodes/node-a/audio/alignment-runs", gotMethod, gotPath)
	}
	if !strings.Contains(stdout.String(), "run-1") {
		t.Fatalf("stdout = %q, want the run id", stdout.String())
	}
}

func TestCmdAudioAlignmentRunStartConflictExitsConflict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/conflict","title":"Audio alignment run already active",
			"status":409,"detail":"already active","serverTime":"2026-08-18T22:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioAlignmentRunStart([]string{"--server", ts.URL, "--node", "node-a"}, &stdout, &stderr, time.Now)
	if code != exitConflict {
		t.Fatalf("exit code = %d, want exitConflict; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestCmdAudioAlignmentRunStopHitsRunScopedPathWithReason(t *testing.T) {
	var gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:05:00Z","run":{
			"id":"run-1","nodeId":"node-a","startedAt":"2026-08-18T22:00:00Z","stoppedAt":"2026-08-18T22:05:00Z",
			"startedBy":"admin-1","stoppedBy":"admin-1","stopReason":"operator stop"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioAlignmentRunStop([]string{"--server", ts.URL, "--node", "node-a", "--run", "run-1", "--reason", "operator stop"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/nodes/node-a/audio/alignment-runs/run-1/stop" {
		t.Fatalf("path = %q, want .../alignment-runs/run-1/stop", gotPath)
	}
	if !strings.Contains(string(gotBody), "operator stop") {
		t.Fatalf("body = %s, want the reason sent", gotBody)
	}
	if !strings.Contains(stdout.String(), "stopped") {
		t.Fatalf("stdout = %q, want a stopped confirmation", stdout.String())
	}
}

func TestCmdAudioAlignmentRunStopUnknownRunExitsNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Resource not found",
			"status":404,"detail":"no run","serverTime":"2026-08-18T22:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioAlignmentRunStop([]string{"--server", ts.URL, "--node", "node-a", "--run", "missing"}, &stdout, &stderr, time.Now)
	if code != exitNotFound {
		t.Fatalf("exit code = %d, want exitNotFound; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestCmdAudioAlignmentRunListHitsNodeScopedPath(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","runs":[
			{"id":"run-2","nodeId":"node-a","startedAt":"2026-08-18T22:10:00Z","stoppedAt":null,"startedBy":"a","stoppedBy":null,"stopReason":null},
			{"id":"run-1","nodeId":"node-a","startedAt":"2026-08-18T22:00:00Z","stoppedAt":"2026-08-18T22:05:00Z","startedBy":"a","stoppedBy":"a","stopReason":"done"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioAlignmentRunList([]string{"--server", ts.URL, "--node", "node-a"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/nodes/node-a/audio/alignment-runs" {
		t.Fatalf("path = %q, want .../audio/alignment-runs", gotPath)
	}
	if !strings.Contains(stdout.String(), "run-2") || !strings.Contains(stdout.String(), "run-1") {
		t.Fatalf("stdout = %q, want both runs listed", stdout.String())
	}
}

func TestCmdAudioAlignmentRunGetPrintsSummaryThenSeries(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z",
			"run":{"id":"run-1","nodeId":"node-a","startedAt":"2026-08-18T22:00:00Z","stoppedAt":null,"startedBy":"a","stoppedBy":null,"stopReason":null},
			"samples":[{"sampledAt":"2026-08-18T22:00:00Z","offsetMs":0,"sessionId":"s1"},{"sampledAt":"2026-08-18T22:00:01Z","offsetMs":2,"sessionId":"s1"}],
			"summary":{"sampleCount":2,"firstSampleAt":"2026-08-18T22:00:00Z","lastSampleAt":"2026-08-18T22:00:01Z",
				"maxExcursionOffsetMs":2,"maxExcursionSampledAt":"2026-08-18T22:00:01Z","driftRateMsPerHour":7200}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioAlignmentRunGet([]string{"--server", ts.URL, "--node", "node-a", "--run", "run-1"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/nodes/node-a/audio/alignment-runs/run-1" {
		t.Fatalf("path = %q, want .../alignment-runs/run-1", gotPath)
	}
	out := stdout.String()
	summaryIdx := strings.Index(out, "max excursion")
	sampleIdx := strings.Index(out, "2026-08-18T22:00:00Z  0.000 ms")
	if summaryIdx == -1 || sampleIdx == -1 || summaryIdx > sampleIdx {
		t.Fatalf("stdout = %q, want the summary printed before the series", out)
	}
	if !strings.Contains(out, "7200.000 ms/hour") {
		t.Fatalf("stdout = %q, want the drift rate printed", out)
	}
}

func TestCmdAudioAlignmentRunGetUnavailableDriftRateReported(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z",
			"run":{"id":"run-1","nodeId":"node-a","startedAt":"2026-08-18T22:00:00Z","stoppedAt":null,"startedBy":"a","stoppedBy":null,"stopReason":null},
			"samples":[],
			"summary":{"sampleCount":0,"firstSampleAt":null,"lastSampleAt":null,"maxExcursionOffsetMs":null,"maxExcursionSampledAt":null,
				"driftRateMsPerHour":null,"driftRateUnavailableReason":"no samples recorded"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioAlignmentRunGet([]string{"--server", ts.URL, "--node", "node-a", "--run", "run-1"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "unavailable (no samples recorded)") {
		t.Fatalf("stdout = %q, want the unavailable reason printed", stdout.String())
	}
}

func TestCmdAudioAlignmentRunRequiresNodeFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAudioAlignmentRunStart([]string{}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage with no --node", code)
	}
}

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
	"time"
)

// TestCmdAudioSilenceDispatchesToNodeScopedPathWithNoParams proves this
// subcommand hits POST /nodes/{nodeId}/audio/silence (not the session
// path) and sends only idempotencyKey - no sessionId, no revision, no
// params field the operation does not take.
func TestCmdAudioSilenceDispatchesToNodeScopedPathWithNoParams(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","command":{
			"commandId":"cmd-1","idempotencyKey":"k","action":"audio.node.silence","nodeId":"node-a",
			"replay":false,"outcome":"confirmed","reason":"","sessionsFound":1,
			"sessions":[{"sessionId":"cue","outcome":"stopped","reason":""}],
			"dispatchedAt":"2026-08-18T22:00:00Z","resolvedAt":"2026-08-18T22:00:00Z","attributionDegraded":false}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioSilence([]string{"--server", ts.URL, "node-a"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/nodes/node-a/audio/silence" {
		t.Fatalf("path = %q, want /api/v1/nodes/node-a/audio/silence", gotPath)
	}
	if _, ok := gotBody["sessionId"]; ok {
		t.Fatalf("body = %v, must not carry sessionId - audio.node.silence is node-scoped", gotBody)
	}
	if _, ok := gotBody["revision"]; ok {
		t.Fatalf("body = %v, must not carry revision - audio.node.silence takes none", gotBody)
	}
	if _, ok := gotBody["idempotencyKey"]; !ok {
		t.Fatalf("body = %v, want an idempotencyKey field", gotBody)
	}
	if !strings.Contains(stdout.String(), "cue: stopped") {
		t.Fatalf("stdout = %q, want the per-session result printed", stdout.String())
	}
}

// TestCmdAudioSilenceSurfacesAgentRefusalReason proves the CLI reports an
// old-agent refusal's own reason, not a generic message, and exits
// exitCommandUnconfirmed rather than exitOK.
func TestCmdAudioSilenceSurfacesAgentRefusalReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","command":{
			"commandId":"cmd-1","idempotencyKey":"k","action":"audio.node.silence","nodeId":"node-a",
			"replay":false,"outcome":"refused","reason":"operation \"audio.node.silence\" is not on the agent's allowlist",
			"sessionsFound":0,"sessions":[],
			"dispatchedAt":"2026-08-18T22:00:00Z","resolvedAt":"2026-08-18T22:00:00Z","attributionDegraded":false}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioSilence([]string{"--server", ts.URL, "node-a"}, &stdout, &stderr, time.Now)
	if code != exitCommandUnconfirmed {
		t.Fatalf("exit code = %d, want exitCommandUnconfirmed; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "not on the agent's allowlist") {
		t.Fatalf("stdout = %q, want the agent's own refusal reason", stdout.String())
	}
}

// TestReportAudioNodeSilenceResultUnknownOutcomeExitsAPIError mirrors
// TestReportAudioSessionCommandResultUnknownOutcomeExitsAPIError one file
// over: an outcome string this program does not recognize must never be
// treated as success.
func TestReportAudioNodeSilenceResultUnknownOutcomeExitsAPIError(t *testing.T) {
	var buf bytes.Buffer
	result := audioNodeSilenceCommandResult{Outcome: "some-future-outcome-this-binary-predates"}
	code := reportAudioNodeSilenceResult(&buf, result)
	if code != exitAPIError {
		t.Fatalf("exit = %d, want exitAPIError (6) for an unrecognized outcome, not a silent success", code)
	}
	if got := exitCodeForAudioNodeSilenceResult(result); got != exitAPIError {
		t.Fatalf("exitCodeForAudioNodeSilenceResult = %d, want exitAPIError (6)", got)
	}
}

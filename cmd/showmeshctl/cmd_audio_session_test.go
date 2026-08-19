package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCmdAudioSessionDefaultRevisionIsNonZero proves finding 22's fix: a
// dispatch that omits --revision must still work for a brand-new session,
// whose pkg/audio.RevisionState starts at 0 and refuses any requested
// revision that is not STRICTLY greater than the current one. The
// pre-fix default of the literal 0 was refused even on a session's very
// first command.
func TestCmdAudioSessionDefaultRevisionIsNonZero(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","command":{
			"commandId":"cmd-1","idempotencyKey":"k","action":"audio.session.stop","nodeId":"node-a","sessionId":"s1",
			"replay":false,"outcome":"stopped","reason":"",
			"dispatchedAt":"2026-08-18T22:00:00Z","resolvedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioSession([]string{"stop", "--server", ts.URL, "node-a", "s1"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	rev, ok := gotBody["revision"].(float64)
	if !ok {
		t.Fatalf("body = %v, want a numeric \"revision\" field", gotBody)
	}
	if rev <= 0 {
		t.Fatalf("default revision = %v, want > 0 — revision 0 is refused even for a brand-new session (pkg/audio.RevisionState starts at current=0)", rev)
	}
}

// TestCmdAudioSessionExplicitRevisionPassesThroughUnchanged proves an
// explicit --revision is never overridden by the new default.
func TestCmdAudioSessionExplicitRevisionPassesThroughUnchanged(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","command":{
			"commandId":"cmd-1","idempotencyKey":"k","action":"audio.session.stop","nodeId":"node-a","sessionId":"s1",
			"replay":false,"outcome":"stopped","reason":"",
			"dispatchedAt":"2026-08-18T22:00:00Z","resolvedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioSession([]string{"stop", "--server", ts.URL, "--revision", "42", "node-a", "s1"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if rev, ok := gotBody["revision"].(float64); !ok || rev != 42 {
		t.Fatalf("body revision = %v, want the explicit 42 unchanged", gotBody["revision"])
	}
}

// TestReportAudioSessionCommandResultKnownOutcomeExitsOK proves every
// reserved success-adjacent outcome still exits 0.
func TestReportAudioSessionCommandResultKnownOutcomeExitsOK(t *testing.T) {
	for _, outcome := range []string{"started", "position", "gain", "fade_complete", "stopped", "completed"} {
		t.Run(outcome, func(t *testing.T) {
			var buf bytes.Buffer
			code := reportAudioSessionCommandResult(&buf, "audio session stop", audioSessionCommandResult{Outcome: outcome})
			if code != exitOK {
				t.Fatalf("exit = %d, want exitOK for recognized outcome %q", code, outcome)
			}
			if got := exitCodeForAudioSessionCommandResult(audioSessionCommandResult{Outcome: outcome}); got != exitOK {
				t.Fatalf("exitCodeForAudioSessionCommandResult = %d, want exitOK for recognized outcome %q", got, outcome)
			}
		})
	}
}

// TestReportAudioSessionCommandResultUnknownOutcomeExitsAPIError proves
// finding 11's CLI half: an outcome string this program does not
// recognize must never be treated as success (exitOK), which is what the
// prior unconditional "default: exitOK" did for ANY value outside the
// four explicitly-handled ones.
func TestReportAudioSessionCommandResultUnknownOutcomeExitsAPIError(t *testing.T) {
	var buf bytes.Buffer
	code := reportAudioSessionCommandResult(&buf, "audio session stop", audioSessionCommandResult{Outcome: "some-future-outcome-this-binary-predates"})
	if code != exitAPIError {
		t.Fatalf("exit = %d, want exitAPIError (6) for an unrecognized outcome, not a silent success", code)
	}
	if got := exitCodeForAudioSessionCommandResult(audioSessionCommandResult{Outcome: "some-future-outcome-this-binary-predates"}); got != exitAPIError {
		t.Fatalf("exitCodeForAudioSessionCommandResult = %d, want exitAPIError (6)", got)
	}
}

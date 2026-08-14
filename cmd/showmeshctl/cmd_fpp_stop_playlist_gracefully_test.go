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

// TestCmdFPPStopPlaylistGracefullyDefaultRequestShape pins the request
// body for a bare "fpp stop-playlist-gracefully <id>" (no --after-loop):
// params always present, afterLoop defaulting to false.
func TestCmdFPPStopPlaylistGracefullyDefaultRequestShape(t *testing.T) {
	var rawBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-1","idempotencyKey":"k","action":"fpp.stop_playlist_gracefully","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"stop-playlist-gracefully", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rawBody, &got); err != nil {
		t.Fatalf("decode request body: %v; body=%s", err, rawBody)
	}
	if got["action"] != "stopPlaylistGracefully" {
		t.Errorf("action = %v, want \"stopPlaylistGracefully\"", got["action"])
	}
	params, ok := got["params"].(map[string]any)
	if !ok {
		t.Fatalf("body = %s, want a \"params\" object", rawBody)
	}
	if params["afterLoop"] != false {
		t.Errorf("params.afterLoop = %v, want false (the documented default)", params["afterLoop"])
	}
}

// TestCmdFPPStopPlaylistGracefullyAfterLoopFlagSetsRequestShape proves
// --after-loop actually reaches the wire request.
func TestCmdFPPStopPlaylistGracefullyAfterLoopFlagSetsRequestShape(t *testing.T) {
	var rawBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-2","idempotencyKey":"k","action":"fpp.stop_playlist_gracefully","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"stop-playlist-gracefully", "--after-loop", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rawBody, &got)
	params := got["params"].(map[string]any)
	if params["afterLoop"] != true {
		t.Errorf("params.afterLoop = %v, want true", params["afterLoop"])
	}
}

// TestCmdFPPStopPlaylistGracefullyConfirmedButStillPlayingSurfacesReason
// is this subcommand's own version of the task's central requirement:
// docs/bench/fpp-command-vocabulary.md section 3.3 measured a graceful
// stop's terminal state as bounded by show content, so its own
// confirmation predicate can answer "confirmed" while fpp.status is only
// "stopping gracefully" — the show is STILL RUNNING. This test proves the
// CLI's stdout does not let that read as "the show stopped": the
// server's own outcomeReason, which says so explicitly, must appear.
// Broken to verify: printing a bare "confirmed: ..." with no reason (the
// old cmd_fpp_stop_playlist.go behavior, before it surfaced the reason on
// EVERY confirmed outcome) makes this test fail.
func TestCmdFPPStopPlaylistGracefullyConfirmedButStillPlayingSurfacesReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-3","idempotencyKey":"k","action":"fpp.stop_playlist_gracefully","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current",
			"outcomeReason":"fpp.status = \"stopping gracefully\" (source fpp_poll): FPP accepted the graceful stop and the show is winding down, but has NOT stopped yet — a graceful stop's terminal state is bounded by the currently playing item's own remaining runtime, not by any deadline ShowMesh can choose",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"stop-playlist-gracefully", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (this outcome IS \"confirmed\" per the server); stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "confirmed") {
		t.Errorf("stdout = %q, want it to report \"confirmed\"", stdout.String())
	}
	if !strings.Contains(stdout.String(), "has NOT stopped yet") {
		t.Fatalf("stdout = %q, want the server's own outcomeReason surfaced verbatim, so an operator cannot read "+
			"\"confirmed\" as \"the show stopped\" (docs/bench/fpp-command-vocabulary.md section 3.3)", stdout.String())
	}
}

// TestCmdFPPStopPlaylistGracefullyReachedIdleSurfacesReasonToo proves the
// OTHER confirmed branch (graceful stop actually reached idle) also
// surfaces its own reason text, which says the stop DID complete — this
// command must not treat "surface the reason" as something only the
// still-playing case needs.
func TestCmdFPPStopPlaylistGracefullyReachedIdleSurfacesReasonToo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-4","idempotencyKey":"k","action":"fpp.stop_playlist_gracefully","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current",
			"outcomeReason":"fpp.status reached \"idle\" (source fpp_poll): the graceful stop completed and playback has ended",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"stop-playlist-gracefully", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK", code)
	}
	if !strings.Contains(stdout.String(), "playback has ended") {
		t.Errorf("stdout = %q, want the server's own outcomeReason surfaced verbatim", stdout.String())
	}
}

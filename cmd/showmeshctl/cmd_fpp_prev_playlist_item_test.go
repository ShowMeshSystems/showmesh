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

// TestCmdFPPPrevPlaylistItemRequestShape pins the exact request body: no
// "params" key at all for this zero-parameter primitive.
func TestCmdFPPPrevPlaylistItemRequestShape(t *testing.T) {
	var gotPath string
	var rawBody []byte
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		rawBody, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(rawBody, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-1","idempotencyKey":"`+fmt.Sprint(gotBody["idempotencyKey"])+`","action":"fpp.prev_playlist_item","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current",
			"outcomeReason":"fpp.playlist.index moved from 2 to 1 (source fpp_poll) — note: this counter also advances on FPP's own item boundaries, so movement is not uniquely attributable to this command",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"prev-playlist-item", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/fpp/bench-fpp/commands" {
		t.Errorf("path = %q, want /api/v1/fpp/bench-fpp/commands", gotPath)
	}
	if gotBody["action"] != "prevPlaylistItem" {
		t.Errorf("action = %v, want \"prevPlaylistItem\"", gotBody["action"])
	}
	if _, present := gotBody["params"]; present {
		t.Errorf("body = %s, want NO \"params\" key at all for this zero-parameter primitive", rawBody)
	}
	if !strings.Contains(stdout.String(), "not uniquely attributable") {
		t.Errorf("stdout = %q, want the server's own attribution caveat surfaced even on a CONFIRMED outcome", stdout.String())
	}
}

// TestCmdFPPPrevPlaylistItemUnconfirmedUnchangedIndexSurfacesReason
// covers capture section 4's own no-idle-fallback rule for this
// primitive: an unchanged index is reported unconfirmed, with a reason,
// never treated as if the command silently succeeded.
func TestCmdFPPPrevPlaylistItemUnconfirmedUnchangedIndexSurfacesReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-2","idempotencyKey":"k","action":"fpp.prev_playlist_item","instanceId":"bench-fpp",
			"replay":false,"outcome":"unconfirmed","outcomeState":"current",
			"outcomeReason":"fpp.playlist.index is unchanged from the pre-dispatch baseline (1, source fpp_poll)",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:20Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"prev-playlist-item", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitCommandUnconfirmed {
		t.Fatalf("exit code = %d, want exitCommandUnconfirmed", code)
	}
	if !strings.Contains(stdout.String(), "unconfirmed") {
		t.Errorf("stdout = %q, want it to report \"unconfirmed\"", stdout.String())
	}
	if !strings.Contains(stdout.String(), "unchanged from the pre-dispatch baseline") {
		t.Errorf("stdout = %q, want the server's own outcomeReason surfaced verbatim", stdout.String())
	}
}

func TestCmdFPPPrevPlaylistItemRequiresInstanceID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"prev-playlist-item"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a missing instance-id argument", code)
	}
}

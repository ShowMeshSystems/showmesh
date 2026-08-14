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

// TestCmdFPPNextPlaylistItemRequestShape pins the exact request body: no
// "params" key at all for this zero-parameter primitive.
func TestCmdFPPNextPlaylistItemRequestShape(t *testing.T) {
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
			"id":"cmd-1","idempotencyKey":"`+fmt.Sprint(gotBody["idempotencyKey"])+`","action":"fpp.next_playlist_item","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current",
			"outcomeReason":"fpp.playlist.index moved from 1 to 2 (source fpp_poll) — note: this counter also advances on FPP's own item boundaries, so movement is not uniquely attributable to this command",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"next-playlist-item", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/fpp/bench-fpp/commands" {
		t.Errorf("path = %q, want /api/v1/fpp/bench-fpp/commands", gotPath)
	}
	if gotBody["action"] != "nextPlaylistItem" {
		t.Errorf("action = %v, want \"nextPlaylistItem\"", gotBody["action"])
	}
	if _, present := gotBody["params"]; present {
		t.Errorf("body = %s, want NO \"params\" key at all for this zero-parameter primitive", rawBody)
	}
	// Capture section 4's own caveat: movement is not uniquely
	// attributable to this command, and the reason string says so — even
	// on a CONFIRMED outcome, this command must surface it (not only on
	// unconfirmed).
	if !strings.Contains(stdout.String(), "not uniquely attributable") {
		t.Errorf("stdout = %q, want the server's own attribution caveat surfaced even on a CONFIRMED outcome", stdout.String())
	}
}

// TestCmdFPPNextPlaylistItemConfirmedAtPlaylistEndSurfacesReason is
// capture section 3.5's own hazard: "Next" past the LAST item ends the
// playlist entirely, and the predicate accepts fpp.status == "idle" as
// confirmation of that. The CLI must surface that this was reached via
// "idle", not silently render it identically to an ordinary item
// advance.
func TestCmdFPPNextPlaylistItemConfirmedAtPlaylistEndSurfacesReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-2","idempotencyKey":"k","action":"fpp.next_playlist_item","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current",
			"outcomeReason":"fpp.status = \"idle\" (source fpp_poll): Next Playlist Item at the last item ends the playlist, which this predicate accepts as confirmation of the command's largest possible effect",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"next-playlist-item", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK", code)
	}
	if !strings.Contains(stdout.String(), "ends the playlist") {
		t.Errorf("stdout = %q, want the server's own outcomeReason surfaced verbatim", stdout.String())
	}
}

// TestCmdFPPNextPlaylistItemUnconfirmedNoBaselineSurfacesReason covers
// the no-pre-dispatch-baseline case (fppCaptureIndexBaseline's own
// IndexKnown=false path): reported unconfirmed rather than inventing a
// baseline, and the CLI must surface that reasoning.
func TestCmdFPPNextPlaylistItemUnconfirmedNoBaselineSurfacesReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-3","idempotencyKey":"k","action":"fpp.next_playlist_item","instanceId":"bench-fpp",
			"replay":false,"outcome":"unconfirmed","outcomeState":"not_collected",
			"outcomeReason":"no pre-dispatch fpp.playlist.index baseline was available at dispatch time, so index movement cannot be evaluated (absence of evidence is not evidence of absence: this is reported unconfirmed rather than inventing a baseline); fpp.status has also not reached \"idle\"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:20Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"next-playlist-item", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitCommandUnconfirmed {
		t.Fatalf("exit code = %d, want exitCommandUnconfirmed", code)
	}
	if !strings.Contains(stdout.String(), "unconfirmed") {
		t.Errorf("stdout = %q, want it to report \"unconfirmed\"", stdout.String())
	}
	if !strings.Contains(stdout.String(), "inventing a baseline") {
		t.Errorf("stdout = %q, want the server's own outcomeReason surfaced verbatim", stdout.String())
	}
}

func TestCmdFPPNextPlaylistItemRequiresInstanceID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"next-playlist-item"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a missing instance-id argument", code)
	}
}

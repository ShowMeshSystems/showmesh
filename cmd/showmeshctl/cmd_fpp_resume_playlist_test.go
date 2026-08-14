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

// TestCmdFPPResumePlaylistRequestShape pins the exact request body: no
// "params" key at all for this zero-parameter primitive.
func TestCmdFPPResumePlaylistRequestShape(t *testing.T) {
	var gotMethod, gotPath string
	var rawBody []byte
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		rawBody, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(rawBody, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-1","idempotencyKey":"`+fmt.Sprint(gotBody["idempotencyKey"])+`","action":"fpp.resume_playlist","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"resume-playlist", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/fpp/bench-fpp/commands" {
		t.Errorf("path = %q, want /api/v1/fpp/bench-fpp/commands", gotPath)
	}
	if gotBody["action"] != "resumePlaylist" {
		t.Errorf("action = %v, want \"resumePlaylist\"", gotBody["action"])
	}
	if _, present := gotBody["params"]; present {
		t.Errorf("body = %s, want NO \"params\" key at all for this zero-parameter primitive", rawBody)
	}
}

// TestCmdFPPResumePlaylistUnconfirmedWhileIdleSurfacesReason: FPP's own
// response text ("Playlist Restarted") is not evidence of anything;
// capture section 3.4 notes the observed index does not move. This
// command's own predicate (fpp.status == "playing") is what decides the
// outcome.
func TestCmdFPPResumePlaylistUnconfirmedWhileIdleSurfacesReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-2","idempotencyKey":"k","action":"fpp.resume_playlist","instanceId":"bench-fpp",
			"replay":false,"outcome":"unconfirmed","outcomeState":"current",
			"outcomeReason":"observed fpp.status = idle (source fpp_poll), want \"playing\"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:20Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"resume-playlist", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitCommandUnconfirmed {
		t.Fatalf("exit code = %d, want exitCommandUnconfirmed", code)
	}
	if !strings.Contains(stdout.String(), "unconfirmed") {
		t.Errorf("stdout = %q, want it to report \"unconfirmed\"", stdout.String())
	}
	if !strings.Contains(stdout.String(), "want \"playing\"") {
		t.Errorf("stdout = %q, want the server's own outcomeReason surfaced verbatim", stdout.String())
	}
}

func TestCmdFPPResumePlaylistRequiresInstanceID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"resume-playlist"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a missing instance-id argument", code)
	}
}

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

// TestCmdFPPStartPlaylistDefaultRequestShape pins the exact request body a
// bare "fpp start-playlist <id> <name>" (no flags) sends: params always
// present (unlike the zero-parameter primitives), repeat defaulting to
// false, ifBusy defaulting to "refuse" — matching
// docs/bench/fpp-command-vocabulary.md section 4's own documented
// defaults, sent explicitly rather than left for the coordinator to
// infer.
func TestCmdFPPStartPlaylistDefaultRequestShape(t *testing.T) {
	var gotPath string
	var rawBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-1","idempotencyKey":"k","action":"fpp.start_playlist","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"start-playlist", "--server", ts.URL, "bench-fpp", "showmesh-test"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/fpp/bench-fpp/commands" {
		t.Errorf("path = %q, want /api/v1/fpp/bench-fpp/commands", gotPath)
	}

	var got map[string]any
	if err := json.Unmarshal(rawBody, &got); err != nil {
		t.Fatalf("decode request body: %v; body=%s", err, rawBody)
	}
	if got["action"] != "startPlaylist" {
		t.Errorf("action = %v, want \"startPlaylist\"", got["action"])
	}
	params, ok := got["params"].(map[string]any)
	if !ok {
		t.Fatalf("body = %s, want a \"params\" object", rawBody)
	}
	if params["playlist"] != "showmesh-test" {
		t.Errorf("params.playlist = %v, want \"showmesh-test\"", params["playlist"])
	}
	if params["repeat"] != false {
		t.Errorf("params.repeat = %v, want false (the documented default)", params["repeat"])
	}
	if params["ifBusy"] != "refuse" {
		t.Errorf("params.ifBusy = %v, want \"refuse\" (the documented default)", params["ifBusy"])
	}
}

// TestCmdFPPStartPlaylistFlagsSetRequestShape proves --repeat and
// --if-busy=replace actually change the wire request, not merely the
// local flag variables.
func TestCmdFPPStartPlaylistFlagsSetRequestShape(t *testing.T) {
	var rawBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-2","idempotencyKey":"k","action":"fpp.start_playlist","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"start-playlist", "--repeat", "--if-busy", "replace", "--server", ts.URL,
		"bench-fpp", "showmesh-test"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rawBody, &got); err != nil {
		t.Fatalf("decode request body: %v; body=%s", err, rawBody)
	}
	params := got["params"].(map[string]any)
	if params["repeat"] != true {
		t.Errorf("params.repeat = %v, want true", params["repeat"])
	}
	if params["ifBusy"] != "replace" {
		t.Errorf("params.ifBusy = %v, want \"replace\"", params["ifBusy"])
	}
}

// TestCmdFPPStartPlaylistRejectsInvalidIfBusy proves an --if-busy value
// outside the two-member wire vocabulary is refused locally, as a usage
// error, before any request is attempted — never sent to the coordinator
// to come back as an unrelated 400.
func TestCmdFPPStartPlaylistRejectsInvalidIfBusy(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"start-playlist", "--if-busy", "bogus", "--server", ts.URL,
		"bench-fpp", "showmesh-test"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for an invalid --if-busy value", code)
	}
	if called {
		t.Error("the coordinator was contacted despite an invalid --if-busy value; want a local refusal before dispatch")
	}
	if !strings.Contains(stderr.String(), "if-busy") {
		t.Errorf("stderr = %q, want it to name --if-busy", stderr.String())
	}
}

// TestCmdFPPStartPlaylistRequiresPlaylistName proves both positional
// arguments are required.
func TestCmdFPPStartPlaylistRequiresPlaylistName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"start-playlist", "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a missing playlist-name argument", code)
	}
}

// TestCmdFPPStartPlaylistBusyConflictNamesWhatIsPlaying is this command's
// own version of the 409 requirement: when the coordinator refuses
// because a DIFFERENT playlist is confirmed playing (ifBusy=refuse, the
// default), the CLI must print what is actually playing and how to
// override, not a bare "conflict" — the coordinator's own Problem.Detail
// (problem.go's fppStartPlaylistBusyProblem) already carries that text,
// and this command must not discard it. The fixture's own `type` is
// "fpp-start-playlist-busy" (Step 8 review finding 8: this used to be the
// plain, shared "conflict" — problemFPPStartPlaylistBusy's own doc
// comment in problem.go), not "conflict" — this test would still pass
// against the OLD type too (this CLI maps every 409 it does not
// specifically recognize to exitConflict via the status fallback), but
// the fixture is kept honest about what the real coordinator now sends,
// per this same finding.
func TestCmdFPPStartPlaylistBusyConflictNamesWhatIsPlaying(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/fpp-start-playlist-busy",
			"title":"Start Playlist refused: a different playlist is currently playing","status":409,
			"detail":"instance \"bench-fpp\" is currently playing \"showmesh-bench-3item\"; this request's ifBusy=\"refuse\" (the default) refuses to interrupt it. Resend with ifBusy=\"replace\" to replace the running show, or wait for it to finish.",
			"serverTime":"2026-08-13T22:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"start-playlist", "--server", ts.URL, "bench-fpp", "showmesh-test"}, &stdout, &stderr, time.Now)
	if code == exitOK {
		t.Fatalf("exit code = 0 for a 409 conflict; must never exit success")
	}
	// Finding 4 (Step 8 client-side review): this must land on its own
	// exitConflict code, not collapse into exitAPIError (6, "the
	// coordinator returned some other error") — a script branching on $?
	// needs to tell "refused on purpose, retry with --if-busy=replace"
	// apart from an actual coordinator malfunction.
	if code != exitConflict {
		t.Errorf("exit code = %d, want exitConflict (%d); stderr=%s", code, exitConflict, stderr.String())
	}
	if !strings.Contains(stderr.String(), "showmesh-bench-3item") {
		t.Errorf("stderr = %q, want it to name what is currently playing", stderr.String())
	}
	if !strings.Contains(stderr.String(), "ifBusy=\\\"replace\\\"") && !strings.Contains(stderr.String(), "replace") {
		t.Errorf("stderr = %q, want it to name the override (ifBusy=replace)", stderr.String())
	}
}

// TestCmdFPPStartPlaylistReplayParamsConflictNamesDifference proves the
// OTHER 409 shape this endpoint can answer — a reused idempotency key with
// DIFFERENT normalized params — is also surfaced with its own detail text
// naming what differed, not collapsed into the same generic message as
// the ifBusy conflict above.
func TestCmdFPPStartPlaylistReplayParamsConflictNamesDifference(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/conflict",
			"title":"Idempotency key already used with different parameters","status":409,
			"detail":"idempotencyKey was already used for command cmd-1 (action \"startPlaylist\", instanceId \"bench-fpp\") with params {\"ifBusy\":\"refuse\",\"playlist\":\"showmesh-test\",\"repeat\":false}; this request names the SAME action and instanceId but DIFFERENT normalized params: {\"ifBusy\":\"refuse\",\"playlist\":\"other-playlist\",\"repeat\":false}.",
			"serverTime":"2026-08-13T22:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"start-playlist", "--server", ts.URL, "bench-fpp", "other-playlist"}, &stdout, &stderr, time.Now)
	if code == exitOK {
		t.Fatalf("exit code = 0 for a 409 conflict; must never exit success")
	}
	if !strings.Contains(stderr.String(), "showmesh-test") || !strings.Contains(stderr.String(), "other-playlist") {
		t.Errorf("stderr = %q, want it to name which params differed (both the original and this request's playlist)", stderr.String())
	}
}

// TestCmdFPPStartPlaylistUnconfirmedSurfacesReason proves the "unconfirmed"
// path prints the server's own outcome reason, matching stop-playlist's
// established convention.
func TestCmdFPPStartPlaylistUnconfirmedSurfacesReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-3","idempotencyKey":"k","action":"fpp.start_playlist","instanceId":"bench-fpp",
			"replay":false,"outcome":"unconfirmed","outcomeState":"current",
			"outcomeReason":"fpp.status reads \"playing\" but fpp.playlist.name = \"other\" (source fpp_poll), want \"showmesh-test\" — FPP's own scheduler may have started a DIFFERENT playlist between dispatch and this check",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:20Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"start-playlist", "--server", ts.URL, "bench-fpp", "showmesh-test"}, &stdout, &stderr, time.Now)
	if code != exitCommandUnconfirmed {
		t.Fatalf("exit code = %d, want exitCommandUnconfirmed", code)
	}
	if !strings.Contains(stdout.String(), "unconfirmed") {
		t.Errorf("stdout = %q, want it to report \"unconfirmed\"", stdout.String())
	}
	if !strings.Contains(stdout.String(), "own scheduler may have started") {
		t.Errorf("stdout = %q, want the server's own outcomeReason surfaced verbatim", stdout.String())
	}
}

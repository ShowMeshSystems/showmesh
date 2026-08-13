package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCmdFPPStopPlaylistConfirmedExitsOK drives the real subcommand
// against a fake coordinator reporting a confirmed outcome, and pins the
// request shape: POST, the exact path, a JSON body naming the "stopPlaylist"
// action and a non-empty idempotencyKey.
func TestCmdFPPStopPlaylistConfirmedExitsOK(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-12T22:00:00Z","command":{
			"id":"cmd-1","idempotencyKey":"`+fmt.Sprint(gotBody["idempotencyKey"])+`","action":"fpp.stop_playlist",
			"instanceId":"bench-fpp","replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-12T22:00:00Z","resolvedAt":"2026-08-12T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"stop-playlist", "--server", ts.URL, "--token", "smsh_test", "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/fpp/bench-fpp/commands" {
		t.Errorf("path = %q, want /api/v1/fpp/bench-fpp/commands", gotPath)
	}
	if gotBody["action"] != "stopPlaylist" {
		t.Errorf("request body action = %v, want \"stopPlaylist\"", gotBody["action"])
	}
	key, _ := gotBody["idempotencyKey"].(string)
	if key == "" {
		t.Errorf("request body idempotencyKey is empty, want a minted value")
	}
	if !strings.Contains(stdout.String(), "confirmed") {
		t.Errorf("stdout = %q, want it to report \"confirmed\"", stdout.String())
	}
}

// TestCmdFPPStopPlaylistUnconfirmedExitsNonZero is this command's own
// version of ADR-003: a 200 HTTP response carrying outcome=unconfirmed
// must not exit 0. This is the test that would pass against a defective
// implementation treating any 2xx as success — see this task's report for
// the mutation run against it.
func TestCmdFPPStopPlaylistUnconfirmedExitsNonZero(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-12T22:00:00Z","command":{
			"id":"cmd-2","idempotencyKey":"k","action":"fpp.stop_playlist","instanceId":"bench-fpp",
			"replay":false,"outcome":"unconfirmed","outcomeState":"current",
			"outcomeReason":"observed fpp.status = playing, want \"idle\"","attributionDegraded":false,
			"dispatchedAt":"2026-08-12T22:00:00Z","resolvedAt":"2026-08-12T22:00:20Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"stop-playlist", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code == exitOK {
		t.Fatalf("exit code = 0 for an UNCONFIRMED outcome; must never exit success on a 200 alone (ADR-003)")
	}
	if code != exitCommandUnconfirmed {
		t.Errorf("exit code = %d, want exitCommandUnconfirmed (%d)", code, exitCommandUnconfirmed)
	}
	if !strings.Contains(stdout.String(), "unconfirmed") {
		t.Errorf("stdout = %q, want it to report \"unconfirmed\"", stdout.String())
	}
	if !strings.Contains(stdout.String(), "playing") {
		t.Errorf("stdout = %q, want the outcomeReason surfaced", stdout.String())
	}
}

// TestCmdFPPStopPlaylistReplayIsSurfaced proves a replay response (never
// dispatched by this invocation) is reported distinctly, not silently
// treated as identical to a fresh confirmed dispatch.
func TestCmdFPPStopPlaylistReplayIsSurfaced(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-12T22:00:00Z","command":{
			"id":"cmd-3","idempotencyKey":"k","action":"fpp.stop_playlist","instanceId":"bench-fpp",
			"replay":true,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-12T21:59:00Z","resolvedAt":"2026-08-12T21:59:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"stop-playlist", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK for a replayed CONFIRMED result; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already used") && !strings.Contains(stderr.String(), "replay") {
		t.Errorf("stderr = %q, want it to flag this as a replay, not a fresh dispatch", stderr.String())
	}
}

// TestCmdFPPStopPlaylistForbiddenNamesScope exercises this command
// against a 403, proving the CLI surfaces the missing-scope detail rather
// than a bare "forbidden".
func TestCmdFPPStopPlaylistForbiddenNamesScope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/forbidden","title":"Forbidden","status":403,
			"detail":"this action requires the fpp:command scope","serverTime":"2026-08-12T22:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"stop-playlist", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitForbidden {
		t.Fatalf("exit code = %d, want exitForbidden; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "fpp:command") {
		t.Errorf("stderr = %q, want it to name the missing scope", stderr.String())
	}
}

// TestCmdFPPStopPlaylistMintsDistinctKeysPerInvocation proves two
// separate invocations never accidentally collide on the same
// idempotency key, which would make the coordinator treat the second, a
// genuinely new request, as a replay of the first.
func TestCmdFPPStopPlaylistMintsDistinctKeysPerInvocation(t *testing.T) {
	var keys []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		keys = append(keys, fmt.Sprint(body["idempotencyKey"]))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-12T22:00:00Z","command":{
			"id":"cmd-x","idempotencyKey":"x","action":"fpp.stop_playlist","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-12T22:00:00Z","resolvedAt":"2026-08-12T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	cmdFPP([]string{"stop-playlist", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	stdout.Reset()
	stderr.Reset()
	cmdFPP([]string{"stop-playlist", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)

	if len(keys) != 2 {
		t.Fatalf("got %d requests, want 2", len(keys))
	}
	if keys[0] == keys[1] {
		t.Errorf("both invocations sent the same idempotencyKey %q, want two distinct values", keys[0])
	}
}

func TestCmdFPPStopPlaylistRequiresInstanceID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"stop-playlist"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a missing instance-id argument", code)
	}
}

// --- Step 7 seam C review defect 1: this subcommand's own request budget
// must never be smaller than what the coordinator's own confirmation
// deadline needs, regardless of --timeout's global default. ---

// TestEffectiveStopPlaylistTimeoutNeverBelowMinimum is the fast, pure half
// of defect 1's guard (the slow, real half is
// test/integration's TestCLIStopPlaylistTimeoutSurvivesServerConfirmDeadline,
// which runs the real coordinator and this real binary together). This
// test alone cannot catch minStopPlaylistClientTimeout itself being set
// too small relative to the SERVER's default — no unit test can, since the
// two are two independent literals by design (see that constant's own doc
// comment) — it only proves --timeout can never override the constant
// downward, which is this function's entire job.
func TestEffectiveStopPlaylistTimeoutNeverBelowMinimum(t *testing.T) {
	cases := []struct {
		name string
		flag time.Duration
		want time.Duration
	}{
		{"global default (10s) is raised to the minimum", 10 * time.Second, minStopPlaylistClientTimeout},
		{"zero is raised to the minimum", 0, minStopPlaylistClientTimeout},
		{"exactly the minimum is left alone", minStopPlaylistClientTimeout, minStopPlaylistClientTimeout},
		{"an explicit larger value is honored, never clamped down", 5 * time.Minute, 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveStopPlaylistTimeout(tc.flag)
			if got != tc.want {
				t.Errorf("effectiveStopPlaylistTimeout(%v) = %v, want %v", tc.flag, got, tc.want)
			}
		})
	}
}

// TestCmdFPPStopPlaylistSurvivesAResponseSlowerThanTheExplicitTimeoutFlag
// is this defect's own reproduction, fixed, kept fast by using an
// explicit small --timeout rather than waiting out the real 35s minimum:
// with --timeout set to 200ms (well below
// [minStopPlaylistClientTimeout]), a fake coordinator that takes 400ms to
// answer must still be reached successfully. Before this fix, this
// subcommand used --timeout directly as both the *http.Client's own
// Timeout and the request context's deadline, so a 400ms response against
// a 200ms budget would abort as a bare transport timeout — exactly what a
// real coordinator's confirmation wait does routinely against the old 10s
// global default. Broken to verify: reverting effectiveStopPlaylistTimeout
// to `return flagTimeout` (ignoring the minimum entirely) makes this test
// fail with a context-deadline-exceeded transport error instead of
// "confirmed" — see this task's report.
func TestCmdFPPStopPlaylistSurvivesAResponseSlowerThanTheExplicitTimeoutFlag(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-12T22:00:00Z","command":{
			"id":"cmd-slow","idempotencyKey":"k","action":"fpp.stop_playlist","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-12T22:00:00Z","resolvedAt":"2026-08-12T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"stop-playlist", "--server", ts.URL, "--timeout", "200ms", "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; a 400ms-slow response must survive a 200ms --timeout flag, because this "+
			"subcommand's own minimum overrides too-small values (ADR-003/defect 1); stdout=%s stderr=%s",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "confirmed") {
		t.Errorf("stdout = %q, want it to report \"confirmed\"", stdout.String())
	}
}

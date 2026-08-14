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

// This file tests cmd_fpp_command.go's shared plumbing directly, rather
// than through one particular subcommand — cmd_fpp_stop_playlist_test.go
// and each of Step 8's seven new cmd_fpp_*_test.go files still exercise
// this machinery end to end through their own subcommand, which is what
// actually proves each one is WIRED to it; these tests are the fast,
// isolated half.

// TestEffectiveFPPCommandTimeoutNeverBelowMinimum is Step 7 seam C review
// defect 1's guard, generalized: the fast, pure half (the slow, real half
// is test/integration's TestCLIStopPlaylistTimeoutSurvivesServerConfirmDeadline,
// which runs the real coordinator and the real showmeshctl binary
// together). This test alone cannot catch minFPPCommandClientTimeout
// itself being set too small relative to the SERVER's default — no unit
// test can, since the two are two independent literals by design (see that
// constant's own doc comment) — it only proves --timeout can never
// override the constant downward, which is this function's entire job.
func TestEffectiveFPPCommandTimeoutNeverBelowMinimum(t *testing.T) {
	cases := []struct {
		name string
		flag time.Duration
		want time.Duration
	}{
		{"global default (10s) is raised to the minimum", 10 * time.Second, minFPPCommandClientTimeout},
		{"zero is raised to the minimum", 0, minFPPCommandClientTimeout},
		{"exactly the minimum is left alone", minFPPCommandClientTimeout, minFPPCommandClientTimeout},
		{"an explicit larger value is honored, never clamped down", 5 * time.Minute, 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveFPPCommandTimeout(tc.flag)
			if got != tc.want {
				t.Errorf("effectiveFPPCommandTimeout(%v) = %v, want %v", tc.flag, got, tc.want)
			}
		})
	}
}

// TestReportFPPCommandResultSurfacesReasonOnConfirmedOutcome is capture
// section 3.3/4's own CLI-side requirement: stopPlaylistGracefully can be
// CONFIRMED while FPP has only entered a stopping state and the show is
// STILL RUNNING, and the server's own outcomeReason says so explicitly. A
// bare "confirmed: ..." line with no reason would let an operator read
// that as "the show stopped." Broken to verify this test would catch it:
// reverting reportFPPCommandResult's confirmed branch to always print the
// two-argument form (never surfacing OutcomeReason) makes this test fail —
// see this task's report.
func TestReportFPPCommandResultSurfacesReasonOnConfirmedOutcome(t *testing.T) {
	var stdout, stderr bytes.Buffer
	result := fppCommandResult{
		ID: "cmd-graceful", Action: "fpp.stop_playlist_gracefully", InstanceID: "bench-fpp",
		Outcome: "confirmed", OutcomeState: "current",
		OutcomeReason: `fpp.status = "stopping gracefully" (source fpp_poll): FPP accepted the graceful stop and ` +
			"the show is winding down, but has NOT stopped yet",
	}
	code := reportFPPCommandResult(&stdout, &stderr, "fpp stop-playlist-gracefully", result)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK for a confirmed outcome", code)
	}
	if !strings.Contains(stdout.String(), "confirmed") {
		t.Errorf("stdout = %q, want it to report \"confirmed\"", stdout.String())
	}
	if !strings.Contains(stdout.String(), "has NOT stopped yet") {
		t.Errorf("stdout = %q, want the server's own outcomeReason surfaced verbatim even on a CONFIRMED outcome, "+
			"so an operator cannot read \"confirmed\" as \"the show stopped\" (docs/bench/fpp-command-vocabulary.md "+
			"section 3.3/4)", stdout.String())
	}
}

// TestReportFPPCommandResultOmitsEmptyReasonOnConfirmedOutcome is the
// companion case: most primitives' confirmed outcome carries an EMPTY
// outcomeReason (evaluateFPPStatusEvidence, evaluateStartPlaylistEvidence,
// evaluateSetVolumeEvidence all return "" on a clean match) — this proves
// the confirmed branch does not fabricate or print a placeholder reason
// string when the server sent none.
func TestReportFPPCommandResultOmitsEmptyReasonOnConfirmedOutcome(t *testing.T) {
	var stdout, stderr bytes.Buffer
	result := fppCommandResult{
		ID: "cmd-1", Action: "fpp.set_volume", InstanceID: "bench-fpp",
		Outcome: "confirmed", OutcomeState: "current", OutcomeReason: "",
	}
	code := reportFPPCommandResult(&stdout, &stderr, "fpp set-volume", result)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK", code)
	}
	want := "confirmed: fpp.set_volume on bench-fpp (command cmd-1)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q (no trailing empty reason suffix when outcomeReason is \"\")", stdout.String(), want)
	}
}

// TestDispatchFPPCommandNotesTimeoutFloorOnStderr is Finding 2 (Step 8
// client-side review): before this fix, effectiveFPPCommandTimeout's
// floor fired SILENTLY, so an operator who explicitly passed a too-small
// --timeout and then waited out the real 35s minimum was told nothing —
// they read that silence as "my flag was honored and the coordinator is
// pathologically slow," which is backwards, since the coordinator holds a
// dispatched command's response open for its own confirmation deadline
// regardless of what this client asked for. Broken to verify: deleting
// the `if timeout != g.timeout { ... }` block in dispatchFPPCommand (or
// just its fmt.Fprintf call) makes this test fail — see this task's
// report.
func TestDispatchFPPCommandNotesTimeoutFloorOnStderr(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-floor-1","idempotencyKey":"k","action":"fpp.stop_playlist","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	g := &globalFlags{server: ts.URL, output: outputText, timeout: 200 * time.Millisecond}
	code := dispatchFPPCommand(&stdout, &stderr, time.Now, g, "fpp stop-playlist", "bench-fpp", "stopPlaylist", nil)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "200ms") {
		t.Errorf("stderr = %q, want it to name the requested --timeout value (200ms)", stderr.String())
	}
	if !strings.Contains(stderr.String(), minFPPCommandClientTimeout.String()) {
		t.Errorf("stderr = %q, want it to name the floor it was raised to (%s)", stderr.String(), minFPPCommandClientTimeout)
	}
}

// TestDispatchFPPCommandSaysNothingWhenTimeoutFlagAlreadyMeetsTheFloor is
// the companion case: an operator who never asked for a too-small
// --timeout (the global 10s default, or an explicit value already at or
// above the floor) must not see this note fire for a flag they never set
// too low — a note that fires unconditionally would be exactly as
// unhelpful as one that never fires at all.
func TestDispatchFPPCommandSaysNothingWhenTimeoutFlagAlreadyMeetsTheFloor(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-floor-2","idempotencyKey":"k","action":"fpp.stop_playlist","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	g := &globalFlags{server: ts.URL, output: outputText, timeout: minFPPCommandClientTimeout}
	code := dispatchFPPCommand(&stdout, &stderr, time.Now, g, "fpp stop-playlist", "bench-fpp", "stopPlaylist", nil)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "is below this command's own minimum") {
		t.Errorf("stderr = %q, want no floor note when --timeout already meets the minimum", stderr.String())
	}
}

// TestFPPCommandRequestParamsOmittedWhenNil proves fppCommandRequest's own
// wire encoding for a zero-parameter primitive: a nil Params map must
// produce a request body with NO "params" key at all — never
// "\"params\":null" and never an explicit "\"params\":{}" the caller did
// not ask for. docs/bench/fpp-command-vocabulary.md section 2 measured
// absent, null, and empty as three different things FPP itself already
// distinguishes; this is that same distinction applied to this program's
// OWN outbound request.
func TestFPPCommandRequestParamsOmittedWhenNil(t *testing.T) {
	body, err := json.Marshal(fppCommandRequest{Action: "pausePlaylist", IdempotencyKey: "k"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(body), "params") {
		t.Errorf("body = %s, want no \"params\" key at all for a nil Params map", body)
	}
}

// TestFPPCommandRequestParamsPresentWhenSet is the companion case: a
// non-nil Params map (even one containing only default values, e.g.
// start-playlist's own --repeat=false/--if-busy=refuse) is sent as a real
// JSON object, never suppressed.
func TestFPPCommandRequestParamsPresentWhenSet(t *testing.T) {
	body, err := json.Marshal(fppCommandRequest{
		Action: "startPlaylist", IdempotencyKey: "k",
		Params: map[string]any{"playlist": "showmesh-test", "repeat": false, "ifBusy": "refuse"},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	params, ok := decoded["params"].(map[string]any)
	if !ok {
		t.Fatalf("body = %s, want a \"params\" object", body)
	}
	if params["playlist"] != "showmesh-test" || params["repeat"] != false || params["ifBusy"] != "refuse" {
		t.Errorf("params = %v, want playlist/repeat/ifBusy round-tripped exactly", params)
	}
}

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

// --- Shared plumbing: the fast, isolated half, mirroring
// cmd_fpp_command_test.go's own split between plumbing tests (this file)
// and end-to-end subcommand tests (below). ---

func TestEffectiveResolumeActionTimeoutNeverBelowMinimum(t *testing.T) {
	cases := []struct {
		name string
		flag time.Duration
		want time.Duration
	}{
		{"global default (10s) is raised to the minimum", 10 * time.Second, minResolumeActionClientTimeout},
		{"zero is raised to the minimum", 0, minResolumeActionClientTimeout},
		{"exactly the minimum is left alone", minResolumeActionClientTimeout, minResolumeActionClientTimeout},
		{"an explicit larger value is honored, never clamped down", 5 * time.Minute, 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveResolumeActionTimeout(tc.flag)
			if got != tc.want {
				t.Errorf("effectiveResolumeActionTimeout(%v) = %v, want %v", tc.flag, got, tc.want)
			}
		})
	}
}

// TestMinResolumeActionClientTimeoutExceedsServerDefault is the CLI-side
// half of the client-timeout-derived-from-server-deadline reconciliation
// CLAUDE.md requires: this program cannot import
// internal/coordinator/api (importgraph_test.go), so
// resolumeActionHTTPWriteDeadline (55s) and command.ClientTimeoutMargin
// (15s) are independently chosen literals here, mirrored by
// TestResolumeActionHTTPWriteDeadlineFitsWithinCLIClientBudget in
// internal/coordinator/api/resolumeaction_test.go, which hardcodes this
// program's own minResolumeActionClientTimeout (80s). Both tests fail if
// either side is changed without updating the other.
func TestMinResolumeActionClientTimeoutExceedsServerDefault(t *testing.T) {
	// These two MUST match resolumeActionHTTPWriteDeadline
	// (internal/coordinator/api/resolumeaction.go) and
	// command.ClientTimeoutMargin (pkg/command/command.go) exactly.
	const serverWriteDeadline = 55 * time.Second
	const roundTripMargin = 15 * time.Second
	// slack is real headroom over the computed floor, matching the
	// reciprocal server-side test's own slack requirement — not a
	// boundary equality, which cannot distinguish "correct" from "wrong
	// by a coincidence."
	const slack = 10 * time.Second

	need := serverWriteDeadline + roundTripMargin
	if minResolumeActionClientTimeout < need {
		t.Fatalf("minResolumeActionClientTimeout (%s) is below resolumeActionHTTPWriteDeadline (%s) plus a %s "+
			"round-trip margin — this program could abort a dispatch before the coordinator's own write deadline "+
			"elapses, producing a false transport-timeout failure for a healthy, still-working conversation. "+
			"Raise minResolumeActionClientTimeout to match.",
			minResolumeActionClientTimeout, serverWriteDeadline, roundTripMargin)
	}
	if got := minResolumeActionClientTimeout - need; got < slack {
		t.Fatalf("minResolumeActionClientTimeout (%s) leaves only %s of slack over the computed floor (%s), want at least %s",
			minResolumeActionClientTimeout, got, need, slack)
	}
}

func resolumeActionServerBody(id, action, outcome, outcomeReason string, replay, degraded bool) string {
	return fmt.Sprintf(`{"serverTime":"2026-08-14T22:00:00Z","result":{
		"id":%q,"idempotencyKey":"k","action":%q,"params":{},"replay":%t,
		"outcome":%q,"outcomeReason":%q,"attributionDegraded":%t,
		"dispatchedAt":"2026-08-14T22:00:00Z","resolvedAt":"2026-08-14T22:00:01Z"}}`,
		id, action, replay, outcome, outcomeReason, degraded)
}

// TestReportResolumeActionResultOutcomeVocabulary walks every one of the
// five outcomes and asserts the exit code AND that the human-readable
// reason always reaches stdout — never omitted, per this seam's own
// "unconfirmable is not an error" rule applied to this program's own exit
// code choice (never conflated with exitOK, and never conflated with each
// other).
func TestReportResolumeActionResultOutcomeVocabulary(t *testing.T) {
	cases := []struct {
		outcome  string
		wantExit int
	}{
		{"confirmed", exitOK},
		{"unconfirmed", exitCommandUnconfirmed},
		{"unconfirmable", exitActionUnconfirmable},
		{"refused", exitActionRefused},
		{"failed", exitActionFailed},
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			var stdout bytes.Buffer
			result := resolumeActionResult{ID: "cmd-1", Action: "launchClip", Outcome: tc.outcome, OutcomeReason: "reason for " + tc.outcome}
			code := reportResolumeActionResult(&stdout, "resolume action launch-clip", result)
			if code != tc.wantExit {
				t.Errorf("exit code = %d, want %d for outcome %q", code, tc.wantExit, tc.outcome)
			}
			if !strings.Contains(stdout.String(), "reason for "+tc.outcome) {
				t.Errorf("stdout = %q, want the outcomeReason surfaced for outcome %q", stdout.String(), tc.outcome)
			}
			if tc.outcome != "confirmed" && code == exitOK {
				t.Errorf("outcome %q must never exit 0", tc.outcome)
			}
		})
	}
}

// TestReportResolumeActionWarningsDegradedAttributionWarns proves the
// ADR-024 decision 11 exemption (blackout/clearLayer proceeding on a
// failing audit store) is surfaced to the operator, not silently absorbed
// — in both --output modes, since dispatchResolumeAction calls
// reportResolumeActionWarnings before branching on g.output.
func TestReportResolumeActionWarningsDegradedAttributionWarns(t *testing.T) {
	var stderr bytes.Buffer
	result := resolumeActionResult{ID: "cmd-1", Action: "blackout", Outcome: "confirmed", OutcomeReason: "ok", AttributionDegraded: true}
	reportResolumeActionWarnings(&stderr, "resolume action blackout", result)
	if !strings.Contains(stderr.String(), "degraded attribution") {
		t.Errorf("stderr = %q, want a degraded-attribution warning", stderr.String())
	}
}

func TestResolumeActionRequestParamsOmittedWhenNil(t *testing.T) {
	body, err := json.Marshal(resolumeActionRequest{Action: "blackout", IdempotencyKey: "k"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(body), "params") {
		t.Errorf("body = %s, want no \"params\" key at all for a nil Params map (blackout takes none)", body)
	}
}

// --- End-to-end: each subcommand driven against a fake coordinator,
// proving actual wiring rather than only the shared plumbing above. ---

func TestCmdResolumeActionLaunchClipConfirmedExitsOK(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, resolumeActionServerBody("cmd-1", "launchClip", "confirmed",
			"clip connected (via resolume-rest)", false, false))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"launch-clip", "--server", ts.URL, "--token", "smsh_test", "clip-1"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/resolume/actions" {
		t.Errorf("path = %q, want /api/v1/resolume/actions", gotPath)
	}
	if gotBody["action"] != "launchClip" {
		t.Errorf("request body action = %v, want \"launchClip\"", gotBody["action"])
	}
	params, _ := gotBody["params"].(map[string]any)
	if params["id"] != "clip-1" {
		t.Errorf("request body params.id = %v, want \"clip-1\"", params["id"])
	}
	key, _ := gotBody["idempotencyKey"].(string)
	if key == "" {
		t.Error("request body idempotencyKey is empty, want a minted value")
	}
	if !strings.Contains(stdout.String(), "confirmed") {
		t.Errorf("stdout = %q, want it to report \"confirmed\"", stdout.String())
	}
}

func TestCmdResolumeActionBlackoutSendsNoParams(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, resolumeActionServerBody("cmd-2", "blackout", "confirmed",
			"every tracked layer's active_clip reported absent", false, false))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"blackout", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, present := gotBody["params"]; present {
		t.Errorf("request body = %v, want no \"params\" key for blackout", gotBody)
	}
}

func TestCmdResolumeActionSetLayerBypassSendsBoolParam(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, resolumeActionServerBody("cmd-3", "setLayerBypass", "confirmed", "layer bypassed reached true", false, false))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"set-layer-bypass", "--server", ts.URL, "layer-1", "true"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	params, _ := gotBody["params"].(map[string]any)
	if params["id"] != "layer-1" || params["bypassed"] != true {
		t.Errorf("request body params = %v, want {id: layer-1, bypassed: true}", params)
	}
}

func TestCmdResolumeActionSetLayerBypassRejectsBadBoolValue(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"set-layer-bypass", "--server", "http://unused.invalid", "layer-1", "maybe"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

func TestCmdResolumeActionUnconfirmableExitsNonZeroButIsNotAnError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, resolumeActionServerBody("cmd-4", "launchClip", "unconfirmable",
			"the clip was already playing before dispatch; post-dispatch evidence cannot confirm or refute this", false, false))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"launch-clip", "--server", ts.URL, "clip-1"}, &stdout, &stderr, time.Now)
	if code != exitActionUnconfirmable {
		t.Fatalf("exit code = %d, want exitActionUnconfirmable (%d); stdout=%s stderr=%s", code, exitActionUnconfirmable, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "unconfirmable") {
		t.Errorf("stdout = %q, want it to report \"unconfirmable\"", stdout.String())
	}
	if strings.Contains(strings.ToLower(stdout.String()), "error") {
		t.Errorf("stdout = %q, want no \"error\" wording — unconfirmable is not an error", stdout.String())
	}
}

func TestCmdResolumeActionRefusedExitsActionRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, resolumeActionServerBody("cmd-5", "launchClip", "refused",
			"clip's deck is not selected: expected deck deck-1, selected deck-2", false, false))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"launch-clip", "--server", ts.URL, "clip-1"}, &stdout, &stderr, time.Now)
	if code != exitActionRefused {
		t.Fatalf("exit code = %d, want exitActionRefused (%d); stdout=%s stderr=%s", code, exitActionRefused, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "refused") {
		t.Errorf("stdout = %q, want it to report \"refused\"", stdout.String())
	}
}

// TestCmdResolumeActionJSONOutputStillWarnsOnStderr proves the Replay and
// AttributionDegraded warnings reach stderr in --output json mode too: the
// warnings are operator-facing, stderr is not the JSON stream, and both
// facts already appear in the JSON body, so omitting them from stderr in
// this mode only would make the two --output modes inconsistent for no
// reason.
func TestCmdResolumeActionJSONOutputStillWarnsOnStderr(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, resolumeActionServerBody("cmd-6", "blackout", "confirmed", "ok", true, true))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"blackout", "--server", ts.URL, "--output", "json"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "already used") {
		t.Errorf("stderr = %q, want the replay warning even in --output json mode", stderr.String())
	}
	if !strings.Contains(stderr.String(), "degraded attribution") {
		t.Errorf("stderr = %q, want the degraded-attribution warning even in --output json mode", stderr.String())
	}
	var decoded resolumeActionResponse
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout was not valid JSON: %v; stdout=%s", err, stdout.String())
	}
	if !decoded.Result.Replay || !decoded.Result.AttributionDegraded {
		t.Errorf("decoded JSON body = %+v, want both Replay and AttributionDegraded true", decoded.Result)
	}
}

func TestCmdResolumeActionForbiddenNamesScope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/forbidden","title":"Forbidden","status":403,
			"detail":"this principal does not hold the required scope: resolume:action","serverTime":"2026-08-14T22:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"launch-clip", "--server", ts.URL, "clip-1"}, &stdout, &stderr, time.Now)
	if code != exitForbidden {
		t.Fatalf("exit code = %d, want exitForbidden; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "resolume:action") {
		t.Errorf("stderr = %q, want it to name the missing scope resolume:action", stderr.String())
	}
}

func TestCmdResolumeActionListRendersVocabulary(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T22:00:00Z","actions":[
			{"name":"blackout","params":[],"auditExempt":true,"coordinatorRequired":true},
			{"name":"launchClip","params":[{"name":"id","kind":"string","required":true}],"auditExempt":false,"coordinatorRequired":true}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"list", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "blackout") || !strings.Contains(stdout.String(), "launchClip") {
		t.Errorf("stdout = %q, want both actions listed", stdout.String())
	}
}

func TestCmdResolumeActionUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"delete-everything"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdResolumeActionRequiresID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"launch-clip"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage (no clip id supplied)", code)
	}
}

func TestCmdResolumeActionHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdResolumeAction([]string{"--help"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK", code)
	}
	if !strings.Contains(stdout.String(), "resolume:action") {
		t.Errorf("help output = %q, want it to name the resolume:action scope", stdout.String())
	}
}

// TestCmdResolumeDispatchesToAction proves "showmeshctl resolume action
// ..." (the top-level "resolume" dispatch) actually reaches
// cmdResolumeAction — the wiring half TestCmdResolumeActionLaunchClipConfirmedExitsOK's
// own direct call cannot prove by itself.
func TestCmdResolumeDispatchesToAction(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, resolumeActionServerBody("cmd-6", "blackout", "confirmed", "ok", false, false))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"action", "blackout", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

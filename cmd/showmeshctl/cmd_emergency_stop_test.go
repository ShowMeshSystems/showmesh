package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- exit code / report plumbing: fast, no HTTP ---

func TestExitCodeForEmergencyStopResultTakesTheWorstStopOutcome(t *testing.T) {
	cases := []struct {
		name                  string
		outcomes              []string
		noInstancesConfigured bool
		want                  int
	}{
		{"all confirmed", []string{"confirmed", "confirmed"}, false, exitOK},
		{"confirmed zero instances", nil, true, exitOK},
		{"one unconfirmed", []string{"confirmed", "unconfirmed"}, false, exitCommandUnconfirmed},
		{"one refused", []string{"confirmed", "refused"}, false, exitActionRefused},
		{"one failed", []string{"confirmed", "failed"}, false, exitActionFailed},
		{"failed outranks refused", []string{"refused", "failed"}, false, exitActionFailed},
		{"failed outranks unconfirmed", []string{"unconfirmed", "failed"}, false, exitActionFailed},
		{"refused outranks unconfirmed", []string{"unconfirmed", "refused"}, false, exitActionRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var outcomes []emergencyStopInstanceOutcome
			for _, o := range tc.outcomes {
				outcomes = append(outcomes, emergencyStopInstanceOutcome{InstanceID: "i", Outcome: o})
			}
			got := exitCodeForEmergencyStopResult(emergencyStopResult{StopOutcomes: outcomes, NoInstancesConfigured: tc.noInstancesConfigured})
			if got != tc.want {
				t.Errorf("exitCodeForEmergencyStopResult(%v, noInstancesConfigured=%v) = %d, want %d", tc.outcomes, tc.noInstancesConfigured, got, tc.want)
			}
		})
	}
}

// An empty StopOutcomes array WITHOUT noInstancesConfigured set (a
// coordinator that predates this field, or a malformed response) used to
// read as a silent success: exit 0, nothing printed. That is the exact
// silent-success shape this whole feature exists to prevent, relocated to
// the client.
func TestExitCodeForEmergencyStopResultTreatsUnexplainedEmptyOutcomesAsFailure(t *testing.T) {
	got := exitCodeForEmergencyStopResult(emergencyStopResult{StopOutcomes: nil, NoInstancesConfigured: false})
	if got != exitActionFailed {
		t.Fatalf("exitCodeForEmergencyStopResult(empty, noInstancesConfigured=false) = %d, want exitActionFailed (%d)", got, exitActionFailed)
	}
}

func TestReportEmergencyStopResultWarnsAndFailsOnUnexplainedEmptyOutcomes(t *testing.T) {
	var stdout bytes.Buffer
	code := reportEmergencyStopResult(&stdout, "showmeshctl emergency-stop stop", emergencyStopResult{Level: "stop"})
	if code != exitActionFailed {
		t.Fatalf("exit code = %d, want exitActionFailed (%d)", code, exitActionFailed)
	}
	if stdout.Len() == 0 {
		t.Fatal("stdout is empty: an unexplained empty result must never print nothing, the exact silent-success shape this fixes")
	}
	if !strings.Contains(stdout.String(), "WARNING") {
		t.Errorf("stdout = %q, want an explicit WARNING", stdout.String())
	}
}

// round 3's own second defect: the night-session step is not a follow-up
// action, and its own failure must move the exit code even when every FPP
// stop instance confirmed.
func TestExitCodeForEmergencyStopResultReflectsNightSessionFailure(t *testing.T) {
	ns := emergencyStopNightSessionOutcome{Present: true, SessionID: "sess-1", Error: "power-down step failed: injected"}
	result := emergencyStopResult{
		StopOutcomes: []emergencyStopInstanceOutcome{{InstanceID: "player-01", Outcome: "confirmed"}},
		NightSession: &ns,
	}
	if got := exitCodeForEmergencyStopResult(result); got != exitActionFailed {
		t.Fatalf("exitCodeForEmergencyStopResult = %d, want exitActionFailed (%d): a night-session failure must not read as exitOK even when the stop itself confirmed", got, exitActionFailed)
	}
}

func TestExitCodeForEmergencyStopResultReflectsFollowUpConfigError(t *testing.T) {
	result := emergencyStopResult{
		StopOutcomes:        []emergencyStopInstanceOutcome{{InstanceID: "player-01", Outcome: "confirmed"}},
		FollowUpConfigError: "follow-up actions could not be read: injected",
	}
	if got := exitCodeForEmergencyStopResult(result); got != exitActionFailed {
		t.Fatalf("exitCodeForEmergencyStopResult = %d, want exitActionFailed (%d)", got, exitActionFailed)
	}
}

// The WORSE of the stop outcomes and the night-session failure always
// wins. failed outranks refused in this file's own severity ranking
// (emergencyStopOutcomeSeverity), NOT in raw exit-code number order (12
// vs 13). That is the exact trap that function's own doc comment warns
// against, so a night-session failure (ranked as "failed") correctly
// overrides a milder "refused" stop outcome here.
func TestExitCodeForEmergencyStopResultTakesTheWorseOfStopOutcomeAndNightSessionFailure(t *testing.T) {
	ns := emergencyStopNightSessionOutcome{Present: true, Error: "injected"}
	result := emergencyStopResult{
		StopOutcomes: []emergencyStopInstanceOutcome{{InstanceID: "player-01", Outcome: "refused"}},
		NightSession: &ns,
	}
	if got := exitCodeForEmergencyStopResult(result); got != exitActionFailed {
		t.Fatalf("exitCodeForEmergencyStopResult = %d, want exitActionFailed (%d): night-session failure ranks as \"failed\", which outranks a \"refused\" stop outcome", got, exitActionFailed)
	}
}

// A night-session failure must never SILENTLY DISAPPEAR behind an
// already-failed stop outcome either: both map to the same top severity,
// so the result stays exitActionFailed either way.
func TestExitCodeForEmergencyStopResultBothFailedStaysFailed(t *testing.T) {
	ns := emergencyStopNightSessionOutcome{Present: true, Error: "injected"}
	result := emergencyStopResult{
		StopOutcomes: []emergencyStopInstanceOutcome{{InstanceID: "player-01", Outcome: "failed"}},
		NightSession: &ns,
	}
	if got := exitCodeForEmergencyStopResult(result); got != exitActionFailed {
		t.Fatalf("exitCodeForEmergencyStopResult = %d, want exitActionFailed (%d)", got, exitActionFailed)
	}
}

// A follow-up action's own outcome must NEVER change the exit code, even
// when every stop outcome is confirmed and every follow-up failed. This is the
// core degrade-safely property this whole file exists to protect.
func TestExitCodeForEmergencyStopResultIgnoresFollowUps(t *testing.T) {
	result := emergencyStopResult{
		StopOutcomes: []emergencyStopInstanceOutcome{{InstanceID: "player-01", Outcome: "confirmed"}},
		FollowUps: []emergencyStopFollowUpResult{
			{ActionID: "worklights-on", Outcome: "failed", OutcomeReason: "relay unreachable"},
			{ActionID: "projectors-off", Outcome: "refused", OutcomeReason: "not configured"},
		},
	}
	if got := exitCodeForEmergencyStopResult(result); got != exitOK {
		t.Fatalf("exitCodeForEmergencyStopResult = %d, want %d (exitOK): a failed follow-up must never change the exit code", got, exitOK)
	}
}

func TestReportEmergencyStopResultPrintsFollowUpsAsBestEffortAndNeverAffectsExitCode(t *testing.T) {
	var stdout bytes.Buffer
	result := emergencyStopResult{
		Level:        "stop",
		StopOutcomes: []emergencyStopInstanceOutcome{{InstanceID: "player-01", Outcome: "confirmed", OutcomeReason: "idle"}},
		FollowUps: []emergencyStopFollowUpResult{
			{ActionID: "worklights-on", Label: "Worklights on", Outcome: "failed", OutcomeReason: "relay unreachable"},
		},
	}
	got := reportEmergencyStopResult(&stdout, "showmeshctl emergency-stop stop", result)
	if got != exitOK {
		t.Fatalf("exit code = %d, want %d", got, exitOK)
	}
	out := stdout.String()
	if !strings.Contains(out, "player-01") {
		t.Errorf("stdout does not mention the stopped instance: %q", out)
	}
	if !strings.Contains(out, "Worklights on") || !strings.Contains(out, "best-effort") {
		t.Errorf("stdout does not clearly label the failed follow-up as best-effort: %q", out)
	}
}

func TestReportEmergencyStopResultNightSessionPresence(t *testing.T) {
	var stdout bytes.Buffer
	present := emergencyStopNightSessionOutcome{Present: true, SessionID: "sess-1", Outcome: "applied"}
	result := emergencyStopResult{Level: "stop-power-down", NightSession: &present}
	reportEmergencyStopResult(&stdout, "showmeshctl emergency-stop stop-power-down", result)
	if !strings.Contains(stdout.String(), "sess-1") {
		t.Errorf("stdout does not mention the forced night session: %q", stdout.String())
	}

	stdout.Reset()
	absent := emergencyStopNightSessionOutcome{Present: false}
	result2 := emergencyStopResult{Level: "stop-power-down", NightSession: &absent}
	reportEmergencyStopResult(&stdout, "showmeshctl emergency-stop stop-power-down", result2)
	if !strings.Contains(stdout.String(), "none active") {
		t.Errorf("stdout does not report the no-active-session case honestly: %q", stdout.String())
	}
}

// --- end-to-end, against a fake coordinator ---

func emergencyStopFakeServer(t *testing.T, armToken string, fireCalls *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/emergency-stop/stop", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, emergencyStopFakeResponse("stop"))
	})
	mux.HandleFunc("/api/v1/emergency-stop/stop-power-down", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, emergencyStopFakeResponse("stop-power-down"))
	})
	mux.HandleFunc("/api/v1/emergency-stop/hard-stop/arm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"serverTime":"2026-08-14T22:00:00Z","armToken":%q,"expiresAt":"2026-08-14T22:00:10Z"}`, armToken)
	})
	mux.HandleFunc("/api/v1/emergency-stop/hard-stop/fire", func(w http.ResponseWriter, r *http.Request) {
		*fireCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, emergencyStopFakeResponse("hard-stop"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func emergencyStopFakeResponse(level string) string {
	return `{"serverTime":"2026-08-14T22:00:00Z","result":{"level":"` + level + `","idempotencyKey":"k",` +
		`"stopOutcomes":[{"instanceId":"player-01","outcome":"confirmed","outcomeReason":"idle","dispatchedAt":"2026-08-14T22:00:00Z","replay":false}],` +
		`"nightSession":null,"followUps":[]}}`
}

func TestCmdEmergencyStopStopEndToEnd(t *testing.T) {
	var fireCalls int
	srv := emergencyStopFakeServer(t, "tok-1", &fireCalls)
	var stdout, stderr bytes.Buffer
	code := cmdEmergencyStop([]string{"stop", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Now() })
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "player-01") {
		t.Errorf("stdout = %q, want it to mention the stopped instance", stdout.String())
	}
}

func TestCmdEmergencyStopHardStopArmThenFireEndToEnd(t *testing.T) {
	var fireCalls int
	srv := emergencyStopFakeServer(t, "tok-1", &fireCalls)
	var armOut, armErr bytes.Buffer
	armCode := cmdEmergencyStop([]string{"hard-stop", "arm", "--server", srv.URL}, &armOut, &armErr, func() time.Time { return time.Now() })
	if armCode != exitOK {
		t.Fatalf("arm exit code = %d, want %d; stderr: %s", armCode, exitOK, armErr.String())
	}
	if !strings.Contains(armOut.String(), "tok-1") {
		t.Fatalf("arm stdout = %q, want it to print the token", armOut.String())
	}

	var fireOut, fireErr bytes.Buffer
	fireCode := cmdEmergencyStop([]string{"hard-stop", "fire", "--arm-token", "tok-1", "--server", srv.URL}, &fireOut, &fireErr, func() time.Time { return time.Now() })
	if fireCode != exitOK {
		t.Fatalf("fire exit code = %d, want %d; stderr: %s", fireCode, exitOK, fireErr.String())
	}
	if fireCalls != 1 {
		t.Fatalf("fire endpoint called %d times, want 1", fireCalls)
	}
}

// "hard-stop" with no further argument is a usage error, not a stop: there
// is deliberately no bare/combined form. See cmd_emergency_stop.go's own
// doc comment for why.
func TestCmdEmergencyStopHardStopWithNoSubcommandIsUsageError(t *testing.T) {
	var fireCalls int
	srv := emergencyStopFakeServer(t, "tok-1", &fireCalls)
	var stdout, stderr bytes.Buffer
	code := cmdEmergencyStop([]string{"hard-stop", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Now() })
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (exitUsage)", code, exitUsage)
	}
	if fireCalls != 0 {
		t.Fatalf("fire endpoint called %d times, want 0", fireCalls)
	}
}

func TestCmdEmergencyStopHardStopFireRequiresToken(t *testing.T) {
	var fireCalls int
	srv := emergencyStopFakeServer(t, "tok-1", &fireCalls)
	var stdout, stderr bytes.Buffer
	code := cmdEmergencyStop([]string{"hard-stop", "fire", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Now() })
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (exitUsage)", code, exitUsage)
	}
	if fireCalls != 0 {
		t.Fatalf("fire endpoint called %d times, want 0: a missing --arm-token must never reach the server", fireCalls)
	}
}

// The two 409 refusals on fire must map to TWO DIFFERENT exit codes: the
// whole reason problemEmergencyStopHardStopNotArmed was minted separate
// from problemConflict is "arm again, then fire promptly" versus "check
// whether the hard stop already happened before retrying blindly", and a
// script cannot act on that distinction if both collapse to one code.
func TestCmdEmergencyStopHardStopFireNotArmedAndAlreadyConsumedMapToDifferentExitCodes(t *testing.T) {
	notArmedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/emergency-stop-hard-stop-not-armed","title":"Hard stop is not armed","status":409,"detail":"no valid, unexpired armed hard-stop token for this principal","serverTime":"2026-08-14T22:00:00Z"}`)
	}))
	defer notArmedSrv.Close()

	var stdout1, stderr1 bytes.Buffer
	code1 := cmdEmergencyStop([]string{"hard-stop", "fire", "--arm-token", "tok-1", "--server", notArmedSrv.URL}, &stdout1, &stderr1, func() time.Time { return time.Now() })
	if code1 != exitActionRefused {
		t.Fatalf("not-armed: exit code = %d, want exitActionRefused (%d); stderr=%s", code1, exitActionRefused, stderr1.String())
	}

	alreadyConsumedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/conflict","title":"Hard stop already fired","status":409,"detail":"this arm token was already consumed by an earlier fire request","serverTime":"2026-08-14T22:00:00Z"}`)
	}))
	defer alreadyConsumedSrv.Close()

	var stdout2, stderr2 bytes.Buffer
	code2 := cmdEmergencyStop([]string{"hard-stop", "fire", "--arm-token", "tok-1", "--server", alreadyConsumedSrv.URL}, &stdout2, &stderr2, func() time.Time { return time.Now() })
	if code2 != exitConflict {
		t.Fatalf("already-consumed: exit code = %d, want exitConflict (%d); stderr=%s", code2, exitConflict, stderr2.String())
	}

	if code1 == code2 {
		t.Fatalf("not-armed and already-consumed both mapped to exit code %d; a script cannot tell them apart", code1)
	}
}

//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// This file is Step 7 seam C review's own required addition: BUILD-PLAN's
// acceptance criterion for POST /api/v1/fpp/{instanceId}/commands says
// "verified against the running stack rather than in a handler test", and
// before this file nothing under test/integration ever referenced
// /commands, stopPlaylist, or fpp:command — the criterion rested entirely
// on a by-hand run. This proves 401 unauthenticated, 403 naming the
// missing scope, 200 for an operator, and the replay behaviour, against a
// real showmesh-coordinator subprocess and the real bench fppd (never the
// deployed fleet — see requireLiveFPP, shared with fpp_e2e_test.go, and
// this task's own standing rule against ever pointing a write at
// docs/reference-installation.md's hosts).
//
// scripts/test-integration-fpp.sh's own -run filter is updated alongside
// this file (see that script) — LESSONS.md's own sharpest lesson, found
// during this same review pass, is that `go test -run <pattern>` exits 0
// when the pattern matches nothing, the identical silent-zero shape as the
// skip this project already shipped once. That script now also greps its
// own -v output for a nonzero "=== RUN" count so a typo'd pattern fails
// loudly instead of quietly running nothing.

// createPrincipalAndIssueToken is [createAdminAndIssueToken]'s general
// form: a principal of an arbitrary role (never just admin), provisioned
// via the coordinator binary's own create-principal/issue-token
// subcommands (ADR-024 decision 9's host-level path), before any
// coordinator subprocess starts against dataDir. Used here for a viewer
// principal (403 naming the missing scope) alongside admin (which already
// holds fpp:command, for the 200 case) — createAdminAndIssueToken alone
// cannot produce anything but an admin.
func createPrincipalAndIssueToken(t *testing.T, dataDir, name, role string) (token string) {
	t.Helper()
	runCoordinatorSubcommand(t, dataDir, []string{"create-principal", "-name=" + name, "-role=" + role, "-kind=machine"}, "")
	out := runCoordinatorSubcommand(t, dataDir, []string{"issue-token", "-principal=" + name, "-label=integration-test"}, "")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("issue-token produced no output")
	}
	token = lines[len(lines)-1]
	if !strings.HasPrefix(token, "smsh_") {
		t.Fatalf("last line of issue-token output = %q, want it to look like a token (prefix \"smsh_\"); full output:\n%s", token, out)
	}
	return token
}

// postRawWithToken is [postRaw] with an explicit bearer token, overriding
// coord.token (which postRaw always attaches) — needed here because a
// single coordinator subprocess in this file is shared across
// unauthenticated, viewer, and operator requests, unlike every other
// caller of postRaw, which mints one coordinator per token.
//
// Deliberately does NOT call postRaw or use coord.client: this file's
// FIRST attempt did exactly that and reproduced defect 1's own failure
// shape against its own test harness — coord.client is built with
// Timeout: 5*time.Second (startCoordinatorWithConfig, sized for a
// snapshot-style GET), and a real confirmation wait against a real
// coordinator routinely exceeds that, which is the entire premise this
// endpoint's own review defect 1 exists to name. commandRequestTimeout
// below is this file's own equivalent of
// cmd/showmeshctl's minStopPlaylistClientTimeout and
// ui/src/api/client.ts's FPP_COMMAND_REQUEST_TIMEOUT_MS — a THIRD
// independently-sized client budget for the identical long-request shape,
// reconciled against the real server here by actually running it (this
// test itself), not by importing a shared constant.
func postRawWithToken(t *testing.T, coord *testCoordinator, path, token string, body any) (status int, respBody []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request body for %s: %v", path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(http.MethodPost, coord.url(path), reader)
	if err != nil {
		t.Fatalf("build POST request for %s: %v", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := commandRequestClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("POST %s: read body: %v", path, err)
	}
	return resp.StatusCode, b
}

// commandRequestTimeout mirrors cmd/showmeshctl's minStopPlaylistClientTimeout
// and ui/src/api/client.ts's FPP_COMMAND_REQUEST_TIMEOUT_MS: the
// coordinator's own default confirmation deadline (20s) plus a margin for
// the round trip itself. A fourth independently-sized literal, on the
// same deliberate footing as the other two client-side ones (see this
// file's own doc comment).
const commandRequestTimeout = 35 * time.Second

var commandRequestClient = &http.Client{Timeout: commandRequestTimeout}

// TestFPPCommandAgainstRealCoordinatorAndBenchFPP is this endpoint's own
// acceptance criterion, all four shapes in one test against one shared
// coordinator (avoiding four separate bench-fppd-backed coordinator
// startups): 401 unauthenticated, 403 naming fpp:command for a viewer, 200
// for an operator (here: admin, which already holds every scope) with a
// real dispatch against the bench fppd and a resolved outcome, and a
// replayed idempotencyKey dispatching nothing and returning the original
// result.
func TestFPPCommandAgainstRealCoordinatorAndBenchFPP(t *testing.T) {
	requireBroker(t)
	url := requireLiveFPP(t)

	dataDir := t.TempDir()
	adminToken := createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")
	viewerToken := createPrincipalAndIssueToken(t, dataDir, "viewer-1", "viewer")

	instanceID := "bench-fpp-cmd-" + uniqueSuffix()
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: "coord-" + uniqueSuffix(),
		bearerToken: adminToken, fppEndpoints: instanceID + "=" + url,
	})

	// Wait for the collector's first poll to land before dispatching
	// anything — otherwise the very first confirmation check would
	// correctly, but uninterestingly, report not_collected.
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		status, body := coord.getRaw(t, "/api/v1/fpp/"+instanceID)
		return status == http.StatusOK && strings.Contains(string(body), `"signal":"fpp.status"`) && !strings.Contains(string(body), `"fpp.status","value":null`)
	}, "the FPP collector's first fpp.status poll to land through the coordinator")

	t.Run("401 unauthenticated", func(t *testing.T) {
		status, body := postRawWithToken(t, coord, "/api/v1/fpp/"+instanceID+"/commands", "", map[string]any{
			"action": "stopPlaylist", "idempotencyKey": "key-401-" + uniqueSuffix(),
		})
		if status != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", status, body)
		}
	})

	t.Run("403 forbidden names the missing scope for a viewer", func(t *testing.T) {
		status, body := postRawWithToken(t, coord, "/api/v1/fpp/"+instanceID+"/commands", viewerToken, map[string]any{
			"action": "stopPlaylist", "idempotencyKey": "key-403-" + uniqueSuffix(),
		})
		if status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", status, body)
		}
		if !strings.Contains(string(body), "fpp:command") {
			t.Errorf("body = %s, want it to name the missing scope fpp:command", body)
		}
	})

	var firstID string
	replayKey := "key-200-" + uniqueSuffix()

	t.Run("200 for an operator, dispatched against the real bench fppd and resolved", func(t *testing.T) {
		status, body := postRawWithToken(t, coord, "/api/v1/fpp/"+instanceID+"/commands", adminToken, map[string]any{
			"action": "stopPlaylist", "idempotencyKey": replayKey,
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", status, body)
		}
		var resp struct {
			Command struct {
				ID            string  `json:"id"`
				Replay        bool    `json:"replay"`
				Outcome       string  `json:"outcome"`
				OutcomeState  string  `json:"outcomeState"`
				OutcomeReason string  `json:"outcomeReason"`
				DispatchedAt  *string `json:"dispatchedAt"`
				ResolvedAt    *string `json:"resolvedAt"`
			} `json:"command"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode: %v; body: %s", err, body)
		}
		firstID = resp.Command.ID
		if resp.Command.Replay {
			t.Errorf("replay = true on a FRESH idempotency key, want false")
		}
		// ADR-003, verified against the real thing: a 200 does not by
		// itself mean success — this coordinator must have actually
		// resolved the command one way or the other, never left it
		// hanging in this response.
		if resp.Command.Outcome != "confirmed" && resp.Command.Outcome != "unconfirmed" {
			t.Fatalf("outcome = %q, want \"confirmed\" or \"unconfirmed\" — never blank against a real coordinator", resp.Command.Outcome)
		}
		if resp.Command.OutcomeState == "" {
			t.Errorf("outcomeState is empty, want a stated evidence state (ADR-020)")
		}
		if resp.Command.DispatchedAt == nil {
			t.Errorf("dispatchedAt is nil, want it set — dispatch to the real bench fppd was attempted")
		}
		if resp.Command.ResolvedAt == nil {
			t.Errorf("resolvedAt is nil, want it set")
		}
		t.Logf("real dispatch against bench fppd: outcome=%s outcomeState=%s outcomeReason=%q", resp.Command.Outcome, resp.Command.OutcomeState, resp.Command.OutcomeReason)
	})

	t.Run("a replayed idempotencyKey dispatches nothing and returns the original result", func(t *testing.T) {
		if firstID == "" {
			t.Skip("the 200 subtest above did not record a command id")
		}
		status, body := postRawWithToken(t, coord, "/api/v1/fpp/"+instanceID+"/commands", adminToken, map[string]any{
			"action": "stopPlaylist", "idempotencyKey": replayKey,
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", status, body)
		}
		var resp struct {
			Command struct {
				ID     string `json:"id"`
				Replay bool   `json:"replay"`
			} `json:"command"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode: %v; body: %s", err, body)
		}
		if !resp.Command.Replay {
			t.Errorf("replay = false on a REUSED idempotency key, want true")
		}
		if resp.Command.ID != firstID {
			t.Errorf("replay id = %q, want the original command's id %q", resp.Command.ID, firstID)
		}
	})
}

// TestCLIStopPlaylistTimeoutSurvivesServerConfirmDeadline is Step 7 seam C
// review defect 1's own reconciliation test, named exactly as
// cmd/showmeshctl/cmd_fpp_stop_playlist.go's minStopPlaylistClientTimeout
// and pkg/command's DefaultFPPCommandConfirmDeadline both cite it by name:
// the real showmeshctl binary, with NO --timeout override (so it runs on
// its bare global default, 10s — shorter than the coordinator's own
// default confirmation deadline, 20s), dispatched against a real
// coordinator and the real bench fppd. Before Step 7 seam C review defect
// 1's fix, this reliably aborted as a bare transport timeout whenever the
// coordinator's own confirmation wait outlasted 10s — which it does
// routinely, because [evaluateFPPStatusEvidence] (defect 2's fix) refuses
// to confirm on evidence collected before dispatch, so confirmation
// always waits for the collector's own NEXT poll (up to
// fpp.DefaultPollInterval, 15s). This is the one test in this suite that
// proves the CLI's own literal timeout constant and the coordinator's own
// literal deadline constant have not silently drifted apart again — the
// two are deliberately independent numbers (see both constants' own doc
// comments for why neither can import the other), so no unit test
// comparing them as numbers could catch that; only running both real
// binaries together can.
func TestCLIStopPlaylistTimeoutSurvivesServerConfirmDeadline(t *testing.T) {
	requireBroker(t)
	url := requireLiveFPP(t)

	dataDir := t.TempDir()
	adminToken := createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")

	instanceID := "bench-fpp-cli-" + uniqueSuffix()
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: "coord-" + uniqueSuffix(),
		bearerToken: adminToken, fppEndpoints: instanceID + "=" + url,
	})

	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		status, body := coord.getRaw(t, "/api/v1/fpp/"+instanceID)
		return status == http.StatusOK && strings.Contains(string(body), `"signal":"fpp.status"`) && !strings.Contains(string(body), `"fpp.status","value":null`)
	}, "the FPP collector's first fpp.status poll to land through the coordinator")

	// Deliberately NO --timeout flag: exercises the CLI's bare global
	// default (10s). The outer exec context timeout (60s) is this TEST's
	// own patience, not the CLI's — it must be comfortably larger than
	// both the CLI's real effective timeout (35s, minStopPlaylistClientTimeout)
	// and the coordinator's own confirm deadline (20s) or this test would
	// kill the process itself and produce a false failure.
	code, stdout, stderr := runShowmeshctl(t, 60*time.Second,
		"fpp", "stop-playlist", "--server", "http://"+coord.httpAddr, "--token", adminToken, instanceID)

	if strings.Contains(stderr, "context deadline exceeded") || strings.Contains(stderr, "timed out contacting") {
		t.Fatalf("showmeshctl reported a TRANSPORT timeout — exactly defect 1's failure mode — despite using its "+
			"bare 10s global default against a coordinator whose own confirmation deadline (20s) legitimately "+
			"exceeds it; exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "confirmed") && !strings.Contains(stdout, "unconfirmed") && !strings.Contains(stdout, "pending") {
		t.Fatalf("stdout = %q, want it to report the coordinator's own resolved (or replay-pending) outcome, "+
			"never a bare process failure; exit=%d stderr=%q", stdout, code, stderr)
	}
	t.Logf("showmeshctl fpp stop-playlist against a real coordinator/bench fppd, default --timeout: exit=%d stdout=%q", code, stdout)
}

// TestFPPCommandReplayOnParameterizedCommandDispatchesNothingAuditsAsReplayAndRefusesParamConflict
// is Step 8's own extension of this file's replay coverage. Everything
// above in this file exercises stopPlaylist, which takes NO parameters —
// so nothing before this function ever proved replay behavior against a
// PARAMETERIZED command, that the replay is recorded in the audit trail
// AS a replay (identity.AuditReplay, surfaced on GET /api/v1/audit's own
// "kind" field), or that a replayed key presented with DIFFERENT
// normalized params is refused as a 409 conflict rather than silently
// answered as if it were the original request
// (fppcommand_primitives.go's own canonicalParamsJSON-keyed conflict
// check, added by this step; see fppCommandReplayParamsConflictProblem).
//
// setVolume is used throughout because its effect is externally
// observable AND independently settable: this test moves the bench
// fppd's volume DIRECTLY (bypassing ShowMesh entirely, via
// dispatchRawFPPCommand in fpp_command_primitives_test.go) between the
// original dispatch and each replay attempt, so "dispatched nothing" is
// proven STRUCTURALLY rather than inferred from a 200/409 status code
// alone: if the replay path had actually re-dispatched "Volume Set 77" to
// FPP, the independently-set value would have been overwritten back to
// 77; if the params-conflict path had actually dispatched "Volume Set
// 99", the bench's volume would show 99. Neither may happen.
func TestFPPCommandReplayOnParameterizedCommandDispatchesNothingAuditsAsReplayAndRefusesParamConflict(t *testing.T) {
	requireBroker(t)
	fppURL := requireLiveFPP(t)
	resetBenchToIdle(t, fppURL)
	t.Cleanup(func() { resetBenchToIdle(t, fppURL) })

	coord, adminToken, instanceID := newFPPCoordinatorForTest(t, fppURL, "bench-fpp-replay")

	replayKey := "key-replay-vol-" + uniqueSuffix()
	first := dispatchFPPCommand(t, coord, adminToken, instanceID, "setVolume", map[string]any{"volume": 77}, replayKey)
	requireConfirmed(t, "setVolume (original)", first)
	if v, ok := benchVolume(fetchBenchFPPStatus(t, fppURL)); !ok || v != 77 {
		t.Fatalf("original setVolume(77) confirmed but bench fppd volume = %v (ok=%v), want 77", v, ok)
	}

	// Move the bench's volume DIRECTLY, bypassing ShowMesh — independent
	// ground truth a replay (or a refused conflict) must not disturb.
	dispatchRawFPPCommand(t, fppURL, "Volume Set", []string{"10"})
	waitForBenchStatus(t, fppURL, 5*time.Second, func(doc map[string]any) bool {
		v, ok := benchVolume(doc)
		return ok && v == 10
	}, "bench fppd volume to read 10 after a direct, ShowMesh-bypassing Volume Set")

	// --- Replay: SAME params. Must dispatch nothing. ---
	status, body := postRawWithToken(t, coord, "/api/v1/fpp/"+instanceID+"/commands", adminToken, map[string]any{
		"action": "setVolume", "idempotencyKey": replayKey, "params": map[string]any{"volume": 77},
	})
	if status != http.StatusOK {
		t.Fatalf("replay with identical params: status = %d, want 200; body: %s", status, body)
	}
	var replayResp v1.FPPCommandResponse
	if err := json.Unmarshal(body, &replayResp); err != nil {
		t.Fatalf("decode replay response: %v; body: %s", err, body)
	}
	if !replayResp.Command.Replay {
		t.Errorf("replay = false on a reused idempotency key with identical params, want true")
	}
	if replayResp.Command.ID != first.ID {
		t.Errorf("replay id = %q, want the original command's id %q", replayResp.Command.ID, first.ID)
	}
	if replayResp.Command.Outcome != first.Outcome {
		t.Errorf("replay outcome = %q, want the ORIGINAL result %q", replayResp.Command.Outcome, first.Outcome)
	}
	if v, ok := benchVolume(fetchBenchFPPStatus(t, fppURL)); !ok || v != 10 {
		t.Fatalf("after a REPLAY of setVolume(77), bench fppd volume = %v (ok=%v), want it UNCHANGED at 10 — the "+
			"replay must have dispatched NOTHING to FPP", v, ok)
	}

	// --- Audit trail: the replay must be recorded AS a replay, correlated
	// to the original command id (ADR-024 decision 11: "a replay is
	// precisely the case an investigator wants to see"). ---
	auditStatus, auditBody := coord.getRaw(t, "/api/v1/audit?limit=200")
	if auditStatus != http.StatusOK {
		t.Fatalf("GET /api/v1/audit: status = %d, want 200; body: %s", auditStatus, auditBody)
	}
	var auditResp v1.AuditResponse
	if err := json.Unmarshal(auditBody, &auditResp); err != nil {
		t.Fatalf("decode audit response: %v; body: %s", err, auditBody)
	}
	var sawReplayEntry bool
	for _, e := range auditResp.Entries {
		if e.Kind == "replay" && e.CommandID == first.ID && e.IdempotencyKey == replayKey {
			sawReplayEntry = true
			if e.Action != "fpp.set_volume" {
				t.Errorf("replay audit entry action = %q, want \"fpp.set_volume\"", e.Action)
			}
		}
	}
	if !sawReplayEntry {
		t.Errorf("no audit_log entry with kind=\"replay\", commandId=%q, idempotencyKey=%q was found among %d entries",
			first.ID, replayKey, len(auditResp.Entries))
	}

	// --- Replay with DIFFERENT params: refused as a 409, dispatches
	// nothing. ---
	conflictStatus, conflictBody := postRawWithToken(t, coord, "/api/v1/fpp/"+instanceID+"/commands", adminToken, map[string]any{
		"action": "setVolume", "idempotencyKey": replayKey, "params": map[string]any{"volume": 99},
	})
	if conflictStatus != http.StatusConflict {
		t.Fatalf("replay with DIFFERENT params: status = %d, want 409; body: %s", conflictStatus, conflictBody)
	}
	if !strings.Contains(string(conflictBody), "DIFFERENT") {
		t.Errorf("409 body does not explain the params conflict; body: %s", conflictBody)
	}
	if v, ok := benchVolume(fetchBenchFPPStatus(t, fppURL)); !ok || v != 10 {
		t.Fatalf("after a REFUSED replay with different params, bench fppd volume = %v (ok=%v), want it STILL "+
			"UNCHANGED at 10 — the 409 must have dispatched nothing", v, ok)
	}
}

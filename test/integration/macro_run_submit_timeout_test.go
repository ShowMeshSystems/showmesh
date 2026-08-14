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
)

// This file is cmd/showmeshctl's own timeout-floor reconciliation test for
// the macro/action/run surface (Step 9 wave 3), the same shape as
// fpp_command_test.go's TestCLIStopPlaylistTimeoutSurvivesServerConfirmDeadline
// one file over, adapted to a materially different contract.
//
// What THIS test proves, stated plainly because it is easy to misread by
// analogy with its sibling: cmd/showmeshctl's minMacroClientTimeout (5s,
// cmd/showmeshctl/macro_client.go) is NOT sized to survive a long
// server-side hold the way minFPPCommandClientTimeout (35s) is — a macro
// run is accepted asynchronously (ADR-031 decision 1: POST
// /macros/{id}/runs answers 202 before any step ever dispatches, from a
// background goroutine the HTTP response does not wait on). So there is no
// server-side wait for a client floor to "survive" here. What this test
// demonstrates instead is the other half of "reconciled, not assumed": that
// a real POST /macros/{id}/runs against a real coordinator (with a real
// bench fppd behind the one show.action step it submits) actually IS fast
// enough that minMacroClientTimeout's 5s floor is comfortable headroom, not
// a number chosen blind. If a future change ever made macro submission
// itself block on step dispatch — resurrecting exactly the Step 7 defect
// ADR-031 decision 1 exists to prevent — this test would start failing on
// its own elapsed-time assertion, not just eventually time out.
//
// Driven through the REAL showmeshctl binary as a subprocess (runShowmeshctl,
// cli_test.go), not an in-process call — the same reason
// TestCLIStopPlaylistTimeoutSurvivesServerConfirmDeadline does, and BUILD-PLAN
// Step 3's acceptance criterion (a): "the API is exercised end to end by a
// non-UI client."

// putRawWithToken is postRawWithToken's (fpp_command_test.go) PUT sibling:
// no such helper existed for PUT before this file, because nothing under
// test/integration issued a PUT until this test needed to author a
// show.action/show.macro fixture. Deliberately its own http.Client with a
// generous, fixed timeout (mirrors commandRequestClient one file over)
// rather than coord.client (5s, sized for a plain snapshot GET per
// startCoordinatorWithConfig's own comment) — a config write is local work
// only, but there is no reason to risk the same "budget sized for a
// different endpoint" mistake that endpoint's own doc comment warns about.
var putRequestClient = &http.Client{Timeout: 15 * time.Second}

func putRawWithToken(t *testing.T, coord *testCoordinator, path, token string, body any) (status int, respBody []byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode PUT body for %s: %v", path, err)
	}
	req, err := http.NewRequest(http.MethodPut, coord.url(path), bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("build PUT request for %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := putRequestClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("PUT %s: read body: %v", path, err)
	}
	return resp.StatusCode, b
}

// TestCLIMacroRunSubmitTimeoutFloorCoversRealSubmissionLatency is this
// file's own name for the reconciliation test cmd/showmeshctl/macro_client.go's
// minMacroClientTimeout doc comment cites by name. See this file's own doc
// comment above for exactly what it proves and why that is NOT the same
// claim its fpp-command sibling proves.
func TestCLIMacroRunSubmitTimeoutFloorCoversRealSubmissionLatency(t *testing.T) {
	requireBroker(t)
	fppURL := requireLiveFPP(t)

	dataDir := t.TempDir()
	adminToken := createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")

	instanceID := "bench-fpp-macro-timeout-" + uniqueSuffix()
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: "coord-" + uniqueSuffix(),
		bearerToken: adminToken, fppEndpoints: instanceID + "=" + fppURL,
	})

	// One show.action (an FPP "stopPlaylist" step — safetyClass "stop" per
	// its own registered Decision 11 class) and one one-step show.macro
	// naming it — the minimum fixture that exercises SubmitRun's real
	// synchronous cost (resolving the macro's pinned revision, resolving
	// its one step's action, and the CreateMacroRun+audit transaction).
	actionID := "stop-main-" + uniqueSuffix()
	actionStatus, actionBody := putRawWithToken(t, coord, "/api/v1/config/show.action/"+actionID, adminToken, map[string]any{
		"show": "bench-show", "label": "Stop main show", "description": "", "safetyClass": "stop",
		"target": map[string]any{"integration": "fpp", "instanceId": instanceID, "primitive": "stopPlaylist"},
	})
	if actionStatus != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d, want 200; body: %s", actionStatus, actionBody)
	}

	macroID := "stop-macro-" + uniqueSuffix()
	macroStatus, macroBody := putRawWithToken(t, coord, "/api/v1/config/show.macro/"+macroID, adminToken, map[string]any{
		"show": "bench-show", "label": "Stop macro", "description": "",
		"steps": []map[string]any{
			{
				"id": "stop", "action": actionID,
				"localFallback": map[string]any{"class": "coordinator-required", "reason": "test fixture: no local delivery path exists yet"},
			},
		},
	})
	if macroStatus != http.StatusOK {
		t.Fatalf("PUT show.macro: status = %d, want 200; body: %s", macroStatus, macroBody)
	}

	// Deliberately NO --timeout override: exercises showmeshctl's own bare
	// internal floor for this surface (minMacroClientTimeout, 5s — smaller
	// than the global --timeout default of 10s, so this also proves the
	// floor never needs to RAISE anything for an ordinary submission; see
	// noteMacroTimeoutFloorIfRaised, which would print a note to stderr if
	// it ever fired here). The outer 30s below is this TEST's own patience
	// for the subprocess, not showmeshctl's request budget.
	start := time.Now()
	code, stdout, stderr := runShowmeshctl(t, 30*time.Second,
		"macro", "run", "--server", "http://"+coord.httpAddr, "--token", adminToken, macroID)
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("showmeshctl macro run: exit = %d, want 0 (accepted); stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "context deadline exceeded") || strings.Contains(stderr, "timed out contacting") {
		t.Fatalf("showmeshctl reported a TRANSPORT timeout submitting a macro run — submission is accepted "+
			"asynchronously (202) and must never legitimately take anywhere near the 5s floor; exit=%d stdout=%q stderr=%q",
			code, stdout, stderr)
	}
	if !strings.Contains(stdout, "accepted") {
		t.Fatalf("stdout = %q, want it to report the run as accepted", stdout)
	}

	// The actual evidence: real submission latency against a real
	// coordinator and a real bench fppd, measured, not assumed. Comfortably
	// under minMacroClientTimeout's 5s floor is the claim; this asserts a
	// full order of magnitude of headroom (under 1s) so ordinary CI/bench
	// jitter cannot make this flaky while still catching a genuine
	// regression toward "submission blocks on dispatch."
	if elapsed > 1*time.Second {
		t.Errorf("macro run submission took %s end-to-end (including subprocess startup); want well under 1s, "+
			"which is what makes the 5s client floor headroom rather than a number chosen blind. A submission "+
			"this slow is worth investigating before trusting the floor further", elapsed)
	}
	t.Logf("showmeshctl macro run (no --timeout override) against a real coordinator: exit=%d elapsed=%s stdout=%q", code, elapsed, stdout)
}

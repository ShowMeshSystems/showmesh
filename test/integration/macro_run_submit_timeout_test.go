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
// server-side wait for a client floor to "survive" here.
//
// The PRIMARY proof below is STRUCTURAL, not a wall-clock race. Review
// finding 6 (Step 9 wave 3) showed the original elapsed-time-only version
// was the weaker of two available proofs: it passed no --timeout, so the
// operative budget was the 10s global default and minMacroClientTimeout
// was never actually exercised; the regression it names (submission
// blocking on step dispatch) costs roughly 15-20s by section 6.3's own
// arithmetic, which already blows past a 10s budget and fails earlier than
// the elapsed-time assertion ever gets to run; and a wall-clock bound on a
// machine that may be concurrently hosting containers is exactly the kind
// of test this project has already caught flaking for reasons unrelated to
// correctness (see LESSONS.md, "a test can be a coin flip, and platform is
// the usual disguise").
//
// internal/coordinator/macro/submit.go's SubmitRun captures the run and
// its step records BEFORE launching background execution ("Start
// background execution and answer 202-shaped: the run's initial state,
// never a completed result"), so the 202 body is a deterministic
// pre-dispatch snapshot: state "running", every step "pending", no step's
// dispatchedAt set. This test decodes that snapshot via --output json and
// asserts exactly that shape. This is true on every machine, at any
// speed, and names the regression directly — a future change that made
// submission wait on the first step's dispatch would flip a step's own
// state away from "pending" (or set its dispatchedAt), which this
// assertion catches regardless of how fast or slow the box is.
//
// A LOOSE wall-clock ceiling is kept alongside the structural assertion,
// deliberately far inside the 10s default budget, purely as a sanity
// check and because acceptance criterion 1 wants the measured number as
// evidence — see this function's own final t.Logf. It is not the
// mechanism that proves the regression is absent; the structural
// assertion above is.
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

// macroRunSubmitResponseForTest decodes the fields this test actually
// needs from POST /macros/{id}/runs' 202 body (via showmeshctl's own
// --output json, which re-serializes its OWN decoded struct — see
// cmd/showmeshctl/types_macro.go's macroRunSubmitResponse for the
// authoritative shape). Declared locally rather than importing
// cmd/showmeshctl's types: this package is test/integration, not
// showmeshctl itself, and duplicating just the handful of fields this
// test asserts on keeps this test from silently tracking an unrelated
// package's decode surface.
type macroRunSubmitResponseForTest struct {
	Run struct {
		State string `json:"state"`
		Steps []struct {
			State        string  `json:"state"`
			DispatchedAt *string `json:"dispatchedAt"`
		} `json:"steps"`
	} `json:"run"`
}

// TestCLIMacroRunSubmitTimeoutFloorCoversRealSubmissionLatency is this
// file's own name for the reconciliation test cmd/showmeshctl/macro_client.go's
// minMacroClientTimeout doc comment cites by name. See this file's own doc
// comment above for exactly what it proves and why that is NOT the same
// claim its fpp-command sibling proves, and for why the structural
// assertion below — not the elapsed-time one — is what actually pins the
// regression this test exists to catch.
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
	// it ever fired here). --output json is what makes the structural
	// assertion below possible: the prose "accepted: run ..." line a
	// human reads carries none of the step-level detail this test needs.
	// The outer 30s below is this TEST's own patience for the subprocess,
	// not showmeshctl's request budget.
	start := time.Now()
	code, stdout, stderr := runShowmeshctl(t, 30*time.Second,
		"macro", "run", "--server", "http://"+coord.httpAddr, "--token", adminToken, "--output", "json", macroID)
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("showmeshctl macro run: exit = %d, want 0 (accepted); stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "context deadline exceeded") || strings.Contains(stderr, "timed out contacting") {
		t.Fatalf("showmeshctl reported a TRANSPORT timeout submitting a macro run — submission is accepted "+
			"asynchronously (202) and must never legitimately take anywhere near the 5s floor; exit=%d stdout=%q stderr=%q",
			code, stdout, stderr)
	}

	// The STRUCTURAL proof (this file's own doc comment explains why this,
	// not the elapsed-time check below, is what actually pins the
	// regression): SubmitRun captures the run and its steps BEFORE
	// launching background execution, so a 202 response reporting
	// anything OTHER than "running" with every step still "pending" and
	// no step's dispatchedAt set means submission waited on dispatch —
	// exactly the Step 7-shaped regression ADR-031 decision 1 exists to
	// prevent. True on any machine, at any speed.
	var resp macroRunSubmitResponseForTest
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decoding --output json stdout as a macro run submission: %v; stdout=%q", err, stdout)
	}
	if resp.Run.State != "running" {
		t.Errorf("run.state = %q, want %q — a 202 response must be the pre-dispatch snapshot, not a result submission waited for", resp.Run.State, "running")
	}
	if len(resp.Run.Steps) == 0 {
		t.Fatalf("run.steps is empty; want the one-step macro fixture's step to be present in the 202 body")
	}
	for i, st := range resp.Run.Steps {
		if st.State != "pending" {
			t.Errorf("step %d state = %q, want %q — submission must not have waited for this step to dispatch before answering 202", i, st.State, "pending")
		}
		if st.DispatchedAt != nil {
			t.Errorf("step %d dispatchedAt = %v, want nil/absent — a dispatched timestamp on the 202 body means submission waited on dispatch", i, *st.DispatchedAt)
		}
	}

	// The SANITY check: a loose wall-clock ceiling, comfortably inside the
	// 10s default budget, plus the measured number logged as acceptance
	// criterion 1's own evidence. This is NOT what proves the regression
	// is absent — the structural assertion above is — but a submission
	// that is this slow is still worth knowing about even though the
	// structural check alone would not catch it.
	const sanityCeiling = 5 * time.Second
	if elapsed > sanityCeiling {
		t.Errorf("macro run submission took %s end-to-end (including subprocess startup); want well under the %s sanity ceiling. "+
			"This is a secondary check — the structural assertion above already proved submission did not wait on dispatch — "+
			"but a submission this slow is still worth investigating", elapsed, sanityCeiling)
	}
	t.Logf("showmeshctl macro run --output json (no --timeout override) against a real coordinator: exit=%d elapsed=%s stdout=%q", code, elapsed, stdout)
}

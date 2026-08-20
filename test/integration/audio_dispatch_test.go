//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// This file proves that an audio session dispatch actually reaches the
// real agent CommandHandler through a real coordinator and a real broker
// — the boundary every unit test in internal/coordinator/api/
// audiodispatch_test.go crosses with a fake AwaitResponse that
// manufactures the agent's own reply, and the boundary
// audio_broker_loss_test.go crosses by building its own CmdPayload by
// hand and publishing it directly, bypassing the coordinator's HTTP
// dispatch entirely. Neither of those exercises the ONE thing that was
// actually broken: the coordinator's dispatch handler never set
// params["revision"], and internal/agent/audiosessionops.go's
// parseAudioSessionCommon refuses every one of the thirteen audio
// commands outright without it ("params.revision is required"). A fake
// that answers with a canned result never runs that parsing code, so it
// never noticed.
//
// The proof does not need a real media file or a pipeline backend: an
// audio.session.stop against a session id nothing has ever applied
// reaches internal/agent/audio.Manager.Stop, which refuses cleanly with
// Outcome "refused", Reason "session does not exist" — a well-formed,
// deterministic answer that is only reachable once revision parsing has
// already succeeded. Before the fix, the SAME request never reached
// Manager.Stop at all: parseAudioSessionCommon returned an error first,
// and the agent answered Outcome "failed", Reason "audio.session.stop:
// params.revision is required" instead.

// dispatchAudioSessionCommand POSTs to
// /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/{op} through the real
// coordinator (never directly at the agent) and requires a 200 — a
// non-200 here means the request itself was refused before ever reaching
// the node, which every assertion in this file needs to rule out first.
func dispatchAudioSessionCommand(t *testing.T, coord *testCoordinator, token, nodeID, sessionID, op string, revision uint64) v1.AudioSessionCommandResult {
	t.Helper()
	body := map[string]any{"revision": revision, "idempotencyKey": "cmd-" + uniqueSuffix()}
	path := "/api/v1/nodes/" + nodeID + "/audio/sessions/" + sessionID + "/" + op
	status, respBody := postRawWithToken(t, coord, path, token, body)
	if status != http.StatusOK {
		t.Fatalf("dispatch %s: status = %d, want 200; body: %s", op, status, respBody)
	}
	var resp v1.AudioSessionCommandResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("decode dispatch %s response: %v; body: %s", op, err, respBody)
	}
	return resp.Command
}

// TestAudioSessionDispatchReachesRealAgentCommandHandler is this task's
// own highest-value proof: a real HTTP request to a real coordinator,
// through a real Mosquitto broker, into a real showmesh-agent
// subprocess's real CommandHandler — asserting the agent ACCEPTED and
// EXECUTED the command (reaching its session layer's own refusal logic),
// never that the coordinator merely believes it did.
func TestAudioSessionDispatchReachesRealAgentCommandHandler(t *testing.T) {
	requireBroker(t)

	nodeID := "audio-node-" + uniqueSuffix()
	assetDir := t.TempDir()
	startAgent(t, agentConfig{nodeID: nodeID, assetDir: assetDir})

	// Warm-up: an agent's SUBSCRIBE to its own cmd topic races this
	// function's return (agent_command_test.go's own
	// awaitAgentReceivingCommands doc comment) — a command dispatched
	// into that window is silently dropped, not delayed, which would make
	// this test hang on the coordinator's own 15s confirmation deadline
	// for the wrong reason (a lost message, not a slow one) rather than
	// prove anything about the fix.
	cli, w := startCmdClient(t, nodeID)
	awaitAgentReceivingCommands(t, cli, w, nodeID)

	dataDir := t.TempDir()
	adminToken := createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: "coord-" + uniqueSuffix(), bearerToken: adminToken,
	})

	sessionID := "night-session-" + uniqueSuffix()
	result := dispatchAudioSessionCommand(t, coord, adminToken, nodeID, sessionID, "stop", 1)

	if result.Outcome == "failed" && strings.Contains(strings.ToLower(result.Reason), "revision") {
		t.Fatalf("dispatch reported the pre-fix defect verbatim: outcome=%q reason=%q — params.revision never reached the agent", result.Outcome, result.Reason)
	}
	if result.Outcome != "refused" {
		t.Fatalf("outcome = %q, want %q (Manager.Stop's own clean refusal for an unknown session — reached only once revision parsing succeeds); reason = %q",
			result.Outcome, "refused", result.Reason)
	}
	if result.Reason != "session does not exist" {
		t.Fatalf("reason = %q, want %q verbatim from internal/agent/audio.Manager.Stop", result.Reason, "session does not exist")
	}
}

// TestAudioSessionDispatchApplyThenStopReachesRealAgentAcrossTwoCommands
// proves the fix on the more commonly exercised path: apply (which
// carries a params object of its own, not just the three common keys)
// followed by stop against the SAME session, both through the real
// coordinator into the real agent, with the session genuinely created in
// between — apply must be accepted (not "failed: params.revision is
// required") and the following stop must then observe the session as
// real and refuse for an entirely different, revision-conflict-free
// reason if any, or succeed.
func TestAudioSessionDispatchApplyThenStopReachesRealAgentAcrossTwoCommands(t *testing.T) {
	requireBroker(t)

	nodeID := "audio-node-" + uniqueSuffix()
	assetDir := t.TempDir()
	startAgent(t, agentConfig{nodeID: nodeID, assetDir: assetDir})

	cli, w := startCmdClient(t, nodeID)
	awaitAgentReceivingCommands(t, cli, w, nodeID)

	dataDir := t.TempDir()
	adminToken := createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: "coord-" + uniqueSuffix(), bearerToken: adminToken,
	})

	sessionID := "apply-session-" + uniqueSuffix()

	applyBody := map[string]any{
		"revision":       1,
		"idempotencyKey": "cmd-apply-" + uniqueSuffix(),
		"params":         map[string]any{"sourceRole": "show"},
	}
	status, respBody := postRawWithToken(t, coord, "/api/v1/nodes/"+nodeID+"/audio/sessions/"+sessionID+"/apply", adminToken, applyBody)
	if status != http.StatusOK {
		t.Fatalf("apply: status = %d, want 200; body: %s", status, respBody)
	}
	var applyResp v1.AudioSessionCommandResponse
	if err := json.Unmarshal(respBody, &applyResp); err != nil {
		t.Fatalf("decode apply response: %v; body: %s", err, respBody)
	}
	if applyResp.Command.Outcome == "failed" && strings.Contains(strings.ToLower(applyResp.Command.Reason), "revision") {
		t.Fatalf("apply reported the pre-fix defect verbatim: outcome=%q reason=%q", applyResp.Command.Outcome, applyResp.Command.Reason)
	}
	// This agent's real playback engine (internal/agent/audio.
	// SwitchableEngine behind the real gstengine backend, Track C phase
	// 1b) has never received an audio.node.configure binding, so its own
	// Available() reports false and Manager's gateAvailability rewrites
	// apply's structural success (Position) to Unconfirmable with THAT
	// reason — still proof revision parsing succeeded, since a
	// params.revision failure would have reported Outcome "failed" with a
	// DIFFERENT reason instead.
	if applyResp.Command.Outcome != "unconfirmable" {
		t.Fatalf("apply outcome = %q, want %q (no audio.node binding has ever been delivered to this node); reason = %q",
			applyResp.Command.Outcome, "unconfirmable", applyResp.Command.Reason)
	}
	if !strings.Contains(applyResp.Command.Reason, "no audio.node configuration has been delivered to this node yet") {
		t.Fatalf("apply reason = %q, want the real engine's own no-binding reason", applyResp.Command.Reason)
	}

	stopResult := dispatchAudioSessionCommand(t, coord, adminToken, nodeID, sessionID, "stop", 2)
	if stopResult.Outcome == "failed" && strings.Contains(strings.ToLower(stopResult.Reason), "revision") {
		t.Fatalf("stop reported the pre-fix defect verbatim: outcome=%q reason=%q", stopResult.Outcome, stopResult.Reason)
	}
	if stopResult.Reason == "session does not exist" {
		t.Fatalf("stop reason = %q, but this session was just created by the apply above — the agent's session state did not persist across two real dispatches", stopResult.Reason)
	}
}

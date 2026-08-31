//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
)

// This file proves Track B seam B4's transport probe end to end, against a
// REAL showmesh-agent subprocess on THIS machine (a genuine
// missing-NDI-runtime node: no NDI SDK is installed here, confirmed by
// reproducing the gst-inspect/gst-launch split by hand before this test was
// written — see the seam's own report), a REAL Mosquitto broker, a REAL
// showmesh-coordinator, and the REAL showmeshctl binary run as its own
// subprocess (matching cli_test.go's own reasoning for why that must be a
// process over a socket, never an in-process call).
//
// There is no coordinator-side dispatcher for render.transport.probe yet
// (a later seam's scope, matching render_test.go's identical note for
// render.surface.apply/clear) — this test plays the dispatcher's role
// itself via a raw MQTT publish to the agent's own cmd topic.
func TestRenderTransportProbeReachesObservationsAndCLI(t *testing.T) {
	dataDir := t.TempDir()
	coord := startCoordinator(t, dataDir, "coord-"+uniqueSuffix())

	const nodeID = "render-transport-01"
	agent := startAgent(t, agentConfig{nodeID: nodeID, label: "Render transport node"})
	defer agent.sigkill(t)

	cli, w := startCmdClient(t, nodeID)
	defer func() { _ = cli.Disconnect(&paho.Disconnect{ReasonCode: 0}) }()

	awaitAgentReceivingCommands(t, cli, w, nodeID)

	// Finding 18: render.transport.probe now refuses a surface this node
	// has never applied (Supervisor.SetTransportProbe no longer creates a
	// runner on demand — a typo'd or stale surface id used to manufacture
	// a permanent phantom `surface` resource with no discoverable removal
	// path). "garage" must exist first, exactly the way render_test.go's
	// own TestRenderReportReachesObservationsAndNodeAPI applies it, before
	// this test's probe against it can mean anything.
	applyCmd := echoCmd(nodeID, "cmd-transport-apply-1", "idem-transport-apply-1", "")
	applyCmd.Action = "render.surface.apply"
	// This test plays the coordinator's own dispatcher role (this file's
	// own doc comment), and every real dispatch sends a surface's complete
	// geometry regardless of whether a sequence is assigned yet
	// (internal/coordinator/api/renderdispatch.go); buildFSEQAssignment
	// now refuses a bare or partial-geometry apply outright rather than
	// silently accepting the "not yet consumed" shape this hand-rolled
	// payload used to send.
	applyCmd.Params = map[string]any{
		"surfaceId":    "garage",
		"channelRange": map[string]any{"startChannel": 1, "channelCount": 12},
		"geometry":     map[string]any{"width": 2, "height": 2, "pixelFormat": "rgb"},
		"frameRate":    40,
	}
	dispatchCmd(t, cli, nodeID, applyCmd)
	applyResult := waitForResult(t, w, applyCmd.CommandID, 15*time.Second)
	if applyResult.Outcome != "confirmed" {
		t.Fatalf("setup render.surface.apply outcome = %q, want confirmed; reason=%q (agent logs:\n%s)",
			applyResult.Outcome, applyResult.Reason, agent.logs.String())
	}

	probeCmd := echoCmd(nodeID, "cmd-transport-probe-1", "idem-transport-probe-1", "")
	probeCmd.Action = "render.transport.probe"
	probeCmd.Params = map[string]any{"surfaceId": "garage"}
	dispatchCmd(t, cli, nodeID, probeCmd)

	// Confirmed is expected to be FALSE: this machine has no NDI runtime,
	// so a real state-transition probe genuinely fails. A result of
	// "confirmed" here would mean the probe stopped being real.
	result := waitForResult(t, w, probeCmd.CommandID, 15*time.Second)
	if result.Outcome != "unconfirmed" {
		t.Fatalf("render.transport.probe outcome = %q, want unconfirmed (this machine has no NDI runtime); reason=%q (agent logs:\n%s)",
			result.Outcome, result.Reason, agent.logs.String())
	}

	// --- GET /api/v1/observations carries the real probe evidence ---
	waitFor(t, 15*time.Second, 250*time.Millisecond, func() bool {
		status, body := coord.getRaw(t, "/api/v1/observations?resourceKind=surface&resourceId=garage")
		return status == 200 && containsAll(string(body), `"surface.transport.available"`, `"value":false`)
	}, "GET /api/v1/observations to report surface.transport.available = false for garage")

	status, obsBody := coord.getRaw(t, "/api/v1/observations?resourceKind=surface&resourceId=garage")
	if status != 200 {
		t.Fatalf("GET /api/v1/observations: status = %d, body: %s", status, obsBody)
	}
	if !containsAll(string(obsBody), `"surface.transport.reason"`) {
		t.Errorf("GET /api/v1/observations body missing surface.transport.reason: %s", obsBody)
	}

	// --- the real showmeshctl binary reads the same evidence and exits 22 ---
	exitCode, stdout, stderr := runShowmeshctl(t, 15*time.Second,
		"render", "transport", "--server", "http://"+coord.httpAddr, "garage")
	if exitCode != 22 {
		t.Fatalf("showmeshctl render transport exit code = %d, want 22 (exitRenderUnavailable); stdout=%q stderr=%q", exitCode, stdout, stderr)
	}
	if !strings.Contains(stdout, "UNAVAILABLE") {
		t.Errorf("showmeshctl render transport stdout = %q, want it to say UNAVAILABLE", stdout)
	}
}

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

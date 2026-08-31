//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// This file proves Track B seam B2b's own claim end to end, against a REAL
// showmesh-agent subprocess, a REAL Mosquitto broker, and a REAL
// showmesh-coordinator: a render report published by the agent reaches
// GET /api/v1/observations (resourceKind=surface) and the same node's
// GET /api/v1/nodes/{id} response, exactly the way coordinator.go actually
// wires internal/coordinator/collector/noderender in production — never a
// test double standing in for ingest, the collector, or the store.
//
// Like agent_command_test.go, there is no coordinator-side command
// dispatcher yet (that is a later seam's scope), so this test plays the
// dispatcher's role itself: a raw MQTT client publishes
// "render.surface.apply" directly to the agent's own cmd topic, exactly
// the mechanism agent_command_test.go already uses for "agent.echo". The
// agent runs a REAL gst-launch-1.0 pipeline drawing its own idle output (no
// FSEQ content is assigned); this environment has GStreamer 1.28.6
// installed, so this test proves the pipeline actually reaches "running",
// not merely that a report was
// fabricated.

func TestRenderReportReachesObservationsAndNodeAPI(t *testing.T) {
	dataDir := t.TempDir()
	// No admin/token needed: reads stay open by default (ADR-024 decision
	// 2) and this test only ever GETs.
	coord := startCoordinator(t, dataDir, "coord-"+uniqueSuffix())

	const nodeID = "render-01"
	agent := startAgent(t, agentConfig{nodeID: nodeID, label: "Render node"})
	defer agent.sigkill(t)

	cli, w := startCmdClient(t, nodeID)
	defer func() { _ = cli.Disconnect(&paho.Disconnect{ReasonCode: 0}) }()

	// Wait for the agent to actually be receiving commands before
	// dispatching the real one — the same warm-up
	// TestAgentCommandAgentEchoConfirmedAndObserved uses, so a command
	// published before the agent's SUBSCRIBE has landed does not silently
	// vanish.
	awaitAgentReceivingCommands(t, cli, w, nodeID)

	applyCmd := echoCmd(nodeID, "cmd-render-apply-1", "idem-render-apply-1", "")
	applyCmd.Action = "render.surface.apply"
	// This test plays the coordinator's own dispatcher role (this file's
	// own doc comment), and every real dispatch sends a surface's complete
	// geometry regardless of whether a sequence is assigned yet
	// (internal/coordinator/api/renderdispatch.go) — buildFSEQAssignment
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

	result := waitForResult(t, w, applyCmd.CommandID, 15*time.Second)
	if result.Outcome != "confirmed" {
		t.Fatalf("render.surface.apply outcome = %q, want confirmed; reason=%q (agent logs:\n%s)", result.Outcome, result.Reason, agent.logs.String())
	}

	// --- GET /api/v1/observations?resourceKind=surface&resourceId=garage ---
	waitFor(t, 15*time.Second, 250*time.Millisecond, func() bool {
		status, body := coord.getRaw(t, "/api/v1/observations?resourceKind=surface&resourceId=garage")
		return status == 200 && len(body) > 0 && containsAll(string(body), `"surface.pipeline.state"`, `"value":"running"`)
	}, "GET /api/v1/observations?resourceKind=surface to report surface.pipeline.state = running for garage")

	status, obsBody := coord.getRaw(t, "/api/v1/observations?resourceKind=surface&resourceId=garage")
	if status != 200 {
		t.Fatalf("GET /api/v1/observations: status = %d, body: %s", status, obsBody)
	}
	// The source is node-scoped ("node-render:<nodeID>"), not the bare
	// collector id — see noderender.SourceFor's doc comment: two different
	// nodes reporting the same surface id must not collide on one row, so
	// every observation this collector produces carries its own node's
	// identity in Source. Asserted as the exact node-scoped value, not a
	// "node-render" prefix match, so this test would fail again if the
	// per-node scoping regressed back to the bare, colliding id.
	if !containsAll(string(obsBody), `"kind":"surface"`, `"id":"garage"`, `"source":"node-render:render-01"`) {
		t.Errorf("GET /api/v1/observations body missing expected surface/garage/node-render:render-01 evidence: %s", obsBody)
	}

	// --- GET /api/v1/nodes/render-01 carries the same evidence under render ---
	var node v1.Node
	waitFor(t, 10*time.Second, 250*time.Millisecond, func() bool {
		nv, ok := coord.findNode(t, nodeID)
		if !ok {
			return false
		}
		node = nv
		return len(node.Render) > 0
	}, "GET /api/v1/nodes/render-01 to carry render evidence")

	found := false
	for _, o := range node.Render {
		if o.Resource.Kind == "surface" && o.Resource.ID == "garage" && o.Signal == "surface.pipeline.state" {
			found = true
			if o.Value == nil {
				t.Errorf("node.render surface.pipeline.state evidence has a nil value")
			}
		}
	}
	if !found {
		t.Errorf("node %+v: render does not contain a surface.pipeline.state entry for surface garage", node)
	}

	// Clean up the pipeline this test started, so a stray gst-launch-1.0
	// process never outlives the test (defer agent.sigkill above also
	// tears it down, but a clean stop here is what confirms
	// render.surface.clear's own reporting works, at no extra process cost
	// since this test already needs the teardown).
	clearCmd := echoCmd(nodeID, "cmd-render-clear-1", "idem-render-clear-1", "")
	clearCmd.Action = "render.surface.clear"
	clearCmd.Params = map[string]any{"surfaceId": "garage"}
	dispatchCmd(t, cli, nodeID, clearCmd)
	clearResult := waitForResult(t, w, clearCmd.CommandID, 15*time.Second)
	if clearResult.Outcome != "confirmed" {
		t.Errorf("render.surface.clear outcome = %q, want confirmed; reason=%q", clearResult.Outcome, clearResult.Reason)
	}
}

// containsAll reports whether haystack contains every one of needles.
func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}

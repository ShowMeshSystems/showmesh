//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

// This file is the acceptance proof for the Track H render-assignment gap
// (see TRACK-H-cues-and-playlists.md), following render_dispatch_test.go's
// established pattern: every claim is driven
// through the REAL showmeshctl binary against a REAL showmesh-coordinator
// subprocess and a REAL showmesh-agent subprocess over a real Mosquitto
// broker. Nothing here dispatches raw MQTT directly.
//
// THE DEFECT THIS CLOSES: nothing ever created a node's persisted render
// assignment except an operator dispatching render.surface.apply by hand.
// ADR-043's H0.7 clears assignments at boot. Together those meant a render
// node that rebooted mid-show never rendered again on its own — the
// catalog could be redeployed, the assets already there, but every cue
// activation kept failing until an operator remembered to run a manual
// apply. This file proves cuecatalog.deploy alone (no render apply, ever)
// is now enough: it establishes every one of the node's active-show
// show.surface objects with NO sequence selected, which is exactly what
// [renderOperations.applySurface] (internal/agent/renderops.go) has always
// tolerated for a params object omitting fseqFilename — build contract
// ruling 4's own "accepted and persisted, not yet all consumed" posture,
// reused here rather than invented.
//
// ACCEPTANCE GAP, stated plainly rather than implied: this test restarts
// the AGENT PROCESS (a real OS process, SIGKILL'd and re-exec'd against the
// same on-disk asset/state directory) to stand in for a node reboot. It
// does not reboot real hardware — this environment has none to reboot.
// Process-level restart already exercises ADR-043 H0.7's own boot-clearing
// decision (decideBootResume, internal/agent/bootresume.go), which does not
// distinguish a process restart from a hardware one; only real hardware
// power-cycle timing (firmware, kernel boot, disk remount) is left
// unverified.
//
// A second, narrower gap: this test does not drive a real cue activation
// through FPP (no bench fppd is started here, and no existing integration
// harness in this package drives FPP-triggered cue.activate end-to-end
// yet — activateSurfaceRender's own path is unchanged by this PR). What it
// proves at the binary level is that the surface holds a CONFIRMED render
// assignment with no manual apply ever issued — the exact evidence
// [ReadinessNodeRenderUnassigned] (internal/coordinator/fppreconcile/
// readiness.go) checks, and the exact precondition activateRender
// (internal/agent/cueactivationrender.go) requires before it will swap in
// an FSEQ at all. That function's own success on top of an established
// assignment is proved separately, at the agent unit-test level, by
// TestActivateRenderSucceedsOnAnEstablishedNoSequenceAssignment
// (internal/agent/cueactivationrender_test.go) — using the SAME
// applySurface code path this test exercises for real over MQTT.
func TestCueCatalogDeployEstablishesRenderAssignmentEndToEnd(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, true)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()

	mustCtl(t, coord, token, []string{"declare", "--label", "catalog establish node"}, nodeID)
	agentDir := writeTempDirHelper(t)
	agent := startAgent(t, agentConfig{nodeID: nodeID, assetDir: agentDir})

	mustCtl(t, coord, token, []string{"show", "set", "--name", "Catalog Establish E2E"}, showID)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)
	mustCtl(t, coord, token, []string{
		// hdmi, not ndi: no real hdmi sink exists yet, so applyOutputSink
		// falls back to the same fakesink+queue pipeline
		// render_dispatch_test.go's own TestRenderApplyClearRestartAgainstRealAgent
		// proved reliable — this test needs a genuine gst-launch-1.0
		// process to reach "running", not NDI transport reachability.
		"surface", "set", "--show", showID, "--name", "Wall", "--node", nodeID,
		"--start-channel", "1", "--channel-count", "12", "--width", "2", "--height", "2",
		"--pixel-format", "rgb", "--frame-rate", "40", "--transport", "hdmi", "--hdmi-display", "HDMI-1",
	}, surfaceID)

	// Deliberately no cue and no asset uploaded anywhere in this test:
	// establishment resolves show.surface, the active show, and
	// render.settings alone — it has nothing to resolve a sequence
	// against, and must not need one.

	// --- before any deploy: no render evidence exists for this node at
	// all (render_dispatch_test.go's own exitRenderUnavailable=22). ---
	code, _, _ := runCtl(t, coord, token, []string{"render", "status"}, nodeID)
	if code != 22 {
		t.Fatalf("render status before any deploy: exit = %d, want 22 (exitRenderUnavailable)", code)
	}

	// --- THE REPRODUCTION: deploy the catalog, with NO manual render
	// apply ever issued. Against unmodified code, this stays exit 22
	// forever — nothing ever creates the assignment. ---
	deployOut := mustCtl(t, coord, token, []string{"cuecatalog", "deploy"}, nodeID)
	if !strings.Contains(deployOut, ": confirmed ") {
		t.Fatalf("cuecatalog deploy stdout = %q, want a confirmed outcome", deployOut)
	}

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		statusCode, statusOut, _ := runCtl(t, coord, token, []string{"render", "status"}, nodeID)
		return statusCode == 0 && strings.Contains(statusOut, surfaceID) && strings.Contains(statusOut, "running")
	}, "surface to be established (running, no sequence) by the catalog deploy alone, with no manual render apply ever issued")

	// --- simulate a node reboot: kill the real agent process and start a
	// fresh one against the SAME on-disk node id and asset directory,
	// exactly as a real reboot would resume with the same persisted
	// on-disk state (see this file's own doc comment for what this does
	// and does not prove). ---
	agent.sigkill(t)
	agent = startAgent(t, agentConfig{nodeID: nodeID, assetDir: agentDir})
	t.Cleanup(func() { agent.stopIfRunning() })
	waitOnline(t, coord, nodeID)

	// --- redeploy ONLY the catalog. Still no manual render apply anywhere
	// in this test. This is the literal acceptance scenario: a node that
	// has been restarted, holds the current catalog and every asset (none
	// are even needed here), and has had NO manual render apply,
	// re-establishes its own render assignment. ---
	redeployOut := mustCtl(t, coord, token, []string{"cuecatalog", "deploy"}, nodeID)
	if !strings.Contains(redeployOut, ": confirmed ") {
		t.Fatalf("cuecatalog deploy (after restart) stdout = %q, want a confirmed outcome", redeployOut)
	}

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		statusCode, statusOut, _ := runCtl(t, coord, token, []string{"render", "status"}, nodeID)
		return statusCode == 0 && strings.Contains(statusOut, surfaceID) && strings.Contains(statusOut, "running")
	}, "surface to be re-established after a restart by a catalog-only redeploy, with no manual render apply ever issued")

	// The CLI's own "render status" (non-JSON) surface too, exactly as an
	// operator with no UI would see it.
	statusOut := mustCtl(t, coord, token, []string{"render", "status"}, nodeID)
	if !strings.Contains(statusOut, surfaceID) || !strings.Contains(statusOut, "running") {
		t.Fatalf("render status after restart+redeploy = %q, want it to report surface %q running", statusOut, surfaceID)
	}
}

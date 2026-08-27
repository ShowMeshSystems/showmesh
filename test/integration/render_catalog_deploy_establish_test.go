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
// ACCEPTANCE GAP, stated plainly rather than implied: this test does not
// drive a real cue activation through FPP (no bench fppd is started here,
// and no existing integration
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
	startAgent(t, agentConfig{nodeID: nodeID, assetDir: agentDir})

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

	// --- THE ACTUAL RECOVERY CLAIM: a node that holds an unchanged catalog
	// but has LOST its render assignment gets it back from a catalog-only
	// redeploy, no manual render apply ever issued. A restart of the real
	// agent process is deliberately NOT how this is reproduced: an
	// unchanged catalog's own H3 authorization tuple still matches what a
	// restarted agent's persisted assignment already carries, so
	// decideBootResume (internal/agent/bootresume.go) resumes it entirely
	// on its own, with NO coordinator round trip at all — proving only
	// boot resume, never establishment, no matter what a redeploy issued
	// afterward does or does not do (this is exactly the gap the
	// reviewer's own probe found in this file's first version: the surface
	// was already "running" again before the post-restart redeploy ever
	// ran). "render clear" is what actually removes the node's own
	// assignment while the SAME live agent process (and the SAME
	// unchanged catalog) stays in place, so nothing but a genuine
	// establishment can bring it back. ---
	clearOut := mustCtl(t, coord, token, []string{"render", "clear"}, nodeID, surfaceID)
	if !strings.Contains(clearOut, "confirmed:") || !strings.Contains(clearOut, `"stopped"`) {
		t.Fatalf("render clear stdout = %q, want a confirmed stopped outcome", clearOut)
	}

	// --- confirm the node cannot render: a cleared surface carries no
	// value-bearing surface.pipeline.state evidence any more — Supervisor.
	// SnapshotAll (internal/agent/pipeline/supervisor.go) excludes a
	// cleared surface entirely rather than reporting it Stopped, so this
	// node's GET goes back to reporting NO surface evidence at all, the
	// exact same exitRenderUnavailable=22 state as "before any deploy". ---
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		statusCode, _, _ := runCtl(t, coord, token, []string{"render", "status"}, nodeID)
		return statusCode == 22
	}, "surface to report no render evidence after render clear")
	clearedCode, clearedStatusOut, _ := runCtl(t, coord, token, []string{"render", "status"}, nodeID)
	if clearedCode != 22 {
		t.Fatalf("render status after clear: exit = %d, want 22 (exitRenderUnavailable) — the node must hold no assignment before the recovery redeploy\nstdout=%s", clearedCode, clearedStatusOut)
	}

	// --- redeploy ONLY the catalog — same generation and revision as the
	// first deploy above, still no manual render apply anywhere in this
	// test. This is the literal acceptance scenario: a node that holds the
	// current catalog and every asset (none are even needed here) but has
	// lost its render assignment re-establishes it on its own from a
	// catalog-only redeploy. ---
	redeployOut := mustCtl(t, coord, token, []string{"cuecatalog", "deploy"}, nodeID)
	if !strings.Contains(redeployOut, ": confirmed ") {
		t.Fatalf("cuecatalog deploy (after clear) stdout = %q, want a confirmed outcome", redeployOut)
	}

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		statusCode, statusOut, _ := runCtl(t, coord, token, []string{"render", "status"}, nodeID)
		return statusCode == 0 && strings.Contains(statusOut, surfaceID) && strings.Contains(statusOut, "running")
	}, "surface to be re-established after render clear by a catalog-only redeploy, with no manual render apply ever issued")

	// The CLI's own "render status" (non-JSON) surface too, exactly as an
	// operator with no UI would see it: the node holds an assignment it
	// did not have a moment ago, and got it from nothing but the redeploy.
	statusOut := mustCtl(t, coord, token, []string{"render", "status"}, nodeID)
	if !strings.Contains(statusOut, surfaceID) || !strings.Contains(statusOut, "running") {
		t.Fatalf("render status after clear+redeploy = %q, want it to report surface %q running", statusOut, surfaceID)
	}
}

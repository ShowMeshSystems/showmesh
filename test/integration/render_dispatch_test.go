//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
)

// This file is Track B seam B2b-front's own acceptance proof, following
// assets_test.go's established pattern: every claim is driven through the
// REAL showmeshctl binary (mustCtl/runCtl) against a REAL
// showmesh-coordinator subprocess and a REAL showmesh-agent subprocess
// (running a real gst-launch-1.0 pipeline — this environment has
// GStreamer 1.28.6 installed) over the real throwaway Mosquitto this
// package's harness starts. Nothing here dispatches raw MQTT directly —
// render_test.go's own dispatch-by-hand approach predates this seam's
// coordinator-side dispatcher and is superseded by this file for the
// HTTP/CLI path specifically (render_test.go itself is left in place: it
// still proves the report-ingestion half, which this seam did not touch).

// TestRenderApplyClearRestartAgainstRealAgent is the full apply -> confirm
// -> clear -> restart cycle: build contract ruling 4's own claim (the
// coordinator resolves and sends a COMPLETE assignment, including the
// asset's runtime filename/content hash by identity — ADR-028) proved
// against a real uploaded asset, a real show.surface, and a real agent
// that actually starts and stops a gst-launch-1.0 pipeline.
//
// Asset sync is REAL here rather than stubbed, and the FSEQ is genuinely
// valid: since seam B3, applySurface hashes the node-local file against the
// assignment's content hash and parses it, so an assignment whose bytes
// never reached the node (or reached it as a placeholder string) fails
// before any pipeline starts. That makes this the first test in which an
// FSEQ moves through the asset store to a node and is then rendered.
func TestRenderApplyClearRestartAgainstRealAgent(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, true)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()

	declareAndStartAgent(t, coord, token, nodeID, "render node")

	mustCtl(t, coord, token, []string{"show", "set", "--name", "Render E2E"}, showID)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)
	mustCtl(t, coord, token, []string{
		"surface", "set", "--show", showID, "--name", "Wall", "--node", nodeID,
		"--start-channel", "1", "--channel-count", "12", "--width", "2", "--height", "2",
		"--pixel-format", "rgb", "--frame-rate", "40", "--transport", "ndi", "--ndi-source-name", "test-source",
	}, surfaceID)

	// 24 channels so the surface's 12-channel range at start channel 1 is a
	// genuine sub-range rather than the whole file, and 40 frames at 25 ms
	// so the frame writer has a second of real content to walk.
	filePath := writeTempFile(t, "opener.fseq", fseqtest.Build(24, 40, 25))
	uploadNodeAsset(t, coord, token, showID, "opener", "fseq", nodeID, filePath)

	// The node must actually hold the bytes before apply: since B3 the
	// agent hashes and parses its local copy, so applying before sync
	// completes fails on a missing file rather than on anything this test
	// is about.
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		return apiNodeManifestState(t, coord, nodeID) == "ready"
	}, "node asset manifest to become ready so the FSEQ is on the node before apply")

	// --- status before apply: no render report published yet -> exitRenderUnavailable (22) ---
	code, stdout, stderr := runCtl(t, coord, token, []string{"render", "status"}, nodeID)
	if code != 22 {
		t.Fatalf("render status (before apply): exit = %d, want 22 (exitRenderUnavailable)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	// --- apply: the coordinator resolves the surface's complete assignment
	// (including the just-uploaded asset's own runtime filename/content
	// hash, never by filename alone) and confirms by evidence against the
	// real agent's real gst-launch-1.0 pipeline. ---
	applyOut := mustCtl(t, coord, token, []string{"render", "apply"}, nodeID, surfaceID, "opener")
	if !strings.Contains(applyOut, "confirmed:") {
		t.Fatalf("render apply stdout = %q, want a confirmed outcome", applyOut)
	}
	if !strings.Contains(applyOut, `"running"`) {
		t.Fatalf("render apply stdout = %q, want it to name the confirmed running state", applyOut)
	}

	// --- status after apply: reports the running pipeline ---
	statusOut := mustCtl(t, coord, token, []string{"render", "status"}, nodeID)
	if !strings.Contains(statusOut, surfaceID) || !strings.Contains(statusOut, "running") {
		t.Fatalf("render status (after apply) = %q, want it to report surface %q running", statusOut, surfaceID)
	}

	// --- restart: clears any fast-failure lockout and restarts from the
	// currently-applied spec; confirms running again. ---
	restartOut := mustCtl(t, coord, token, []string{"render", "restart"}, nodeID, surfaceID)
	if !strings.Contains(restartOut, "confirmed:") {
		t.Fatalf("render restart stdout = %q, want a confirmed outcome", restartOut)
	}

	// --- clear: stops the real pipeline and confirms stopped. ---
	clearOut := mustCtl(t, coord, token, []string{"render", "clear"}, nodeID, surfaceID)
	if !strings.Contains(clearOut, "confirmed:") || !strings.Contains(clearOut, `"stopped"`) {
		t.Fatalf("render clear stdout = %q, want a confirmed stopped outcome", clearOut)
	}
}

// TestRenderApplyRefusesWhenNoAssetResolvesEndToEnd is build contract
// ruling 4's refusal half, driven through the real CLI: a surface with no
// matching current asset for the requested sequence is refused outright —
// the coordinator never dispatches a partial assignment to the real
// agent, and this test proves that by checking the agent never received
// anything for it (no pipeline evidence appears within a bounded wait).
func TestRenderApplyRefusesWhenNoAssetResolvesEndToEnd(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, false)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()

	declareAndStartAgent(t, coord, token, nodeID, "render node")

	mustCtl(t, coord, token, []string{"show", "set", "--name", "Render Refusal E2E"}, showID)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)
	mustCtl(t, coord, token, []string{
		"surface", "set", "--show", showID, "--name", "Wall", "--node", nodeID,
		"--start-channel", "1", "--channel-count", "12", "--width", "2", "--height", "2",
		"--pixel-format", "rgb", "--frame-rate", "40", "--transport", "ndi", "--ndi-source-name", "test-source",
	}, surfaceID)
	// Deliberately no asset uploaded for sequence "opener".

	code, stdout, stderr := runCtl(t, coord, token, []string{"render", "apply"}, nodeID, surfaceID, "opener")
	if code == 0 {
		t.Fatalf("render apply with no resolvable asset: exit = 0, want non-zero refusal\nstdout=%s", stdout)
	}
	if !strings.Contains(stderr, "no asset found for surface") {
		t.Fatalf("render apply stderr = %q, want it to name the unresolved surface/sequence", stderr)
	}

	// No pipeline evidence should ever appear for this surface — the
	// refusal happened before any dispatch reached the agent.
	time.Sleep(500 * time.Millisecond)
	statusCode, statusOut, _ := runCtl(t, coord, token, []string{"render", "status"}, nodeID)
	if statusCode == 0 && strings.Contains(statusOut, surfaceID) {
		t.Fatalf("render status after a refused apply unexpectedly shows surface %q: %s", surfaceID, statusOut)
	}
}

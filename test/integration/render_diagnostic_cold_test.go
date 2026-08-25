//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the running-system gate for the owner ruling that the
// diagnostic idle output must render unconditionally: "99 out of 100 times
// I'll use that is when FPP wouldn't be on to display output."
//
// It is the closest this repository can get to the ruling's own test
// condition, nothing running but the agent. A real showmesh-agent
// subprocess starts with a node-local diagnostic surface configured and
// NOTHING else: no coordinator process, no render.surface.apply ever
// dispatched, no FSEQ in its asset directory, no MultiSync sender, no held
// cue catalog, no persisted assignment. Like render_failure_mode_test.go it
// proves the OUTPUT BYTES rather than a colour anybody claims to have seen,
// through the same SHOWMESH_GST_LAUNCH capture script.
//
// The broker is the one thing still running, because this package's harness
// cannot start an agent without one and the retained render report is read
// off it. The agent is never sent a command over it, and the frames
// asserted below are produced with no coordinator in existence.

const (
	coldDiagSurfaceID = "cold-diagnostic-surface"

	// A small surface keeps one frame short. 64 wide is also wide enough
	// that the bar (width/32, floored at one pixel) is a real 2-pixel mark
	// rather than the clamped minimum.
	coldDiagWidth         = 64
	coldDiagHeight        = 4
	coldDiagBytesPerPixel = 3
	coldDiagFrameBytes    = coldDiagWidth * coldDiagHeight * coldDiagBytesPerPixel

	// The two constants internal/agent/pipeline/frame.go fills the
	// diagnostic pattern with, restated here rather than imported: this is
	// the wire-visible output of the agent under test, and a gate that
	// imports the constant it is checking cannot catch the constant
	// changing.
	coldDiagBackgroundFill = 0x30
	coldDiagBarFill        = 0xFF
)

// TestDiagnosticSurfaceRendersOnAColdAgent starts a real agent with a
// node-local diagnostic surface and no other configuration at all, and
// asserts the diagnostic pattern reaches the pipeline.
func TestDiagnosticSurfaceRendersOnAColdAgent(t *testing.T) {
	workDir := t.TempDir()

	// An empty asset dir is three absences at once: no FSEQ, no persisted
	// assignment (.render-state), and no held cue catalog.
	assetDir := filepath.Join(workDir, "assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("mkdir asset dir: %v", err)
	}
	scriptPath, captureDir := writeCaptureScript(t, workDir)

	const nodeID = "cold-diag-node-01"
	agent := startAgent(t, agentConfig{
		nodeID:   nodeID,
		label:    "Cold diagnostic node",
		assetDir: assetDir,
		extraEnv: []string{
			"SHOWMESH_GST_LAUNCH=" + scriptPath,
			"SHOWMESH_RENDER_DIAGNOSTIC_SURFACE=" + coldDiagSurfaceID,
			fmt.Sprintf("SHOWMESH_RENDER_DIAGNOSTIC_WIDTH=%d", coldDiagWidth),
			fmt.Sprintf("SHOWMESH_RENDER_DIAGNOSTIC_HEIGHT=%d", coldDiagHeight),
			"SHOWMESH_RENDER_DIAGNOSTIC_FRAME_RATE=40",
			gateRenderInterval,
		},
	})
	defer agent.sigkill(t)

	// No command is ever dispatched to this agent and no coordinator is
	// started. Everything below is the agent acting on its own.

	frames := awaitColdDiagnosticCapture(t, captureDir)

	first, last := frames[0], frames[len(frames)-1]
	if !bytes.Contains(last, bytes.Repeat([]byte{coldDiagBackgroundFill}, coldDiagBytesPerPixel)) {
		t.Fatalf("the captured frame carries no diagnostic background fill: %x", last[:min(48, len(last))])
	}
	if !bytes.Contains(last, bytes.Repeat([]byte{coldDiagBarFill}, coldDiagBytesPerPixel)) {
		t.Fatalf("the captured frame carries no diagnostic bar: %x", last[:min(48, len(last))])
	}
	if bytes.Equal(last, make([]byte, coldDiagFrameBytes)) {
		t.Fatal("the captured frame is all black, which is exactly what this node drew before the ruling was implemented")
	}
	if bytes.Equal(first, last) {
		t.Fatal("the pattern never moved across the captured frames; ruling 3 requires generated content, never a held frame")
	}

	// The node's own retained render report is the operator-visible half:
	// a surface that draws but never appears in the report leaves the
	// coordinator unable to tell it from a node that was never assigned.
	report := awaitRenderReportMatching(t, nodeID, func(s mqttproto.RenderSurfaceReport) bool {
		return s.SurfaceID == coldDiagSurfaceID
	})
	var found *mqttproto.RenderSurfaceReport
	for i := range report.Surfaces {
		if report.Surfaces[i].SurfaceID == coldDiagSurfaceID {
			found = &report.Surfaces[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("the diagnostic surface never appeared in the node's render report; surfaces=%+v", report.Surfaces)
	}
	if found.Drawing != mqttproto.RenderDrawingIdle {
		t.Fatalf("the diagnostic surface reports drawing=%q, want %q", found.Drawing, mqttproto.RenderDrawingIdle)
	}
	if found.IdleMode != "diagnostic" {
		t.Fatalf("the diagnostic surface reports idleMode=%q, want %q", found.IdleMode, "diagnostic")
	}
	t.Logf("cold agent evidence: %d bytes captured, drawing=%q idleMode=%q timelineState=%q",
		len(last), found.Drawing, found.IdleMode, found.TimelineState)
}

// awaitColdDiagnosticCapture waits until the capture file holds at least two
// whole frames and returns them, so the caller can prove the pattern moves.
func awaitColdDiagnosticCapture(t *testing.T, captureDir string) [][]byte {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, b := newestCapture(t, captureDir)
		if len(b) >= 3*coldDiagFrameBytes {
			var frames [][]byte
			for off := 0; off+coldDiagFrameBytes <= len(b); off += coldDiagFrameBytes {
				frames = append(frames, b[off:off+coldDiagFrameBytes])
			}
			return frames
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the cold agent never wrote a diagnostic frame to its pipeline")
	return nil
}

// TestDiagnosticSurfaceOverridesAnAssignedIdleOutputOnARealAgent is the
// other half of the ruling, against the real binaries: a node whose local
// configuration names the same surface a real coordinator then assigns.
//
// The assignment's own idle output is the build contract's default, black.
// The node-local setting must win, because the node that matters here is
// the one holding an assignment when FPP dies: its timeline reads unknown,
// unknown is an idle state, and black is a dark wall with the diagnostic
// surface configured and doing nothing. No MultiSync sender runs in this
// test, so the surface is idle exactly as it would be with FPP off.
func TestDiagnosticSurfaceOverridesAnAssignedIdleOutputOnARealAgent(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, true)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()

	// The agent is told to draw the diagnostic pattern on the very surface
	// the coordinator is about to assign to it. Started before the surface
	// exists, exactly as a real node is: the node-local setting is boot
	// configuration, not something it learns from the control plane.
	mustCtl(t, coord, token, []string{"declare", "--label", "override node"}, nodeID)
	agentDir := filepath.Join(t.TempDir(), "assets")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent asset dir: %v", err)
	}
	startAgent(t, agentConfig{
		nodeID:   nodeID,
		assetDir: agentDir,
		extraEnv: []string{
			"SHOWMESH_RENDER_DIAGNOSTIC_SURFACE=" + surfaceID,
			"SHOWMESH_RENDER_DIAGNOSTIC_WIDTH=2",
			"SHOWMESH_RENDER_DIAGNOSTIC_HEIGHT=2",
			"SHOWMESH_RENDER_DIAGNOSTIC_FRAME_RATE=40",
			gateRenderInterval,
		},
	})

	mustCtl(t, coord, token, []string{"show", "set", "--name", "Override E2E"}, showID)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)
	mustCtl(t, coord, token, []string{
		// hdmi for TestRenderApplyClearRestartAgainstRealAgent's own
		// reason: this proves idle-output precedence, not NDI reachability.
		"surface", "set", "--show", showID, "--name", "Wall", "--node", nodeID,
		"--start-channel", "1", "--channel-count", "12", "--width", "2", "--height", "2",
		"--pixel-format", "rgb", "--frame-rate", "40", "--transport", "hdmi", "--hdmi-display", "HDMI-1",
	}, surfaceID)

	filePath := writeTempFile(t, "opener.fseq", fseqtest.Build(24, 40, 25))
	uploadNodeAsset(t, coord, token, showID, "opener", "fseq", nodeID, filePath)
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		return apiNodeManifestState(t, coord, nodeID) == "ready"
	}, "node asset manifest to become ready so the FSEQ is on the node before apply")

	applyOut := mustCtl(t, coord, token, []string{"render", "apply"}, nodeID, surfaceID, "opener")
	if !strings.Contains(applyOut, "confirmed:") {
		t.Fatalf("render apply did not confirm: %s", applyOut)
	}

	// Wait for a settled idle report for this surface, then assert which
	// idle output actually won. Matching on a non-empty IdleMode rather
	// than on "diagnostic" means a regression produces a clear assertion
	// failure naming what it drew instead, not an opaque timeout.
	report := awaitRenderReportMatching(t, nodeID, func(s mqttproto.RenderSurfaceReport) bool {
		return s.SurfaceID == surfaceID && s.Drawing == mqttproto.RenderDrawingIdle && s.IdleMode != ""
	})
	var found *mqttproto.RenderSurfaceReport
	for i := range report.Surfaces {
		if report.Surfaces[i].SurfaceID == surfaceID {
			found = &report.Surfaces[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("the assigned surface never appeared in the node's render report; surfaces=%+v", report.Surfaces)
	}
	if found.IdleMode != "diagnostic" {
		t.Fatalf("the assigned surface reports idleMode=%q, want %q: the node-local diagnostic setting must take precedence over the assignment's own idle output, or a node holding an assignment with FPP dead shows a black wall", found.IdleMode, "diagnostic")
	}
	t.Logf("override evidence from the real agent: surface=%s drawing=%q idleMode=%q timelineState=%q",
		found.SurfaceID, found.Drawing, found.IdleMode, found.TimelineState)
}

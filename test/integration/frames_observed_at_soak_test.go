//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/noderender"
	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
)

// TestFramesObservedAtStaysCurrentPastTheStaleWindow is the load-bearing
// gate for this issue: render evidence going permanently stale 45 seconds
// after any apply, on a healthy, continuously running pipeline. It runs a
// REAL showmesh-agent subprocess (with a REAL FSEQ asset, so B3's
// FrameWriter is actually running, not merely B2a's bare test-pattern
// pipeline) against a REAL Mosquitto broker and a REAL
// showmesh-coordinator, with the production SHOWMESH_RENDER_REPORT_INTERVAL
// default (15s), and polls GET /api/v1/nodes/{id} every 5s for longer than
// DefaultValidFor (45s) so the bug (or its absence) actually has time to
// show up, not merely a single snapshot.
//
// Run once against the pre-fix tree (production code reverted to main,
// this test file kept) it reproduces the bug: every render signal,
// including surface.frames.written, pins its observedAt at apply time and
// flips to stale at T0+45s despite framesWritten climbing the whole time.
// Run again against the fixed tree, it proves the four frame signals'
// observedAt keeps advancing (in steps of at most 5s, the frame writer's
// own sampling window) and never goes stale, while surface.pipeline.state's
// own observedAt stays pinned at T0 throughout, exactly as it must (that
// pinning is the invariant the coordinator's own render-command
// confirmation depends on, and this issue's fix must not touch it).
//
// surface.pipeline.state itself IS expected to flip current -> stale
// during this soak, on both trees: it never transitions again after the
// initial apply (setState only stamps ObservedAt on a real transition), so
// its own evidence genuinely does age past DefaultValidFor. That is
// correct, unrelated behavior this issue does not change; this test
// asserts only that its ObservedAt VALUE stays pinned, never that its
// State stays current.
func TestFramesObservedAtStaysCurrentPastTheStaleWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("soak test: run explicitly, not under -short")
	}

	// The production default. Deliberately NOT compressed: DefaultValidFor
	// (45s, a hardcoded const) does not scale with this, so a compressed
	// report interval would not reproduce the real 3x-reports-per-window
	// ratio the bug report measured on a real node.
	t.Setenv(envRenderReportInterval, "15s")

	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, true)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()

	mustCtl(t, coord, token, []string{"declare", "--label", "soak node"}, nodeID)
	agentDir := writeTempDirHelper(t)
	agent := startAgent(t, agentConfig{nodeID: nodeID, assetDir: agentDir})
	defer agent.sigkill(t)

	mustCtl(t, coord, token, []string{"show", "set", "--name", "Soak"}, showID)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)
	mustCtl(t, coord, token, []string{
		// hdmi, not ndi: this development machine has no NDI runtime (same
		// reasoning render_kill_test.go's own comment gives). A real,
		// continuously-running gst-launch-1.0 pipeline is what this test
		// needs, not NDI transport reachability.
		"surface", "set", "--show", showID, "--name", "Soak Wall", "--node", nodeID,
		"--start-channel", "1", "--channel-count", "12", "--width", "2", "--height", "2",
		"--pixel-format", "rgb", "--frame-rate", "40", "--transport", "hdmi", "--hdmi-display", "HDMI-1",
	}, surfaceID)

	// 24 channels, 40 frames, 25ms/frame = a real 1s sequence; the writer
	// holds the last frame past end-of-file (frameIndexFor's documented
	// behavior) for the rest of the soak, so this stays a real, continuous
	// B3 FrameWriter run the whole time, not a one-shot.
	filePath := writeTempFile(t, "soak.fseq", fseqtest.Build(24, 40, 25))
	uploadNodeAsset(t, coord, token, showID, "opener", "fseq", nodeID, filePath)

	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		return apiNodeManifestState(t, coord, nodeID) == "ready"
	}, "node asset manifest to become ready so the FSEQ is on the node before apply")

	applyOut := mustCtl(t, coord, token, []string{"render", "apply"}, nodeID, surfaceID, "opener")
	if !strings.Contains(applyOut, "confirmed:") || !strings.Contains(applyOut, `"running"`) {
		t.Fatalf("render apply stdout = %q, want a confirmed running outcome", applyOut)
	}

	t0 := time.Now()
	t.Logf("=== soak start T0=%s, surface=%s, node=%s, SHOWMESH_RENDER_REPORT_INTERVAL=15s ===", t0.Format(time.RFC3339Nano), surfaceID, nodeID)

	signalsOfInterest := []string{
		string(noderender.SignalSurfacePipelineState),
		string(noderender.SignalSurfaceFramesWritten),
		string(noderender.SignalSurfaceFramesRate),
		string(noderender.SignalSurfaceFramesLate),
		string(noderender.SignalSurfaceFramesDropped),
	}

	type sample struct {
		at       time.Time
		signal   string
		value    any
		state    string
		observed string
		collect  string
	}

	var samples []sample
	const soakDuration = 130 * time.Second // > DefaultValidFor(45s), long enough to see the flip (or its absence) with margin
	const pollEvery = 5 * time.Second

	deadline := t0.Add(soakDuration)
	for time.Now().Before(deadline) {
		for _, sig := range signalsOfInterest {
			e, ok := findRenderSignal(t, coord, nodeID, surfaceID, sig)
			if !ok {
				t.Logf("t+%6.1fs  %-24s not found yet", time.Since(t0).Seconds(), sig)
				continue
			}
			s := sample{at: time.Now(), signal: sig, value: e.Value, state: e.State}
			if e.ObservedAt != nil {
				s.observed = *e.ObservedAt
			} else {
				s.observed = "<nil>"
			}
			if e.CollectedAt != nil {
				s.collect = *e.CollectedAt
			} else {
				s.collect = "<nil>"
			}
			samples = append(samples, s)
			t.Logf("t+%6.1fs  %-24s value=%-10v state=%-9s observedAt=%s collectedAt=%s",
				time.Since(t0).Seconds(), sig, e.Value, e.State, s.observed, s.collect)
		}
		time.Sleep(pollEvery)
	}

	t.Logf("=== soak end, wall-clock duration=%s, %d samples recorded ===", time.Since(t0), len(samples))

	if len(samples) == 0 {
		t.Fatalf("no samples recorded at all; agent logs:\n%s", agent.logs.String())
	}

	// Assert surface.pipeline.state's own observedAt stays pinned at
	// whatever its first recorded value was for the entire soak. This is
	// the guard this issue's fix must NOT break. If this assertion fails,
	// the fix defeated the setState-only-stamps-ObservedAt invariant
	// instead of fixing the frame-counter staleness bug.
	var pipelineStateObservedAt string
	for _, s := range samples {
		if s.signal != string(noderender.SignalSurfacePipelineState) {
			continue
		}
		if pipelineStateObservedAt == "" {
			pipelineStateObservedAt = s.observed
			continue
		}
		if s.observed != pipelineStateObservedAt {
			t.Errorf("surface.pipeline.state observedAt moved from %s to %s at t+%.1fs: this must stay pinned at T0 (setState-only invariant broken)",
				pipelineStateObservedAt, s.observed, s.at.Sub(t0).Seconds())
		}
	}
	if pipelineStateObservedAt == "" {
		t.Errorf("never observed a surface.pipeline.state sample")
	} else {
		t.Logf("surface.pipeline.state observedAt pinned at %s across the whole soak: PASS", pipelineStateObservedAt)
	}

	// The fact this issue is actually about: none of the four frame
	// signals may ever read stale during the soak, on a healthy,
	// continuously-advancing pipeline. On the pre-fix tree this fails
	// (every frame signal flips to stale at t+45s); on the fixed tree it
	// must pass.
	frameSignals := []string{
		string(noderender.SignalSurfaceFramesWritten),
		string(noderender.SignalSurfaceFramesRate),
		string(noderender.SignalSurfaceFramesLate),
		string(noderender.SignalSurfaceFramesDropped),
	}
	wentStale := map[string]bool{}
	for _, s := range samples {
		for _, sig := range frameSignals {
			if s.signal == sig && s.state == "stale" {
				wentStale[sig] = true
			}
		}
	}
	for _, sig := range frameSignals {
		if wentStale[sig] {
			t.Errorf("%s went stale during the soak: on a healthy, continuously-advancing pipeline this must never happen (this is the bug this issue fixes)", sig)
		} else {
			t.Logf("%s: never observed stale during the soak: PASS", sig)
		}
	}

	// framesWritten must actually have climbed during the soak (proves the
	// pipeline was really running and really being sampled, not that the
	// counters happened to sit at their initial value the whole time).
	var firstWritten, lastWritten int64
	haveFirst := false
	for _, s := range samples {
		if s.signal != string(noderender.SignalSurfaceFramesWritten) {
			continue
		}
		n, ok := toInt64(s.value)
		if !ok {
			continue
		}
		if !haveFirst {
			firstWritten = n
			haveFirst = true
		}
		lastWritten = n
	}
	if !haveFirst {
		t.Errorf("never observed a surface.frames.written value")
	} else if lastWritten <= firstWritten {
		t.Errorf("surface.frames.written did not advance: first=%d last=%d (pipeline may not actually be running)", firstWritten, lastWritten)
	} else {
		t.Logf("surface.frames.written advanced from %d to %d over the soak: PASS", firstWritten, lastWritten)
	}

	// Clean up the pipeline this test started.
	_ = mustCtl(t, coord, token, []string{"render", "clear"}, nodeID, surfaceID)
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

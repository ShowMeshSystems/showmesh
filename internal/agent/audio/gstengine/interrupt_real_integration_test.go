//go:build cgo

package gstengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file proves announcement mix policy "interrupt" against this
// package's own real Engine — real GStreamer pipelines, "fakesink" so no
// physical device is needed, same as this package's other real-integration
// tests — driven through the actual internal/agent/audio.Manager session
// layer rather than by calling Engine methods directly. Nothing here goes
// through MQTT: internal/agent's wire-level audio.session.apply parser
// (audiosessionops.go's parseApplyRequest) does not accept a mixPolicy
// field at all, a pre-existing gap unrelated to interrupt's own
// implementation, so a full broker-to-agent round trip cannot carry this
// policy today. This proves the real engine backing instead, at the same
// Manager API test/integration/audio_gstengine_test.go already proves
// reachable from a real dispatched command.

// resolveFromAssetDir mirrors internal/agent/audio.ProbeAsset's own path
// resolution (filepath.Join(assetDir, RuntimeFilename)) so a MediaRef
// resolves identically for asset probing and for this Engine's file open
// — this package's other real-engine tests drive Engine directly and
// never exercise that join.
func resolveFromAssetDir(assetDir string) AssetResolver {
	return func(m pkgaudio.MediaRef) (string, error) {
		return filepath.Join(assetDir, m.RuntimeFilename), nil
	}
}

func contentHashOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// newManagerTestEngine is [newTestEngine] with a Resolve bound to
// assetDir instead of the raw-RuntimeFilename resolver those tests use,
// since a Manager-driven session's RuntimeFilename is a bare filename
// under assetDir, never an absolute path.
func newManagerTestEngine(t *testing.T, assetDir string) *Engine {
	t.Helper()
	e, err := New(testConfig(resolveFromAssetDir(assetDir)))
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() {
		// Bounded, not a bare SetState: a test that deliberately abandons a
		// state change leaves a goroutine inside gst_element_set_state
		// holding that element's state lock, and this bin-level transition
		// recurses into the same element. An unbounded call here blocks the
		// whole test binary until its timeout rather than the branch alone.
		ctx, cancel := context.WithTimeout(context.Background(), testCleanupTimeout)
		defer cancel()
		if err := boundedCall(ctx, func() error {
			e.pipeline.SetState(gst.StateNull)
			return nil
		}); err != nil {
			t.Logf("test engine cleanup abandoned the pipeline's NULL transition: %v", err)
		}
	})
	return e
}

func findSnapshot(t *testing.T, snaps []agentaudio.SessionSnapshot, id pkgaudio.SessionID) agentaudio.SessionSnapshot {
	t.Helper()
	for _, s := range snaps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no snapshot for session %q among %d snapshots", id, len(snaps))
	return agentaudio.SessionSnapshot{}
}

// TestInterruptSuspendsAndResumesRealSession proves interrupt end to end
// against the real Engine: a "show" session actually reaches Playing on
// real GStreamer state, an "announcement" session with mix policy
// Interrupt actually pauses it (real Engine.Pause, not a gain change),
// and stopping the announcement actually resumes it (real Engine.Start
// re-anchoring position) — never a session that merely LOOKS resumed
// because its pre-interrupt position was left untouched.
func TestInterruptSuspendsAndResumesRealSession(t *testing.T) {
	dir := t.TempDir()

	// The engine is built first: New calls gst.Init(), and gst.ParseLaunch
	// inside generateWAV needs the plugin registry it loads — this order
	// matters, unlike this package's other real-engine tests, which never
	// generate a fixture before their first Engine.
	e := newManagerTestEngine(t, dir)
	m := agentaudio.NewManager(e, agentaudio.NewFileSessionStore(dir), dir, agentaudio.RealDecoder{}, time.Now, nil)

	showPath := filepath.Join(dir, "show.wav")
	generateWAV(t, showPath, 5.0)
	annPath := filepath.Join(dir, "ann.wav")
	generateWAV(t, annPath, 3.0)
	ctx := context.Background()

	showRef := pkgaudio.MediaRef{AssetID: "show", ContentHash: contentHashOf(t, showPath), RuntimeFilename: "show.wav"}
	const showID = pkgaudio.SessionID("show")
	if r := m.Apply(ctx, showID, "show-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Media:      pkgaudio.SetField(showRef),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show apply: unexpectedly refused: %+v", r)
	}
	if r := m.Start(ctx, showID, "show-start", 2); r.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("show start: outcome = %+v, want started", r)
	}

	// Let the real pipeline genuinely play for a bit before the
	// announcement arrives, so there is a real, non-zero pre-interrupt
	// position to prove the resume re-anchors from.
	time.Sleep(400 * time.Millisecond)

	preInterrupt := findSnapshot(t, m.Snapshot(ctx), showID)
	if preInterrupt.State != pkgaudio.StatePlaying || !preInterrupt.PositionKnown {
		t.Fatalf("show before interrupt: state=%q positionKnown=%v, want playing with a known position", preInterrupt.State, preInterrupt.PositionKnown)
	}
	if preInterrupt.Position <= 0 {
		t.Fatalf("show before interrupt: position = %v, want > 0 (real playback must have advanced)", preInterrupt.Position)
	}

	annRef := pkgaudio.MediaRef{AssetID: "ann", ContentHash: contentHashOf(t, annPath), RuntimeFilename: "ann.wav"}
	const annID = pkgaudio.SessionID("ann")
	if r := m.Apply(ctx, annID, "ann-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleAnnouncement),
		Media:      pkgaudio.SetField(annRef),
		MixPolicy:  pkgaudio.SetField(pkgaudio.MixPolicyInterrupt),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("ann apply: unexpectedly refused: %+v", r)
	}
	if r := m.Start(ctx, annID, "ann-start", 2); r.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("ann start: outcome = %+v, want started", r)
	}

	// Manager.Start runs interruptLowerPriority synchronously before
	// returning, so by now the real engine handle behind "show" must
	// already be paused. Ann's own apply/prepare/decode-probe cost real
	// wall time BEFORE it started (and so before show was paused), so the
	// right pre-suspend baseline is this snapshot, never preInterrupt.
	interrupted := findSnapshot(t, m.Snapshot(ctx), showID)
	if interrupted.State != pkgaudio.StatePaused || !interrupted.PositionKnown {
		t.Fatalf("show while ann is playing: state=%q positionKnown=%v, want paused (interrupted) with a known position — real Engine.Pause", interrupted.State, interrupted.PositionKnown)
	}
	if interrupted.Position < preInterrupt.Position {
		t.Fatalf("show position at interrupt = %v, want >= the earlier position %v (real time passed in between)", interrupted.Position, preInterrupt.Position)
	}

	if r := m.Stop(ctx, annID, "ann-stop", 3); r.Outcome == pkgaudio.OutcomeFailed {
		t.Fatalf("ann stop: outcome = %+v", r)
	}

	// Manager.Stop runs restoreInterrupted synchronously before
	// returning, so "show" must already be resumed.
	resumed := findSnapshot(t, m.Snapshot(ctx), showID)
	if resumed.State != pkgaudio.StatePlaying {
		t.Fatalf("show after ann stopped: state = %q, want playing (resumed) — real Engine.Resume", resumed.State)
	}
	if !resumed.PositionKnown {
		t.Fatal("show after resume: position not known — must be freshly re-anchored, not left stale")
	}
	// The resumed position must be close to where it was suspended
	// (interrupted.Position), never jumped forward by the wall time spent
	// interrupted (essentially none here, since nothing sleeps between
	// the interrupted check and Stop): a discontinuity re-anchors, it
	// does not extrapolate.
	if resumed.Position < interrupted.Position || resumed.Position > interrupted.Position+time.Second {
		t.Fatalf("show position after resume = %v, want close to the suspended position %v (re-anchored, not extrapolated)", resumed.Position, interrupted.Position)
	}

	time.Sleep(400 * time.Millisecond)
	later := findSnapshot(t, m.Snapshot(ctx), showID)
	if later.State != pkgaudio.StatePlaying || !later.PositionKnown || later.Position <= resumed.Position {
		t.Fatalf("show after further playback: state=%q positionKnown=%v position=%v (was %v at resume), want genuinely still advancing", later.State, later.PositionKnown, later.Position, resumed.Position)
	}
}

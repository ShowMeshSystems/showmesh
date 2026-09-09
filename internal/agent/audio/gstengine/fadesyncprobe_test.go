//go:build cgo

package gstengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

func newFadingBranch(t *testing.T, handle string, fadeDuration time.Duration) (*Engine, *branch) {
	t.Helper()
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 5)
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, agentaudio.EngineHandle(handle), mediaRef(wav), 5*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, agentaudio.EngineHandle(handle), 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, handle, 100*time.Millisecond, 5*time.Second)

	b, err := e.branchFor(agentaudio.EngineHandle(handle))
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}
	if _, err := e.Fade(ctx, agentaudio.EngineHandle(handle), pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: fadeDuration, TargetGain: 0}); err != nil {
		t.Fatalf("Fade: %v", err)
	}
	return e, b
}

// TestFadeSyncProbeTracksSyncedBufferPTS proves installFadeSyncProbe
// keeps fadeSyncedPos advancing as real buffers flow through volume,
// rather than leaving it pinned at the fade's own start position for the
// fade's whole duration.
func TestFadeSyncProbeTracksSyncedBufferPTS(t *testing.T) {
	e, b := newFadingBranch(t, "fsp1", 2*time.Second)

	b.mu.Lock()
	probeID := b.fadeSyncProbeID
	initial := b.fadeSyncedPos
	b.mu.Unlock()
	if probeID == 0 {
		t.Fatalf("fadeSyncProbeID = 0 immediately after startFade, want a nonzero probe id")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		advanced := b.fadeSyncedPos > initial
		b.mu.Unlock()
		if advanced {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	b.mu.Lock()
	final := b.fadeSyncedPos
	b.mu.Unlock()
	if final <= initial {
		t.Fatalf("fadeSyncedPos never advanced past its start value %s after 2s of real playback", initial)
	}

	_ = e.Release(context.Background(), "fsp1")
}

// TestRemoveFadeSyncProbeIsIdempotentAndStopsTracking proves
// removeFadeSyncProbe is safe to call more than once, clears
// fadeSyncProbeID, and genuinely detaches the probe: fadeSyncedPos must
// stop advancing even though the branch keeps playing and pushing real
// buffers through volume afterward.
func TestRemoveFadeSyncProbeIsIdempotentAndStopsTracking(t *testing.T) {
	e, b := newFadingBranch(t, "fsp2", 2*time.Second)

	// Let at least one buffer land before removing, or "frozen" below
	// would trivially hold with nothing yet having moved.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		got := b.fadeSyncedPos > b.fadeStartPos
		b.mu.Unlock()
		if got {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	b.removeFadeSyncProbe()
	b.mu.Lock()
	if b.fadeSyncProbeID != 0 {
		t.Fatalf("fadeSyncProbeID = %d after removeFadeSyncProbe, want 0", b.fadeSyncProbeID)
	}
	frozen := b.fadeSyncedPos
	b.mu.Unlock()

	b.removeFadeSyncProbe() // must not panic or error on an already-removed probe

	time.Sleep(500 * time.Millisecond)
	b.mu.Lock()
	after := b.fadeSyncedPos
	b.mu.Unlock()
	if after != frozen {
		t.Fatalf("fadeSyncedPos moved from %s to %s after removeFadeSyncProbe, want it frozen", frozen, after)
	}

	_ = e.Release(context.Background(), "fsp2")
}

// TestStartFadeSupersedesPriorProbe proves a second Fade dispatched
// while an earlier one is still active -- Engine.Fade calls startFade
// directly with no intervening cancelFade -- does not leave the first
// fade's probe attached alongside the new one. If it did, a single
// removeFadeSyncProbe call would stop only the second probe and
// fadeSyncedPos would keep moving from the orphaned first one.
func TestStartFadeSupersedesPriorProbe(t *testing.T) {
	e, b := newFadingBranch(t, "fsp3", 5*time.Second)

	b.mu.Lock()
	firstProbeID := b.fadeSyncProbeID
	b.mu.Unlock()
	if firstProbeID == 0 {
		t.Fatalf("fadeSyncProbeID = 0 after the first Fade, want a nonzero probe id")
	}

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	if _, err := e.Fade(ctx, "fsp3", pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: 5 * time.Second, TargetGain: 0.2}); err != nil {
		t.Fatalf("second Fade: %v", err)
	}

	b.mu.Lock()
	secondProbeID := b.fadeSyncProbeID
	b.mu.Unlock()
	if secondProbeID == 0 {
		t.Fatalf("fadeSyncProbeID = 0 after the second Fade, want a nonzero probe id")
	}

	b.removeFadeSyncProbe() // exactly one call: nothing should be left attached
	b.mu.Lock()
	frozen := b.fadeSyncedPos
	b.mu.Unlock()

	time.Sleep(500 * time.Millisecond)
	b.mu.Lock()
	after := b.fadeSyncedPos
	b.mu.Unlock()
	if after != frozen {
		t.Fatalf("fadeSyncedPos moved from %s to %s after one removeFadeSyncProbe call following a superseding Fade, want it frozen -- a second, orphaned probe is still attached", frozen, after)
	}

	_ = e.Release(context.Background(), "fsp3")
}

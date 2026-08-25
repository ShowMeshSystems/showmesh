//go:build cgo

package gstengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestStartWithPositionOnPausedBranchWithActiveSiblingLandsAtPosition is
// SM-143's real-pipeline scenario: a positioned Start (seek then play)
// issued on a branch that is currently paused (blocked, not released)
// while a sibling branch actively feeds the same channel mixers. A
// buffer-level reproduction against this package's real GStreamer
// pipeline (see startunblockorder_test.go's commit message and PR for
// the measurement) found no run in which the reported position actually
// drifted from the named target: seekTo re-freezes the frozen bookmark
// to the target unconditionally while the branch is still paused, and
// this environment's flushing seeks reliably discarded whatever sat
// parked at the flow block before it could reach the mixer. What the
// real pipeline could not exercise reliably is guarded deterministically
// instead, by startunblockorder_test.go: Start must not unblock this
// branch's flow before its own seek has actually landed, exactly as
// Resume already does, rather than depending on that race resolving the
// same way every time. This test stays as the runtime regression guard
// for the position contract itself.
func TestStartWithPositionOnPausedBranchWithActiveSiblingLandsAtPosition(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wavSibling := filepath.Join(dir, "sibling.wav")
	wavPaused := filepath.Join(dir, "paused.wav")
	generateWAV(t, wavSibling, 12)
	generateWAV(t, wavPaused, 12)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "sib", mediaRef(wavSibling), 12*time.Second); err != nil {
		t.Fatalf("Load sib: %v", err)
	}
	if _, err := e.Load(ctx, "pb", mediaRef(wavPaused), 12*time.Second); err != nil {
		t.Fatalf("Load pb: %v", err)
	}
	if _, err := e.Start(ctx, "sib", 0); err != nil {
		t.Fatalf("Start sib: %v", err)
	}
	if _, err := e.Start(ctx, "pb", 0); err != nil {
		t.Fatalf("Start pb: %v", err)
	}

	waitForPosition(t, e, "pb", 300*time.Millisecond, 5*time.Second)

	pauseObs, err := e.Pause(ctx, "pb")
	if err != nil {
		t.Fatalf("Pause pb: %v", err)
	}
	if pauseObs.State != pkgaudio.StatePaused {
		t.Fatalf("after Pause: state = %q, want paused", pauseObs.State)
	}
	pausedAt := pauseObs.Position

	// Hold well past queueMaxSizeTime, and let the sibling (and the
	// mixer keep-alive pads) keep advancing the shared pipeline's
	// running time, before the positioned Start below.
	const hold = 3 * time.Second
	time.Sleep(hold)
	waitForPosition(t, e, "sib", hold-500*time.Millisecond, 5*time.Second)

	// A target far enough from pausedAt that landing near pausedAt
	// instead is unambiguous, and well inside the 12s fixture.
	target := pausedAt + 6*time.Second

	startObs, err := e.Start(ctx, "pb", target)
	if err != nil {
		t.Fatalf("Start pb with position: %v", err)
	}
	t.Logf("Start(pb, %s) on a paused branch (paused at %s, held %s with sib active): observed position=%s state=%s",
		target, pausedAt, hold, startObs.Position, startObs.State)
	if startObs.State != pkgaudio.StatePlaying {
		t.Fatalf("after positioned Start on a paused branch: state = %q, want playing", startObs.State)
	}

	const tolerance = 700 * time.Millisecond
	if startObs.Position < target-tolerance || startObs.Position > target+tolerance {
		t.Fatalf("WRONG POSITION: positioned Start on a paused branch with an active sibling requested %s but the returned observation reported %s (paused at %s before this call) -- re-anchored to a stale value instead of the named position",
			target, startObs.Position, pausedAt)
	}

	// The position must also hold up a moment later, not just in the
	// instant the call returned.
	time.Sleep(300 * time.Millisecond)
	obs, err := e.Observe(ctx, "pb")
	if err != nil {
		t.Fatalf("Observe pb: %v", err)
	}
	if obs.Position < target-tolerance || obs.Position > target+2*time.Second {
		t.Fatalf("pb position %s settled far from the Start target %s", obs.Position, target)
	}

	_ = e.Release(context.Background(), "sib")
	_ = e.Release(context.Background(), "pb")
}

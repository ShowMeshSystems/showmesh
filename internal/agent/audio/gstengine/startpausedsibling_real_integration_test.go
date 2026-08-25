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
// a non-regression guard, not this defect's acceptance evidence: it
// passes both before and after the ordering fix in Start, since the
// reported position is protected by seekTo's own frozen-bookmark
// bookkeeping regardless of unblockFlow's ordering (see
// TestStartLeavesFlowBlockedWhenItsOwnSeekTimesOut in
// startunblockorder_test.go for the actual runtime proof of the
// ordering). It keeps a real-pipeline check on the position contract
// itself -- a positioned Start (seek then play) on a branch that is
// currently paused, while a sibling branch actively feeds the same
// channel mixers, must land and hold at the named target.
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

	startedAt := time.Now()
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
	// instant the call returned: it must have kept advancing in real
	// time from target, neither stuck nor jumped. The settle window is
	// symmetric around target plus the real elapsed time since Start
	// returned, not an arbitrary asymmetric range, so a branch that
	// jumped seconds ahead (or never advanced at all) fails this just as
	// surely as one that landed short.
	const settle = 300 * time.Millisecond
	time.Sleep(settle)
	obs, err := e.Observe(ctx, "pb")
	if err != nil {
		t.Fatalf("Observe pb: %v", err)
	}
	expected := target + time.Since(startedAt)
	if obs.Position < expected-tolerance || obs.Position > expected+tolerance {
		t.Fatalf("pb position %s is %s away from %s (target %s plus %s of real elapsed time since Start), want within %s",
			obs.Position, obs.Position-expected, expected, target, time.Since(startedAt), tolerance)
	}

	_ = e.Release(context.Background(), "sib")
	_ = e.Release(context.Background(), "pb")
}

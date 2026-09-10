//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestTimedOutSeekNeverMarksTheOldBranchAnchorUnknown is the swap-era
// counterpart of what this test used to prove: once Start has joined a
// branch to the mixer, Seek never flush-seeks it in place — it builds a
// replacement and prepares that off the mixer instead (see
// swapToPosition). An exhausted ctx here fails preparing the
// replacement; the original branch was never issued a seek that could
// still land late, so — unlike TestTimedOutSeekLeavesSegmentStartStale's
// genuine in-place seek timeout — it must not be marked errAnchorUnknown,
// and the handle must still resolve to it, still playable by a later
// call.
func TestTimedOutSeekNeverMarksTheOldBranchAnchorUnknown(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const handle = "seekstale2"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 6*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, handle, 200*time.Millisecond, 5*time.Second)

	before, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor before the timed-out Seek: %v", err)
	}

	tctx, tcancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer tcancel()
	if _, err := e.Seek(tctx, handle, 4*time.Second); err == nil {
		t.Fatalf("Seek with an exhausted deadline: err = nil, want a timeout")
	}

	after, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor after the timed-out Seek: %v", err)
	}
	if after != before {
		t.Fatalf("handle now resolves to a different branch after a timed-out Seek swap: " +
			"a failed swap must leave the original branch in place")
	}

	if _, err := e.Start(ctx, handle, 1*time.Second); errors.Is(err, errAnchorUnknown) {
		t.Fatalf("Start on the branch a timed-out Seek left in place returned errAnchorUnknown; " +
			"a timed-out swap never flush-seeked it, so its anchoring was never put in question")
	} else if err != nil {
		t.Fatalf("Start on the branch a timed-out Seek left in place: %v", err)
	}
	if _, err := e.Seek(ctx, handle, 2*time.Second); errors.Is(err, errAnchorUnknown) {
		t.Fatalf("Seek after an earlier timed-out Seek returned errAnchorUnknown; the original branch was never flush-seeked")
	} else if err != nil {
		t.Fatalf("Seek after an earlier timed-out Seek: %v", err)
	}
	if _, err := e.Resume(ctx, handle); err != nil {
		// Resume only applies to a paused or stopped branch; the sequence
		// above left this one playing, so refusal here is a session-level
		// concern this package does not enforce -- only errAnchorUnknown
		// specifically must never appear.
		if errors.Is(err, errAnchorUnknown) {
			t.Fatalf("Resume after an earlier timed-out Seek returned errAnchorUnknown; the original branch was never flush-seeked")
		}
	}
	fade := pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: 200 * time.Millisecond, TargetGain: 0}
	if _, err := e.Fade(ctx, handle, fade); errors.Is(err, errAnchorUnknown) {
		t.Fatalf("Fade after an earlier timed-out Seek returned errAnchorUnknown; the original branch was never flush-seeked")
	}

	_ = e.Release(context.Background(), handle)
}

// TestExplicitlyCanceledSeekAlsoNeverMarksTheOldBranchAnchorUnknown is
// TestTimedOutSeekNeverMarksTheOldBranchAnchorUnknown's counterpart for
// an already-canceled ctx (not merely an elapsed deadline): the swap it
// drives fails the exact same way preparing its replacement, never
// touching the original branch's own segment or anchoring.
func TestExplicitlyCanceledSeekAlsoNeverMarksTheOldBranchAnchorUnknown(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const handle = "seekcanceled1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 6*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, handle, 200*time.Millisecond, 5*time.Second)

	cctx, ccancel := context.WithCancel(context.Background())
	ccancel() // already canceled before Seek ever runs
	_, err := e.Seek(cctx, handle, 4*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Seek with an already-canceled ctx: err = %v, want context.Canceled in its chain", err)
	}

	if _, err := e.Start(ctx, handle, 1*time.Second); errors.Is(err, errAnchorUnknown) {
		t.Fatalf("Start after an explicitly canceled Seek returned errAnchorUnknown; the original branch was never flush-seeked")
	} else if err != nil {
		t.Fatalf("Start after an explicitly canceled Seek: %v", err)
	}

	_ = e.Release(context.Background(), handle)
}

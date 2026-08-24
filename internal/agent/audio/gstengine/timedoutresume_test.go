//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// resumeTimeoutProbe returns the ctx deadline trial i of a Resume-timeout
// test should use. Resume's own bounded call, the flushing seek back to
// the frozen position, measured noticeably faster than Start's
// first-ever transition on this environment (the branch already warmed
// up its clock/basetime negotiation during the original Start), so startTimeoutProbe's single
// 200us value never lands inside Resume's much narrower window; sweeping
// 1-20us per trial reliably catches it instead of chasing one magic
// number.
func resumeTimeoutProbe(trial int) time.Duration {
	return time.Duration(1+trial) * time.Microsecond
}

// TestTimedOutResumeUnfreezesOnlyAfterItsSeekSucceeds is Resume's
// counterpart to TestTimedOutStartUnfreezesOnlyAfterReachingPlaying: a
// Resume whose own ctx deadline fires before its flushing seek returns
// must not have already switched Position reporting to a live query
// while the session's own state stays non-playing. Resume halts flow
// with a pad block rather than an element state change, so the seek is
// the step that can be abandoned here; the ordering rule under test is
// the same one either way. Each trial
// pauses a fresh branch (Resume's precondition) before the timed-out
// Resume, sweeping resumeTimeoutProbe across the trial range to land
// inside the window.
func TestTimedOutResumeUnfreezesOnlyAfterItsSeekSucceeds(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 4)

	// See the identical shrink in TestTimedOutStartUnfreezesOnlyAfterReachingPlaying.
	withShrunkTeardownTimeout(t, time.Second)

	const trials = 3
	var buggy, timedOut int
	for i := 0; i < trials; i++ {
		func() {
			// Each trial gets its own engine and its own ctx: a
			// timed-out Resume abandons a goroutine that keeps driving
			// its branch toward PLAYING, and Release on that branch
			// defers rather than removing it, so running many trials on
			// one shared engine accumulates leaked branches still
			// pushing into the shared channel mixers. Deliberately not
			// calling Close per trial: Close now always attempts the
			// pipeline's own bin-level SetState(NULL) even past a
			// deferred branch, which recurses into that very branch's
			// still-abandoned elements and can itself collide with the
			// abandoned goroutine this trial just created; newTestEngine
			// reaps each trial's engine once, batched at the end of the
			// test, via its own t.Cleanup. A per-trial ctx still matters
			// on its own: a single ctx shared across every trial would
			// let an earlier trial's slow teardown eat into a later
			// trial's own Load budget instead of its own.
			e := newTestEngine(t)

			ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
			defer cancel()

			handle := agentaudio.EngineHandle(fmt.Sprintf("resumefreeze-%d", i))
			if _, err := e.Load(ctx, handle, mediaRef(wav), 4*time.Second); err != nil {
				t.Fatalf("Load (trial %d): %v", i, err)
			}
			if _, err := e.Start(ctx, handle, 0); err != nil {
				t.Fatalf("Start (trial %d): %v", i, err)
			}
			if _, err := e.Pause(ctx, handle); err != nil {
				t.Fatalf("Pause (trial %d): %v", i, err)
			}
			b, err := e.branchFor(handle)
			if err != nil {
				t.Fatalf("branchFor (trial %d): %v", i, err)
			}

			probe := resumeTimeoutProbe(i)
			tctx, tcancel := context.WithTimeout(context.Background(), probe)
			_, resumeErr := e.Resume(tctx, handle)
			tcancel()

			b.mu.Lock()
			frozen, state := b.frozen, b.state
			b.mu.Unlock()

			if errors.Is(resumeErr, context.DeadlineExceeded) {
				timedOut++
				t.Logf("trial %d (probe=%s): resumeErr=%v frozen=%v state=%v", i, probe, resumeErr, frozen, state)
				if state != pkgaudio.StatePlaying && !frozen {
					// The bug: Position now reads a live query
					// (frozen=false) even though the branch never
					// actually resumed after Pause.
					buggy++
				}
			}
		}()
	}

	if timedOut == 0 {
		t.Skipf("no trial's Resume actually timed out sweeping 1-%dus deadlines on this environment; cannot exercise the ordering bug's window here", trials)
	}
	if buggy > 0 {
		t.Fatalf("%d/%d timed-out trials unfroze before resuming (%d/%d trials timed out at all): "+
			"Resume's unfreeze must not run until its flushing seek actually succeeds", buggy, timedOut, timedOut, trials)
	}
	t.Logf("%d/%d trials timed out and none unfroze early", timedOut, trials)
}

// TestTimedOutResumeMarksAnchorUnknown proves the second half of the
// Resume fix: Resume's flushing seek is issued before its own ctx
// deadline can fire, and an abandoned seek may still land arbitrarily
// late with no way to know when, so the branch must be marked
// errAnchorUnknown rather than left silently reanchored.
func TestTimedOutResumeMarksAnchorUnknown(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 4)

	withShrunkTeardownTimeout(t, time.Second)

	const trials = 3
	var caught int
	for i := 0; i < trials; i++ {
		func() {
			// One engine and one ctx per trial, deliberately not closed
			// per trial; see the identical note in
			// TestTimedOutResumeUnfreezesOnlyAfterItsSeekSucceeds.
			e := newTestEngine(t)

			ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
			defer cancel()

			handle := agentaudio.EngineHandle(fmt.Sprintf("resumeanchor-%d", i))
			if _, err := e.Load(ctx, handle, mediaRef(wav), 4*time.Second); err != nil {
				t.Fatalf("Load (trial %d): %v", i, err)
			}
			if _, err := e.Start(ctx, handle, 0); err != nil {
				t.Fatalf("Start (trial %d): %v", i, err)
			}
			if _, err := e.Pause(ctx, handle); err != nil {
				t.Fatalf("Pause (trial %d): %v", i, err)
			}

			tctx, tcancel := context.WithTimeout(context.Background(), resumeTimeoutProbe(i))
			_, resumeErr := e.Resume(tctx, handle)
			tcancel()

			if errors.Is(resumeErr, context.DeadlineExceeded) {
				caught++
				if _, err := e.Seek(ctx, handle, 1*time.Second); !errors.Is(err, errAnchorUnknown) {
					t.Fatalf("trial %d: Seek on a branch left by a timed-out Resume: err = %v, want errAnchorUnknown in its chain", i, err)
				}
			}
		}()
	}

	if caught == 0 {
		t.Skipf("no trial's Resume actually timed out sweeping 1-%dus deadlines on this environment; cannot exercise this window here", trials)
	}
	t.Logf("exercised the anchorUnknown path on %d/%d trials", caught, trials)
}

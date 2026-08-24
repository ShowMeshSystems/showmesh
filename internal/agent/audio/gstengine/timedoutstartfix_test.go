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
)

// TestTeardownWaitsForAbandonedStartTransition is a dynamic check that a
// Release issued immediately after a timed-out Start does not report
// success while that Start's abandoned goroutine is still marked as
// possibly touching this branch's elements. It uses startTimeoutProbe,
// the same deadline calibrated in timedoutstart_test.go to reliably land
// inside setElementsState's real transition rather than before or after
// it, and repeats across fresh branches until it has actually caught the
// window at least once; a run that never lands in the window proves
// nothing about the guard. See TestTeardownGuardsAgainstEveryAbandonedStateChange
// in timedoutstart_test.go for the unconditional, source-level proof
// this repository already uses elsewhere (closeorder_test.go) for
// exactly this class of hazard: GStreamer's own internal locking makes a
// true crash impossible to force onto the vulnerable window from Go on
// any environment available here.
func TestTeardownWaitsForAbandonedStartTransition(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 4)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const trials = 20
	var caughtPending int
	for i := 0; i < trials; i++ {
		handle := agentaudio.EngineHandle(fmt.Sprintf("startteardown-%d", i))
		if _, err := e.Load(ctx, handle, mediaRef(wav), 4*time.Second); err != nil {
			t.Fatalf("Load (trial %d): %v", i, err)
		}
		b, err := e.branchFor(handle)
		if err != nil {
			t.Fatalf("branchFor (trial %d): %v", i, err)
		}

		tctx, tcancel := context.WithTimeout(context.Background(), startTimeoutProbe)
		_, startErr := e.Start(tctx, handle, 0)
		tcancel()
		if !errors.Is(startErr, context.DeadlineExceeded) {
			_ = e.Release(context.Background(), handle)
			continue
		}

		pendingAtReturn := b.pendingStateChanges.Load()
		releaseErr := e.Release(context.Background(), handle)

		if pendingAtReturn == 0 {
			continue
		}
		caughtPending++

		// The abandoned goroutine had not yet finished when Release was
		// called: teardown must have either waited it out (releaseErr
		// nil, pendingStateChanges now 0) or refused rather than race it
		// (releaseErr wraps errTeardownDeferredForRace), never proceeded
		// to touch elements while the count was still nonzero.
		if releaseErr != nil && !errors.Is(releaseErr, errTeardownDeferredForRace) {
			t.Fatalf("trial %d: Release after a timed-out Start returned %v, want nil or errTeardownDeferredForRace", i, releaseErr)
		}
		if releaseErr == nil && b.pendingStateChanges.Load() != 0 {
			t.Fatalf("trial %d: Release reported success while an abandoned state change was still marked pending", i)
		}
	}

	if caughtPending == 0 {
		t.Skipf("no trial's Release ran while pendingStateChanges was still nonzero on this environment; cannot exercise the guard's window here")
	}
	t.Logf("exercised the guard on %d/%d trials", caughtPending, trials)
}

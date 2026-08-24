//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// withShrunkTeardownTimeout temporarily replaces teardownTimeout so a
// test can force awaitNoElementRace's bound to expire without a real 5s
// wait, restoring the original value on cleanup.
func withShrunkTeardownTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := teardownTimeout
	teardownTimeout = d
	t.Cleanup(func() { teardownTimeout = orig })
}

// TestTeardownRefusesWhenAbandonedStateChangeNeverDrains proves teardown
// honors pendingStateChanges rather than only its own NULL transition's
// ctx: a branch with a state change permanently marked pending (standing
// in for one truly abandoned to a timed-out Start, per
// TestTeardownWaitsForAbandonedStartTransition) must have its teardown
// refuse with errTeardownDeferredForRace, bounded by teardownTimeout,
// rather than proceed to remove its elements.
func TestTeardownRefusesWhenAbandonedStateChangeNeverDrains(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	withShrunkTeardownTimeout(t, 200*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "deferredrace1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}

	// Stand in for a state change abandoned to a timed-out Start: this
	// count is never decremented, exactly as a real one would not be
	// until its background goroutine actually finished.
	b.pendingStateChanges.Add(1)

	start := time.Now()
	teardownErr := b.teardown(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(teardownErr, errTeardownDeferredForRace) {
		t.Fatalf("teardown with a permanently pending state change returned %v, want errTeardownDeferredForRace", teardownErr)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("teardown took %s to refuse, want it bounded close to the shrunk teardownTimeout", elapsed)
	}
}

// TestCloseReportsIncompleteWhenATeardownIsDeferred proves Close's error
// return actually surfaces a branch teardown that could not complete: it
// is what a caller like closeReplacedEngine needs to tell "torn down
// cleanly" apart from "teardown abandoned, elements or the device may
// still be live" before it reuses the device for a replacement engine.
func TestCloseReportsIncompleteWhenATeardownIsDeferred(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	withShrunkTeardownTimeout(t, 200*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "closeincomplete1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}
	b.pendingStateChanges.Add(1)

	closeErr := e.Close()
	if !errors.Is(closeErr, errCloseIncomplete) {
		t.Fatalf("Close with a deferred branch teardown returned %v, want errCloseIncomplete", closeErr)
	}
	if fault := pkgaudio.ClassifyFault(closeErr); fault != pkgaudio.FaultOther {
		t.Fatalf("Close's incomplete error classified as %q, want %q: it is this branch's teardown failing, not the shared pipeline itself", fault, pkgaudio.FaultOther)
	}

	// Idempotent: a second Close reports the same outcome rather than
	// recomputing or clearing it.
	if second := e.Close(); !errors.Is(second, errCloseIncomplete) {
		t.Fatalf("second Close = %v, want the same errCloseIncomplete the first call computed", second)
	}
}

// TestCloseReportsIncompleteAfterAPriorDeferredRelease proves Close does
// not report a false clean close when a branch's teardown already
// deferred via Release before Close ran: Release deletes the handle from
// e.handles as soon as it looks the branch up, so a fan-out that only
// walks e.handles would never see this branch and would wrongly report
// success. This is the path a real rebuild cares about: a wrong clean
// answer here means probing a device Release never actually released.
func TestCloseReportsIncompleteAfterAPriorDeferredRelease(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	withShrunkTeardownTimeout(t, 200*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "closeafterrelease1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}
	b.pendingStateChanges.Add(1)

	releaseErr := e.Release(context.Background(), handle)
	if !errors.Is(releaseErr, errTeardownDeferredForRace) {
		t.Fatalf("Release = %v, want errTeardownDeferredForRace", releaseErr)
	}

	// The handle is gone from e.handles now (Release deleted it before
	// calling teardown), so Close's own fan-out sees nothing to tear
	// down, which is exactly the case that must still surface as
	// incomplete.
	closeErr := e.Close()
	if !errors.Is(closeErr, errCloseIncomplete) {
		t.Fatalf("Close after a prior deferred Release returned %v, want errCloseIncomplete", closeErr)
	}

	// And Close must not have run SetState(NULL) on the shared pipeline:
	// that recurses into every child element, including this branch's
	// still-abandoned ones, which is exactly the concurrent SetState the
	// branch-level guard exists to prevent. The pipeline should still be
	// PLAYING, never having been asked to reach NULL.
	current, _, _ := e.pipeline.GetState(0)
	if current == gst.StateNull {
		t.Fatalf("pipeline reached NULL despite a deferred branch teardown: Close ran the guarded SetState(NULL) anyway")
	}
}

// TestRetriedTeardownReportsTheSameDeferralNotAFalseSuccess proves a
// second call to teardown after a deferred first attempt returns the
// same errTeardownDeferredForRace, not nil: a released-only guard set
// before the drain check ran used to let a retry believe teardown had
// succeeded while the branch's elements were still attached and its
// abandoned state change potentially still live.
func TestRetriedTeardownReportsTheSameDeferralNotAFalseSuccess(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	withShrunkTeardownTimeout(t, 200*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "retriedteardown1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}
	b.pendingStateChanges.Add(1)

	first := b.teardown(context.Background())
	if !errors.Is(first, errTeardownDeferredForRace) {
		t.Fatalf("first teardown = %v, want errTeardownDeferredForRace", first)
	}

	second := b.teardown(context.Background())
	if !errors.Is(second, errTeardownDeferredForRace) {
		t.Fatalf("second teardown = %v, want the same errTeardownDeferredForRace, not a false success", second)
	}

	bin, ok := e.pipeline.(gst.Bin)
	if !ok {
		t.Fatalf("engine pipeline is not a gst.Bin")
	}
	if bin.GetByName(b.filesrcName) == nil {
		t.Fatalf("branch element %q was removed from the pipeline despite both teardown attempts deferring", b.filesrcName)
	}
}

// TestTeardownWaitsThenSucceedsWhenPendingDrainsInTime proves
// awaitNoElementRace's wait is real, not a no-op: a state change marked
// pending that drains well inside teardownTimeout must let teardown
// actually wait for it and then proceed to remove the branch's elements,
// rather than either returning instantly or refusing regardless.
func TestTeardownWaitsThenSucceedsWhenPendingDrainsInTime(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "drainsintime1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}

	const drainAfter = 150 * time.Millisecond
	b.pendingStateChanges.Add(1)
	go func() {
		time.Sleep(drainAfter)
		b.pendingStateChanges.Add(-1)
	}()

	start := time.Now()
	teardownErr := b.teardown(context.Background())
	elapsed := time.Since(start)

	if teardownErr != nil {
		t.Fatalf("teardown with a state change that drains in time returned %v, want nil", teardownErr)
	}
	if elapsed < drainAfter-30*time.Millisecond {
		t.Fatalf("teardown returned after only %s, want it to have actually waited roughly %s for pendingStateChanges to drain", elapsed, drainAfter)
	}

	bin, ok := e.pipeline.(gst.Bin)
	if !ok {
		t.Fatalf("engine pipeline is not a gst.Bin")
	}
	if bin.GetByName(b.filesrcName) != nil {
		t.Fatalf("branch element %q still present after a teardown that reported success", b.filesrcName)
	}
}

// TestCloseReportsIncompleteWhenPipelineNullTimesOut isolates the
// pipeline-level half of errCloseIncomplete from the per-branch half:
// with zero loaded branches, the only way Close can report incomplete is
// the final pipeline transition to NULL itself timing out.
func TestCloseReportsIncompleteWhenPipelineNullTimesOut(t *testing.T) {
	e := newTestEngine(t)
	withShrunkTeardownTimeout(t, time.Nanosecond)

	closeErr := e.Close()
	if !errors.Is(closeErr, errCloseIncomplete) {
		t.Fatalf("Close with teardownTimeout shrunk to %s and no loaded branches returned %v, want errCloseIncomplete from the pipeline's own NULL transition timing out", time.Nanosecond, closeErr)
	}
}

// TestTeardownBoundedByCallerCtxNotOnlyTeardownTimeout proves teardown
// respects ctx's own deadline even when it is far shorter than
// teardownTimeout: a caller like Session.releaseEngineLocked holds a
// lock across Release and passes its own bounded ctx, and must not be
// stalled for the full teardownTimeout merely because a branch's state
// change never drains. teardownTimeout itself is left at its normal
// default here specifically to prove ctx alone is what bounds the wait.
func TestTeardownBoundedByCallerCtxNotOnlyTeardownTimeout(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "ctxbounddrain1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}
	// A permanently pending state change, standing in for one truly
	// abandoned: it never drains within this test's lifetime.
	b.pendingStateChanges.Add(1)

	const callerBudget = 50 * time.Millisecond
	shortCtx, shortCancel := context.WithTimeout(context.Background(), callerBudget)
	defer shortCancel()

	start := time.Now()
	teardownErr := b.teardown(shortCtx)
	elapsed := time.Since(start)

	if !errors.Is(teardownErr, errTeardownDeferredForRace) {
		t.Fatalf("teardown bounded by a %s caller ctx (teardownTimeout left at its %s default) returned %v, want errTeardownDeferredForRace", callerBudget, teardownTimeout, teardownErr)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("teardown(shortCtx) took %s, want it bounded by the caller's %s ctx rather than the much larger teardownTimeout default of %s", elapsed, callerBudget, teardownTimeout)
	}
}

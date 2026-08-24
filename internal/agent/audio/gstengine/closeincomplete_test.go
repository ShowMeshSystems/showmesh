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
	if !errors.Is(closeErr, pkgaudio.ErrEnginePipelineCrash) {
		t.Fatalf("Close's incomplete error does not classify as a pipeline crash: %v", closeErr)
	}

	// Idempotent: a second Close reports the same outcome rather than
	// recomputing or clearing it.
	if second := e.Close(); !errors.Is(second, errCloseIncomplete) {
		t.Fatalf("second Close = %v, want the same errCloseIncomplete the first call computed", second)
	}
}

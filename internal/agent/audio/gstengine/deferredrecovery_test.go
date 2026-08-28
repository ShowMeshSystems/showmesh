//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestDeferredTeardownStaysIndexedForBusAttribution proves a branch whose
// teardown deferred stays reachable via [Engine.branchForSource]: its
// elements are left in the pipeline (see errTeardownDeferredForRace), so
// a late bus error they produce must still attribute back to this
// branch — a harmless no-op via reportLoadError, since nothing reads
// loadErrCh once Release has returned — rather than falling through to
// [Engine.markBroken] and poisoning every other branch's next Load. An
// orphaned deferred branch's late bus error resolving to no branch is
// exactly what marks the whole engine broken forever.
func TestDeferredTeardownStaysIndexedForBusAttribution(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	withShrunkTeardownTimeout(t, 200*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "deferredindex1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}

	b.pendingStateChanges.Add(1)
	teardownErr := b.teardown(context.Background())
	if !errors.Is(teardownErr, errTeardownDeferredForRace) {
		t.Fatalf("teardown = %v, want errTeardownDeferredForRace", teardownErr)
	}

	found := e.branchForSource(b.filesrc)
	if found != b {
		t.Fatalf("branchForSource(b.filesrc) after a deferred teardown = %v, want the deferred branch %v (a late bus error must attribute here, not fall through to markBroken)", found, b)
	}

	if err := e.brokenErr(); err != nil {
		t.Fatalf("brokenErr() after a deferred teardown alone = %v, want nil", err)
	}
}

// TestDeferredTeardownSilencesTheBranch proves a branch whose teardown
// deferred is muted: doTeardown always calls unblockFlow ahead of the
// state change it may go on to abandon, so without this the branch would
// be left unblocked and possibly still PLAYING, holding its mixer
// request pads, and would keep sounding under whatever the session loads
// next.
func TestDeferredTeardownSilencesTheBranch(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	withShrunkTeardownTimeout(t, 200*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "deferredsilence1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	b.pendingStateChanges.Add(1)
	teardownErr := b.teardown(context.Background())
	if !errors.Is(teardownErr, errTeardownDeferredForRace) {
		t.Fatalf("teardown = %v, want errTeardownDeferredForRace", teardownErr)
	}

	if gain := b.currentGain(); gain != 0 {
		t.Fatalf("deferred branch gain = %v, want 0 (silenced so it cannot keep sounding under the next item)", gain)
	}
}

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

// TestDeferredTeardownSilencesTheBranchDespiteALiveFade proves the mute
// survives a GstController binding left over from an earlier Fade.
// startFade re-enables the binding on b.volume's "volume" property, and
// the property keeps being driven from it on every buffer for as long as
// the branch keeps flowing — which a deferred teardown never stops (see
// silenceDeferredBranch's own doc comment) — so a mute that only sets the
// property once, without disabling the binding, is silently overwritten
// by the very next buffer's evaluation of the fade's own curve.
func TestDeferredTeardownSilencesTheBranchDespiteALiveFade(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	withShrunkTeardownTimeout(t, 200*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "deferredsilencefade1"
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

	// A long, non-zero-target ramp still clearly in flight (nowhere near
	// its target and nowhere near elapsed) when teardown defers, so the
	// controller has a live curve to keep re-asserting against the mute.
	const fadeDuration = 5 * time.Second
	if _, err := e.Fade(ctx, handle, pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: fadeDuration, TargetGain: 0.8}); err != nil {
		t.Fatalf("Fade: %v", err)
	}

	b.pendingStateChanges.Add(1)
	teardownErr := b.teardown(context.Background())
	if !errors.Is(teardownErr, errTeardownDeferredForRace) {
		t.Fatalf("teardown = %v, want errTeardownDeferredForRace", teardownErr)
	}

	if gain := b.currentGain(); gain != 0 {
		t.Fatalf("deferred branch gain immediately after teardown deferred = %v, want 0", gain)
	}

	// The branch keeps flowing (unblockFlow already ran ahead of the
	// abandoned state change), so give the controller several buffers'
	// worth of time to have re-synced the property from its still-active
	// fade curve, which is exactly the failure this test exists to catch.
	time.Sleep(300 * time.Millisecond)

	if gain := b.currentGain(); gain != 0 {
		t.Fatalf("deferred branch gain %s after teardown deferred = %v, want still 0 (a live fade binding re-synced the volume property and un-muted the branch)", 300*time.Millisecond, gain)
	}
}

// TestDeferredTeardownStaysIndexedForEveryElement proves elementIndex
// resolves a bus error from any of a branch's eight elements, not only
// filesrc and decodebin: every one of them is a direct sibling in the
// shared pipeline (see branch's own doc comment), so any of them can be a
// deferred branch's bus error source. queue is exercised here as a
// representative of the six elements that are not filesrc/decodebin and
// have no internal children of their own for a decodebin-style error to
// walk up through.
func TestDeferredTeardownStaysIndexedForEveryElement(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	withShrunkTeardownTimeout(t, 200*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "deferredindexqueue1"
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

	for _, el := range []struct {
		name string
		el   gst.Element
	}{
		{"queue", b.queue},
		{"deinterleave", b.deinterleave},
		{"audioconvert", b.audioconvert},
		{"audioresample", b.audioresample},
		{"capsfilter", b.capsfilter},
		{"volume", b.volume},
	} {
		if found := e.branchForSource(el.el); found != b {
			t.Fatalf("branchForSource(b.%s) after a deferred teardown = %v, want the deferred branch %v (a bus error from this element must attribute here, not fall through to markBroken)", el.name, found, b)
		}
	}

	if err := e.brokenErr(); err != nil {
		t.Fatalf("brokenErr() after a deferred teardown alone = %v, want nil", err)
	}
}

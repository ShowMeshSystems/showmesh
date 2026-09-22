package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestWatchTickReleasesADuckWhenItsSessionResolvesViaAVanishedHandle
// proves watchTick releases a session's own duck/interrupt membership
// when checkStopCompletionLocked resolves it to Stopped on its own, not
// only when a session reaches StateCompleted naturally: every path that
// dispatches a Stop (or SilenceAll) already restores a duck itself once
// it attempts to stop the ducking session, but a session whose handle
// the engine has already forgotten by some OTHER means (an emergency
// stop's own sweep, here dispatched directly against the engine so no
// Stop is ever attempted against ann at all) resolves through watchTick
// alone, and before this fix that never restored anything it was
// ducking.
func TestWatchTickReleasesADuckWhenItsSessionResolvesViaAVanishedHandle(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	engine := NewFakeEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	bg, ok := m.get("bg")
	if !ok {
		t.Fatal("bg session missing")
	}
	bg.mu.Lock()
	bgHandle := bg.handle
	duckedBefore := len(bg.duckedByAll) > 0
	bg.mu.Unlock()
	if !duckedBefore {
		t.Fatal("precondition: bg must be ducked by ann before the test begins")
	}

	ann, ok := m.get("ann")
	if !ok {
		t.Fatal("ann session missing")
	}

	// Tears down every branch except bg's -- ann's included -- entirely
	// through the engine, bypassing every session-level path (Stop,
	// SilenceAll) that already restores a duck on its own.
	if _, err := engine.ReleaseAll(ctx, bgHandle); err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	// Simulates ann having been left in StateStopping by some earlier,
	// unrelated Stop failure (see stopsweepstrand_test.go's identical
	// precondition) -- the state checkStopCompletionLocked requires
	// before it will even look.
	ann.mu.Lock()
	ann.state = pkgaudio.StateStopping
	ann.mu.Unlock()

	m.watchTick(ctx)

	ann.mu.Lock()
	annState := ann.state
	ann.mu.Unlock()
	if annState != pkgaudio.StateStopped {
		t.Fatalf("ann state after watch tick = %q, want Stopped", annState)
	}

	bg.mu.Lock()
	stillDucked := len(bg.duckedByAll) > 0
	bg.mu.Unlock()
	if stillDucked {
		t.Fatal("bg is still ducked after ann resolved to Stopped via watchTick alone; want the duck released")
	}

	obs, err := m.engine.Observe(ctx, bgHandle)
	if err != nil {
		t.Fatalf("Observe bg: %v", err)
	}
	if !obs.FadeActive {
		t.Fatal("engine reports no restore fade in progress for bg after ann's duck released")
	}
}

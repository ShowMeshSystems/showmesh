package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestFakeEnginePauseFreezesFadeGain proves FakeEngine's Pause halts an
// in-progress fade's own ramp exactly as the real engine's genuine flow
// block does (see gstengine's blockFlow): wall-clock time spent paused
// must not count toward the ramp, so gain must sit at exactly whatever
// FakeEngine reported at Pause, unmoved by any further time passing
// while paused.
func TestFakeEnginePauseFreezesFadeGain(t *testing.T) {
	c := newClock(time.Now())
	e := NewFakeEngine(c.now)
	ctx := context.Background()
	const handle = EngineHandle("h1")

	if _, err := e.Load(ctx, handle, pkgaudio.MediaRef{}, 10*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.Fade(ctx, handle, pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: time.Second, TargetGain: 0}); err != nil {
		t.Fatalf("Fade: %v", err)
	}

	c.advance(300 * time.Millisecond)
	pauseObs, err := e.Pause(ctx, handle)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !pauseObs.FadeActive {
		t.Fatalf("immediately after Pause interrupted a fade short of its target: FadeActive = false, want true")
	}
	heldGain := pauseObs.Gain

	// Wall time keeps passing well past the fade's own 1s duration, but
	// the handle is paused throughout.
	c.advance(2 * time.Second)
	obs, err := e.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Gain != heldGain {
		t.Fatalf("gain moved while paused: was %v, now %v", heldGain, obs.Gain)
	}
	if !obs.FadeActive {
		t.Fatalf("FadeActive cleared while the fade was held by Pause short of its target")
	}
}

// TestFakeEngineResumeContinuesFadeFromHeldGain proves Resume lets a
// fade Pause held pick back up from exactly where it stopped, rather
// than restarting or skipping ahead by the held duration.
func TestFakeEngineResumeContinuesFadeFromHeldGain(t *testing.T) {
	c := newClock(time.Now())
	e := NewFakeEngine(c.now)
	ctx := context.Background()
	const handle = EngineHandle("h1")

	if _, err := e.Load(ctx, handle, pkgaudio.MediaRef{}, 10*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.Fade(ctx, handle, pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: time.Second, TargetGain: 0}); err != nil {
		t.Fatalf("Fade: %v", err)
	}

	c.advance(300 * time.Millisecond)
	pauseObs, err := e.Pause(ctx, handle)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	heldGain := pauseObs.Gain

	c.advance(5 * time.Second)
	if _, err := e.Resume(ctx, handle); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// Immediately after Resume, gain must not have jumped: no wall time
	// has passed since Resume itself.
	obs, err := e.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Gain != heldGain {
		t.Fatalf("gain jumped immediately on Resume: was %v, now %v", heldGain, obs.Gain)
	}

	// The remaining ~700ms of the ramp completes from here, not from the
	// full 1s duration and not instantly from the 5s that passed while
	// paused.
	c.advance(700 * time.Millisecond)
	obs, err = e.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.FadeActive {
		t.Fatalf("fade did not complete after its own remaining duration elapsed post-Resume")
	}
	if obs.Gain != 0 {
		t.Fatalf("gain after the fade completed = %v, want 0", obs.Gain)
	}
}

// TestFakeEngineStopFreezesFadeGain proves Stop, like Pause, halts an
// in-progress fade's ramp: the FakeEngine counterpart to gstengine's
// TestFadeHeldByStopStaysActive.
func TestFakeEngineStopFreezesFadeGain(t *testing.T) {
	c := newClock(time.Now())
	e := NewFakeEngine(c.now)
	ctx := context.Background()
	const handle = EngineHandle("h1")

	if _, err := e.Load(ctx, handle, pkgaudio.MediaRef{}, 10*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.Fade(ctx, handle, pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: time.Second, TargetGain: 0}); err != nil {
		t.Fatalf("Fade: %v", err)
	}

	c.advance(300 * time.Millisecond)
	stopObs, err := e.Stop(ctx, handle)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !stopObs.FadeActive {
		t.Fatalf("immediately after Stop interrupted a fade short of its target: FadeActive = false, want true")
	}
	heldGain := stopObs.Gain

	c.advance(3 * time.Second)
	obs, err := e.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Gain != heldGain {
		t.Fatalf("gain moved after Stop: was %v, now %v", heldGain, obs.Gain)
	}
	if !obs.FadeActive {
		t.Fatalf("FadeActive cleared after Stop held the fade short of its target")
	}
}

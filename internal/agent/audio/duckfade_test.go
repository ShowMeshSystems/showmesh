package audio

import (
	"context"
	"strings"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file is this fix's own acceptance suite: a duck must fade a session
// down to the duck depth and fade it back up on release, never step it
// instantly, and the fade machinery must handle the traps a naive fade
// dispatch would get wrong — two overlapping duckers, a release that is
// not the last one, a fade already in flight, mute/ceiling composition,
// and every path that reaches [Manager.removeDuckerLocked].

// namedDurationDecoder is [staticDecoder]'s sibling for a test that needs
// two sessions to complete at different times: it reports whichever
// duration in durations has its key as a substring of the probed path
// (writeTestAsset's own filename), falling back to def otherwise.
type namedDurationDecoder struct {
	durations map[string]time.Duration
	def       time.Duration
}

func (d namedDurationDecoder) Decode(_ context.Context, path string) DecodeResult {
	dur := d.def
	for name, v := range d.durations {
		if strings.Contains(path, name) {
			dur = v
			break
		}
	}
	return DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/wav", Decoded: true,
		Discoverer: DiscovererEvidence{Ran: true, Duration: dur},
	}
}

// TestDuckDownIsAFadeNotAStep is this fix's own baseline: duckOneLocked
// must dispatch [Engine.Fade], not step the engine straight to the duck
// depth via SetGain. Reading the engine's gain in the same instant the
// duck starts, with no clock advance, proves it: a step would already
// show the duck depth; a fade still shows a value on the way there.
func TestDuckDownIsAFadeNotAStep(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))

	bg, _ := m.get("bg")
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	obs, err := m.engine.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.FadeActive {
		t.Fatal("engine reports no fade in progress right after the duck started; the duck stepped the gain instead of fading it")
	}
	if obs.Gain == duckDepth(m) {
		t.Fatalf("engine gain reached the duck depth %v with the clock never advanced; want it still on the way there", duckDepth(m))
	}

	c.advance(300 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != duckDepth(m) {
		t.Fatalf("engine gain once the duck-down fade should have finished = %v, want the duck depth %v", got, duckDepth(m))
	}
}

// TestDuckRestoreIsAFadeNotAStep is the release-side mirror: once the
// last ducker stops, the bed must fade back up, not step there.
func TestDuckRestoreIsAFadeNotAStep(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)
	c.advance(300 * time.Millisecond) // let the duck-down fade finish first

	bg, _ := m.get("bg")
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()

	m.Stop(ctx, "ann", "inv-ann-stop", 3)

	obs, err := m.engine.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.FadeActive {
		t.Fatal("engine reports no fade in progress right after the duck released; the restore stepped the gain instead of fading it")
	}
	if obs.Gain == pkgaudio.Gain(0.9) {
		t.Fatalf("engine gain already reached the configured 0.9 with the clock never advanced; want it still on the way there")
	}

	c.advance(900 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.9) {
		t.Fatalf("engine gain once the restore fade should have finished = %v, want the configured 0.9", got)
	}
}

// TestSecondDuckerArrivingMidFadeDoesNotStackOrRestartTheFade is trap 1
// and trap 3 together: a second, overlapping ducker must not dispatch a
// second fade on top of the first (trap 1 — two duckers ducking the same
// target compose to the SAME depth, so there is nothing new to fade
// toward), and the ORIGINAL fade already in flight must keep running
// undisturbed rather than jump or restart (trap 3).
func TestSecondDuckerArrivingMidFadeDoesNotStackOrRestartTheFade(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))

	bg, _ := m.get("bg")
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()

	ann1Ref := writeTestAsset(t, m.assetDir, "ann1.wav", "asset-ann1", []byte("ann1"))
	startPlaying(t, m, ctx, "ann1", ann1Ref, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	// Partway through the 200ms default duck-down fade.
	c.advance(100 * time.Millisecond)
	midFadeGain := observedGain(t, m, ctx, handle)

	ann2Ref := writeTestAsset(t, m.assetDir, "ann2.wav", "asset-ann2", []byte("ann2"))
	startPlaying(t, m, ctx, "ann2", ann2Ref, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	// A second fade dispatched on top would jump or restart the ramp
	// (dispatching against the SAME current value is a no-op on a fake
	// engine, but a real one restarts its own internal ramp state, and
	// either way this is the value a caller reads right now): reading
	// the engine in the same instant ann2 arrived must show no jump.
	if got := observedGain(t, m, ctx, handle); got != midFadeGain {
		t.Fatalf("engine gain jumped from %v to %v the instant the second ducker arrived; want the original fade undisturbed", midFadeGain, got)
	}

	// The original fade must still be running, toward the SAME target,
	// and reach it on the SAME schedule — proving nothing restarted it.
	c.advance(100 * time.Millisecond) // 200ms total: the original fade's own duration
	if got := observedGain(t, m, ctx, handle); got != duckDepth(m) {
		t.Fatalf("engine gain once the original 200ms fade should have finished = %v, want the duck depth %v", got, duckDepth(m))
	}
}

// TestDuckReleaseWithAnotherDuckerStillActiveDoesNotStartRestoreFade is
// trap 2: releasing one of two overlapping duckers must not touch the
// engine at all while the other ducker still holds the session — only
// the LAST ducker leaving may start the restore fade.
func TestDuckReleaseWithAnotherDuckerStillActiveDoesNotStartRestoreFade(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))

	ann1Ref := writeTestAsset(t, m.assetDir, "ann1.wav", "asset-ann1", []byte("ann1"))
	startPlaying(t, m, ctx, "ann1", ann1Ref, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)
	ann2Ref := writeTestAsset(t, m.assetDir, "ann2.wav", "asset-ann2", []byte("ann2"))
	startPlaying(t, m, ctx, "ann2", ann2Ref, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)
	c.advance(300 * time.Millisecond) // let the duck-down fade settle

	bg, _ := m.get("bg")
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()

	m.Stop(ctx, "ann1", "inv-ann1-stop", 3)

	obs, err := m.engine.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.FadeActive {
		t.Fatal("engine reports a fade in progress after releasing one of two duckers; only the LAST ducker leaving may start a restore")
	}
	if obs.Gain != duckDepth(m) {
		t.Fatalf("engine gain after releasing one of two duckers = %v, want still the duck depth %v (ann2 is still playing)", obs.Gain, duckDepth(m))
	}
}

// TestDuckFadeRespectsMuteComposition is trap 4: a muted session's duck
// transition must never make it audible. effectiveGainLocked already
// composes mute ahead of duck (mute wins unconditionally); this proves
// the FADE this fix adds carries that composition through rather than
// fading toward the raw duck depth.
func TestDuckFadeRespectsMuteComposition(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))
	if r := m.Mute(ctx, "bg", "inv-bg-mute", 4); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}

	bg, _ := m.get("bg")
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	// Even mid-fade (no clock advance at all), a muted session must never
	// read above silence: the fade's own target is the muted gain, not
	// the duck depth.
	if got := observedGain(t, m, ctx, handle); got != mutedGain {
		t.Fatalf("engine gain the instant a duck starts on a muted session = %v, want %v (silence, not the duck depth)", got, mutedGain)
	}
	c.advance(300 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != mutedGain {
		t.Fatalf("engine gain once the duck-down fade should have finished, still muted = %v, want %v", got, mutedGain)
	}
}

// TestDuckFadeRespectsCeilingComposition is trap 4's other half: a
// session's ceiling must still bound the fade's own target, exactly as
// it already bounds every other path through effectiveGainLocked.
func TestDuckFadeRespectsCeilingComposition(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	const ceiling = pkgaudio.Ceiling(0.1) // below the default duck depth (0.25)
	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground),
		Media:      pkgaudio.SetField(bgRef),
		MixPolicy:  pkgaudio.SetField(pkgaudio.MixPolicyMix),
		Ceiling:    pkgaudio.SetField(ceiling),
	}
	if r := m.Apply(ctx, "bg", "bg-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}
	if r := m.Start(ctx, "bg", "bg-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start refused: %+v", r)
	}
	if r := m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain set refused: %+v", r)
	}

	bg, _ := m.get("bg")
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)
	c.advance(300 * time.Millisecond)

	// duckDepth(m) (0.25) exceeds the ceiling (0.1): a duck never RAISES
	// a session, and here the ceiling, not the duck depth, is the
	// binding constraint once the fade settles.
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(ceiling) {
		t.Fatalf("engine gain once the duck-down fade should have finished = %v, want the ceiling %v (below the duck depth %v)", got, ceiling, duckDepth(m))
	}
}

// TestDuckReleaseFadesOnClearAndNaturalCompletionToo is trap 5: Stop
// (covered by every other test in this file), Clear, and natural
// completion all reach [Manager.restoreDucked], and all three must fade,
// not step.
func TestDuckReleaseFadesOnClearAndNaturalCompletionToo(t *testing.T) {
	t.Run("clear", func(t *testing.T) {
		c := newClock(time.Now())
		m := newTestManager(t, c)
		ctx := context.Background()

		bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
		startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
		m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))

		annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
		startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)
		c.advance(300 * time.Millisecond)

		bg, _ := m.get("bg")
		bg.mu.Lock()
		handle := bg.handle
		bg.mu.Unlock()

		if r := m.Clear(ctx, "ann", "inv-ann-clear", 3); r.Outcome == pkgaudio.OutcomeRefused {
			t.Fatalf("clear unexpectedly refused: %+v", r)
		}

		obs, err := m.engine.Observe(ctx, handle)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if !obs.FadeActive {
			t.Fatal("engine reports no fade in progress right after clear released the duck; the restore stepped the gain instead of fading it")
		}

		c.advance(900 * time.Millisecond)
		if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.9) {
			t.Fatalf("engine gain once the restore fade should have finished = %v, want the configured 0.9", got)
		}
	})

	t.Run("natural completion", func(t *testing.T) {
		c := newClock(time.Now())
		dir := t.TempDir()
		decoder := namedDurationDecoder{
			durations: map[string]time.Duration{"ann": 300 * time.Millisecond},
			def:       30 * time.Second, // bg: long enough to outlast this test
		}
		m := NewManager(NewFakeEngine(c.now), NewFileSessionStore(dir), dir, decoder, c.now, nil)
		ctx := context.Background()

		bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
		startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
		m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))

		annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
		startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

		bg, _ := m.get("bg")
		bg.mu.Lock()
		handle := bg.handle
		bg.mu.Unlock()

		// Past ann's own 300ms duration (and the 200ms duck-down fade
		// that started under it): ann completes naturally, and watchTick
		// is what notices and releases its duck.
		c.advance(310 * time.Millisecond)
		m.watchTick(ctx)

		ann, _ := m.get("ann")
		ann.mu.Lock()
		annState := ann.state
		ann.mu.Unlock()
		if annState != pkgaudio.StateCompleted {
			t.Fatalf("ann state = %q, want Completed (this test's premise is that it finished on its own)", annState)
		}

		obs, err := m.engine.Observe(ctx, handle)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if !obs.FadeActive {
			t.Fatal("engine reports no fade in progress right after natural completion released the duck; the restore stepped the gain instead of fading it")
		}

		c.advance(900 * time.Millisecond)
		if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.9) {
			t.Fatalf("engine gain once the restore fade should have finished = %v, want the configured 0.9", got)
		}
	})
}

// TestDuckArrivingMidOperatorFadeResolvesTheSupersededInvocation is trap
// 3: [Session.checkFadeCompletionLocked]'s exactly-once resolution is
// keyed on s.fadeInvocation, and a duck's own fade dispatch overwrites
// that field to track its own (invocation-less) ramp instead of the
// operator fade it just replaced in the engine. Without a fix, an
// operator's audio.gain.fade invocation caught mid-flight by a duck is
// never resolved: its cached result stays whatever GainFade returned at
// dispatch time ("fade dispatched, not yet complete") forever, because
// the invocation that would need resolving has already been cleared by
// the time anything goes looking for it. A client that replays the same
// invocation to learn its outcome, the documented way to observe an
// async fade's eventual result, would see it reported as still in
// flight indefinitely, even long after the duck settled.
func TestDuckArrivingMidOperatorFadeResolvesTheSupersededInvocation(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	// availableFakeEngine (defined in mix_test.go): gateAvailability
	// passes outcomes through unchanged only when the engine reports
	// itself available, which the shipped FakeEngine never does. Needed
	// here so the cached result text this test inspects is the real
	// terminal outcome, not gateAvailability's generic unavailable-engine
	// substitute.
	m := NewManager(availableFakeEngine{NewFakeEngine(c.now)}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))

	bg, _ := m.get("bg")

	// An operator-invoked fade, slow enough that the duck below lands
	// while it is still ramping.
	fadeInvocation := pkgaudio.InvocationID("inv-op-fade")
	m.GainFade(ctx, "bg", fadeInvocation, 4, pkgaudio.FadeCurveLinear, 2*time.Second, pkgaudio.Gain(0.5))

	c.advance(200 * time.Millisecond) // partway through the 2s operator fade

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	// Let the duck's own (faster, default 200ms) fade run to completion
	// and give watchTick a chance to resolve whatever it is still
	// tracking.
	c.advance(300 * time.Millisecond)
	m.watchTick(ctx)

	bg.mu.Lock()
	result, ok := bg.executedResults[fadeInvocation]
	bg.mu.Unlock()
	if !ok {
		t.Fatal("operator fade invocation's cached result was never recorded at all")
	}
	if result.Outcome == pkgaudio.OutcomeGain && result.Reason == "fade dispatched, not yet complete" {
		t.Fatalf("operator fade invocation %q still reports %+v after being superseded by a duck; it must resolve to a terminal outcome instead of staying stuck at its initial dispatch result forever", fadeInvocation, result)
	}
	if result.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("superseded operator fade invocation outcome = %+v, want Unconfirmable", result)
	}
	if !strings.Contains(result.Reason, "duck") {
		t.Fatalf("superseded operator fade invocation reason = %q, want it to explain that a duck superseded it", result.Reason)
	}
}

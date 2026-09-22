package audio

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file is the owner ruling 2026-09-18 fix's own acceptance suite: a
// session with mix policy Duck must duck the bed and let the configured
// fade have its full duration BEFORE its own engine.Start ever runs, on
// an unscheduled start, a scheduled start, and Promote; a start that does
// not end Playing after ducking must restore the bed; two nodes given the
// same scheduled instant must apply engine.Start at the same offset from
// it; and every other session shape must start exactly as before.
//
// Every test here arms [Manager.duckFadeWait] instead of letting
// [Manager.waitDuckFade] run its real timer: the hook advances the SAME
// clock [FakeEngine]'s own gain and position interpolation reads, so
// "the fade's duration has elapsed" and "the bed's gain reached the duck
// depth" are the same, deterministic, non-sleeping assertion.

// newAvailableTestManager is [newTestManager] wrapped in
// [availableFakeEngine] (mix_test.go): this file's own tests check the
// returned Outcome for Started, which [Manager.gateAvailability] would
// otherwise downgrade to Unconfirmable against a plain [FakeEngine],
// whose Available() always reports false.
func newAvailableTestManager(t *testing.T, c *clock) *Manager {
	t.Helper()
	dir := t.TempDir()
	return NewManager(availableFakeEngine{NewFakeEngine(c.now)}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
}

// TestDuckThenStartUnscheduledWaitsForTheBedBeforeEngineStart is this
// fix's own baseline: an unscheduled Start for a Duck session must not
// call engine.Start until the bed's own duck fade has had its configured
// duration.
func TestDuckThenStartUnscheduledWaitsForTheBedBeforeEngineStart(t *testing.T) {
	c := newClock(time.Now())
	m := newAvailableTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))
	bg, _ := m.get("bg")
	bg.mu.Lock()
	bgHandle := bg.handle
	bg.mu.Unlock()

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	if r := m.Apply(ctx, "ann", "ann-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleAnnouncement),
		Media:      pkgaudio.SetField(annRef),
		MixPolicy:  pkgaudio.SetField(pkgaudio.MixPolicyDuck),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}
	ann, _ := m.get("ann")

	var hookCalled bool
	m.duckFadeWait = func(_ context.Context, d time.Duration) error {
		hookCalled = true
		obs, err := m.engine.Observe(ctx, bgHandle)
		if err != nil {
			t.Fatalf("Observe bg: %v", err)
		}
		if !obs.FadeActive || obs.Gain == duckDepth(m) {
			t.Fatalf("bed not mid-fade when the duck fade wait began: %+v", obs)
		}
		// Read ann's own handle field directly, with no lock: this hook runs
		// from inside Manager.start while ann.mu (== s.mu there, since this
		// call is Starting ann itself) is already held by this same
		// goroutine, so re-locking it here would self-deadlock. prepareLocked
		// (which mints the handle) has already run by the time this hook
		// fires, since the duck-then-start wait sits strictly after it in
		// [Manager.start]; reading it earlier would race a fresh, unique
		// [Session.engineHandleFor] mint.
		annHandle := ann.handle
		if annObs, err := m.engine.Observe(ctx, annHandle); err == nil && annObs.State == pkgaudio.StatePlaying {
			t.Fatal("announcement's own handle is already Playing before the duck fade wait completed")
		}
		c.advance(d)
		if got := observedGain(t, m, ctx, bgHandle); got != duckDepth(m) {
			t.Fatalf("bed gain once the duck fade wait completed = %v, want the duck depth %v", got, duckDepth(m))
		}
		return nil
	}

	out := m.Start(ctx, "ann", "ann-start", 2)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %+v, want started", out)
	}
	if !hookCalled {
		t.Fatal("the duck fade wait was never invoked; the announcement started without waiting for the bed to duck")
	}
}

// TestDuckThenStartScheduledAnchorsTimelineToTheActualStart proves the
// scheduled-start path: the duck fade wait happens strictly after the
// scheduled instant is reached and strictly before engine.Start, and the
// resulting timeline is anchored to the ACTUAL engine start (the
// scheduled instant plus the configured fade length), never the pre-fade
// instant — a session anchored to the pre-fade instant would report a
// false lateness of one fade length on every subsequent drift
// evaluation.
func TestDuckThenStartScheduledAnchorsTimelineToTheActualStart(t *testing.T) {
	c := newClock(time.Unix(1_700_000_000, 0))
	dir := t.TempDir()
	m := NewManager(availableFakeEngine{NewFakeEngine(c.now)}, NewFileSessionStore(dir), dir, staticDecoder{duration: time.Hour}, c.now, nil)
	media := newFakeClockSource(time.Unix(4_000_000_000, 0))
	media.setWallNow(c.now)
	m.SetClockSource(media)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	bg, _ := m.get("bg")
	bg.mu.Lock()
	bgHandle := bg.handle
	bg.mu.Unlock()

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	if r := m.Apply(ctx, "ann", "ann-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleAnnouncement),
		Media:      pkgaudio.SetField(annRef),
		MixPolicy:  pkgaudio.SetField(pkgaudio.MixPolicyDuck),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}

	fadeMs := m.SettingsSnapshot().DuckFadeDurationMs
	var hookCalled bool
	m.duckFadeWait = func(_ context.Context, d time.Duration) error {
		hookCalled = true
		obs, err := m.engine.Observe(ctx, bgHandle)
		if err != nil {
			t.Fatalf("Observe bg: %v", err)
		}
		if !obs.FadeActive {
			t.Fatal("bed has no fade in progress when the scheduled duck fade wait began")
		}
		c.advance(d)
		return nil
	}

	t0 := media.Now(ctx).Time.Add(40 * time.Millisecond)
	before := media.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- m.StartAt(ctx, "ann", "ann-start", 2, t0.UnixNano())
	}()
	media.waitForReads(t, before+1)
	media.advance(40 * time.Millisecond)

	var out pkgaudio.OutcomeResult
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StartAt never returned after the media clock reached T0")
	}
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt = %+v, want started", out)
	}
	if !hookCalled {
		t.Fatal("the duck fade wait was never invoked for the scheduled start")
	}

	ann, _ := m.get("ann")
	ann.mu.Lock()
	tl := ann.timeline
	ann.mu.Unlock()
	if tl == nil {
		t.Fatal("scheduled announcement has no timeline anchored")
	}
	want := t0.Add(time.Duration(fadeMs) * time.Millisecond)
	if !tl.t0.Equal(want) {
		t.Fatalf("timeline t0 = %v, want the scheduled instant plus the configured fade length (%v), never the pre-fade instant itself", tl.t0, want)
	}
}

// TestDuckThenStartPromoteWaitsForBedBeforeEngineStart is this fix's own
// second call site: [Manager.Promote] must duck-then-start exactly as
// [Manager.Start] does.
func TestDuckThenStartPromoteWaitsForBedBeforeEngineStart(t *testing.T) {
	c := newClock(time.Now())
	m := newAvailableTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))
	bg, _ := m.get("bg")
	bg.mu.Lock()
	bgHandle := bg.handle
	bg.mu.Unlock()

	const staging = pkgaudio.SessionID("staging")
	const show = pkgaudio.SessionID("show")
	ref := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	if r := m.Apply(ctx, staging, "stage-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage apply refused: %+v", r)
	}
	if r := m.Prepare(ctx, staging, "stage-prepare", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage prepare refused: %+v", r)
	}
	if r := m.Apply(ctx, show, "show-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleAnnouncement),
		Media:      pkgaudio.SetField(ref),
		MixPolicy:  pkgaudio.SetField(pkgaudio.MixPolicyDuck),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show apply refused: %+v", r)
	}

	var hookCalled bool
	m.duckFadeWait = func(_ context.Context, d time.Duration) error {
		hookCalled = true
		obs, err := m.engine.Observe(ctx, bgHandle)
		if err != nil {
			t.Fatalf("Observe bg: %v", err)
		}
		if !obs.FadeActive || obs.Gain == duckDepth(m) {
			t.Fatalf("bed not mid-fade when the promote's duck fade wait began: %+v", obs)
		}
		c.advance(d)
		return nil
	}

	res := m.Promote(ctx, staging, show, "show-start", 2)
	if res.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Promote = %+v, want started", res)
	}
	if !hookCalled {
		t.Fatal("the duck fade wait was never invoked; Promote started without waiting for the bed to duck")
	}
	if got := observedGain(t, m, ctx, bgHandle); got != duckDepth(m) {
		t.Fatalf("bed gain after Promote = %v, want the duck depth %v", got, duckDepth(m))
	}
}

// TestDuckThenStartFailedStartRestoresTheDuckedBed proves the recovery
// path this fix adds: a duck applied before a start that then fails must
// be released, exactly as an ended announcement's own Stop already
// releases it, so a failed announcement never leaves the bed held down.
func TestDuckThenStartFailedStartRestoresTheDuckedBed(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))
	bg, _ := m.get("bg")
	bg.mu.Lock()
	bgHandle := bg.handle
	bg.mu.Unlock()

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	if r := m.Apply(ctx, "ann", "ann-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleAnnouncement),
		Media:      pkgaudio.SetField(annRef),
		MixPolicy:  pkgaudio.SetField(pkgaudio.MixPolicyDuck),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}
	// Prepare ahead of Start so the handle exists to arm a one-shot
	// failure on, and Start's own auto-prepare (which would otherwise
	// consume that same armed failure) has nothing left to do.
	if r := m.Prepare(ctx, "ann", "ann-prepare", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("prepare refused: %+v", r)
	}
	ann, _ := m.get("ann")
	ann.mu.Lock()
	annHandle := ann.handle
	ann.mu.Unlock()

	fakeEngine, ok := m.engine.(*FakeEngine)
	if !ok {
		t.Fatalf("engine = %T, want *FakeEngine", m.engine)
	}
	fakeEngine.InjectFailure(annHandle, errors.New("engine start failed"))

	m.duckFadeWait = func(_ context.Context, d time.Duration) error {
		c.advance(d)
		return nil
	}

	out := m.Start(ctx, "ann", "ann-start", 3)
	if out.Outcome != pkgaudio.OutcomeFailed {
		t.Fatalf("Start = %+v, want failed", out)
	}

	obs, err := m.engine.Observe(ctx, bgHandle)
	if err != nil {
		t.Fatalf("Observe bg: %v", err)
	}
	if !obs.FadeActive {
		t.Fatal("bed has no restore fade in progress after a failed start; a failed announcement must not leave the bed held down")
	}

	c.advance(900 * time.Millisecond)
	if got := observedGain(t, m, ctx, bgHandle); got != pkgaudio.Gain(0.9) {
		t.Fatalf("bed gain once the restore fade should have finished = %v, want the configured 0.9", got)
	}
}

// TestDuckThenStartSameScheduledInstantProducesTheSameOffset proves the
// ordering requirement's whole point: two nodes given the same scheduled
// instant apply the same configured fade length, so engine.Start lands
// at the same offset from that instant on both, keeping them in step.
func TestDuckThenStartSameScheduledInstantProducesTheSameOffset(t *testing.T) {
	fadeMs := DefaultSettings.DuckFadeDurationMs
	var offsets [2]time.Duration

	for i := range offsets {
		c := newClock(time.Unix(1_700_000_000, 0))
		dir := t.TempDir()
		engine := NewFakeEngine(c.now)
		m := NewManager(availableFakeEngine{engine}, NewFileSessionStore(dir), dir, staticDecoder{duration: time.Hour}, c.now, nil)
		media := newFakeClockSource(time.Unix(4_000_000_000, 0))
		media.setWallNow(c.now)
		m.SetClockSource(media)
		ctx := context.Background()

		bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
		startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)

		annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
		if r := m.Apply(ctx, "ann", "ann-apply", 1, pkgaudio.ApplyRequest{
			SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleAnnouncement),
			Media:      pkgaudio.SetField(annRef),
			MixPolicy:  pkgaudio.SetField(pkgaudio.MixPolicyDuck),
		}); r.Outcome == pkgaudio.OutcomeRefused {
			t.Fatalf("apply refused: %+v", r)
		}

		var duckCalledAt time.Time
		m.duckFadeWait = func(_ context.Context, d time.Duration) error {
			duckCalledAt = c.now()
			c.advance(d)
			return nil
		}

		t0 := media.Now(ctx).Time.Add(50 * time.Millisecond)
		before := media.reads()
		done := make(chan pkgaudio.OutcomeResult, 1)
		go func() {
			done <- m.StartAt(ctx, "ann", "ann-start", 2, t0.UnixNano())
		}()
		media.waitForReads(t, before+1)
		media.advance(50 * time.Millisecond)

		var out pkgaudio.OutcomeResult
		select {
		case out = <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("StartAt never returned after the media clock reached T0")
		}
		if out.Outcome != pkgaudio.OutcomeStarted {
			t.Fatalf("StartAt = %+v, want started", out)
		}
		if duckCalledAt.IsZero() {
			t.Fatal("the duck fade wait was never invoked")
		}

		ann, _ := m.get("ann")
		ann.mu.Lock()
		handle := ann.handle
		ann.mu.Unlock()
		h, ok := engine.handles[handle]
		if !ok {
			t.Fatal("announcement has no loaded handle after starting")
		}
		offsets[i] = h.playStartedAt.Sub(duckCalledAt)
	}

	want := time.Duration(fadeMs) * time.Millisecond
	for i, got := range offsets {
		if got != want {
			t.Fatalf("node %d: engine.Start offset from the duck fade wait beginning = %v, want exactly the configured fade length %v", i, got, want)
		}
	}
}

// TestDuckThenStartLeavesInterruptAndNoDuckSessionsUnaffected is this
// fix's own negative space: a session whose mix policy is Interrupt, or
// unset entirely, must never invoke the duck-then-start wait at all and
// must start exactly as before.
func TestDuckThenStartLeavesInterruptAndNoDuckSessionsUnaffected(t *testing.T) {
	newGuardedManager := func(t *testing.T) (*Manager, context.Context) {
		t.Helper()
		c := newClock(time.Now())
		m := newAvailableTestManager(t, c)
		m.duckFadeWait = func(context.Context, time.Duration) error {
			t.Fatal("the duck fade wait must never run for a session whose mix policy is not duck")
			return nil
		}
		return m, context.Background()
	}

	t.Run("interrupt", func(t *testing.T) {
		m, ctx := newGuardedManager(t)
		bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
		startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
		annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
		startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyInterrupt)
	})

	t.Run("no mix policy", func(t *testing.T) {
		m, ctx := newGuardedManager(t)
		ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
		req := pkgaudio.ApplyRequest{
			SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
			Media:      pkgaudio.SetField(ref),
		}
		if r := m.Apply(ctx, "show", "show-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
			t.Fatalf("apply refused: %+v", r)
		}
		if r := m.Start(ctx, "show", "show-start", 2); r.Outcome != pkgaudio.OutcomeStarted {
			t.Fatalf("Start = %+v, want started", r)
		}
	})
}

package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// newTestManagerInDir is [newTestManager] with an explicit, reusable
// directory rather than a fresh t.TempDir() each call — the crash-
// recovery tests below need two Manager instances (a "before" and an
// "after restart") that see the same persisted store.
func newTestManagerInDir(dir string, c *clock) *Manager {
	return NewManager(NewFakeEngine(c.now), NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
}

// startPlaying gets id to Playing with the given source role and mix
// policy, via Apply then Start (Start auto-prepares).
func startPlaying(t *testing.T, m *Manager, ctx context.Context, id pkgaudio.SessionID, ref pkgaudio.MediaRef, role pkgaudio.SourceRole, policy pkgaudio.MixPolicy) {
	t.Helper()
	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(role),
		Media:      pkgaudio.SetField(ref),
		MixPolicy:  pkgaudio.SetField(policy),
	}
	if r := m.Apply(ctx, id, pkgaudio.InvocationID(string(id)+"-apply"), 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply %s: unexpectedly refused: %+v", id, r)
	}
	if r := m.Start(ctx, id, pkgaudio.InvocationID(string(id)+"-start"), 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start %s: unexpectedly refused: %+v", id, r)
	}
	s, ok := m.get(id)
	if !ok {
		t.Fatalf("session %s was not created", id)
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("session %s internal state = %q, want playing", id, state)
	}
}

// mutation target: Manager.Apply's "unsupported" mix policy refusal. Flip
// the equality check to always-false (or delete it) and this test starts
// passing on an accepted apply instead of a refusal. "unsupported" only
// ever appears in an adapter's own capability report; a session may never
// desire it, unlike duck/mix/interrupt, which are all real, requestable
// policies now.
func TestApplyRefusesUnsupportedMixPolicy(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	req := pkgaudio.ApplyRequest{MixPolicy: pkgaudio.SetField(pkgaudio.MixPolicyUnsupported)}

	r := m.Apply(ctx, "ann", "inv-1", 1, req)
	if r.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("outcome = %+v, want refused", r)
	}
	if r.Reason == "" {
		t.Fatal("refusal must carry a reason")
	}
}

// mutation target: Manager.Apply must accept all three real mix policies.
// This is the negative space of the refusal above: interrupt must reach
// desired state rather than being refused.
func TestApplyAcceptsInterruptMixPolicy(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	// availableFakeEngine, not newTestManager's FakeEngine: under a
	// permanently-unavailable engine every accepted Apply reads as
	// Unconfirmable too, which would make this test pass whether or not
	// MixPolicyInterrupt was actually accepted.
	m := NewManager(availableFakeEngine{NewFakeEngine(c.now)}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	req := pkgaudio.ApplyRequest{MixPolicy: pkgaudio.SetField(pkgaudio.MixPolicyInterrupt)}

	r := m.Apply(ctx, "ann", "inv-1", 1, req)
	if r.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("outcome = %+v, want Position (accepted)", r)
	}
}

// mutation target: setGainLocked's ApplyCeiling call. Removing the clamp
// (using requested directly) makes this test's ceiling assertion fail.
func TestGainSetClampsToCeiling(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	ceiling := pkgaudio.Ceiling(0.5)
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref), Ceiling: pkgaudio.SetField(ceiling)}
	m.Apply(ctx, id, "inv-apply", 1, req)
	m.Start(ctx, id, "inv-start", 2)

	m.GainSet(ctx, id, "inv-gain", 3, pkgaudio.Gain(1.0))

	s, _ := m.get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.desired.Gain == nil || *s.desired.Gain != pkgaudio.Gain(0.5) {
		t.Fatalf("desired gain = %v, want clamped to ceiling 0.5", s.desired.Gain)
	}
}

// Ceiling enforcement on the fade path: the fade's own TARGET is
// clamped before it is ever dispatched to the engine, not merely
// validated against it.
func TestGainFadeClampsTargetToCeiling(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	ceiling := pkgaudio.Ceiling(0.4)
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref), Ceiling: pkgaudio.SetField(ceiling)}
	m.Apply(ctx, id, "inv-apply", 1, req)
	m.Start(ctx, id, "inv-start", 2)

	m.GainFade(ctx, id, "inv-fade", 3, pkgaudio.FadeCurveLinear, time.Second, pkgaudio.Gain(1.0))

	s, _ := m.get(id)
	s.mu.Lock()
	fade := s.desired.Fade
	s.mu.Unlock()
	if fade == nil || fade.TargetGain != pkgaudio.Gain(0.4) {
		t.Fatalf("fade target = %v, want clamped to ceiling 0.4", fade)
	}
}

// mutation target: checkFadeCompletionLocked's obs.FadeActive check.
// Deleting that check (treating a fade as complete the instant it is
// dispatched) makes this test's mid-fade assertion fail: fadePending
// would already be false, and desired.Gain would already read the
// target, before the clock ever reaches the fade's duration.
func TestFadeCompletionIsObservedNotTimed(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	m.GainFade(ctx, id, "inv-fade", 3, pkgaudio.FadeCurveLinear, time.Second, pkgaudio.Gain(0))

	assertFadePending := func(want bool, label string) {
		t.Helper()
		s, _ := m.get(id)
		s.mu.Lock()
		pending := s.fadePending
		s.mu.Unlock()
		if pending != want {
			t.Fatalf("%s: fadePending = %v, want %v", label, pending, want)
		}
	}

	assertFadePending(true, "immediately after dispatch")

	// Halfway through the fade's own duration, a tick must not report
	// completion — this is the case a timed (duration-elapsed) shortcut
	// would get wrong.
	c.advance(500 * time.Millisecond)
	m.watchTick(ctx)
	assertFadePending(true, "halfway through the fade")

	// Past the duration, the engine's own gain has genuinely reached the
	// target and a tick observes that.
	c.advance(600 * time.Millisecond)
	m.watchTick(ctx)
	assertFadePending(false, "past the fade duration")

	s, _ := m.get(id)
	s.mu.Lock()
	gain := s.desired.Gain
	s.mu.Unlock()
	if gain == nil || *gain != pkgaudio.Gain(0) {
		t.Fatalf("desired gain after fade completion = %v, want 0", gain)
	}
}

// availableFakeEngine wraps [FakeEngine] and overrides only Available()
// to report true — every other method, including Fade/Observe's gain
// simulation, is [FakeEngine]'s own real behavior via method promotion.
// [FakeEngine] itself can already represent a fade reaching its target
// (that is what TestFadeCompletionIsObservedNotTimed proves); what it
// cannot do is ever report itself available, by design, so no test
// against the shipped FakeEngine can observe [Manager.gateAvailability]
// pass a value through. This type exists solely to prove the ungated
// completion outcome is computed correctly; it is never used outside
// this file's tests and must never be mistaken for a working engine.
type availableFakeEngine struct{ *FakeEngine }

func (availableFakeEngine) Available() (bool, string) { return true, "" }

// mutation target: checkFadeCompletionLocked's `obs.Gain == target`
// branch that produces OutcomeFadeComplete. This is the "declared in the
// vocabulary, produced by nothing" gap: OutcomeFadeComplete must become
// reachable the moment a backend reports the gain actually reached, not
// stay permanently unproduced.
func TestFadeCompletionOutcomeIsFadeCompleteWhenEngineReachesTarget(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	m := NewManager(availableFakeEngine{NewFakeEngine(c.now)}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	const fadeInvocation = pkgaudio.InvocationID("inv-fade")
	m.GainFade(ctx, id, fadeInvocation, 3, pkgaudio.FadeCurveLinear, time.Second, pkgaudio.Gain(0))

	c.advance(1100 * time.Millisecond)
	m.watchTick(ctx)

	s, _ := m.get(id)
	s.mu.Lock()
	result, ok := s.executedResults[fadeInvocation]
	s.mu.Unlock()
	if !ok {
		t.Fatal("fade invocation's cached result was never updated with a terminal outcome")
	}
	if result.Outcome != pkgaudio.OutcomeFadeComplete {
		t.Fatalf("outcome = %+v, want fade_complete", result)
	}
}

// observedGainLocked reads handle's current gain straight from the
// engine, the ground truth an assertion on s.desired.Gain alone cannot
// provide: desired.Gain is the CONFIGURED gain in this package's derived
// design and is never zeroed by mute or a duck, only the value the
// engine is actually driven to is.
func observedGain(t *testing.T, m *Manager, ctx context.Context, handle EngineHandle) pkgaudio.Gain {
	t.Helper()
	obs, err := m.engine.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	return obs.Gain
}

// mutation target: the derived-gain composition in setGainLocked,
// muteLocked, and unmuteLocked. The configured gain (desired.Gain) must
// survive being muted untouched: mute and duck are reasons to reduce
// the ENGINE's gain, neither writes to the configured value, while the
// engine independently reflects mutedGain while muted and the
// configured gain again once unmuted. A repeated mute is idempotent by
// construction now: there is no separate restore value for a second
// mute to corrupt.
func TestMuteIsIdempotentAndUnmuteRestoresOriginalGain(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)
	m.GainSet(ctx, id, "inv-gain", 3, pkgaudio.Gain(0.7))

	m.Mute(ctx, id, "inv-mute-1", 4)
	m.Mute(ctx, id, "inv-mute-2", 5) // idempotent: must not disturb the configured gain

	s, _ := m.get(id)
	s.mu.Lock()
	configured, handle := *s.desired.Gain, s.handle
	s.mu.Unlock()
	if configured != pkgaudio.Gain(0.7) {
		t.Fatalf("configured gain while muted = %v, want it untouched at 0.7", configured)
	}
	if got := observedGain(t, m, ctx, handle); got != mutedGain {
		t.Fatalf("engine gain while muted = %v, want %v", got, mutedGain)
	}

	m.Unmute(ctx, id, "inv-unmute", 6)
	s.mu.Lock()
	muted, configuredAfter := s.muted, *s.desired.Gain
	s.mu.Unlock()
	if muted {
		t.Fatal("session still reports muted after unmute")
	}
	if configuredAfter != pkgaudio.Gain(0.7) {
		t.Fatalf("configured gain after unmute = %v, want 0.7", configuredAfter)
	}
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.7) {
		t.Fatalf("engine gain after unmute = %v, want the restored 0.7", got)
	}
}

// mutation target: duckLowerPriority's priority comparison. A
// background session with a duck policy must NOT duck an active show
// session (background outranks nothing) — flipping the comparison
// direction makes this test's "show stays at unity" assertion fail.
func TestDuckOnlyAffectsLowerPriorityRoles(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	showRef := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", showRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyDuck)

	show, _ := m.get("show")
	show.mu.Lock()
	defer show.mu.Unlock()
	if len(show.duckedByAll) != 0 {
		t.Fatalf("show session was ducked by a lower-priority background session (duckedByAll=%v)", show.duckedByAll)
	}
}

// mutation target: Manager.duckLowerPriority/restoreDucked's interaction
// with an announcement's start/stop — this is the exact behavior Track
// F's F5 consumes: an announcement over background, mix policy duck,
// restores the background's exact prior gain on stop.
func TestAnnouncementDucksAndRestoresBackgroundOnStop(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.8))

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	bg, _ := m.get("bg")
	bg.mu.Lock()
	_, duckedByAnn := bg.duckedByAll["ann"]
	handle := bg.handle
	bg.mu.Unlock()
	if !duckedByAnn {
		t.Fatalf("bg after ann started: duckedByAll=%v, want ducked by ann", bg.duckedByAll)
	}
	// The duck-down is a fade, not a step: let it finish before reading
	// the engine's own settled gain.
	c.advance(300 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != duckDepth(m) {
		t.Fatalf("bg engine gain after ann started = %v, want the duck depth %v", got, duckDepth(m))
	}

	m.Stop(ctx, "ann", "inv-ann-stop", 3)

	bg.mu.Lock()
	duckedByAllAfter := bg.duckedByAll
	configured := *bg.desired.Gain
	bg.mu.Unlock()
	if len(duckedByAllAfter) != 0 {
		t.Fatalf("bg still shows duckedByAll=%v after the ducking session stopped", duckedByAllAfter)
	}
	if configured != pkgaudio.Gain(0.8) {
		t.Fatalf("bg configured gain after restore = %v, want the pre-duck 0.8", configured)
	}
	// The restore is also a fade: let it finish too.
	c.advance(900 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.8) {
		t.Fatalf("bg engine gain after restore = %v, want the pre-duck 0.8", got)
	}
}

// mutation target: removeDuckerLocked's own membership guard, called
// directly and repeatedly rather than through a caller that already
// gates on duckedByAll — both real callers happen to check membership
// before calling, so this is the one test that actually exercises the
// function's own idempotence rather than a caller's. Without the guard,
// a second removal call after the operator has already set a new gain
// would incorrectly stomp it back to a default.
func TestRemoveDuckerLockedIsIdempotentOnItsOwn(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	s, _ := m.get(id)
	s.mu.Lock()
	configured := pkgaudio.Gain(0.6)
	s.desired.Gain = &configured
	s.duckedByAll = map[pkgaudio.SessionID]struct{}{"ann": {}}
	m.removeDuckerLocked(ctx, s, "ann") // first removal: clears the set, applies the configured 0.6
	handle := s.handle
	s.mu.Unlock()

	// The restore this dispatches is a fade, not a step: let it finish
	// before reading the engine's own settled gain.
	c.advance(900 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != configured {
		t.Fatalf("engine gain after the first removal = %v, want the configured %v", got, configured)
	}

	// An operator gain.set arrives after the restore, driving the engine
	// directly: this test holds s.mu across both removeDuckerLocked
	// calls to isolate the membership guard itself, so it cannot go
	// through m.GainSet without deadlocking on that same lock.
	operatorGain := pkgaudio.Gain(0.3)
	if _, err := m.engine.SetGain(ctx, handle, operatorGain); err != nil {
		t.Fatalf("SetGain: %v", err)
	}

	s.mu.Lock()
	m.removeDuckerLocked(ctx, s, "ann") // must be a no-op: "ann" is already absent
	s.mu.Unlock()

	if got := observedGain(t, m, ctx, handle); got != operatorGain {
		t.Fatalf("engine gain after a second removeDuckerLocked call = %v, want the operator's %v untouched", got, operatorGain)
	}
}

// mutation target: removeDuckerLocked's membership guard —
// the entire exactly-once mechanism. Simulates a crash where the
// announcement's own Stop persisted (its SessionState is Stopped on
// disk) but the corresponding restore of the background session's gain
// never ran — the restore boundary this ruling names. A fresh Manager
// restoring from the same store must restore bg's gain exactly once.
func TestDuckRestoreExactlyOnce_CrashAfterDuckerStopped(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.6))

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	// Simulate the crash: persist ann as Stopped WITHOUT going through
	// Manager.Stop (which would itself call restoreDucked) — exactly the
	// state a crash between "ann's stop persisted" and "bg's restore
	// persisted" leaves on disk.
	ann, _ := m.get("ann")
	ann.mu.Lock()
	ann.state = pkgaudio.StateStopped
	_ = ann.persistLocked()
	ann.mu.Unlock()

	bg, _ := m.get("bg")
	bg.mu.Lock()
	if _, ok := bg.duckedByAll["ann"]; !ok {
		bg.mu.Unlock()
		t.Fatalf("precondition: bg should still be ducked pre-restart")
	}
	bg.mu.Unlock()

	// "Restart": a fresh Manager over the same store.
	m2 := newTestManagerInDir(dir, c)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	bg2, ok := m2.get("bg")
	if !ok {
		t.Fatal("bg session was not restored")
	}
	bg2.mu.Lock()
	duckedByAll := bg2.duckedByAll
	configured := bg2.desired.Gain
	handle := bg2.handle
	bg2.mu.Unlock()
	if len(duckedByAll) != 0 {
		t.Fatalf("bg2.duckedByAll = %v after restart, want restored (empty) since ann is gone", duckedByAll)
	}
	if configured == nil || *configured != pkgaudio.Gain(0.6) {
		t.Fatalf("bg2 configured gain after restart = %v, want the pre-duck 0.6", configured)
	}
	// The engine handle restoreOne re-prepared is fresh and defaults to
	// unity: it must have been driven to the restored configured gain,
	// not left there. That restore is a fade now, not a step: let it
	// finish before reading the engine's own settled gain.
	c.advance(900 * time.Millisecond)
	if got := observedGain(t, m2, ctx, handle); got != pkgaudio.Gain(0.6) {
		t.Fatalf("bg2 engine gain after restart = %v, want the pre-duck 0.6", got)
	}
}

// The other side of the same boundary: a crash while the ducking session
// is STILL legitimately playing must leave the ducked session ducked —
// restoring here would be the premature-restore failure this ruling
// exists to prevent (a stuck, or in this case a wrongly-UNstuck, duck).
func TestDuckRestoreExactlyOnce_CrashWhileDuckerStillPlaying(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.9))

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	// No mutation here: both sessions' persisted records reflect exactly
	// what a crash right now would leave — ann Playing, bg ducked.

	m2 := newTestManagerInDir(dir, c)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	bg2, ok := m2.get("bg")
	if !ok {
		t.Fatal("bg session was not restored")
	}
	bg2.mu.Lock()
	_, duckedByAnn := bg2.duckedByAll["ann"]
	configured := bg2.desired.Gain
	handle := bg2.handle
	bg2.mu.Unlock()
	if !duckedByAnn {
		t.Fatalf("bg2.duckedByAll = %v after restart, want still ducked by ann (ann is still playing)", bg2.duckedByAll)
	}
	if configured == nil || *configured != pkgaudio.Gain(0.9) {
		t.Fatalf("bg2 configured gain after restart = %v, want the pre-duck 0.9 untouched", configured)
	}
	// The freshly re-prepared handle defaults to unity: restoreOne must
	// have driven it to 0 immediately, since ann is still playing and bg
	// is still ducked, or a restart would momentarily (or, on a crash
	// looping before the next duck resolution, indefinitely) play the
	// bed over the announcement at unity.
	if got := observedGain(t, m2, ctx, handle); got != duckDepth(m2) {
		t.Fatalf("bg2 engine gain after restart = %v, want still ducked to %v", got, duckDepth(m2))
	}

	// Now stop ann for real, through the live path, and confirm bg
	// restores exactly once from here too.
	m2.Stop(ctx, "ann", "inv-ann-stop", 3)

	bg2.mu.Lock()
	duckedByAllAfter := bg2.duckedByAll
	configuredAfter := *bg2.desired.Gain
	bg2.mu.Unlock()
	if len(duckedByAllAfter) != 0 {
		t.Fatalf("bg2.duckedByAll = %v after ann finally stopped, want restored", duckedByAllAfter)
	}
	if configuredAfter != pkgaudio.Gain(0.9) {
		t.Fatalf("bg2 configured gain after ann finally stopped = %v, want restored 0.9", configuredAfter)
	}
	// The live restore is a fade, not a step: let it finish before
	// reading the engine's own settled gain.
	c.advance(900 * time.Millisecond)
	if got := observedGain(t, m2, ctx, handle); got != pkgaudio.Gain(0.9) {
		t.Fatalf("bg2 engine gain after ann finally stopped = %v, want restored 0.9", got)
	}
}

// TestRestartThenResumeRecoversAPausedSession verifies that a session
// persisted Paused, then restored after a restart, must actually be
// resumable — Manager.Resume must not refuse and the underlying engine
// call must not fail: restoreOne must drive a paused session past
// prepareLocked (engine handle Ready) all the way to Paused, or a
// later Resume's own Engine.Resume call fails against a handle that was
// never actually paused inside the engine.
func TestRestartThenResumeRecoversAPausedSession(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-a", []byte("content-a"))
	startPlaying(t, m, ctx, id, ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	c.advance(500 * time.Millisecond)

	if r := m.Pause(ctx, id, "inv-pause", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("pause unexpectedly refused: %+v", r)
	}
	s, _ := m.get(id)
	s.mu.Lock()
	if s.state != pkgaudio.StatePaused || s.bookmark == nil {
		s.mu.Unlock()
		t.Fatal("precondition: session should be paused with a bookmark before the restart")
	}
	s.mu.Unlock()

	// "Restart": a fresh Manager and a fresh Engine over the same store —
	// the engine has no memory of s.handle at all, matching a real process
	// restart, not just a resume against the same in-memory engine.
	m2 := newTestManagerInDir(dir, c)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	s2, ok := m2.get(id)
	if !ok {
		t.Fatal("session was not restored")
	}
	s2.mu.Lock()
	restoredState := s2.state
	s2.mu.Unlock()
	if restoredState != pkgaudio.StatePaused {
		t.Fatalf("restored state = %q, want paused", restoredState)
	}

	r := m2.Resume(ctx, id, "inv-resume", 4)
	if r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("resume after restart unexpectedly refused: %+v", r)
	}

	s2.mu.Lock()
	defer s2.mu.Unlock()
	if s2.state != pkgaudio.StatePlaying {
		t.Fatalf("state after resume = %q, want playing (Engine.Resume must have succeeded against a genuinely-paused handle)", s2.state)
	}
}

// TestTwoOverlappingDuckersBothMustReleaseBeforeGainRestores verifies
// that with two announcements ducking one background session,
// stopping the FIRST must not restore the background's gain while the
// SECOND is still playing: mix.go must track the full set of duckers,
// not a single duckedBy id, or a second duck silently no-ops (bg already
// looks ducked) and the first duck's stop restores bg's gain out from
// under the second.
func TestTwoOverlappingDuckersBothMustReleaseBeforeGainRestores(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.7))

	ann1Ref := writeTestAsset(t, m.assetDir, "ann1.wav", "asset-ann1", []byte("ann1"))
	startPlaying(t, m, ctx, "ann1", ann1Ref, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	ann2Ref := writeTestAsset(t, m.assetDir, "ann2.wav", "asset-ann2", []byte("ann2"))
	startPlaying(t, m, ctx, "ann2", ann2Ref, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	bg, _ := m.get("bg")
	bg.mu.Lock()
	_, byAnn1 := bg.duckedByAll["ann1"]
	_, byAnn2 := bg.duckedByAll["ann2"]
	handle := bg.handle
	bg.mu.Unlock()
	if !byAnn1 || !byAnn2 {
		t.Fatalf("bg after both announcements started: duckedByAll=%v, want ducked by both ann1 and ann2", bg.duckedByAll)
	}
	// Only the FIRST ducker's arrival dispatches a fade; let it finish
	// before reading the engine's own settled gain.
	c.advance(300 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != duckDepth(m) {
		t.Fatalf("bg engine gain after both announcements started = %v, want the duck depth %v", got, duckDepth(m))
	}

	// ann1 stops first: bg must stay ducked at 0 because ann2 is still
	// playing.
	m.Stop(ctx, "ann1", "inv-ann1-stop", 3)

	bg.mu.Lock()
	_, stillByAnn2 := bg.duckedByAll["ann2"]
	bg.mu.Unlock()
	if !stillByAnn2 {
		t.Fatalf("bg after ann1 stopped (ann2 still playing): duckedByAll=%v, want still ducked by ann2", bg.duckedByAll)
	}
	if got := observedGain(t, m, ctx, handle); got != duckDepth(m) {
		t.Fatalf("bg engine gain after ann1 stopped (ann2 still playing) = %v, want still the duck depth %v", got, duckDepth(m))
	}

	// ann2 stops second: only now must bg's original gain be restored.
	m.Stop(ctx, "ann2", "inv-ann2-stop", 3)

	bg.mu.Lock()
	duckedByAllAfter := bg.duckedByAll
	configured := *bg.desired.Gain
	bg.mu.Unlock()
	if len(duckedByAllAfter) != 0 {
		t.Fatalf("bg.duckedByAll = %v after both announcements stopped, want empty", duckedByAllAfter)
	}
	if configured != pkgaudio.Gain(0.7) {
		t.Fatalf("bg configured gain after both announcements stopped = %v, want restored 0.7", configured)
	}
	// The restore is a fade too: let it finish before the final check.
	c.advance(900 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.7) {
		t.Fatalf("bg engine gain after both announcements stopped = %v, want restored 0.7", got)
	}
}

// TestFadeSupervisionSurvivesRestart verifies that a fade dispatched
// and then interrupted by a crash must not lose its pending invocation
// or leave it permanently stuck reporting "not yet complete":
// fadePending/fadeInvocation/fadeState must be persisted, or a restored
// session comes back with fadePending=false regardless of what was
// actually in flight, and watchTick's checkFadeCompletionLocked — gated
// on fadePending — can never run for that invocation again.
func TestFadeSupervisionSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	const fadeInvocation = pkgaudio.InvocationID("inv-fade")
	m.GainFade(ctx, id, fadeInvocation, 3, pkgaudio.FadeCurveLinear, time.Second, pkgaudio.Gain(0))

	s, _ := m.get(id)
	s.mu.Lock()
	if !s.fadePending || s.fadeInvocation != fadeInvocation || s.fadeState != FadeStateInProgress {
		s.mu.Unlock()
		t.Fatalf("precondition: fade should be pending before the restart (pending=%v invocation=%q state=%q)", s.fadePending, s.fadeInvocation, s.fadeState)
	}
	s.mu.Unlock()

	// "Restart": a fresh Manager and engine over the same store, with no
	// intervening watchTick — the crash this finding names.
	m2 := newTestManagerInDir(dir, c)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	s2, ok := m2.get(id)
	if !ok {
		t.Fatal("session was not restored")
	}
	s2.mu.Lock()
	pending, invocation, state := s2.fadePending, s2.fadeInvocation, s2.fadeState
	s2.mu.Unlock()
	if !pending || invocation != fadeInvocation || state != FadeStateInProgress {
		t.Fatalf("fade supervision state lost across restart: pending=%v invocation=%q state=%q, want it preserved", pending, invocation, state)
	}

	// Now that fadePending survived, the normal watcher must still be
	// ABLE to resolve the invocation to a terminal outcome — proving the
	// persisted state is not just present but functional.
	m2.watchTick(ctx)
	s2.mu.Lock()
	defer s2.mu.Unlock()
	result, ok := s2.executedResults[fadeInvocation]
	if !ok {
		t.Fatal("fade invocation's cached result is missing after restart")
	}
	if result.Outcome == pkgaudio.OutcomeGain && result.Reason == "fade dispatched, not yet complete" {
		t.Fatalf("fade invocation's outcome is still the pre-restart dispatch outcome %+v, want it resolved by watchTick", result)
	}
	if s2.fadePending {
		t.Fatal("fadePending is still true after watchTick observed the fade is no longer active")
	}
}

// TestFadeSupervisionRestartDoesNotJumpGainUpward verifies the sharper
// half of the restart finding: a restored session's first supervision
// tick must never infer a fade's completion from a freshly loaded engine
// handle that was never actually driven through that fade. A pre-crash
// desired gain of 0.4 mid-fade toward 0 must not become the new handle's
// default unity gain once RestoreAll and a watchTick run.
func TestFadeSupervisionRestartDoesNotJumpGainUpward(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)
	m.GainSet(ctx, id, "inv-gain", 3, pkgaudio.Gain(0.4))
	m.GainFade(ctx, id, "inv-fade", 4, pkgaudio.FadeCurveLinear, time.Second, pkgaudio.Gain(0))

	// The fade's own target (0) becomes the configured gain immediately
	// on dispatch in this package's derived design. The pre-fade 0.4 is
	// what a virgin restored handle must never exceed, not what
	// desired.Gain itself still reads.
	const preFadeGain = pkgaudio.Gain(0.4)
	s, _ := m.get(id)
	s.mu.Lock()
	preCrashGain := *s.desired.Gain
	s.mu.Unlock()
	if preCrashGain != pkgaudio.Gain(0) {
		t.Fatalf("precondition: pre-crash configured gain = %v, want the fade's own target 0", preCrashGain)
	}

	// "Restart": a fresh Manager and engine over the same store, with no
	// intervening watchTick — the crash TestFadeSupervisionSurvivesRestart
	// also uses. The new engine handle this creates has never been given
	// the fade that was in flight when the store was last written.
	m2 := newTestManagerInDir(dir, c)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	m2.watchTick(ctx)

	s2, ok := m2.get(id)
	if !ok {
		t.Fatal("session was not restored")
	}
	s2.mu.Lock()
	configured, handle := s2.desired.Gain, s2.handle
	s2.mu.Unlock()
	if configured == nil {
		t.Fatal("configured gain is nil after restore")
	}
	if *configured > preFadeGain {
		t.Fatalf("configured gain after restart = %v, want it not to exceed the pre-fade gain %v (a virgin handle's default must never be read as this fade's outcome)", *configured, preFadeGain)
	}
	if got := observedGain(t, m2, ctx, handle); got > preFadeGain {
		t.Fatalf("engine gain after restart = %v, want it not to exceed the pre-fade gain %v (a virgin handle's default must never be read as this fade's outcome)", got, preFadeGain)
	}
}

// TestFadeCompletionToleratesFloatingPointDrift proves a fade whose
// engine-reported gain differs from its target only by floating-point
// error still reports fade_complete. Exact equality here would report a
// completed fade as unconfirmable forever on any real backend, and the
// fake stores exact values, so no fake-only test can reach this.
func TestFadeCompletionToleratesFloatingPointDrift(t *testing.T) {
	const target = pkgaudio.Gain(0.2)
	drifted := pkgaudio.Gain(0.3) - pkgaudio.Gain(0.1) // 0.19999999999999998
	if drifted == target {
		t.Skip("this platform's arithmetic produced an exact value; nothing to prove")
	}
	if !gainsEqual(drifted, target) {
		t.Fatalf("gainsEqual(%v, %v) = false, want true: a fade that reached its target within floating-point error must report complete", drifted, target)
	}
	if gainsEqual(pkgaudio.Gain(0.3), target) {
		t.Fatal("gainsEqual(0.3, 0.2) = true, want false: the tolerance must not accept a gain that genuinely missed its target")
	}
}

// TestFadeDispatchedAfterRestartResolvesNormally guards the restart guard
// itself: it is armed by a restore and must be disarmed by the next fade
// actually dispatched, or a fade issued between the restore and the first
// supervision tick is answered as one interrupted by a restart it was
// never part of.
func TestFadeDispatchedAfterRestartResolvesNormally(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)
	m.GainFade(ctx, id, "inv-fade", 3, pkgaudio.FadeCurveLinear, time.Second, pkgaudio.Gain(0))

	m2 := newTestManagerInDir(dir, c)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	m2.GainFade(ctx, id, "inv-fade-2", 4, pkgaudio.FadeCurveLinear, time.Second, pkgaudio.Gain(0.2))
	m2.watchTick(ctx)

	s, ok := m2.get(id)
	if !ok {
		t.Fatal("session was not restored")
	}
	s.mu.Lock()
	if !s.fadePending {
		s.mu.Unlock()
		t.Fatal("the fade dispatched after the restore was resolved by the restart guard rather than left in progress")
	}
	if s.fadeState != FadeStateInProgress {
		fadeState := s.fadeState
		s.mu.Unlock()
		t.Fatalf("fade state = %q, want %q", fadeState, FadeStateInProgress)
	}
	handle := s.handle
	s.mu.Unlock()

	// fadePending is this package's own bookkeeping; it would still read
	// true if the dispatcher skipped the engine.Fade call entirely. Only
	// the engine's own evidence proves the call actually reached it.
	obs, err := m2.engine.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.FadeActive {
		t.Fatal("engine reports no fade in progress; engine.Fade was never dispatched")
	}
}

// TestFadePendingResolvedWhenSessionCompletesNaturally verifies that a
// fade still pending when a single-item session reaches natural
// completion is resolved to a terminal outcome, not left stranded:
// [Session.advanceLocked]'s no-successor-item branch must clear
// fadePending itself, since nothing else ever will once the handle it
// would have polled is released.
func TestFadePendingResolvedWhenSessionCompletesNaturally(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c) // staticDecoder reports a 2s duration
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	const fadeInvocation = pkgaudio.InvocationID("inv-fade")
	if r := m.GainFade(ctx, id, fadeInvocation, 3, pkgaudio.FadeCurveLinear, 5*time.Second, pkgaudio.Gain(0)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("fade unexpectedly refused: %+v", r)
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	if !s.fadePending {
		s.mu.Unlock()
		t.Fatal("precondition: fade should be pending")
	}
	s.mu.Unlock()

	// The item's own 2s duration elapses well before the 5s fade would
	// have finished, so natural completion reaches advanceLocked while
	// the fade is still in progress.
	c.advance(3 * time.Second)
	m.watchTick(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StateCompleted {
		t.Fatalf("state = %q, want Completed", s.state)
	}
	if s.fadePending {
		t.Fatal("fadePending is still true after the session completed; it will never reach a terminal outcome")
	}
	result, ok := s.executedResults[fadeInvocation]
	if !ok {
		t.Fatal("the fade's own invocation has no recorded outcome")
	}
	if result.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("fade outcome after being stranded by completion = %+v, want Unconfirmable", result)
	}
}

// TestFadePendingResolvedByStop proves the ordinary fade-out-then-stop
// operation resolves the fade rather than leaving fadePending true
// forever: Stop releases the engine handle, and checkFadeCompletionLocked
// requires one (it bails on !handleLoaded), so a fade Stop interrupts has
// no other path to a terminal outcome.
func TestFadePendingResolvedByStop(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	const fadeInvocation = pkgaudio.InvocationID("inv-fade")
	if r := m.GainFade(ctx, id, fadeInvocation, 3, pkgaudio.FadeCurveLinear, 5*time.Second, pkgaudio.Gain(0)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("fade unexpectedly refused: %+v", r)
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	if !s.fadePending {
		s.mu.Unlock()
		t.Fatal("precondition: fade should be pending")
	}
	s.mu.Unlock()

	// The fade's own 5s duration has not elapsed: Stop interrupts it well
	// short of its target, the canonical end-of-cue path.
	c.advance(time.Second)
	if r := m.Stop(ctx, id, "inv-stop", 4); r.Outcome == pkgaudio.OutcomeFailed {
		t.Fatalf("stop: unexpectedly failed: %+v", r)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fadePending {
		t.Fatal("fadePending is still true after Stop interrupted it; it will never reach a terminal outcome")
	}
	result, ok := s.executedResults[fadeInvocation]
	if !ok {
		t.Fatal("the fade's own invocation has no recorded outcome")
	}
	if result.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("fade outcome after being stranded by Stop = %+v, want Unconfirmable", result)
	}
}

// TestFadePendingResolvedByClear proves Clear resolves a still-pending
// fade exactly as Stop does: the same hazard, since Clear also releases
// the engine handle checkFadeCompletionLocked requires.
func TestFadePendingResolvedByClear(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	const fadeInvocation = pkgaudio.InvocationID("inv-fade")
	if r := m.GainFade(ctx, id, fadeInvocation, 3, pkgaudio.FadeCurveLinear, 5*time.Second, pkgaudio.Gain(0)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("fade unexpectedly refused: %+v", r)
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}

	c.advance(time.Second)
	m.Clear(ctx, id, "inv-clear", 4)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fadePending {
		t.Fatal("fadePending is still true after Clear interrupted it; it will never reach a terminal outcome")
	}
	result, ok := s.executedResults[fadeInvocation]
	if !ok {
		t.Fatal("the fade's own invocation has no recorded outcome")
	}
	if result.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("fade outcome after being stranded by Clear = %+v, want Unconfirmable", result)
	}
}

// TestFadePendingResolvedByDeferredStopCompletion proves the other half
// of the stop path also resolves a stranded fade: a session left in
// StateStopping by a failed Engine.Stop, later re-resolved by
// checkStopCompletionLocked once engine evidence shows it actually
// stopped, see TestWatchTickResolvesStuckStoppingSession for that path
// on its own, must not leave fadePending true forever either.
func TestFadePendingResolvedByDeferredStopCompletion(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()

	const fadeInvocation = pkgaudio.InvocationID("inv-fade")
	if r := m.GainFade(ctx, id, fadeInvocation, 3, pkgaudio.FadeCurveLinear, 5*time.Second, pkgaudio.Gain(0)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("fade unexpectedly refused: %+v", r)
	}

	fake, ok := m.engine.(*FakeEngine)
	if !ok {
		t.Fatalf("test manager's engine is %T, want *FakeEngine", m.engine)
	}
	fake.InjectFailure(handle, pkgaudio.ErrEnginePipelineCrash)
	m.Stop(ctx, id, "inv-stop", 4)

	s.mu.Lock()
	if s.state != pkgaudio.StateStopping {
		s.mu.Unlock()
		t.Fatalf("precondition: session state = %q, want stopping", s.state)
	}
	if !s.fadePending {
		s.mu.Unlock()
		t.Fatal("precondition: fade should still be pending: the failed Stop must not have resolved it")
	}
	s.mu.Unlock()

	// The engine actually reaches Stopped on its own, out from under the
	// failed call above, exactly as TestWatchTickResolvesStuckStoppingSession
	// simulates.
	if _, err := fake.Stop(ctx, handle); err != nil {
		t.Fatalf("fake.Stop: %v", err)
	}

	m.watchTick(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fadePending {
		t.Fatal("fadePending is still true after the deferred stop resolved; it will never reach a terminal outcome")
	}
	result, ok := s.executedResults[fadeInvocation]
	if !ok {
		t.Fatal("the fade's own invocation has no recorded outcome")
	}
	if result.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("fade outcome after being stranded by a deferred stop = %+v, want Unconfirmable", result)
	}
}

// TestFadePendingResolvedByStopAfterEngineRebind proves Stop's own
// no-handle-loaded branch resolves a fade invalidateActiveSessions (an
// engine rebind after a route change, see RebindEngine) left pending: a
// route change clears handleLoaded directly, without going through
// checkFadeCompletionLocked or resolveFadePendingStrandedLocked itself,
// so the very next Stop is the only remaining chance to close it out.
func TestFadePendingResolvedByStopAfterEngineRebind(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	switchable := NewSwitchableEngine()
	first := NewFakeEngine(c.now)
	switchable.Set(first)

	m := NewManager(switchable, store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	const fadeInvocation = pkgaudio.InvocationID("inv-fade")
	if r := m.GainFade(ctx, id, fadeInvocation, 3, pkgaudio.FadeCurveLinear, 5*time.Second, pkgaudio.Gain(0)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("fade unexpectedly refused: %+v", r)
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	if !s.fadePending {
		s.mu.Unlock()
		t.Fatal("precondition: fade should be pending")
	}
	s.mu.Unlock()

	second := NewFakeEngine(c.now)
	m.RebindEngine(context.Background(), switchable, second, "test rebind")

	s.mu.Lock()
	if s.handleLoaded {
		s.mu.Unlock()
		t.Fatal("precondition: handle should no longer be loaded after RebindEngine")
	}
	if !s.fadePending {
		s.mu.Unlock()
		t.Fatal("precondition: fade should still be pending: RebindEngine does not resolve it on its own")
	}
	s.mu.Unlock()

	if r := m.Stop(ctx, id, "inv-stop", 4); r.Outcome == pkgaudio.OutcomeFailed {
		t.Fatalf("stop: unexpectedly failed: %+v", r)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fadePending {
		t.Fatal("fadePending is still true after Stop's no-handle-loaded branch ran; it will never reach a terminal outcome")
	}
	result, ok := s.executedResults[fadeInvocation]
	if !ok {
		t.Fatal("the fade's own invocation has no recorded outcome")
	}
	if result.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("fade outcome after being stranded by an engine rebind = %+v, want Unconfirmable", result)
	}
}

// muteDuckFixture starts a background session at a known configured gain
// and an announcement session that ducks it, returning both sessions so
// a test can drive mute/unmute and duck end/start in whatever order it
// needs. bg's configured gain is 0.8; its engine gain is the configured
// duck depth (ducked by ann) before this returns, while its configured gain stays 0.8
// throughout, since duck never writes that field in this package's
// derived design.
func muteDuckFixture(t *testing.T, m *Manager, ctx context.Context, c *clock) (bg, ann *Session) {
	t.Helper()
	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	if r := m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.8)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain set on bg unexpectedly refused: %+v", r)
	}

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	bg, _ = m.get("bg")
	ann, _ = m.get("ann")

	bg.mu.Lock()
	_, ducked := bg.duckedByAll["ann"]
	configured := *bg.desired.Gain
	handle := bg.handle
	bg.mu.Unlock()
	if !ducked || configured != pkgaudio.Gain(0.8) {
		t.Fatalf("precondition failed: bg after ann started = ducked %v configured %v, want ducked with configured gain untouched at 0.8", ducked, configured)
	}
	// The duck-down is a fade, not a step: let it finish before reading
	// the engine's own settled gain.
	c.advance(300 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != duckDepth(m) {
		t.Fatalf("precondition failed: bg engine gain after ann started = %v, want the duck depth %v", got, duckDepth(m))
	}
	return bg, ann
}

// Mute during a duck, unmute during the same duck, then end the duck.
// The engine must read the duck level while still ducked, whether or
// not mute is also active, and land on bg's configured gain (0.8) only
// once both mute and the duck have released.
func TestMuteThenUnmuteDuringDuckThenDuckEndsRestoresConfiguredGain(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bg, _ := muteDuckFixture(t, m, ctx, c)
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()

	if r := m.Mute(ctx, "bg", "inv-bg-mute", 4); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}
	if got := observedGain(t, m, ctx, handle); got != 0 {
		t.Fatalf("engine gain while muted and ducked = %v, want 0", got)
	}

	if r := m.Unmute(ctx, "bg", "inv-bg-unmute", 5); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("unmute unexpectedly refused: %+v", r)
	}
	if got := observedGain(t, m, ctx, handle); got != duckDepth(m) {
		t.Fatalf("engine gain after unmuting while still ducked = %v, want the duck level %v (the announcement is still playing)", got, duckDepth(m))
	}

	if r := m.Stop(ctx, "ann", "inv-ann-stop", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stop unexpectedly refused: %+v", r)
	}

	bg.mu.Lock()
	duckedByAll := bg.duckedByAll
	configured := *bg.desired.Gain
	bg.mu.Unlock()
	if len(duckedByAll) != 0 {
		t.Fatalf("bg still shows duckedByAll=%v after the ducking session stopped", duckedByAll)
	}
	if configured != pkgaudio.Gain(0.8) {
		t.Fatalf("bg configured gain once mute and duck have both released = %v, want 0.8", configured)
	}
	// The restore is a fade: let it finish before the final check.
	c.advance(900 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.8) {
		t.Fatalf("bg engine gain once mute and duck have both released = %v, want the configured 0.8", got)
	}
}

// Mute during a duck, end the duck while still muted, then unmute. The
// engine must stay silent while muted even after the duck it was also
// under has released, and reach bg's configured gain only once unmuted.
func TestMuteThenDuckEndsWhileMutedThenUnmuteRestoresConfiguredGain(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bg, _ := muteDuckFixture(t, m, ctx, c)
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()

	if r := m.Mute(ctx, "bg", "inv-bg-mute", 4); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}

	if r := m.Stop(ctx, "ann", "inv-ann-stop", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stop unexpectedly refused: %+v", r)
	}

	bg.mu.Lock()
	stillMuted := bg.muted
	bg.mu.Unlock()
	if !stillMuted {
		t.Fatal("bg reports unmuted after only the duck released; mute was never lifted")
	}
	if got := observedGain(t, m, ctx, handle); got != 0 {
		t.Fatalf("engine gain while still muted after the duck released = %v, want 0 (mute must not be bypassed by the duck restore)", got)
	}

	if r := m.Unmute(ctx, "bg", "inv-bg-unmute", 5); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("unmute unexpectedly refused: %+v", r)
	}

	bg.mu.Lock()
	configured := *bg.desired.Gain
	bg.mu.Unlock()
	if configured != pkgaudio.Gain(0.8) {
		t.Fatalf("bg configured gain once mute and duck have both released = %v, want 0.8", configured)
	}
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.8) {
		t.Fatalf("bg engine gain once mute and duck have both released = %v, want the configured 0.8", got)
	}
}

// Duck during a mute, end the duck, then unmute. Mirrors the previous
// case with mute applied first, exercising the duck side's own
// composition while the session is already muted.
func TestDuckDuringMuteThenDuckEndsThenUnmuteRestoresConfiguredGain(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	if r := m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.8)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain set on bg unexpectedly refused: %+v", r)
	}
	if r := m.Mute(ctx, "bg", "inv-bg-mute", 4); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	bg, _ := m.get("bg")
	bg.mu.Lock()
	_, ducked := bg.duckedByAll["ann"]
	handle := bg.handle
	bg.mu.Unlock()
	if !ducked {
		t.Fatal("precondition failed: bg was not ducked by ann while muted")
	}
	if got := observedGain(t, m, ctx, handle); got != 0 {
		t.Fatalf("engine gain while muted and ducked = %v, want 0", got)
	}

	if r := m.Stop(ctx, "ann", "inv-ann-stop", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stop unexpectedly refused: %+v", r)
	}
	if got := observedGain(t, m, ctx, handle); got != 0 {
		t.Fatalf("engine gain after the duck released while still muted = %v, want 0", got)
	}

	if r := m.Unmute(ctx, "bg", "inv-bg-unmute", 5); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("unmute unexpectedly refused: %+v", r)
	}

	bg.mu.Lock()
	configured := *bg.desired.Gain
	bg.mu.Unlock()
	if configured != pkgaudio.Gain(0.8) {
		t.Fatalf("bg configured gain once mute and duck have both released = %v, want 0.8", configured)
	}
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.8) {
		t.Fatalf("bg engine gain once mute and duck have both released = %v, want the configured 0.8", got)
	}
}

// Mute before a duck starts, then unmute while the duck is still active.
// Unmuting must return the ENGINE to the duck level, not to the
// configured gain: the announcement is still playing, and driving the
// engine to the configured gain unconditionally on unmute would blow
// straight past the duck to full volume.
func TestUnmuteWhileStillDuckedReturnsToDuckLevelNotConfiguredGain(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	if r := m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.8)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain set unexpectedly refused: %+v", r)
	}
	if r := m.Mute(ctx, "bg", "inv-bg-mute", 4); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	if r := m.Unmute(ctx, "bg", "inv-bg-unmute", 5); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("unmute unexpectedly refused: %+v", r)
	}

	bg, _ := m.get("bg")
	bg.mu.Lock()
	ducked := len(bg.duckedByAll) != 0
	handle := bg.handle
	bg.mu.Unlock()
	if !ducked {
		t.Fatal("precondition failed: bg is not ducked by ann")
	}
	if got := observedGain(t, m, ctx, handle); got != duckDepth(m) {
		t.Fatalf("engine gain after unmuting while still ducked = %v, want the duck level %v, not the configured gain (the announcement is still playing)", got, duckDepth(m))
	}
}

// mutation target: setGainLocked and startFadeLocked driving the engine
// straight from the requested value instead of through
// effectiveGainLocked's composition. A gain.set or a gain.fade landing
// while a session is muted or ducked must never reach the engine above
// the active suppression's own target: it may only change what the
// session comes back to once every suppression releases.
func TestGainSetWhileMutedDoesNotReachTheEngine(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	startPlaying(t, m, ctx, id, ref, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, id, "inv-gain-1", 3, pkgaudio.Gain(0.5))
	if r := m.Mute(ctx, id, "inv-mute", 4); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}

	s, _ := m.get(id)
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()

	if r := m.GainSet(ctx, id, "inv-gain-2", 5, pkgaudio.Gain(0.6)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain.set while muted unexpectedly refused: %+v", r)
	}
	if got := observedGain(t, m, ctx, handle); got != 0 {
		t.Fatalf("engine gain after gain.set while muted = %v, want 0 (a muted bed must stay silent)", got)
	}

	if r := m.Unmute(ctx, id, "inv-unmute", 6); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("unmute unexpectedly refused: %+v", r)
	}
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.6) {
		t.Fatalf("engine gain after unmute = %v, want the gain.set that landed while muted, 0.6", got)
	}
}

func TestGainSetWhileDuckedDoesNotReachTheEngine(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bg, _ := muteDuckFixture(t, m, ctx, c)
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()

	if r := m.GainSet(ctx, "bg", "inv-bg-gain-2", 5, pkgaudio.Gain(0.6)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain.set while ducked unexpectedly refused: %+v", r)
	}
	if got := observedGain(t, m, ctx, handle); got != duckDepth(m) {
		t.Fatalf("engine gain after gain.set while ducked = %v, want the duck depth %v (the bed must not jump back up mid-announcement)", got, duckDepth(m))
	}

	if r := m.Stop(ctx, "ann", "inv-ann-stop", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stop unexpectedly refused: %+v", r)
	}
	// The restore is a fade: let it finish before the final check.
	c.advance(900 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.6) {
		t.Fatalf("engine gain once the duck released = %v, want the gain.set that landed mid-duck, 0.6", got)
	}
}

// mutation target: startFadeLocked dispatching engine.Fade with the raw
// requested target instead of the current effective gain. A fade
// requested while muted must not ramp the engine up before the mute
// lifts, even once the clock advances past the fade's own duration.
func TestGainFadeWhileMutedDoesNotRampTheEngineUp(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	// availableFakeEngine, not newTestManager's FakeEngine: this test
	// checks the fade invocation's own ungated outcome, which
	// Manager.gateAvailability rewrites to unconfirmable against an
	// engine that never reports itself available.
	m := NewManager(availableFakeEngine{NewFakeEngine(c.now)}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	startPlaying(t, m, ctx, id, ref, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	if r := m.Mute(ctx, id, "inv-mute", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}

	s, _ := m.get(id)
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()

	if r := m.GainFade(ctx, id, "inv-fade", 4, pkgaudio.FadeCurveLinear, time.Second, pkgaudio.Gain(0.7)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain.fade while muted unexpectedly refused: %+v", r)
	}

	c.advance(1100 * time.Millisecond)
	m.watchTick(ctx)

	if got := observedGain(t, m, ctx, handle); got != 0 {
		t.Fatalf("engine gain after a fade to 0.7 requested while muted, clock advanced past its duration = %v, want 0 (the muted bed must not ramp up)", got)
	}
	s.mu.Lock()
	result, ok := s.executedResults[pkgaudio.InvocationID("inv-fade")]
	s.mu.Unlock()
	if !ok {
		t.Fatal("the fade invocation's own outcome was never recorded")
	}
	if result.Outcome != pkgaudio.OutcomeFadeComplete {
		t.Fatalf("fade outcome once it resolved while muted = %+v, want fade_complete (the engine genuinely reached the suppressed target, 0, which is what the fade should be judged against while muted)", result)
	}

	if r := m.Unmute(ctx, id, "inv-unmute", 5); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("unmute unexpectedly refused: %+v", r)
	}
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(0.7) {
		t.Fatalf("engine gain after unmute = %v, want the fade's own target, 0.7", got)
	}
}

// mutation target: prepareLocked's applyEffectiveGainBestEffortLocked
// call. A fresh engine handle defaults to unity; without that call, a
// muted session's next playlist item, or any other path that releases
// and re-prepares a handle, plays at unity, above any configured
// ceiling, until some unrelated later command happens to correct it.
func TestFreshHandleAfterPlaylistAdvanceNeverExceedsCeilingWhileMuted(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	item1 := writeTestAsset(t, m.assetDir, "item1.wav", "asset-item1", []byte("1"))
	item2 := writeTestAsset(t, m.assetDir, "item2.wav", "asset-item2", []byte("2"))
	const ceiling = pkgaudio.Ceiling(0.4)
	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground),
		Playlist: pkgaudio.SetField(pkgaudio.PlaylistRef{
			OwnerKind: "show", OwnerID: "s1", OwnerRevision: 1,
			Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyRestart,
			RequestedTransition: pkgaudio.ItemTransitionSequential,
			Items: []pkgaudio.PlaylistItem{
				{ItemID: "item-1", Index: 0, Media: item1},
				{ItemID: "item-2", Index: 1, Media: item2},
			},
		}),
		MixPolicy: pkgaudio.SetField(pkgaudio.MixPolicyMix),
		Ceiling:   pkgaudio.SetField(ceiling),
	}
	if r := m.Apply(ctx, id, "inv-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}
	if r := m.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start refused: %+v", r)
	}
	if r := m.GainSet(ctx, id, "inv-gain", 3, pkgaudio.Gain(0.4)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain.set refused: %+v", r)
	}
	if r := m.Mute(ctx, id, "inv-mute", 4); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}

	if r := m.Advance(ctx, id, "inv-advance", 5); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("advance unexpectedly refused: %+v", r)
	}

	s, _ := m.get(id)
	s.mu.Lock()
	itemID, muted, handle := s.currentItemID, s.muted, s.handle
	s.mu.Unlock()
	if itemID != "item-2" {
		t.Fatalf("precondition failed: current item = %q, want item-2 (the advance must have actually moved to a fresh handle)", itemID)
	}
	if !muted {
		t.Fatal("precondition failed: session is no longer muted")
	}
	if got := observedGain(t, m, ctx, handle); got != 0 {
		t.Fatalf("engine gain on the freshly prepared next item, while muted = %v, want 0 (a virgin handle's default unity must never be left in place, doubly above the %v ceiling)", got, ceiling)
	}
}

// mutation target: the same applyEffectiveGainBestEffortLocked call in
// prepareLocked, reached this time through restoreOne rather than a
// live advance. A session persisted muted, with a ceiling, must not come
// back from a restart audible at the engine's own default unity.
func TestRestoreNeverExceedsCeilingWhileMuted(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	const ceiling = pkgaudio.Ceiling(0.5)
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground),
		Media:      pkgaudio.SetField(ref),
		MixPolicy:  pkgaudio.SetField(pkgaudio.MixPolicyMix),
		Ceiling:    pkgaudio.SetField(ceiling),
	}
	if r := m.Apply(ctx, id, "inv-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}
	if r := m.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start refused: %+v", r)
	}
	if r := m.GainSet(ctx, id, "inv-gain", 3, pkgaudio.Gain(0.5)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain.set refused: %+v", r)
	}
	if r := m.Mute(ctx, id, "inv-mute", 4); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}

	m2 := newTestManagerInDir(dir, c)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	s2, ok := m2.get(id)
	if !ok {
		t.Fatal("session was not restored")
	}
	s2.mu.Lock()
	state, muted, handle := s2.state, s2.muted, s2.handle
	s2.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("restored state = %q, want playing", state)
	}
	if !muted {
		t.Fatal("precondition failed: session is not muted after restore")
	}
	if got := observedGain(t, m2, ctx, handle); got != 0 {
		t.Fatalf("engine gain after restart, state=%s muted=%v = %v, want 0 (it must not come back loud, above the %v ceiling, still flagged muted)", state, muted, got, ceiling)
	}
}

// No ordering of mute/duck across a ceiling may drive the ENGINE above
// its configured maximum, including transiently while unmuting under an
// active duck.
func TestMuteUnmuteAcrossDuckNeverExceedsCeiling(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	const ceiling = pkgaudio.Ceiling(0.5)
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
	if r := m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(1.0)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain set refused: %+v", r)
	}

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	bg, _ := m.get("bg")
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()

	assertNeverAboveCeiling := func(label string) {
		t.Helper()
		if got := observedGain(t, m, ctx, handle); got > pkgaudio.Gain(ceiling) {
			t.Fatalf("%s: engine gain = %v, exceeds configured ceiling %v", label, got, ceiling)
		}
	}
	assertNeverAboveCeiling("after duck starts")

	if r := m.Mute(ctx, "bg", "inv-bg-mute", 4); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}
	assertNeverAboveCeiling("after mute")

	if r := m.Unmute(ctx, "bg", "inv-bg-unmute", 5); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("unmute unexpectedly refused: %+v", r)
	}
	assertNeverAboveCeiling("after unmute while still ducked")

	if r := m.Stop(ctx, "ann", "inv-ann-stop", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stop unexpectedly refused: %+v", r)
	}
	assertNeverAboveCeiling("after duck ends")

	// The restore is a fade: let it finish before the final exact check.
	c.advance(900 * time.Millisecond)
	if got := observedGain(t, m, ctx, handle); got != pkgaudio.Gain(ceiling) {
		t.Fatalf("final engine gain = %v, want clamped to ceiling %v", got, ceiling)
	}
}

// mutation target: snapshotLocked's gate on reporting HasGain/Gain.
// Muting or ducking a session that never received an explicit
// audio.gain.set must still report a gain: those are themselves reasons
// effectiveGainLocked has a well-defined answer (unity reduced by the
// active suppression), and gating on desired.Gain alone leaves a
// suppressed session reporting no gain at all, which is real
// operator-visibility evidence lost, not merely an unset field.
func TestSnapshotReportsGainWhenSuppressedWithoutAnyExplicitGainSet(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	// Muted, never gain.set.
	const mutedID = pkgaudio.SessionID("muted")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-a", []byte("a"))
	startPlaying(t, m, ctx, mutedID, ref, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	if r := m.Mute(ctx, mutedID, "inv-mute", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}
	muted, _ := m.get(mutedID)
	muted.mu.Lock()
	snap := muted.snapshotLocked(ctx)
	muted.mu.Unlock()
	if !snap.HasGain {
		t.Fatal("muted session with no gain.set ever sent reports HasGain=false, want true")
	}
	if snap.Gain != 0 {
		t.Fatalf("muted session with no gain.set ever sent reports Gain=%v, want 0", snap.Gain)
	}

	// Ducked, never gain.set.
	const bgID = pkgaudio.SessionID("bg-unducked-gain")
	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, bgID, bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)
	bg, _ := m.get(bgID)
	bg.mu.Lock()
	ducked := len(bg.duckedByAll) != 0
	snap = bg.snapshotLocked(ctx)
	bg.mu.Unlock()
	if !ducked {
		t.Fatal("precondition failed: bg was not ducked by ann")
	}
	if !snap.HasGain {
		t.Fatal("ducked session with no gain.set ever sent reports HasGain=false, want true")
	}
	if snap.Gain != duckDepth(m) {
		t.Fatalf("ducked session with no gain.set ever sent reports Gain=%v, want the duck depth %v", snap.Gain, duckDepth(m))
	}
}

// mutation target: checkFadeCompletionLocked judging completion against
// fadeDispatchedTarget rather than the current effective gain. A mute
// landing mid-fade cancels the ramp (applyEffectiveGainLocked drives the
// engine to its own target via SetGain, which the fake engine's own
// SetGain clears any in-progress fade for) short of the fade's own
// dispatched target. The fade's own invocation must report
// Unconfirmable, honestly stating the target it was actually judged
// against never being reached, not FadeComplete: the CURRENT effective
// gain happens to equal the engine's evidence only because mute also
// forces 0, which is a coincidence of the suppression's own target, not
// evidence the requested fade reached anywhere near its real target.
func TestFadeCancelledByMuteReportsUnconfirmableNotComplete(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	// availableFakeEngine: this test checks the fade invocation's own
	// ungated outcome, which Manager.gateAvailability rewrites to
	// unconfirmable against an engine that never reports itself
	// available.
	m := NewManager(availableFakeEngine{NewFakeEngine(c.now)}, NewFileSessionStore(dir), dir, staticDecoder{duration: 30 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	startPlaying(t, m, ctx, id, ref, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	if r := m.GainSet(ctx, id, "inv-gain", 3, pkgaudio.Gain(0.1)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain.set unexpectedly refused: %+v", r)
	}
	const fadeInvocation = pkgaudio.InvocationID("inv-fade")
	if r := m.GainFade(ctx, id, fadeInvocation, 4, pkgaudio.FadeCurveLinear, 10*time.Second, pkgaudio.Gain(0.9)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain.fade unexpectedly refused: %+v", r)
	}

	// 10% of the way through a real ramp toward 0.9, well short of it.
	c.advance(1 * time.Second)

	if r := m.Mute(ctx, id, "inv-mute", 5); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("mute unexpectedly refused: %+v", r)
	}
	m.watchTick(ctx)

	s, _ := m.get(id)
	s.mu.Lock()
	result, ok := s.executedResults[fadeInvocation]
	handle := s.handle
	s.mu.Unlock()
	if !ok {
		t.Fatal("the fade invocation's own outcome was never recorded")
	}
	if result.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("fade outcome once mute cancelled it partway = %+v, want unconfirmable (the fade's own dispatched target, 0.9, was never reached)", result)
	}
	if got := observedGain(t, m, ctx, handle); got != 0 {
		t.Fatalf("engine gain after the mute that cancelled the fade = %v, want 0", got)
	}
}

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

// mutation target: Manager.Apply's interrupt refusal. Flip the equality
// check to always-false (or delete it) and this test starts passing on
// an accepted apply instead of a refusal.
func TestApplyRefusesInterruptMixPolicy(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	req := pkgaudio.ApplyRequest{MixPolicy: pkgaudio.SetField(pkgaudio.MixPolicyInterrupt)}

	r := m.Apply(ctx, "ann", "inv-1", 1, req)
	if r.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("outcome = %+v, want refused", r)
	}
	if r.Reason == "" {
		t.Fatal("refusal must carry a reason")
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

// mutation target: Manager.Mute's already-muted short-circuit. Removing
// it would overwrite preMuteGain with 0 on a second mute, losing the
// original gain unmute needs to restore.
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
	m.Mute(ctx, id, "inv-mute-2", 5) // must not clobber preMuteGain with 0

	s, _ := m.get(id)
	s.mu.Lock()
	if *s.desired.Gain != 0 {
		t.Fatalf("muted gain = %v, want 0", *s.desired.Gain)
	}
	if s.preMuteGain == nil || *s.preMuteGain != pkgaudio.Gain(0.7) {
		t.Fatalf("preMuteGain = %v, want 0.7", s.preMuteGain)
	}
	s.mu.Unlock()

	m.Unmute(ctx, id, "inv-unmute", 6)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.muted {
		t.Fatal("session still reports muted after unmute")
	}
	if *s.desired.Gain != pkgaudio.Gain(0.7) {
		t.Fatalf("gain after unmute = %v, want restored 0.7", *s.desired.Gain)
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
	if show.duckedBy != "" {
		t.Fatalf("show session was ducked by a lower-priority background session (duckedBy=%q)", show.duckedBy)
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
	duckedBy, gain := bg.duckedBy, *bg.desired.Gain
	bg.mu.Unlock()
	if duckedBy != "ann" || gain != 0 {
		t.Fatalf("bg after ann started = duckedBy %q gain %v, want ducked by ann to 0", duckedBy, gain)
	}

	m.Stop(ctx, "ann", "inv-ann-stop", 3)

	bg.mu.Lock()
	defer bg.mu.Unlock()
	if bg.duckedBy != "" {
		t.Fatalf("bg still shows duckedBy=%q after the ducking session stopped", bg.duckedBy)
	}
	if *bg.desired.Gain != pkgaudio.Gain(0.8) {
		t.Fatalf("bg gain after restore = %v, want the pre-duck 0.8", *bg.desired.Gain)
	}
}

// mutation target: restoreOneDuckLocked's own `if t.duckedBy == ""`
// guard, called directly and repeatedly rather than through a caller
// that already gates on duckedBy — both real callers happen to check
// duckedBy before calling, so this is the one test that actually
// exercises the function's own idempotence rather than a caller's.
// Without the guard, a second restore call after the operator has
// already set a new gain would incorrectly stomp it back to a default.
func TestRestoreOneDuckLockedIsIdempotentOnItsOwn(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	s, _ := m.get(id)
	s.mu.Lock()
	prior := pkgaudio.Gain(0.6)
	s.preDuckGain = &prior
	s.duckedBy = "ann"
	m.restoreOneDuckLocked(ctx, s) // first restore: clears duckedBy/preDuckGain, sets gain to 0.6

	operatorGain := pkgaudio.Gain(0.3)
	s.desired.Gain = &operatorGain // an operator gain.set arrives after the restore

	m.restoreOneDuckLocked(ctx, s) // must be a no-op: duckedBy is already empty
	got := *s.desired.Gain
	s.mu.Unlock()

	if got != operatorGain {
		t.Fatalf("gain after a second restoreOneDuckLocked call = %v, want the operator's %v untouched", got, operatorGain)
	}
}

// mutation target: restoreOneDuckLocked's `if t.duckedBy == ""` guard —
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
	ann.persistLocked()
	ann.mu.Unlock()

	bg, _ := m.get("bg")
	bg.mu.Lock()
	if bg.duckedBy != "ann" {
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
	defer bg2.mu.Unlock()
	if bg2.duckedBy != "" {
		t.Fatalf("bg2.duckedBy = %q after restart, want restored (empty) since ann is gone", bg2.duckedBy)
	}
	if bg2.desired.Gain == nil || *bg2.desired.Gain != pkgaudio.Gain(0.6) {
		t.Fatalf("bg2 gain after restart = %v, want the pre-duck 0.6", bg2.desired.Gain)
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
	duckedBy, gain := bg2.duckedBy, bg2.desired.Gain
	bg2.mu.Unlock()
	if duckedBy != "ann" {
		t.Fatalf("bg2.duckedBy = %q after restart, want still ducked by ann (ann is still playing)", duckedBy)
	}
	if gain == nil || *gain != pkgaudio.Gain(0) {
		t.Fatalf("bg2 gain after restart = %v, want still ducked to 0", gain)
	}

	// Now stop ann for real, through the live path, and confirm bg
	// restores exactly once from here too.
	m2.Stop(ctx, "ann", "inv-ann-stop", 3)

	bg2.mu.Lock()
	defer bg2.mu.Unlock()
	if bg2.duckedBy != "" {
		t.Fatalf("bg2.duckedBy = %q after ann finally stopped, want restored", bg2.duckedBy)
	}
	if *bg2.desired.Gain != pkgaudio.Gain(0.9) {
		t.Fatalf("bg2 gain after ann finally stopped = %v, want restored 0.9", *bg2.desired.Gain)
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

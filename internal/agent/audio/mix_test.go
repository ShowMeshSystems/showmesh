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
	gain := *bg.desired.Gain
	bg.mu.Unlock()
	if !duckedByAnn || gain != 0 {
		t.Fatalf("bg after ann started = duckedByAll %v gain %v, want ducked by ann to 0", bg.duckedByAll, gain)
	}

	m.Stop(ctx, "ann", "inv-ann-stop", 3)

	bg.mu.Lock()
	defer bg.mu.Unlock()
	if len(bg.duckedByAll) != 0 {
		t.Fatalf("bg still shows duckedByAll=%v after the ducking session stopped", bg.duckedByAll)
	}
	if *bg.desired.Gain != pkgaudio.Gain(0.8) {
		t.Fatalf("bg gain after restore = %v, want the pre-duck 0.8", *bg.desired.Gain)
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
	prior := pkgaudio.Gain(0.6)
	s.preDuckGain = &prior
	s.duckedByAll = map[pkgaudio.SessionID]struct{}{"ann": {}}
	m.removeDuckerLocked(ctx, s, "ann") // first removal: clears the set/preDuckGain, sets gain to 0.6

	operatorGain := pkgaudio.Gain(0.3)
	s.desired.Gain = &operatorGain // an operator gain.set arrives after the restore

	m.removeDuckerLocked(ctx, s, "ann") // must be a no-op: "ann" is already absent
	got := *s.desired.Gain
	s.mu.Unlock()

	if got != operatorGain {
		t.Fatalf("gain after a second removeDuckerLocked call = %v, want the operator's %v untouched", got, operatorGain)
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
	defer bg2.mu.Unlock()
	if len(bg2.duckedByAll) != 0 {
		t.Fatalf("bg2.duckedByAll = %v after restart, want restored (empty) since ann is gone", bg2.duckedByAll)
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
	_, duckedByAnn := bg2.duckedByAll["ann"]
	gain := bg2.desired.Gain
	bg2.mu.Unlock()
	if !duckedByAnn {
		t.Fatalf("bg2.duckedByAll = %v after restart, want still ducked by ann (ann is still playing)", bg2.duckedByAll)
	}
	if gain == nil || *gain != pkgaudio.Gain(0) {
		t.Fatalf("bg2 gain after restart = %v, want still ducked to 0", gain)
	}

	// Now stop ann for real, through the live path, and confirm bg
	// restores exactly once from here too.
	m2.Stop(ctx, "ann", "inv-ann-stop", 3)

	bg2.mu.Lock()
	defer bg2.mu.Unlock()
	if len(bg2.duckedByAll) != 0 {
		t.Fatalf("bg2.duckedByAll = %v after ann finally stopped, want restored", bg2.duckedByAll)
	}
	if *bg2.desired.Gain != pkgaudio.Gain(0.9) {
		t.Fatalf("bg2 gain after ann finally stopped = %v, want restored 0.9", *bg2.desired.Gain)
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
	gain := *bg.desired.Gain
	bg.mu.Unlock()
	if !byAnn1 || !byAnn2 || gain != 0 {
		t.Fatalf("bg after both announcements started: duckedByAll=%v gain=%v, want ducked by both ann1 and ann2 at 0", bg.duckedByAll, gain)
	}

	// ann1 stops first: bg must stay ducked at 0 because ann2 is still
	// playing.
	m.Stop(ctx, "ann1", "inv-ann1-stop", 3)

	bg.mu.Lock()
	_, stillByAnn2 := bg.duckedByAll["ann2"]
	gainAfterFirstStop := *bg.desired.Gain
	bg.mu.Unlock()
	if !stillByAnn2 || gainAfterFirstStop != 0 {
		t.Fatalf("bg after ann1 stopped (ann2 still playing): duckedByAll=%v gain=%v, want still ducked by ann2 at 0", bg.duckedByAll, gainAfterFirstStop)
	}

	// ann2 stops second: only now must bg's original gain be restored.
	m.Stop(ctx, "ann2", "inv-ann2-stop", 3)

	bg.mu.Lock()
	defer bg.mu.Unlock()
	if len(bg.duckedByAll) != 0 {
		t.Fatalf("bg.duckedByAll = %v after both announcements stopped, want empty", bg.duckedByAll)
	}
	if *bg.desired.Gain != pkgaudio.Gain(0.7) {
		t.Fatalf("bg gain after both announcements stopped = %v, want restored 0.7", *bg.desired.Gain)
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

	s, _ := m.get(id)
	s.mu.Lock()
	preCrashGain := *s.desired.Gain
	s.mu.Unlock()
	if preCrashGain != pkgaudio.Gain(0.4) {
		t.Fatalf("precondition: pre-crash desired gain = %v, want 0.4", preCrashGain)
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
	defer s2.mu.Unlock()
	if s2.desired.Gain == nil {
		t.Fatal("desired gain is nil after restore")
	}
	if *s2.desired.Gain > preCrashGain {
		t.Fatalf("desired gain after restart = %v, want it not to exceed the pre-crash gain %v (a virgin handle's default must never be read as this fade's outcome)", *s2.desired.Gain, preCrashGain)
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
	m.RebindEngine(switchable, second, "test rebind")

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

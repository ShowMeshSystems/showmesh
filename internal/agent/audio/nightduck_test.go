package audio

import (
	"context"
	"math"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// The night controller's own announcement/background-audio case, run
// against a real [Manager] with the exact shape internal/coordinator/api's
// night session sends: a background-role bed applied with mix policy
// "mix", its gain set once to the linear equivalent of the configured
// maxGainDb, and an announcement-role session applied with mix policy
// "duck". These prove the node owns ducking end to end, which is why the
// coordinator declares the policy and never drives the bed's gain.

// nightBedGain reads id's current desired gain plus its duck bookkeeping.
func nightBedGain(t *testing.T, m *Manager, id pkgaudio.SessionID) (pkgaudio.Gain, int) {
	t.Helper()
	s, ok := m.get(id)
	if !ok {
		t.Fatalf("no session %s", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effectiveGainLocked(), len(s.duckedByAll)
}

// nightApplyBed applies a background-role, mix-policy session without
// starting it, so a caller can put gain before start the way the night
// controller does.
func nightApplyBed(t *testing.T, m *Manager, ctx context.Context, id pkgaudio.SessionID, ref pkgaudio.MediaRef) {
	t.Helper()
	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground),
		Media:      pkgaudio.SetField(ref),
		MixPolicy:  pkgaudio.SetField(pkgaudio.MixPolicyMix),
	}
	if r := m.Apply(ctx, id, pkgaudio.InvocationID(string(id)+"-apply"), 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply %s: unexpectedly refused: %+v", id, r)
	}
}

// nightConfiguredGain is nightBackgroundCeilingGain's own arithmetic for
// a configured maxGainDb, duplicated here rather than imported: this
// package must not depend on the coordinator.
func nightConfiguredGain(maxGainDb float64) pkgaudio.Gain {
	return pkgaudio.Gain(math.Pow(10, maxGainDb/20))
}

// mutation target: removeDuckerLocked's applyEffectiveGainBestEffortLocked
// call. Delete it, or have effectiveGainLocked fall back to Gain(1)
// instead of the configured gain, and the final assertion fails. This is
// the invariant the coordinator's own duck/restore used to duplicate: an
// announcement ducks the bed and the bed returns to the CONFIGURED gain,
// with no second mechanism involved.
func TestNightAnnouncementDucksBedAndRestoresConfiguredGain(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))
	configured := nightConfiguredGain(-10)

	startPlaying(t, m, ctx, "night-bg", bedRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	if r := m.GainSet(ctx, "night-bg", "bg-gain", 3, configured); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain.set refused: %+v", r)
	}

	startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)
	ducked, duckers := nightBedGain(t, m, "night-bg")
	wantDucked := m.SettingsSnapshot().DuckTargetGain
	if duckers != 1 || ducked != wantDucked {
		t.Fatalf("during the announcement: gain = %v, duckers = %d; want %v and exactly one ducker", ducked, duckers, wantDucked)
	}

	m.Stop(ctx, "announcement-1", "ann-stop", 3)
	restored, duckers := nightBedGain(t, m, "night-bg")
	if duckers != 0 {
		t.Fatalf("after the announcement stopped, the bed still has %d ducker(s)", duckers)
	}
	if restored != configured {
		t.Fatalf("after the announcement: gain = %v, want the configured %v (the bed must never be left stranded quiet)", restored, configured)
	}
}

// mutation target: this package's own derived-gain composition
// (configuredGainLocked plus the active suppression reasons). Replacing
// it with a captured constant stops observing what this test exists to
// observe.
//
// This is the compounding proof, and the reason the coordinator no
// longer sends its own audio.gain.fade around an announcement. Run with
// the exact values the night controller used to send: a fade to a
// quarter of the configured gain before the announcement, and the node's
// own duck on top of it. The coordinator's own fade already overwrote
// the bed's configured gain to the quarter level BEFORE the node's duck
// ever started, so once the announcement ends, the node restores to that
// quarter, not to the original full configured gain, even though nothing
// in the node itself is broken: it composes correctly from whatever it
// was told, and it was told a quarter.
func TestNightTwoDuckMechanismsCompoundBelowEitherIntendedLevel(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))
	configured := nightConfiguredGain(-6)
	coordinatorDuck := pkgaudio.Gain(float64(configured) * 0.25)

	// A node duck depth below the coordinator's own level, so the two
	// mechanisms genuinely stack rather than the deeper one simply
	// winning: the depth is an operator setting now.
	s := DefaultSettings
	s.DuckTargetGain = pkgaudio.Gain(0.05)
	m.SetSettings(s)

	startPlaying(t, m, ctx, "night-bg", bedRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "night-bg", "bg-gain", 3, configured)

	// A second mechanism ducks first.
	m.GainFade(ctx, "night-bg", "bg-duck", 4, pkgaudio.FadeCurveLinear, 500*time.Millisecond, coordinatorDuck)
	c.advance(500 * time.Millisecond)
	m.watchTick(ctx)
	if g, _ := nightBedGain(t, m, "night-bg"); g != coordinatorDuck {
		t.Fatalf("coordinator duck fade left gain %v, want %v", g, coordinatorDuck)
	}

	// The node then ducks the same session on top of it.
	startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	if live, _ := nightBedGain(t, m, "night-bg"); live >= coordinatorDuck {
		t.Fatalf("bed gain under both mechanisms = %v, want strictly below the coordinator's own intended duck level %v", live, coordinatorDuck)
	}

	m.Stop(ctx, "announcement-1", "ann-stop", 3)
	restored, duckers := nightBedGain(t, m, "night-bg")
	if duckers != 0 {
		t.Fatalf("bed still has %d ducker(s) after the announcement stopped", duckers)
	}
	if restored != coordinatorDuck {
		t.Fatalf("bed gain once the node's own duck released = %v, want the coordinator's own quarter-level fade target %v, not the original %v; this is the compounding", restored, coordinatorDuck, configured)
	}
}

// mutation target: rememberIntendedGainWhileDuckedLocked, called from
// setGainLocked. Delete either and this fails with the bed back at the
// node's default of unity, which is ABOVE the configured maximum.
//
// The sequence is the reachable one: the bed's gain step does not take
// effect on this node, so the session starts at the node's own default;
// an announcement ducks it, capturing that default as the gain to
// restore; the controller's retry lands mid-announcement; the
// announcement ends. The bed must come back at the configured maximum,
// never above it.
func TestNightGainRetryDuringAnAnnouncementIsWhatTheBedComesBackAt(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))
	configured := nightConfiguredGain(-10)

	// The gain step never reached this node, so the session starts at the
	// engine's own default of unity rather than at the configured maximum.
	startPlaying(t, m, ctx, "night-bg", bedRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	if g, _ := nightBedGain(t, m, "night-bg"); g != pkgaudio.Gain(1) {
		t.Fatalf("bed gain with no gain step delivered = %v, want the default 1; this test's premise is that the configured gain never landed", g)
	}

	startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)
	if _, duckers := nightBedGain(t, m, "night-bg"); duckers != 1 {
		t.Fatal("the announcement did not duck the bed")
	}

	// The retry lands while the announcement is still playing.
	if r := m.GainSet(ctx, "night-bg", "bg-gain-retry", 3, configured); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("the retried gain.set was refused: %+v", r)
	}

	m.Stop(ctx, "announcement-1", "ann-stop", 3)
	final, duckers := nightBedGain(t, m, "night-bg")
	if duckers != 0 {
		t.Fatalf("bed still ducked by %d after the announcement stopped", duckers)
	}
	if final > configured {
		t.Fatalf("bed came back at %v, ABOVE the configured maximum %v", final, configured)
	}
	if final != configured {
		t.Fatalf("bed came back at %v, want the configured maximum %v", final, configured)
	}
}

// mutation target: rememberIntendedGainWhileDuckedLocked, called from
// startFadeLocked. The same rule for the fade path: a fade dispatched
// while a session is ducked names the gain the restore returns to, so
// gain.set and gain.fade cannot disagree about what the newest intended
// gain is.
func TestNightGainFadeDuringAnAnnouncementIsWhatTheBedComesBackAt(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))
	configured := nightConfiguredGain(-10)

	startPlaying(t, m, ctx, "night-bg", bedRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	if r := m.GainFade(ctx, "night-bg", "bg-fade", 3, pkgaudio.FadeCurveLinear, 500*time.Millisecond, configured); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("the fade was refused: %+v", r)
	}

	m.Stop(ctx, "announcement-1", "ann-stop", 3)
	final, _ := nightBedGain(t, m, "night-bg")
	if final > configured {
		t.Fatalf("bed came back at %v, ABOVE the configured maximum %v", final, configured)
	}
	if final != configured {
		t.Fatalf("bed came back at %v, want the fade's own target %v", final, configured)
	}
}

// mutation target: this package's derived-gain composition read through
// a gain.set that lands on an UNducked session, then a duck/undock cycle
// afterward. There is no separate restore slot for a duck to capture
// here at all in this design: the configured gain a gain.set records is
// what a later duck's own release always reads fresh, so a gain change
// with nobody ducking has nothing else to disturb.
func TestNightGainChangeOnAnUnduckedSessionSurvivesALaterDuckCycle(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))

	startPlaying(t, m, ctx, "night-bg", bedRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "night-bg", "bg-gain", 3, pkgaudio.Gain(0.4))
	if g, duckers := nightBedGain(t, m, "night-bg"); duckers != 0 || g != pkgaudio.Gain(0.4) {
		t.Fatalf("bed after an unducked gain.set: gain=%v duckers=%d, want 0.4 and none", g, duckers)
	}

	// And the duck that follows still captures the gain in force now.
	startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)
	m.Stop(ctx, "announcement-1", "ann-stop", 3)
	if final, _ := nightBedGain(t, m, "night-bg"); final != pkgaudio.Gain(0.4) {
		t.Fatalf("bed came back at %v, want the 0.4 it held when the duck began", final)
	}
}

// mutation target: effectiveGainLocked's own ceiling clamp. Skip it and
// this fails: a ceiling lowered while the bed was ducked must still
// bound the restore, so nothing ever puts the bed back above the
// configured maximum.
func TestNightDuckRestoreNeverExceedsTheCeiling(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))

	startPlaying(t, m, ctx, "night-bg", bedRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "night-bg", "bg-gain", 3, pkgaudio.Gain(0.9))
	startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	lowered := pkgaudio.Ceiling(0.2)
	if r := m.Apply(ctx, "night-bg", "bg-ceiling", 4, pkgaudio.ApplyRequest{Ceiling: pkgaudio.SetField(lowered)}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply lowered ceiling refused: %+v", r)
	}

	m.Stop(ctx, "announcement-1", "ann-stop", 3)
	final, _ := nightBedGain(t, m, "night-bg")
	if final != pkgaudio.Gain(lowered) {
		t.Fatalf("restored gain = %v, want it clamped to the ceiling %v that was in force at restore time", final, lowered)
	}
}

// mutation target: Manager.Stop's `m.restoreDucked(ctx, id)` call, gated
// on res.executed rather than on confirmation. An announcement whose own
// stop cannot be confirmed by the engine must STILL release the bed: a
// stuck duck is audible for the rest of the night, which is the defect
// class this whole seam exists to close.
func TestNightBedIsReleasedEvenWhenTheAnnouncementStopIsUnconfirmable(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	// availableFakeEngine, not newTestManager's FakeEngine: under a
	// permanently-unavailable engine EVERY stop reads as unconfirmable,
	// so this test could not tell an unconfirmable stop from an ordinary
	// one and would prove nothing about the release being unconditional.
	fake := NewFakeEngine(c.now)
	m := NewManager(availableFakeEngine{fake}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))
	configured := nightConfiguredGain(-10)

	startPlaying(t, m, ctx, "night-bg", bedRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "night-bg", "bg-gain", 3, configured)
	startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	ann, _ := m.get("announcement-1")
	ann.mu.Lock()
	handle := ann.handle
	ann.mu.Unlock()
	fake.InjectFailure(handle, errWrap(pkgaudio.ErrEnginePipelineCrash))

	if r := m.Stop(ctx, "announcement-1", "ann-stop", 3); r.Outcome == pkgaudio.OutcomeStopped {
		t.Fatalf("stop reported %+v; this test needs an unconfirmable stop to be meaningful", r)
	}
	final, duckers := nightBedGain(t, m, "night-bg")
	if duckers != 0 || final != configured {
		t.Fatalf("after an unconfirmable announcement stop: gain = %v, duckers = %d; want the configured %v and no duckers", final, duckers, configured)
	}
}

// mutation target: Manager.Start's submitToActivePolicies call. Delete
// it and this fails with duckers = 0 and full gain.
//
// The night controller's own ordering makes this the ordinary case, not
// a corner one: nightloop.go runs the enterResting cue list before
// nightAdvanceBackgroundAudio, and background audio takes several ticks
// to reach Playing, so an enterResting announcement is normally ALREADY
// playing when the bed starts under it.
func TestNightBedStartingUnderAPlayingAnnouncementIsDucked(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))
	configured := nightConfiguredGain(-10)

	// The announcement is playing first, with nothing to duck yet.
	startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	// Then the bed starts underneath it, in the night controller's own
	// order: apply, then gain (always before start, so the bed is never
	// audible for a tick at the node's prior gain), then start.
	nightApplyBed(t, m, ctx, "night-bg", bedRef)
	if r := m.GainSet(ctx, "night-bg", "bg-gain", 2, configured); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain.set refused: %+v", r)
	}
	if r := m.Start(ctx, "night-bg", "bg-start", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start refused: %+v", r)
	}
	ducked, duckers := nightBedGain(t, m, "night-bg")
	if duckers != 1 {
		t.Fatalf("bed started under a playing duck-policy announcement has %d ducker(s), want 1", duckers)
	}
	if wantDucked := m.SettingsSnapshot().DuckTargetGain; ducked != wantDucked {
		t.Fatalf("bed gain under the announcement = %v, want %v", ducked, wantDucked)
	}

	m.Stop(ctx, "announcement-1", "ann-stop", 3)
	restored, duckers := nightBedGain(t, m, "night-bg")
	if duckers != 0 || restored != configured {
		t.Fatalf("after the announcement stopped: gain = %v, duckers = %d; want the configured %v and none", restored, duckers, configured)
	}
}

// mutation target: submitToActivePolicies' MixPolicyInterrupt arm. The
// same ordering problem for an interrupt-policy announcement: a bed
// starting underneath one must be suspended, not left audible over it.
func TestNightBedStartingUnderAPlayingInterruptAnnouncementIsSuspended(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))

	startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)
	// Not startPlaying: the bed is suspended the instant it starts, so a
	// helper that asserts Playing afterwards would fail on the very
	// behavior under test.
	nightApplyBed(t, m, ctx, "night-bg", bedRef)
	if r := m.Start(ctx, "night-bg", "bg-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start refused: %+v", r)
	}

	bed, _ := m.get("night-bg")
	bed.mu.Lock()
	state, suspenders := bed.state, len(bed.interruptedByAll)
	bed.mu.Unlock()
	if state != pkgaudio.StatePaused || suspenders != 1 {
		t.Fatalf("bed started under a playing interrupt-policy announcement: state = %q, interrupters = %d; want paused and 1", state, suspenders)
	}

	m.Stop(ctx, "announcement-1", "ann-stop", 3)
	bed.mu.Lock()
	state, suspenders = bed.state, len(bed.interruptedByAll)
	bed.mu.Unlock()
	if state != pkgaudio.StatePlaying || suspenders != 0 {
		t.Fatalf("after the announcement stopped: bed state = %q, interrupters = %d; want playing and none", state, suspenders)
	}
}

// mutation target: submitToActivePolicies' OutranksForMixing guard. A
// session that does NOT outrank the one already playing must be left
// alone, or every ordinary session would suppress itself under a
// same-or-lower-priority neighbour.
func TestNightSubmitToActivePoliciesOnlyYieldsToAHigherRole(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "bg-asset", []byte("x"))
	otherRef := writeTestAsset(t, m.assetDir, "other.wav", "other-asset", []byte("y"))

	// A background-role session with a duck policy is already playing.
	startPlaying(t, m, ctx, "bed-a", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyDuck)
	// A second background-role session starts under it: equal priority,
	// so nothing may happen.
	startPlaying(t, m, ctx, "bed-b", otherRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)

	if _, duckers := nightBedGain(t, m, "bed-b"); duckers != 0 {
		t.Fatalf("an equal-priority neighbour ducked the starting session (%d duckers)", duckers)
	}
}

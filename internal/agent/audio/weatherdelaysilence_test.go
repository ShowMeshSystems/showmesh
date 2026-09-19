package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// Covers ZeroGainExcept and SilenceAllExcept, which leave the weather delay
// alert session alone.

// TestZeroGainExceptDrivesEveryOtherSessionToZeroAndLeavesTheExcludedOne
// proves every other session's engine gain goes straight to zero and the
// excluded session's is untouched.
func TestZeroGainExceptDrivesEveryOtherSessionToZeroAndLeavesTheExcludedOne(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	const other = pkgaudio.SessionID("other")
	const excluded = pkgaudio.SessionID("excluded")
	otherRef := writeTestAsset(t, m.assetDir, "other.wav", "other-asset", []byte("other"))
	excludedRef := writeTestAsset(t, m.assetDir, "excluded.wav", "excluded-asset", []byte("excluded"))

	startPlaying(t, m, ctx, other, otherRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	startPlaying(t, m, ctx, excluded, excludedRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyMix)

	// Set a nonzero configured gain on each so a zero reading afterward
	// proves ZeroGainExcept actually changed it, not merely observed a
	// session that started at zero.
	if r := m.GainSet(ctx, other, "gain-other", 3, pkgaudio.Gain(0.8)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain set other: %+v", r)
	}
	if r := m.GainSet(ctx, excluded, "gain-excluded", 3, pkgaudio.Gain(0.8)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain set excluded: %+v", r)
	}

	if unmuted := m.ZeroGainExcept(ctx, excluded, time.Second); len(unmuted) != 0 {
		t.Fatalf("unmuted = %v, want none", unmuted)
	}

	otherHandle, ok := m.get(other)
	if !ok {
		t.Fatal("other session missing")
	}
	obs, err := m.engine.Observe(ctx, otherHandle.handle)
	if err != nil {
		t.Fatalf("observe other: %v", err)
	}
	if obs.Gain != pkgaudio.Gain(0) {
		t.Fatalf("other session's engine gain = %v, want 0", obs.Gain)
	}

	excludedHandle, ok := m.get(excluded)
	if !ok {
		t.Fatal("excluded session missing")
	}
	excludedObs, err := m.engine.Observe(ctx, excludedHandle.handle)
	if err != nil {
		t.Fatalf("observe excluded: %v", err)
	}
	if excludedObs.Gain != pkgaudio.Gain(0.8) {
		t.Fatalf("excluded session's engine gain = %v, want unchanged 0.8", excludedObs.Gain)
	}
}

// wedgingEngine blocks on SetGain for one named handle until its context
// ends.
type wedgingEngine struct {
	*FakeEngine
	wedge EngineHandle
}

func (e *wedgingEngine) SetGain(ctx context.Context, handle EngineHandle, gain pkgaudio.Gain) (EngineObservation, error) {
	if handle == e.wedge {
		<-ctx.Done()
		return EngineObservation{}, ctx.Err()
	}
	return e.FakeEngine.SetGain(ctx, handle, gain)
}

// TestZeroGainExceptDoesNotHangOnAWedgedSession proves a wedged SetGain
// holds the call no longer than its bound, is reported as unmuted, and does
// not stop every other session being muted.
func TestZeroGainExceptDoesNotHangOnAWedgedSession(t *testing.T) {
	old := engineCallTimeout
	engineCallTimeout = 200 * time.Millisecond
	defer func() { engineCallTimeout = old }()

	c := newClock(time.Now())
	dir := t.TempDir()
	fake := NewFakeEngine(c.now)
	ctx := context.Background()

	const wedged = pkgaudio.SessionID("wedged")
	const healthy = pkgaudio.SessionID("healthy")
	const excluded = pkgaudio.SessionID("excluded")
	wedgedRef := writeTestAsset(t, dir, "wedged.wav", "wedged-asset", []byte("wedged"))
	healthyRef := writeTestAsset(t, dir, "healthy.wav", "healthy-asset", []byte("healthy"))
	excludedRef := writeTestAsset(t, dir, "excluded.wav", "excluded-asset", []byte("excluded"))

	engine := &wedgingEngine{FakeEngine: fake}
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	startPlaying(t, m, ctx, wedged, wedgedRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	startPlaying(t, m, ctx, healthy, healthyRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	startPlaying(t, m, ctx, excluded, excludedRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyMix)

	wedgedSession, _ := m.get(wedged)
	wedgedSession.mu.Lock()
	engine.wedge = wedgedSession.handle
	wedgedSession.mu.Unlock()

	const bound = 50 * time.Millisecond
	began := time.Now()
	unmuted := m.ZeroGainExcept(ctx, excluded, bound)
	if elapsed := time.Since(began); elapsed > bound+100*time.Millisecond {
		t.Fatalf("ZeroGainExcept took %v, want no more than about %v", elapsed, bound)
	}
	if len(unmuted) != 1 || unmuted[0] != wedged {
		t.Fatalf("unmuted = %v, want exactly %q", unmuted, wedged)
	}

	healthySession, _ := m.get(healthy)
	obs, err := m.engine.Observe(ctx, healthySession.handle)
	if err != nil {
		t.Fatalf("observe healthy: %v", err)
	}
	if obs.Gain != pkgaudio.Gain(0) {
		t.Fatalf("healthy session's engine gain = %v, want 0 (a wedged sibling must not prevent this)", obs.Gain)
	}
}

// TestSilenceAllExceptLeavesTheExcludedSessionPlaying proves
// SilenceAllExcept stops every session but the excluded one, matching
// SilenceAll's own behavior toward every other session.
func TestSilenceAllExceptLeavesTheExcludedSessionPlaying(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	const other = pkgaudio.SessionID("other")
	const excluded = pkgaudio.SessionID("excluded")
	otherRef := writeTestAsset(t, m.assetDir, "other.wav", "other-asset", []byte("other"))
	excludedRef := writeTestAsset(t, m.assetDir, "excluded.wav", "excluded-asset", []byte("excluded"))

	startPlaying(t, m, ctx, other, otherRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	startPlaying(t, m, ctx, excluded, excludedRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyMix)

	results := m.SilenceAllExcept(ctx, excluded)
	if len(results) != 1 || results[0].ID != other {
		t.Fatalf("SilenceAllExcept results = %+v, want exactly one result for %q", results, other)
	}

	otherSession, _ := m.get(other)
	otherSession.mu.Lock()
	otherState := otherSession.state
	otherSession.mu.Unlock()
	if otherState != pkgaudio.StateStopped {
		t.Fatalf("other session state = %q, want Stopped", otherState)
	}

	excludedSession, _ := m.get(excluded)
	excludedSession.mu.Lock()
	excludedState := excludedSession.state
	excludedSession.mu.Unlock()
	if excludedState != pkgaudio.StatePlaying {
		t.Fatalf("excluded session state = %q, want Playing (SilenceAllExcept must not touch it)", excludedState)
	}
}

// TestZeroGainExceptDoesNotWaitOnAHeldSessionLock proves a session whose
// lock is held by another call (a commanded Stop wedged in the engine)
// cannot hold up ZeroGainExcept, and so cannot hold up the alert.
func TestZeroGainExceptDoesNotWaitOnAHeldSessionLock(t *testing.T) {
	old := engineCallTimeout
	engineCallTimeout = 200 * time.Millisecond
	defer func() { engineCallTimeout = old }()

	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	const busy = pkgaudio.SessionID("busy")
	const excluded = pkgaudio.SessionID("excluded")
	busyRef := writeTestAsset(t, m.assetDir, "busy.wav", "busy-asset", []byte("busy"))
	startPlaying(t, m, ctx, busy, busyRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	busySession, _ := m.get(busy)
	busySession.mu.Lock()
	defer busySession.mu.Unlock()

	unmuted := m.ZeroGainExcept(ctx, excluded, 50*time.Millisecond)
	if len(unmuted) != 1 || unmuted[0] != busy {
		t.Fatalf("unmuted = %v, want exactly %q", unmuted, busy)
	}
}

// TestZeroGainExceptMutesAStagedNextItemHandle proves a staged next item
// cannot become audible at its boundary after the mute step.
func TestZeroGainExceptMutesAStagedNextItemHandle(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	const other = pkgaudio.SessionID("other")
	ref := writeTestAsset(t, m.assetDir, "other.wav", "other-asset", []byte("other"))
	nextRef := writeTestAsset(t, m.assetDir, "next.wav", "next-asset", []byte("next"))
	startPlaying(t, m, ctx, other, ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	const stagedHandle = EngineHandle("other/stage/1")
	if _, err := m.engine.Load(ctx, stagedHandle, nextRef, 2*time.Second); err != nil {
		t.Fatalf("load staged handle: %v", err)
	}
	if _, err := m.engine.SetGain(ctx, stagedHandle, pkgaudio.Gain(0.8)); err != nil {
		t.Fatalf("set staged gain: %v", err)
	}
	s, _ := m.get(other)
	s.mu.Lock()
	s.stage = &itemStage{index: 1, handle: stagedHandle, ready: true}
	s.mu.Unlock()

	if unmuted := m.ZeroGainExcept(ctx, "excluded", time.Second); len(unmuted) != 0 {
		t.Fatalf("unmuted = %v, want none", unmuted)
	}
	obs, err := m.engine.Observe(ctx, stagedHandle)
	if err != nil {
		t.Fatalf("observe staged: %v", err)
	}
	if obs.Gain != pkgaudio.Gain(0) {
		t.Fatalf("staged handle gain = %v, want 0", obs.Gain)
	}
}

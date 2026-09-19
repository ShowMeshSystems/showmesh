package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file covers ZeroGainExcept and SilenceAllExcept: ADR-053 decision
// 7's own steps (a) and (c), the node-scoped weather delay alert's
// counterparts to gainCall/SilenceAll that leave one named session alone.

// TestZeroGainExceptDrivesEveryOtherSessionToZeroAndLeavesTheExcludedOne
// proves ZeroGainExcept sets every other session's gain to zero directly
// (no fade, no state change reported through Snapshot's gain field) while
// leaving the excluded session's own gain untouched.
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

	m.ZeroGainExcept(ctx, excluded)

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

// wedgingEngine wraps [FakeEngine] and blocks forever on SetGain for one
// named handle, proving ZeroGainExcept's own per-session bound (not an
// unbounded wait) still lets the call return and still reaches every
// other session.
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

// TestZeroGainExceptDoesNotHangOnAWedgedSession proves a session whose
// own SetGain call never returns cannot make ZeroGainExcept itself hang:
// engineCallTimeout still bounds that one call, and every other session
// is still reached.
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

	done := make(chan struct{})
	go func() {
		m.ZeroGainExcept(ctx, excluded)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ZeroGainExcept did not return within 5s of a wedged session's SetGain")
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

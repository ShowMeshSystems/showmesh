package audio

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestWatchTickResolvesAStopSweptOutFromUnderAFailedStop proves a session
// left in StateStopping by a Stop failure that is NOT ErrHandleNotLoaded
// (so [Manager.stopExecLocked] never resolved it itself) still recovers
// once the handle it is stuck behind stops existing at all: SilenceAll's
// own unconditional final sweep releases that handle from the engine
// regardless of the failed per-session Stop, and a later watch tick's
// Observe on it now returns ErrHandleNotLoaded. Before the fix,
// [Session.checkStopCompletionLocked] returned on any Observe error and
// left the session stuck reporting StateStopping forever, so the next
// Start refused against a handle no later Observe could ever confirm
// again.
func TestWatchTickResolvesAStopSweptOutFromUnderAFailedStop(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	engine := NewFakeEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	const id = pkgaudio.SessionID("stuck")
	ref := writeTestAsset(t, m.assetDir, "stuck.wav", "stuck-asset", []byte("stuck"))
	startPlaying(t, m, ctx, id, ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session missing")
	}
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()

	engine.InjectFailure(handle, errors.New("injected: stop failed for a reason that is not a missing handle"))

	m.SilenceAll(ctx)

	s.mu.Lock()
	stateAfterSilence := s.state
	s.mu.Unlock()
	if stateAfterSilence != pkgaudio.StateStopping {
		t.Fatalf("precondition: state after the failed Stop = %q, want StateStopping (this test proves recovery FROM that stuck state)", stateAfterSilence)
	}

	// The failed Stop never released it, but SilenceAll's own
	// unconditional final ReleaseAll sweep tore the handle down anyway --
	// the engine now genuinely has nothing under this name.
	if _, err := engine.Observe(ctx, handle); err == nil {
		t.Fatal("precondition: engine still reports this handle after SilenceAll's final sweep")
	}

	for i := 0; i < 3; i++ {
		m.watchTick(ctx)
	}

	s.mu.Lock()
	stateAfterTicks, handleLoaded := s.state, s.handleLoaded
	s.mu.Unlock()
	if stateAfterTicks != pkgaudio.StateStopped {
		t.Fatalf("state after watch ticks = %q, want Stopped (a handle the engine has already forgotten must resolve, not wedge forever)", stateAfterTicks)
	}
	if handleLoaded {
		t.Fatal("handleLoaded still true after watch ticks resolved the stop; want false so the next Start re-prepares")
	}

	if r := m.Start(ctx, id, "restart-1", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("first Start after recovery was refused: %+v", r)
	}
	s.mu.Lock()
	stateAfterRestart := s.state
	s.mu.Unlock()
	if stateAfterRestart != pkgaudio.StatePlaying {
		t.Fatalf("state after the first Start following recovery = %q, want Playing", stateAfterRestart)
	}
}

// TestWatchTickResolvesAPendingFadeSweptOutFromUnderIt is [
// TestWatchTickResolvesAStopSweptOutFromUnderAFailedStop]'s counterpart
// for [Session.checkFadeCompletionLocked]: a fade still in progress when
// its own handle stops existing at the engine (an emergency stop's own
// unconditional sweep, here dispatched directly against the engine
// rather than through a failed per-session Stop) must resolve to
// stopped with the fade itself resolved, not left pending forever.
func TestWatchTickResolvesAPendingFadeSweptOutFromUnderIt(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	engine := NewFakeEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	const id = pkgaudio.SessionID("fading")
	ref := writeTestAsset(t, m.assetDir, "fading.wav", "fading-asset", []byte("fading"))
	startPlaying(t, m, ctx, id, ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	m.GainFade(ctx, id, "fade-1", 3, pkgaudio.FadeCurveLinear, time.Second, pkgaudio.Gain(0))

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session missing")
	}
	s.mu.Lock()
	fadePendingBefore := s.fadePending
	s.mu.Unlock()
	if !fadePendingBefore {
		t.Fatal("precondition: fadePending must be true right after dispatching the fade")
	}

	// Simulates an emergency stop's unconditional final sweep releasing
	// this session's handle without going through its own Stop at all.
	if _, err := engine.ReleaseAll(ctx); err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}

	for i := 0; i < 3; i++ {
		m.watchTick(ctx)
	}

	s.mu.Lock()
	state, fadePendingAfter, handleLoaded := s.state, s.fadePending, s.handleLoaded
	s.mu.Unlock()
	if state != pkgaudio.StateStopped {
		t.Fatalf("state after watch ticks = %q, want Stopped", state)
	}
	if fadePendingAfter {
		t.Fatal("fadePending still true after watch ticks resolved a handle the engine had already forgotten")
	}
	if handleLoaded {
		t.Fatal("handleLoaded still true after watch ticks resolved the stop; want false so the next Start re-prepares")
	}
}

// TestRestoreOneReleasesThePreviousInMemorySessionsStagedHandle proves a
// re-restore (a second startup after a crash mid-restore, or a hot
// reload finding an already in-memory session for id) releases the
// PREVIOUS session's staged successor handle, not only its currently
// loaded one: before the fix, restoreOne called
// previous.releaseEngineLocked but never previous.discardStageLocked, so
// a staged handle survived as a permanent orphan in the engine, live but
// unreachable through any session.
func TestRestoreOneReleasesThePreviousInMemorySessionsStagedHandle(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	const id = pkgaudio.SessionID("restaged")
	ref := writeTestAsset(t, m.assetDir, "restaged.wav", "restaged-asset", []byte("restaged"))
	nextRef := writeTestAsset(t, m.assetDir, "restaged-next.wav", "restaged-next-asset", []byte("restaged-next"))
	startPlaying(t, m, ctx, id, ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	const stagedHandle = EngineHandle("restaged/stage/1")
	if _, err := m.engine.Load(ctx, stagedHandle, nextRef, 2*time.Second); err != nil {
		t.Fatalf("load staged handle: %v", err)
	}
	s, ok := m.get(id)
	if !ok {
		t.Fatal("session missing")
	}
	s.mu.Lock()
	s.stage = &itemStage{index: 1, handle: stagedHandle, ready: true}
	loadedHandle := s.handle
	s.mu.Unlock()

	// A re-restore: the persisted record already exists (startPlaying
	// persisted it), and m.sessions[id] already holds the in-memory
	// session above -- restoreOne's own "hadPrevious" branch.
	if err := m.restoreOne(ctx, id, false); err != nil {
		t.Fatalf("restoreOne (re-restore): %v", err)
	}

	if _, err := m.engine.Observe(ctx, stagedHandle); err == nil {
		t.Fatal("the previous session's staged handle is still live in the engine after a re-restore; want it released")
	}
	if _, err := m.engine.Observe(ctx, loadedHandle); err == nil {
		t.Fatal("the previous session's loaded handle is still live in the engine after a re-restore; want it released")
	}
}

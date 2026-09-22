package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// stageOnVanishedHandleSession returns a playing session whose engine
// handle the engine has already forgotten, carrying both a schedule and
// a ready stage, plus the staged handle so a test can check it was
// released.
func stageOnVanishedHandleSession(t *testing.T, m *Manager, engine *FakeEngine, id pkgaudio.SessionID) (*Session, EngineHandle) {
	t.Helper()
	ctx := context.Background()
	ref := writeTestAsset(t, m.assetDir, string(id)+".wav", "asset-"+string(id), []byte("bytes"))
	startPlaying(t, m, ctx, id, ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session missing")
	}
	staged := EngineHandle(string(id) + "/stage/99")
	if _, err := engine.Load(ctx, staged, ref, time.Second); err != nil {
		t.Fatalf("staging Load: %v", err)
	}
	s.mu.Lock()
	handle := s.handle
	s.schedule = &itemSchedule{itemStartAt: m.now()}
	s.stage = &itemStage{index: 1, handle: staged, ready: true}
	s.mu.Unlock()

	// The emergency stop's own sweep is what makes the loaded handle
	// vanish from under a session that still believes it holds one.
	if _, err := engine.ReleaseAll(ctx, staged); err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	if _, err := engine.Observe(ctx, handle); err == nil {
		t.Fatal("precondition: the engine still holds the session's handle")
	}
	return s, staged
}

// TestAStopResolvedByAVanishedHandleClearsItsScheduleAndStage proves the
// two paths that resolve a session whose engine handle the stop already
// released finish the same work a commanded stop does. Leaving the
// schedule and the staged handle behind let the next watch tick advance
// a stopped session to its successor item and leak the staged branch.
func TestAStopResolvedByAVanishedHandleClearsItsScheduleAndStage(t *testing.T) {
	t.Run("stop completion", func(t *testing.T) {
		c := newClock(time.Now())
		dir := t.TempDir()
		engine := NewFakeEngine(c.now)
		m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)

		s, staged := stageOnVanishedHandleSession(t, m, engine, "stopping")
		s.mu.Lock()
		s.state = pkgaudio.StateStopping
		s.mu.Unlock()

		m.watchTick(context.Background())
		assertStopCleanedUp(t, engine, s, staged)
	})

	t.Run("fade completion", func(t *testing.T) {
		c := newClock(time.Now())
		dir := t.TempDir()
		engine := NewFakeEngine(c.now)
		m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)

		s, staged := stageOnVanishedHandleSession(t, m, engine, "fading")
		s.mu.Lock()
		s.fadePending = true
		s.fadeDispatchedTarget = pkgaudio.Gain(0.5)
		s.mu.Unlock()

		m.watchTick(context.Background())
		assertStopCleanedUp(t, engine, s, staged)
	})
}

func assertStopCleanedUp(t *testing.T, engine *FakeEngine, s *Session, staged EngineHandle) {
	t.Helper()
	s.mu.Lock()
	state, schedule, stage, loaded := s.state, s.schedule, s.stage, s.handleLoaded
	s.mu.Unlock()
	if state != pkgaudio.StateStopped {
		t.Fatalf("state = %q, want Stopped", state)
	}
	if loaded {
		t.Fatal("handleLoaded is still true after the handle vanished")
	}
	if schedule != nil {
		t.Fatal("schedule survived the stop; the next tick would advance a stopped session to its successor item")
	}
	if stage != nil {
		t.Fatal("stage survived the stop")
	}
	if _, err := engine.Observe(context.Background(), staged); err == nil {
		t.Fatal("the staged handle was never released; a stop must not leave a staged branch loaded")
	}
}

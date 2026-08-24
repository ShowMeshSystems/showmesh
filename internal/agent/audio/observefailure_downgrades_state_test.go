package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// startPlayingSession applies and starts a session against m, returning
// the running session with its handle already loaded — the shared setup
// for both reproductions below, which both prove the SAME defect
// (a failed Observe leaves a session claiming State: Playing) from the
// two call sites that can trigger it: the per-tick background poll
// (watchTick, in restore.go) and an on-demand snapshot read
// (snapshotLocked, in session.go).
func startPlayingSession(t *testing.T, m *Manager, id pkgaudio.SessionID) *Session {
	t.Helper()
	ctx := context.Background()
	ref := writeTestAsset(t, m.assetDir, string(id)+".wav", "asset-"+string(id), []byte(id))
	m.Apply(ctx, id, pkgaudio.InvocationID("inv-"+string(id)+"-apply"), 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, pkgaudio.InvocationID("inv-"+string(id)+"-start"), 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatalf("session %s not found after Apply/Start", id)
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("setup: session state = %q, want %q", state, pkgaudio.StatePlaying)
	}
	return s
}

// TestWatchTickDowngradesStateAfterObserveFailure proves watchTick's
// Observe failure branch (restore.go) downgrades s.state, not just the
// fault field, so a session whose pipeline just crashed stops being
// reported State: Playing.
func TestWatchTickDowngradesStateAfterObserveFailure(t *testing.T) {
	c := newClock(time.Now())
	engine := NewFakeEngine(c.now)
	dir := t.TempDir()
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)

	s := startPlayingSession(t, m, "s1")
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()
	engine.InjectFailure(handle, pkgaudio.ErrEnginePipelineCrash)

	m.watchTick(context.Background())

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == pkgaudio.StatePlaying {
		t.Fatalf("session state = %q after a failed Observe; must not still claim Playing on a dead pipeline", s.state)
	}
	if s.fault != pkgaudio.FaultPipelineCrash {
		t.Fatalf("session fault = %q, want %q", s.fault, pkgaudio.FaultPipelineCrash)
	}
}

// TestSnapshotDowngradesStateAfterObserveFailure reproduces defect 3 at
// its on-demand call site: [Manager.Snapshot] (session.go's
// snapshotLocked) has the identical gap — a failed Observe sets the
// fault but the reported State stays Playing.
func TestSnapshotDowngradesStateAfterObserveFailure(t *testing.T) {
	c := newClock(time.Now())
	engine := NewFakeEngine(c.now)
	dir := t.TempDir()
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)

	s := startPlayingSession(t, m, "s1")
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()
	engine.InjectFailure(handle, pkgaudio.ErrEnginePipelineCrash)

	snaps := m.Snapshot(context.Background())
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snaps))
	}
	got := snaps[0]
	if got.State == pkgaudio.StatePlaying {
		t.Fatalf("snapshot State = %q after a failed Observe; must not still claim Playing on a dead pipeline", got.State)
	}
	if got.Fault != pkgaudio.FaultPipelineCrash {
		t.Fatalf("snapshot Fault = %q, want %q", got.Fault, pkgaudio.FaultPipelineCrash)
	}
}

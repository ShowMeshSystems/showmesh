package audio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// releaseFailsOnceEngine wraps [FakeEngine] and fails exactly one future
// Release call against an armed handle, leaving every other call —
// including the Engine.Stop that [Manager.Stop] issues first — running
// normally. FakeEngine's own [FakeEngine.InjectFailure] cannot express
// this: it arms the NEXT call of any kind against a handle, and
// Manager.Stop issues Stop before Release against the same handle in one
// dispatch, so a plain injection would fail the wrong call.
type releaseFailsOnceEngine struct {
	*FakeEngine

	mu     sync.Mutex
	handle EngineHandle
	err    error
}

func newReleaseFailsOnceEngine(now func() time.Time) *releaseFailsOnceEngine {
	return &releaseFailsOnceEngine{FakeEngine: NewFakeEngine(now)}
}

func (e *releaseFailsOnceEngine) armRelease(handle EngineHandle, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handle, e.err = handle, err
}

func (e *releaseFailsOnceEngine) Release(ctx context.Context, handle EngineHandle) error {
	e.mu.Lock()
	if handle == e.handle && e.err != nil {
		err := e.err
		e.handle, e.err = "", nil
		e.mu.Unlock()
		return err
	}
	e.mu.Unlock()
	return e.FakeEngine.Release(ctx, handle)
}

// TestStopResolvesSessionWhenReleaseFailsAfterStopSucceeds proves
// Manager.Stop does not strand a session in StateStopping when
// Engine.Stop itself succeeds but the subsequent Engine.Release fails
// (the deferred-teardown shape a real node's gstengine can produce):
// Release always discards the handle at the engine regardless of its own
// outcome, so no later Observe can ever confirm this stop the way
// [Session.checkStopCompletionLocked] expects, and the session must
// resolve here instead of being left to poll forever.
func TestStopResolvesSessionWhenReleaseFailsAfterStopSucceeds(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	engine := newReleaseFailsOnceEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("content"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()

	engine.armRelease(handle, errors.New("simulated deferred teardown"))

	r := m.Stop(ctx, id, "inv-stop", 3)
	if r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Stop outcome = %+v, must never be refused", r)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StateStopped {
		t.Fatalf("session state after a failed Release = %q, want stopped (resolved immediately, not stranded in stopping)", s.state)
	}
	if s.handleLoaded {
		t.Fatal("session handle still marked loaded after a failed Release; the engine has already discarded it")
	}
}

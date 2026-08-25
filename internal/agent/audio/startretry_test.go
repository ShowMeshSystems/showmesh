package audio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// stickyFaultEngine wraps a [FakeEngine] so a failure poisoned onto a
// handle persists across every Start/Resume call against that exact
// handle until the handle is reloaded via Load, which clears it. This
// models a genuinely broken engine handle, which [FakeEngine.InjectFailure]
// cannot by itself: an injected failure is consumed by whichever engine
// call reaches it next, so it cannot alone prove that a retry keeps
// calling into the SAME broken handle rather than a freshly re-prepared
// one.
type stickyFaultEngine struct {
	*FakeEngine
	mu     sync.Mutex
	broken map[EngineHandle]error
}

func newStickyFaultEngine(fe *FakeEngine) *stickyFaultEngine {
	return &stickyFaultEngine{FakeEngine: fe, broken: make(map[EngineHandle]error)}
}

// poison arms handle so every subsequent Start or Resume against it
// fails with err, until a fresh Load for that same handle clears it.
func (e *stickyFaultEngine) poison(handle EngineHandle, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.broken[handle] = err
}

func (e *stickyFaultEngine) Load(ctx context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	delete(e.broken, handle)
	e.mu.Unlock()
	return e.FakeEngine.Load(ctx, handle, media, duration)
}

func (e *stickyFaultEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	err, poisoned := e.broken[handle]
	e.mu.Unlock()
	if poisoned {
		return EngineObservation{}, err
	}
	return e.FakeEngine.Start(ctx, handle, position)
}

func (e *stickyFaultEngine) Resume(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	e.mu.Lock()
	err, poisoned := e.broken[handle]
	e.mu.Unlock()
	if poisoned {
		return EngineObservation{}, err
	}
	return e.FakeEngine.Resume(ctx, handle)
}

// newStickyFaultTestManager is [newTestManager] with the engine swapped
// for a [stickyFaultEngine], so a test can poison one handle and observe
// whether a retry still reaches it broken or gets a freshly prepared one.
func newStickyFaultTestManager(t *testing.T, c *clock) (*Manager, *stickyFaultEngine) {
	t.Helper()
	dir := t.TempDir()
	se := newStickyFaultEngine(NewFakeEngine(c.now))
	m := NewManager(se, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	return m, se
}

// TestFailedStartRetryRecoversWithoutExplicitPrepare proves a failed
// Start does not leave a session refusing every ordinary retry forever.
// Manager.Start only re-prepares when the handle is unloaded or the item
// identity changed; a failed [Engine.Start] call itself changes neither
// unless the failure branch also drops the handle, in which case the
// next Start's own re-prepare branch fires and reaches [Engine.Load],
// clearing the poison.
//
// [FakeEngine] (and this wrapper) always reports itself unavailable
// ([Manager.gateAvailability] then forces every non-Refused/Failed
// [pkgaudio.OutcomeResult] to Unconfirmable), so a real transition is
// read off s.state directly, the same convention fault_test.go uses, not
// off the returned outcome.
func TestFailedStartRetryRecoversWithoutExplicitPrepare(t *testing.T) {
	ctx := context.Background()
	c := newClock(time.Now())
	m, se := newStickyFaultTestManager(t, c)
	id := pkgaudio.SessionID("sm226-start")

	ref := writeTestAsset(t, m.assetDir, "sm226-start.wav", "sm226-start", []byte("content"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}
	m.Apply(ctx, id, pkgaudio.InvocationID(id+"-apply"), 1, req)
	m.Prepare(ctx, id, pkgaudio.InvocationID(id+"-prepare"), 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatalf("session %s was not created", id)
	}
	s.mu.Lock()
	handleLoadedAfterPrepare := s.handleLoaded
	handle := s.handle
	s.mu.Unlock()
	if !handleLoadedAfterPrepare {
		t.Fatal("setup Prepare: handle was not loaded")
	}

	// A generic engine-error fault, not a pipeline crash — and it stays
	// broken until a fresh Load clears it.
	se.poison(handle, errors.New("engine start failed: seek deadline exceeded"))

	res := m.Start(ctx, id, pkgaudio.InvocationID(id+"-start1"), 3)
	if res.Outcome != pkgaudio.OutcomeFailed {
		t.Fatalf("first Start: outcome = %v, want Failed", res.Outcome)
	}

	s.mu.Lock()
	stateAfterFailure := s.state
	s.mu.Unlock()
	if stateAfterFailure != pkgaudio.StateFailed {
		t.Fatalf("state after failed start = %v, want Failed", stateAfterFailure)
	}

	// The ordinary operator retry: Start again, no Prepare in between.
	retry := m.Start(ctx, id, pkgaudio.InvocationID(id+"-start2"), 4)

	s.mu.Lock()
	stateAfterRetry := s.state
	s.mu.Unlock()

	if retry.Outcome == pkgaudio.OutcomeRefused || stateAfterRetry == pkgaudio.StateFailed {
		t.Fatalf("retry Start after failed start: outcome = %v (reason %q), state = %v — "+
			"an ordinary retry must recover without an explicit Prepare", retry.Outcome, retry.Reason, stateAfterRetry)
	}
	if stateAfterRetry != pkgaudio.StatePlaying {
		t.Fatalf("retry Start after failed start: state = %v, want Playing", stateAfterRetry)
	}
}

// TestFailedResumeRetryRecoversAtBookmarkPosition is Resume's half of the
// same defect, and pins the property that actually matters: a failed
// Resume must keep the session Paused with its bookmark intact (not
// Failed, and not dropped) so the ordinary next Resume — an operator's,
// or an automated caller's own per-tick retry, which never falls
// through to Start — re-prepares and continues from the bookmarked
// position, never from 0.
func TestFailedResumeRetryRecoversAtBookmarkPosition(t *testing.T) {
	ctx := context.Background()
	c := newClock(time.Now())
	m, se := newStickyFaultTestManager(t, c)
	id := pkgaudio.SessionID("sm226-resume")

	ref := writeTestAsset(t, m.assetDir, "sm226-resume.wav", "sm226-resume", []byte("content"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}
	m.Apply(ctx, id, pkgaudio.InvocationID(id+"-apply"), 1, req)
	m.Start(ctx, id, pkgaudio.InvocationID(id+"-start"), 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatalf("session %s was not created", id)
	}
	s.mu.Lock()
	stateAfterStart := s.state
	s.mu.Unlock()
	if stateAfterStart != pkgaudio.StatePlaying {
		t.Fatalf("setup Start: state = %v, want Playing", stateAfterStart)
	}

	const pausedAt = 700 * time.Millisecond
	c.advance(pausedAt)

	m.Pause(ctx, id, pkgaudio.InvocationID(id+"-pause"), 3)
	s.mu.Lock()
	stateAfterPause := s.state
	bookmarkAfterPause := s.bookmark
	handle := s.handle
	s.mu.Unlock()
	if stateAfterPause != pkgaudio.StatePaused {
		t.Fatalf("setup Pause: state = %v, want Paused", stateAfterPause)
	}
	if bookmarkAfterPause == nil || bookmarkAfterPause.Position != pausedAt {
		t.Fatalf("setup Pause: bookmark = %+v, want position %s", bookmarkAfterPause, pausedAt)
	}

	se.poison(handle, errors.New("engine resume failed: seek deadline exceeded"))

	res := m.Resume(ctx, id, pkgaudio.InvocationID(id+"-resume1"), 4)
	if res.Outcome != pkgaudio.OutcomeFailed {
		t.Fatalf("first Resume: outcome = %v, want Failed", res.Outcome)
	}

	s.mu.Lock()
	stateAfterFailure := s.state
	handleLoadedAfterFailure := s.handleLoaded
	bookmarkAfterFailure := s.bookmark
	s.mu.Unlock()
	if stateAfterFailure != pkgaudio.StatePaused {
		t.Fatalf("state after failed resume = %v, want still Paused", stateAfterFailure)
	}
	if handleLoadedAfterFailure {
		t.Fatal("handle after failed resume: still loaded, want dropped")
	}
	if bookmarkAfterFailure == nil || bookmarkAfterFailure.Position != pausedAt {
		t.Fatalf("bookmark after failed resume = %+v, want intact at position %s", bookmarkAfterFailure, pausedAt)
	}

	// The ordinary retry after a failed Resume is another Resume: the
	// session is still Paused, so that is what an operator or an
	// automated caller retrying under a fresh revision issues next —
	// never a Start, which this session's own paused-guard would refuse.
	retry := m.Resume(ctx, id, pkgaudio.InvocationID(id+"-resume2"), 5)

	s.mu.Lock()
	stateAfterRetry := s.state
	snap := s.snapshotLocked(ctx)
	s.mu.Unlock()

	if retry.Outcome == pkgaudio.OutcomeRefused || stateAfterRetry == pkgaudio.StateFailed {
		t.Fatalf("retry Resume after failed resume: outcome = %v (reason %q), state = %v — "+
			"an ordinary retry must recover without an explicit Prepare", retry.Outcome, retry.Reason, stateAfterRetry)
	}
	if stateAfterRetry != pkgaudio.StatePlaying {
		t.Fatalf("retry Resume after failed resume: state = %v, want Playing", stateAfterRetry)
	}
	if !snap.PositionKnown || snap.Position != pausedAt {
		t.Fatalf("position after recovered resume = %v (known=%v), want exactly %s, not 0", snap.Position, snap.PositionKnown, pausedAt)
	}
}

// TestExplicitPrepareRecoversAfterFailedStart pins Prepare's own
// release-and-reprepare path: an explicit Prepare, unlike a plain
// retry, has always released and re-prepared the handle, so it
// recovers a session stuck by a failed Start. Kept alongside the retry
// tests so a regression there is caught the same way. Uses the
// sticky-fault engine too, so this stays a meaningful check against a
// genuinely persisting handle fault, not just a one-shot injected error
// a plain retry would clear by accident.
func TestExplicitPrepareRecoversAfterFailedStart(t *testing.T) {
	ctx := context.Background()
	c := newClock(time.Now())
	m, se := newStickyFaultTestManager(t, c)
	id := pkgaudio.SessionID("sm226-prepare")

	ref := writeTestAsset(t, m.assetDir, "sm226-prepare.wav", "sm226-prepare", []byte("content"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}
	m.Apply(ctx, id, pkgaudio.InvocationID(id+"-apply"), 1, req)
	m.Prepare(ctx, id, pkgaudio.InvocationID(id+"-prepare1"), 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatalf("session %s was not created", id)
	}
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()

	se.poison(handle, errors.New("engine start failed: seek deadline exceeded"))

	if res := m.Start(ctx, id, pkgaudio.InvocationID(id+"-start1"), 3); res.Outcome != pkgaudio.OutcomeFailed {
		t.Fatalf("first Start: outcome = %v, want Failed", res.Outcome)
	}

	m.Prepare(ctx, id, pkgaudio.InvocationID(id+"-prepare2"), 4)
	s.mu.Lock()
	stateAfterPrepare := s.state
	s.mu.Unlock()
	if stateAfterPrepare != pkgaudio.StateReady {
		t.Fatalf("explicit Prepare after failed start: state = %v, want Ready", stateAfterPrepare)
	}

	m.Start(ctx, id, pkgaudio.InvocationID(id+"-start2"), 5)
	s.mu.Lock()
	stateAfterStart := s.state
	s.mu.Unlock()
	if stateAfterStart != pkgaudio.StatePlaying {
		t.Fatalf("Start after explicit Prepare: state = %v, want Playing", stateAfterStart)
	}
}

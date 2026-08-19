package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

func twoItemPlaylist(t *testing.T, dir string) pkgaudio.PlaylistRef {
	t.Helper()
	a := writeTestAsset(t, dir, "a.wav", "asset-a", []byte("aaa"))
	b := writeTestAsset(t, dir, "b.wav", "asset-b", []byte("bbb"))
	return pkgaudio.PlaylistRef{
		OwnerKind: "show", OwnerID: "night-session", OwnerRevision: 1,
		Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyRestart,
		RequestedTransition: pkgaudio.ItemTransitionSequential,
		Items: []pkgaudio.PlaylistItem{
			{ItemID: "item-a", Index: 0, Media: a},
			{ItemID: "item-b", Index: 1, Media: b},
		},
	}
}

// TestAdvanceCrashBeforePersist proves the pre-persist crash side: a
// crash before advanceLocked's persist call leaves the on-disk record
// unchanged, so a fresh Manager built from that store recovers to the
// item that was current before the advance was ever attempted — never
// skipped, never a partial transition.
func TestAdvanceCrashBeforePersist(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("night-session")

	playlist := twoItemPlaylist(t, dir)
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)

	s, _ := m.get(id)
	s.mu.Lock()
	if s.currentItemID != "item-a" || s.state != pkgaudio.StatePlaying {
		s.mu.Unlock()
		t.Fatalf("precondition: session not playing item-a: state=%s item=%s", s.state, s.currentItemID)
	}
	s.mu.Unlock()

	// Simulate a crash that occurs before advanceLocked's own persist —
	// nothing beyond the Start above was ever written for the advance
	// itself, so recovery must land on item-a exactly as it was left.
	fresh := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	if err := fresh.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	rs, ok := fresh.get(id)
	if !ok {
		t.Fatal("session not restored")
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.currentItemID != "item-a" {
		t.Fatalf("recovered current item = %q, want item-a (pre-advance)", rs.currentItemID)
	}
	if rs.state != pkgaudio.StatePlaying {
		t.Fatalf("recovered state = %q, want playing", rs.state)
	}
}

// TestAdvanceCrashAfterPersist proves the post-persist crash side: once
// advanceLocked's persist call has landed, recovery lands on the NEW
// current item and (re)starts it — never resuming or replaying the item
// that had already been advanced away from.
func TestAdvanceCrashAfterPersist(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("night-session")

	playlist := twoItemPlaylist(t, dir)
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)

	s, _ := m.get(id)
	s.mu.Lock()
	// Simulate advanceLocked's persist boundary directly: item-b is now
	// the persisted current item, but (simulating a crash) the engine was
	// never told to load or start it.
	s.currentIndex = 1
	s.currentItemID = "item-b"
	s.state = pkgaudio.StatePreparing
	s.bookmark = nil
	_ = s.persistLocked()
	s.mu.Unlock()

	fresh := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	if err := fresh.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	rs, ok := fresh.get(id)
	if !ok {
		t.Fatal("session not restored")
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.currentItemID != "item-b" {
		t.Fatalf("recovered current item = %q, want item-b (post-advance)", rs.currentItemID)
	}
	if rs.state != pkgaudio.StatePlaying {
		t.Fatalf("recovered state = %q, want playing (restore starts the persisted-but-never-started item)", rs.state)
	}
	if !rs.handleLoaded {
		t.Fatal("recovered session has no engine handle for item-b")
	}
}

// TestNaturalCompletionAdvancesExactlyOnceAndDiffersFromStop proves ruling
// 3: natural completion and a commanded stop remain distinguishable, and
// a completed first item advances to the second exactly once even across
// several extra watcher ticks.
func TestNaturalCompletionAdvancesExactlyOnceAndDiffersFromStop(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("night-session")

	playlist := twoItemPlaylist(t, dir)
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)

	c.advance(3 * time.Second) // past item-a's 2s duration

	// Several ticks in a row must advance exactly once, not once per tick.
	for i := 0; i < 5; i++ {
		m.watchTick(ctx)
	}

	s, _ := m.get(id)
	s.mu.Lock()
	if s.currentItemID != "item-b" {
		s.mu.Unlock()
		t.Fatalf("current item after natural completion = %q, want item-b", s.currentItemID)
	}
	if s.state != pkgaudio.StatePlaying {
		s.mu.Unlock()
		t.Fatalf("state after auto-advance = %q, want playing", s.state)
	}
	s.mu.Unlock()

	// Now let item-b run out too: with RepeatNone and no further item,
	// the session reaches Completed — never Stopped, which is reserved
	// for a COMMANDED stop.
	c.advance(3 * time.Second)
	m.watchTick(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StateCompleted {
		t.Fatalf("state at end of playlist = %q, want completed (never stopped)", s.state)
	}
}

// TestNaturalCompletionOfASingleMediaSession proves the non-playlist
// path the previous test's playlist cannot exercise: applied via
// ApplyRequest.Media rather than a PlaylistRef, currentItemLocked treats
// the single ref as a one-item virtual playlist, but advanceLocked used
// to refuse unconditionally whenever s.desired.Playlist was nil — which
// is every Media-only session, always — so watchTick's call to
// advanceLocked(ctx, false) on natural completion did nothing, and the
// session reported Playing forever after the engine actually finished.
// This asserts the fix: unforced advance on a Media-only session moves
// it to Completed exactly like a playlist running off its end with no
// repeat.
func TestNaturalCompletionOfASingleMediaSession(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("single-media-session")

	ref := writeTestAsset(t, dir, "solo.wav", "asset-solo", []byte("solo"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	c.advance(3 * time.Second) // past the asset's 2s duration

	// Several ticks in a row must land on Completed exactly once, never
	// bounce back to Playing or stay stuck there.
	for i := 0; i < 5; i++ {
		m.watchTick(ctx)
	}

	s, _ := m.get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StateCompleted {
		t.Fatalf("state after a single-media session's natural completion = %q, want completed", s.state)
	}
}

func TestCommandedStopIsDistinctFromCompletion(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-2", 2)
	m.Stop(ctx, id, "inv-3", 3)

	s, _ := m.get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StateStopped {
		t.Fatalf("state after Stop = %q, want stopped", s.state)
	}
}

// checkpointEngine wraps [FakeEngine] and calls onLoad synchronously
// inside Load, before returning — used to observe exactly what
// [SessionStore] holds at the instant the engine is first told about a
// newly-advanced item, proving advanceLocked's persist genuinely
// happens BEFORE the engine call rather than merely "somewhere in the
// same function".
type checkpointEngine struct {
	*FakeEngine
	onLoad func(handle EngineHandle)
}

func (e *checkpointEngine) Load(ctx context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	if e.onLoad != nil {
		e.onLoad(handle)
	}
	return e.FakeEngine.Load(ctx, handle, media, duration)
}

// TestAdvanceLockedPersistsBeforeTellingTheEngine directly proves the
// ordering [TestAdvanceCrashBeforePersist]/[TestAdvanceCrashAfterPersist]
// only prove the CONSEQUENCE of: advanceLocked's own persist call must
// run, and complete, before it ever calls Engine.Load for the newly
// current item.
func TestAdvanceLockedPersistsBeforeTellingTheEngine(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	fake := NewFakeEngine(c.now)
	const id = pkgaudio.SessionID("night-session")

	var sawPersistedItem string
	engine := &checkpointEngine{FakeEngine: fake, onLoad: func(handle EngineHandle) {
		if handle != EngineHandle(string(id)+"/item-b") {
			return
		}
		rec, ok, err := store.Load(id)
		if err != nil || !ok {
			t.Errorf("checkpointEngine.Load(item-b): store.Load failed: ok=%v err=%v", ok, err)
			return
		}
		sawPersistedItem = rec.CurrentItemID
	}}

	m := NewManager(engine, store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	playlist := twoItemPlaylist(t, dir)
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)
	m.Advance(ctx, id, "inv-advance", 3)

	if sawPersistedItem != "item-b" {
		t.Fatalf("at the moment the engine was told to load item-b, the persisted current item was %q, want item-b — the persist did not happen before the engine call", sawPersistedItem)
	}
}

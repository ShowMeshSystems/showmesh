package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// gapDelayEngine wraps [FakeEngine] and advances clk by loadDelay
// immediately before every Start — simulating real load/start latency
// between an item's completion evidence and its successor's confirmed
// start evidence. loadDelay is the independently known ground truth this
// package's gap tests check the measured value against: it is injected
// directly by the test, never read back from anything the production
// code itself computed.
type gapDelayEngine struct {
	*FakeEngine
	clk       *clock
	loadDelay time.Duration
}

func (e *gapDelayEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.clk.advance(e.loadDelay)
	return e.FakeEngine.Start(ctx, handle, position)
}

// TestItemGapMeasuredFromEngineEvidence proves the gap is a genuine
// measurement: the interval between the completion Observe call's own
// ObservedAt and the successor Start's own ObservedAt, checked against
// loadDelay — a value this test controls directly and the production
// code never sees — rather than against any number advanceLocked itself
// derived.
func TestItemGapMeasuredFromEngineEvidence(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	const loadDelay = 137 * time.Millisecond
	engine := &gapDelayEngine{FakeEngine: NewFakeEngine(c.now), clk: c, loadDelay: loadDelay}
	m := NewManager(engine, store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("gap-session")

	playlist := twoItemPlaylist(t, dir)
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)

	c.advance(3 * time.Second) // past item-a's 2s duration
	completionEvidenceAt := c.now()
	m.watchTick(ctx)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentItemID != "item-b" {
		t.Fatalf("current item after natural completion = %q, want item-b", s.currentItemID)
	}
	if !s.gapKnown {
		t.Fatalf("gap not known after a natural-completion advance: reason=%q", s.gapReason)
	}
	if s.gap != loadDelay {
		t.Fatalf("measured gap = %v, want %v (the independently injected load delay)", s.gap, loadDelay)
	}
	wantObservedAt := completionEvidenceAt.Add(loadDelay)
	if !s.gapObservedAt.Equal(wantObservedAt) {
		t.Fatalf("gap observed-at = %v, want %v (the successor Start's own evidence time)", s.gapObservedAt, wantObservedAt)
	}
	if s.gapReason != "" {
		t.Fatalf("gap reason = %q, want empty since the gap is known", s.gapReason)
	}
}

// TestItemGapUnknownBeforeAnyAdvance proves the first-item / never-
// advanced case: a session that has started but never advanced reports
// the gap unknown with a stated reason, never zero.
func TestItemGapUnknownBeforeAnyAdvance(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	playlist := twoItemPlaylist(t, m.assetDir)
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)

	s, _ := m.get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gapKnown {
		t.Fatalf("gap known = true before any advance, want false")
	}
	if s.gapReason == "" {
		t.Fatal("gap reason is empty, want a stated reason")
	}
}

// TestItemGapUnknownOnForcedAdvance proves an operator-forced advance —
// whose predecessor did not complete naturally — never reports a
// measured gap, even when a genuine measurement was standing from an
// earlier natural completion: the forced advance must clear it, not
// leave a stale number in place.
func TestItemGapUnknownOnForcedAdvance(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	engine := &gapDelayEngine{FakeEngine: NewFakeEngine(c.now), clk: c, loadDelay: 20 * time.Millisecond}
	m := NewManager(engine, store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	playlist := twoItemPlaylist(t, dir)
	playlist.Repeat = pkgaudio.RepeatPlaylist // so a forced advance past item-b has somewhere to land
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)

	c.advance(3 * time.Second) // past item-a's 2s duration: natural completion to item-b
	m.watchTick(ctx)

	s, _ := m.get(id)
	s.mu.Lock()
	if s.currentItemID != "item-b" || !s.gapKnown {
		s.mu.Unlock()
		t.Fatalf("precondition: want item-b with a known gap, got item=%q gapKnown=%v", s.currentItemID, s.gapKnown)
	}
	s.mu.Unlock()

	// An operator skip now, not a natural completion.
	m.Advance(ctx, id, "inv-advance", 3)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentItemID != "item-a" {
		t.Fatalf("current item after forced advance = %q, want item-a (playlist wrapped)", s.currentItemID)
	}
	if s.gapKnown {
		t.Fatal("gap known = true after a forced advance, want false — the earlier natural-completion measurement must not survive it")
	}
	if s.gapReason == "" {
		t.Fatal("gap reason is empty, want a stated reason")
	}
}

// TestItemGapUnknownAfterStop proves a stopped session never reports a
// stale gap value from before it stopped.
func TestItemGapUnknownAfterStop(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	const loadDelay = 50 * time.Millisecond
	engine := &gapDelayEngine{FakeEngine: NewFakeEngine(c.now), clk: c, loadDelay: loadDelay}
	m := NewManager(engine, store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("gap-session")

	playlist := twoItemPlaylist(t, dir)
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)
	c.advance(3 * time.Second)
	m.watchTick(ctx)

	s, _ := m.get(id)
	s.mu.Lock()
	if !s.gapKnown {
		s.mu.Unlock()
		t.Fatal("precondition: gap should be known before Stop")
	}
	s.mu.Unlock()

	m.Stop(ctx, id, "inv-stop", 3)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gapKnown {
		t.Fatal("gap known = true after Stop, want false — a stopped session must not report a stale measurement")
	}
	if s.gapReason == "" {
		t.Fatal("gap reason is empty, want a stated reason")
	}
}

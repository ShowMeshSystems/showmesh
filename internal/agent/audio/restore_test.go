package audio

import (
	"context"
	"sync"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestRestorePausedRestartPolicyIgnoresBookmark verifies that a paused
// playlist session whose ResumePolicy is "restart" comes back from a
// restart at position 0, not at its bookmarked position — restoreOne's
// Paused branch must gate resolveBookmarkPositionLocked on
// Resume == ResumePolicyResume exactly as its Playing/Preparing sibling
// branch already does. Before the fix, the Paused branch called
// resolveBookmarkPositionLocked unconditionally, so a restart session
// resumed from its old position instead of restarting from 0.
func TestRestorePausedRestartPolicyIgnoresBookmark(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("night-session")

	playlist := twoItemPlaylist(t, dir) // Resume: ResumePolicyRestart
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)

	c.advance(3 * time.Second)
	if r := m.Pause(ctx, id, "inv-pause", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("pause unexpectedly refused: %+v", r)
	}
	s, _ := m.get(id)
	s.mu.Lock()
	if s.state != pkgaudio.StatePaused || s.bookmark == nil || s.bookmark.Position <= 0 {
		s.mu.Unlock()
		t.Fatalf("precondition: session should be paused with a positive bookmark; got state=%s bookmark=%+v", s.state, s.bookmark)
	}
	s.mu.Unlock()

	// "Restart": a fresh Manager and a fresh Engine over the same store,
	// matching TestRestartThenResumeRecoversAPausedSession's own rig.
	m2 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	s2, ok := m2.get(id)
	if !ok {
		t.Fatal("session was not restored")
	}
	s2.mu.Lock()
	restoredState, handle := s2.state, s2.handle
	s2.mu.Unlock()
	if restoredState != pkgaudio.StatePaused {
		t.Fatalf("restored state = %q, want paused", restoredState)
	}

	obs, err := m2.engine.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Position != 0 {
		t.Fatalf("restored position = %v, want 0 — a \"restart\" resume policy must never resume from the bookmark", obs.Position)
	}
}

// TestStartRefusesBookmarkFromADifferentMediaAsset verifies that pausing
// media A, replacing it with media B via a second Apply on the same
// (media, non-playlist) session, and then Start must not resume B from
// A's bookmarked position: [Session.resolveBookmarkPositionLocked] must
// compare the SAME ItemID|AssetID|ContentHash identity
// [Manager.Start] already uses to decide a loaded engine handle is
// stale, not ItemID alone — a media session's ItemID is always the
// constant "media", so an ItemID-only comparison never distinguishes
// one Apply'd asset from another.
func TestStartRefusesBookmarkFromADifferentMediaAsset(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	refA := writeTestAsset(t, m.assetDir, "a.wav", "asset-a", []byte("content-a"))
	startPlaying(t, m, ctx, id, refA, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	c.advance(1500 * time.Millisecond)

	if r := m.Pause(ctx, id, "inv-pause", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("pause unexpectedly refused: %+v", r)
	}
	s, _ := m.get(id)
	s.mu.Lock()
	if s.bookmark == nil || s.bookmark.Position < 1400*time.Millisecond {
		s.mu.Unlock()
		t.Fatalf("precondition: expected a bookmark near 1.5s, got %+v", s.bookmark)
	}
	s.mu.Unlock()

	refB := writeTestAsset(t, m.assetDir, "b.wav", "asset-b", []byte("content-b"))
	if r := m.Apply(ctx, id, "inv-apply-b", 4, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(refB)}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply B unexpectedly refused: %+v", r)
	}

	// A stale bookmark refuses Start rather than silently seeking into it
	// (Manager.Start's own "Visible and self-healing" comment) and clears
	// itself so the very next Start is not refused forever by the same
	// dead reference — so this asserts the refusal, then starts again.
	if r := m.Start(ctx, id, "inv-start-2", 5); r.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("start B against asset-a's bookmark = %+v, want refused (stale bookmark identity)", r)
	}
	if r := m.Start(ctx, id, "inv-start-3", 6); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start B unexpectedly refused after the stale bookmark was cleared: %+v", r)
	}

	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()
	obs, err := m.engine.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Position != 0 {
		t.Fatalf("position after starting a different asset = %v, want 0 — a bookmark taken against asset-a must never seek asset-b", obs.Position)
	}
}

// releaseCountingEngine wraps [FakeEngine] and counts calls to Release —
// for a test to prove a handle was actually released, not merely
// overwritten by the next Load using the same key.
type releaseCountingEngine struct {
	*FakeEngine
	mu       sync.Mutex
	releases int
}

func (e *releaseCountingEngine) Release(ctx context.Context, handle EngineHandle) error {
	e.mu.Lock()
	e.releases++
	e.mu.Unlock()
	return e.FakeEngine.Release(ctx, handle)
}

func (e *releaseCountingEngine) releaseCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.releases
}

// TestRestoreAllIsIdempotent verifies that calling [Manager.RestoreAll]
// twice on the same Manager — two consecutive crashes during startup, or
// a hot reload — releases the previous in-memory session's engine handle
// before rebuilding it, rather than leaking it, and still leaves exactly
// one session behind in the same state either call alone would produce.
func TestRestoreAllIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, dir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Media:      pkgaudio.SetField(ref),
	})
	if r := m.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start: unexpectedly refused: %+v", r)
	}

	engine := &releaseCountingEngine{FakeEngine: NewFakeEngine(c.now)}
	m2 := NewManager(engine, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)

	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("first RestoreAll: %v", err)
	}
	s1, ok := m2.get(id)
	if !ok {
		t.Fatal("session was not restored on the first call")
	}
	s1.mu.Lock()
	handle1 := s1.handle
	s1.mu.Unlock()

	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("second RestoreAll: %v", err)
	}
	if engine.releaseCount() == 0 {
		t.Fatal("second RestoreAll never released the first call's engine handle — it leaked")
	}

	s2, ok := m2.get(id)
	if !ok {
		t.Fatal("session missing after the second restore")
	}
	s2.mu.Lock()
	state2, handle2 := s2.state, s2.handle
	s2.mu.Unlock()
	if state2 != pkgaudio.StatePlaying {
		t.Fatalf("state after the second RestoreAll = %q, want Playing", state2)
	}
	if handle2 != handle1 {
		t.Fatalf("handle identity changed across restores: %q vs %q, want the same logical handle key", handle1, handle2)
	}

	m2.mu.Lock()
	count := len(m2.sessions)
	m2.mu.Unlock()
	if count != 1 {
		t.Fatalf("session count after two RestoreAll calls = %d, want 1", count)
	}
}

// TestRestoreAllResetsLTCOwnershipBeforeRebuilding verifies that
// [Manager.RestoreAll] clears any standing LTC ownership before it starts
// rebuilding sessions — a prior run that failed mid-loop can otherwise
// leave ownership pointing at a session identity this run has no way to
// address, permanently blocking every future claim.
func TestRestoreAllResetsLTCOwnershipBeforeRebuilding(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, dir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Media:      pkgaudio.SetField(ref),
	})
	if r := m.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start: unexpectedly refused: %+v", r)
	}

	m2 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	configureLTC(m2, pkgaudio.LTCFrameRate30, "00:00:00:00")

	// Simulate a prior RestoreAll that failed partway through, leaving
	// ownership pointing at a session identity this run has never heard
	// of — nothing else on this path ever clears it.
	m2.ltc.id, m2.ltc.owned = "stale-owner", true

	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	m2.ltc.mu.Lock()
	owner, owned := m2.ltc.id, m2.ltc.owned
	m2.ltc.mu.Unlock()
	if !owned || owner != id {
		t.Fatalf("LTC ownership after RestoreAll = (id=%q owned=%v), want (id=%q owned=true): a stale claim from a prior failed run must not survive", owner, owned, id)
	}
}

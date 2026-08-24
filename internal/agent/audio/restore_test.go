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

// TestRestoreAllWithNoEngineBoundDoesNotFailPersistedState reproduces the
// node-reboot defect: RestoreAll always runs at agent startup before any
// audio.node binding can arrive over MQTT, so the real production Manager
// is built with a [SwitchableEngine] that has never had Set called on it.
// A restore that hits this "no binding yet" condition must not convert
// the ON-DISK persisted Playing session to Failed — that conversion is
// permanent damage a later binding cannot undo, because
// invalidateActiveSessions skips Failed sessions and nothing retries.
func TestRestoreAllWithNoEngineBoundDoesNotFailPersistedState(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)

	// First "boot": a real, available engine, session reaches Playing,
	// and its Playing record lands on disk.
	m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("reboot-session")

	ref := writeTestAsset(t, dir, "reboot.wav", "asset-reboot", []byte("content-reboot"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start: unexpectedly refused: %+v", r)
	}

	rec, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("precondition: persisted record missing after start: ok=%v err=%v", ok, err)
	}
	if rec.SessionState != pkgaudio.StatePlaying {
		t.Fatalf("precondition: persisted state = %q, want Playing", rec.SessionState)
	}

	// "Reboot": a fresh Manager built with a SwitchableEngine that has
	// never had Set called on it, exactly matching internal/agent/agent.go's
	// real startup ordering (RestoreAll runs before any audio.node binding
	// can arrive).
	switchable := NewSwitchableEngine()
	m2 := NewManager(switchable, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	rec2, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("persisted record missing after RestoreAll: ok=%v err=%v", ok, err)
	}
	if rec2.SessionState == pkgaudio.StateFailed {
		t.Fatalf("persisted state after a no-engine-bound RestoreAll = Failed, want the persisted desired state (Playing) to survive a reboot that happens before an audio.node binding arrives")
	}

	// Before the binding arrives, the session must be left exactly as
	// deferred: still Playing in memory (never Failed), but with no
	// engine handle actually loaded — leaving it reported "Playing" with
	// a handle would be its own lie.
	s2, ok := m2.get(id)
	if !ok {
		t.Fatalf("session %s missing from the rebooted manager", id)
	}
	s2.mu.Lock()
	stateBeforeBind, handleLoadedBeforeBind := s2.state, s2.handleLoaded
	s2.mu.Unlock()
	if stateBeforeBind != pkgaudio.StatePlaying {
		t.Fatalf("in-memory state before any binding = %q, want Playing (deferred, not failed)", stateBeforeBind)
	}
	if handleLoadedBeforeBind {
		t.Fatalf("session reports a loaded engine handle before any binding arrived, want none")
	}

	// Now the binding arrives, through the same [Manager.RebindEngine]
	// path production code uses (internal/agent/audioengine.go's
	// rebuild), and the deferred restore must actually fire: the session
	// must reach a REAL loaded, playing handle on the newly bound
	// engine, not merely retain its old, undisturbed in-memory state.
	real := NewFakeEngine(c.now)
	m2.RebindEngine(switchable, real, "audio.node binding delivered")

	// restoreOne (invoked again by the retry) replaces m2.sessions[id]
	// with a fresh *Session, exactly as it does on any restore — the
	// pointer captured before the bind is now stale, so re-fetch it
	// rather than asserting against the old, now-abandoned object.
	s2, ok = m2.get(id)
	if !ok {
		t.Fatalf("session %s missing from the manager after the deferred restore retried", id)
	}
	s2.mu.Lock()
	stateAfterBind, handleLoadedAfterBind, handle := s2.state, s2.handleLoaded, s2.handle
	s2.mu.Unlock()
	if stateAfterBind != pkgaudio.StatePlaying {
		t.Fatalf("in-memory session state after binding an engine = %q, want Playing (a later binding must resume the session, not leave it stuck)", stateAfterBind)
	}
	if !handleLoadedAfterBind {
		t.Fatalf("session has no loaded engine handle after the deferred restore should have fired")
	}
	if _, err := real.Observe(ctx, handle); err != nil {
		t.Fatalf("Observe on the resumed handle: %v (the deferred restore did not actually start playback on the newly bound engine)", err)
	}
}

// TestRestoreAllWithNoEngineBoundStillFailsAGenuineFailure proves "no
// engine bound yet" and "restore genuinely failed" stay distinct: a
// session whose asset is missing must still end up Failed on disk even
// while restoreOne never reaches the engine at all — prepareLocked's own
// ProbeAsset runs before any engine call, so this failure has nothing to
// do with SwitchableEngine's binding state, and deferring it would just
// hide a real defect (a lost asset) as a false "not yet".
func TestRestoreAllWithNoEngineBoundStillFailsAGenuineFailure(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)

	m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("missing-asset-session")

	ref := writeTestAsset(t, dir, "missing.wav", "asset-missing", []byte("content-missing"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start: unexpectedly refused: %+v", r)
	}
	if err := removeTestAsset(dir, "missing.wav"); err != nil {
		t.Fatalf("removeTestAsset: %v", err)
	}

	// "Reboot", same as the sibling test: a fresh Manager, an unbound
	// SwitchableEngine, no audio.node binding has arrived.
	switchable := NewSwitchableEngine()
	m2 := NewManager(switchable, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	rec, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("persisted record missing after RestoreAll: ok=%v err=%v", ok, err)
	}
	if rec.SessionState != pkgaudio.StateFailed {
		t.Fatalf("persisted state after restoring a session with a missing asset = %q, want Failed: a genuine restore failure must still be reported, not silently deferred as \"not yet\"", rec.SessionState)
	}

	m2.mu.Lock()
	_, pending := m2.pendingEngineRestore[id]
	m2.mu.Unlock()
	if pending {
		t.Fatalf("session %s is queued for a deferred retry after a genuine (non-engine-binding) failure, want it left resolved as Failed", id)
	}
}

// TestRebindEngineRetriesDeferredRestoreExactlyOnce proves the deferred
// restore fires exactly once per session: a second RebindEngine after
// the first successful retry must not re-run restoreOne against the
// already-resumed session. Instead the ordinary invalidateActiveSessions
// path handles it, exactly as it would for any other in-flight session
// on a genuine rebind — proving there is no lingering pendingEngineRestore
// entry left to double-fire on.
func TestRebindEngineRetriesDeferredRestoreExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)

	m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("double-bind-session")

	ref := writeTestAsset(t, dir, "double.wav", "asset-double", []byte("content-double"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start: unexpectedly refused: %+v", r)
	}

	switchable := NewSwitchableEngine()
	m2 := NewManager(switchable, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	m2.mu.Lock()
	_, pendingBeforeBind := m2.pendingEngineRestore[id]
	m2.mu.Unlock()
	if !pendingBeforeBind {
		t.Fatalf("session %s not queued for deferred restore before any binding, want it pending", id)
	}

	first := NewFakeEngine(c.now)
	m2.RebindEngine(switchable, first, "first audio.node binding")

	m2.mu.Lock()
	_, pendingAfterFirstBind := m2.pendingEngineRestore[id]
	m2.mu.Unlock()
	if pendingAfterFirstBind {
		t.Fatalf("session %s still queued for deferred restore after the first bind resolved it, want it cleared", id)
	}

	s2, ok := m2.get(id)
	if !ok {
		t.Fatalf("session %s missing after the first bind", id)
	}
	s2.mu.Lock()
	stateAfterFirstBind, handleLoadedAfterFirstBind := s2.state, s2.handleLoaded
	s2.mu.Unlock()
	if stateAfterFirstBind != pkgaudio.StatePlaying || !handleLoadedAfterFirstBind {
		t.Fatalf("state after first bind = (state=%s handleLoaded=%v), want (Playing, true)", stateAfterFirstBind, handleLoadedAfterFirstBind)
	}

	// A second, genuine rebind: the session now has a real handle on
	// "first", so the ordinary invalidateActiveSessions path must fail
	// it as a route change — not restoreOne being invoked a second time
	// against a session that was never deferred on this bind.
	second := NewFakeEngine(c.now)
	m2.RebindEngine(switchable, second, RebindReasonEngineRebind)

	s2.mu.Lock()
	stateAfterSecondBind, faultAfterSecondBind := s2.state, s2.fault
	s2.mu.Unlock()
	if stateAfterSecondBind != pkgaudio.StateFailed || faultAfterSecondBind != pkgaudio.FaultRouteChanged {
		t.Fatalf("state after a second, genuine rebind = (state=%s fault=%s), want (Failed, %s): the ordinary invalidate-on-rebind path, not a re-fired deferred restore", stateAfterSecondBind, faultAfterSecondBind, pkgaudio.FaultRouteChanged)
	}

	m2.mu.Lock()
	pendingCount := len(m2.pendingEngineRestore)
	m2.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pendingEngineRestore has %d entries after the second bind, want 0 (nothing left to retry twice)", pendingCount)
	}
}

// TestRestoreAllWithNoEngineBoundDeferrsAPausedSession is the Paused
// branch's own copy of
// TestRestoreAllWithNoEngineBoundDoesNotFailPersistedState: restoreOne's
// Paused branch has its own, separate prepareLocked call and its own
// no-binding check, so a Paused session's persisted record must survive
// a reboot before any binding arrives exactly as a Playing one does, and
// must actually resume (Paused, with a loaded handle) once the binding
// lands.
func TestRestoreAllWithNoEngineBoundDeferrsAPausedSession(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)

	m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("paused-reboot-session")

	playlist := twoItemPlaylist(t, dir)
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m1.Start(ctx, id, "inv-start", 2)
	c.advance(3 * time.Second)
	if r := m1.Pause(ctx, id, "inv-pause", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("pause unexpectedly refused: %+v", r)
	}

	rec, ok, err := store.Load(id)
	if err != nil || !ok || rec.SessionState != pkgaudio.StatePaused {
		t.Fatalf("precondition: persisted state = %q ok=%v err=%v, want Paused", rec.SessionState, ok, err)
	}

	switchable := NewSwitchableEngine()
	m2 := NewManager(switchable, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	rec2, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("persisted record missing after RestoreAll: ok=%v err=%v", ok, err)
	}
	if rec2.SessionState == pkgaudio.StateFailed {
		t.Fatalf("persisted state after a no-engine-bound RestoreAll of a Paused session = Failed, want the persisted desired state (Paused) to survive")
	}

	real := NewFakeEngine(c.now)
	m2.RebindEngine(switchable, real, "audio.node binding delivered")

	s2, ok := m2.get(id)
	if !ok {
		t.Fatalf("session %s missing after the deferred restore retried", id)
	}
	s2.mu.Lock()
	state, handleLoaded, handle := s2.state, s2.handleLoaded, s2.handle
	s2.mu.Unlock()
	if state != pkgaudio.StatePaused {
		t.Fatalf("in-memory state after binding an engine = %q, want Paused", state)
	}
	if !handleLoaded {
		t.Fatalf("session has no loaded engine handle after the deferred restore should have fired")
	}
	if _, err := real.Observe(ctx, handle); err != nil {
		t.Fatalf("Observe on the resumed handle: %v (the deferred restore did not actually reload the Paused session on the newly bound engine)", err)
	}
}

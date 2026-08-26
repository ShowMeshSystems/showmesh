package audio

import (
	"context"
	"fmt"
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
	m2.RebindEngine(ctx, switchable, real, "audio.node binding delivered")

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
	m2.RebindEngine(ctx, switchable, first, "first audio.node binding")

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
	m2.RebindEngine(ctx, switchable, second, RebindReasonEngineRebind)

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

// TestRestoreAllWithNoEngineBoundDefersAPausedSession is the Paused
// branch's own copy of
// TestRestoreAllWithNoEngineBoundDoesNotFailPersistedState: restoreOne's
// Paused branch has its own, separate prepareLocked call and its own
// no-binding check, so a Paused session's persisted record must survive
// a reboot before any binding arrives exactly as a Playing one does, and
// must actually resume (Paused, with a loaded handle) once the binding
// lands.
func TestRestoreAllWithNoEngineBoundDefersAPausedSession(t *testing.T) {
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
	m2.RebindEngine(ctx, switchable, real, "audio.node binding delivered")

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

// TestRebindEngineConcurrentBindingsNeverPersistAnUnexplainedFailure
// reproduces the concurrency defect an adversarial review found in the
// first version of this fix: RebindEngine's invalidate/Set/retry was
// not one atomic operation, so two audio.node.configure commands
// delivered back to back (genuinely concurrent — MQTT dispatches each
// inbound command on its own goroutine) could interleave. One call's
// invalidateActiveSessions could run against a pre-swap snapshot while
// a second call's retry started a deferred session against the WRONG
// engine, and that session's own Start then failed against an engine
// that never saw its handle — persisting Failed with NO fault recorded
// (fault=none, reason=""), which is worse evidence than the original
// defect: an operator sees a dead session with no explanation at all.
//
// A LEGITIMATE two-binding sequence can still end a session Failed
// (recorded, not fixed, as a pre-existing rebind contract — see
// TestRebindEngineRetriesDeferredRestoreExactlyOnce and this package's
// PR description), but that path always goes through
// invalidateActiveSessions, which always calls setFaultLocked with
// [pkgaudio.FaultRouteChanged] and a real reason. So Failed with
// Fault == FaultNone is never legitimate — it is exactly the race
// signature. This test asserts that signature never appears across many
// concurrent-binding iterations, run under go test -race to also catch
// the underlying data race directly.
func TestRebindEngineConcurrentBindingsNeverPersistAnUnexplainedFailure(t *testing.T) {
	const iterations = 300
	const concurrentBindings = 12

	for iter := 0; iter < iterations; iter++ {
		dir := t.TempDir()
		c := newClock(time.Now())
		store := NewFileSessionStore(dir)
		ctx := context.Background()

		ids := []pkgaudio.SessionID{"s1", "s2", "s3"}
		m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
		for _, id := range ids {
			ref := writeTestAsset(t, dir, string(id)+fmt.Sprintf("-%d.wav", iter), "asset-"+string(id), []byte("content-"+string(id)))
			m1.Apply(ctx, id, pkgaudio.InvocationID(string(id)+"-apply"), 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
			if r := m1.Start(ctx, id, pkgaudio.InvocationID(string(id)+"-start"), 2); r.Outcome == pkgaudio.OutcomeRefused {
				t.Fatalf("iter %d: start for %s unexpectedly refused: %+v", iter, id, r)
			}
		}

		switchable := NewSwitchableEngine()
		m2 := NewManager(switchable, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
		if err := m2.RestoreAll(ctx); err != nil {
			t.Fatalf("iter %d: RestoreAll: %v", iter, err)
		}

		var wg sync.WaitGroup
		for i := 0; i < concurrentBindings; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				m2.RebindEngine(ctx, switchable, NewFakeEngine(c.now), fmt.Sprintf("concurrent binding %d", i))
			}(i)
		}
		wg.Wait()

		for _, id := range ids {
			rec, ok, err := store.Load(id)
			if err != nil || !ok {
				t.Fatalf("iter %d: persisted record missing for %s: ok=%v err=%v", iter, id, ok, err)
			}
			if rec.SessionState == pkgaudio.StateFailed && rec.Fault == pkgaudio.FaultNone {
				t.Fatalf("iter %d: session %s persisted as Failed with no fault (fault=%q reason=%q) — this is the interleaved-engine race signature, never a legitimate rebind invalidation", iter, id, rec.Fault, rec.FaultReason)
			}
		}
	}
}

// gatedLoadEngine wraps [FakeEngine] so a test can pause a Load call
// mid-flight (after the wrapped engine's own Load has actually
// succeeded) without holding any lock the test itself needs — unlike
// gating inside a real restoreOne call, which runs entirely under the
// session's own s.mu, so a second goroutine trying to inspect that same
// session (as invalidateActiveSessions does) would deadlock rather than
// race. That structural fact is itself why a full end-to-end race
// reproduction between two concurrent RebindEngine calls is not
// attempted here: any real interleaving window is either already
// serialized by a lock the mutex-under-test does not need to duplicate
// (session-level s.mu, or the pendingEngineRestore drain under m.mu),
// or requires reproducing a window of a few pure allocations with
// nothing to gate on. What IS directly testable, and is the actual
// property rebindMu exists to guarantee, is that no other RebindEngine
// call's body can make ANY progress — not even past
// invalidateActiveSessions — while this one is still inside
// retryDeferredRestores. That is what this test checks, via
// sync.Mutex.TryLock on the SAME rebindMu the production code uses
// (this file is in package audio, so rebindMu is reachable directly).
type gatedLoadEngine struct {
	*FakeEngine
	loadReached chan struct{}
	release     chan struct{}
}

func newGatedLoadEngine(now func() time.Time) *gatedLoadEngine {
	return &gatedLoadEngine{FakeEngine: NewFakeEngine(now), loadReached: make(chan struct{}), release: make(chan struct{})}
}

func (e *gatedLoadEngine) Load(ctx context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	obs, err := e.FakeEngine.Load(ctx, handle, media, duration)
	if err == nil {
		close(e.loadReached)
		<-e.release
	}
	return obs, err
}

// TestRebindEngineHoldsRebindMuAcrossItsWholeBody proves rebindMu is
// still held partway through RebindEngine's retryDeferredRestores step
// — deep inside the critical section, well after invalidateActiveSessions
// and engine.Set have already run — not merely across the first line or
// two. A goroutine holds RebindEngine paused (via gatedLoadEngine) at
// exactly that point; the test then calls rebindMu.TryLock() itself: it
// must fail, proving the mutex is genuinely still held, not merely
// acquired-and-released early. Without rebindMu (asserted below by
// reverting it), TryLock succeeds while RebindEngine is still paused —
// this fails.
func TestRebindEngineHoldsRebindMuAcrossItsWholeBody(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)
	ctx := context.Background()
	const id = pkgaudio.SessionID("mutex-proof-session")

	m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ref := writeTestAsset(t, dir, "mutex-proof.wav", "asset-mutex-proof", []byte("content-mutex-proof"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start unexpectedly refused: %+v", r)
	}

	switchable := NewSwitchableEngine()
	m2 := NewManager(switchable, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	gated := newGatedLoadEngine(c.now)
	done := make(chan struct{})
	go func() {
		defer close(done)
		m2.RebindEngine(ctx, switchable, gated, "binding under test")
	}()

	<-gated.loadReached // RebindEngine is now paused inside retryDeferredRestores, deep past invalidate+Set.
	if m2.rebindMu.TryLock() {
		m2.rebindMu.Unlock()
		close(gated.release)
		<-done
		t.Fatal("rebindMu.TryLock() succeeded while a RebindEngine call was still paused mid-retry — the mutex is not held across the whole body")
	}
	close(gated.release)
	<-done
}

// TestStartDuringBootWindowRefusesRatherThanFails proves Manager.Start
// treats "no engine bound yet" as a refusal, not a permanent failure,
// for a session that never went through restoreOne at all — a fresh
// session Applied and Started while this node has not yet received its
// audio.node binding, the same boot window restoreOne's own deferral
// exists for. Before this fix, Start's own prepareLocked/engine.Start
// failure paths were untouched by the rest of this change: they set
// StateFailed and dispatch always persists after exec(), so the
// persisted record was overwritten with Failed even though
// pendingEngineRestore still held the session — the retry that
// eventually ran found Failed on disk, fell through restoreOne's
// switch, and did nothing.
func TestStartDuringBootWindowRefusesRatherThanFails(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)
	switchable := NewSwitchableEngine()
	m := NewManager(switchable, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("boot-window-session")

	ref := writeTestAsset(t, dir, "boot.wav", "asset-boot", []byte("content-boot"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})

	r := m.Start(ctx, id, "inv-start", 2)
	if r.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("Start outcome with no engine bound = %s (reason=%q), want Refused", r.Outcome, r.Reason)
	}

	rec, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("persisted record missing: ok=%v err=%v", ok, err)
	}
	if rec.SessionState == pkgaudio.StateFailed {
		t.Fatalf("persisted state after Start with no engine bound = Failed, want the pre-Start state to survive so the pending retry can still resume it")
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatalf("session %s missing", id)
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state == pkgaudio.StateFailed {
		t.Fatalf("in-memory state after Start with no engine bound = Failed, want it left unchanged")
	}

	// Now bind: a real command retried after the binding arrives must
	// actually reach Playing with a loaded handle. The reported OUTCOME
	// is Unconfirmable, not Started — FakeEngine.Available() always
	// reports false (see FakeEngineUnavailableReason), so
	// gateAvailability rewrites every successful outcome the same way
	// every other test against FakeEngine observes; the state machine's
	// own internal state is what proves this actually worked.
	real := NewFakeEngine(c.now)
	m.RebindEngine(ctx, switchable, real, "audio.node binding delivered")
	if r := m.Start(ctx, id, "inv-start-2", 3); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("Start after binding = %s (reason=%q), want Unconfirmable (FakeEngine is never Available)", r.Outcome, r.Reason)
	}
	s.mu.Lock()
	stateAfterBind, handleLoadedAfterBind, handle := s.state, s.handleLoaded, s.handle
	s.mu.Unlock()
	if stateAfterBind != pkgaudio.StatePlaying || !handleLoadedAfterBind {
		t.Fatalf("state after Start following a binding = (state=%s handleLoaded=%v), want (Playing, true)", stateAfterBind, handleLoadedAfterBind)
	}
	if _, err := real.Observe(ctx, handle); err != nil {
		t.Fatalf("Observe on the started handle: %v (Start after binding did not actually drive the new engine)", err)
	}
}

// startAlwaysFailsEngine wraps [FakeEngine] so Load succeeds normally
// (registering the handle) but Start for one specific handle always
// fails with a fixed, non-sentinel error — modeling a genuine engine
// refusal during a retry, as opposed to [ErrNoEngineBinding].
// [FakeEngine.InjectFailure] cannot express this on its own: it is a
// one-shot arm-on-any-call, and Load (which restoreOne calls first)
// would consume it before Start ever ran.
type startAlwaysFailsEngine struct {
	*FakeEngine
	handle EngineHandle
	err    error
}

func (e *startAlwaysFailsEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	if handle == e.handle {
		return EngineObservation{}, e.err
	}
	return e.FakeEngine.Start(ctx, handle, position)
}

// TestRebindEngineRetryStartFailureRequeuesInsteadOfFailing proves
// Must-fix-1b: a Start failure during a deferred-restore retry that is
// NOT ErrNoEngineBinding (a genuine engine refusal) re-queues the
// session in pendingEngineRestore rather than persisting Failed —
// deliberately conservative, since a session resuming in the very call
// that just bound a new engine is not where this package makes a single
// failed attempt permanent.
func TestRebindEngineRetryStartFailureRequeuesInsteadOfFailing(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)
	ctx := context.Background()
	const id = pkgaudio.SessionID("retry-start-failure-session")

	m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ref := writeTestAsset(t, dir, "retry-fail.wav", "asset-retry-fail", []byte("content-retry-fail"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start unexpectedly refused: %+v", r)
	}

	switchable := NewSwitchableEngine()
	m2 := NewManager(switchable, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	failingEngine := &startAlwaysFailsEngine{
		FakeEngine: NewFakeEngine(c.now),
		handle:     EngineHandle(string(id) + "/media"),
		err:        errWrap(pkgaudio.ErrEngineFreeze),
	}
	m2.RebindEngine(ctx, switchable, failingEngine, "binding with a broken engine")

	rec, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("persisted record missing: ok=%v err=%v", ok, err)
	}
	if rec.SessionState == pkgaudio.StateFailed {
		t.Fatalf("persisted state after a genuine retry-path Start failure = Failed, want it left as Playing and re-queued instead")
	}

	m2.mu.Lock()
	_, pending := m2.pendingEngineRestore[id]
	m2.mu.Unlock()
	if !pending {
		t.Fatalf("session %s not re-queued in pendingEngineRestore after a genuine retry-path Start failure", id)
	}

	s, ok := m2.get(id)
	if !ok {
		t.Fatalf("session %s missing", id)
	}
	s.mu.Lock()
	fault, faultReason := s.fault, s.faultReason
	s.mu.Unlock()
	if fault == pkgaudio.FaultNone {
		t.Fatalf("session fault after a genuine retry-path Start failure = none, want a real reason recorded (got reason=%q)", faultReason)
	}
}

// TestRestoreAllPlayingStartFailureRecordsAFault proves the Playing/
// Preparing branch's own engine.Start failure sets a fault, matching
// the Paused branch's own long-standing practice — the asymmetry an
// adversarial review found: before this fix, a Playing session whose
// Start failed during restore persisted Failed with Fault == FaultNone,
// which is worse evidence than no fix at all, since an operator sees a
// dead session with no explanation.
func TestRestoreAllPlayingStartFailureRecordsAFault(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)
	ctx := context.Background()
	const id = pkgaudio.SessionID("playing-start-failure-session")

	m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ref := writeTestAsset(t, dir, "playing-fail.wav", "asset-playing-fail", []byte("content-playing-fail"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start unexpectedly refused: %+v", r)
	}

	failingEngine := &startAlwaysFailsEngine{
		FakeEngine: NewFakeEngine(c.now),
		handle:     EngineHandle(string(id) + "/media"),
		err:        errWrap(pkgaudio.ErrEngineFreeze),
	}
	m2 := NewManager(failingEngine, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	rec, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("persisted record missing: ok=%v err=%v", ok, err)
	}
	if rec.SessionState != pkgaudio.StateFailed {
		t.Fatalf("persisted state after a genuine Start failure = %q, want Failed", rec.SessionState)
	}
	if rec.Fault == pkgaudio.FaultNone {
		t.Fatalf("persisted fault after a genuine Start failure = none, want a real fault recorded (reason=%q)", rec.FaultReason)
	}
}

// TestDeferredRestoreSetsADeliberateFault proves a session
// deferRestoreLocked defers reports WHY it is not actually driving
// audio, deliberately (queueForRetryLocked's own setFaultLocked call),
// not merely because prepareLocked's internal fault-setting happened to
// leave one behind on its way out. In-memory only: nothing is persisted
// on this path (see TestRestoreAllWithNoEngineBoundDoesNotFailPersistedState),
// so the fault is read from the live session, not the store.
func TestDeferredRestoreSetsADeliberateFault(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)
	ctx := context.Background()
	const id = pkgaudio.SessionID("deferred-fault-session")

	m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	ref := writeTestAsset(t, dir, "deferred-fault.wav", "asset-deferred-fault", []byte("content-deferred-fault"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start unexpectedly refused: %+v", r)
	}

	switchable := NewSwitchableEngine()
	m2 := NewManager(switchable, store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	s, ok := m2.get(id)
	if !ok {
		t.Fatalf("session %s missing after RestoreAll", id)
	}
	s.mu.Lock()
	fault, faultReason := s.fault, s.faultReason
	s.mu.Unlock()
	if fault == pkgaudio.FaultNone {
		t.Fatalf("in-memory fault after a deferred restore = none, want a deliberate reason recorded")
	}
	if faultReason == "" {
		t.Fatalf("in-memory fault reason after a deferred restore is empty, want a deliberate explanation")
	}
}

// TestDeferredRestoreSurvivesAnEngineThatRefusesToBuild reproduces a
// node-reboot defect: the retained audio.node binding can be
// redelivered before discovery has published probe evidence for that
// route, so the newly bound engine correctly refuses to build (a real
// error, never [ErrNoEngineBinding]). Before the fix, restoreOne's
// prepareLocked-failure branch went straight to Failed regardless of
// retry, so that refusal permanently consumed the pending restore, and
// a later binding, one discovery had actually run against, could never
// resume the session. The restore must be triggered by an engine
// actually becoming available, not merely by a binding arriving.
func TestDeferredRestoreSurvivesAnEngineThatRefusesToBuild(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)
	ctx := context.Background()
	const id = pkgaudio.SessionID("refused-build-session")

	a := writeTestAsset(t, dir, "refused-build.wav", "asset-refused-build", []byte("content-refused-build"))
	playlist := pkgaudio.PlaylistRef{
		OwnerKind: "show", OwnerID: string(id), OwnerRevision: 1,
		Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyResume,
		RequestedTransition: pkgaudio.ItemTransitionSequential,
		Items:               []pkgaudio.PlaylistItem{{ItemID: "item-a", Index: 0, Media: a}},
	}

	m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start: unexpectedly refused: %+v", r)
	}
	c.advance(3 * time.Second)

	// "Reboot": a fresh Manager, an unbound SwitchableEngine, no
	// audio.node binding has arrived yet.
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

	// The retained audio.node binding is redelivered before discovery
	// has published probe evidence: the newly bound engine correctly
	// refuses to build.
	handle := EngineHandle(string(id) + "/item-a")
	unavailable := NewFakeEngine(c.now)
	unavailable.InjectFailure(handle, fmt.Errorf("gstengine: engine is not available: this node has no advertised probe evidence"))
	m2.RebindEngine(ctx, switchable, unavailable, "audio.node binding delivered before probe evidence")

	rec, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("persisted record missing after the refused bind: ok=%v err=%v", ok, err)
	}
	if rec.SessionState == pkgaudio.StateFailed {
		t.Fatalf("persisted state after an engine that refuses to build = Failed, want the session left pending for a later binding")
	}
	m2.mu.Lock()
	_, pendingAfterRefusal := m2.pendingEngineRestore[id]
	m2.mu.Unlock()
	if !pendingAfterRefusal {
		t.Fatalf("session %s not queued for deferred restore after the engine refused to build, want it still pending: a binding that fails to build must not consume the pending restore", id)
	}

	// A later audio.node push, once discovery has actually run: the
	// engine builds successfully.
	available := NewFakeEngine(c.now)
	m2.RebindEngine(ctx, switchable, available, "audio.node binding delivered after probe evidence")

	s2, ok := m2.get(id)
	if !ok {
		t.Fatalf("session %s missing after the successful bind", id)
	}
	s2.mu.Lock()
	state, handleLoaded, gotHandle := s2.state, s2.handleLoaded, s2.handle
	s2.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("in-memory state after an engine finally became available = %q, want Playing", state)
	}
	if !handleLoaded {
		t.Fatalf("session has no loaded engine handle after the deferred restore should have fired")
	}
	if _, err := available.Observe(ctx, gotHandle); err != nil {
		t.Fatalf("Observe on the resumed handle: %v (a session playing before a reboot must end up playing once an engine actually becomes available)", err)
	}
}

// TestDeferredRestoreOfAPausedSessionSurvivesAnEngineThatRefusesToBuild
// is the Paused branch's own copy of
// TestDeferredRestoreSurvivesAnEngineThatRefusesToBuild: restoreOne's
// Paused branch has its own, separate prepareLocked call and its own
// retry-vs-fail decision on a build refusal, so a build refusal there
// must equally re-queue the pending restore rather than consume it.
func TestDeferredRestoreOfAPausedSessionSurvivesAnEngineThatRefusesToBuild(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	store := NewFileSessionStore(dir)
	ctx := context.Background()
	const id = pkgaudio.SessionID("refused-build-paused-session")

	playlist := twoItemPlaylist(t, dir)
	m1 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
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

	// "Reboot": a fresh Manager, an unbound SwitchableEngine.
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

	// The retained audio.node binding is redelivered before discovery
	// has published probe evidence: the newly bound engine correctly
	// refuses to build.
	handle := EngineHandle(string(id) + "/item-a")
	unavailable := NewFakeEngine(c.now)
	unavailable.InjectFailure(handle, fmt.Errorf("gstengine: engine is not available: this node has no advertised probe evidence"))
	m2.RebindEngine(ctx, switchable, unavailable, "audio.node binding delivered before probe evidence")

	rec2, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("persisted record missing after the refused bind: ok=%v err=%v", ok, err)
	}
	if rec2.SessionState == pkgaudio.StateFailed {
		t.Fatalf("persisted state after an engine that refuses to build = Failed, want the session left pending for a later binding")
	}
	m2.mu.Lock()
	_, pendingAfterRefusal := m2.pendingEngineRestore[id]
	m2.mu.Unlock()
	if !pendingAfterRefusal {
		t.Fatalf("session %s not queued for deferred restore after the engine refused to build, want it still pending: a binding that fails to build must not consume the pending restore", id)
	}

	// A later audio.node push, once discovery has actually run: the
	// engine builds successfully.
	available := NewFakeEngine(c.now)
	m2.RebindEngine(ctx, switchable, available, "audio.node binding delivered after probe evidence")

	s2, ok := m2.get(id)
	if !ok {
		t.Fatalf("session %s missing after the successful bind", id)
	}
	s2.mu.Lock()
	state, handleLoaded, gotHandle := s2.state, s2.handleLoaded, s2.handle
	s2.mu.Unlock()
	if state != pkgaudio.StatePaused {
		t.Fatalf("in-memory state after an engine finally became available = %q, want Paused", state)
	}
	if !handleLoaded {
		t.Fatalf("session has no loaded engine handle after the deferred restore should have fired")
	}
	if _, err := available.Observe(ctx, gotHandle); err != nil {
		t.Fatalf("Observe on the resumed handle: %v (a paused session must end up resumed on the engine once one actually becomes available)", err)
	}
}

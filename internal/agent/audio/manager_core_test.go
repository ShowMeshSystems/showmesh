package audio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

func TestFakeEngineNeverReportsAvailable(t *testing.T) {
	e := NewFakeEngine(time.Now)
	ok, reason := e.Available()
	if ok {
		t.Fatal("FakeEngine.Available() must always be false; nothing may report it as a working audio engine")
	}
	if reason == "" {
		t.Fatal("FakeEngine.Available() must carry a reason when false")
	}
}

// mutation target: gateAvailability's outcome switch — every successful
// engine-shaped outcome, from a genuinely-executed operation, must arrive
// at the caller as Unconfirmable while the wired Engine is unavailable.
func TestManagerNeverReportsSuccessWithFakeEngine(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("content"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}

	apply := m.Apply(ctx, id, "inv-1", 1, req)
	assertUnconfirmable(t, apply)

	start := m.Start(ctx, id, "inv-2", 2)
	assertUnconfirmable(t, start)

	pause := m.Pause(ctx, id, "inv-3", 3)
	assertUnconfirmable(t, pause)

	// Internal state must still have genuinely progressed through the
	// state machine, proving gateAvailability only affects the OUTWARD
	// report — see the doc comment on why exec must still run.
	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StatePaused {
		t.Fatalf("internal session state = %q, want %q (fake engine ran but the caller sees Unconfirmable)", s.state, pkgaudio.StatePaused)
	}
}

func assertUnconfirmable(t *testing.T, r pkgaudio.OutcomeResult) {
	t.Helper()
	if r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("outcome = %q (%q), want %q", r.Outcome, r.Reason, pkgaudio.OutcomeUnconfirmable)
	}
	if r.Reason == "" {
		t.Fatal("Unconfirmable outcome must carry a reason")
	}
}

func TestApplyRejectsStaleRevision(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}

	if r := m.Apply(ctx, id, "inv-1", 5, req); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("first apply = %+v", r)
	}
	r := m.Apply(ctx, id, "inv-2", 5, req) // not strictly greater than 5
	if r.Outcome != pkgaudio.OutcomeRefused || r.Reason != pkgaudio.ReasonStaleRevision {
		t.Fatalf("stale revision apply = %+v, want refused/%s", r, pkgaudio.ReasonStaleRevision)
	}
}

func TestApplyReplayIsIdempotentAndDoesNotReexecute(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}

	first := m.Apply(ctx, id, "inv-1", 1, req)
	second := m.Apply(ctx, id, "inv-1", 1, req) // exact replay
	if first != second {
		t.Fatalf("replay returned a different outcome: %+v vs %+v", first, second)
	}

	mismatch := m.Apply(ctx, id, "inv-1", 2, req) // same invocation, different revision
	if mismatch.Outcome != pkgaudio.OutcomeRefused || mismatch.Reason != pkgaudio.ReasonInvocationRevisionMismatch {
		t.Fatalf("mismatched replay = %+v", mismatch)
	}
}

func TestStopNeverRefusedForWantOfEvidence(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	// A session that has never been applied at all still reports Stopped
	// (well, Refused only because it does not exist — Stop on a NON-
	// EXISTENT session id is the one legitimate refusal shape; an
	// existing session with no loaded engine handle must not refuse).
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})

	r := m.Stop(ctx, id, "inv-2", 2)
	if r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Stop on a session with no active playback must never be Refused, got %+v", r)
	}
}

func TestClearNeverRefusedForWantOfEvidence(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	r := m.Clear(ctx, "never-existed", "inv-1", 1)
	if r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Clear on an unknown session must never be Refused, got %+v", r)
	}
}

// TestClearTearsDownDespiteStaleRevision is the recovery property this
// exemption exists for: a clear carrying a revision below the session's
// current one, exactly the shape a delayed clear would arrive with after
// a newer start already re-established the session, must still tear the
// session down rather than being refused stale_revision.
func TestClearTearsDownDespiteStaleRevision(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))

	if r := m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("apply = %+v", r)
	}
	if r := m.Start(ctx, id, "inv-start", 2); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("start = %+v", r)
	}

	// revision 1 is stale against the session's current revision of 2:
	// an ordinary command carrying it would be refused stale_revision
	// (see TestApplyRejectsStaleRevision). Clear must not be.
	r := m.Clear(ctx, id, "inv-clear-stale", 1)
	if r.Outcome == pkgaudio.OutcomeRefused || r.Reason == pkgaudio.ReasonStaleRevision {
		t.Fatalf("clear with a stale revision = %+v, want it to tear the session down, not refuse", r)
	}
	if _, ok := m.get(id); ok {
		t.Fatal("session still exists after a clear that should have torn it down despite its stale revision")
	}
}

// TestClearWithEmptyInvocationStillRefused proves the exemption is
// narrower than "clear always executes": an empty invocation id is a
// caller bug, not an ordering symptom, and must still refuse.
func TestClearWithEmptyInvocationStillRefused(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})

	r := m.Clear(ctx, id, "", 2)
	if r.Outcome != pkgaudio.OutcomeRefused || r.Reason != "invocation id is required" {
		t.Fatalf("clear with empty invocation = %+v, want refused/%q", r, "invocation id is required")
	}
	assertSessionStillHoldsAppliedState(t, m, id)
}

// TestClearReusingInvocationWithDifferentRevisionStillRefused proves the
// exemption does not waive invocation_revision_mismatch either: reusing
// an invocation id for a second, different revision is a caller bug, not
// an ordering symptom.
func TestClearReusingInvocationWithDifferentRevisionStillRefused(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))

	if r := m.Apply(ctx, id, "reused-inv", 3, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("apply = %+v", r)
	}

	// "reused-inv" was already recorded against revision 3; asking Clear
	// to apply it against revision 7 is a reused invocation id carrying a
	// different revision, not a legitimate replay.
	r := m.Clear(ctx, id, "reused-inv", 7)
	if r.Outcome != pkgaudio.OutcomeRefused || r.Reason != pkgaudio.ReasonInvocationRevisionMismatch {
		t.Fatalf("clear reusing an invocation id at a different revision = %+v, want refused/%s", r, pkgaudio.ReasonInvocationRevisionMismatch)
	}
	assertSessionStillHoldsAppliedState(t, m, id)
}

// assertSessionStillHoldsAppliedState is the observable consequence a
// caller actually depends on when a clear is refused: not merely that
// the session's map entry still exists, but that the teardown Clear's
// own exec would have performed (releasing the engine, wiping desired
// state to its zero value, resetting state to Stopped) never ran.
// Checking a refusal's reason string alone would keep passing under a
// broader, wrong exemption that still happened to report the same
// reason on some other path; checking that Apply's own media survives
// on the live session object catches that a teardown actually ran,
// regardless of what reason, if any, the caller was told.
func assertSessionStillHoldsAppliedState(t *testing.T, m *Manager, id pkgaudio.SessionID) {
	t.Helper()
	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was torn down by a clear that should have been refused")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.desired.Media == nil {
		t.Fatal("session's desired media was wiped by a clear that should have been refused")
	}
	if s.state == pkgaudio.StateStopped {
		t.Fatal("session state was reset to stopped by a clear that should have been refused")
	}
}

// TestNonClearCommandsStillEnforceRevisionOrdering proves the exemption
// is scoped to Clear alone: on the very same session a stale clear just
// tore down would have been exempted for, a non-clear command carrying a
// stale revision is still refused.
func TestNonClearCommandsStillEnforceRevisionOrdering(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))

	if r := m.Apply(ctx, id, "inv-apply", 5, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("apply = %+v", r)
	}

	r := m.Stop(ctx, id, "inv-stop-stale", 2) // 2 is not strictly greater than 5
	if r.Outcome != pkgaudio.OutcomeRefused || r.Reason != pkgaudio.ReasonStaleRevision {
		t.Fatalf("stop with a stale revision = %+v, want refused/%s", r, pkgaudio.ReasonStaleRevision)
	}
}

// TestStartReprepresIdentityChangedBetweenPrepareAndStart proves finding
// 7: preparing item A, then Applying a change to item B, then Start must
// play B, never A. Before the fix, Start only checked s.handleLoaded and
// skipped repreparing whenever a handle was already loaded for any
// reason, so a media change landed by Apply after Prepare was silently
// ignored and the stale handle for A was started instead.
func TestStartRepreparesIdentityChangedBetweenPrepareAndStart(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	refA := writeTestAsset(t, m.assetDir, "a.wav", "asset-a", []byte("content-a"))
	refB := writeTestAsset(t, m.assetDir, "b.wav", "asset-b", []byte("content-b"))

	m.Apply(ctx, id, "inv-apply-a", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(refA)})
	if r := m.Prepare(ctx, id, "inv-prepare", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("prepare A unexpectedly refused: %+v", r)
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	loadedAfterPrepareA := s.loadedIdentity
	s.mu.Unlock()
	if loadedAfterPrepareA == "" {
		t.Fatal("loadedIdentity was never set by Prepare")
	}

	// Apply B AFTER preparing A — the desired media changes between
	// Prepare and Start.
	m.Apply(ctx, id, "inv-apply-b", 3, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(refB)})
	m.Start(ctx, id, "inv-start", 4)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadedIdentity != itemIdentity(pkgaudio.PlaylistItem{ItemID: "media", Media: refB}) {
		t.Fatalf("loadedIdentity = %q after Start, want it to reflect asset B, not the stale A handle", s.loadedIdentity)
	}
	h, err := m.engine.(*FakeEngine).get(s.handle)
	if err != nil {
		t.Fatalf("engine has no handle for the started session: %v", err)
	}
	if h.media.AssetID != "asset-b" {
		t.Fatalf("engine handle's loaded media = %q, want asset-b (B), not the stale A load", h.media.AssetID)
	}
}

// TestStopReportsFailureAndKeepsHandleForRetry verifies that when
// Engine.Stop or Engine.Release fails, Manager.Stop must not silently
// discard the handle and report success — it keeps the handle loaded
// (so a retry can still address it) and reports the failure, rather
// than always releasing the handle and reporting Stopped regardless of
// what the engine said.
func TestStopReportsFailureAndKeepsHandleForRetry(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	fake := NewFakeEngine(c.now)
	m := NewManager(availableFakeEngine{fake}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()

	fake.InjectFailure(handle, pkgaudio.ErrEnginePipelineCrash)

	r := m.Stop(ctx, id, "inv-stop", 3)
	if r.Outcome == pkgaudio.OutcomeStopped {
		t.Fatalf("Stop reported Stopped despite Engine.Stop failing: %+v", r)
	}
	if r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Stop must never be Refused (ADR-024 decision 7), got %+v", r)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.handleLoaded {
		t.Fatal("handle was released despite Engine.Stop failing; a retry has nothing left to address")
	}
	if s.handle != handle {
		t.Fatalf("handle changed to %q, want the original %q preserved for retry", s.handle, handle)
	}
	if s.fault == pkgaudio.FaultNone {
		t.Fatal("no fault was recorded for the failed Stop")
	}
}

// TestStopFailureStillReleasesDuckAndAllowsRetry verifies that a failed
// Engine.Stop must not strand a ducked background session at duck gain
// for the rest of the night: the duck this session imposed is released
// once its own stop was attempted, regardless of whether the engine
// confirmed it, and a retried Stop still succeeds against the preserved
// handle.
func TestStopFailureStillReleasesDuckAndAllowsRetry(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	m.GainSet(ctx, "bg", "inv-bg-gain", 3, pkgaudio.Gain(0.8))

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	bg, _ := m.get("bg")
	bg.mu.Lock()
	_, duckedByAnn := bg.duckedByAll["ann"]
	bg.mu.Unlock()
	if !duckedByAnn {
		t.Fatal("precondition: bg should be ducked by ann before ann is stopped")
	}

	ann, ok := m.get("ann")
	if !ok {
		t.Fatal("ann session was not created")
	}
	ann.mu.Lock()
	handle := ann.handle
	ann.mu.Unlock()

	fake, ok := m.engine.(*FakeEngine)
	if !ok {
		t.Fatalf("test manager's engine is %T, want *FakeEngine", m.engine)
	}
	fake.InjectFailure(handle, pkgaudio.ErrEnginePipelineCrash)

	r := m.Stop(ctx, "ann", "inv-ann-stop-1", 3)
	if r.Outcome == pkgaudio.OutcomeStopped {
		t.Fatalf("Stop reported Stopped despite Engine.Stop failing: %+v", r)
	}

	bg.mu.Lock()
	_, stillDuckedByAnn := bg.duckedByAll["ann"]
	bgGain := *bg.desired.Gain
	bgHandle := bg.handle
	bg.mu.Unlock()
	if stillDuckedByAnn {
		t.Fatal("bg is still ducked by ann after ann's failed stop; a stuck duck plays silence all night")
	}
	if bgGain != pkgaudio.Gain(0.8) {
		t.Fatalf("bg gain after ann's failed stop = %v, want restored 0.8", bgGain)
	}
	// bg.desired.Gain is this package's own bookkeeping; only the
	// engine's own evidence proves the fade that restores it actually
	// reached the engine. The restore is a fade, not a step: let it
	// finish before reading the engine's own settled gain.
	c.advance(900 * time.Millisecond)
	bgObs, err := m.engine.Observe(ctx, bgHandle)
	if err != nil {
		t.Fatalf("Observe(bg): %v", err)
	}
	if bgObs.Gain != pkgaudio.Gain(0.8) {
		t.Fatalf("engine-reported bg gain after ann's failed stop = %v, want restored 0.8", bgObs.Gain)
	}

	ann.mu.Lock()
	annState := ann.state
	annHandleLoaded := ann.handleLoaded
	ann.mu.Unlock()
	if annState != pkgaudio.StateStopping {
		t.Fatalf("ann state after failed stop = %q, want stopping (visible, not silently lost)", annState)
	}
	if !annHandleLoaded {
		t.Fatal("ann's handle was released despite the failed stop; a retry has nothing to address")
	}

	// Retry: the handle is still there, so a second Stop can still
	// address it and this time nothing is armed to fail. The outward
	// outcome stays Unconfirmable (this test's engine always reports
	// unavailable, per gateAvailability), so success is checked against
	// the session's own internal state, the same pattern
	// TestManagerNeverReportsSuccessWithFakeEngine uses.
	r2 := m.Stop(ctx, "ann", "inv-ann-stop-2", 4)
	if r2.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("retried Stop outcome = %+v, must never be refused", r2)
	}
	ann.mu.Lock()
	defer ann.mu.Unlock()
	if ann.state != pkgaudio.StateStopped || ann.handleLoaded {
		t.Fatalf("ann after retried stop: state=%q handleLoaded=%v, want stopped and released", ann.state, ann.handleLoaded)
	}
}

// TestWatchTickResolvesStuckStoppingSession verifies that a session left
// in StateStopping by a failed Engine.Stop is not abandoned there
// forever: once the engine's own evidence later shows playback actually
// stopped, RunWatcher's tick must notice and finalize the session
// itself, releasing its handle.
func TestWatchTickResolvesStuckStoppingSession(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()

	fake, ok := m.engine.(*FakeEngine)
	if !ok {
		t.Fatalf("test manager's engine is %T, want *FakeEngine", m.engine)
	}
	fake.InjectFailure(handle, pkgaudio.ErrEnginePipelineCrash)
	m.Stop(ctx, id, "inv-stop", 3)

	s.mu.Lock()
	if s.state != pkgaudio.StateStopping {
		s.mu.Unlock()
		t.Fatalf("precondition: session state = %q, want stopping", s.state)
	}
	s.mu.Unlock()

	// Simulate the engine actually reaching Stopped on its own — a
	// retried transport succeeding underneath, out from under the failed
	// call above — without going back through Manager.Stop.
	if _, err := fake.Stop(ctx, handle); err != nil {
		t.Fatalf("fake.Stop: %v", err)
	}

	m.watchTick(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StateStopped {
		t.Fatalf("session state after watchTick observed the engine stopped = %q, want stopped", s.state)
	}
	if s.handleLoaded {
		t.Fatal("session handle still loaded after watchTick resolved the stop")
	}
}

// TestStartRefusesNegativeBookmarkPosition verifies that a
// negative bookmark position must never reach Engine.Start — it is
// refused visibly and the bookmark is cleared so a subsequent Start is
// not refused forever by the same bad value.
func TestStartRefusesNegativeBookmarkPosition(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start-1", 2)
	m.Pause(ctx, id, "inv-pause", 3)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	if s.bookmark == nil {
		s.mu.Unlock()
		t.Fatal("precondition: pause should have set a bookmark")
	}
	s.bookmark.Position = -5 * time.Second
	// Force out of Paused so the paused-session guard on Start (which
	// refuses before ever consulting the bookmark) does not intercept
	// this call before resolveBookmarkPositionLocked runs.
	s.state = pkgaudio.StateReady
	s.mu.Unlock()

	r := m.Start(ctx, id, "inv-start-2", 4)
	if r.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("outcome = %+v, want Refused for a negative bookmark position", r)
	}
	if r.Reason == "" {
		t.Fatal("refusal must carry a reason naming the bad bookmark")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bookmark != nil {
		t.Fatalf("bookmark = %+v, want cleared after being refused so a later Start is not stuck", s.bookmark)
	}
}

// TestStartRefusesStalePlaylistBookmark proves the other half of finding
// 16: a bookmark pinned to a playlist revision that no longer matches the
// session's current desired playlist must be refused via
// [pkgaudio.Bookmark.Resolve], never silently used as if it still
// applied to the new playlist.
func TestStartRefusesStalePlaylistBookmark(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	playlist := twoItemPlaylist(t, m.assetDir)
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start-1", 2)
	m.Pause(ctx, id, "inv-pause", 3)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	if s.bookmark == nil || s.bookmark.PlaylistRevision != playlist.OwnerRevision {
		s.mu.Unlock()
		t.Fatalf("precondition: bookmark should pin the current playlist revision, got %+v", s.bookmark)
	}
	// Simulate the playlist having moved on: the bookmark's own revision
	// no longer matches, exactly the way a stale reference reaches Start
	// after an Apply lands a new playlist revision between pause and the
	// next Start.
	s.bookmark.PlaylistRevision = playlist.OwnerRevision + 1
	// Force out of Paused so the paused-session guard on Start (which
	// refuses before ever consulting the bookmark) does not intercept
	// this call before resolveBookmarkPositionLocked runs.
	s.state = pkgaudio.StateReady
	s.mu.Unlock()

	r := m.Start(ctx, id, "inv-start-2", 4)
	if r.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("outcome = %+v, want Refused for a stale playlist bookmark", r)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bookmark != nil {
		t.Fatalf("bookmark = %+v, want cleared after being refused as stale", s.bookmark)
	}
}

// TestStartOnPausedSessionIsRefused verifies that Start on a session
// still paused on the same item it was paused on is refused rather than
// reusing the pause-time bookmark: under the known pause-fidelity
// limitation the engine's own position may have moved on since the
// pause, so Start would seek it backwards. The engine must never be
// touched, and the session must stay Paused so a subsequent Resume still
// works.
func TestStartOnPausedSessionIsRefused(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start-1", 2)
	c.advance(500 * time.Millisecond)
	if r := m.Pause(ctx, id, "inv-pause", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("pause unexpectedly refused: %+v", r)
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()
	before, err := m.engine.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe before the refused Start: %v", err)
	}

	r := m.Start(ctx, id, "inv-start-2", 4)
	if r.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("outcome = %+v, want Refused for Start on a paused session", r)
	}
	if r.Reason == "" {
		t.Fatal("refusal must state that Resume, not Start, is the way to continue a paused session")
	}

	s.mu.Lock()
	state := s.state
	bookmark := s.bookmark
	s.mu.Unlock()
	if state != pkgaudio.StatePaused {
		t.Fatalf("state after the refused Start = %q, want still Paused", state)
	}
	if bookmark == nil {
		t.Fatal("bookmark was cleared by the refused Start; a later Resume needs it")
	}

	after, err := m.engine.Observe(ctx, handle)
	if err != nil {
		t.Fatalf("Observe after the refused Start: %v", err)
	}
	if after.State != before.State || after.Position != before.Position {
		t.Fatalf("engine moved from %+v to %+v; a refused Start must never reach the engine at all", before, after)
	}
}

// TestDispatchReportsFailureWhenPersistFails verifies that a
// command that executed but could not be durably saved must report
// failure, not the underlying command's optimistic success — a caller
// told "started" or "stopped" over a persist that silently failed would
// believe a guarantee (surviving this process's next crash) that was
// never actually met.
func TestDispatchReportsFailureWhenPersistFails(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := &failingSessionStore{SessionStore: NewFileSessionStore(dir)}
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))

	store.armSaveFailures(1, nil)
	r := m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r.Outcome != pkgaudio.OutcomeFailed {
		t.Fatalf("outcome = %+v, want Failed when persist fails, not the command's own optimistic outcome", r)
	}
	if r.Reason == "" {
		t.Fatal("a persist-failure outcome must carry a reason")
	}

	// The cached result for THIS invocation must be consistent with what
	// was reported, not the discarded optimistic one, so a replay of the
	// same invocation returns the same true answer.
	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	cached := s.executedResults["inv-apply"]
	s.mu.Unlock()
	if cached.Outcome != pkgaudio.OutcomeFailed {
		t.Fatalf("cached result = %+v, want Failed to match what was reported", cached)
	}

	// Once the store recovers, a fresh command must succeed normally —
	// this failure must not be sticky.
	r2 := m.Apply(ctx, id, "inv-apply-2", 2, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r2.Outcome == pkgaudio.OutcomeFailed {
		t.Fatalf("outcome = %+v, want success once the store recovers", r2)
	}
}

// TestCorruptPersistedSessionRaisesFaultEvidence verifies that a
// malformed or truncated persisted session file must not silently
// vanish from the fleet — RestoreAll must not stop for it, but
// Manager.Snapshot must still report it as retained fault evidence, not
// omit it (indistinguishable from "never persisted").
func TestCorruptPersistedSessionRaisesFaultEvidence(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())

	// A healthy session, so the corrupt one is proven not to block
	// restoring anything else.
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()
	healthyRef := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, "healthy", "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(healthyRef)})

	// Write a truncated/invalid JSON file directly into the session
	// store's directory, simulating a crash mid-write or disk corruption.
	sessionDir := filepath.Join(m.assetDir, sessionStateSubdir)
	if err := os.WriteFile(filepath.Join(sessionDir, "truncated.json"), []byte(`{"ID": "broken", "Desi`), 0o644); err != nil {
		t.Fatalf("write corrupt session file: %v", err)
	}

	m2 := newTestManagerInDir(dir, c)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	if _, ok := m2.get("healthy"); !ok {
		t.Fatal("the healthy session was not restored; a corrupt sibling file must not block it")
	}

	snaps := m2.Snapshot(ctx)
	var found bool
	for _, snap := range snaps {
		if snap.State == pkgaudio.StateFailed && snap.Fault == pkgaudio.FaultOther && snap.FaultReason != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Snapshot() = %+v, want an entry reporting the corrupt file as fault evidence", snaps)
	}
}

// TestExecutedResultsAreBoundedAndOldestEvicted verifies that a
// session's invocation decisions/results must not grow without bound.
// After exceeding maxRetainedInvocations, the oldest invocation is
// evicted, the newest ones survive, and the persisted record's Decisions
// map never carries an invocation ExecutedResults no longer has (the
// consistency [Session.retainedDecisionsLocked] exists for).
func TestExecutedResultsAreBoundedAndOldestEvicted(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	total := maxRetainedInvocations + 25
	for i := 0; i < total; i++ {
		rev := pkgaudio.Revision(i + 1)
		inv := pkgaudio.InvocationID(fmt.Sprintf("inv-%04d", i))
		m.Apply(ctx, id, inv, rev, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.executedResults) != maxRetainedInvocations {
		t.Fatalf("len(executedResults) = %d, want bounded to %d", len(s.executedResults), maxRetainedInvocations)
	}
	if len(s.executedOrder) != maxRetainedInvocations {
		t.Fatalf("len(executedOrder) = %d, want %d", len(s.executedOrder), maxRetainedInvocations)
	}

	// The earliest invocations were evicted...
	oldest := pkgaudio.InvocationID("inv-0000")
	if _, ok := s.executedResults[oldest]; ok {
		t.Fatalf("oldest invocation %q was not evicted", oldest)
	}
	// ...and the most recent ones survived.
	newest := pkgaudio.InvocationID(fmt.Sprintf("inv-%04d", total-1))
	if _, ok := s.executedResults[newest]; !ok {
		t.Fatalf("newest invocation %q was evicted, want retained", newest)
	}

	// The persisted record's two invocation-keyed maps must agree on
	// membership: no Decision survives for an invocation ExecutedResults
	// no longer has.
	rec := s.persistedLocked()
	if len(rec.Decisions) != len(rec.ExecutedResults) {
		t.Fatalf("len(Decisions) = %d, len(ExecutedResults) = %d, want equal membership", len(rec.Decisions), len(rec.ExecutedResults))
	}
	for invocation := range rec.ExecutedResults {
		if _, ok := rec.Decisions[invocation]; !ok {
			t.Fatalf("invocation %q has an ExecutedResult but no matching Decision", invocation)
		}
	}
	if _, ok := rec.Decisions[oldest]; ok {
		t.Fatalf("evicted invocation %q still has a persisted Decision", oldest)
	}
}

// hangingObserveEngine wraps [FakeEngine] and makes Observe against one
// specific handle block until its context is done, simulating a hung
// engine, which cannot be reached against a shipped FakeEngine that
// always answers immediately.
type hangingObserveEngine struct {
	*FakeEngine
	hangHandle EngineHandle
}

func (e *hangingObserveEngine) Observe(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	if handle == e.hangHandle {
		<-ctx.Done()
		return EngineObservation{}, ctx.Err()
	}
	return e.FakeEngine.Observe(ctx, handle)
}

// TestWatchTickBoundsAHungObserveCall verifies that watchTick's
// per-session supervision loop must not let one hung Engine.Observe call
// block supervision of every other session behind it forever: Observe
// must run under a bounded context rather than the tick's own, or a
// genuinely hung handle stalls the whole tick — and every session after
// it — indefinitely.
func TestWatchTickBoundsAHungObserveCall(t *testing.T) {
	prevTimeout := observeTimeout
	observeTimeout = 50 * time.Millisecond
	defer func() { observeTimeout = prevTimeout }()

	c := newClock(time.Now())
	dir := t.TempDir()
	hung := &hangingObserveEngine{FakeEngine: NewFakeEngine(c.now)}
	m := NewManager(hung, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	refA := writeTestAsset(t, m.assetDir, "a.wav", "asset-a", []byte("a"))
	refB := writeTestAsset(t, m.assetDir, "b.wav", "asset-b", []byte("b"))
	m.Apply(ctx, "hang", "inv-hang-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(refA)})
	m.Start(ctx, "hang", "inv-hang-start", 2)
	m.Apply(ctx, "ok", "inv-ok-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(refB)})
	m.Start(ctx, "ok", "inv-ok-start", 2)

	hangSession, _ := m.get("hang")
	hangSession.mu.Lock()
	hung.hangHandle = hangSession.handle
	hangSession.mu.Unlock()

	okSession, _ := m.get("ok")
	okSession.mu.Lock()
	okSession.lastObservedAt = time.Time{}
	okSession.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.watchTick(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchTick did not return within a bounded time despite one hung Observe call")
	}

	okSession.mu.Lock()
	defer okSession.mu.Unlock()
	if okSession.lastObservedAt.IsZero() {
		t.Fatal("the healthy session was never observed; the hung session's Observe call blocked the whole tick")
	}
}

package audio

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// configureLTC gives m a Configured [Settings] with rate/offset, keeping
// every other default this package's existing tests already assume.
func configureLTC(m *Manager, rate pkgaudio.LTCFrameRate, offset pkgaudio.LTCTimecode) {
	s := DefaultSettings
	s.LTCFrameRate = rate
	s.LTCDefaultStartOffset = offset
	m.SetSettings(s)
}

func fakeOf(t *testing.T, m *Manager) *FakeEngine {
	t.Helper()
	fake, ok := m.engine.(*FakeEngine)
	if !ok {
		t.Fatalf("manager engine is %T, want *FakeEngine", m.engine)
	}
	return fake
}

// requestedLTC returns the fake engine's most recent LTC run request.
// Lifecycle transitions are asserted against the REQUEST, because that is
// all a lifecycle transition can be held to: whether frames then reach the
// output is the backend's own evidence, and a fake that confirmed its own
// request would pass against a backend emitting nothing.
func requestedLTC(t *testing.T, m *Manager) (LTCSpec, bool) {
	t.Helper()
	return fakeOf(t, m).LastLTCRequest()
}

// assertLTCRequestedAt asserts a run is currently requested at tc.
func assertLTCRequestedAt(t *testing.T, m *Manager, tc pkgaudio.LTCTimecode) {
	t.Helper()
	spec, ok := requestedLTC(t, m)
	if !ok || spec.StartTimecode != tc {
		t.Fatalf("LTC request = %+v requested=%v, want a run at %s", spec, ok, tc)
	}
}

// itemIdentityFor builds the exact identity [Session.resolveBookmarkPositionLocked]
// requires a single-media session's bookmark to carry.
func itemIdentityFor(ref pkgaudio.MediaRef) string {
	return itemIdentity(pkgaudio.PlaylistItem{ItemID: "media", Media: ref})
}

func TestLTCStartsForShowSessionAtZeroPosition(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "01:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	spec, ok := requestedLTC(t, m)
	if !ok {
		t.Fatal("no LTC run was requested for a playing show session")
	}
	if spec.StartTimecode != "01:00:00:00" {
		t.Fatalf("LTC start timecode = %q, want 01:00:00:00", spec.StartTimecode)
	}
	if spec.FrameRate != pkgaudio.LTCFrameRate30 {
		t.Fatalf("LTC frame rate = %q, want 30", spec.FrameRate)
	}
}

// TestLTCStartsAtNonZeroPositionFromBookmark proves a Start that resumes
// from a bookmarked position emits the timecode for THAT position, never
// the session's start offset alone.
func TestLTCStartsAtNonZeroPositionFromBookmark(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Media:      pkgaudio.SetField(ref),
	}
	if r := m.Apply(ctx, "show", "apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}

	s, _ := m.get("show")
	s.mu.Lock()
	s.bookmark = &pkgaudio.Bookmark{ItemID: "media", Identity: itemIdentityFor(ref), Position: 5 * time.Second}
	s.mu.Unlock()

	if r := m.Start(ctx, "show", "start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start refused: %+v", r)
	}

	assertLTCRequestedAt(t, m, "00:00:05:00")
}

func TestLTCResumesAtThePausedPosition(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	m := NewManager(NewFakeEngine(c.now), NewFileSessionStore(dir), dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, dir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	c.advance(3 * time.Second)
	m.Pause(ctx, "show", "pause", 3)

	if obs := fakeOf(t, m).ObserveLTC(ctx); obs.State != LTCStopped {
		t.Fatalf("LTC state after pause = %q, want stopped", obs.State)
	}

	// Elapsed wall time during the pause must not leak into the resumed
	// timecode: only the frozen playback position matters.
	c.advance(10 * time.Second)
	m.Resume(ctx, "show", "resume", 4)

	assertLTCRequestedAt(t, m, "00:00:03:00")
}

func TestLTCSeekReanchorsToTheSeekedPosition(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	m := NewManager(NewFakeEngine(c.now), NewFileSessionStore(dir), dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, dir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	if spec, ok := requestedLTC(t, m); !ok || spec.StartTimecode == "00:00:07:00" {
		t.Fatalf("precondition: LTC request %+v requested=%v is already at the seek target", spec, ok)
	}

	m.Seek(ctx, "show", "seek", 3, 7*time.Second)

	assertLTCRequestedAt(t, m, "00:00:07:00")
}

func TestLTCStopsOnPause(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	m.Pause(ctx, "show", "pause", 3)

	if obs := fakeOf(t, m).ObserveLTC(ctx); obs.State != LTCStopped {
		t.Fatalf("LTC state after pause = %q, want stopped", obs.State)
	}
}

func TestLTCStopsOnCommandedStop(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	m.Stop(ctx, "show", "stop", 3)

	if obs := fakeOf(t, m).ObserveLTC(ctx); obs.State != LTCStopped {
		t.Fatalf("LTC state after stop = %q, want stopped", obs.State)
	}
}

func TestLTCStopsOnNaturalCompletion(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	m := NewManager(NewFakeEngine(c.now), NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, dir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	c.advance(3 * time.Second) // past the 2s asset duration
	m.watchTick(ctx)

	s, _ := m.get("show")
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StateCompleted {
		t.Fatalf("session state = %q, want completed", state)
	}
	if obs := fakeOf(t, m).ObserveLTC(ctx); obs.State != LTCStopped {
		t.Fatalf("LTC state after natural completion = %q, want stopped", obs.State)
	}
}

func TestLTCStopsOnClear(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	m.Clear(ctx, "show", "clear", 3)

	if obs := fakeOf(t, m).ObserveLTC(ctx); obs.State != LTCStopped {
		t.Fatalf("LTC state after clear = %q, want stopped", obs.State)
	}
}

func TestLTCRestoreLeavesLTCConsistentWithWhatActuallyResumed(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, dir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	c.advance(4 * time.Second)

	fresh := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	configureLTC(fresh, pkgaudio.LTCFrameRate30, "00:00:00:00")
	if err := fresh.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	s, ok := fresh.get("show")
	if !ok {
		t.Fatal("session not restored")
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("restored session state = %q, want playing", state)
	}

	if _, ok := requestedLTC(t, fresh); !ok {
		t.Fatal("no LTC run was requested after restoring a session that resumed playing")
	}
}

// TestLTCRestoreNeverStartsForASessionThatDidNotResume proves the other
// half: a session restored into Paused (never Playing) must never have
// started an LTC run in the first place.
func TestLTCRestoreNeverStartsForASessionThatDidNotResume(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, dir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	m.Pause(ctx, "show", "pause", 3)

	fresh := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	configureLTC(fresh, pkgaudio.LTCFrameRate30, "00:00:00:00")
	if err := fresh.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	s, ok := fresh.get("show")
	if !ok {
		t.Fatal("session not restored")
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StatePaused {
		t.Fatalf("restored session state = %q, want paused", state)
	}
	if obs := fakeOf(t, fresh).ObserveLTC(ctx); obs.State != LTCStopped {
		t.Fatalf("LTC state after restoring a paused session = %q, want stopped", obs.State)
	}
}

// TestLTCNeverDrivenByABackgroundSession proves the role gate: a
// background source never starts, realigns, or stops LTC.
func TestLTCNeverDrivenByABackgroundSession(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", ref, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)

	if spec, ok := requestedLTC(t, m); ok {
		t.Fatalf("a background session requested an LTC run at %s, want none", spec.StartTimecode)
	}
	if obs := fakeOf(t, m).ObserveLTC(ctx); obs.State != LTCStopped {
		t.Fatalf("LTC state after a background session started = %q, want stopped (never touched)", obs.State)
	}
}

// TestLTCStartFailureNeverFailsTheSessionOperation proves the isolation
// rule: an LTC generator failure is recorded as LTC evidence and logged,
// never propagated into the session's own outcome or internal state.
func TestLTCStartFailureNeverFailsTheSessionOperation(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	wantErr := errors.New("simulated LTC generator failure")
	fakeOf(t, m).InjectLTCFailure(wantErr)

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	s, _ := m.get("show")
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("session state = %q, want playing: an LTC failure must never fail the session operation", state)
	}

	obs := fakeOf(t, m).ObserveLTC(ctx)
	if obs.State != LTCFailed || obs.Reason == "" {
		t.Fatalf("LTC observation = %+v, want failed with a stated reason", obs)
	}
}

// TestLTCRefusesToRunWithUnconfiguredSettings proves the settings gate:
// with audio.settings never configured, a show session still plays, but
// LTC never starts, and its stated evidence says why.
func TestLTCRefusesToRunWithUnconfiguredSettings(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c) // never calls SetSettings
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	s, _ := m.get("show")
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("session state = %q, want playing", state)
	}

	obs := fakeOf(t, m).ObserveLTC(ctx)
	if obs.State != LTCStopped || obs.Reason == "" {
		t.Fatalf("LTC observation = %+v, want stopped with a stated reason (settings never configured)", obs)
	}
}

// TestLTCRealignsAcrossAPlaylistAdvance proves the natural-advance-to-
// next-item path (the [Session.advanceLocked] success branch) restarts
// LTC at the new item's own position 0, never leaves it emitting the
// prior item's stale timecode.
func TestLTCRealignsAcrossAPlaylistAdvance(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	m := NewManager(NewFakeEngine(c.now), NewFileSessionStore(dir), dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	playlist := twoItemPlaylist(t, dir)
	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Playlist:   pkgaudio.SetField(playlist),
	}
	if r := m.Apply(ctx, "night-session", "apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}
	m.Start(ctx, "night-session", "start", 2)

	// Move item-a's LTC off 0 so a genuine realignment at item-b is
	// distinguishable from LTC simply never having been touched again.
	m.Seek(ctx, "night-session", "seek", 3, 5*time.Second)
	assertLTCRequestedAt(t, m, "00:00:05:00")

	c.advance(11 * time.Second) // past item-a's 10s duration, seeked position included
	m.watchTick(ctx)

	s, _ := m.get("night-session")
	s.mu.Lock()
	itemID, state := s.currentItemID, s.state
	s.mu.Unlock()
	if itemID != "item-b" || state != pkgaudio.StatePlaying {
		t.Fatalf("session after natural advance: item=%q state=%q, want item-b playing", itemID, state)
	}

	assertLTCRequestedAt(t, m, "00:00:00:00")
}

// TestLTCStopsWhenAPlaylistRunsOut proves the OTHER completion branch:
// no next item and no repeat still stops LTC, the same as a single-media
// session's natural completion.
func TestLTCStopsWhenAPlaylistRunsOut(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	m := NewManager(NewFakeEngine(c.now), NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	playlist := twoItemPlaylist(t, dir)
	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Playlist:   pkgaudio.SetField(playlist),
	}
	m.Apply(ctx, "night-session", "apply", 1, req)
	m.Start(ctx, "night-session", "start", 2)

	c.advance(3 * time.Second)
	m.watchTick(ctx) // item-a completes, advances to item-b
	c.advance(3 * time.Second)
	m.watchTick(ctx) // item-b completes, no repeat: session completes

	s, _ := m.get("night-session")
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StateCompleted {
		t.Fatalf("session state = %q, want completed", state)
	}
	if obs := fakeOf(t, m).ObserveLTC(ctx); obs.State != LTCStopped {
		t.Fatalf("LTC state after the playlist ran out = %q, want stopped", obs.State)
	}
}

// TestLTCDispatchAloneNeverReportsRunning is the rule the rest of this
// file is calibrated against: a requested run reports itself unconfirmed
// with a reason until the backend says a frame was emitted. A fake that
// confirmed its own request would make every test above pass against a
// backend producing silence.
func TestLTCDispatchAloneNeverReportsRunning(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	fake := fakeOf(t, m)
	obs := fake.ObserveLTC(ctx)
	if obs.State == LTCRunning {
		t.Fatalf("LTC reported running from a dispatch alone: %+v", obs)
	}
	if obs.Reason == "" {
		t.Fatalf("LTC observation %+v carries no reason for not running", obs)
	}
	if obs.TimecodeKnown {
		t.Fatalf("LTC reported a timecode with no frame emitted: %+v", obs)
	}

	fake.EmitLTCFrame()
	if obs := fake.ObserveLTC(ctx); obs.State != LTCRunning || obs.Timecode != "00:00:00:00" {
		t.Fatalf("LTC after an emitted frame = %+v, want running at 00:00:00:00", obs)
	}
}

// TestSecondShowSessionNeverTakesLTCFromTheFirst reproduces what an
// unguarded claim did: a second show session re-anchored LTC to its own
// timeline while the first was still playing, and its stop then killed
// LTC outright while the first still played.
func TestSecondShowSessionNeverTakesLTCFromTheFirst(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	m := NewManager(NewFakeEngine(c.now), NewFileSessionStore(dir), dir, staticDecoder{duration: 10 * time.Second}, c.now, nil)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	refA := writeTestAsset(t, dir, "a.wav", "asset-a", []byte("a"))
	refB := writeTestAsset(t, dir, "b.wav", "asset-b", []byte("b"))
	startPlaying(t, m, ctx, "show-a", refA, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	// show-b's own start offset makes a takeover visible: an unguarded
	// claim would leave the request at show-b's hour, not show-a's zero.
	c.advance(4 * time.Second)
	offsetB := pkgaudio.LTCTimecode("01:00:00:00")
	reqB := pkgaudio.ApplyRequest{
		SourceRole:     pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Media:          pkgaudio.SetField(refB),
		LTCStartOffset: pkgaudio.SetField(offsetB),
	}
	if r := m.Apply(ctx, "show-b", "apply-b", 7, reqB); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply show-b refused: %+v", r)
	}
	if r := m.Start(ctx, "show-b", "start-b", 8); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start show-b refused: %+v", r)
	}

	assertLTCRequestedAt(t, m, "00:00:00:00")

	m.Stop(ctx, "show-b", "stop-b", 9)

	s, _ := m.get("show-a")
	s.mu.Lock()
	stateA := s.state
	s.mu.Unlock()
	if stateA != pkgaudio.StatePlaying {
		t.Fatalf("show-a state = %q, want playing", stateA)
	}
	if _, ok := requestedLTC(t, m); !ok {
		t.Fatal("show-b's stop killed the LTC run show-a still owns")
	}
}

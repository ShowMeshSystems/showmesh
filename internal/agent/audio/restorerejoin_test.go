package audio

import (
	"context"
	"sync"
	"testing"
	"time"

	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// startRecordingEngine remembers every Start position and counts Seeks.
type startRecordingEngine struct {
	*FakeEngine
	mu        sync.Mutex
	positions []time.Duration
	seeks     []time.Duration
}

func newStartRecordingEngine(now func() time.Time) *startRecordingEngine {
	return &startRecordingEngine{FakeEngine: NewFakeEngine(now)}
}

func (e *startRecordingEngine) Available() (bool, string) { return true, "" }

func (e *startRecordingEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	e.positions = append(e.positions, position)
	e.mu.Unlock()
	return e.FakeEngine.Start(ctx, handle, position)
}

func (e *startRecordingEngine) Seek(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	e.seeks = append(e.seeks, position)
	e.mu.Unlock()
	return e.FakeEngine.Seek(ctx, handle, position)
}

func (e *startRecordingEngine) starts() []time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Duration(nil), e.positions...)
}

func (e *startRecordingEngine) seekTargets() []time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Duration(nil), e.seeks...)
}

const (
	rejoinItemLength  = 10 * time.Second
	rejoinPlayedFor   = 4 * time.Second
	rejoinGrandmaster = "001122.fffe.334455"
)

// identifiedClock is a locked fake media clock that names its grandmaster,
// which is what makes a schedule persisted against it recognisable later.
func identifiedClock(mediaStart time.Time, wallNow func() time.Time) *fakeClockSource {
	src := newFakeClockSource(mediaStart)
	src.status.Provider = agentclock.ProviderExternal
	src.status.Domain, src.status.DomainKnown = 0, true
	src.status.GrandmasterIdentity, src.status.GMKnown = rejoinGrandmaster, true
	src.setWallNow(wallNow)
	return src
}

// rejoinFixture is a two-item scheduled playlist started through one
// Manager and left rejoinPlayedFor into its first item, ready to be
// restored through a second Manager over the same session store.
type rejoinFixture struct {
	boundaryFixture
	t0 time.Time
}

func newRejoinFixture(t *testing.T, repeat pkgaudio.RepeatMode) *rejoinFixture {
	t.Helper()
	f := &rejoinFixture{boundaryFixture: *newBoundaryFixture(t)}
	f.dec.def = knownDurationResult(rejoinItemLength)
	f.media = identifiedClock(time.Unix(4_000_000_000, 0), f.clk.now)
	f.m.SetClockSource(f.media)
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{
		f.item(t, "item-a", "a.wav", "asset-a", 0),
		f.item(t, "item-b", "b.wav", "asset-b", 1),
	}, repeat, pkgaudio.ItemTransitionGapless)
	out, t0 := f.startAt(t, time.Second)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("scheduled StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}
	f.t0 = t0
	f.clk.advance(rejoinPlayedFor)
	f.media.advance(rejoinPlayedFor)
	return f
}

// restart restores the fixture's session through a fresh Manager after an
// outage, against clockAfter (built by the caller from the media instant
// the outage ends on).
func (f *rejoinFixture) restart(t *testing.T, outage time.Duration, clockAfter func(mediaNow time.Time) ClockSource) (*Manager, *startRecordingEngine) {
	t.Helper()
	f.clk.advance(outage)
	f.media.advance(outage)
	engine := newStartRecordingEngine(f.clk.now)
	m := NewManager(engine, NewFileSessionStore(f.dir), f.dir, f.dec, f.clk.now, nil)
	if src := clockAfter(f.media.Now(context.Background()).Time); src != nil {
		m.SetClockSource(src)
	}
	if err := m.RestoreAll(context.Background()); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	return m, engine
}

func (f *rejoinFixture) sameClock(mediaNow time.Time) ClockSource {
	return identifiedClock(mediaNow, f.clk.now)
}

func restoredSession(t *testing.T, m *Manager, id pkgaudio.SessionID) *Session {
	t.Helper()
	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not restored")
	}
	return s
}

func wantOneStartAt(t *testing.T, engine *startRecordingEngine, want time.Duration) {
	t.Helper()
	starts := engine.starts()
	if len(starts) != 1 || starts[0] != want {
		t.Fatalf("engine starts = %v, want exactly one at %s", starts, want)
	}
}

func TestRestartMidItemRejoinsAtTheScheduledPosition(t *testing.T) {
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	const outage = 3 * time.Second
	m, engine := f.restart(t, outage, f.sameClock)

	wantOneStartAt(t, engine, rejoinPlayedFor+outage)
	s := restoredSession(t, m, f.id)
	s.mu.Lock()
	itemID, state, schedule := s.currentItemID, s.state, s.schedule
	s.mu.Unlock()
	if itemID != "item-a" || state != pkgaudio.StatePlaying {
		t.Fatalf("restored to %s in state %s, want item-a playing", itemID, state)
	}
	if schedule == nil || !schedule.itemStartAt.Equal(f.t0) || !schedule.boundaryAt.Equal(f.t0.Add(rejoinItemLength)) {
		t.Fatalf("restored item schedule = %+v, want item start %s and boundary one item later", schedule, f.t0)
	}
}

func TestFirstTimelineSnapshotAfterRestartReportsTheRestart(t *testing.T) {
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	m, engine := f.restart(t, 3*time.Second, f.sameClock)

	snap := m.TimelineSnapshot(context.Background())
	if !snap.Scheduled || snap.ScheduledAtNs != f.t0.UnixNano() {
		t.Fatalf("timeline after restart = %+v, want scheduled at the original instant %d", snap, f.t0.UnixNano())
	}
	if snap.LastResyncReason != ResyncReasonAgentRestart || snap.Resyncs != 0 {
		t.Fatalf("timeline after restart reports %d resyncs, last %q; want no seek counted and %q", snap.Resyncs, snap.LastResyncReason, ResyncReasonAgentRestart)
	}

	// The first evaluation carries the restart as its cause, so an error
	// beyond the threshold is corrected once; the same error on a later
	// evaluation, with no cause behind it, is left alone.
	m.SetSettings(Settings{DriftIgnoreThresholdMs: 20})
	engine.SetPresentedAdjust(-200 * time.Millisecond)
	sourceAfter := m.clockSourceSnapshot().(*fakeClockSource)
	f.clk.advance(time.Second)
	sourceAfter.advance(time.Second)
	m.watchTick(context.Background())

	wantSeek := rejoinPlayedFor + 3*time.Second + time.Second
	if seeks := engine.seekTargets(); len(seeks) != 1 || seeks[0] != wantSeek {
		t.Fatalf("seeks after the first evaluation = %v, want exactly one to %s", seeks, wantSeek)
	}
	snap = m.TimelineSnapshot(context.Background())
	if snap.Resyncs != 1 || snap.LastResyncReason != ResyncReasonAgentRestart {
		t.Fatalf("timeline after the corrective seek reports %d resyncs, last %q; want 1, %q", snap.Resyncs, snap.LastResyncReason, ResyncReasonAgentRestart)
	}

	engine.SetPresentedAdjust(-400 * time.Millisecond)
	f.clk.advance(time.Second)
	sourceAfter.advance(time.Second)
	m.watchTick(context.Background())
	if seeks := engine.seekTargets(); len(seeks) != 1 {
		t.Fatalf("a later evaluation with no cause seeked: %v", seeks)
	}
}

func TestRestartAcrossAnItemBoundaryRejoinsInTheNextItem(t *testing.T) {
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	const outage = 9 * time.Second
	m, engine := f.restart(t, outage, f.sameClock)

	wantOneStartAt(t, engine, rejoinPlayedFor+outage-rejoinItemLength)
	s := restoredSession(t, m, f.id)
	s.mu.Lock()
	itemID, index, schedule := s.currentItemID, s.currentIndex, s.schedule
	s.mu.Unlock()
	if itemID != "item-b" || index != 1 {
		t.Fatalf("restored to %s at index %d, want item-b at index 1", itemID, index)
	}
	if schedule == nil || !schedule.itemStartAt.Equal(f.t0.Add(rejoinItemLength)) {
		t.Fatalf("restored item schedule = %+v, want item start one item after %s", schedule, f.t0)
	}
	// A running node holds no timeline past its first item either.
	if snap := m.TimelineSnapshot(context.Background()); snap.Scheduled {
		t.Fatalf("timeline after rejoining in a later item = %+v, want none", snap)
	}
}

func TestRestartOfARepeatingSessionRejoinsInTheCurrentPass(t *testing.T) {
	f := newRejoinFixture(t, pkgaudio.RepeatPlaylist)
	// 4s played plus 61s away is 65s on a 20s loop: 5s into item-a again.
	const outage = 61 * time.Second
	m, engine := f.restart(t, outage, f.sameClock)

	wantOneStartAt(t, engine, 5*time.Second)
	s := restoredSession(t, m, f.id)
	s.mu.Lock()
	itemID, schedule := s.currentItemID, s.schedule
	s.mu.Unlock()
	if itemID != "item-a" {
		t.Fatalf("restored to %s, want item-a", itemID)
	}
	if schedule == nil || !schedule.itemStartAt.Equal(f.t0.Add(60*time.Second)) {
		t.Fatalf("restored item schedule = %+v, want item start 60s after %s", schedule, f.t0)
	}
}

func TestRestartPastTheEndOfANonRepeatingSessionStartsNothing(t *testing.T) {
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	m, engine := f.restart(t, 30*time.Second, f.sameClock)

	if starts := engine.starts(); len(starts) != 0 {
		t.Fatalf("engine starts = %v, want none", starts)
	}
	if live, _ := engine.LiveHandles(context.Background()); len(live) != 0 {
		t.Fatalf("engine holds handles %v, want none loaded", live)
	}
	s := restoredSession(t, m, f.id)
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StateCompleted {
		t.Fatalf("state = %s, want completed", state)
	}
	rec, ok, err := NewFileSessionStore(f.dir).Load(f.id)
	if err != nil || !ok || rec.SessionState != pkgaudio.StateCompleted {
		t.Fatalf("persisted record = %+v (ok %v, err %v), want completed", rec.SessionState, ok, err)
	}
}

// TestRestartWithoutTheSameClockStartsOnArrival covers every way the
// restored node can fail to show its media clock is the one the schedule
// was read on. Each starts on arrival from the stored position with no
// timeline, and keeps the stored item boundary only when the media clock
// can be read at all, as a restore did before a rejoin existed.
func TestRestartWithoutTheSameClockStartsOnArrival(t *testing.T) {
	type clockCase struct {
		after        func(f *rejoinFixture, mediaNow time.Time) ClockSource
		keepBoundary bool
	}
	cases := map[string]clockCase{
		"no media clock wired": {after: func(*rejoinFixture, time.Time) ClockSource { return nil }},
		"media clock unreadable": {after: func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.valid, src.reason = false, "the hardware clock could not be read"
			return src
		}},
		"provider not locked": {keepBoundary: true, after: func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.setState(agentclock.StateAcquiring, "startup")
			return src
		}},
		"different grandmaster": {keepBoundary: true, after: func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.status.GrandmasterIdentity = "aabbcc.fffe.ddeeff"
			return src
		}},
		"grandmaster not reported": {keepBoundary: true, after: func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.status.GMKnown = false
			return src
		}},
		"different domain": {keepBoundary: true, after: func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.status.Domain = 7
			return src
		}},
		"different provider": {keepBoundary: true, after: func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.status.Provider = agentclock.ProviderFPP
			return src
		}},
		"clock reads earlier than the stored instant": {keepBoundary: true, after: func(f *rejoinFixture, _ time.Time) ClockSource {
			return identifiedClock(f.t0.Add(-time.Hour), f.clk.now)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRejoinFixture(t, pkgaudio.RepeatNone)
			m, engine := f.restart(t, 3*time.Second, func(mediaNow time.Time) ClockSource { return tc.after(f, mediaNow) })

			wantOneStartAt(t, engine, 0)
			s := restoredSession(t, m, f.id)
			s.mu.Lock()
			itemID, schedule, timeline := s.currentItemID, s.schedule, s.timeline
			s.mu.Unlock()
			if itemID != "item-a" || timeline != nil {
				t.Fatalf("restored to %s with timeline %+v, want item-a and no timeline", itemID, timeline)
			}
			if kept := schedule != nil && schedule.itemStartAt.Equal(f.t0); kept != tc.keepBoundary {
				t.Fatalf("restored item schedule = %+v, want the stored boundary kept: %v", schedule, tc.keepBoundary)
			}
			rec, ok, err := NewFileSessionStore(f.dir).Load(f.id)
			if err != nil || !ok {
				t.Fatalf("load persisted record: ok %v, err %v", ok, err)
			}
			if tc.keepBoundary && rec.ScheduleClock == nil {
				t.Fatal("the record lost its clock identity, so a later restart could never rejoin")
			}
		})
	}
}

// TestRestartOfARecordWithNoClockIdentityRestoresAsBefore is a session
// file written before a schedule carried its clock: started from the
// stored position, with the stored item boundary reinstalled.
func TestRestartOfARecordWithNoClockIdentityRestoresAsBefore(t *testing.T) {
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	store := NewFileSessionStore(f.dir)
	rec, ok, err := store.Load(f.id)
	if err != nil || !ok {
		t.Fatalf("load persisted record: ok %v, err %v", ok, err)
	}
	if rec.ScheduleClock == nil || !rec.TimelineActive {
		t.Fatalf("a scheduled start persisted clock %+v, timeline active %v; want both", rec.ScheduleClock, rec.TimelineActive)
	}
	rec.ScheduleClock = nil
	rec.TimelineActive, rec.TimelineT0, rec.TimelineStartPosition = false, time.Time{}, 0
	if err := store.Save(f.id, rec); err != nil {
		t.Fatalf("save older-shaped record: %v", err)
	}

	m, engine := f.restart(t, 3*time.Second, f.sameClock)

	wantOneStartAt(t, engine, 0)
	s := restoredSession(t, m, f.id)
	s.mu.Lock()
	schedule, timeline := s.schedule, s.timeline
	s.mu.Unlock()
	if schedule == nil || !schedule.itemStartAt.Equal(f.t0) {
		t.Fatalf("restored item schedule = %+v, want the stored item start %s", schedule, f.t0)
	}
	if timeline != nil {
		t.Fatalf("restored with timeline %+v, want none", timeline)
	}
}

func TestRestartWithAnItemOfUnknownLengthIgnoresTheStoredSchedule(t *testing.T) {
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	f.dec.setResult("b.wav", unknownDurationResult())
	_, engine := f.restart(t, 25*time.Second, f.sameClock)

	wantOneStartAt(t, engine, 0)
}

// restartUnbound restores the fixture's session the way a real agent
// boots: no clock wired and no engine bound, so the restore is deferred.
func (f *rejoinFixture) restartUnbound(t *testing.T, outage time.Duration) (*Manager, *SwitchableEngine, *startRecordingEngine) {
	t.Helper()
	f.clk.advance(outage)
	f.media.advance(outage)
	switchable := NewSwitchableEngine()
	m := NewManager(switchable, NewFileSessionStore(f.dir), f.dir, f.dec, f.clk.now, nil)
	if err := m.RestoreAll(context.Background()); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if got := m.PendingRestoreCount(); got != 1 {
		t.Fatalf("pending restores after an unbound RestoreAll = %d, want 1", got)
	}
	return m, switchable, newStartRecordingEngine(f.clk.now)
}

func shrinkRestartClockWait(t *testing.T, bound time.Duration) {
	t.Helper()
	prevBound, prevPoll := restartClockWaitBound, restartClockWaitPollInterval
	restartClockWaitBound, restartClockWaitPollInterval = bound, 5*time.Millisecond
	t.Cleanup(func() { restartClockWaitBound, restartClockWaitPollInterval = prevBound, prevPoll })
}

func waitForStarts(t *testing.T, engine *startRecordingEngine, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(engine.starts()) < want {
		if time.Now().After(deadline) {
			t.Fatalf("engine starts = %v, waiting for %d", engine.starts(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRestartRejoinsWhenTheClockLocksAfterTheEngineBinding is the order a
// real agent sees: restore with nothing bound, then the audio binding,
// then the clock locking a little later.
func TestRestartRejoinsWhenTheClockLocksAfterTheEngineBinding(t *testing.T) {
	shrinkRestartClockWait(t, 10*time.Second)
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	const outage = 3 * time.Second
	m, switchable, engine := f.restartUnbound(t, outage)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.RebindEngine(ctx, switchable, engine, RebindReasonEngineRebind)
	if starts := engine.starts(); len(starts) != 0 {
		t.Fatalf("engine starts before the clock exists = %v, want none", starts)
	}
	if got := m.PendingRestoreCount(); got != 1 {
		t.Fatalf("pending restores while waiting for the clock = %d, want 1", got)
	}

	src := identifiedClock(f.media.Now(ctx).Time, f.clk.now)
	src.setState(agentclock.StateAcquiring, "startup")
	m.SetClockSource(src)
	time.Sleep(50 * time.Millisecond)
	if starts := engine.starts(); len(starts) != 0 {
		t.Fatalf("engine starts while the clock is still acquiring = %v, want none", starts)
	}
	src.setState(agentclock.StateLocked, "")

	waitForStarts(t, engine, 1)
	wantOneStartAt(t, engine, rejoinPlayedFor+outage)
	if snap := m.TimelineSnapshot(ctx); snap.LastResyncReason != ResyncReasonAgentRestart {
		t.Fatalf("timeline after the rejoin = %+v, want the restart reported", snap)
	}
	if got := m.PendingRestoreCount(); got != 0 {
		t.Fatalf("pending restores after the rejoin = %d, want 0", got)
	}
}

func TestRestartRejoinsWhenTheClockIsLockedBeforeTheEngineBinding(t *testing.T) {
	shrinkRestartClockWait(t, 10*time.Second)
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	const outage = 3 * time.Second
	m, switchable, engine := f.restartUnbound(t, outage)

	m.SetClockSource(f.sameClock(f.media.Now(context.Background()).Time))
	m.RebindEngine(context.Background(), switchable, engine, RebindReasonEngineRebind)

	wantOneStartAt(t, engine, rejoinPlayedFor+outage)
}

func TestRestartStartsOnArrivalWhenTheClockNeverLocksInTime(t *testing.T) {
	shrinkRestartClockWait(t, 60*time.Millisecond)
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	m, switchable, engine := f.restartUnbound(t, 3*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := identifiedClock(f.media.Now(ctx).Time, f.clk.now)
	src.setState(agentclock.StateAcquiring, "startup")
	m.SetClockSource(src)

	m.RebindEngine(ctx, switchable, engine, RebindReasonEngineRebind)
	if starts := engine.starts(); len(starts) != 0 {
		t.Fatalf("engine starts before the wait ran out = %v, want none", starts)
	}

	waitForStarts(t, engine, 1)
	wantOneStartAt(t, engine, 0)
	// The wait is spent and its goroutine gone: nothing starts again.
	time.Sleep(30 * time.Millisecond)
	wantOneStartAt(t, engine, 0)
}

func TestRestartDoesNotWaitForADifferentClock(t *testing.T) {
	shrinkRestartClockWait(t, time.Hour)
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	m, switchable, engine := f.restartUnbound(t, 3*time.Second)
	src := identifiedClock(f.media.Now(context.Background()).Time, f.clk.now)
	src.status.GrandmasterIdentity = "aabbcc.fffe.ddeeff"
	m.SetClockSource(src)

	m.RebindEngine(context.Background(), switchable, engine, RebindReasonEngineRebind)

	wantOneStartAt(t, engine, 0)
}

// TestRestartOfAPausedRecordWithAScheduleStaysPaused is a record that says
// paused while still carrying a schedule: it comes back paused at its
// stored position, and the schedule is not consulted.
func TestRestartOfAPausedRecordWithAScheduleStaysPaused(t *testing.T) {
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	store := NewFileSessionStore(f.dir)
	rec, ok, err := store.Load(f.id)
	if err != nil || !ok || !rec.ScheduleActive || rec.ScheduleClock == nil {
		t.Fatalf("persisted record: ok %v, err %v, schedule active %v", ok, err, rec.ScheduleActive)
	}
	rec.SessionState = pkgaudio.StatePaused
	if err := store.Save(f.id, rec); err != nil {
		t.Fatalf("save paused record: %v", err)
	}

	m, engine := f.restart(t, 3*time.Second, f.sameClock)

	// Restoring a paused session loads it at its stored position and
	// pauses it at once; that is the only engine start.
	wantOneStartAt(t, engine, 0)
	s := restoredSession(t, m, f.id)
	s.mu.Lock()
	state, handle, schedule, timeline := s.state, s.handle, s.schedule, s.timeline
	s.mu.Unlock()
	if state != pkgaudio.StatePaused || schedule != nil || timeline != nil {
		t.Fatalf("restored in state %s with schedule %+v and timeline %+v, want paused with neither", state, schedule, timeline)
	}
	obs, err := engine.Observe(context.Background(), handle)
	if err != nil || obs.State != pkgaudio.StatePaused {
		t.Fatalf("engine reports %s (err %v), want paused", obs.State, err)
	}
}

// savingStore records every record written, so a test can read the one a
// crash at that instant would have left behind.
type savingStore struct {
	SessionStore
	mu    sync.Mutex
	saved []PersistedSession
}

func (s *savingStore) Save(id pkgaudio.SessionID, rec PersistedSession) error {
	s.mu.Lock()
	s.saved = append(s.saved, rec)
	s.mu.Unlock()
	return s.SessionStore.Save(id, rec)
}

// TestEveryRecordWrittenAtAnItemBoundaryNamesItsOwnItemStart covers a
// crash between the two writes a scheduled item change makes: the first
// already carries the new item's own start instant.
func TestEveryRecordWrittenAtAnItemBoundaryNamesItsOwnItemStart(t *testing.T) {
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	store := &savingStore{SessionStore: f.m.store}
	f.m.store = store

	f.clk.advance(rejoinItemLength - rejoinPlayedFor - time.Second)
	f.media.advance(rejoinItemLength - rejoinPlayedFor - time.Second)
	f.m.watchTick(context.Background())
	f.clk.advance(time.Second)
	f.media.advance(time.Second)
	f.m.watchTick(context.Background())
	if got := f.currentItemID(t); got != "item-b" {
		t.Fatalf("current item after the boundary = %s, want item-b", got)
	}

	var onNewItem int
	for _, rec := range store.saved {
		if rec.CurrentIndex != 1 {
			continue
		}
		onNewItem++
		if !rec.ScheduleActive || rec.ScheduleItemIndex != 1 || !rec.ScheduleItemStartAt.Equal(f.t0.Add(rejoinItemLength)) {
			t.Fatalf("a record on item-b carries schedule index %d starting %s, want index 1 starting one item after %s",
				rec.ScheduleItemIndex, rec.ScheduleItemStartAt, f.t0)
		}
	}
	if onNewItem < 2 {
		t.Fatalf("saw %d records on item-b, want both writes of the item change", onNewItem)
	}
}

// TestRestartOfARecordWhoseScheduleNamesAnotherItemStartsOnArrival is a
// record written after an advance moved on but before the schedule was
// re-anchored: the stored instant belongs to the previous item.
func TestRestartOfARecordWhoseScheduleNamesAnotherItemStartsOnArrival(t *testing.T) {
	f := newRejoinFixture(t, pkgaudio.RepeatNone)
	store := NewFileSessionStore(f.dir)
	rec, ok, err := store.Load(f.id)
	if err != nil || !ok {
		t.Fatalf("load persisted record: ok %v, err %v", ok, err)
	}
	rec.CurrentIndex, rec.CurrentItemID = 1, "item-b"
	if err := store.Save(f.id, rec); err != nil {
		t.Fatalf("save record: %v", err)
	}

	m, engine := f.restart(t, 3*time.Second, f.sameClock)

	wantOneStartAt(t, engine, 0)
	s := restoredSession(t, m, f.id)
	s.mu.Lock()
	itemID, schedule := s.currentItemID, s.schedule
	s.mu.Unlock()
	if itemID != "item-b" || schedule != nil {
		t.Fatalf("restored to %s with schedule %+v, want item-b under no schedule", itemID, schedule)
	}
}

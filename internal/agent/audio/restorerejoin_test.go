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
	if snap.LastResyncReason != ResyncReasonAgentRestart || snap.Resyncs != 1 {
		t.Fatalf("timeline after restart reports %d resyncs, last %q; want 1, %q", snap.Resyncs, snap.LastResyncReason, ResyncReasonAgentRestart)
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
	if snap.Resyncs != 2 || snap.LastResyncReason != ResyncReasonAgentRestart {
		t.Fatalf("timeline after the corrective seek reports %d resyncs, last %q; want 2, %q", snap.Resyncs, snap.LastResyncReason, ResyncReasonAgentRestart)
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

// TestRestartIgnoresTheStoredScheduleWithoutTheSameClock covers every way
// the restored node can fail to show its media clock is the one the
// schedule was read on. Each restores as an unsynchronized node does:
// started on arrival from the stored position, under no schedule.
func TestRestartIgnoresTheStoredScheduleWithoutTheSameClock(t *testing.T) {
	cases := map[string]func(f *rejoinFixture, mediaNow time.Time) ClockSource{
		"no media clock wired": func(*rejoinFixture, time.Time) ClockSource { return nil },
		"media clock unreadable": func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.valid, src.reason = false, "the hardware clock could not be read"
			return src
		},
		"provider not locked": func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.setState(agentclock.StateAcquiring, "startup")
			return src
		},
		"different grandmaster": func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.status.GrandmasterIdentity = "aabbcc.fffe.ddeeff"
			return src
		},
		"grandmaster not reported": func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.status.GMKnown = false
			return src
		},
		"different domain": func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.status.Domain = 7
			return src
		},
		"different provider": func(f *rejoinFixture, mediaNow time.Time) ClockSource {
			src := identifiedClock(mediaNow, f.clk.now)
			src.status.Provider = agentclock.ProviderFPP
			return src
		},
		"clock reads earlier than the stored instant": func(f *rejoinFixture, _ time.Time) ClockSource {
			return identifiedClock(f.t0.Add(-time.Hour), f.clk.now)
		},
	}
	for name, clockAfter := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRejoinFixture(t, pkgaudio.RepeatNone)
			m, engine := f.restart(t, 3*time.Second, func(mediaNow time.Time) ClockSource { return clockAfter(f, mediaNow) })

			wantOneStartAt(t, engine, 0)
			s := restoredSession(t, m, f.id)
			s.mu.Lock()
			itemID, schedule, timeline := s.currentItemID, s.schedule, s.timeline
			s.mu.Unlock()
			if itemID != "item-a" {
				t.Fatalf("restored to %s, want item-a", itemID)
			}
			if schedule != nil || timeline != nil {
				t.Fatalf("restored with schedule %+v and timeline %+v, want neither", schedule, timeline)
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

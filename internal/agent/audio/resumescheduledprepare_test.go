package audio

import (
	"context"
	"sync"
	"testing"
	"time"

	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// orderLog records the relative order of engine and clock calls a
// scheduled resume makes, so a test can prove ordering directly instead
// of inferring it from timing.
type orderLog struct {
	mu     sync.Mutex
	events []string
}

func (o *orderLog) record(event string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (o *orderLog) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = nil
}

func (o *orderLog) indexOf(event string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, e := range o.events {
		if e == event {
			return i
		}
	}
	return -1
}

func (o *orderLog) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, len(o.events))
	copy(out, o.events)
	return out
}

// slowPrepareEngine is a [FakeEngine] whose Load simulates prepare taking
// measurable wall-clock and media-clock time, exactly as a real pipeline
// opening, decoding and prerolling an asset takes real time on both
// clocks whether or not the code waiting on them accounts for it.
type slowPrepareEngine struct {
	*FakeEngine
	log     *orderLog
	wall    *clock
	media   *fakeClockSource
	prepare time.Duration
}

func (e *slowPrepareEngine) Available() (bool, string) { return true, "" }

func (e *slowPrepareEngine) Load(ctx context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	e.log.record("engine:load")
	e.wall.advance(e.prepare)
	e.media.advance(e.prepare)
	return e.FakeEngine.Load(ctx, handle, media, duration)
}

func (e *slowPrepareEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.log.record("engine:start")
	return e.FakeEngine.Start(ctx, handle, position)
}

// orderedClockSource is [fakeClockSource] that also records when it is
// consulted, so a test can prove a scheduled resume's own schedule
// resolution happens only after its prepare work already ran.
type orderedClockSource struct {
	*fakeClockSource
	log *orderLog
}

func (o *orderedClockSource) Poll(ctx context.Context) agentclock.Status {
	o.log.record("clock:poll")
	return o.fakeClockSource.Poll(ctx)
}

func (o *orderedClockSource) Now(ctx context.Context) agentclock.MediaTime {
	o.log.record("clock:now")
	return o.fakeClockSource.Now(ctx)
}

// slowResumeFixture is a single-item, paused session against an engine
// whose Load takes measurable time on both clocks, so a test can prove
// WHEN, relative to that prepare work and to the scheduled instant,
// engine.Start actually runs.
type slowResumeFixture struct {
	m        *Manager
	engine   *slowPrepareEngine
	wall     *clock
	rawMedia *fakeClockSource
	log      *orderLog
	id       pkgaudio.SessionID
}

func newSlowResumeFixture(t *testing.T, prepare time.Duration) *slowResumeFixture {
	t.Helper()
	dir := t.TempDir()
	wall := newClock(time.Unix(1_700_000_000, 0))
	rawMedia := newFakeClockSource(time.Unix(4_000_000_000, 0))
	log := &orderLog{}
	engine := &slowPrepareEngine{FakeEngine: NewFakeEngine(wall.now), log: log, wall: wall, media: rawMedia, prepare: prepare}
	media := &orderedClockSource{fakeClockSource: rawMedia, log: log}
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, wall.now, nil)
	m.SetClockSource(media)

	const id = pkgaudio.SessionID("bed-resume-1")
	f := &slowResumeFixture{m: m, engine: engine, wall: wall, rawMedia: rawMedia, log: log, id: id}

	ctx := context.Background()
	item := pkgaudio.PlaylistItem{ItemID: "item-1", Index: 0, Media: writeTestAsset(t, dir, "a.wav", "asset-a", []byte("item-1"))}
	pl := pkgaudio.PlaylistRef{
		OwnerKind: "show", OwnerID: "night-session", OwnerRevision: 1,
		Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyRestart, RequestedTransition: pkgaudio.ItemTransitionSequential,
		Items: []pkgaudio.PlaylistItem{item},
	}
	if out := m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(pl)}); out.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Apply refused: %s", out.Reason)
	}
	if out := m.Start(ctx, id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}
	if out := m.Pause(ctx, id, "inv-pause", 3); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("Pause = %q (%s), want position", out.Outcome, out.Reason)
	}
	log.reset()
	return f
}

func (f *slowResumeFixture) session(t *testing.T) *Session {
	t.Helper()
	s, ok := f.m.get(f.id)
	if !ok {
		t.Fatal("session not found")
	}
	return s
}

// resumeAt calls [Manager.ResumeAt] at lead ahead of the current media
// instant, in a background goroutine, and advances the media clock to
// that instant once the call has read it at least once -- mirroring
// [boundaryFixture.resumeAt], which this file cannot reuse directly since
// it needs its own clock-source and engine wrappers to record order.
func (f *slowResumeFixture) resumeAt(t *testing.T, invocation pkgaudio.InvocationID, revision pkgaudio.Revision, lead time.Duration, point *ResumePoint) (pkgaudio.OutcomeResult, time.Time) {
	t.Helper()
	ctx := context.Background()
	t0 := f.rawMedia.Now(ctx).Time.Add(lead)
	before := f.rawMedia.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- f.m.ResumeAt(context.Background(), f.id, invocation, revision, t0.UnixNano(), point)
	}()
	f.rawMedia.waitForReads(t, before+1)
	if remaining := t0.Sub(f.rawMedia.Now(ctx).Time); remaining > 0 {
		f.rawMedia.advance(remaining)
	}
	select {
	case out := <-done:
		return out, t0
	case <-time.After(10 * time.Second):
		t.Fatal("ResumeAt never returned after the media clock reached its instant")
		return pkgaudio.OutcomeResult{}, t0
	}
}

// timelineErrorMs runs one timeline evaluation and returns its measured
// error, fatally failing the test if nothing was measured.
func (f *slowResumeFixture) timelineErrorMs(t *testing.T) int64 {
	t.Helper()
	s := f.session(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	f.m.evaluateTimelineLocked(context.Background(), s)
	snap, ok := s.timelineSnapshotLocked()
	if !ok || !snap.Measured {
		t.Fatalf("timeline not measured after the scheduled resume (ok=%v, snap=%+v)", ok, snap)
	}
	return snap.ErrorMs
}

// TestScheduledResumePreparesBeforeWaitingForTheInstant proves the fix:
// ResumeAt's release-and-prepare work (engine.Load, standing in for a
// real pipeline's open/decode/preroll) runs BEFORE the schedule is
// resolved and waited on, and engine.Start runs only once that wait is
// over -- never the reverse, which is what let a slow prepare add itself
// on top of the scheduled instant instead of finishing before it.
func TestScheduledResumePreparesBeforeWaitingForTheInstant(t *testing.T) {
	const prepareDelay = 900 * time.Millisecond
	f := newSlowResumeFixture(t, prepareDelay)

	out, _ := f.resumeAt(t, "inv-resume", 4, 2*time.Second, nil)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("ResumeAt = %q (%s), want started", out.Outcome, out.Reason)
	}

	events := f.log.snapshot()
	loadAt := f.log.indexOf("engine:load")
	pollAt := f.log.indexOf("clock:poll")
	startAt := f.log.indexOf("engine:start")
	if loadAt < 0 || pollAt < 0 || startAt < 0 {
		t.Fatalf("expected engine:load, clock:poll and engine:start to all be recorded, got %v", events)
	}
	if loadAt > pollAt {
		t.Fatalf("engine.Load ran at position %d, after the schedule's first clock read at %d (order: %v); prepare must finish before the instant is even resolved, not after", loadAt, pollAt, events)
	}
	if startAt < pollAt {
		t.Fatalf("engine.Start ran at position %d, before the schedule's clock read at %d (order: %v); the engine must only start once the wait for the instant is over", startAt, pollAt, events)
	}

	if errMs := f.timelineErrorMs(t); errMs < -50 || errMs > 50 {
		t.Fatalf("timeline error = %dms, want near zero; prepare's %s must not leak into presentation lateness", errMs, prepareDelay)
	}
}

// TestScheduledResumeRefusedAfterPrepareStaysResumable proves the
// consequence of preparing before the wait: a resume whose prepare
// finishes only after the requested instant has itself passed (prepare
// is now the thing that can make an instant go stale, since it runs
// first) is refused, exactly as an instant that was already stale on
// arrival would be -- and the session is left honestly Paused with no
// loaded handle, not silently holding one from the prepare that already
// ran, so a later plain Resume still works rather than calling
// Engine.Resume on a handle that was never actually paused.
func TestScheduledResumeRefusedAfterPrepareStaysResumable(t *testing.T) {
	const prepareDelay = 900 * time.Millisecond
	f := newSlowResumeFixture(t, prepareDelay)
	ctx := context.Background()

	// lead is shorter than prepareDelay: prepare's own advance of the
	// media clock pushes it past this instant before the schedule is
	// ever resolved, so the refusal happens only after prepare already
	// released and reloaded the engine handle.
	const lead = 200 * time.Millisecond
	t0 := f.rawMedia.Now(ctx).Time.Add(lead)
	out := f.m.ResumeAt(ctx, f.id, "inv-resume", 4, t0.UnixNano(), nil)
	if out.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("ResumeAt = %q (%s), want refused", out.Outcome, out.Reason)
	}
	if !containsString(out.Reason, pkgaudio.ReasonScheduledStartInPast) {
		t.Fatalf("refusal reason = %q, want it to carry %q", out.Reason, pkgaudio.ReasonScheduledStartInPast)
	}

	s := f.session(t)
	s.mu.Lock()
	state, handleLoaded, bookmark := s.state, s.handleLoaded, s.bookmark
	s.mu.Unlock()
	if state != pkgaudio.StatePaused {
		t.Fatalf("state after the refused resume = %q, want Paused", state)
	}
	if handleLoaded {
		t.Fatal("a handle stayed loaded after the refused resume; a later plain Resume would call Engine.Resume on a handle that was never actually paused")
	}
	if bookmark == nil {
		t.Fatal("bookmark was lost by the refused resume; the session can no longer resume from where it was paused")
	}

	if out := f.m.Resume(ctx, f.id, "inv-resume-2", 5, nil); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Resume after the refused ResumeAt = %q (%s), want started", out.Outcome, out.Reason)
	}
}

// TestScheduledResumeWithPointPreparesBeforeWaitingForTheInstant is the
// same ordering proof for a resume carrying an explicit [ResumePoint]
// (ADR-049 decision 4, amended), which takes a different branch through
// [Session.applyResumePointLocked] before reaching the same prepare/wait
// sequence.
func TestScheduledResumeWithPointPreparesBeforeWaitingForTheInstant(t *testing.T) {
	const prepareDelay = 900 * time.Millisecond
	f := newSlowResumeFixture(t, prepareDelay)

	point := &ResumePoint{ItemID: "item-1", Index: 0, Position: 750 * time.Millisecond}
	out, _ := f.resumeAt(t, "inv-resume", 4, 2*time.Second, point)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("ResumeAt(point) = %q (%s), want started", out.Outcome, out.Reason)
	}

	events := f.log.snapshot()
	loadAt := f.log.indexOf("engine:load")
	pollAt := f.log.indexOf("clock:poll")
	startAt := f.log.indexOf("engine:start")
	if loadAt < 0 || pollAt < 0 || startAt < 0 {
		t.Fatalf("expected engine:load, clock:poll and engine:start to all be recorded, got %v", events)
	}
	if loadAt > pollAt {
		t.Fatalf("engine.Load ran at position %d, after the schedule's first clock read at %d (order: %v); prepare must finish before the instant is even resolved, not after", loadAt, pollAt, events)
	}
	if startAt < pollAt {
		t.Fatalf("engine.Start ran at position %d, before the schedule's clock read at %d (order: %v); the engine must only start once the wait for the instant is over", startAt, pollAt, events)
	}

	if errMs := f.timelineErrorMs(t); errMs < -50 || errMs > 50 {
		t.Fatalf("timeline error = %dms, want near zero; prepare's %s must not leak into presentation lateness", errMs, prepareDelay)
	}
}

package audio

import (
	"context"
	"sync"
	"testing"
	"time"

	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// fakeClockSource is a scriptable media clock: a status a test sets
// field by field, and a media instant it advances independently of the
// Manager's own wall clock, because the whole point of this seam is that
// those are two different clocks.
type fakeClockSource struct {
	mu     sync.Mutex
	status agentclock.Status
	media  time.Time
	valid  bool
	reason string

	// nowCalls counts reads, so a test can advance this clock only after
	// the code under test has already sampled it. Without that
	// sequencing, advancing races the schedule's own read and a start
	// meant to be in the future is sometimes resolved as already past.
	nowCalls int

	// wallNow times Poll's own cache entry in the same time domain the
	// Manager under test measures freshness against (its injected wall
	// clock), never real time.Now, so a test can control cache age
	// exactly. Defaults to time.Now when unset.
	wallNow func() time.Time

	pollCalls    int
	lastStatus   agentclock.Status
	lastPolledAt time.Time
	lastKnown    bool

	// pollEntered and pollGate, when armed by blockNextPoll, make Poll
	// close pollEntered and then block until pollGate is closed.
	pollEntered chan struct{}
	pollGate    chan struct{}
}

func newFakeClockSource(mediaStart time.Time) *fakeClockSource {
	return &fakeClockSource{
		status: agentclock.Status{State: agentclock.StateLocked, Timescale: agentclock.TimescalePTP},
		media:  mediaStart,
		valid:  true,
	}
}

func (f *fakeClockSource) Poll(context.Context) agentclock.Status {
	f.mu.Lock()
	f.pollCalls++
	entered, gate := f.pollEntered, f.pollGate
	f.pollEntered, f.pollGate = nil, nil
	f.mu.Unlock()

	if entered != nil {
		close(entered)
	}
	if gate != nil {
		<-gate
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastStatus = f.status
	if f.wallNow != nil {
		f.lastPolledAt = f.wallNow()
	} else {
		f.lastPolledAt = time.Now()
	}
	f.lastKnown = true
	return f.status
}

// blockNextPoll arms this fake so its NEXT Poll call (and only that one)
// closes entered, proving Poll was actually reached, and then blocks
// until release is closed.
func (f *fakeClockSource) blockNextPoll(entered, release chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pollEntered, f.pollGate = entered, release
}

// Last reports the status from this fake's most recent Poll call. ok is
// false until a test (or the code under test) has called Poll at least
// once, matching a real [clock.Tracker] that has never polled.
func (f *fakeClockSource) Last() (agentclock.Status, time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastStatus, f.lastPolledAt, f.lastKnown
}

// setWallNow wires this fake's cache-entry clock to fn, so a test can put
// the fake's cache in the same time domain as the Manager it is wired
// into.
func (f *fakeClockSource) setWallNow(fn func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wallNow = fn
}

// pollCount reports how many times Poll has been called, so a test can
// prove resolveScheduleLocked served a start from cache without polling.
func (f *fakeClockSource) pollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pollCalls
}

func (f *fakeClockSource) Now(context.Context) agentclock.MediaTime {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nowCalls++
	return agentclock.MediaTime{Time: f.media, Valid: f.valid, Reason: f.reason}
}

func (f *fakeClockSource) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nowCalls
}

// waitForReads blocks until this clock has been read at least want times.
func (f *fakeClockSource) waitForReads(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for f.reads() < want {
		if time.Now().After(deadline) {
			t.Fatalf("media clock was read %d times, waiting for %d", f.reads(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *fakeClockSource) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.media = f.media.Add(d)
}

func (f *fakeClockSource) setState(state agentclock.State, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status.State, f.status.Reason = state, reason
}

func (f *fakeClockSource) recordStep(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status.LastStepAt, f.status.LastStepKnown = at, true
}

// seekCountingEngine counts flushing seeks and remembers the last target,
// so a test can prove not merely that the position is right but that the
// position was moved exactly the number of times the rule allows.
type seekCountingEngine struct {
	*FakeEngine
	mu     sync.Mutex
	seeks  int
	target time.Duration
}

func newSeekCountingEngine(now func() time.Time) *seekCountingEngine {
	return &seekCountingEngine{FakeEngine: NewFakeEngine(now)}
}

func (e *seekCountingEngine) Available() (bool, string) { return true, "" }

func (e *seekCountingEngine) Seek(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	e.seeks++
	e.target = position
	e.mu.Unlock()
	return e.FakeEngine.Seek(ctx, handle, position)
}

func (e *seekCountingEngine) seekCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seeks
}

func (e *seekCountingEngine) lastTarget() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.target
}

// scheduledFixture is one prepared, startable session against a fake
// media clock and a seek-counting engine.
type scheduledFixture struct {
	m      *Manager
	engine *seekCountingEngine
	clk    *clock
	media  *fakeClockSource
	id     pkgaudio.SessionID
}

func newScheduledFixture(t *testing.T, thresholdMs int) *scheduledFixture {
	t.Helper()
	c := newClock(time.Unix(1_700_000_000, 0))
	dir := t.TempDir()
	engine := newSeekCountingEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: time.Hour}, c.now, nil)
	// A media clock deliberately far from the Manager's own wall clock,
	// so a test that accidentally measured against time.Now instead of
	// the media clock produces an obviously wrong number rather than a
	// plausible one.
	media := newFakeClockSource(time.Unix(4_000_000_000, 0))
	media.setWallNow(c.now)
	m.SetClockSource(media)
	if thresholdMs > 0 {
		m.SetSettings(Settings{
			DriftIgnoreThresholdMs: thresholdMs,
			DefaultFadeCurve:       DefaultSettings.DefaultFadeCurve,
			DefaultFadeDurationMs:  DefaultSettings.DefaultFadeDurationMs,
			DuckTargetGain:         DefaultSettings.DuckTargetGain,
			DuckFadeDurationMs:     DefaultSettings.DuckFadeDurationMs,

			DuckRestoreFadeDurationMs: DefaultSettings.DuckRestoreFadeDurationMs,
			LTCFrameRate:              DefaultSettings.LTCFrameRate,
			LTCDefaultStartOffset:     DefaultSettings.LTCDefaultStartOffset,
		})
	}

	const id = pkgaudio.SessionID("scheduled-1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	if out := m.Apply(context.Background(), id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); out.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Apply refused: %s", out.Reason)
	}
	return &scheduledFixture{m: m, engine: engine, clk: c, media: media, id: id}
}

// startScheduled starts the fixture's session at lead ahead of the
// current media instant, and advances the media clock to T0 so the start
// does not actually wait in real time.
func (f *scheduledFixture) startScheduled(t *testing.T, lead time.Duration) pkgaudio.OutcomeResult {
	t.Helper()
	t0 := f.media.Now(context.Background()).Time.Add(lead)
	before := f.media.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- f.m.StartAt(context.Background(), f.id, "inv-start", 2, t0.UnixNano())
	}()
	// Advance only once the start has already resolved the instant
	// against this clock, so the future T0 it was given is genuinely in
	// the future when it reads it.
	f.media.waitForReads(t, before+1)
	f.media.advance(lead)
	select {
	case out := <-done:
		if out.Outcome != pkgaudio.OutcomeStarted {
			t.Fatalf("scheduled StartAt = %q (%s), want started", out.Outcome, out.Reason)
		}
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("StartAt never returned after the media clock reached T0")
		return pkgaudio.OutcomeResult{}
	}
}

// tick advances both clocks by d and runs one supervision tick. The
// engine's presented running time follows the Manager's wall clock, so
// advancing both by the same amount is a node whose sink and media clock
// agree exactly.
func (f *scheduledFixture) tick(ctx context.Context, d time.Duration) {
	f.clk.advance(d)
	f.media.advance(d)
	f.m.watchTick(ctx)
}

func (f *scheduledFixture) timeline(t *testing.T) TimelineSnapshot {
	t.Helper()
	return f.m.TimelineSnapshot(context.Background())
}

// TestScheduledStartInThePastIsRefused is the seam's own show-continues
// rule pointing the unusual way: a node given a T0 it has already passed
// refuses, rather than starting late or clamping the instant to now.
func TestScheduledStartInThePastIsRefused(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()

	past := f.media.Now(ctx).Time.Add(-500 * time.Millisecond)
	out := f.m.StartAt(ctx, f.id, "inv-start", 2, past.UnixNano())

	if out.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("StartAt(past T0) outcome = %q (reason %q), want %q", out.Outcome, out.Reason, pkgaudio.OutcomeRefused)
	}
	if !containsString(out.Reason, pkgaudio.ReasonScheduledStartInPast) {
		t.Fatalf("refusal reason = %q, want it to carry %q", out.Reason, pkgaudio.ReasonScheduledStartInPast)
	}
	s, _ := f.m.get(f.id)
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state == pkgaudio.StatePlaying {
		t.Fatal("a refused scheduled start left the session Playing; it must not have started at all")
	}
}

// TestScheduledStartHonorsShortLeadFromFreshCacheWithoutPolling proves a
// lead shorter than an external clock provider's own Poll cost (RES-019
// evidence: pollViaUDS's pmc round trips run ~400ms) is still honored when
// the clock report loop has already polled recently: resolveScheduleLocked
// must use that cached status rather than pay for a live Poll before
// comparing the requested instant.
func TestScheduledStartHonorsShortLeadFromFreshCacheWithoutPolling(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()

	f.media.Poll(ctx) // prime the cache, as the agent's own clock report loop does
	pollsBefore := f.media.pollCount()

	const lead = 100 * time.Millisecond
	t0 := f.media.Now(ctx).Time.Add(lead)
	before := f.media.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- f.m.StartAt(ctx, f.id, "inv-start", 2, t0.UnixNano())
	}()
	f.media.waitForReads(t, before+1)

	if got := f.media.pollCount(); got != pollsBefore {
		t.Fatalf("resolveScheduleLocked polled the clock source %d extra time(s) despite a fresh cache, want 0", got-pollsBefore)
	}

	f.media.advance(lead)
	select {
	case out := <-done:
		if out.Outcome != pkgaudio.OutcomeStarted {
			t.Fatalf("scheduled StartAt = %q (%s), want started", out.Outcome, out.Reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StartAt never returned after the media clock reached T0")
	}
}

// TestScheduledStartFallsBackToPollWhenCachedStatusIsStale proves a cache
// older than clockStatusFreshnessBound is not trusted: resolveScheduleLocked
// must pay for one live Poll rather than decide a schedule off evidence
// that predates the agent's own clock report cadence.
func TestScheduledStartFallsBackToPollWhenCachedStatusIsStale(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()

	f.media.Poll(ctx) // prime the cache
	f.clk.advance(clockStatusFreshnessBound + time.Second)

	pollsBefore := f.media.pollCount()
	out := f.startScheduled(t, 100*time.Millisecond)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("scheduled StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}
	if got := f.media.pollCount(); got != pollsBefore+1 {
		t.Fatalf("resolveScheduleLocked polled %d extra time(s) after a stale cache, want exactly 1 fallback poll", got-pollsBefore)
	}
}

// TestScheduledStartWithCachedUnlockedStatusStartsOnArrival proves a fresh
// cached status that is not locked produces exactly today's
// start-on-arrival behaviour (the instant ignored, note carrying
// [pkgaudio.ReasonScheduledStartIgnored]) without polling the source
// again — the freshness cache changes only which status resolveScheduleLocked
// reads, never the accepted/ignored/refused rule itself.
func TestScheduledStartWithCachedUnlockedStatusStartsOnArrival(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()

	f.media.setState(agentclock.StateAcquiring, "not yet locked")
	f.media.Poll(ctx) // prime the cache with the unlocked status
	pollsBefore := f.media.pollCount()

	t0 := f.media.Now(ctx).Time.Add(2 * time.Second)
	out := f.m.StartAt(ctx, f.id, "inv-start", 2, t0.UnixNano())

	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt with a fresh unlocked cached status = %q (%s), want started (today's start-on-arrival behaviour)", out.Outcome, out.Reason)
	}
	if !containsString(out.Reason, pkgaudio.ReasonScheduledStartIgnored) {
		t.Fatalf("reason = %q, want it to carry %q", out.Reason, pkgaudio.ReasonScheduledStartIgnored)
	}
	if got := f.media.pollCount(); got != pollsBefore {
		t.Fatalf("resolveScheduleLocked polled the clock source %d extra time(s) despite a fresh cache, want 0", got-pollsBefore)
	}
}

// TestStartAtPositionPresentsTheExplicitPositionAtT0 proves a scheduled
// start's play head is exactly the caller's own position on the SAME
// engine call, never 0 (the bookmark-less default [Manager.StartAt]
// would use), the defect a Start-then-Seek pair cannot avoid, per
// [Manager.StartAtPosition]'s own doc comment.
func TestStartAtPositionPresentsTheExplicitPositionAtT0(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()

	const wantPosition = 12500 * time.Millisecond
	t0 := f.media.Now(ctx).Time.Add(30 * time.Millisecond)
	before := f.media.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- f.m.StartAtPosition(ctx, f.id, "inv-start", 2, t0.UnixNano(), wantPosition)
	}()
	f.media.waitForReads(t, before+1)
	f.media.advance(30 * time.Millisecond)

	var out pkgaudio.OutcomeResult
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StartAtPosition never returned after the media clock reached T0")
	}
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAtPosition = %q (%s), want started", out.Outcome, out.Reason)
	}

	snaps := f.m.Snapshot(ctx)
	var got *SessionSnapshot
	for i := range snaps {
		if snaps[i].ID == f.id {
			got = &snaps[i]
		}
	}
	if got == nil {
		t.Fatalf("no snapshot for session %q", f.id)
	}
	if !got.PositionKnown || got.Position != wantPosition {
		t.Fatalf("Position = %v (known=%v), want %v", got.Position, got.PositionKnown, wantPosition)
	}
}

// TestStartAtPositionOverridesAnyBookmark proves the explicit position
// is used even when the session holds a bookmark that would otherwise be
// STALE (a mismatched item identity): [Manager.start] never even reaches
// resolveBookmarkPositionLocked's own stale-bookmark refusal once an
// explicit position is given, since it is never consulted at all.
func TestStartAtPositionOverridesAnyBookmark(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()

	s, ok := f.m.get(f.id)
	if !ok {
		t.Fatal("session not found")
	}
	s.mu.Lock()
	s.bookmark = &pkgaudio.Bookmark{ItemID: "not-the-real-item", Position: 999 * time.Second}
	s.mu.Unlock()

	const wantPosition = 3 * time.Second
	t0 := f.media.Now(ctx).Time.Add(30 * time.Millisecond)
	before := f.media.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- f.m.StartAtPosition(ctx, f.id, "inv-start", 2, t0.UnixNano(), wantPosition)
	}()
	f.media.waitForReads(t, before+1)
	f.media.advance(30 * time.Millisecond)

	var out pkgaudio.OutcomeResult
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StartAtPosition never returned after the media clock reached T0")
	}
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAtPosition = %q (%s), want started (a stale bookmark must never refuse an explicit-position start)", out.Outcome, out.Reason)
	}
	snaps := f.m.Snapshot(ctx)
	var got *SessionSnapshot
	for i := range snaps {
		if snaps[i].ID == f.id {
			got = &snaps[i]
		}
	}
	if got == nil {
		t.Fatalf("no snapshot for session %q", f.id)
	}
	if !got.PositionKnown || got.Position != wantPosition {
		t.Fatalf("Position = %v (known=%v), want %v: the stale bookmark's position leaked through", got.Position, got.PositionKnown, wantPosition)
	}
}

// TestScheduledStartAppliesOutputLatency proves RES-019 section 8's
// adjustment: a bound calibrated output latency shifts the engine's own
// start instant earlier by exactly that amount, so the timeline's own
// ScheduledAtNs (what the engine actually targets) reflects the
// requested T0 minus the latency, not the requested T0 itself.
func TestScheduledStartAppliesOutputLatency(t *testing.T) {
	f := newScheduledFixture(t, 20)
	const latencyUs = 56_000
	f.m.SetOutputLatency(latencyUs)

	out := f.startScheduled(t, 2*time.Second)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("scheduled StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}
	snap := f.timeline(t)
	if !snap.Scheduled {
		t.Fatalf("timeline reports not scheduled: %+v", snap)
	}
	// startScheduled requested T0 = (media instant at call time) + lead,
	// and has already advanced the media clock by that same lead, so the
	// requested T0 equals the CURRENT media instant. The engine's own
	// ScheduledAtNs (RES-019 section 8's adjustment) must read latencyUs
	// microseconds earlier than that.
	nowNs := f.media.Now(context.Background()).Time.UnixNano()
	wantNs := nowNs - int64(latencyUs)*int64(time.Microsecond)
	if snap.ScheduledAtNs != wantNs {
		t.Fatalf("ScheduledAtNs = %d, want %d (requested T0 minus %d us of output latency)", snap.ScheduledAtNs, wantNs, latencyUs)
	}
}

// TestScheduledStartOutputLatencyCanPushT0IntoThePast proves the
// adjustment is applied BEFORE the past/lead checks: a start instant
// that is comfortably in the future on its own is refused once the
// bound output latency is subtracted and the ADJUSTED instant is no
// longer after the media clock.
func TestScheduledStartOutputLatencyCanPushT0IntoThePast(t *testing.T) {
	f := newScheduledFixture(t, 20)
	// 2 seconds of latency against a 1 second lead: the adjusted T0 is
	// 1 second BEHIND the media clock by the time it is evaluated.
	f.m.SetOutputLatency(2_000_000)
	ctx := context.Background()

	t0 := f.media.Now(ctx).Time.Add(1 * time.Second)
	out := f.m.StartAt(ctx, f.id, "inv-start", 2, t0.UnixNano())
	if out.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("StartAt outcome = %q (%s), want refused once output latency is subtracted", out.Outcome, out.Reason)
	}
	if !containsString(out.Reason, pkgaudio.ReasonScheduledStartInPast) {
		t.Fatalf("refusal reason = %q, want it to carry %q", out.Reason, pkgaudio.ReasonScheduledStartInPast)
	}
	if !containsString(out.Reason, "2000000us") {
		t.Fatalf("refusal reason = %q, want it to name the calibrated output latency that caused the refusal", out.Reason)
	}
}

// TestScheduledStartIgnoredWhenProviderIsNotLocked pins the other half:
// a node whose clock is not locked keeps today's start-on-arrival
// behaviour exactly, and says so rather than silently discarding the
// instant.
func TestScheduledStartIgnoredWhenProviderIsNotLocked(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.media.setState(agentclock.StateHoldover, "grandmaster unreachable")

	t0 := f.media.Now(ctx).Time.Add(2 * time.Second)
	out := f.m.StartAt(ctx, f.id, "inv-start", 2, t0.UnixNano())

	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt on an unlocked node = %q (%s), want %q started on arrival", out.Outcome, out.Reason, pkgaudio.OutcomeStarted)
	}
	if !containsString(out.Reason, "holdover") {
		t.Fatalf("outcome reason = %q, want it to name the clock state that caused the instant to be ignored", out.Reason)
	}
	if snap := f.timeline(t); snap.Scheduled {
		t.Fatalf("timeline reports scheduled on a node that ignored its start instant: %+v", snap)
	}
}

// TestStartWithNoInstantReportsNoTimeline is the unchanged-behaviour
// guard: a start carrying no instant is exactly what it was before this
// seam, and mints no timeline for the operator to misread.
func TestStartWithNoInstantReportsNoTimeline(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()

	if out := f.m.Start(ctx, f.id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}
	snap := f.timeline(t)
	if snap.Scheduled {
		t.Fatalf("an unscheduled start produced a timeline: %+v", snap)
	}
	if snap.Reason == "" {
		t.Fatal("a node with no scheduled session reported no reason; an absent reason reads as fine")
	}
}

// TestTimelineMeasuresAgainstTheMediaClockNotTheWallClock proves the
// three measured values are real, and that expected is measured from T0
// on the media clock.
func TestTimelineMeasuresAgainstTheMediaClockNotTheWallClock(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.startScheduled(t, 200*time.Millisecond)

	f.tick(ctx, 3*time.Second)

	snap := f.timeline(t)
	if !snap.Scheduled || !snap.Measured {
		t.Fatalf("timeline = %+v, want scheduled and measured", snap)
	}
	if snap.ExpectedMs != 3000 {
		t.Fatalf("expected_ms = %d, want 3000 (media_now minus T0)", snap.ExpectedMs)
	}
	if snap.ErrorMs != 0 {
		t.Fatalf("error_ms = %d, want 0: the sink and media clocks advanced together", snap.ErrorMs)
	}
	if snap.ScheduledAtNs == 0 {
		t.Fatal("scheduled_at reported zero")
	}
}

// TestLargeErrorWithNoCauseIsReportedAndNeverSeeked is the whole reason
// this seam has a cause gate. A sound card's own skew correction produces
// exactly this shape: an error far beyond the threshold with no PTP step,
// no provider restart and no device change behind it. It must be
// reported and must not move the show's position.
func TestLargeErrorWithNoCauseIsReportedAndNeverSeeked(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.startScheduled(t, 200*time.Millisecond)

	// Ten times the threshold, and the exact shape of the measured sink
	// correction: the output presents less than the media clock says it
	// should have.
	f.engine.SetPresentedAdjust(-200 * time.Millisecond)
	f.tick(ctx, 3*time.Second)

	snap := f.timeline(t)
	if snap.ErrorMs != 200 {
		t.Fatalf("error_ms = %d, want 200: the error must be REPORTED, not swallowed", snap.ErrorMs)
	}
	if got := f.engine.seekCount(); got != 0 {
		t.Fatalf("a %dms error with no PTP step, restart or device change caused %d seek(s); it must cause none at any magnitude", snap.ErrorMs, got)
	}
	if snap.Resyncs != 0 || snap.LastResyncReason != "" {
		t.Fatalf("resyncs = %d, last_resync_reason = %q, want 0 and empty", snap.Resyncs, snap.LastResyncReason)
	}
}

// TestPTPStepWithLargeErrorSeeksExactlyOnce is the other side: with a
// cause established from the tracker's own step evidence, the same error
// earns one flushing seek and is reported as a resync. f.media.Poll(ctx)
// after recordStep stands in for the agent's own clock report loop
// refreshing evaluateTimelineLocked's cached status.
func TestPTPStepWithLargeErrorSeeksExactlyOnce(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.startScheduled(t, 200*time.Millisecond)

	f.engine.SetPresentedAdjust(-200 * time.Millisecond)
	f.media.recordStep(f.clk.now().Add(time.Second))
	f.media.Poll(ctx)
	f.tick(ctx, 3*time.Second)

	if got := f.engine.seekCount(); got != 1 {
		t.Fatalf("seeks after one PTP step with a %v error = %d, want exactly 1", 200*time.Millisecond, got)
	}
	snap := f.timeline(t)
	if snap.Resyncs != 1 || snap.LastResyncReason != ResyncReasonPTPStep {
		t.Fatalf("resyncs = %d, reason = %q, want 1 and %q", snap.Resyncs, snap.LastResyncReason, ResyncReasonPTPStep)
	}
	if want := 3 * time.Second; f.engine.lastTarget() != want {
		t.Fatalf("seek target = %v, want %v (startPosition plus expected)", f.engine.lastTarget(), want)
	}

	// The same step must not fire again on the next tick: it has been
	// consumed into the timeline's own anchor.
	f.tick(ctx, time.Second)
	if got := f.engine.seekCount(); got != 1 {
		t.Fatalf("seeks after a second tick with no new step = %d, want still 1", got)
	}
}

// TestProviderRestartIsACause covers the second cause: the lock episode
// broken and remade, which is what a ptp4l owner stopping and restarting
// it looks like from this node. The pollCount assertion proves
// consumeCause detected it from the cache alone, with no live Poll.
func TestProviderRestartIsACause(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.startScheduled(t, 200*time.Millisecond)
	f.engine.SetPresentedAdjust(-200 * time.Millisecond)

	f.media.setState(agentclock.StateFailed, "ptp4l is not running")
	f.media.Poll(ctx)
	f.tick(ctx, time.Second)
	if got := f.engine.seekCount(); got != 0 {
		t.Fatalf("seeks while the clock was unlocked = %d, want 0: an unlocked clock is not a measurement to correct against", got)
	}

	f.media.setState(agentclock.StateLocked, "")
	f.media.Poll(ctx)
	pollsBefore := f.media.pollCount()
	f.tick(ctx, time.Second)

	if got := f.engine.seekCount(); got != 1 {
		t.Fatalf("seeks after the lock episode was remade = %d, want 1", got)
	}
	if snap := f.timeline(t); snap.LastResyncReason != ResyncReasonProviderRestart {
		t.Fatalf("last_resync_reason = %q, want %q", snap.LastResyncReason, ResyncReasonProviderRestart)
	}
	if got := f.media.pollCount(); got != pollsBefore {
		t.Fatalf("evaluateTimelineLocked polled the clock source %d extra time(s) despite a fresh cache, want 0: it must detect the provider restart from cache alone", got-pollsBefore)
	}
}

// TestErrorExactlyAtTheThresholdDoesNotSeek pins the comparison, which is
// not a hypothetical boundary: the audio sink's own skew correction is
// measured at exactly 20.000000 ms and the threshold this field is
// expected to carry is also 20, so exactly-on-the-threshold is the one
// value where the two coincide. The field is a drift IGNORE threshold, so
// an error exactly at it is on the ignore side.
func TestErrorExactlyAtTheThresholdDoesNotSeek(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.startScheduled(t, 200*time.Millisecond)

	f.engine.SetPresentedAdjust(-20 * time.Millisecond)
	f.media.recordStep(f.clk.now().Add(time.Second))
	f.media.Poll(ctx)
	f.tick(ctx, 3*time.Second)

	if got := f.engine.seekCount(); got != 0 {
		t.Fatalf("an error of exactly the threshold caused %d seek(s), want 0", got)
	}

	// One nanosecond past it, with a fresh cause, does seek: the
	// comparison runs at full precision, not at millisecond resolution.
	f.engine.SetPresentedAdjust(-20*time.Millisecond - time.Nanosecond)
	f.media.recordStep(f.clk.now().Add(2 * time.Second))
	f.media.Poll(ctx)
	f.tick(ctx, time.Second)
	if got := f.engine.seekCount(); got != 1 {
		t.Fatalf("an error one nanosecond past the threshold caused %d seek(s), want 1", got)
	}
}

// TestNoConfiguredThresholdNeverSeeks proves this package invents no
// threshold of its own: with no audio.settings push, a node reports its
// error and leaves the position alone even with a cause present.
func TestNoConfiguredThresholdNeverSeeks(t *testing.T) {
	f := newScheduledFixture(t, 0)
	ctx := context.Background()
	f.startScheduled(t, 200*time.Millisecond)

	f.engine.SetPresentedAdjust(-5 * time.Second)
	f.media.recordStep(f.clk.now().Add(time.Second))
	f.tick(ctx, 3*time.Second)

	if got := f.engine.seekCount(); got != 0 {
		t.Fatalf("seeks with no configured threshold = %d, want 0", got)
	}
	if snap := f.timeline(t); snap.ErrorMs != 5000 {
		t.Fatalf("error_ms = %d, want 5000 still reported", snap.ErrorMs)
	}
}

// TestUnreadableMediaClockReportsNoMeasurementRatherThanZero: a tick that
// could not read the clock must not publish a zero error, which reads as
// a session perfectly on time.
func TestUnreadableMediaClockReportsNoMeasurementRatherThanZero(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.startScheduled(t, 200*time.Millisecond)
	f.tick(ctx, time.Second)

	f.media.mu.Lock()
	f.media.valid, f.media.reason = false, "PHC read failed"
	f.media.mu.Unlock()
	f.tick(ctx, time.Second)

	snap := f.timeline(t)
	if !snap.Scheduled {
		t.Fatal("the session still holds a T0; it must still report as scheduled")
	}
	if snap.Measured {
		t.Fatalf("timeline reported a measurement with an unreadable media clock: %+v", snap)
	}
	if snap.ExpectedMs != 0 || snap.ActualMs != 0 || snap.ErrorMs != 0 {
		t.Fatalf("unmeasured timeline carried stale numbers: %+v", snap)
	}
	if !containsString(snap.Reason, "PHC read failed") {
		t.Fatalf("reason = %q, want it to carry the clock's own failure text", snap.Reason)
	}
}

// TestCommandedSeekEndsTheScheduledRun: an operator moved the position
// deliberately, so a timeline that kept measuring against the old T0
// would report that as error and try to seek it back.
func TestCommandedSeekEndsTheScheduledRun(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.startScheduled(t, 200*time.Millisecond)
	f.tick(ctx, time.Second)
	if !f.timeline(t).Scheduled {
		t.Fatal("timeline was not established by the scheduled start")
	}

	f.m.Seek(ctx, f.id, "inv-seek", 3, 30*time.Second)
	if snap := f.timeline(t); snap.Scheduled {
		t.Fatalf("timeline survived a commanded seek: %+v", snap)
	}
}

// TestWatcherClockPollDoesNotDelayAConcurrentPromote reproduces the rig
// defect this fix answers: a watcher tick forced into a live clock Poll
// must not hold the session lock a concurrent Promote needs. Plain
// Promote (no scheduledAtNs) keeps this test clear of
// resolveScheduleLocked's own cache, which is covered elsewhere.
func TestWatcherClockPollDoesNotDelayAConcurrentPromote(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.startScheduled(t, 50*time.Millisecond)

	const stagingID = pkgaudio.SessionID("staging-1")
	ref := writeTestAsset(t, f.m.assetDir, "a.wav", "asset-1", []byte("x"))
	if out := f.m.Apply(ctx, stagingID, "inv-stage-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); out.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("staging Apply refused: %s", out.Reason)
	}
	if out := f.m.Prepare(ctx, stagingID, "inv-stage-prepare", 2); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("staging Prepare = %q (%s), want position (ready)", out.Outcome, out.Reason)
	}

	// Age the cache past clockStatusFreshnessBound so the watcher tick
	// below is forced into evaluateTimelineLocked's live-Poll fallback.
	// Plain Promote never reads the clock, so this cannot affect it.
	f.clk.advance(clockStatusFreshnessBound + time.Second)

	entered := make(chan struct{})
	release := make(chan struct{})
	f.media.blockNextPoll(entered, release)

	s, ok := f.m.get(f.id)
	if !ok {
		t.Fatal("session not found")
	}

	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		s.mu.Lock()
		f.m.evaluateTimelineLocked(ctx, s)
		s.mu.Unlock()
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher tick never reached the clock source's Poll")
	}

	const lockBudget = 50 * time.Millisecond
	promoteBegin := time.Now()
	out := f.m.Promote(ctx, stagingID, f.id, "inv-promote", 5)
	elapsed := time.Since(promoteBegin)

	close(release)
	<-tickDone

	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Promote = %q (%s), want started", out.Outcome, out.Reason)
	}
	if elapsed > lockBudget {
		t.Fatalf("Promote took %v while the watcher's own clock poll was in flight, want at most %v", elapsed, lockBudget)
	}
}

// TestMediaNowReportsInvalidWithoutAClock: no wired clock is reported as
// an invalid reading with a reason, never a plausible instant.
func TestMediaNowReportsInvalidWithoutAClock(t *testing.T) {
	c := newClock(time.Unix(1_700_000_000, 0))
	m := newTestManager(t, c)
	got := m.MediaNow(context.Background())
	if got.Valid {
		t.Fatalf("MediaNow with no clock wired reported valid: %+v", got)
	}
	if got.Reason == "" {
		t.Fatal("an invalid media clock reading carried no reason")
	}
}

// TestPrerollLatencyIsMeasuredNotDefaulted: a session that has never
// prepared reports known=false rather than a zero a coordinator would
// schedule against.
func TestPrerollLatencyIsMeasuredNotDefaulted(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()

	if _, known := f.m.PrerollLatency(f.id); known {
		t.Fatal("a session that has never prepared reported a known preroll latency")
	}
	if out := f.m.Prepare(ctx, f.id, "inv-prepare", 2); out.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Prepare refused: %s", out.Reason)
	}
	if _, known := f.m.PrerollLatency(f.id); !known {
		t.Fatal("a prepared session reported no preroll latency")
	}
}

func containsString(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

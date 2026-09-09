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
	defer f.mu.Unlock()
	return f.status
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
// earns one flushing seek and is reported as a resync.
func TestPTPStepWithLargeErrorSeeksExactlyOnce(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.startScheduled(t, 200*time.Millisecond)

	f.engine.SetPresentedAdjust(-200 * time.Millisecond)
	f.media.recordStep(f.clk.now().Add(time.Second))
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
// it looks like from this node.
func TestProviderRestartIsACause(t *testing.T) {
	f := newScheduledFixture(t, 20)
	ctx := context.Background()
	f.startScheduled(t, 200*time.Millisecond)
	f.engine.SetPresentedAdjust(-200 * time.Millisecond)

	f.media.setState(agentclock.StateFailed, "ptp4l is not running")
	f.tick(ctx, time.Second)
	if got := f.engine.seekCount(); got != 0 {
		t.Fatalf("seeks while the clock was unlocked = %d, want 0: an unlocked clock is not a measurement to correct against", got)
	}

	f.media.setState(agentclock.StateLocked, "")
	f.tick(ctx, time.Second)

	if got := f.engine.seekCount(); got != 1 {
		t.Fatalf("seeks after the lock episode was remade = %d, want 1", got)
	}
	if snap := f.timeline(t); snap.LastResyncReason != ResyncReasonProviderRestart {
		t.Fatalf("last_resync_reason = %q, want %q", snap.LastResyncReason, ResyncReasonProviderRestart)
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
	f.tick(ctx, 3*time.Second)

	if got := f.engine.seekCount(); got != 0 {
		t.Fatalf("an error of exactly the threshold caused %d seek(s), want 0", got)
	}

	// One nanosecond past it, with a fresh cause, does seek: the
	// comparison runs at full precision, not at millisecond resolution.
	f.engine.SetPresentedAdjust(-20*time.Millisecond - time.Nanosecond)
	f.media.recordStep(f.clk.now().Add(2 * time.Second))
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

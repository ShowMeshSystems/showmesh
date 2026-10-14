package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	clock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// runningLockedClockSource is a locked media clock that moves with real time
// from origin, so a scheduled start a little ahead actually arrives.
type runningLockedClockSource struct {
	origin  time.Time
	started time.Time
}

func newRunningLockedClockSource(origin time.Time) runningLockedClockSource {
	return runningLockedClockSource{origin: origin, started: time.Now()}
}

func (r runningLockedClockSource) Poll(context.Context) clock.Status {
	return clock.Status{State: clock.StateLocked, Timescale: clock.TimescalePTP}
}

func (r runningLockedClockSource) Now(context.Context) clock.MediaTime {
	return clock.MediaTime{Time: r.origin.Add(time.Since(r.started)), Valid: true}
}

func (r runningLockedClockSource) Last() (clock.Status, time.Time, bool) {
	return clock.Status{}, time.Time{}, false
}

// lateStartRecorder stands in for the engine calls startCaughtUp makes.
type lateStartRecorder struct {
	mu          sync.Mutex
	scheduledAt int64
	scheduledTo time.Duration
	scheduled   pkgaudio.OutcomeResult
	starts      int
	seekTo      time.Duration
	seeks       int
}

func (r *lateStartRecorder) steps() lateStartSteps {
	return lateStartSteps{
		scheduled: func(atNs int64, position time.Duration) pkgaudio.OutcomeResult {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.scheduledAt, r.scheduledTo = atNs, position
			return r.scheduled
		},
		start: func() pkgaudio.OutcomeResult {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.starts++
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStarted}
		},
		seek: func(position time.Duration) pkgaudio.OutcomeResult {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.seeks++
			r.seekTo = position
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStarted}
		},
	}
}

// TestStartCaughtUpReschedulesAtTheReachedPosition proves a missed start is
// rescheduled just ahead and plays from where the on-time nodes will be then.
func TestStartCaughtUpReschedulesAtTheReachedPosition(t *testing.T) {
	mediaNow := time.Date(2026, 10, 14, 19, 0, 10, 0, time.UTC)
	mgr, _ := newTestAudioManager(t, t.TempDir(), &fakeClock{t: mediaNow})
	mgr.SetClockSource(newRunningLockedClockSource(mediaNow))
	t0 := mediaNow.Add(-1500 * time.Millisecond).UnixNano()

	rec := &lateStartRecorder{scheduled: pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStarted}}
	failedStep, _ := startCaughtUp(context.Background(), mgr, t0, 2*time.Second, rec.steps())
	if failedStep != "" {
		t.Fatalf("failedStep = %q, want none", failedStep)
	}
	if rec.starts != 0 || rec.seeks != 0 {
		t.Fatalf("starts=%d seeks=%d, want the rescheduled start alone", rec.starts, rec.seeks)
	}
	if ahead := time.Duration(rec.scheduledAt - mediaNow.UnixNano()); ahead < lateStartRescheduleLead || ahead > lateStartRescheduleLead+100*time.Millisecond {
		t.Fatalf("rescheduled %v ahead of the media clock, want about %v", ahead, lateStartRescheduleLead)
	}
	if want := 2*time.Second + time.Duration(rec.scheduledAt-t0); rec.scheduledTo != want {
		t.Fatalf("rescheduled position = %v, want %v (the cue position plus the time since T0)", rec.scheduledTo, want)
	}
}

// TestStartCaughtUpSeeksToTheReachedPositionWhenTheRescheduleIsMissed proves
// the start-then-seek fallback also seeks past the cue position by the lateness.
func TestStartCaughtUpSeeksToTheReachedPositionWhenTheRescheduleIsMissed(t *testing.T) {
	mediaNow := time.Date(2026, 10, 14, 19, 0, 10, 0, time.UTC)
	mgr, _ := newTestAudioManager(t, t.TempDir(), &fakeClock{t: mediaNow})
	mgr.SetClockSource(newRunningLockedClockSource(mediaNow))
	t0 := mediaNow.Add(-time.Second).UnixNano()

	rec := &lateStartRecorder{scheduled: pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: pkgaudio.ReasonScheduledStartInPast + ": too late"}}
	failedStep, _ := startCaughtUp(context.Background(), mgr, t0, 0, rec.steps())
	if failedStep != "" {
		t.Fatalf("failedStep = %q, want none", failedStep)
	}
	if rec.starts != 1 || rec.seeks != 1 {
		t.Fatalf("starts=%d seeks=%d, want one start and one seek", rec.starts, rec.seeks)
	}
	if rec.seekTo < time.Second || rec.seekTo > 1100*time.Millisecond {
		t.Fatalf("sought to %v, want about 1s (the time since T0), never the cue's own 0", rec.seekTo)
	}
}

// TestStartCaughtUpReportsAnOrdinaryRefusalWithoutFallingBack proves a refusal
// other than a missed instant is reported, not papered over with a start.
func TestStartCaughtUpReportsAnOrdinaryRefusalWithoutFallingBack(t *testing.T) {
	mediaNow := time.Date(2026, 10, 14, 19, 0, 10, 0, time.UTC)
	mgr, _ := newTestAudioManager(t, t.TempDir(), &fakeClock{t: mediaNow})
	mgr.SetClockSource(newRunningLockedClockSource(mediaNow))

	rec := &lateStartRecorder{scheduled: pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "not set up"}}
	failedStep, outcome := startCaughtUp(context.Background(), mgr, mediaNow.Add(-time.Second).UnixNano(), 0, rec.steps())
	if failedStep != lateStepStart || outcome.Reason != "not set up" {
		t.Fatalf("failedStep=%q outcome=%+v, want the start refusal reported", failedStep, outcome)
	}
	if rec.starts != 0 {
		t.Fatalf("starts = %d, want none", rec.starts)
	}
}

// TestStartCaughtUpSeeksWhenTheRescheduleStartsAtOnce proves a retry that
// started immediately, because the clock unlocked, is moved to the reached position.
func TestStartCaughtUpSeeksWhenTheRescheduleStartsAtOnce(t *testing.T) {
	mediaNow := time.Date(2026, 10, 14, 19, 0, 10, 0, time.UTC)
	mgr, _ := newTestAudioManager(t, t.TempDir(), &fakeClock{t: mediaNow})
	mgr.SetClockSource(newRunningLockedClockSource(mediaNow))

	rec := &lateStartRecorder{scheduled: pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStarted, Reason: pkgaudio.ReasonScheduledStartIgnored + ": unlocked"}}
	failedStep, _ := startCaughtUp(context.Background(), mgr, mediaNow.Add(-time.Second).UnixNano(), 0, rec.steps())
	if failedStep != "" || rec.starts != 0 || rec.seeks != 1 {
		t.Fatalf("failedStep=%q starts=%d seeks=%d, want one seek after the immediate start", failedStep, rec.starts, rec.seeks)
	}
	if rec.seekTo < time.Second || rec.seekTo > 1100*time.Millisecond {
		t.Fatalf("sought to %v, want about 1s, where the song is now", rec.seekTo)
	}
}

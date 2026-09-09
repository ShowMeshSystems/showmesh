//go:build cgo

package gstengine

import (
	"context"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// The transition guard beginTransition arms on every StartLTC as
// well as StopLTC, so after a seek or restart, observe() kept reporting
// the outgoing run's evidence for the full fixed guard window even once
// the feeder had already confirmed the incoming run. Anything comparing
// reported LTC against show position read a stale value for that whole
// window. This is loopback evidence against a real pipeline (fakesink,
// sync=true), nothing audible, no hardware.
//
// Draining the appsrc's own queue of pre-seek content before the incoming
// run's first buffer can even be pushed takes on the order of
// ltcAppSrcLeadDuration by itself, real physics no evidence-based guard
// can shortcut -- so the wall-clock gap from StartLTC's own call to the
// first confirmed sample is not what this proves. What this fix's
// acceptance actually asks is that observe() stops padding a further,
// artificial wait on top of that: once the feeder's own pad-probe evidence
// (emittedGeneration, the same evidence [ltcChannel.observe] itself
// trusts) confirms the incoming generation, ObserveLTC must reflect it
// within about one feeder period, not wait out ltcTransitionGuardDuration
// regardless. This is a white-box package test (not _test, an external
// package) specifically so it can read that same internal evidence
// directly as ground truth, rather than re-deriving it from decoded audio.

// ltcGuardPollInterval is how often the polling loops below sample
// internal state and ObserveLTC: tight enough that the measured gap is
// dominated by real confirmation and reporting timing, not polling
// granularity.
const ltcGuardPollInterval = 2 * time.Millisecond

// ltcGuardMaxFollowLatency bounds how long ObserveLTC may lag behind the
// feeder's own confirmation of the incoming generation (ch.emittedGeneration
// reaching it): generous over one feeder period (ltcSilenceChunk, 20ms)
// for real scheduling jitter in a loopback pipeline, but far under
// ltcTransitionGuardDuration -- a regression back to the fixed-duration
// guard would fail this by hundreds of milliseconds, not a marginal one.
const ltcGuardMaxFollowLatency = 100 * time.Millisecond

func TestLTCObserveFollowsSeekPromptlyAfterConfirmation(t *testing.T) {
	e := newLTCTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()

	const rate = pkgaudio.LTCFrameRate25
	first := agentaudio.LTCSpec{FrameRate: rate, StartTimecode: "01:00:00:00"}
	if _, err := e.StartLTC(ctx, first); err != nil {
		t.Fatalf("StartLTC (first run): %v", err)
	}
	waitForLTCState(t, e, agentaudio.LTCRunning, ltcOpTimeout)
	// Let the first run emit real, confirmed content before the seek swaps
	// its generation out, the same way the queue-lag suite does.
	time.Sleep(1 * time.Second)

	const seekTC = pkgaudio.LTCTimecode("05:00:00:00")
	if _, err := e.StartLTC(ctx, agentaudio.LTCSpec{FrameRate: rate, StartTimecode: seekTC}); err != nil {
		t.Fatalf("StartLTC (seek realignment): %v", err)
	}
	e.ltc.mu.Lock()
	seekGeneration := e.ltc.generation
	e.ltc.mu.Unlock()

	deadline := time.Now().Add(ltcOpTimeout)
	var confirmedAt, reportedAt time.Time
	for time.Now().Before(deadline) {
		if confirmedAt.IsZero() && e.ltc.emittedGeneration.Load() == seekGeneration {
			confirmedAt = time.Now()
		}
		if reportedAt.IsZero() {
			obs := e.ObserveLTC(context.Background())
			if obs.State == agentaudio.LTCRunning && obs.TimecodeKnown && obs.Timecode >= seekTC {
				reportedAt = time.Now()
			}
		}
		if !confirmedAt.IsZero() && !reportedAt.IsZero() {
			break
		}
		time.Sleep(ltcGuardPollInterval)
	}
	if confirmedAt.IsZero() {
		t.Fatalf("the feeder never confirmed generation %d (post-seek run) within %s", seekGeneration, ltcOpTimeout)
	}
	if reportedAt.IsZero() {
		t.Fatalf("ObserveLTC never reported the post-seek run (want Timecode >= %q) within %s", seekTC, ltcOpTimeout)
	}

	latency := reportedAt.Sub(confirmedAt)
	t.Logf("feeder confirmed generation %d at %s; ObserveLTC first reported the post-seek run at %s, %s later",
		seekGeneration, confirmedAt.Format(time.RFC3339Nano), reportedAt.Format(time.RFC3339Nano), latency)
	if latency < 0 {
		// ObserveLTC reported the new run before this poll loop observed
		// emittedGeneration catch up -- both are read from the same
		// atomic without a lock, so a reordering here is a race in the
		// test's own observation, not evidence the guard is wrong.
		t.Logf("ObserveLTC's own report preceded this test's poll of emittedGeneration by %s; that is poll-order noise, not a defect", -latency)
		return
	}
	if latency > ltcGuardMaxFollowLatency {
		t.Fatalf("ObserveLTC took %s to surface the post-seek run after the feeder confirmed it, want within %s (the guard must end on evidence, not wait out a fixed duration)",
			latency, ltcGuardMaxFollowLatency)
	}
}

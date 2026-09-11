//go:build cgo

package gstengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This suite drives a real GStreamer pipeline through go-gst with
// "fakesink": no physical audio device, no claim about ALSA or real
// hardware output, and no claim about what a real speaker or a real LTC
// receiver would measure. What it proves is that this package's own
// Alignment implementation reads the program branch and the LTC channel
// against the same shared pipeline running time and reports the signed
// offset correctly, both near zero and after an injected discontinuity.

// alignmentToleranceMs bounds every measured-offset assertion in this
// suite: [ltcLagToleranceFrames] converted to milliseconds at rate,
// matching the neighbouring LTC-lag suite's own tolerance budget.
func alignmentToleranceMs(rate pkgaudio.LTCFrameRate) int64 {
	return int64(float64(ltcLagToleranceFrames) / rate.Rate() * 1000)
}

// waitForAlignmentKnown retries e.Alignment(handle) until it reports
// known, or fails the test once timeout elapses. A fresh Seek can leave a
// swapped-in replacement branch briefly short of its presented position
// (the underrun guard), which clears itself once decode catches up.
func waitForAlignmentKnown(t *testing.T, e *Engine, handle agentaudio.EngineHandle, timeout time.Duration) agentaudio.AlignmentSample {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastReason string
	for time.Now().Before(deadline) {
		sample, known, reason := e.Alignment(context.Background(), handle)
		if known {
			return sample
		}
		lastReason = reason
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Alignment never reported known within %s (last reason: %s)", timeout, lastReason)
	return agentaudio.AlignmentSample{}
}

// TestAlignmentSampleNearZeroThenNegativeAfterASeek covers both directions
// this design requires from one real pipeline: shortly after a start,
// program and LTC are within tolerance of aligned; a program-only Seek
// that moves position forward without realigning LTC (a Manager always
// pairs a real Seek with a StartLTC realignment, which this test
// deliberately withholds) must then read LTC as behind the program by
// roughly the seek's own size, negative.
func TestAlignmentSampleNearZeroThenNegativeAfterASeek(t *testing.T) {
	e := newLTCTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()

	dir := t.TempDir()
	wav := filepath.Join(dir, "alignment.wav")
	generateWAV(t, wav, 10)

	const handle = agentaudio.EngineHandle("prog")
	if _, err := e.Load(ctx, handle, mediaRef(wav), 10*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}

	const rate = pkgaudio.LTCFrameRate25
	if _, err := e.StartLTC(ctx, agentaudio.LTCSpec{FrameRate: rate, StartTimecode: "00:00:00:00"}); err != nil {
		t.Fatalf("StartLTC: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForLTCState(t, e, agentaudio.LTCRunning, ltcOpTimeout)
	time.Sleep(1 * time.Second)

	first := waitForAlignmentKnown(t, e, handle, ltcOpTimeout)
	tolerance := alignmentToleranceMs(rate)
	firstOffsetMs, err := first.LTCTimecode.DiffMs(mustAdvance(t, "00:00:00:00", first.ProgramPosition, rate), rate)
	if err != nil {
		t.Fatalf("DiffMs: %v", err)
	}
	// tolerance absorbs the real gap between StartLTC taking effect on the
	// wire and the program branch joining the shared mixer.
	if firstOffsetMs > tolerance || firstOffsetMs < -tolerance {
		t.Fatalf("offset shortly after start = %dms, want within %dms of zero", firstOffsetMs, tolerance)
	}

	// Seek relative to first's own ProgramPosition, not a fresh
	// e.Observe() query: Observe's live position query resolves upstream
	// toward the decoder and over-reports by however far decode is
	// running ahead of what the mixer is actually presenting (see
	// branch.queryPosition's own doc comment), and that decode-ahead
	// bias would land in the seek target, not in the offset this test is
	// trying to isolate.
	const seekAdvance = 2 * time.Second
	rtBeforeSeek := e.pipeline.GetCurrentRunningTime()
	if _, err := e.Seek(ctx, handle, first.ProgramPosition+seekAdvance); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	rtAfterSeek := e.pipeline.GetCurrentRunningTime()

	second := waitForAlignmentKnown(t, e, handle, ltcOpTimeout)
	if !second.SampledAt.After(first.SampledAt) {
		t.Fatalf("second sample's SampledAt %v did not advance past the first %v", second.SampledAt, first.SampledAt)
	}
	secondOffsetMs, err := second.LTCTimecode.DiffMs(mustAdvance(t, "00:00:00:00", second.ProgramPosition, rate), rate)
	if err != nil {
		t.Fatalf("DiffMs: %v", err)
	}
	// The replacement branch joins anchored at the running time the Seek
	// call itself consumed, so the true lag is the seek's own advance
	// minus that consumed running time, not the seek's advance alone.
	swapElapsed := time.Duration(rtAfterSeek - rtBeforeSeek)
	want := -(seekAdvance - swapElapsed).Milliseconds()
	if secondOffsetMs > want+tolerance || secondOffsetMs < want-tolerance {
		t.Fatalf("offset after a %s program-only seek = %dms, want within %dms of %dms (LTC behind program, negative)", seekAdvance, secondOffsetMs, tolerance, want)
	}
	if secondOffsetMs >= 0 {
		t.Fatalf("offset after a program-only seek = %dms, want negative (LTC behind program)", secondOffsetMs)
	}
}

// mustAdvance is [pkgaudio.LTCTimecode.Advance], failing the test on
// error rather than threading one more return value through this suite's
// own arithmetic, the same shape ltcValueAtOffset's neighbours use.
func mustAdvance(t *testing.T, base pkgaudio.LTCTimecode, d time.Duration, rate pkgaudio.LTCFrameRate) pkgaudio.LTCTimecode {
	t.Helper()
	tc, err := base.Advance(d, rate)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	return tc
}

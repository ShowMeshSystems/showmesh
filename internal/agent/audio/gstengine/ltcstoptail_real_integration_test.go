//go:build cgo

package gstengine

import (
	"context"
	"sync"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This suite proves the stop-observation fix directly against real decoded
// audio, the same way ltclag_real_integration_test.go proves the queue-lag
// fix: an appsink captures the actual LTC channel, and the question is
// whether ObserveLTC's reported state ever claims LTCStopped with an
// unknown timecode while that captured channel is still carrying real,
// non-silent LTC content. Neither Start nor Stop flushes the appsrc (see
// ltcAppSrcLeadDuration's doc comment on why not), so up to that much of
// the outgoing run's audio is still on the wire after either call returns.
// This is the realignment tail, measured here in wall-clock time rather
// than through capture.framesCaptured(): that counter tracks real time
// only when a continuously flowing program branch paces the pipeline, and
// this suite deliberately runs LTC alone, so each pulled buffer's own
// arrival time is recorded directly instead.

// ltcTailSilenceThreshold bounds how far from zero a captured S16LE
// sample may sit and still count as silence: pushLTCSilence writes exact
// zero bytes at the internal F32LE mix stage, but audioconvert's F32 to
// S16 dithering on the path to this test's sink turns exact zero into a
// few LSBs of dither noise, so an exact-zero check never matches. Real
// LTC signal swings the full S16 range, so this threshold cannot mistake
// real content for silence.
const ltcTailSilenceThreshold = 200

// ltcTailChunk is one buffer pulled off the two-channel appsink during
// captureLTCTail: when it arrived (wall clock, loopback evidence only,
// nothing audible) and whether its LTC-channel content was silence
// (within ltcTailSilenceThreshold) throughout.
type ltcTailChunk struct {
	at     time.Time
	silent bool
}

// captureLTCTail pulls interleaved S16LE samples off the ltclag sink
// format (see newLTCLagSink) until stop closes, recording each buffer's
// arrival wall time and whether its LTC channel (index 1 of the
// 2-channel frame) was silence throughout, within
// ltcTailSilenceThreshold.
func captureLTCTail(capture *ltcLagCapture, stop <-chan struct{}) (snapshot func() []ltcTailChunk) {
	var mu sync.Mutex
	var chunks []ltcTailChunk
	go func() {
		lastLen := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, ch2 := capture.snapshot()
			if len(ch2) > lastLen {
				at := time.Now()
				silent := true
				for _, v := range ch2[lastLen:] {
					if v > ltcTailSilenceThreshold || v < -ltcTailSilenceThreshold {
						silent = false
						break
					}
				}
				mu.Lock()
				chunks = append(chunks, ltcTailChunk{at: at, silent: silent})
				mu.Unlock()
				lastLen = len(ch2)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return func() []ltcTailChunk {
		mu.Lock()
		defer mu.Unlock()
		out := make([]ltcTailChunk, len(chunks))
		copy(out, chunks)
		return out
	}
}

// TestLTCStopDoesNotClaimStoppedWhileAudible is the core reproduction and
// regression guard for the stop observation defect: it samples ObserveLTC
// repeatedly through the tail window after a real Stop, then afterward
// locates -- from the captured LTC channel itself -- the wall-clock
// instant real, non-silent LTC content actually stopped reaching the
// wire. Before the fix, the very first sample (StopLTC's own return
// value) already claims LTCStopped with no known timecode despite audio
// still playing; after it, every sample taken before the wire actually
// goes quiet reports evidence consistent with that, never a confident
// false stop. This is loopback and appsink evidence only, nothing
// audible, no hardware.
//
// The same samples also cover the guard's upper edge -- every
// sample taken after the captured channel actually went silent must not
// still claim LTCRunning. ltcTransitionGuardDuration used to be a fixed
// 2x lead (400ms) against a measured real tail of about 290ms, so for
// roughly 110ms after the wire actually went quiet, observe() kept
// reporting the outgoing run as running with an extrapolated timecode;
// this loop is what catches that.
func TestLTCStopDoesNotClaimStoppedWhileAudible(t *testing.T) {
	e, capture := newLTCLagEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()

	const rate = pkgaudio.LTCFrameRate25
	if _, err := e.StartLTC(ctx, agentaudio.LTCSpec{FrameRate: rate, StartTimecode: "01:00:00:00"}); err != nil {
		t.Fatalf("StartLTC: %v", err)
	}
	waitForLTCState(t, e, agentaudio.LTCRunning, ltcOpTimeout)
	// Let real, decodable LTC content accumulate before stopping, not
	// just the appsrc's initial silence-filled queue.
	time.Sleep(1 * time.Second)

	stopWatch := make(chan struct{})
	snapshotChunks := captureLTCTail(capture, stopWatch)

	stopAt := time.Now()
	stopObs, err := e.StopLTC(ctx)
	if err != nil {
		t.Fatalf("StopLTC: %v", err)
	}

	type sampledObs struct {
		at  time.Time
		obs agentaudio.LTCObservation
	}
	samples := []sampledObs{{stopAt, stopObs}}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		samples = append(samples, sampledObs{time.Now(), e.ObserveLTC(context.Background())})
		time.Sleep(10 * time.Millisecond)
	}
	// Give the tail time to fully drain before deciding where the wire
	// actually went quiet: generous margin over ltcAppSrcLeadDuration since
	// this is measuring, not assuming, the real tail.
	time.Sleep(2 * time.Second)
	close(stopWatch)
	chunks := snapshotChunks()

	// silenceOnset is the arrival time of the first chunk, after stopAt,
	// that is exact digital silence and is immediately followed by
	// another exact-silence chunk -- a two-chunk confirmation so one
	// coincidental all-zero encoder chunk at a frame boundary cannot be
	// mistaken for the real onset.
	var silenceOnset time.Time
	for i, c := range chunks {
		if !c.at.After(stopAt) {
			continue
		}
		if c.silent && i+1 < len(chunks) && chunks[i+1].silent {
			silenceOnset = c.at
			break
		}
	}
	if silenceOnset.IsZero() {
		t.Fatalf("captured LTC channel never reached a confirmed silent stretch after Stop within the capture window (%d chunks captured)", len(chunks))
	}
	tail := silenceOnset.Sub(stopAt)
	t.Logf("measured stop realignment latency (appsink, loopback, wall clock): real LTC audio kept playing for %s after StopLTC returned; want on the order of ltcAppSrcLeadSeconds (%.4fs)",
		tail, ltcAppSrcLeadSeconds)

	falseStops := 0
	falseRunning := 0
	for _, s := range samples {
		if s.at.Before(silenceOnset) {
			if s.obs.State == agentaudio.LTCStopped && !s.obs.TimecodeKnown {
				falseStops++
				t.Errorf("ObserveLTC at %s (wire stays audible until %s, %s later) claims LTCStopped with no known timecode: %+v",
					s.at.Format(time.RFC3339Nano), silenceOnset.Format(time.RFC3339Nano), silenceOnset.Sub(s.at), s.obs)
			}
			continue
		}
		// The wire is genuinely silent by the time this sample was
		// taken, so a guard sized past the real tail must not still be
		// reporting the outgoing run as running.
		if s.obs.State == agentaudio.LTCRunning {
			falseRunning++
			t.Errorf("ObserveLTC at %s (wire went silent at %s, %s earlier) still claims LTCRunning: %+v",
				s.at.Format(time.RFC3339Nano), silenceOnset.Format(time.RFC3339Nano), s.at.Sub(silenceOnset), s.obs)
		}
	}
	t.Logf("%d of %d samples taken while the wire was still audible falsely claimed LTCStopped", falseStops, len(samples))
	t.Logf("%d of %d samples taken after the wire went silent falsely claimed LTCRunning", falseRunning, len(samples))
}

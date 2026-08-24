//go:build cgo

package gstengine

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// channelPeaks accumulates, per interleaved output channel, the largest
// absolute S32LE sample magnitude a buffer probe has observed at the
// sink pad -- the routing evidence a channel-transposition test asserts
// on. Guarded by mu because the probe runs on GStreamer's own streaming
// thread, never the test goroutine.
type channelPeaks struct {
	mu    sync.Mutex
	peaks []int64
}

func newChannelPeaks(channelCount int) *channelPeaks {
	return &channelPeaks{peaks: make([]int64, channelCount)}
}

// observe decodes one S32LE-interleaved buffer of channelCount channels
// and folds each channel's per-frame absolute sample value into the
// running per-channel peak.
func (p *channelPeaks) observe(data []byte, channelCount int) {
	const bytesPerSample = 4
	frameBytes := bytesPerSample * channelCount
	p.mu.Lock()
	defer p.mu.Unlock()
	for off := 0; off+frameBytes <= len(data); off += frameBytes {
		for ch := 0; ch < channelCount; ch++ {
			raw := binary.LittleEndian.Uint32(data[off+ch*bytesPerSample:])
			sample := int64(int32(raw))
			if sample < 0 {
				sample = -sample
			}
			if sample > p.peaks[ch] {
				p.peaks[ch] = sample
			}
		}
	}
}

func (p *channelPeaks) snapshot() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int64, len(p.peaks))
	copy(out, p.peaks)
	return out
}

// newPeakProbingSink is [newCapsRestrictedSink] plus a buffer probe that
// decodes and folds every arriving buffer through observe, rather than
// merely counting buffers -- routing evidence, not just flow evidence.
func newPeakProbingSink(t *testing.T, caps string, channelCount int) (capsRestrictedSink, *channelPeaks) {
	t.Helper()
	sink := newCapsRestrictedSink(t, caps)
	peaks := newChannelPeaks(channelCount)
	inner := sink.element.(gst.Bin).GetByName("innersink")
	inner.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			return gst.PadProbeOK
		}
		mapped, ok := buf.Map(gst.MapRead)
		if !ok {
			return gst.PadProbeOK
		}
		peaks.observe(mapped.Data(), channelCount)
		mapped.Unmap()
		return gst.PadProbeOK
	})
	return sink, peaks
}

// routingSignificant is the S32LE magnitude threshold separating "this
// output channel is carrying a real signal" from "this output channel
// is silence that survived format conversion and resampling with some
// residual noise floor" -- LTC's generated waveform peaks near full
// scale, orders of magnitude above any such residual.
const routingSignificant = int64(1) << 20

// TestChannelRoutingMatchesConfiguration is this branch's single most
// important test: it proves each logical channel's audio reaches the
// physical output index it was configured for, not merely that the
// pipeline reaches PLAYING and keeps streaming. Positioning is what
// makes this checkable at all here -- once interleave takes channel
// positions from each pad's own caps (channel-positions-from-input),
// its output sample order follows the assigned positions' ascending bit
// order, not pad-request order, so a mislabeled position silently
// transposes physical outputs even though every other observable
// (negotiation, buffer flow) still looks healthy. See the sink-side mask
// pin in [linkInterleaveToSink] for the complementary case, a mismatched
// layout that must fail outright rather than transpose or remix.
//
// LTCChannel is deliberately channel 1, not the LTC seam's usual highest
// index, so a channel-1/channel-2 transposition -- the classic L/R swap,
// and not caught by a reversal alone -- moves LTC's signal to a
// different, individually asserted physical index rather than landing on
// another already-silent channel where the swap would be invisible.
// ProgramChannels {2,3} stay idle (no session loaded, so both mixers
// carry only their permanently silent keep-alive pad); channel 4 is
// silence. channelPositionBits(4) assigns, in ascending 1-based channel
// order, FL, FR, RL, RR (0x1, 0x2, 0x10, 0x20), whose ascending-bit-index
// order is already channels 1,2,3,4 in that same order -- so a correct
// build places LTC's signal at interleaved output index 0 (0-based), and
// everywhere else stays at the noise floor.
func TestChannelRoutingMatchesConfiguration(t *testing.T) {
	sink, peaks := newPeakProbingSink(t, "audio/x-raw,format=S32LE,rate=48000,channels=4,channel-mask=(bitmask)0x33", 4)
	useSinkElement(t, sink.element)

	cfg := Config{
		SinkFactory:     "fakesink",
		ProgramChannels: []int{2, 3},
		LTCChannel:      1,
		ChannelCount:    4,
		SampleRate:      48000,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()
	if _, err := e.StartLTC(ctx, agentaudio.LTCSpec{FrameRate: pkgaudio.LTCFrameRate25, StartTimecode: "00:00:00:00"}); err != nil {
		t.Fatalf("StartLTC: %v", err)
	}

	requireSustainedPlayback(t, e, sink.buffers)
	time.Sleep(500 * time.Millisecond) // let real LTC-encoded audio, not just silence, reach the sink

	got := peaks.snapshot()
	if len(got) != 4 {
		t.Fatalf("observed %d channel peaks, want 4", len(got))
	}
	for ch, peak := range got {
		wantSignal := ch == 0 // 0-based index 0 == 1-based channel 1, the LTC channel
		if wantSignal && peak < routingSignificant {
			t.Errorf("output channel %d (LTC): peak %d, want >= %d (a real LTC signal)", ch+1, peak, routingSignificant)
		}
		if !wantSignal && peak >= routingSignificant {
			t.Errorf("output channel %d: peak %d, want < %d (should be silent, not carrying LTC or another channel's signal)", ch+1, peak, routingSignificant)
		}
	}
	if t.Failed() {
		t.Logf("observed per-channel peaks (1-based output channel -> peak): %v", got)
	}
}

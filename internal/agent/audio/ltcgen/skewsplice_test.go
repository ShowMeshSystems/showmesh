//go:build cgo

package ltcgen

import (
	"fmt"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/agent/audio/ltcgen/ltcdecodetest"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// What happens to generated LTC when the audio sink corrects clock drift
// by adding or removing samples underneath it.
//
// This is not hypothetical. Measured on a real node: the pipeline runs on
// the system clock rather than the sound card's, the card drifts about
// 15.7 ppm against it, and GstAudioBaseSink's default skew slaving
// corrects that by about 882 samples roughly every 22 minutes. The
// program and LTC channels share one interface and one clock domain, so
// whatever the sink does to hold the card to the system clock, it does to
// the timecode.

const (
	// skewSamples is the correction size observed on real hardware:
	// driftsamples 882 to 886 across four events, 18.4 ms at 48 kHz.
	skewSamples = 882

	skewTestFPS      = 30
	skewSamplesPerTC = testSampleRate / skewTestFPS
	skewTestFrames   = 40
)

// spliceOut removes n samples starting at cut, which is what a sink
// skipping ahead does to the stream a decoder sees.
func spliceOut(s []int16, cut, n int) []int16 {
	out := make([]int16, 0, len(s))
	out = append(out, s[:cut]...)
	return append(out, s[cut+n:]...)
}

// spliceRepeat replays n samples from cut, which is what a sink holding
// back does.
func spliceRepeat(s []int16, cut, n int) []int16 {
	out := make([]int16, 0, len(s)+n)
	out = append(out, s[:cut+n]...)
	return append(out, s[cut:]...)
}

// TestASampleSpliceCanMakeLTCDecodeATimecodeThatWasNeverSent
// characterises the LTC format, not this project's code, and that is why
// it asserts that the corruption happens rather than that it does not.
//
// LTC carries a sync word and no checksum. A decoder that finds the sync
// word at a plausible offset after samples are added or removed emits
// whatever bits precede it, with no way to know they are wrong. That is
// true of every LTC signal in the world, it is not a defect anyone can
// fix here, and it will still be true after this project's own clock
// defect is gone. Asserting it is characterising an external system.
//
// Measured, deterministic, four of thirty-two splice positions in a
// signal running 01:00:00:00 to 01:00:01:08:
//
//	offset  500, samples repeated -> 01:00:10:08
//	offset  600, samples repeated -> 01:00:70:08   not a valid timecode
//	offset 1400, samples dropped  -> 01:03:00:00
//	offset 1500, samples dropped  -> 01:15:00:00   fifteen minutes into
//	                                               a 1.3 second signal
//
// WHY THIS MATTERS HERE. The agent's pipeline selects the system clock
// rather than the audio sink's, so the sound card drifts against it and
// the sink corrects by splicing samples: measured at 15.7 ppm on one real
// card, corrected in about 882-sample steps roughly every 22 minutes.
// Program and LTC share one interface, so those splices land in generated
// timecode. Fixing the clock selection removes the splices in production.
//
// THIS TEST CANNOT OBSERVE THAT FIX. It splices the samples itself, in
// the test body, and never touches a pipeline or a clock. It will pass
// before and after, unchanged. The acceptance test for the clock work
// lives with that work: read the selected clock from the engine's own
// pipeline, and count skew corrections over a run longer than the
// correction interval. This test exists to say what those corrections
// cost, which is the reason the clock has to be right.
//
// The sweep is load bearing. Severity depends on where in a frame the
// splice lands, and splicing exactly on a frame boundary produces no
// wrong value at all, so pinning one offset chooses the answer instead of
// measuring it. See the boundary case below.
func TestASampleSpliceCanMakeLTCDecodeATimecodeThatWasNeverSent(t *testing.T) {
	clean, expected := encodeRun(t, pkgaudio.LTCFrameRate30, "01:00:00:00", skewTestFrames)

	sent := make(map[pkgaudio.LTCTimecode]bool, len(expected))
	for _, tc := range expected {
		sent[tc] = true
	}

	var wrong []string
	for _, splice := range []struct {
		name string
		fn   func([]int16, int, int) []int16
	}{
		{"samples dropped", spliceOut},
		{"samples repeated", spliceRepeat},
	} {
		// Sixteen offsets across one frame. A real correction is equally
		// likely anywhere in that window.
		step := skewSamplesPerTC / 16
		for off := 0; off < skewSamplesPerTC; off += step {
			cut := skewSamplesPerTC*20 + off
			frames, err := ltcdecodetest.Decode(splice.fn(clean, cut, skewSamples), skewSamplesPerTC)
			if err != nil {
				t.Fatalf("%s at offset %d: decode: %v", splice.name, off, err)
			}
			for _, f := range frames {
				if got := decodedString(f); !sent[got] {
					wrong = append(wrong, fmt.Sprintf("%s at offset %d decoded %s", splice.name, off, got))
				}
			}
		}
	}

	if len(wrong) == 0 {
		// Not a pass. Either the decoder gained a validity check, or the
		// signal or splice sizes here stopped reproducing the condition.
		// Both are real news about what a clock correction costs, and the
		// figures in this test's doc comment would need re-measuring.
		t.Fatalf("no splice position produced a wrong timecode; this test exists because four of " +
			"thirty-two did. Either libltc now rejects corrupted frames, or this signal no longer " +
			"reproduces the condition. Re-measure before trusting the cost figures above")
	}
	t.Logf("splices producing a timecode that was never sent: %d of 32 positions", len(wrong))
	for _, w := range wrong {
		t.Logf("  %s", w)
	}
}

func TestSkewCorrectionOnAFrameBoundaryIsTheBenignCase(t *testing.T) {
	clean, expected := encodeRun(t, pkgaudio.LTCFrameRate30, "01:00:00:00", skewTestFrames)
	sent := make(map[pkgaudio.LTCTimecode]bool, len(expected))
	for _, tc := range expected {
		sent[tc] = true
	}

	frames, err := ltcdecodetest.Decode(spliceOut(clean, skewSamplesPerTC*20, skewSamples), skewSamplesPerTC)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, f := range frames {
		if got := decodedString(f); !sent[got] {
			t.Fatalf("a frame-boundary splice produced %s, which was never sent; "+
				"this case is supposed to be the benign one", got)
		}
	}
}

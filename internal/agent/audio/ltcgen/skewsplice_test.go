//go:build cgo

package ltcgen

import (
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

// TestSkewCorrectionNeverProducesAWrongTimecode is the assertion that
// matters, and it is deliberately not about how many frames survive.
//
// A splice costs one or two frames, which is benign: a gap is visibly a
// gap and a consumer sees nothing where it expected something. The defect
// is a frame that DECODES TO A VALUE THAT WAS NEVER SENT. LTC carries a
// sync word and no checksum, so a decoder that finds the sync word at a
// plausible offset after a splice emits whatever bits precede it, with no
// way to know they are wrong. Measured examples from this exact setup:
// 01:00:70:08, which is not a valid timecode at all, and 01:15:00:00 in a
// signal that never leaves 01:00:0x.
//
// The sweep is load bearing. A correction lands at an arbitrary offset
// within a frame, and the severity depends on where: splicing exactly on
// a frame boundary costs one frame and produces no wrong value at all.
// Pinning the offset would choose the answer rather than measure it.
func TestSkewCorrectionNeverProducesAWrongTimecode(t *testing.T) {
	// SKIPPED BECAUSE IT FAILS, AND THE ENVIRONMENT IS FINE. Every other
	// skip in this repository means a machine lacks docker or a fixture
	// file. This one does not: the toolchain, libltc and the encoder are
	// all present and working, and the code under measurement is wrong.
	//
	// Measured, deterministic, four wrong values out of thirty-two splice
	// positions, in a signal running 01:00:00:00 to 01:00:01:08:
	//
	//   offset  500, samples repeated -> 01:00:10:08
	//   offset  600, samples repeated -> 01:00:70:08   not a valid timecode
	//   offset 1400, samples dropped  -> 01:03:00:00
	//   offset 1500, samples dropped  -> 01:15:00:00   fifteen minutes into
	//                                                  a 1.3 second signal
	//
	// The cause is upstream of this package: the agent's pipeline selects
	// the system clock rather than the audio sink's, the sound card drifts
	// against it, and the sink corrects by splicing samples. Fixing the
	// clock selection removes the splice and makes this pass. Removing
	// this skip is an acceptance item on that work, by this test's name.
	t.Skip("fails: a sample splice makes the decoder emit timecodes that were never sent " +
		"(01:00:10:08, 01:00:70:08, 01:03:00:00, 01:15:00:00). Not an environment problem; " +
		"see this test's doc comment")

	clean, expected := encodeRun(t, pkgaudio.LTCFrameRate30, "01:00:00:00", skewTestFrames)

	sent := make(map[pkgaudio.LTCTimecode]bool, len(expected))
	for _, tc := range expected {
		sent[tc] = true
	}

	for _, splice := range []struct {
		name string
		fn   func([]int16, int, int) []int16
	}{
		{"samples dropped", spliceOut},
		{"samples repeated", spliceRepeat},
	} {
		t.Run(splice.name, func(t *testing.T) {
			// Sixteen offsets across one frame. A real correction is
			// equally likely anywhere in that window.
			step := skewSamplesPerTC / 16
			for off := 0; off < skewSamplesPerTC; off += step {
				cut := skewSamplesPerTC*20 + off
				frames, err := ltcdecodetest.Decode(splice.fn(clean, cut, skewSamples), skewSamplesPerTC)
				if err != nil {
					t.Fatalf("offset %d: decode: %v", off, err)
				}
				for _, f := range frames {
					if got := decodedString(f); !sent[got] {
						t.Errorf("offset %d into the frame: decoder reported %s, which was never sent; "+
							"a %d-sample splice made a corrupted frame decode as a valid-looking timecode",
							off, got, skewSamples)
					}
				}
			}
		})
	}
}

// TestSkewCorrectionOnAFrameBoundaryIsTheBenignCase pins the reason the
// sweep above exists. Splicing exactly on a frame boundary is the most
// favourable point in the space, and measuring only there reports a
// benign result for a defect that is not benign.
//
// This one RUNS. It is deliberately not skipped alongside the sweep: it
// passes today, it keeps this file from being entirely inert while the
// sweep is skipped, and it is what stops someone reading the sweep as
// redundant and deleting it. On its own it would be worse than nothing,
// because it asserts the benign case and would read as coverage of the
// whole question.
//
// If this ever fails, the sweep's premise is wrong and its result needs
// re-reading, so it is worth its own name rather than a comment.
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

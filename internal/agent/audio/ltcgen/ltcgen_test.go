//go:build cgo

package ltcgen

import (
	"fmt"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/agent/audio/ltcgen/ltcdecodetest"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

const testSampleRate = 48000

func encodeRun(t *testing.T, rate pkgaudio.LTCFrameRate, start pkgaudio.LTCTimecode, frames int) (samples []int16, expected []pkgaudio.LTCTimecode) {
	t.Helper()
	enc, err := NewEncoder(rate, start, testSampleRate)
	if err != nil {
		t.Fatalf("NewEncoder(%s, %s): %v", rate, start, err)
	}
	defer enc.Close()

	for i := 0; i < frames; i++ {
		s, tc, err := enc.NextFrame()
		if err != nil {
			t.Fatalf("NextFrame %d: %v", i, err)
		}
		if len(s) == 0 {
			t.Fatalf("NextFrame %d: empty buffer", i)
		}
		samples = append(samples, s...)
		expected = append(expected, tc)
	}
	return samples, expected
}

func decodedString(f ltcdecodetest.Frame) pkgaudio.LTCTimecode {
	return pkgaudio.LTCTimecode(fmt.Sprintf("%02d:%02d:%02d:%02d", f.Hours, f.Mins, f.Secs, f.Frame))
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		rate  pkgaudio.LTCFrameRate
		start pkgaudio.LTCTimecode
	}{
		{"24fps zero offset", pkgaudio.LTCFrameRate24, "00:00:00:00"},
		{"24fps nonzero offset", pkgaudio.LTCFrameRate24, "00:05:12:03"},
		{"25fps zero offset", pkgaudio.LTCFrameRate25, "00:00:00:00"},
		{"25fps nonzero offset", pkgaudio.LTCFrameRate25, "01:00:00:00"},
		{"29.97fps zero offset", pkgaudio.LTCFrameRate2997, "00:00:00:00"},
		{"29.97fps nonzero offset", pkgaudio.LTCFrameRate2997, "00:10:00:00"},
		{"30fps zero offset", pkgaudio.LTCFrameRate30, "00:00:00:00"},
		{"30fps nonzero offset", pkgaudio.LTCFrameRate30, "00:02:30:15"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const frames = 40
			samples, expected := encodeRun(t, tc.rate, tc.start, frames)

			apv := int(testSampleRate / tc.rate.Rate())
			decoded, err := ltcdecodetest.Decode(samples, apv)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if len(decoded) == 0 {
				t.Fatalf("decoder recovered no frames")
			}

			// libltc's decoder cannot close out a frame until it sees the
			// following frame's sync word, so the last encoded frame is
			// never recovered; every earlier one must decode exactly,
			// starting at the offset with no lag (measured: 40 encoded
			// frames consistently produce 39 decoded ones).
			if len(decoded) != len(expected)-1 {
				t.Fatalf("decoded %d frames, expected %d (one less than encoded)", len(decoded), len(expected)-1)
			}
			for i, d := range decoded {
				got := decodedString(d)
				want := expected[i]
				if got != want {
					t.Fatalf("decoded frame %d = %s, want %s", i, got, want)
				}
				if tc.rate == pkgaudio.LTCFrameRate2997 && d.DropFrame {
					t.Fatalf("decoded frame %d at 29.97 fps carries the drop-frame bit; this project ships non-drop", i)
				}
			}
		})
	}
}

func TestNewEncoderRefusesInvalidRate(t *testing.T) {
	if _, err := NewEncoder("23.976", "00:00:00:00", testSampleRate); err == nil {
		t.Fatalf("expected an error for an unauthorized frame rate")
	}
}

func TestNewEncoderRefusesMalformedOffset(t *testing.T) {
	cases := []pkgaudio.LTCTimecode{"not a timecode", "25:00:00:00", "00:00:00:99"}
	for _, start := range cases {
		if _, err := NewEncoder(pkgaudio.LTCFrameRate25, start, testSampleRate); err == nil {
			t.Fatalf("expected an error for malformed offset %q", start)
		}
	}
}

//go:build cgo

package gstengine

import (
	"testing"
	"time"
)

// TestLocalRunningTimeClampsToZero proves localRunningTime never goes
// negative, which is what stops resyncMixerPads and Fade from anchoring
// against a position that precedes the branch's own segment start.
func TestLocalRunningTimeClampsToZero(t *testing.T) {
	cases := []struct {
		name         string
		segmentStart time.Duration
		atPos        time.Duration
		want         time.Duration
	}{
		{"no seek yet", 0, 3 * time.Second, 3 * time.Second},
		{"elapsed since seek", 5 * time.Second, 8 * time.Second, 3 * time.Second},
		// Start always calls seekTo(position) before resyncMixerPads(position),
		// so segmentStart and atPos are always equal here; this is what keeps
		// resyncMixerPads' offset from going negative on a repeat Start.
		{"Start's seek-then-resync sequence", 5 * time.Second, 5 * time.Second, 0},
		{"position behind segment start", 5 * time.Second, 2 * time.Second, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &branch{segmentStart: tc.segmentStart}
			if got := b.localRunningTime(tc.atPos); got != tc.want {
				t.Fatalf("localRunningTime(%s) with segmentStart %s = %s, want %s", tc.atPos, tc.segmentStart, got, tc.want)
			}
		})
	}
}

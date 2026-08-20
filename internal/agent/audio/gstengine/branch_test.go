//go:build cgo

package gstengine

import (
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
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
		{"segmentStart equals atPos", 5 * time.Second, 5 * time.Second, 0},
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

// TestFadeArrivedRequiresGainAtTarget proves the elapsed-duration
// completion bound never clears FadeActive on elapsed time alone: an
// elapsed fade whose gain has not reached its target must stay reported
// in progress, since a caller detects completion by Gain equalling the
// target, never by inferring it from fade.Duration having elapsed.
func TestFadeArrivedRequiresGainAtTarget(t *testing.T) {
	const fadeDuration = 3 * time.Second
	cases := []struct {
		name    string
		local   time.Duration
		running time.Duration
		gain    pkgaudio.Gain
		target  pkgaudio.Gain
		want    bool
	}{
		{"neither clock elapsed", 1 * time.Second, 1 * time.Second, 0, 0, false},
		{"local elapsed, gain at target", fadeDuration, 1 * time.Second, 0, 0, true},
		{"running elapsed, gain at target", 1 * time.Second, fadeDuration, 0.4, 0.4, true},
		{"running elapsed, gain stuck short of target", 1 * time.Second, fadeDuration, 0.76, 0, false},
		{"local elapsed, gain within tolerance", fadeDuration, 0, 0.4001, 0.4, true},
		{"local elapsed, gain outside tolerance", fadeDuration, 0, 0.5, 0.4, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fadeArrived(tc.local, 0, tc.running, 0, fadeDuration, tc.gain, tc.target)
			if got != tc.want {
				t.Fatalf("fadeArrived(local=%s, running=%s, gain=%v, target=%v) = %v, want %v",
					tc.local, tc.running, tc.gain, tc.target, got, tc.want)
			}
		})
	}
}

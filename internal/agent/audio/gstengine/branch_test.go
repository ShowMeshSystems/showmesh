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

// TestCheckStallLockedReportsAfterSustainedNoAdvance proves a Playing,
// unfrozen branch whose position sits exactly unchanged for at least
// positionStallThreshold reports a non-empty Reason -- the exact
// signature measured on a Raspberry Pi node whose output pipeline
// prerolled and then never advanced again, with no error anywhere.
func TestCheckStallLockedReportsAfterSustainedNoAdvance(t *testing.T) {
	b := &branch{}
	base := time.Now()

	// First observation at a given position always resets tracking, even
	// while Playing: there is nothing to compare against yet.
	if got := b.checkStallLocked(pkgaudio.StatePlaying, time.Second, base); got != "" {
		t.Fatalf("checkStallLocked() on the first observation = %q, want \"\"", got)
	}

	// Same position, but not enough time has passed yet.
	if got := b.checkStallLocked(pkgaudio.StatePlaying, time.Second, base.Add(positionStallThreshold-time.Millisecond)); got != "" {
		t.Fatalf("checkStallLocked() just under the threshold = %q, want \"\"", got)
	}

	// Same position, threshold now exceeded.
	got := b.checkStallLocked(pkgaudio.StatePlaying, time.Second, base.Add(positionStallThreshold+time.Millisecond))
	if got == "" {
		t.Fatalf("checkStallLocked() past the threshold with an unchanged position = \"\", want a stated reason")
	}
}

// TestCheckStallLockedResetsWhenPositionAdvances proves ordinary playback
// -- position genuinely moving between polls -- never reports a stall,
// regardless of how long the branch has been Playing.
func TestCheckStallLockedResetsWhenPositionAdvances(t *testing.T) {
	b := &branch{}
	base := time.Now()
	b.checkStallLocked(pkgaudio.StatePlaying, time.Second, base)

	got := b.checkStallLocked(pkgaudio.StatePlaying, 2*time.Second, base.Add(positionStallThreshold+time.Millisecond))
	if got != "" {
		t.Fatalf("checkStallLocked() with an advancing position = %q, want \"\"", got)
	}
}

// TestCheckStallLockedIgnoresNonPlayingAndFrozenBranches proves a branch
// that is not Playing, or is Playing but frozen (Pause's own deliberate
// hold), never reports a stall even after positionStallThreshold has
// elapsed with no position change -- both are legitimate reasons a
// position does not move.
func TestCheckStallLockedIgnoresNonPlayingAndFrozenBranches(t *testing.T) {
	base := time.Now()
	later := base.Add(positionStallThreshold + time.Millisecond)

	paused := &branch{}
	paused.checkStallLocked(pkgaudio.StatePaused, time.Second, base)
	if got := paused.checkStallLocked(pkgaudio.StatePaused, time.Second, later); got != "" {
		t.Fatalf("checkStallLocked() for a Paused branch = %q, want \"\"", got)
	}

	frozen := &branch{frozen: true}
	frozen.checkStallLocked(pkgaudio.StatePlaying, time.Second, base)
	if got := frozen.checkStallLocked(pkgaudio.StatePlaying, time.Second, later); got != "" {
		t.Fatalf("checkStallLocked() for a frozen (Paused-held) branch = %q, want \"\"", got)
	}
}

// TestFadeArrivedRequiresGainAtTarget proves the elapsed-duration
// completion bound never clears FadeActive on elapsed time alone: an
// elapsed fade whose gain has not reached its target must stay reported
// in progress, since a caller detects completion by Gain equalling the
// target, never by inferring it from fade.Duration having elapsed. It
// also proves the bound is raw stream position alone, per fadeArrived's
// doc comment.
func TestFadeArrivedRequiresGainAtTarget(t *testing.T) {
	const fadeDuration = 3 * time.Second
	cases := []struct {
		name   string
		pos    time.Duration
		gain   pkgaudio.Gain
		target pkgaudio.Gain
		want   bool
	}{
		{"not elapsed", 1 * time.Second, 0, 0, false},
		{"elapsed, gain at target", fadeDuration, 0, 0, true},
		{"elapsed, gain stuck short of target", fadeDuration, 0.76, 0, false},
		{"elapsed, gain within tolerance", fadeDuration, 0.4001, 0.4, true},
		{"elapsed, gain outside tolerance", fadeDuration, 0.5, 0.4, false},
		{"not elapsed even though gain already at target", 1 * time.Second, 0.4, 0.4, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fadeArrived(tc.pos, 0, fadeDuration, tc.gain, tc.target)
			if got != tc.want {
				t.Fatalf("fadeArrived(pos=%s, gain=%v, target=%v) = %v, want %v",
					tc.pos, tc.gain, tc.target, got, tc.want)
			}
		})
	}
}

// TestMixerLagBeyondPaceIsZeroForAMixerKeepingPace pins the line between
// a mixer that keeps pace with the clock, whose branches stay anchored to
// the clock, and one that has fallen behind it.
func TestMixerLagBeyondPaceIsZeroForAMixerKeepingPace(t *testing.T) {
	const latency = 31333333 * time.Nanosecond
	const running = 700000 * time.Second
	cases := []struct {
		name   string
		behind time.Duration
		want   time.Duration
	}{
		{"nearest a mixer keeping pace was measured", 41 * time.Millisecond, 0},
		{"99th percentile of a mixer keeping pace", 64 * time.Millisecond, 0},
		{"furthest a mixer keeping pace was measured", 76 * time.Millisecond, 0},
		{"exactly on the line", mixerPaceLatencies * latency, 0},
		{"a 600ms drain gap", 650 * time.Millisecond, 650*time.Millisecond - mixerPaceLatencies*latency},
		{"the late node", 2930 * time.Millisecond, 2930*time.Millisecond - mixerPaceLatencies*latency},
	}
	for _, tc := range cases {
		if got := mixerLagBeyondPace(running, running-tc.behind, latency); got != tc.want {
			t.Errorf("%s: mixer %s behind the clock: lag beyond pace = %s, want %s", tc.name, tc.behind, got, tc.want)
		}
	}
	if got := mixerLagBeyondPace(0, 0, latency); got != 0 {
		t.Errorf("pipeline with no running time yet: lag beyond pace = %s, want 0", got)
	}
}

// TestMixerLagFloorMovesAStartOnlyForLagThatStood pins which lag reaches a
// joining branch: lag recorded in every slot of the window, never lag
// from part of it.
func TestMixerLagFloorMovesAStartOnlyForLagThatStood(t *testing.T) {
	const window = mixerLagFloorSlots * mixerLagFloorSlot
	const age = 100 * window
	const lag = 2 * time.Second
	fill := func(f *mixerLagFloor, from, to time.Duration, lag time.Duration) {
		for at := from; at < to; at += mixerLagSampleInterval {
			f.record(at, lag)
		}
	}

	var stood mixerLagFloor
	fill(&stood, age-2*window, age, lag)
	stood.record(age, lag)
	if got := stood.standing(age, lag); got != lag {
		t.Errorf("lag recorded across the whole window: standing = %s, want %s", got, lag)
	}
	if got := stood.standing(age, lag/2); got != lag/2 {
		t.Errorf("a smaller reading at the join: standing = %s, want %s", got, lag/2)
	}

	var moment mixerLagFloor
	fill(&moment, age-2*window, age, lag)
	moment.record(age-window/2, 0)
	if got := moment.standing(age, lag); got != 0 {
		t.Errorf("one reading in pace inside the window: standing = %s, want 0", got)
	}

	var recent mixerLagFloor
	fill(&recent, age-2*window, age-window/2, 0)
	fill(&recent, age-window/2, age, lag)
	if got := recent.standing(age, lag); got != 0 {
		t.Errorf("lag that began half a window ago: standing = %s, want 0", got)
	}

	var starved mixerLagFloor
	fill(&starved, age-2*window, age-window/2, lag)
	fill(&starved, age-window/2+mixerLagFloorSlot, age, lag)
	if got := starved.standing(age, lag); got != 0 {
		t.Errorf("a slot of the window with no reading: standing = %s, want 0", got)
	}

	var young mixerLagFloor
	fill(&young, 0, window/2, lag)
	if got := young.standing(window/2, lag); got != 0 {
		t.Errorf("an engine younger than the window: standing = %s, want 0", got)
	}
}

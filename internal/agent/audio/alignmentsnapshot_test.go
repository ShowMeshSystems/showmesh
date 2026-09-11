package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestAlignmentSnapshotSignAndMagnitude proves the whole arithmetic path,
// both directions: an LTC timecode 2000ms ahead of the expected timecode
// reads as +2000, and a program position 500ms ahead of LTC (LTC behind)
// reads as -500.
func TestAlignmentSnapshotSignAndMagnitude(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	fake := fakeOf(t, m)
	sampledAt := c.now()

	// Program at 10s, expected LTC 00:00:10:00. Inject LTC 2s ahead.
	fake.SetAlignment(AlignmentSample{
		ProgramPosition: 10 * time.Second,
		LTCTimecode:     "00:00:12:00",
		LTCFrameRate:    pkgaudio.LTCFrameRate30,
		RunningTime:     10 * time.Second,
		SampledAt:       sampledAt,
	})
	snap := m.AlignmentSnapshot(ctx)
	if !snap.Measured {
		t.Fatalf("AlignmentSnapshot = %+v, want Measured true", snap)
	}
	if snap.OffsetMs != 2000 {
		t.Errorf("OffsetMs = %d, want 2000 (LTC ahead of program)", snap.OffsetMs)
	}
	if !snap.SampledAt.Equal(sampledAt) {
		t.Errorf("SampledAt = %v, want %v", snap.SampledAt, sampledAt)
	}
	if snap.SessionID != "show" {
		t.Errorf("SessionID = %q, want %q", snap.SessionID, "show")
	}

	// Program at 10s, expected LTC 00:00:10:00. Inject LTC 500ms behind.
	secondSample := sampledAt.Add(time.Second)
	fake.SetAlignment(AlignmentSample{
		ProgramPosition: 10 * time.Second,
		LTCTimecode:     "00:00:09:15",
		LTCFrameRate:    pkgaudio.LTCFrameRate30,
		RunningTime:     11 * time.Second,
		SampledAt:       secondSample,
	})
	snap2 := m.AlignmentSnapshot(ctx)
	if !snap2.Measured {
		t.Fatalf("AlignmentSnapshot (second sample) = %+v, want Measured true", snap2)
	}
	if snap2.OffsetMs != -500 {
		t.Errorf("OffsetMs = %d, want -500 (LTC behind program)", snap2.OffsetMs)
	}
	if !snap2.SampledAt.Equal(secondSample) {
		t.Errorf("SampledAt (second sample) = %v, want %v (must advance with a new sample)", snap2.SampledAt, secondSample)
	}
}

// TestAlignmentSnapshotPropagatesEngineNotKnown proves an engine sample
// that reports known=false propagates as Measured=false with the
// engine's own reason, never a fabricated offset.
func TestAlignmentSnapshotPropagatesEngineNotKnown(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	fake := fakeOf(t, m)
	fake.SetAlignmentUnknown("program branch has not rendered up to its presented position; underrun suspected")

	snap := m.AlignmentSnapshot(ctx)
	if snap.Measured {
		t.Fatalf("AlignmentSnapshot = %+v, want Measured false", snap)
	}
	if snap.Reason == "" {
		t.Error("Reason is empty, want the engine's stated reason")
	}
	if snap.SessionID != "show" {
		t.Errorf("SessionID = %q, want %q even when not measured", snap.SessionID, "show")
	}
}

// TestAlignmentSnapshotReportsNotMeasuredWithNoLTCHoldingSession proves a
// node with no session currently holding its one LTC run reports not
// measured, never a value inferred from anything else.
func TestAlignmentSnapshotReportsNotMeasuredWithNoLTCHoldingSession(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	snap := m.AlignmentSnapshot(ctx)
	if snap.Measured {
		t.Fatalf("AlignmentSnapshot = %+v, want Measured false (no session holds LTC)", snap)
	}
	if snap.Reason == "" {
		t.Error("Reason is empty, want a stated explanation")
	}
	if snap.SessionID != "" {
		t.Errorf("SessionID = %q, want empty with no holding session", snap.SessionID)
	}
}

// TestAlignmentSnapshotNotMeasuredWhilePaused proves the gate that only a
// Playing session with a loaded handle is sampled: pausing the
// LTC-holding session must not fall through to an engine sample.
func TestAlignmentSnapshotNotMeasuredWhilePaused(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	m.Pause(ctx, "show", "pause", 3)

	fake := fakeOf(t, m)
	fake.SetAlignment(AlignmentSample{
		ProgramPosition: 10 * time.Second,
		LTCTimecode:     "00:00:10:00",
		LTCFrameRate:    pkgaudio.LTCFrameRate30,
		RunningTime:     10 * time.Second,
		SampledAt:       c.now(),
	})

	snap := m.AlignmentSnapshot(ctx)
	if snap.Measured {
		t.Fatalf("AlignmentSnapshot while paused = %+v, want Measured false", snap)
	}
}

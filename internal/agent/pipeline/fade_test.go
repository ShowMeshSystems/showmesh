package pipeline

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

func TestFadeLevelRampsLinearlyAndHoldsAtTheEnds(t *testing.T) {
	start := time.Unix(1000, 0)
	f := Fade{From: 1, To: 0, Start: start, End: start.Add(2 * time.Second)}
	for _, c := range []struct {
		at   time.Duration
		want float64
	}{{-time.Second, 1}, {0, 1}, {500 * time.Millisecond, 0.75}, {time.Second, 0.5}, {2 * time.Second, 0}, {5 * time.Second, 0}} {
		if got := f.Level(start.Add(c.at)); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("Level at %v = %v, want %v", c.at, got, c.want)
		}
	}
	instant := Fade{From: 0.2, To: 1, Start: start, End: start}
	if got := instant.Level(start.Add(time.Millisecond)); got != 1 {
		t.Fatalf("an instant fade reads %v just after its start, want 1", got)
	}
}

func TestScaleBytesIsIdentityAtFullAndZeroAtNone(t *testing.T) {
	src := []byte{0, 1, 11, 128, 254, 255}
	dst := make([]byte, len(src))
	ScaleBytes(dst, src, 1)
	for i := range src {
		if dst[i] != src[i] {
			t.Fatalf("full level changed byte %d: %d -> %d", i, src[i], dst[i])
		}
	}
	ScaleBytes(dst, src, 0)
	if !allBytesEqual(dst, 0) {
		t.Fatalf("zero level left %v", dst)
	}
	ScaleBytes(dst, []byte{255, 100}, 0.5)
	if dst[0] != 128 || dst[1] != 50 {
		t.Fatalf("half level gave %v, want [128 50]", dst[:2])
	}
}

// fakeLevelHoldBlack is a hold-black source that also reports a level.
type fakeLevelHoldBlack struct {
	fakeHoldBlack
	level atomic.Uint64
}

func (f *fakeLevelHoldBlack) Level(time.Time) float64 { return math.Float64frombits(f.level.Load()) }

func (f *fakeLevelHoldBlack) setLevel(l float64) { f.level.Store(math.Float64bits(l)) }

func TestFrameWriterFadeScalesContentAndBlackoutStillWins(t *testing.T) {
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)
	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 5, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StatePlaying, 50) // frame 10 -> byte value 11
	src := &fakeLevelHoldBlack{}
	src.setLevel(1)

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "seq.fseq", 0, 8, 8, 1, IdleOutputHold, nil, src, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)
	t.Cleanup(func() {
		cancel()
		fw.Stop()
	})

	waitFor(t, func() bool { w, _, _ := fw.Counts(); return w >= 1 })
	if snap := fp.stdinSnapshot(); !allBytesEqual(snap, 11) {
		t.Fatalf("full level must be byte-identical to content, got %v", snap[:8])
	}

	mark := len(fp.stdinSnapshot())
	src.setLevel(0.5)
	waitFor(t, func() bool {
		snap := fp.stdinSnapshot()
		return len(snap) >= mark+16 && allBytesEqual(snap[len(snap)-8:], 6)
	})

	src.held.Store(true)
	waitFor(t, func() bool {
		snap := fp.stdinSnapshot()
		return len(snap) >= 8 && allBytesEqual(snap[len(snap)-8:], 0)
	})
	if s, _ := sup.Snapshot(surfaceID); s.Drawing != DrawingBlackout {
		t.Fatalf("Drawing = %q while held black during a fade, want %q", s.Drawing, DrawingBlackout)
	}
}

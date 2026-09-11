//go:build cgo

package gstengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestSeekAtZeroGainNeverBurstsDuringSwap proves a swap carries the old
// branch's gain to the replacement before any buffer can reach the mix:
// a session muted to gain 0 stays silent in the real captured program
// output across a Seek, which swaps in a replacement branch. Before the
// fix, volume was set only after the replacement's held buffer and up
// to queueMaxSizeTime already queued at the property's default of 1.0,
// so this session would have measured a real, audible burst at join.
func TestSeekAtZeroGainNeverBurstsDuringSwap(t *testing.T) {
	e, capture := newLTCLagEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "tone.wav")
	generateWAV(t, wav, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "prog", mediaRef(wav), 6*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "prog", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.SetGain(ctx, "prog", 0); err != nil {
		t.Fatalf("SetGain: %v", err)
	}

	// A generous real wall-clock wait, not just a captured-sample count:
	// SetGain's own effect must propagate through queue's own buffering
	// before the output is genuinely silent, and this must fully clear
	// before "before" is taken as the pre-seek silence baseline.
	time.Sleep(1500 * time.Millisecond)
	before := capture.framesCaptured()
	if before == 0 {
		t.Fatalf("no program audio captured before the seek")
	}
	countBad := func(ch1 []int16, from, to int) (count int, first, last int, maxMag int16) {
		first, last = -1, -1
		for i := from; i < to; i++ {
			v := ch1[i]
			if v > silenceThreshold || v < -silenceThreshold {
				count++
				if first == -1 {
					first = i
				}
				last = i
				if v < 0 {
					v = -v
				}
				if v > maxMag {
					maxMag = v
				}
			}
		}
		return
	}

	preCh1, _ := capture.snapshot()
	preWindow := before - 4410 // last 100ms before the baseline
	if preWindow < 0 {
		preWindow = 0
	}
	if n, first, last, mag := countBad(preCh1, preWindow, before); n > 0 {
		t.Fatalf("%d non-silent sample(s) already present in the 100ms before the baseline "+
			"(range %d..%d), max magnitude %d: SetGain had not yet reached the output; "+
			"this is a test methodology problem, not evidence about the swap", n, first, last, mag)
	}

	if _, err := e.Seek(ctx, "prog", 3*time.Second); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	ch1, _ := capture.snapshot()
	if before >= len(ch1) {
		t.Fatalf("capture did not grow past the seek: before=%d after=%d", before, len(ch1))
	}
	if n, first, last, mag := countBad(ch1, before, len(ch1)); n > 0 {
		t.Fatalf("%d non-silent sample(s) after Seek at gain 0 (range %d..%d of %d total, before=%d), max magnitude %d: "+
			"a unity-gain burst leaked through the swap", n, first, last, len(ch1), before, mag)
	}

	_ = e.Release(context.Background(), "prog")
}

//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestTimedOutSeekRefusesFurtherAnchoring proves the fix: once a seek's
// own ctx deadline fires, every later operation that would anchor the
// mixer or a fade to segmentStart refuses with errAnchorUnknown instead
// of silently reanchoring against a segment it can no longer vouch for.
func TestTimedOutSeekRefusesFurtherAnchoring(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const handle = "seekstale2"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 6*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, handle, 200*time.Millisecond, 5*time.Second)

	tctx, tcancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer tcancel()
	if _, err := e.Seek(tctx, handle, 4*time.Second); err == nil {
		t.Fatalf("Seek with an exhausted deadline: err = nil, want a timeout")
	}

	// Give the abandoned seek time to land, matching the reproduction
	// above, so this exercises the branch in the state a real timeout
	// leaves it in rather than immediately after the call returns.
	time.Sleep(500 * time.Millisecond)

	if _, err := e.Start(ctx, handle, 1*time.Second); !errors.Is(err, errAnchorUnknown) {
		t.Fatalf("Start on a branch with unknown anchoring: err = %v, want errAnchorUnknown in its chain", err)
	}
	if _, err := e.Seek(ctx, handle, 2*time.Second); !errors.Is(err, errAnchorUnknown) {
		t.Fatalf("Seek on a branch with unknown anchoring: err = %v, want errAnchorUnknown in its chain", err)
	}
	if _, err := e.Resume(ctx, handle); !errors.Is(err, errAnchorUnknown) {
		t.Fatalf("Resume on a branch with unknown anchoring: err = %v, want errAnchorUnknown in its chain", err)
	}
	fade := pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: 200 * time.Millisecond, TargetGain: 0}
	if _, err := e.Fade(ctx, handle, fade); !errors.Is(err, errAnchorUnknown) {
		t.Fatalf("Fade on a branch with unknown anchoring: err = %v, want errAnchorUnknown in its chain", err)
	}

	_ = e.Release(context.Background(), handle)
}

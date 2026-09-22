//go:build cgo

package gstengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// TestReleaseAllMutesEveryTargetBeforeAnyTeardownAndReturnsWithinBound
// proves ReleaseAll's own required ordering (see its doc comment): every
// targeted branch is muted before any of them is torn down, not as a
// side effect of teardown itself starting, and a ctx that leaves no
// budget for teardown at all still returns promptly with every targeted
// branch already silent. A ctx already expired when ReleaseAll is
// called forces every teardown pass to stop before running a single
// teardown, isolating the mute pass: whatever it did ran strictly
// before that deadline was ever checked, and nothing here can block
// waiting on GStreamer, so a fast return proves the bound holds even
// when every teardown this call would otherwise attempt cannot run at
// all -- the same property a teardown genuinely forced to block would
// leave: every target already silent, regardless of how much of the
// teardown itself completed.
func TestReleaseAllMutesEveryTargetBeforeAnyTeardownAndReturnsWithinBound(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	handles := []agentaudio.EngineHandle{"mutefirst1", "mutefirst2"}
	branches := make([]*branch, 0, len(handles))
	for _, h := range handles {
		wav := filepath.Join(dir, string(h)+".wav")
		generateWAV(t, wav, 3)
		if _, err := e.Load(ctx, h, mediaRef(wav), 3*time.Second); err != nil {
			t.Fatalf("Load %s: %v", h, err)
		}
		if _, err := e.Start(ctx, h, 0); err != nil {
			t.Fatalf("Start %s: %v", h, err)
		}
		waitForPosition(t, e, string(h), 50*time.Millisecond, 5*time.Second)
		b, err := e.branchFor(h)
		if err != nil {
			t.Fatalf("branchFor %s: %v", h, err)
		}
		branches = append(branches, b)
	}

	expiredCtx, expiredCancel := context.WithTimeout(ctx, time.Nanosecond)
	defer expiredCancel()
	time.Sleep(time.Millisecond)

	began := time.Now()
	if _, err := e.ReleaseAll(expiredCtx); err == nil {
		t.Fatal("ReleaseAll with an already-expired ctx returned no error, want the ctx deadline reported")
	}
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Fatalf("ReleaseAll with an already-expired ctx took %s, want well under its own bound: "+
			"it must never start a teardown it cannot finish rather than returning promptly", elapsed)
	}

	for i, b := range branches {
		for k, pad := range b.channelMixerPads {
			if pad == nil {
				continue
			}
			muted, _ := pad.ObjectProperty("mute").(bool)
			if !muted {
				t.Fatalf("%s channel %d pad not muted after ReleaseAll, want muted even though its own teardown never ran", handles[i], k)
			}
		}
	}

	live, err := e.LiveHandles(context.Background())
	if err != nil {
		t.Fatalf("LiveHandles: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("LiveHandles = %v, want empty: handles are removed from e.handles as part of the silence-first step, not only once torn down", live)
	}
}

// TestReleaseAllDoesNotCountAnOrdinarySwapsRetiringBranch proves the
// returned count (and the handle list it is len of) never includes a
// retiring or in-flight leftover an ordinary, uncontested Seek, Resume,
// or Start left behind: that branch was never live under any handle
// this call could report, and counting it would inflate an orphan count
// with normal bookkeeping that has nothing to do with this sweep.
func TestReleaseAllDoesNotCountAnOrdinarySwapsRetiringBranch(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const handle = agentaudio.EngineHandle("ordinaryseek")
	wav := filepath.Join(dir, "ordinaryseek.wav")
	generateWAV(t, wav, 4)
	if _, err := e.Load(ctx, handle, mediaRef(wav), 4*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, string(handle), 100*time.Millisecond, 5*time.Second)

	// An uncontested Seek on an already-joined branch always swaps in a
	// replacement, leaving the old branch retiring in the background --
	// nothing here races it, so it commits normally.
	if _, err := e.Seek(ctx, handle, 2*time.Second); err != nil {
		t.Fatalf("Seek: %v", err)
	}

	released, err := e.ReleaseAll(ctx, handle)
	if err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	if len(released) != 0 {
		t.Fatalf("ReleaseAll(except %s) released %v, want none: the ordinary Seek's own retiring branch is not an orphan", handle, released)
	}

	if _, err := e.Observe(ctx, handle); err != nil {
		t.Fatalf("Observe(%s) after ReleaseAll excepted it: %v", handle, err)
	}
	_ = e.Release(context.Background(), handle)
}

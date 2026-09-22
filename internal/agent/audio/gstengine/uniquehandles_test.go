//go:build cgo

package gstengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestLoadRefusesToOverwriteALiveHandle reproduces the rehearsal defect
// at the engine level: Load must never silently take over a handle a
// live branch already answers to (the old behavior: e.handles[handle] =
// b, unconditionally). A second Load under the same name must fail, and
// the branch already there, still playing, must survive it untouched,
// audible and addressable, rather than becoming unreachable.
func TestLoadRefusesToOverwriteALiveHandle(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wavA := filepath.Join(dir, "a.wav")
	wavB := filepath.Join(dir, "b.wav")
	generateWAV(t, wavA, 4)
	generateWAV(t, wavB, 4)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "dup", mediaRef(wavA), 4*time.Second); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	if _, err := e.Start(ctx, "dup", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "dup", 100*time.Millisecond, 5*time.Second)

	if _, err := e.Load(ctx, "dup", mediaRef(wavB), 4*time.Second); err == nil {
		t.Fatal("second Load under the same handle succeeded, want a refusal: this would silently orphan the first branch")
	}

	obs, err := e.Observe(ctx, "dup")
	if err != nil {
		t.Fatalf("Observe after the refused second Load: %v", err)
	}
	if obs.State != pkgaudio.StatePlaying {
		t.Fatalf("state after the refused second Load = %q, want playing (the first branch must survive)", obs.State)
	}

	_ = e.Release(context.Background(), "dup")
}

// TestCueSequenceWithDistinctHandlesThenReleaseAllLeavesNoLiveBranch is
// the engine-level replay of the rehearsal sequence (the session layer's
// own acceptance test, TestSilenceAllAfterPromoteAndRestagingReleasesEveryBranch
// in package audio, runs the same shape through [agentaudio.Manager]):
// load a branch under a handle (the staged cue), "promote" it by
// continuing to address it under that SAME handle (Promote never
// releases the handle it hands to the show session, it only reassigns
// ownership), then restage a second, different cue under a genuinely
// distinct handle, exactly what a unique-per-load name guarantees
// ([TestLoadRefusesToOverwriteALiveHandle] is what catches a regression
// that reuses the same name instead). A final [Engine.ReleaseAll], the
// same call [agentaudio.Manager.SilenceAll] ends on, must then leave the
// engine holding no live branch at all.
func TestCueSequenceWithDistinctHandlesThenReleaseAllLeavesNoLiveBranch(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wavA := filepath.Join(dir, "a.wav")
	wavB := filepath.Join(dir, "b.wav")
	generateWAV(t, wavA, 4)
	generateWAV(t, wavB, 4)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	// Staging loads cue A, then it is "promoted": the show session keeps
	// addressing the exact same handle Promote handed it, never releasing
	// it.
	const show = agentaudio.EngineHandle("cue-activation:show/media")
	if _, err := e.Load(ctx, show, mediaRef(wavA), 4*time.Second); err != nil {
		t.Fatalf("Load (staged cue A, now owned by the show handle): %v", err)
	}
	if _, err := e.Start(ctx, show, 0); err != nil {
		t.Fatalf("Start show: %v", err)
	}
	waitForPosition(t, e, string(show), 100*time.Millisecond, 5*time.Second)

	// The prepare-ahead restages a second, different cue under a
	// genuinely distinct handle.
	const restaged = agentaudio.EngineHandle("cue-activation:stage/media")
	if _, err := e.Load(ctx, restaged, mediaRef(wavB), 4*time.Second); err != nil {
		t.Fatalf("Load (restaged cue B under a distinct handle): %v", err)
	}

	// The show branch must still be there, still playing, fully
	// addressable, never silently replaced.
	obs, err := e.Observe(ctx, show)
	if err != nil {
		t.Fatalf("Observe show after the restage: %v", err)
	}
	if obs.State != pkgaudio.StatePlaying {
		t.Fatalf("show branch state after the restage = %q, want playing", obs.State)
	}

	released, err := e.ReleaseAll(ctx)
	if err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	if len(released) != 2 {
		t.Fatalf("ReleaseAll released %d branch(es), want 2", len(released))
	}
	live, err := e.LiveHandles(ctx)
	if err != nil {
		t.Fatalf("LiveHandles: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("engine still holds %d live branch(es) after ReleaseAll: %v", len(live), live)
	}
}

// TestReleaseAllTearsDownEveryBranchExceptTheNamed proves
// [Engine.ReleaseAll]: every live branch except the named handles is torn
// down, the excepted one survives untouched, and the reported count
// matches exactly what was released.
func TestReleaseAllTearsDownEveryBranchExceptTheNamed(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	handles := []agentaudio.EngineHandle{"keep", "drop1", "drop2"}
	for _, h := range handles {
		wav := filepath.Join(dir, string(h)+".wav")
		generateWAV(t, wav, 3)
		if _, err := e.Load(ctx, h, mediaRef(wav), 3*time.Second); err != nil {
			t.Fatalf("Load %s: %v", h, err)
		}
		if _, err := e.Start(ctx, h, 0); err != nil {
			t.Fatalf("Start %s: %v", h, err)
		}
	}

	released, err := e.ReleaseAll(ctx, "keep")
	if err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	if len(released) != 2 {
		t.Fatalf("ReleaseAll released %d branch(es), want 2", len(released))
	}

	live, err := e.LiveHandles(ctx)
	if err != nil {
		t.Fatalf("LiveHandles: %v", err)
	}
	if len(live) != 1 || live[0] != "keep" {
		t.Fatalf("live handles after ReleaseAll = %v, want exactly [keep]", live)
	}

	if _, err := e.Observe(ctx, "keep"); err != nil {
		t.Fatalf("Observe on the kept handle after ReleaseAll: %v", err)
	}
	for _, h := range []agentaudio.EngineHandle{"drop1", "drop2"} {
		if _, err := e.Observe(ctx, h); err == nil {
			t.Fatalf("Observe on %s after ReleaseAll returned no error, want it gone", h)
		}
	}

	_ = e.Release(context.Background(), "keep")
}

// TestReleaseAllWithNoExceptionsReleasesEverything proves the
// [Manager.SilenceAll] shape of the call: no exceptions at all leaves the
// engine holding nothing.
func TestReleaseAllWithNoExceptionsReleasesEverything(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	wav := filepath.Join(dir, "only.wav")
	generateWAV(t, wav, 3)
	if _, err := e.Load(ctx, "only", mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "only", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	released, err := e.ReleaseAll(ctx)
	if err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	if len(released) != 1 {
		t.Fatalf("ReleaseAll released %d branch(es), want 1", len(released))
	}

	live, err := e.LiveHandles(ctx)
	if err != nil {
		t.Fatalf("LiveHandles: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("live handles after a no-exceptions ReleaseAll = %v, want none", live)
	}
}

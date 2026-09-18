package audio

import (
	"context"
	"strings"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file pins the guarantee that a slow Prepare on one session never
// holds up a command for another session on the same node: [Manager]
// locks per session ([Session.mu]), never node-wide, so Prepare's call
// into the engine (which can be slow -- a cold decode of a large asset)
// blocks only the session it was issued against. Deterministic, like
// command_test.go's blockingOp: a channel-gated hook, not a sleep, so the
// interleaving these tests need is constructed rather than hoped for.

func TestPrepareOnOneSessionDoesNotBlockPauseOnAnother(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	engine := NewFakeEngine(c.now)
	mgr := NewManager(availableFakeEngine{engine}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)

	assetA := writeTestAsset(t, dir, "a.wav", "asset-a", []byte("aaaa"))
	assetB := writeTestAsset(t, dir, "b.wav", "asset-b", []byte("bbbb"))
	ctx := context.Background()

	if out := mgr.Apply(ctx, "sessionA", "invA1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(assetA)}); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("apply A: %+v", out)
	}
	if out := mgr.Apply(ctx, "sessionB", "invB1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(assetB)}); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("apply B: %+v", out)
	}
	// Session B has to already be prepared and started for Pause to have
	// anything to act on.
	if out := mgr.Prepare(ctx, "sessionB", "invB2", 2); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("prepare B: %+v", out)
	}
	if out := mgr.Start(ctx, "sessionB", "invB3", 3); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("start B: %+v", out)
	}

	entered := make(chan struct{})
	unblock := make(chan struct{})
	engine.LoadHook = func(handle EngineHandle) {
		if !strings.HasPrefix(string(handle), "sessionA/") {
			return
		}
		close(entered)
		<-unblock
	}

	prepareDone := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		prepareDone <- mgr.Prepare(ctx, "sessionA", "invA2", 2)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("session A's prepare never reached its slow Load")
	}

	pauseDone := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		pauseDone <- mgr.Pause(ctx, "sessionB", "invB4", 4)
	}()

	select {
	case out := <-pauseDone:
		if out.Outcome != pkgaudio.OutcomePosition {
			t.Errorf("pause B outcome = %+v, want position", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pause on session B did not complete while session A's prepare was still in flight -- cross-session blocking")
	}

	close(unblock)
	select {
	case out := <-prepareDone:
		if out.Outcome != pkgaudio.OutcomePosition {
			t.Errorf("prepare A outcome = %+v, want position", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session A's prepare never completed after being unblocked")
	}
}

func TestSameSessionCommandsStillRunInOrder(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	engine := NewFakeEngine(c.now)
	mgr := NewManager(availableFakeEngine{engine}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)

	asset := writeTestAsset(t, dir, "a.wav", "asset-a", []byte("aaaa"))
	ctx := context.Background()

	if out := mgr.Apply(ctx, "sessionA", "invA1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(asset)}); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("apply A: %+v", out)
	}

	entered := make(chan struct{})
	unblock := make(chan struct{})
	engine.LoadHook = func(handle EngineHandle) {
		close(entered)
		<-unblock
	}

	prepareDone := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		prepareDone <- mgr.Prepare(ctx, "sessionA", "invA2", 2)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("session A's prepare never reached its slow Load")
	}

	// A second command for the SAME session, dispatched while the first is
	// still executing: it must wait behind the first, not run concurrently
	// with or ahead of it.
	pauseDone := make(chan pkgaudio.OutcomeResult, 1)
	pauseStarted := make(chan struct{})
	go func() {
		close(pauseStarted)
		pauseDone <- mgr.Pause(ctx, "sessionA", "invA3", 3)
	}()
	<-pauseStarted

	select {
	case out := <-pauseDone:
		t.Fatalf("pause for session A completed (%+v) before its own session's in-flight prepare finished -- same-session ordering violated", out)
	case <-time.After(200 * time.Millisecond):
		// Expected: pause is still blocked behind prepare's own s.mu.
	}

	close(unblock)

	select {
	case out := <-prepareDone:
		if out.Outcome != pkgaudio.OutcomePosition {
			t.Errorf("prepare A outcome = %+v, want position", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session A's prepare never completed after being unblocked")
	}

	select {
	case out := <-pauseDone:
		// Prepare leaves the session Ready, not Playing, so this pause
		// does not confirm -- what matters here is only that it never
		// executed before prepare finished, proven above.
		if out.Outcome == pkgaudio.OutcomeStarted || out.Outcome == pkgaudio.OutcomePosition {
			t.Errorf("pause A outcome = %+v, want a non-confirming outcome (nothing was playing to pause)", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session A's pause never completed after prepare finished")
	}
}

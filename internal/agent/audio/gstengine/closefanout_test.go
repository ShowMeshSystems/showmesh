//go:build cgo

package gstengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// TestCloseTearsDownThreeConcurrentPlayingBranches exercises the fan-out
// [Engine.Close] added: every branch teardown runs on its own goroutine
// rather than sequentially. Every other Close-related test in this
// package loads zero or one branch, which proves nothing about that
// fan-out. This is the missing case: three loaded and playing branches
// torn down at once, run under -race like the rest of the suite.
func TestCloseTearsDownThreeConcurrentPlayingBranches(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()

	handles := [3]string{"fanout1", "fanout2", "fanout3"}
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	for _, h := range handles {
		wav := filepath.Join(dir, h+".wav")
		generateWAV(t, wav, 4)
		if _, err := e.Load(ctx, agentaudio.EngineHandle(h), mediaRef(wav), 4*time.Second); err != nil {
			t.Fatalf("Load %s: %v", h, err)
		}
		if _, err := e.Start(ctx, agentaudio.EngineHandle(h), 0); err != nil {
			t.Fatalf("Start %s: %v", h, err)
		}
	}

	for _, h := range handles {
		waitForPosition(t, e, h, 100*time.Millisecond, 5*time.Second)
	}

	if err := e.Close(); err != nil {
		t.Fatalf("Close with three loaded, playing branches: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close is not idempotent: %v", err)
	}

	ok, reason := e.Available()
	if ok {
		t.Fatal("a closed engine reports itself available")
	}
	if reason != closedReason {
		t.Fatalf("closed reason = %q, want %q", reason, closedReason)
	}
	for _, h := range handles {
		if _, err := e.Observe(ctx, agentaudio.EngineHandle(h)); err == nil {
			t.Fatalf("Observe(%s) on a closed engine returned no error", h)
		}
	}
}

//go:build cgo

package gstengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestStartAfterCompletedSeekPlaysAgain proves a never-joined branch that
// reached Completed (a Seek at or past its own end) is still playable by
// a later Start at a normal position. Before the eosSeq fix, onEOS's
// early return on an already-Completed state swallowed prepare's own
// EOS wait, and Start's currentState()==Completed check read the stale
// state left over from the first Seek, so the branch never played.
func TestStartAfterCompletedSeekPlaysAgain(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const handle = "staleeosstart"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}

	obs, err := e.Seek(ctx, handle, 10*time.Second)
	if err != nil {
		t.Fatalf("Seek past end: %v", err)
	}
	if obs.State != pkgaudio.StateCompleted {
		t.Fatalf("Seek past end: state = %q, want completed", obs.State)
	}

	obs, err = e.Start(ctx, handle, 0)
	if err != nil {
		t.Fatalf("Start after a completed Seek: %v", err)
	}
	if obs.State != pkgaudio.StatePlaying {
		t.Fatalf("Start after a completed Seek: state = %q, want playing", obs.State)
	}
	if state := waitForPosition(t, e, handle, 200*time.Millisecond, 5*time.Second); state != pkgaudio.StatePlaying {
		t.Fatalf("after Start: state = %q, want playing", state)
	}
}

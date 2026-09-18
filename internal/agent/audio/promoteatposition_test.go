package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestPromoteAtPositionPresentsPositionAtT0UsingStagedHandle proves
// ADR-051's own reuse of a prepare-ahead staged handle at a precise,
// media-clock-scheduled T0: [Manager.PromoteAtPosition] waits for the
// scheduled instant (exactly as [Manager.StartAtPosition] does, see
// TestStartAtPositionPresentsTheExplicitPositionAtT0, timeline_test.go),
// moves the ALREADY-LOADED staged handle rather than loading a fresh one,
// and presents the caller's own explicit position on that SAME engine
// call, never a Start-then-Seek pair.
func TestPromoteAtPositionPresentsPositionAtT0UsingStagedHandle(t *testing.T) {
	f := newScheduledFixture(t, 0)
	ctx := context.Background()
	const staging = pkgaudio.SessionID("staging")

	ref := writeTestAsset(t, f.m.assetDir, "b.wav", "asset-2", []byte("staged content"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}

	if r := f.m.Apply(ctx, staging, "stage-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage apply refused: %+v", r)
	}
	if r := f.m.Prepare(ctx, staging, "stage-prepare", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage prepare refused: %+v", r)
	}
	stagedHandle, ok := f.engine.LastLoadedHandle()
	if !ok {
		t.Fatal("no Load recorded after staging Apply+Prepare; test setup is broken")
	}

	// f.id's own fixture Apply used a different asset; re-Apply it with the
	// SAME content just staged, so Promote's identity check has something
	// to match against, mirroring activateAudio's own real sequence
	// (Apply onto the cue session, then Promote from staging).
	if r := f.m.Apply(ctx, f.id, "show-reapply", 5, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show re-apply refused: %+v", r)
	}

	const wantPosition = 4200 * time.Millisecond
	t0 := f.media.Now(ctx).Time.Add(30 * time.Millisecond)
	before := f.media.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- f.m.PromoteAtPosition(ctx, staging, f.id, "promote-at", 6, t0.UnixNano(), wantPosition)
	}()
	f.media.waitForReads(t, before+1)
	f.media.advance(30 * time.Millisecond)

	var out pkgaudio.OutcomeResult
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("PromoteAtPosition never returned after the media clock reached T0")
	}
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("PromoteAtPosition = %q (%s), want started", out.Outcome, out.Reason)
	}
	if f.engine.seekCount() != 0 {
		t.Fatalf("Seek called %d times, want 0: position must be presented in the same engine call, never a trailing Seek", f.engine.seekCount())
	}

	gotHandle, ok := f.engine.LastLoadedHandle()
	if !ok || gotHandle != stagedHandle {
		t.Fatalf("last loaded handle = %q (ok=%v), want the staged handle %q unchanged: PromoteAtPosition must reuse it, never load a fresh one", gotHandle, ok, stagedHandle)
	}

	snaps := f.m.Snapshot(ctx)
	var got *SessionSnapshot
	for i := range snaps {
		if snaps[i].ID == f.id {
			got = &snaps[i]
		}
	}
	if got == nil {
		t.Fatalf("no snapshot for session %q: %+v", f.id, snaps)
	}
	if got.State != pkgaudio.StatePlaying {
		t.Fatalf("state = %s, want playing", got.State)
	}
	if !got.PositionKnown || got.Position != wantPosition {
		t.Fatalf("position = %v (known=%v), want %v", got.Position, got.PositionKnown, wantPosition)
	}
}

// TestPromoteAtPositionInThePastIsRefused proves PromoteAtPosition shares
// [Manager.StartAtPosition]'s own show-continues refusal for an already-
// passed T0, rather than promoting late.
func TestPromoteAtPositionInThePastIsRefused(t *testing.T) {
	f := newScheduledFixture(t, 0)
	ctx := context.Background()
	const staging = pkgaudio.SessionID("staging")

	ref := writeTestAsset(t, f.m.assetDir, "b.wav", "asset-2", []byte("staged content"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}
	if r := f.m.Apply(ctx, staging, "stage-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage apply refused: %+v", r)
	}
	if r := f.m.Prepare(ctx, staging, "stage-prepare", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage prepare refused: %+v", r)
	}
	if r := f.m.Apply(ctx, f.id, "show-reapply", 5, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show re-apply refused: %+v", r)
	}

	past := f.media.Now(ctx).Time.Add(-time.Second)
	out := f.m.PromoteAtPosition(ctx, staging, f.id, "promote-past", 6, past.UnixNano(), time.Second)
	if out.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("PromoteAtPosition(past T0) outcome = %q (%s), want refused", out.Outcome, out.Reason)
	}

	snaps := f.m.Snapshot(ctx)
	for _, s := range snaps {
		if s.ID == f.id && s.State == pkgaudio.StatePlaying {
			t.Fatal("a refused PromoteAtPosition left the show session playing")
		}
	}
}

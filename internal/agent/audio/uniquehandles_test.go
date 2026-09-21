package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestSilenceAllAfterPromoteAndRestagingReleasesEveryBranch reproduces the
// rehearsal defect directly against [FakeEngine]: [Session.engineHandleFor]
// used to name a handle from only <sessionId>/<itemId>, and a session
// applying MEDIA directly (never a playlist) always resolves itemId to
// the same "media" constant ([nonPlaylistItemID]), so a staging session
// id reused for two different cues minted the identical handle name
// twice. [Manager.Promote] transfers the first handle onto the show
// session without ever releasing it, so the staging session's SECOND
// load then silently overwrote the still-playing show branch's own slot
// in the engine's handle map, and no later Stop could ever reach it
// again: whichever of the two sessions [Manager.SilenceAll] happened to
// visit first released the shared slot, and the other's own Stop then
// found nothing there and stuck in StateStopping.
//
// This must fail before the fix (one of the two sessions never reaches
// StateStopped) regardless of which session SilenceAll's own map
// iteration visits first; looped so a run against this package proves it
// for both possible orders rather than whichever one Go's randomized map
// iteration happens to pick on a single run.
func TestSilenceAllAfterPromoteAndRestagingReleasesEveryBranch(t *testing.T) {
	const staging = pkgaudio.SessionID("cue-activation:prepare-staging")
	const show = pkgaudio.SessionID("cue-activation:show")

	for attempt := 0; attempt < 30; attempt++ {
		c := newClock(time.Now())
		m := newAvailableTestManager(t, c)
		ctx := context.Background()

		refA := writeTestAsset(t, m.assetDir, "song-a.wav", "asset-a", []byte("song a content"))
		if r := m.Apply(ctx, staging, "stage-apply-a", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(refA)}); r.Outcome == pkgaudio.OutcomeRefused {
			t.Fatalf("attempt %d: stage apply a: %+v", attempt, r)
		}
		if r := m.Prepare(ctx, staging, "stage-prepare-a", 2); r.Outcome == pkgaudio.OutcomeRefused {
			t.Fatalf("attempt %d: stage prepare a: %+v", attempt, r)
		}

		if r := m.Apply(ctx, show, "show-apply-a", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(refA)}); r.Outcome == pkgaudio.OutcomeRefused {
			t.Fatalf("attempt %d: show apply a: %+v", attempt, r)
		}
		if r := m.Promote(ctx, staging, show, "show-start-a", 2); r.Outcome != pkgaudio.OutcomeStarted {
			t.Fatalf("attempt %d: promote a onto show: %+v, want started", attempt, r)
		}

		// The prepare-ahead: staging is restaged for a different cue's
		// media before the promoted show session is ever stopped.
		refB := writeTestAsset(t, m.assetDir, "song-b.wav", "asset-b", []byte("song b content"))
		if r := m.Apply(ctx, staging, "stage-apply-b", 3, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(refB)}); r.Outcome == pkgaudio.OutcomeRefused {
			t.Fatalf("attempt %d: stage apply b: %+v", attempt, r)
		}
		if r := m.Prepare(ctx, staging, "stage-prepare-b", 4); r.Outcome == pkgaudio.OutcomeRefused {
			t.Fatalf("attempt %d: stage prepare b: %+v", attempt, r)
		}

		results, _ := m.SilenceAll(ctx)
		if len(results) != 2 {
			t.Fatalf("attempt %d: SilenceAll returned %d results, want 2", attempt, len(results))
		}

		for _, id := range []pkgaudio.SessionID{staging, show} {
			s, ok := m.get(id)
			if !ok {
				t.Fatalf("attempt %d: session %s missing after SilenceAll", attempt, id)
			}
			s.mu.Lock()
			state := s.state
			s.mu.Unlock()
			if state != pkgaudio.StateStopped {
				t.Fatalf("attempt %d: session %s state after SilenceAll = %q, want Stopped (a song staged behind an unreleased duplicate handle keeps playing)", attempt, id, state)
			}
		}

		live, err := m.engine.LiveHandles(ctx)
		if err != nil {
			t.Fatalf("attempt %d: LiveHandles: %v", attempt, err)
		}
		if len(live) != 0 {
			t.Fatalf("attempt %d: engine still holds %d live branch(es) after SilenceAll: %v", attempt, len(live), live)
		}
	}
}

// TestStopResolvesStoppedWhenTheEngineAlreadyHoldsNoSuchHandle proves
// [Manager.Stop]'s handling of the exact failure mode the rehearsal
// system hit: an Engine.Stop that reports the handle is not loaded (the
// engine already holds nothing under that name) must resolve the session
// exactly as a successful stop, StateStopped with handleLoaded false, not
// strand it in StateStopping, and a second Stop on it must then succeed
// too, rather than repeating the identical failure forever.
func TestStopResolvesStoppedWhenTheEngineAlreadyHoldsNoSuchHandle(t *testing.T) {
	c := newClock(time.Now())
	m := newAvailableTestManager(t, c)
	ctx := context.Background()

	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	if r := m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply: %+v", r)
	}
	if r := m.Start(ctx, id, "inv-start", 2); r.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("start: %+v, want started", r)
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session not created")
	}
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()

	// Simulate the engine having already forgotten this handle behind the
	// session's own back (an overwrite, an orphan sweep, a race with
	// another release), exactly the evidence a commanded Stop can meet
	// in production once the engine itself reports it.
	if err := m.engine.Release(ctx, handle); err != nil {
		t.Fatalf("releasing the handle directly: %v", err)
	}

	r := m.Stop(ctx, id, "inv-stop-1", 3)
	if r.Outcome != pkgaudio.OutcomeStopped {
		t.Fatalf("Stop when the engine already holds no such handle = %+v, want Stopped", r)
	}
	s.mu.Lock()
	state, handleLoaded := s.state, s.handleLoaded
	s.mu.Unlock()
	if state != pkgaudio.StateStopped {
		t.Fatalf("state after Stop = %q, want Stopped (never stuck in Stopping)", state)
	}
	if handleLoaded {
		t.Fatal("handleLoaded is still true after Stop found no such handle")
	}

	r2 := m.Stop(ctx, id, "inv-stop-2", 4)
	if r2.Outcome != pkgaudio.OutcomeStopped {
		t.Fatalf("second Stop = %+v, want Stopped again, not the same failure repeated", r2)
	}
}

// TestSilenceAllExceptReleasesEveryBranchIncludingOnesNoSessionOwns proves
// [Manager.SilenceAllExcept]'s own final sweep: the excluded session's
// branch survives it, every other session's branch is gone, and so is a
// branch planted directly in the engine that no session ever owned,
// exactly the orphan case the sweep exists for.
func TestSilenceAllExceptReleasesEveryBranchIncludingOnesNoSessionOwns(t *testing.T) {
	c := newClock(time.Now())
	m := newAvailableTestManager(t, c)
	ctx := context.Background()

	const other = pkgaudio.SessionID("other")
	const excluded = pkgaudio.SessionID("excluded")
	otherRef := writeTestAsset(t, m.assetDir, "other.wav", "other-asset", []byte("other"))
	excludedRef := writeTestAsset(t, m.assetDir, "excluded.wav", "excluded-asset", []byte("excluded"))

	startPlaying(t, m, ctx, other, otherRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	startPlaying(t, m, ctx, excluded, excludedRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyMix)

	const planted = EngineHandle("nobody-owns-this/planted")
	plantedRef := writeTestAsset(t, m.assetDir, "planted.wav", "planted-asset", []byte("planted"))
	if _, err := m.engine.Load(ctx, planted, plantedRef, time.Second); err != nil {
		t.Fatalf("planting an orphan handle: %v", err)
	}

	results, released := m.SilenceAllExcept(ctx, excluded)
	if len(results) != 1 || results[0].ID != other {
		t.Fatalf("SilenceAllExcept results = %+v, want exactly one result for %q", results, other)
	}
	if released != 1 {
		t.Fatalf("SilenceAllExcept's final sweep released %d branch(es), want exactly 1 (the planted orphan; other's own branch was already released by its own stop)", released)
	}

	excludedSession, ok := m.get(excluded)
	if !ok {
		t.Fatal("excluded session missing")
	}
	excludedSession.mu.Lock()
	excludedState, excludedHandle := excludedSession.state, excludedSession.handle
	excludedSession.mu.Unlock()
	if excludedState != pkgaudio.StatePlaying {
		t.Fatalf("excluded session state = %q, want Playing (SilenceAllExcept must not touch it)", excludedState)
	}

	live, err := m.engine.LiveHandles(ctx)
	if err != nil {
		t.Fatalf("LiveHandles: %v", err)
	}
	if len(live) != 1 || live[0] != excludedHandle {
		t.Fatalf("live handles after SilenceAllExcept = %v, want exactly the excluded session's own handle %q", live, excludedHandle)
	}
}

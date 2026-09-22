package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// releaseAllWedgeEngine wedges every ReleaseAll call open until the test
// releases it, so a test can prove what does or does not run while
// [Manager.releaseEveryEngineBranchExceptSessions] is inside its own
// engine sweep, holding every excluded session's lock.
type releaseAllWedgeEngine struct {
	*FakeEngine
	entered chan struct{}
	proceed chan struct{}
}

func (e *releaseAllWedgeEngine) ReleaseAll(ctx context.Context, except ...EngineHandle) ([]EngineHandle, error) {
	close(e.entered)
	<-e.proceed
	return e.FakeEngine.ReleaseAll(ctx, except...)
}

// TestSilenceAllExceptDoesNotRaceAnExcludedSessionAcquiringItsHandle
// proves the fix for the race [Manager.handlesOwnedBy]'s old two-step
// snapshot-then-sweep left open: an excluded session with no handle
// loaded yet, starting exactly while SilenceAllExcept's final sweep is
// in progress, cannot land in a window this sweep never learns to
// except. The excluded session's own Start blocks on its session lock,
// held for the whole sweep (see
// [Manager.releaseEveryEngineBranchExceptSessions]), so it can only
// load strictly after the sweep, never during it -- unlike the old
// code, which held no lock across the gap between reading the excluded
// handles and actually sweeping the engine.
func TestSilenceAllExceptDoesNotRaceAnExcludedSessionAcquiringItsHandle(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	inner := NewFakeEngine(c.now)
	engine := &releaseAllWedgeEngine{FakeEngine: inner, entered: make(chan struct{}), proceed: make(chan struct{})}
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	const excluded = pkgaudio.SessionID("excluded")
	excludedRef := writeTestAsset(t, m.assetDir, "excluded.wav", "excluded-asset", []byte("excluded"))

	// excluded exists (so [Manager.releaseEveryEngineBranchExceptSessions]
	// has a session to lock) but has no handle loaded yet -- the
	// precondition item 2's own spec names.
	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleAnnouncement),
		Media:      pkgaudio.SetField(excludedRef),
		MixPolicy:  pkgaudio.SetField(pkgaudio.MixPolicyMix),
	}
	if r := m.Apply(ctx, excluded, "excluded-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply excluded: unexpectedly refused: %+v", r)
	}
	s, ok := m.get(excluded)
	if !ok {
		t.Fatal("excluded session was not created by Apply")
	}
	s.mu.Lock()
	handleLoadedBefore := s.handleLoaded
	s.mu.Unlock()
	if handleLoadedBefore {
		t.Fatal("precondition: excluded must have no handle loaded before the race")
	}

	type silenceResult struct {
		outcomes    []SessionSilenceOutcome
		released    int
		sweepReason string
	}
	silenceDone := make(chan silenceResult, 1)
	go func() {
		outcomes, released, sweepReason := m.SilenceAllExcept(ctx, excluded)
		silenceDone <- silenceResult{outcomes, released, sweepReason}
	}()

	select {
	case <-engine.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("SilenceAllExcept never reached its final engine sweep")
	}

	startDone := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		startDone <- m.Start(ctx, excluded, "excluded-start", 2)
	}()

	select {
	case out := <-startDone:
		t.Fatalf("excluded session's Start completed (%+v) while SilenceAllExcept's sweep was still holding its lock -- the race is not closed", out)
	case <-time.After(150 * time.Millisecond):
		// Expected: Start is blocked behind the sweep's own session lock.
	}

	close(engine.proceed)

	select {
	case res := <-silenceDone:
		if res.sweepReason != "" {
			t.Fatalf("SilenceAllExcept sweep reported %q, want a clean sweep", res.sweepReason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SilenceAllExcept never returned after its sweep was unblocked")
	}

	select {
	case out := <-startDone:
		if out.Outcome == pkgaudio.OutcomeRefused {
			t.Fatalf("excluded session's Start (after the sweep) was refused: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("excluded session's Start never completed after the sweep released its lock")
	}

	s.mu.Lock()
	state, handleLoaded := s.state, s.handleLoaded
	s.mu.Unlock()
	if state != pkgaudio.StatePlaying || !handleLoaded {
		t.Fatalf("excluded session state=%q handleLoaded=%v after the race, want Playing with a loaded handle: "+
			"SilenceAllExcept's sweep released a handle it raced the excluded session for", state, handleLoaded)
	}
}

// TestSilenceAllExceptSurvivesTheExcludedSessionsStagedHandle proves the
// exception SilenceAllExcept computes for an excluded session covers its
// STAGED successor handle too, not only its currently loaded one: the
// handle a playlist stages ahead of its own item boundary (see
// itemschedule.go) must survive an excluded session's own sweep exactly
// as its loaded handle does. Missing before item 2's fix replaced
// [Manager.handlesOwnedBy] with a locked, by-session sweep.
func TestSilenceAllExceptSurvivesTheExcludedSessionsStagedHandle(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	const excluded = pkgaudio.SessionID("excluded")
	ref := writeTestAsset(t, m.assetDir, "excluded.wav", "excluded-asset", []byte("excluded"))
	nextRef := writeTestAsset(t, m.assetDir, "next.wav", "next-asset", []byte("next"))
	startPlaying(t, m, ctx, excluded, ref, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyMix)

	const stagedHandle = EngineHandle("excluded/stage/99")
	if _, err := m.engine.Load(ctx, stagedHandle, nextRef, 2*time.Second); err != nil {
		t.Fatalf("load staged handle: %v", err)
	}
	s, ok := m.get(excluded)
	if !ok {
		t.Fatal("excluded session missing")
	}
	s.mu.Lock()
	s.stage = &itemStage{index: 1, handle: stagedHandle, ready: true}
	s.mu.Unlock()

	if _, released, sweepReason := m.SilenceAllExcept(ctx, excluded); released != 0 || sweepReason != "" {
		t.Fatalf("SilenceAllExcept(except excluded) released=%d sweepReason=%q, want released=0 and a clean sweep "+
			"(both the excluded session's loaded and staged handles must survive)", released, sweepReason)
	}

	if _, err := m.engine.Observe(ctx, stagedHandle); err != nil {
		t.Fatalf("observe staged handle after SilenceAllExcept: %v (it must still exist, excepted alongside the loaded handle)", err)
	}
	s.mu.Lock()
	loadedHandle, handleLoaded := s.handle, s.handleLoaded
	s.mu.Unlock()
	if !handleLoaded {
		t.Fatal("excluded session's loaded handle was released; want it excepted")
	}
	if _, err := m.engine.Observe(ctx, loadedHandle); err != nil {
		t.Fatalf("observe excluded session's loaded handle after SilenceAllExcept: %v", err)
	}
}

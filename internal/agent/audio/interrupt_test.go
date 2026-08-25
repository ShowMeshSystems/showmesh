package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// mutation target: Manager.Start's interrupt trigger and
// interruptOneLocked's suspend. Interrupt is the strongest of the three
// mix policies: unlike mix (no effect on other sessions) and duck (a
// gain reduction, session stays Playing), a lower-priority session must
// actually stop — real Engine.Pause, state Paused — not merely quiet
// down.
func TestInterruptSuspendsLowerPriorityAndResumesOnStop(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)

	bg, _ := m.get("bg")
	bg.mu.Lock()
	state := bg.state
	_, interruptedByAnn := bg.interruptedByAll["ann"]
	bg.mu.Unlock()
	if state != pkgaudio.StatePaused || !interruptedByAnn {
		t.Fatalf("bg after ann started: state=%q interruptedByAll=%v, want paused and interrupted by ann", state, bg.interruptedByAll)
	}

	m.Stop(ctx, "ann", "inv-ann-stop", 3)

	bg.mu.Lock()
	defer bg.mu.Unlock()
	if bg.state != pkgaudio.StatePlaying {
		t.Fatalf("bg after ann stopped: state=%q, want playing (resumed)", bg.state)
	}
	if len(bg.interruptedByAll) != 0 {
		t.Fatalf("bg.interruptedByAll = %v after ann stopped, want empty", bg.interruptedByAll)
	}
}

// mix and duck must both leave the target Playing — only interrupt
// actually suspends it. Mutation target: a mix-up between the three
// policies' effect on the OTHER session's state.
func TestMixAndDuckNeverSuspendTheTarget(t *testing.T) {
	for _, policy := range []pkgaudio.MixPolicy{pkgaudio.MixPolicyMix, pkgaudio.MixPolicyDuck} {
		t.Run(string(policy), func(t *testing.T) {
			c := newClock(time.Now())
			m := newTestManager(t, c)
			ctx := context.Background()

			bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
			startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

			annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
			startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, policy)

			bg, _ := m.get("bg")
			bg.mu.Lock()
			defer bg.mu.Unlock()
			if bg.state != pkgaudio.StatePlaying {
				t.Fatalf("bg under mix policy %q: state=%q, want still playing", policy, bg.state)
			}
			if len(bg.interruptedByAll) != 0 {
				t.Fatalf("bg under mix policy %q: interruptedByAll=%v, want empty", policy, bg.interruptedByAll)
			}
		})
	}
}

// mutation target: interruptLowerPriority's priority comparison. A
// background session with an interrupt policy must NOT interrupt an
// active show session — flipping the comparison direction makes this
// test's "show stays playing" assertion fail, the same shape as duck's
// own TestDuckOnlyAffectsLowerPriorityRoles.
func TestInterruptOnlyAffectsLowerPriorityRoles(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	showRef := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", showRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyInterrupt)

	show, _ := m.get("show")
	show.mu.Lock()
	defer show.mu.Unlock()
	if show.state != pkgaudio.StatePlaying {
		t.Fatalf("show session was interrupted by a lower-priority background session: state=%q interruptedByAll=%v", show.state, show.interruptedByAll)
	}
}

// mutation target: removeInterrupterLocked's Resume-policy branch and its
// use of the frozen bookmark position. A single-media session (no
// playlist, so interruptResumePolicyLocked defaults to Resume) must come
// back from EXACTLY the position it was suspended at, never 0 and never
// extrapolated through the interruption — proving both halves of the
// resume-policy requirement against a deterministic injected clock.
func TestInterruptResumePolicyResumeContinuesFromExactPosition(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	c.advance(500 * time.Millisecond)

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)

	bg, _ := m.get("bg")
	bg.mu.Lock()
	if bg.state != pkgaudio.StatePaused || bg.bookmark == nil || bg.bookmark.Position != 500*time.Millisecond {
		bg.mu.Unlock()
		t.Fatalf("bg while interrupted: state=%q bookmark=%v, want paused with a 500ms bookmark", bg.state, bg.bookmark)
	}
	bg.mu.Unlock()

	// No clock advance here: the interruption itself takes no time in
	// this deterministic clock, so the resumed position must be exactly
	// where it was suspended, not extrapolated through any gap.
	m.Stop(ctx, "ann", "inv-ann-stop", 3)

	bg.mu.Lock()
	if bg.state != pkgaudio.StatePlaying {
		t.Fatalf("bg after ann stopped: state=%q, want playing", bg.state)
	}
	snap := bg.snapshotLocked(ctx)
	bg.mu.Unlock()
	if !snap.PositionKnown || snap.Position != 500*time.Millisecond {
		t.Fatalf("bg position immediately after resume = %v (known=%v), want exactly 500ms", snap.Position, snap.PositionKnown)
	}

	// Genuinely resumed, not stuck: playback must still be advancing.
	c.advance(300 * time.Millisecond)
	bg.mu.Lock()
	snap = bg.snapshotLocked(ctx)
	bg.mu.Unlock()
	if !snap.PositionKnown || snap.Position != 800*time.Millisecond {
		t.Fatalf("bg position after further playback = %v (known=%v), want 800ms", snap.Position, snap.PositionKnown)
	}
}

// mutation target: removeInterrupterLocked's Restart-policy branch.
// A playlist session with Resume=Restart must come back at 0 on the SAME
// current item, never from the position it was suspended at.
func TestInterruptResumePolicyRestartStartsCurrentItemOver(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	playlist := twoItemPlaylist(t, dir) // Resume: Restart
	m.Apply(ctx, "show", "show-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Playlist:   pkgaudio.SetField(playlist),
	})
	if r := m.Start(ctx, "show", "show-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show start: unexpectedly refused: %+v", r)
	}

	c.advance(700 * time.Millisecond)

	annRef := writeTestAsset(t, dir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)

	show, _ := m.get("show")
	show.mu.Lock()
	if show.state != pkgaudio.StatePaused || show.currentItemID != "item-a" {
		show.mu.Unlock()
		t.Fatalf("show while interrupted: state=%q item=%q, want paused on item-a", show.state, show.currentItemID)
	}
	show.mu.Unlock()

	m.Stop(ctx, "ann", "inv-ann-stop", 3)

	show.mu.Lock()
	defer show.mu.Unlock()
	if show.state != pkgaudio.StatePlaying || show.currentItemID != "item-a" {
		t.Fatalf("show after ann stopped: state=%q item=%q, want playing, still on item-a (restarted, not advanced)", show.state, show.currentItemID)
	}
	snap := show.snapshotLocked(ctx)
	if !snap.PositionKnown || snap.Position != 0 {
		t.Fatalf("show position after restart = %v (known=%v), want exactly 0", snap.Position, snap.PositionKnown)
	}
}

// mutation target: Manager.Apply's "unsupported" refusal must not have
// collaterally started refusing interrupt too; and Manager.Resume must
// refuse an operator resume while a session is suspended by an
// interrupting announcement, so an operator command can never race the
// system's own eventual un-interrupt.
func TestResumeRefusesASessionSuspendedByInterrupt(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)

	r := m.Resume(ctx, "bg", "inv-operator-resume", 3)
	if r.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("operator resume of an interrupted session: outcome=%+v, want refused", r)
	}
}

// mutation target: removeInterrupterLocked's membership guard — the
// entire exactly-once mechanism, mirroring
// TestDuckRestoreExactlyOnce_CrashAfterDuckerStopped. Simulates a crash
// between "ann's stop persisted" and "bg's un-interrupt persisted": a
// fresh Manager restoring from the same store must resume bg exactly
// once.
func TestInterruptRestoreExactlyOnce_CrashAfterInterrupterStopped(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)

	// Simulate the crash: persist ann as Stopped WITHOUT going through
	// Manager.Stop (which would itself call restoreInterrupted) — exactly
	// the state a crash between "ann's stop persisted" and "bg's
	// un-interrupt persisted" leaves on disk.
	ann, _ := m.get("ann")
	ann.mu.Lock()
	ann.state = pkgaudio.StateStopped
	_ = ann.persistLocked()
	ann.mu.Unlock()

	bg, _ := m.get("bg")
	bg.mu.Lock()
	if _, ok := bg.interruptedByAll["ann"]; !ok {
		bg.mu.Unlock()
		t.Fatalf("precondition: bg should still be interrupted pre-restart")
	}
	bg.mu.Unlock()

	m2 := newTestManagerInDir(dir, c)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	bg2, ok := m2.get("bg")
	if !ok {
		t.Fatal("bg session was not restored")
	}
	bg2.mu.Lock()
	defer bg2.mu.Unlock()
	if len(bg2.interruptedByAll) != 0 {
		t.Fatalf("bg2.interruptedByAll = %v after restart, want restored (empty) since ann is gone", bg2.interruptedByAll)
	}
	if bg2.state != pkgaudio.StatePlaying {
		t.Fatalf("bg2.state = %q after restart, want playing (resumed since ann never survived)", bg2.state)
	}
}

// The other side of the same boundary: a crash while the interrupting
// session is STILL legitimately playing must leave the interrupted
// session suspended — resuming here would be the premature-resume
// failure this guarantee exists to prevent.
func TestInterruptRestoreExactlyOnce_CrashWhileInterrupterStillPlaying(t *testing.T) {
	dir := t.TempDir()
	c := newClock(time.Now())
	m := newTestManagerInDir(dir, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)

	// No mutation here: both sessions' persisted records reflect exactly
	// what a crash right now would leave — ann Playing, bg interrupted.

	m2 := newTestManagerInDir(dir, c)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	bg2, ok := m2.get("bg")
	if !ok {
		t.Fatal("bg session was not restored")
	}
	bg2.mu.Lock()
	_, interruptedByAnn := bg2.interruptedByAll["ann"]
	state := bg2.state
	bg2.mu.Unlock()
	if !interruptedByAnn || state != pkgaudio.StatePaused {
		t.Fatalf("bg2 after restart: state=%q interruptedByAll=%v, want still paused and interrupted by ann (ann is still playing)", state, bg2.interruptedByAll)
	}

	// Now stop ann for real, through the live path, and confirm bg
	// resumes exactly once from here too.
	m2.Stop(ctx, "ann", "inv-ann-stop", 3)

	bg2.mu.Lock()
	defer bg2.mu.Unlock()
	if len(bg2.interruptedByAll) != 0 {
		t.Fatalf("bg2.interruptedByAll = %v after ann finally stopped, want empty", bg2.interruptedByAll)
	}
	if bg2.state != pkgaudio.StatePlaying {
		t.Fatalf("bg2.state = %q after ann finally stopped, want playing", bg2.state)
	}
}

// TestInterruptedSessionResumeFailureStaysRecoverable verifies that when
// the engine.Resume call [Manager.removeInterrupterLocked] makes on an
// interrupting announcement's stop fails, the interrupted session stays
// Paused, not Failed, with its handle untouched — this internal
// reconciliation call never released it, so there is no stale handle to
// guard against — so an operator Resume can still recover it instead of
// the session being stuck for the rest of the night.
func TestInterruptedSessionResumeFailureStaysRecoverable(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)

	bg, ok := m.get("bg")
	if !ok {
		t.Fatal("bg session was not created")
	}
	bg.mu.Lock()
	if bg.state != pkgaudio.StatePaused {
		state := bg.state
		bg.mu.Unlock()
		t.Fatalf("precondition: bg should be paused while interrupted, got %q", state)
	}
	handle := bg.handle
	bg.mu.Unlock()

	fake, ok := m.engine.(*FakeEngine)
	if !ok {
		t.Fatalf("test manager's engine is %T, want *FakeEngine", m.engine)
	}
	fake.InjectFailure(handle, pkgaudio.ErrEnginePipelineCrash)

	if r := m.Stop(ctx, "ann", "inv-ann-stop", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stop ann: unexpectedly refused: %+v", r)
	}

	bg.mu.Lock()
	state := bg.state
	_, stillInterrupted := bg.interruptedByAll["ann"]
	bg.mu.Unlock()
	if stillInterrupted {
		t.Fatal("bg is still recorded as interrupted by ann after ann stopped")
	}
	if state != pkgaudio.StatePaused {
		t.Fatalf("bg state after a failed automatic resume = %q, want still Paused (recoverable), not Failed", state)
	}

	// Recovery: nothing is armed to fail this Resume, so the operator can
	// still bring bg back from exactly here.
	r := m.Resume(ctx, "bg", "inv-bg-resume", 4)
	if r.Outcome == pkgaudio.OutcomeRefused || r.Outcome == pkgaudio.OutcomeFailed {
		t.Fatalf("recovery Resume after the automatic one failed = %+v, want it to succeed", r)
	}
	bg.mu.Lock()
	finalState := bg.state
	bg.mu.Unlock()
	if finalState != pkgaudio.StatePlaying {
		t.Fatalf("bg state after recovery Resume = %q, want Playing", finalState)
	}
}

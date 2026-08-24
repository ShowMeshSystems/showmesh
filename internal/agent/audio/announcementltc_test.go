package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file proves, at this package's own level, the two mechanisms
// TRACK-H-cues-and-playlists.md section H5 says already exist and must
// NOT be reimplemented: the "LTC follows the show session" rule under an
// announcement interrupt, and a pinned PlaylistRef
// advancing (and repeating) on its own with no coordinator involvement.
// Neither combination (interrupt+LTC together; repeat="playlist" wrap) had
// a direct test before this seam — interrupt_test.go and ltclifecycle_test.go
// each prove their own half in isolation.

// TestInterruptStopsShowLTCAndRestartsOnResume proves the "LTC follows
// the show session" ruling end to end: an announcement session with
// MixPolicyInterrupt suspends a
// Playing show session AND stops its LTC run (interruptOneLocked calls
// stopLTCLocked); once the announcement leaves Playing, the show session
// resumes AND its LTC restarts (removeInterrupterLocked calls
// startLTCLocked on both the resume and the restart path). TRACK-H-cues-and-playlists.md section H5's
// own instruction is to prove this, not reimplement it — this test adds
// no new production code.
func TestInterruptStopsShowLTCAndRestartsOnResume(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	showRef := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", showRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	assertLTCRequestedAt(t, m, "00:00:00:00")

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)

	show, ok := m.get("show")
	if !ok {
		t.Fatal("show session missing")
	}
	show.mu.Lock()
	state := show.state
	show.mu.Unlock()
	if state != pkgaudio.StatePaused {
		t.Fatalf("show session state after announcement interrupt = %q, want paused", state)
	}
	if _, requested := requestedLTC(t, m); requested {
		t.Fatal("show session's LTC is still requested after an interrupting announcement started; it must stop")
	}

	// The announcement ends (a commanded Stop stands in for its own
	// natural completion — restoreInterrupted runs identically either
	// way, per restore.go's watchTick).
	m.Stop(ctx, "ann", "inv-ann-stop", 3)

	show.mu.Lock()
	state = show.state
	show.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("show session state after announcement ended = %q, want playing (resumed)", state)
	}
	if _, requested := requestedLTC(t, m); !requested {
		t.Fatal("show session's LTC did not restart after the interrupting announcement ended")
	}
}

// showmeshAudioPlaylistRef builds a two-item playlist shaped exactly like
// the showmesh-audio runner's own coordinator-side resolver
// (internal/coordinator/assetsync's ResolveShowmeshAudioPlaylistRef)
// produces: OwnerKind "show.playlist", real assets, and repeat controlled
// by the caller.
func showmeshAudioPlaylistRef(t *testing.T, dir string, repeat pkgaudio.RepeatMode) pkgaudio.PlaylistRef {
	t.Helper()
	a := writeTestAsset(t, dir, "bed-a.wav", "asset-bed-a", []byte("aaa"))
	b := writeTestAsset(t, dir, "bed-b.wav", "asset-bed-b", []byte("bbb"))
	return pkgaudio.PlaylistRef{
		OwnerKind: "show.playlist", OwnerID: "bg-playlist", OwnerRevision: 1,
		Repeat: repeat, Resume: pkgaudio.ResumePolicyRestart,
		RequestedTransition: pkgaudio.ItemTransitionSequential,
		Items: []pkgaudio.PlaylistItem{
			{ItemID: "item-a", Index: 0, Media: a},
			{ItemID: "item-b", Index: 1, Media: b},
		},
	}
}

// TestShowmeshAudioPlaylistAdvancesAndHonorsRepeatPlaylist proves build
// item 1's core claim: ONE Apply of a pkgaudio.PlaylistRef against the
// background session (SourceRoleBackground, mirroring
// cueactivation.BackgroundSessionID) is enough for the node's own
// Manager.RunWatcher (watchTick, called here directly the same way
// advance_test.go's existing RepeatNone coverage does) to advance through
// every entry AND wrap back to item-a under repeat "playlist" — the
// "all" -> RepeatPlaylist mapping TRACK-H-cues-and-playlists.md section H5 build item 1 specifies —
// with no second Apply and no coordinator involvement.
func TestShowmeshAudioPlaylistAdvancesAndHonorsRepeatPlaylist(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("cue-activation:background")

	playlist := showmeshAudioPlaylistRef(t, m.assetDir, pkgaudio.RepeatPlaylist)
	if r := m.Apply(ctx, id, "bg-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground),
		Playlist:   pkgaudio.SetField(playlist),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply background playlist: unexpectedly refused: %+v", r)
	}
	// Prepare, then Start — the SAME two-step sequence the real
	// production path dispatches (internal/coordinator/api/
	// showmeshaudiodispatch.go's applyShowmeshAudioPlaylistIfAny,
	// TRACK-H-cues-and-playlists.md section H5 build item 1's own fix),
	// not Apply-then-Start alone: a coordinator that only ever dispatched
	// Apply is exactly the defect that fix closes, so this precondition
	// must not construct a state the production path cannot produce.
	if r := m.Prepare(ctx, id, "bg-prepare", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("prepare background playlist: unexpectedly refused: %+v", r)
	}
	if r := m.Start(ctx, id, "bg-start", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start background playlist: unexpectedly refused: %+v", r)
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatal("background session was not created")
	}
	s.mu.Lock()
	if s.currentItemID != "item-a" {
		s.mu.Unlock()
		t.Fatalf("current item = %q, want item-a", s.currentItemID)
	}
	s.mu.Unlock()

	// item-a's staticDecoder duration is 2s; run it out and let the
	// watcher advance to item-b entirely on its own.
	c.advance(3 * time.Second)
	m.watchTick(ctx)
	s.mu.Lock()
	if s.currentItemID != "item-b" || s.state != pkgaudio.StatePlaying {
		item, state := s.currentItemID, s.state
		s.mu.Unlock()
		t.Fatalf("after item-a completed: item=%q state=%q, want item-b playing", item, state)
	}
	s.mu.Unlock()

	// item-b completes too: with RepeatPlaylist, the watcher wraps back
	// to item-a — never Completed, which RepeatNone's own existing
	// coverage (advance_test.go) already proves for the non-repeating
	// case.
	c.advance(3 * time.Second)
	m.watchTick(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentItemID != "item-a" {
		t.Fatalf("current item after repeat-playlist wrap = %q, want item-a", s.currentItemID)
	}
	if s.state != pkgaudio.StatePlaying {
		t.Fatalf("state after repeat-playlist wrap = %q, want playing", s.state)
	}
}

// TestShowmeshAudioPlaylistEmitsNoLTCUnlessDeclared proves build item 1's
// own "it emits no LTC unless a Cue explicitly declares LTC": a
// background-role session never starts LTC by construction
// (isShowSessionLocked, ltclifecycle.go), even with LTC settings fully
// configured and even though this Apply carries no LTCStartOffset at all
// — the showmesh-audio runner never sends one (TRACK-H-cues-and-playlists.md section H5 build item 1).
func TestShowmeshAudioPlaylistEmitsNoLTCUnlessDeclared(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()
	const id = pkgaudio.SessionID("cue-activation:background")

	playlist := showmeshAudioPlaylistRef(t, m.assetDir, pkgaudio.RepeatNone)
	if r := m.Apply(ctx, id, "bg-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground),
		Playlist:   pkgaudio.SetField(playlist),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply background playlist: unexpectedly refused: %+v", r)
	}
	if r := m.Prepare(ctx, id, "bg-prepare", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("prepare background playlist: unexpectedly refused: %+v", r)
	}
	if r := m.Start(ctx, id, "bg-start", 3); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start background playlist: unexpectedly refused: %+v", r)
	}

	if _, requested := requestedLTC(t, m); requested {
		t.Fatal("a background-role session requested an LTC run; only a show-role session may ever start one")
	}
}

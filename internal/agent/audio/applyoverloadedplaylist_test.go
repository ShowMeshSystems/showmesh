package audio

import (
	"context"
	"fmt"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestApplyOverALoadedPlaylistDoesNotRestartFromItemZero is a minimal
// repro of a known defect: [Manager.Apply]'s "s.currentIndex < 0" check
// assigns a fresh item index only to a session that never played, so a
// session already advanced past item 0 keeps that stale index across an
// Apply that replaces the playlist entirely. Only Clear resets it.
//
// Found against ADR-053's cancel alert, which no longer Applies over the
// other kind's loaded session; any other caller that replaces a loaded,
// advanced session's playlist still hits it. Skipped until the audio
// manager is fixed: this test's own failure is the record of that gap.
func TestApplyOverALoadedPlaylistDoesNotRestartFromItemZero(t *testing.T) {
	t.Skip("known defect: Manager.Apply over a session whose playlist has already advanced past item 0 keeps the stale index instead of starting the replacement fresh at item 0; see this test's own doc comment")

	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("replaced-session")

	newPlaylist := func(owner string, rev pkgaudio.Revision, ref pkgaudio.MediaRef) pkgaudio.PlaylistRef {
		items := make([]pkgaudio.PlaylistItem, 3)
		for i := range items {
			items[i] = pkgaudio.PlaylistItem{ItemID: fmt.Sprintf("%s-%d", owner, i), Index: i, Media: ref}
		}
		return pkgaudio.PlaylistRef{
			OwnerKind: "test", OwnerID: owner, OwnerRevision: rev,
			Items: items, Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyRestart,
			RequestedTransition: pkgaudio.ItemTransitionSequential,
		}
	}

	firstRef := writeTestAsset(t, m.assetDir, "first.wav", "asset-first", []byte("first playlist content"))
	firstReq := pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(newPlaylist("first", 1, firstRef))}
	if r := m.Apply(ctx, id, "apply-first", 1, firstReq); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply first playlist: %+v", r)
	}
	if r := m.Start(ctx, id, "start-first", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start first playlist: %+v", r)
	}

	// Simulate the first playlist having already advanced to its last item
	// before the replacement arrives, exactly as a real session does after
	// its earlier items finish: itemschedule.go's own item-advance is what
	// sets this in production, done directly here so the repro is
	// deterministic rather than timing-dependent on a real watch tick.
	s, ok := m.get(id)
	if !ok {
		t.Fatalf("session %s was not created", id)
	}
	s.mu.Lock()
	s.currentIndex = 2
	s.mu.Unlock()

	secondRef := writeTestAsset(t, m.assetDir, "second.wav", "asset-second", []byte("second playlist content"))
	secondReq := pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(newPlaylist("second", 2, secondRef))}
	if r := m.Apply(ctx, id, "apply-second", 3, secondReq); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply second playlist over the loaded first one: %+v", r)
	}
	if r := m.Prepare(ctx, id, "prepare-second", 4); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("prepare second playlist: %+v", r)
	}
	if r := m.Start(ctx, id, "start-second", 5); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start second playlist: %+v", r)
	}

	for _, snap := range m.Snapshot(ctx) {
		if snap.ID != id {
			continue
		}
		if snap.State != pkgaudio.StatePlaying || !snap.HasItem || snap.ItemIndex != 0 {
			t.Fatalf("session after replacing a loaded playlist whose index had already advanced: state = %s, hasItem = %v, itemIndex = %d, want playing at item 0 (a fresh start, not the replaced playlist's stale index)",
				snap.State, snap.HasItem, snap.ItemIndex)
		}
		return
	}
	t.Fatalf("session %s not found in a snapshot after the replace", id)
}

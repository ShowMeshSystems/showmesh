package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// This file covers TRACK-H-cues-and-playlists.md section H5's build item
// 2: an announcement Cue is directly activatable without being a Playlist
// entry, runs in [cueactivation.AnnouncementSessionID] as
// [pkgaudio.SourceRoleAnnouncement], and applies its declared duck/mix/
// interrupt [pkgaudio.MixPolicy] to whatever background session is
// already running — never altering FPP, rendering, or LTC unless its own
// Cue separately declares that output.

// announcementActivation is [testActivation] with Playlist/PlaylistRevision/
// EntryID cleared: H3 spec section 5's own "absent for a directly
// activated announcement" rule (already documented on
// [cueactivation.Activation.EntryID]) — an announcement Cue is activatable
// WITHOUT being a Playlist entry.
func announcementActivation(activationID, cueID string, cueRev int64, show string, generation int64, catalogRev string) cueactivation.Activation {
	act := testActivation(activationID, cueID, cueRev, show, generation, catalogRev, 0)
	act.Playlist = ""
	act.PlaylistRevision = 0
	act.EntryID = ""
	return act
}

// findSnapshot returns id's own [audio.SessionSnapshot], failing the test
// if no session with that id exists.
func findSnapshot(t *testing.T, mgr *audio.Manager, id pkgaudio.SessionID) audio.SessionSnapshot {
	t.Helper()
	for _, snap := range mgr.Snapshot(context.Background()) {
		if snap.ID == id {
			return snap
		}
	}
	t.Fatalf("no session snapshot for id %q", id)
	return audio.SessionSnapshot{}
}

// TestAnnouncementCueAppliesDuckMixInterruptToRunningBackgroundSession
// proves TRACK-H-cues-and-playlists.md section H5 build item 2 end to end: a directly-activated
// announcement Cue (no EntryID, not a Playlist entry) runs in
// [cueactivation.AnnouncementSessionID] and its declared policy governs a
// concurrently-running background session exactly as
// internal/agent/audio's own duck/mix/interrupt machinery (already proven
// at the pkg/audio level by mix_test.go/interrupt_test.go) predicts —
// this test's own job is to prove cueactivationaudio.go's activateAudio
// actually WIRES a Cue's outputs.announcement.policy into that machinery,
// which it did not before this seam (it hardcoded SourceRoleShow and
// never set MixPolicy at all).
func TestAnnouncementCueAppliesDuckMixInterruptToRunningBackgroundSession(t *testing.T) {
	for _, tc := range []struct {
		policy     string
		wantDucked bool
		wantPaused bool
	}{
		{policy: "duck", wantDucked: true, wantPaused: false},
		{policy: "mix", wantDucked: false, wantPaused: false},
		{policy: "interrupt", wantDucked: false, wantPaused: true},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			dir := t.TempDir()
			clock := &fakeClock{t: time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC)}
			mgr, fake := newTestAudioManager(t, dir, clock)

			// A running background session — the showmesh-audio runner's
			// own session id (TRACK-H-cues-and-playlists.md section H5 ruling 3) — simulating build item
			// 1's own Apply already having landed.
			bgHash := writeAssetFixture(t, dir, "bg.wav", []byte("pretend background bed bytes"))
			bgID := pkgaudio.SessionID(cueactivation.BackgroundSessionID)
			ctx := context.Background()
			if outcome := mgr.Apply(ctx, bgID, "bg-apply", 1, pkgaudio.ApplyRequest{
				SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground),
				Media: pkgaudio.SetField(pkgaudio.MediaRef{
					AssetID: "bg-asset", ContentHash: bgHash, RuntimeFilename: "bg.wav",
				}),
			}); audioOutcomeFailed(outcome) {
				t.Fatalf("background apply: %+v", outcome)
			}
			// Prepare, then Start — the SAME two-step sequence the real
			// production path dispatches (internal/coordinator/api/
			// showmeshaudiodispatch.go's applyShowmeshAudioPlaylistIfAny,
			// TRACK-H-cues-and-playlists.md section H5 build item 1's own
			// fix), not Apply-then-Start alone: a coordinator that only
			// ever dispatched Apply is exactly the defect that fix closes,
			// so this precondition must not construct a state the
			// production path cannot produce.
			if outcome := mgr.Prepare(ctx, bgID, "bg-prepare", 2); audioOutcomeFailed(outcome) {
				t.Fatalf("background prepare: %+v", outcome)
			}
			if outcome := mgr.Start(ctx, bgID, "bg-start", 3); audioOutcomeFailed(outcome) {
				t.Fatalf("background start: %+v", outcome)
			}
			if snap := findSnapshot(t, mgr, bgID); snap.State != pkgaudio.StatePlaying {
				t.Fatalf("precondition: background session state = %s, want playing", snap.State)
			}

			// The directly-activated announcement Cue: no Playlist entry,
			// declares audio + announcement, deliberately NOT render or
			// ltc — H0.4's own "an announcement must not stop or alter
			// FPP, rendering, or LTC unless the Cue explicitly declares
			// that output".
			annHash := writeAssetFixture(t, dir, "ann.wav", []byte("pretend announcement bytes"))
			catalogStore := heldcatalog.NewFileStore(dir)
			entry := cuecatalog.Entry{
				CueID: "cue-ann-" + tc.policy, CueRevision: 1,
				Outputs: cuecatalog.Outputs{
					Audio:        &cuecatalog.AudioOutput{Asset: "ann-asset", Filename: "ann.wav", AssetHashes: []string{annHash}},
					Announcement: &cuecatalog.AnnouncementOutput{Policy: tc.policy, FadeMillis: 0},
				},
			}
			saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

			// op.render is deliberately nil: build item 2's own "does not
			// stop or alter FPP, rendering, or LTC unless declared" would
			// otherwise fail loudly (cueactivationops.go's activate refuses
			// a declared render output with no render surfaces configured)
			// if this activation ever tried to touch it — proving by
			// construction that it never does, since Outputs.Render is nil
			// on this Cue.
			op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, audioMgr: mgr}
			act := announcementActivation("act-ann-"+tc.policy, "cue-ann-"+tc.policy, 1, "halloween-2026", 3, "rev-a")

			result, err := op.activate(ctx, activationParams(t, act), clock.now)
			if err != nil {
				t.Fatalf("activate: %v", err)
			}
			if !result.Confirmed {
				t.Fatalf("activate did not confirm: %+v", result)
			}

			annID := pkgaudio.SessionID(cueactivation.AnnouncementSessionID)
			annSnap := findSnapshot(t, mgr, annID)
			if annSnap.State != pkgaudio.StatePlaying {
				t.Fatalf("announcement session state = %s, want playing", annSnap.State)
			}
			if !annSnap.HasSourceRole || annSnap.SourceRole != pkgaudio.SourceRoleAnnouncement {
				t.Fatalf("announcement session source role = (%v, %v), want announcement", annSnap.HasSourceRole, annSnap.SourceRole)
			}

			bgSnap := findSnapshot(t, mgr, bgID)
			if bgSnap.Ducked != tc.wantDucked {
				t.Fatalf("policy %s: background ducked = %v, want %v", tc.policy, bgSnap.Ducked, tc.wantDucked)
			}
			gotPaused := bgSnap.State == pkgaudio.StatePaused
			if gotPaused != tc.wantPaused {
				t.Fatalf("policy %s: background paused = %v (state %s), want %v", tc.policy, gotPaused, bgSnap.State, tc.wantPaused)
			}
			if !tc.wantPaused && bgSnap.State != pkgaudio.StatePlaying {
				t.Fatalf("policy %s: background state = %s, want still playing", tc.policy, bgSnap.State)
			}

			// H0.4: the announcement Cue declared no ltc output, and its
			// role is never Show, so no LTC run is ever requested —
			// startLTCLocked's own isShowSessionLocked gate, proven here
			// at this seam's own level rather than re-asserted only at
			// pkg/audio's.
			if _, requested := fake.LastLTCRequest(); requested {
				t.Fatalf("policy %s: an LTC run was requested; the announcement Cue declared no ltc output", tc.policy)
			}
		})
	}
}

// TestAnnouncementCueCoexistsWithBackgroundMusic proves H0.5's own reason
// an announcement Cue and a background Cue never collide at claim
// arbitration: the announcement Cue claims only announcement-session,
// never program-audio-route, so both sessions keep running side by side
// (background merely ducked/mixed, never stopped) — the SessionID split
// (TRACK-H-cues-and-playlists.md section H5 ruling 3) is what makes that true at the node level, mirroring
// [config.DeriveShowCueClaims]'s identical rule at the authoring/catalog
// level (see internal/coordinator/assetsync's own claim-conflict test).
func TestAnnouncementCueCoexistsWithBackgroundMusic(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ctx := context.Background()

	bgHash := writeAssetFixture(t, dir, "bg.wav", []byte("pretend background bed bytes"))
	bgID := pkgaudio.SessionID(cueactivation.BackgroundSessionID)
	if outcome := mgr.Apply(ctx, bgID, "bg-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground),
		Media: pkgaudio.SetField(pkgaudio.MediaRef{
			AssetID: "bg-asset", ContentHash: bgHash, RuntimeFilename: "bg.wav",
		}),
	}); audioOutcomeFailed(outcome) {
		t.Fatalf("background apply: %+v", outcome)
	}
	// Prepare, then Start — see the identical fix and comment in
	// TestAnnouncementCueAppliesDuckMixInterruptToRunningBackgroundSession
	// above.
	if outcome := mgr.Prepare(ctx, bgID, "bg-prepare", 2); audioOutcomeFailed(outcome) {
		t.Fatalf("background prepare: %+v", outcome)
	}
	if outcome := mgr.Start(ctx, bgID, "bg-start", 3); audioOutcomeFailed(outcome) {
		t.Fatalf("background start: %+v", outcome)
	}

	annHash := writeAssetFixture(t, dir, "ann.wav", []byte("pretend announcement bytes"))
	catalogStore := heldcatalog.NewFileStore(dir)
	entry := cuecatalog.Entry{
		CueID: "cue-ann-mix", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			Audio:        &cuecatalog.AudioOutput{Asset: "ann-asset", Filename: "ann.wav", AssetHashes: []string{annHash}},
			Announcement: &cuecatalog.AnnouncementOutput{Policy: "mix", FadeMillis: 0},
		},
	}
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, audioMgr: mgr}
	act := announcementActivation("act-ann-mix", "cue-ann-mix", 1, "halloween-2026", 3, "rev-a")
	result, err := op.activate(ctx, activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("activate did not confirm: %+v", result)
	}

	// Both sessions coexist: neither was refused, and background is still
	// genuinely playing (mix duplicates neither ducks nor interrupts).
	if snaps := mgr.Snapshot(ctx); len(snaps) != 2 {
		t.Fatalf("session count = %d, want 2 (background + announcement coexisting)", len(snaps))
	}
	bgSnap := findSnapshot(t, mgr, bgID)
	if bgSnap.State != pkgaudio.StatePlaying || bgSnap.Ducked {
		t.Fatalf("background session = %+v, want still playing and not ducked", bgSnap)
	}
	annSnap := findSnapshot(t, mgr, pkgaudio.SessionID(cueactivation.AnnouncementSessionID))
	if annSnap.State != pkgaudio.StatePlaying {
		t.Fatalf("announcement session state = %s, want playing", annSnap.State)
	}
}

// TestSecondAnnouncementRefusedWhileFirstIsPlaying proves TRACK-H-cues-
// and-playlists.md section H5 build item 3's own ruling: a second,
// DIFFERENT announcement Cue arriving while the first is still Playing in
// [cueactivation.AnnouncementSessionID] is REFUSED — naming both Cue ids
// and the exact claim string — never superseded. Before this fix,
// activateAudio routed every announcement to the SAME session
// unconditionally, so the second Cue's own Apply tore the first down
// mid-sentence and reported Confirmed.
func TestSecondAnnouncementRefusedWhileFirstIsPlaying(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ctx := context.Background()

	firstHash := writeAssetFixture(t, dir, "first.wav", []byte("pretend first announcement bytes"))
	secondHash := writeAssetFixture(t, dir, "second.wav", []byte("pretend second announcement bytes"))
	catalogStore := heldcatalog.NewFileStore(dir)
	entries := []cuecatalog.Entry{
		{
			CueID: "cue-ann-first", CueRevision: 1,
			Outputs: cuecatalog.Outputs{
				Audio:        &cuecatalog.AudioOutput{Asset: "first-asset", Filename: "first.wav", AssetHashes: []string{firstHash}},
				Announcement: &cuecatalog.AnnouncementOutput{Policy: "mix", FadeMillis: 0},
			},
		},
		{
			CueID: "cue-ann-second", CueRevision: 1,
			Outputs: cuecatalog.Outputs{
				Audio:        &cuecatalog.AudioOutput{Asset: "second-asset", Filename: "second.wav", AssetHashes: []string{secondHash}},
				Announcement: &cuecatalog.AnnouncementOutput{Policy: "mix", FadeMillis: 0},
			},
		},
	}
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", entries)

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, audioMgr: mgr, nodeID: "audio-01"}

	firstAct := announcementActivation("act-ann-first", "cue-ann-first", 1, "halloween-2026", 3, "rev-a")
	firstResult, err := op.activate(ctx, activationParams(t, firstAct), clock.now)
	if err != nil {
		t.Fatalf("first activate: %v", err)
	}
	if !firstResult.Confirmed {
		t.Fatalf("first activate did not confirm: %+v", firstResult)
	}

	secondAct := announcementActivation("act-ann-second", "cue-ann-second", 1, "halloween-2026", 3, "rev-a")
	secondResult, err := op.activate(ctx, activationParams(t, secondAct), clock.now)
	if err != nil {
		t.Fatalf("second activate: %v", err)
	}
	if secondResult.Confirmed {
		t.Fatalf("second activate confirmed: %+v, want refused (H0.5: a second announcement is refused, never superseded)", secondResult)
	}
	value, ok := secondResult.Value.(map[string]any)
	if !ok {
		t.Fatalf("second result.Value = %+v (%T), want a map", secondResult.Value, secondResult.Value)
	}
	reasons, _ := value["reasons"].([]string)
	if len(reasons) != 1 {
		t.Fatalf("second result reasons = %+v, want exactly one refusal reason", reasons)
	}
	reason := reasons[0]
	if !strings.Contains(reason, "cue-ann-first") || !strings.Contains(reason, "cue-ann-second") {
		t.Fatalf("refusal reason %q does not name both cue ids", reason)
	}
	if !strings.Contains(reason, "announcement-session:audio-01") {
		t.Fatalf("refusal reason %q does not contain the exact claim string", reason)
	}

	// Not superseded: the announcement session is still Playing (never
	// torn down by the refused second activation's own Apply).
	annSnap := findSnapshot(t, mgr, pkgaudio.SessionID(cueactivation.AnnouncementSessionID))
	if annSnap.State != pkgaudio.StatePlaying {
		t.Fatalf("announcement session state = %s after a refused second activation, want still playing", annSnap.State)
	}

	// A redelivery of the FIRST activation (same ActivationID) must still
	// be accepted as an idempotent replay, never refused as "concurrent
	// with itself".
	replayResult, err := op.activate(ctx, activationParams(t, firstAct), clock.now)
	if err != nil {
		t.Fatalf("replay of first activate: %v", err)
	}
	if !replayResult.Confirmed {
		t.Fatalf("replay of first activate did not confirm: %+v", replayResult)
	}
}

package assetsync

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file covers TRACK-H-cues-and-playlists.md section H5: the
// showmesh-audio runner's own coordinator-side resolver
// (ResolveShowmeshAudioPlaylistRef), and [ResolveCueCatalog]'s new claim-
// conflict arbitration (build item 3 — [config.DeriveShowCueClaims] had no
// production caller before this seam).

func showmeshAudioPlaylist(showID string, cueIDs []string, repeat string) config.ShowPlaylistPayload {
	entries := make([]config.ShowPlaylistEntry, 0, len(cueIDs))
	for _, cueID := range cueIDs {
		entries = append(entries, config.ShowPlaylistEntry{ID: cueID + "-entry", Cue: cueID})
	}
	return config.ShowPlaylistPayload{
		Show: showID, Name: "background", Runner: config.ShowPlaylistRunnerShowmeshAudio,
		ShowmeshAudio: &config.ShowPlaylistShowmeshAudio{Repeat: repeat},
		Entries:       entries,
	}
}

// --- ResolveShowmeshAudioPlaylistRef: build item 1 -------------------

func TestResolveShowmeshAudioPlaylistRefBuildsOneOrderedRepeatingRef(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01")

	putCue(t, st, "bed-a", "halloween-2026", config.ShowCuePayload{
		Name: "Bed A", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "bed-a-audio"}},
	})
	putCue(t, st, "bed-b", "halloween-2026", config.ShowCuePayload{
		Name: "Bed B", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "bed-b-audio"}},
	})
	hashA := "sha256:" + strings.Repeat("a", 64)
	hashB := "sha256:" + strings.Repeat("b", 64)
	createAsset(t, st, "halloween-2026", "bed-a-audio", store.AssetTargetKindShow, "", hashA, "bed-a.wav")
	createAsset(t, st, "halloween-2026", "bed-b-audio", store.AssetTargetKindShow, "", hashB, "bed-b.wav")

	payload := showmeshAudioPlaylist("halloween-2026", []string{"bed-a", "bed-b"}, config.ShowPlaylistShowmeshAudioRepeatAll)
	putPlaylist(t, st, "background", payload)

	ref, err := ResolveShowmeshAudioPlaylistRef(ctx, st, "halloween-2026", "audio-01", "background", 1, payload)
	if err != nil {
		t.Fatalf("ResolveShowmeshAudioPlaylistRef: %v", err)
	}
	if ref.OwnerKind != config.ShowPlaylistConfigKind || ref.OwnerID != "background" || ref.OwnerRevision != 1 {
		t.Fatalf("ref owner identity = %+v, want show.playlist/background/1", ref)
	}
	if ref.Repeat != pkgaudio.RepeatPlaylist {
		t.Fatalf("ref.Repeat = %q, want RepeatPlaylist (showmeshAudio.repeat=%q must map to it)", ref.Repeat, config.ShowPlaylistShowmeshAudioRepeatAll)
	}
	if len(ref.Items) != 2 {
		t.Fatalf("ref.Items = %+v, want exactly 2, in entry order", ref.Items)
	}
	if ref.Items[0].ItemID != "bed-a-entry" || ref.Items[0].Media.RuntimeFilename != "bed-a.wav" {
		t.Fatalf("ref.Items[0] = %+v, want bed-a-entry/bed-a.wav", ref.Items[0])
	}
	if ref.Items[1].ItemID != "bed-b-entry" || ref.Items[1].Media.RuntimeFilename != "bed-b.wav" {
		t.Fatalf("ref.Items[1] = %+v, want bed-b-entry/bed-b.wav", ref.Items[1])
	}
	if err := ref.Validate(); err != nil {
		t.Fatalf("ref failed pkgaudio.PlaylistRef.Validate: %v", err)
	}
}

func TestResolveShowmeshAudioPlaylistRefMapsRepeatNone(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01")
	putCue(t, st, "bed-a", "halloween-2026", config.ShowCuePayload{
		Name: "Bed A", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "bed-a-audio"}},
	})
	hashA := "sha256:" + strings.Repeat("a", 64)
	createAsset(t, st, "halloween-2026", "bed-a-audio", store.AssetTargetKindShow, "", hashA, "bed-a.wav")

	payload := showmeshAudioPlaylist("halloween-2026", []string{"bed-a"}, config.ShowPlaylistShowmeshAudioRepeatNone)

	ref, err := ResolveShowmeshAudioPlaylistRef(ctx, st, "halloween-2026", "audio-01", "background", 1, payload)
	if err != nil {
		t.Fatalf("ResolveShowmeshAudioPlaylistRef: %v", err)
	}
	if ref.Repeat != pkgaudio.RepeatNone {
		t.Fatalf("ref.Repeat = %q, want RepeatNone", ref.Repeat)
	}
}

func TestResolveShowmeshAudioPlaylistRefFailsVisiblyOnMissingAsset(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01")
	putCue(t, st, "bed-a", "halloween-2026", config.ShowCuePayload{
		Name: "Bed A", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "bed-a-audio"}},
	})
	// Deliberately no createAsset call: nothing uploaded for bed-a-audio.
	payload := showmeshAudioPlaylist("halloween-2026", []string{"bed-a"}, config.ShowPlaylistShowmeshAudioRepeatNone)

	if _, err := ResolveShowmeshAudioPlaylistRef(ctx, st, "halloween-2026", "audio-01", "background", 1, payload); err == nil {
		t.Fatal("ResolveShowmeshAudioPlaylistRef with no uploaded asset: err = nil, want a refusal (fails visibly, never guesses)")
	}
}

// TestResolveShowmeshAudioPlaylistRefRefusesCueDeclaringLTC proves TRACK-
// H-cues-and-playlists.md section H5 build item 5's own ruling: a
// background-Playlist entry whose Cue declares an ltc output is refused
// visibly, not silently dropped. Before this fix, [resolveCueOutputs]-
// style resolution never even looked at Outputs.LTC for a background
// entry, so the declared LTC start offset simply vanished with no trace —
// exactly the one option H0.4/H5 forbid ("a Cue must not alter LTC unless
// it declares that output").
func TestResolveShowmeshAudioPlaylistRefRefusesCueDeclaringLTC(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01")

	putCue(t, st, "bed-a", "halloween-2026", config.ShowCuePayload{
		Name: "Bed A",
		Outputs: config.ShowCueOutputs{
			Audio: &config.ShowCueAudioOutput{Asset: "bed-a-audio"},
			LTC:   &config.ShowCueLTCOutput{StartOffsetMillis: 1000},
		},
	})
	hashA := "sha256:" + strings.Repeat("a", 64)
	createAsset(t, st, "halloween-2026", "bed-a-audio", store.AssetTargetKindShow, "", hashA, "bed-a.wav")

	payload := showmeshAudioPlaylist("halloween-2026", []string{"bed-a"}, config.ShowPlaylistShowmeshAudioRepeatNone)

	_, err := ResolveShowmeshAudioPlaylistRef(ctx, st, "halloween-2026", "audio-01", "background", 1, payload)
	if err == nil {
		t.Fatal("ResolveShowmeshAudioPlaylistRef with a Cue declaring ltc: err = nil, want a refusal (a background session has no LTC generator, ADR-018)")
	}
	if !strings.Contains(err.Error(), "bed-a") || !strings.Contains(err.Error(), "ltc") {
		t.Fatalf("refusal %q does not name the offending cue and its ltc output", err.Error())
	}
}

// --- ResolveCueCatalog claim arbitration: build item 3 ----------------

// TestCueCatalogRefusesTwoCuesHoldingTheSameExclusiveClaim proves build
// item 3 as narrowed by build item 2's own ruling: [config.
// DeriveShowCueClaims] now has a real production caller (ResolveCueCatalog),
// and two Cues from DIFFERENT Playlists that would both hold
// program-audio-route on the same node are reported as a conflict on the
// resolved catalog — DATA, never an error out of ResolveCueCatalog itself
// (a conflict must not break Decide/Authorize/participatingNodesForShow
// for the rest of the fleet) — naming both Cue ids and the exact colliding
// claim string.
func TestCueCatalogRefusesTwoCuesHoldingTheSameExclusiveClaim(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01") // ProgramRoute "usb-interface"

	putCue(t, st, "cue-a", "halloween-2026", config.ShowCuePayload{
		Name: "A", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "a-audio"}},
	})
	putCue(t, st, "cue-b", "halloween-2026", config.ShowCuePayload{
		Name: "B", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "b-audio"}},
	})

	putPlaylist(t, st, "playlist-a", showmeshAudioPlaylist("halloween-2026", []string{"cue-a"}, config.ShowPlaylistShowmeshAudioRepeatNone))
	putPlaylist(t, st, "playlist-b", showmeshAudioPlaylist("halloween-2026", []string{"cue-b"}, config.ShowPlaylistShowmeshAudioRepeatNone))
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := ResolveCueCatalog(ctx, st, active, "audio-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog with two cues claiming the same program-audio-route: %v, want no error (a conflict is data, not an error)", err)
	}
	if len(catalog.Conflicts) != 1 {
		t.Fatalf("catalog.Conflicts = %+v, want exactly one conflict", catalog.Conflicts)
	}
	// Both Cues are still resolved as entries: a conflict does not remove
	// either Cue from the catalog, only marks the claim as contested.
	if len(catalog.Entries) != 2 {
		t.Fatalf("catalog entries = %+v, want both cues still included", catalog.Entries)
	}
	msg := catalog.Conflicts[0].Detail()
	if !strings.Contains(msg, "cue-a") || !strings.Contains(msg, "cue-b") {
		t.Fatalf("conflict detail %q does not name both cue ids", msg)
	}
	wantClaim := "program-audio-route:audio-01:usb-interface"
	if !strings.Contains(msg, wantClaim) {
		t.Fatalf("conflict detail %q does not contain the exact claim string %q", msg, wantClaim)
	}
	// The reported pair must be deterministic, never dependent on
	// st.ListConfigObjects' own iteration order: CatalogConflict.CueA is
	// always the lexically smaller id, regardless of which of the two
	// Cues ResolveCueCatalog happened to visit first.
	if catalog.Conflicts[0].CueA != "cue-a" || catalog.Conflicts[0].CueB != "cue-b" {
		t.Fatalf("catalog.Conflicts[0] = %+v, want CueA=cue-a CueB=cue-b (sorted, deterministic)", catalog.Conflicts[0])
	}
}

// TestCueCatalogEntriesOfOnePlaylistNeverConflict proves the exemption
// [detectClaimConflicts] (cuecatalog.go) exists for: two entries of the
// SAME Playlist routinely share the identical program-audio-route claim
// (there is only one program route per node), and that must never refuse
// an ordinary multi-entry Playlist — H1 spec section 4's "two entries of
// one Playlist are never concurrently active."
func TestCueCatalogEntriesOfOnePlaylistNeverConflict(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01")

	putCue(t, st, "cue-a", "halloween-2026", config.ShowCuePayload{
		Name: "A", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "a-audio"}},
	})
	putCue(t, st, "cue-b", "halloween-2026", config.ShowCuePayload{
		Name: "B", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "b-audio"}},
	})
	putPlaylist(t, st, "background", showmeshAudioPlaylist("halloween-2026", []string{"cue-a", "cue-b"}, config.ShowPlaylistShowmeshAudioRepeatNone))
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := ResolveCueCatalog(ctx, st, active, "audio-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog with two entries of ONE playlist sharing a claim: %v, want no refusal", err)
	}
	if len(catalog.Entries) != 2 {
		t.Fatalf("catalog entries = %+v, want both cues included", catalog.Entries)
	}
	if len(catalog.Conflicts) != 0 {
		t.Fatalf("catalog.Conflicts = %+v, want none (ordinary sequential entries of one Playlist)", catalog.Conflicts)
	}
}

// TestCueCatalogSameSinglePlaylistExemptsACueReferencedByTwoPlaylists
// proves TRACK-H-cues-and-playlists.md section H5 build item 2's own bug
// fix: sameSinglePlaylist used to require BOTH sides to be referenced by
// exactly one playlist each, so a Cue referenced by two playlists stopped
// exempting against ANYTHING — including an ordinary sibling entry of one
// of its own playlists — and the catalog refused an entirely unremarkable
// authoring shape. cue-a is here an entry of BOTH "background" and an
// unrelated fpp-runner playlist "main"; cue-b is an ordinary sibling
// entry of "background" alone. They must not conflict.
func TestCueCatalogSameSinglePlaylistExemptsACueReferencedByTwoPlaylists(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01")

	putCue(t, st, "cue-a", "halloween-2026", config.ShowCuePayload{
		Name: "A", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "a-audio"}},
	})
	putCue(t, st, "cue-b", "halloween-2026", config.ShowCuePayload{
		Name: "B", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "b-audio"}},
	})
	putPlaylist(t, st, "background", showmeshAudioPlaylist("halloween-2026", []string{"cue-a", "cue-b"}, config.ShowPlaylistShowmeshAudioRepeatNone))
	// cue-a is ALSO an entry of an unrelated fpp-runner playlist — giving
	// it TWO playlist memberships ({background, main}) while cue-b keeps
	// exactly one ({background}). Under the pre-fix rule
	// (len(a.playlists)==1 required on BOTH sides), sameSinglePlaylist(a,
	// b) would return false here even though they are plain sequential
	// entries of the SAME "background" Playlist.
	putPlaylist(t, st, "main", config.ShowPlaylistPayload{
		Show: "halloween-2026", Name: "main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "11111111-1111-1111-1111-111111111111", PlaylistName: "Main", PlaylistHash: strings.Repeat("a", 64)},
		Entries:        []config.ShowPlaylistEntry{{ID: "e1", Cue: "cue-a", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}}},
	})
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := ResolveCueCatalog(ctx, st, active, "audio-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v, want no error (a conflict, if any, is data)", err)
	}
	if len(catalog.Conflicts) != 0 {
		t.Fatalf("catalog.Conflicts = %+v, want none: cue-a and cue-b are ordinary sequential entries of the SAME playlist, regardless of cue-a's extra safeCueRef membership", catalog.Conflicts)
	}
}

// TestCueCatalogAnnouncementCueCoexistsWithBackgroundClaim proves H0.5's
// own reason an announcement Cue and a background-audio Cue never
// collide: the announcement Cue claims ONLY announcement-session, never
// program-audio-route, so it is never refused alongside a Playlist-
// referenced background Cue that DOES claim program-audio-route on the
// same node — even though both Cues declare `audio` (H0.5's own "An
// announcement Cue still declares audio, and that is not a
// contradiction").
func TestCueCatalogAnnouncementCueCoexistsWithBackgroundClaim(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01")

	putCue(t, st, "cue-bg", "halloween-2026", config.ShowCuePayload{
		Name: "Background", Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "bg-audio"}},
	})
	fadeMillis := 0
	putCue(t, st, "cue-ann", "halloween-2026", config.ShowCuePayload{
		Name: "Announcement",
		Outputs: config.ShowCueOutputs{
			Audio:        &config.ShowCueAudioOutput{Asset: "ann-audio"},
			Announcement: &config.ShowCueAnnouncementOutput{Policy: config.ShowCueAnnouncementPolicyMix, FadeMillis: fadeMillis},
		},
	})
	putPlaylist(t, st, "background", showmeshAudioPlaylist("halloween-2026", []string{"cue-bg"}, config.ShowPlaylistShowmeshAudioRepeatNone))
	// cue-ann is a directly-activatable announcement Cue (H0.4's own "not
	// required to be a Playlist entry") and declares no Playlist reference
	// of any kind here — deliberately, to prove [ResolveCueCatalog] itself
	// includes it because it declares the `announcement` output, not
	// because a test wired it in as a safeCueRef. TRACK-H-cues-and-
	// playlists.md section H5 build item 7's own fix: before it, this
	// test could only reach cue-ann by registering it as a safeCueRef, and
	// its own comment incorrectly claimed the real dispatch path resolves
	// independently of catalog membership — it does not; a node that
	// never received cue-ann in its held catalog refuses its activation as
	// an unknown Cue.
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := ResolveCueCatalog(ctx, st, active, "audio-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog with an announcement cue and a background cue on the same node: %v, want no refusal", err)
	}
	foundBG, foundAnn := false, false
	for _, e := range catalog.Entries {
		if e.CueID == "cue-bg" {
			foundBG = true
		}
		if e.CueID == "cue-ann" {
			foundAnn = true
			if e.Outputs.Announcement == nil {
				t.Fatalf("cue-ann catalog entry has no announcement output: %+v", e.Outputs)
			}
		}
	}
	if !foundBG || !foundAnn {
		t.Fatalf("catalog entries = %+v, want both cue-bg and cue-ann present", catalog.Entries)
	}
}

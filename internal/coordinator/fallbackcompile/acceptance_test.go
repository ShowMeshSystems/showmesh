package fallbackcompile

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// This file is TRACK-J-fpp-fallback.md J1's own acceptance gate: "unit
// and integration tests demonstrate that a changed Cue, playlist
// binding, target assignment, active-show generation, or catalog
// revision produces a new signed package; a stale package cannot be
// treated as current." Each of the five is its own test, not a
// table-driven case, per MGR-J's instruction that a shared table could
// pass by accident.

// TestChangedCueProducesNewSignedPackage covers the first of the five: a
// same-Cue, higher-revision change (the render sequence's asset content
// changes, which is what actually makes Filename/AssetHashes change)
// produces a new package revision and a new CueRevision in the affected
// entry.
func TestChangedCueProducesNewSignedPackage(t *testing.T) {
	f := newBaseFixture(t)
	before := f.compile(t)
	f.requirePublished(t, before)

	// Rewrite the Cue itself (a new show.cue revision, exactly what
	// "changed Cue" means): rename its render sequence.
	putCue(t, f.st, "thriller", f.showID, config.ShowCuePayload{
		Name:    "Thriller Redux",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	ackNodeCatalog(t, f.st, f.resolveActive(t), f.nodeID, f.now)

	after := f.compile(t)
	f.requirePublished(t, after)

	if after.Program.Program.Revision == before.Program.Program.Revision {
		t.Fatalf("a changed Cue must produce a new package revision; both compiles reported %q", before.Program.Program.Revision)
	}
	if after.Program.Program.Entries[0].CueRevision == before.Program.Program.Entries[0].CueRevision {
		t.Fatalf("a changed Cue must bump the entry's own CueRevision; both reported %d", before.Program.Program.Entries[0].CueRevision)
	}
}

// TestChangedPlaylistBindingProducesNewSignedPackage covers the second:
// rebinding the FPP entry to a different Cue (a new show.playlist
// revision) produces a new package revision and changes which Cue the
// entry key maps to.
func TestChangedPlaylistBindingProducesNewSignedPackage(t *testing.T) {
	f := newBaseFixture(t)
	before := f.compile(t)
	f.requirePublished(t, before)

	createAsset(t, f.st, f.showID, "safe", strings.Repeat("b", 64), "safe.fseq")
	putCue(t, f.st, "safe", f.showID, config.ShowCuePayload{
		Name:    "Safe",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "safe"}},
	})
	// Rebind the same (mainPlaylist, position 0) slot to the "safe" Cue.
	putPlaylist(t, f.st, "main", config.ShowPlaylistPayload{
		Show: f.showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "safe", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	ackNodeCatalog(t, f.st, f.resolveActive(t), f.nodeID, f.now)

	after := f.compile(t)
	f.requirePublished(t, after)

	if after.Program.Program.Revision == before.Program.Program.Revision {
		t.Fatalf("a changed playlist binding must produce a new package revision; both compiles reported %q", before.Program.Program.Revision)
	}
	if after.Program.Program.Entries[0].CueID != "safe" {
		t.Fatalf("Entries[0].CueID = %q after rebinding, want %q", after.Program.Program.Entries[0].CueID, "safe")
	}
	if before.Program.Program.Entries[0].CueID != "thriller" {
		t.Fatalf("precondition: before.Entries[0].CueID = %q, want %q", before.Program.Program.Entries[0].CueID, "thriller")
	}
}

// TestChangedPlaylistMetadataAloneProducesNewSignedPackage isolates
// PlaylistRevisions specifically, distinct from
// TestChangedPlaylistBindingProducesNewSignedPackage above: that test
// rebinds the entry to a different Cue, which changes BOTH the
// playlist's own revision AND the compiled Entries content (CueID) in
// the same step, so it cannot by itself prove PlaylistRevisions
// contributes to the hash at all, only that Entries does (a compiler bug
// that dropped PlaylistRevisions entirely from the hash input would
// still pass it). This test rewrites the SAME playlist with only its
// Name changed, FPP binding and entries byte-identical, so the playlist's
// own revision bumps while the compiled Entries content does not change
// at all.
func TestChangedPlaylistMetadataAloneProducesNewSignedPackage(t *testing.T) {
	f := newBaseFixture(t)
	before := f.compile(t)
	f.requirePublished(t, before)

	putPlaylist(t, f.st, "main", config.ShowPlaylistPayload{
		Show: f.showID, Name: "Main Renamed", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	ackNodeCatalog(t, f.st, f.resolveActive(t), f.nodeID, f.now)

	after := f.compile(t)
	f.requirePublished(t, after)

	if after.Program.Program.Entries[0].EntryKey != before.Program.Program.Entries[0].EntryKey ||
		after.Program.Program.Entries[0].CueID != before.Program.Program.Entries[0].CueID ||
		after.Program.Program.Entries[0].CueRevision != before.Program.Program.Entries[0].CueRevision {
		t.Fatalf("precondition: renaming the playlist alone must not change the compiled entry; before %+v, after %+v",
			before.Program.Program.Entries[0], after.Program.Program.Entries[0])
	}
	if after.Program.Program.Revision == before.Program.Program.Revision {
		t.Fatalf("a playlist revision bump with byte-identical compiled entries must still produce a new package revision (PlaylistRevisions must reach the hash on its own); both compiles reported %q", before.Program.Program.Revision)
	}
}

// TestChangedTargetAssignmentProducesNewSignedPackage covers the third:
// moving the show.surface from render-01 to a second node, render-02,
// changes which node the entry's render activation targets and produces
// a new package revision.
func TestChangedTargetAssignmentProducesNewSignedPackage(t *testing.T) {
	f := newBaseFixture(t)
	before := f.compile(t)
	f.requirePublished(t, before)

	declareNode(t, f.st, "render-02")
	putSurface(t, f.st, "garage", f.showID, "render-02") // reassign, same surface id
	ackNodeCatalog(t, f.st, f.resolveActive(t), "render-02", f.now)

	after := f.compile(t)
	f.requirePublished(t, after)

	if after.Program.Program.Revision == before.Program.Program.Revision {
		t.Fatalf("a changed target assignment must produce a new package revision; both compiles reported %q", before.Program.Program.Revision)
	}
	if len(after.Program.Program.Entries[0].Targets) != 1 || after.Program.Program.Entries[0].Targets[0].NodeID != "render-02" {
		t.Fatalf("Entries[0].Targets after reassignment = %+v, want exactly one target on render-02", after.Program.Program.Entries[0].Targets)
	}
}

// TestChangedActiveShowGenerationProducesNewSignedPackage covers the
// fourth: reissuing show.active (a deliberate reissue of the identical
// show, TRACK-H-H3-SPEC.md section 2) bumps the generation and produces
// a new package revision even though no Cue, playlist, or asset changed.
func TestChangedActiveShowGenerationProducesNewSignedPackage(t *testing.T) {
	f := newBaseFixture(t)
	before := f.compile(t)
	f.requirePublished(t, before)
	beforeGeneration := before.Program.Program.Generation

	putActiveShow(t, f.st, f.showID) // re-write revision 2 of the identical show
	ackNodeCatalog(t, f.st, f.resolveActive(t), f.nodeID, f.now)

	after := f.compile(t)
	f.requirePublished(t, after)

	if after.Program.Program.Generation == beforeGeneration {
		t.Fatalf("a reissued show.active must bump Generation; both compiles reported %d", beforeGeneration)
	}
	if after.Program.Program.Revision == before.Program.Program.Revision {
		t.Fatalf("a changed active-show generation must produce a new package revision; both compiles reported %q", before.Program.Program.Revision)
	}
}

// TestChangedCatalogRevisionProducesNewSignedPackage covers the fifth,
// distinct from "changed Cue": re-uploading a new asset for the SAME
// sequence (a different content hash superseding the old one) changes
// the resolved Cue-catalog's Revision without bumping the Cue's own
// config revision at all.
func TestChangedCatalogRevisionProducesNewSignedPackage(t *testing.T) {
	f := newBaseFixture(t)
	before := f.compile(t)
	f.requirePublished(t, before)
	beforeCueRevision := before.Program.Program.Entries[0].CueRevision

	createAsset(t, f.st, f.showID, "thriller", strings.Repeat("d", 64), "thriller-v2.fseq")
	ackNodeCatalog(t, f.st, f.resolveActive(t), f.nodeID, f.now)

	after := f.compile(t)
	f.requirePublished(t, after)

	if after.Program.Program.Revision == before.Program.Program.Revision {
		t.Fatalf("a changed catalog revision must produce a new package revision; both compiles reported %q", before.Program.Program.Revision)
	}
	if after.Program.Program.Entries[0].CueRevision != beforeCueRevision {
		t.Fatalf("Entries[0].CueRevision changed (%d -> %d) from a mere asset re-upload; the Cue's own config never changed",
			beforeCueRevision, after.Program.Program.Entries[0].CueRevision)
	}
	if after.Program.Program.Entries[0].Targets[0].Render.Filename != "thriller-v2.fseq" {
		t.Fatalf("Entries[0].Targets[0].Render.Filename = %q, want the newly current asset's filename",
			after.Program.Program.Entries[0].Targets[0].Render.Filename)
	}
}

// TestChangedCatalogRevisionAloneProducesNewSignedPackage isolates
// CatalogRevisions specifically, distinct from
// TestChangedCatalogRevisionProducesNewSignedPackage above: that test's
// re-uploaded asset changes the compiled entry's own
// Targets[0].Render.Filename, which is itself part of Entries, so it
// cannot by itself prove CatalogRevisions contributes to the hash (a
// compiler bug that dropped CatalogRevisions entirely from the hash
// input would still pass it, because Entries changed anyway). This test
// instead adds a SECOND FPP host's playlist, referencing a brand-new Cue
// that ALSO targets render-01 (the same node the fixture's own host
// already targets), so render-01's OWN resolved cue-catalog revision
// changes (a node's catalog spans the whole active show, not one FPP
// host's own playlists) while every entry the FIXTURE's own host
// compiles stays byte-identical: nothing about "M4-7840e12f81da4191c0d00fbb6a889314"'s
// own playlist, Cue, or asset changes at all.
func TestChangedCatalogRevisionAloneProducesNewSignedPackage(t *testing.T) {
	f := newBaseFixture(t)
	before := f.compile(t)
	f.requirePublished(t, before)

	const otherInstanceUUID = "M4-other-instance-00000000000000"
	createAsset(t, f.st, f.showID, "unrelated", strings.Repeat("e", 64), "unrelated.fseq")
	putCue(t, f.st, "unrelated", f.showID, config.ShowCuePayload{
		Name:    "Unrelated",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "unrelated"}},
	})
	putPlaylist(t, f.st, "other-host-playlist", config.ShowPlaylistPayload{
		Show: f.showID, Name: "Other Host", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: otherInstanceUUID, PlaylistName: "Other", PlaylistHash: strings.Repeat("9", 64),
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "unrelated", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	ackNodeCatalog(t, f.st, f.resolveActive(t), f.nodeID, f.now)

	after := f.compile(t)
	f.requirePublished(t, after)

	if after.Program.Program.Entries[0].EntryKey != before.Program.Program.Entries[0].EntryKey ||
		after.Program.Program.Entries[0].CueID != before.Program.Program.Entries[0].CueID ||
		after.Program.Program.Entries[0].CueRevision != before.Program.Program.Entries[0].CueRevision ||
		after.Program.Program.Entries[0].Targets[0].Render.Filename != before.Program.Program.Entries[0].Targets[0].Render.Filename {
		t.Fatalf("precondition: an unrelated host's new playlist must not change this host's own compiled entry; before %+v, after %+v",
			before.Program.Program.Entries[0], after.Program.Program.Entries[0])
	}
	if len(after.Program.Program.Entries) != len(before.Program.Program.Entries) {
		t.Fatalf("precondition: entry count changed (%d -> %d); the unrelated host's Cue must not appear in THIS host's own entries",
			len(before.Program.Program.Entries), len(after.Program.Program.Entries))
	}
	if after.Program.Program.Revision == before.Program.Program.Revision {
		t.Fatalf("render-01's own resolved cue-catalog revision changed (an unrelated Cue now targets it) with byte-identical compiled entries; this must still produce a new package revision (CatalogRevisions must reach the hash on its own); both compiles reported %q", before.Program.Program.Revision)
	}
}

// TestStalePackageIsNotTreatedAsCurrent proves the mechanism the five
// tests above exist to make possible: once the coordinator has published
// a package and something relevant changes, the stored (published)
// package's revision no longer matches what the coordinator currently
// resolves, so a caller comparing the two, the same comparison a
// reconciler or the HTTP read route performs, can tell a stale package
// apart from a current one by revision equality alone, never by assuming
// whatever was last published is still good.
func TestStalePackageIsNotTreatedAsCurrent(t *testing.T) {
	f := newBaseFixture(t)
	ctx := context.Background()

	published := f.compile(t)
	f.requirePublished(t, published)
	if err := f.st.PutFallbackProgram(ctx, publishedRecord(t, published)); err != nil {
		t.Fatalf("PutFallbackProgram: %v", err)
	}

	// Something changes: a new Cue revision.
	putCue(t, f.st, "thriller", f.showID, config.ShowCuePayload{
		Name:    "Thriller Redux",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	ackNodeCatalog(t, f.st, f.resolveActive(t), f.nodeID, f.now)

	recompiled := f.compile(t)
	f.requirePublished(t, recompiled)

	stored, err := f.st.GetFallbackProgram(ctx, testInstanceUUID)
	if err != nil {
		t.Fatalf("GetFallbackProgram: %v", err)
	}

	if stored.Revision != published.Program.Program.Revision {
		t.Fatalf("precondition: stored revision %q should still be the originally published one %q", stored.Revision, published.Program.Program.Revision)
	}
	if stored.Revision == recompiled.Program.Program.Revision {
		t.Fatalf("the stale stored package's revision must not equal the freshly recompiled one, or staleness becomes undetectable")
	}
}

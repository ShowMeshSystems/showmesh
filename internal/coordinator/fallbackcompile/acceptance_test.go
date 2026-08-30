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

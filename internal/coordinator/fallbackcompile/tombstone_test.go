package fallbackcompile

import (
	"context"
	"errors"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// TestCompileFailsSafelyWhenPlaylistEntryCueIsTombstoned is the
// show.playlist -> show.cue edge of the tombstone delete design's
// referential-safety decision: this codebase has no pre-flight check for a
// playlist entry's cue reference (unlike show.action's own targets and
// night.session's action bindings, both covered by existing readiness
// surfaces), so a dangling reference here must fail safely at the point it
// is actually used, not before. loadShowCuePayload (compile.go) is that
// point for a fallback-program compile.
//
// A playlist entry naming a deleted cue must make Compile return a non-nil
// error naming the cue (wrapping store.ErrConfigObjectNotFound, the same
// outcome a cue id that was never created produces), never panic and never
// silently compile a program missing that cue's own outputs.
func TestCompileFailsSafelyWhenPlaylistEntryCueIsTombstoned(t *testing.T) {
	st := openTestStore(t)
	now := testNow()
	showID, nodeID := "halloween", "render-01"

	putShow(t, st, showID, "Halloween")
	declareNode(t, st, nodeID)
	putSurface(t, st, "garage", showID, nodeID)
	putCue(t, st, "thriller", showID, config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	putPlaylist(t, st, "main", config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	putDefinition(t, st, testInstanceUUID, testPlaylistHash, "thriller.fseq")
	putActiveShow(t, st, showID)

	if _, err := st.TombstoneConfigObject(context.Background(), config.ShowCueConfigKind, "thriller"); err != nil {
		t.Fatalf("tombstone show.cue: %v", err)
	}

	_, err := Compile(context.Background(), st, fakeSigner{}, testInstanceUUID, now)
	if err == nil {
		t.Fatal("Compile() error = nil, want a non-nil error for a playlist entry naming a tombstoned cue")
	}
	if !errors.Is(err, store.ErrConfigObjectNotFound) {
		t.Errorf("Compile() error = %v, want it to wrap store.ErrConfigObjectNotFound", err)
	}
}

package assetsync

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// putAudioCue writes a two-output-node show.cue named "wake-up" whose audio
// output targets exactly targets, in that declared order, and references it
// from a playlist entry so referencedCueIDs (and this file's own fallback
// resolution, which is scoped to referenced Cues the same way
// [NodeCueSequenceIDs] is) actually picks it up.
func putAudioCue(t *testing.T, st *store.Store, showID string, targets []string) {
	t.Helper()
	cuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: "wake-up",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "wake-up-mh-test-audio", Targets: targets}},
	})
	if err != nil {
		t.Fatalf("encode wake-up cue: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, "wake-up", cuePayload)
	playlistPayload, err := config.EncodeShowPlaylistPayload(config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerShowmeshAudio,
		Entries: []config.ShowPlaylistEntry{{ID: "e1", Cue: "wake-up"}},
	})
	if err != nil {
		t.Fatalf("encode playlist: %v", err)
	}
	putConfig(t, st, config.ShowPlaylistConfigKind, "playlist-1", playlistPayload)
}

// wakeUpFixture seeds ADR-049 decision 5's own real-rig scenario: a two-node
// audio Cue ("wake-up", asset "wake-up-mh-test-audio") targeting
// showmesh-node-01 (the M4) and pi-audio-01 (the Scarlett-fed Pi), both
// declared audio.node objects.
func wakeUpFixture(t *testing.T) (st *store.Store, showID, nodeA, nodeB string) {
	t.Helper()
	st = openTestStore(t)
	showID = "halloween-2026"
	nodeA, nodeB = "showmesh-node-01", "pi-audio-01"
	putShow(t, st, showID, "Halloween 2026")
	declareNode(t, st, nodeA)
	declareNode(t, st, nodeB)
	putAudioNode(t, st, nodeA)
	putAudioNode(t, st, nodeB)
	putAudioCue(t, st, showID, []string{nodeA, nodeB})
	return st, showID, nodeA, nodeB
}

func assetHashes(t *testing.T, got ExpectedSet, seq string) []string {
	t.Helper()
	var hashes []string
	for _, a := range got.Assets {
		if a.SequenceID == seq {
			hashes = append(hashes, a.ContentHash)
		}
	}
	return hashes
}

// TestExpectedAssetsForNodeAudioFallbackBorrowsSoleOtherTargetsRow is this
// seam's case 1: only nodeA has a node-scoped row. nodeB, a declared target
// of the SAME Cue with nothing of its own and no show-scoped row either,
// must resolve nodeA's row rather than nothing.
func TestExpectedAssetsForNodeAudioFallbackBorrowsSoleOtherTargetsRow(t *testing.T) {
	st, showID, nodeA, nodeB := wakeUpFixture(t)
	createAssetWithMediaType(t, st, showID, "wake-up-mh-test-audio", store.AssetTargetKindNode, nodeA, "audio", "sha256:aaa", "wake-up.mp3")

	gotA, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeA)
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(%s) error = %v", nodeA, err)
	}
	if hashes := assetHashes(t, gotA, "wake-up-mh-test-audio"); len(hashes) != 1 || hashes[0] != "sha256:aaa" {
		t.Fatalf("ExpectedAssetsForNode(%s) hashes = %v, want [sha256:aaa] (its own row)", nodeA, hashes)
	}

	gotB, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeB)
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(%s) error = %v", nodeB, err)
	}
	hashesB := assetHashes(t, gotB, "wake-up-mh-test-audio")
	if len(hashesB) != 1 || hashesB[0] != "sha256:aaa" {
		t.Fatalf("ExpectedAssetsForNode(%s) hashes = %v, want [sha256:aaa] borrowed from %s", nodeB, hashesB, nodeA)
	}
}

// TestExpectedAssetsForNodeAudioFallbackShowScopedRowCoversBoth is case 2:
// a show-scoped row (no node-scoped row at all) already covers every
// target through the existing merge, with nothing new to borrow.
func TestExpectedAssetsForNodeAudioFallbackShowScopedRowCoversBoth(t *testing.T) {
	st, showID, nodeA, nodeB := wakeUpFixture(t)
	createAssetWithMediaType(t, st, showID, "wake-up-mh-test-audio", store.AssetTargetKindShow, "", "audio", "sha256:show", "wake-up.mp3")

	for _, nodeID := range []string{nodeA, nodeB} {
		got, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeID)
		if err != nil {
			t.Fatalf("ExpectedAssetsForNode(%s) error = %v", nodeID, err)
		}
		if hashes := assetHashes(t, got, "wake-up-mh-test-audio"); len(hashes) != 1 || hashes[0] != "sha256:show" {
			t.Fatalf("ExpectedAssetsForNode(%s) hashes = %v, want [sha256:show]", nodeID, hashes)
		}
	}
}

// TestExpectedAssetsForNodeAudioFallbackNodeRowsNeverCrossDeliver is case 3:
// both targets already hold their own node-scoped row (a deliberately
// different mix per node), and neither may end up resolving the other's.
func TestExpectedAssetsForNodeAudioFallbackNodeRowsNeverCrossDeliver(t *testing.T) {
	st, showID, nodeA, nodeB := wakeUpFixture(t)
	createAssetWithMediaType(t, st, showID, "wake-up-mh-test-audio", store.AssetTargetKindNode, nodeA, "audio", "sha256:aaa", "wake-up-a.mp3")
	createAssetWithMediaType(t, st, showID, "wake-up-mh-test-audio", store.AssetTargetKindNode, nodeB, "audio", "sha256:bbb", "wake-up-b.mp3")

	gotA, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeA)
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(%s) error = %v", nodeA, err)
	}
	if hashes := assetHashes(t, gotA, "wake-up-mh-test-audio"); len(hashes) != 1 || hashes[0] != "sha256:aaa" {
		t.Fatalf("ExpectedAssetsForNode(%s) hashes = %v, want ONLY its own [sha256:aaa], no cross-delivery from %s", nodeA, hashes, nodeB)
	}

	gotB, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeB)
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(%s) error = %v", nodeB, err)
	}
	if hashes := assetHashes(t, gotB, "wake-up-mh-test-audio"); len(hashes) != 1 || hashes[0] != "sha256:bbb" {
		t.Fatalf("ExpectedAssetsForNode(%s) hashes = %v, want ONLY its own [sha256:bbb], no cross-delivery from %s", nodeB, hashes, nodeA)
	}
}

// TestExpectedAssetsForNodeAudioFallbackOwnRowWinsOverShowRow is case 4: a
// node-scoped row for nodeB coexists with a current show-scoped row for the
// identical sequence (ADR-028 decision 4 allows both to be current at
// once, since they are different identities). nodeB's own row must keep
// winning rather than the merge exposing both, or an ambiguous pick
// between them.
func TestExpectedAssetsForNodeAudioFallbackOwnRowWinsOverShowRow(t *testing.T) {
	st, showID, _, nodeB := wakeUpFixture(t)
	createAssetWithMediaType(t, st, showID, "wake-up-mh-test-audio", store.AssetTargetKindNode, nodeB, "audio", "sha256:own", "wake-up-b.mp3")
	createAssetWithMediaType(t, st, showID, "wake-up-mh-test-audio", store.AssetTargetKindShow, "", "audio", "sha256:show", "wake-up.mp3")

	got, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeB)
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(%s) error = %v", nodeB, err)
	}
	if hashes := assetHashes(t, got, "wake-up-mh-test-audio"); len(hashes) != 1 || hashes[0] != "sha256:own" {
		t.Fatalf("ExpectedAssetsForNode(%s) hashes = %v, want ONLY its own node-scoped row [sha256:own], not the show-scoped row too", nodeB, hashes)
	}
}

// TestExpectedAssetsForNodeAudioFallbackNoRowAnywhereResolvesNothing proves
// the fallback never fabricates an asset when nothing exists for the
// sequence at all: neither target holds a row and there is no show-scoped
// row either, so both must resolve to nothing, exactly as an ordinary gap
// unrelated to fallback would.
func TestExpectedAssetsForNodeAudioFallbackNoRowAnywhereResolvesNothing(t *testing.T) {
	st, showID, nodeA, nodeB := wakeUpFixture(t)

	for _, nodeID := range []string{nodeA, nodeB} {
		got, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeID)
		if err != nil {
			t.Fatalf("ExpectedAssetsForNode(%s) error = %v", nodeID, err)
		}
		if hashes := assetHashes(t, got, "wake-up-mh-test-audio"); len(hashes) != 0 {
			t.Fatalf("ExpectedAssetsForNode(%s) hashes = %v, want none: nothing was ever uploaded for this sequence", nodeID, hashes)
		}
	}
}

// TestExpectedAssetsForNodeAudioFallbackSingleTargetUnchanged proves a
// single-target Cue's resolution is untouched by this seam: with only one
// audio.node declared, an untargeted output's own pre-existing "resolve to
// the sole audio.node" behavior is exactly what it was before, node-scoped
// row present or absent.
func TestExpectedAssetsForNodeAudioFallbackSingleTargetUnchanged(t *testing.T) {
	st := openTestStore(t)
	showID, nodeID := "halloween-2026", "showmesh-node-01"
	putShow(t, st, showID, "Halloween 2026")
	declareNode(t, st, nodeID)
	putAudioNode(t, st, nodeID)
	putAudioCue(t, st, showID, nil) // no explicit targets: resolves to the sole audio.node

	// Nothing uploaded yet: resolves to nothing, same as always.
	got, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeID)
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode() error = %v", err)
	}
	if hashes := assetHashes(t, got, "wake-up-mh-test-audio"); len(hashes) != 0 {
		t.Fatalf("ExpectedAssetsForNode() hashes = %v, want none before upload", hashes)
	}

	createAssetWithMediaType(t, st, showID, "wake-up-mh-test-audio", store.AssetTargetKindNode, nodeID, "audio", "sha256:solo", "wake-up.mp3")
	got, err = ExpectedAssetsForNode(context.Background(), st, showID, nodeID)
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode() error = %v", err)
	}
	if hashes := assetHashes(t, got, "wake-up-mh-test-audio"); len(hashes) != 1 || hashes[0] != "sha256:solo" {
		t.Fatalf("ExpectedAssetsForNode() hashes = %v, want [sha256:solo]", hashes)
	}
}

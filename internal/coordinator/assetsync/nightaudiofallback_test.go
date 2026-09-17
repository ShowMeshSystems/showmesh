package assetsync

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// putNightSessionBed writes a night.session object naming targets as its
// resting.backgroundAudio.Targets (ADR-049 decision 7), with one item per
// sequence in sequences, each item's own Asset.Target set to itemTarget
// (the "originally registered" node - decision 7's own point that this no
// longer decides WHERE the item plays, only which registered copy is
// canonical). targets may be nil/empty to build a bed that declares none.
func putNightSessionBed(t *testing.T, st *store.Store, id, showID string, targets []string, itemTarget string, sequences []string) {
	t.Helper()
	items := make([]config.NightSessionBackgroundAudioItem, 0, len(sequences))
	for _, seq := range sequences {
		items = append(items, config.NightSessionBackgroundAudioItem{
			ItemID: seq, Asset: config.NightSessionAssetRef{Show: showID, Sequence: seq, Target: itemTarget},
		})
	}
	payload := config.NightSessionPayload{
		Show: showID, Label: "bed test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "fpp-main", Playlist: "show"},
		Resting: config.NightSessionResting{
			FPPInstanceID: "fpp-main", Playlist: "resting",
			TimelineAsset: config.NightSessionAssetRef{Show: showID, Sequence: "resting-loop", Target: "fpp-main"},
			BackgroundAudio: &config.NightSessionBackgroundAudio{
				Items: items, Repeat: config.NightSessionBackgroundRepeatPlaylist,
				Resume: config.NightSessionBackgroundResumeRestart, ItemTransition: config.NightSessionItemTransitionSequential,
				MaxGainDb: -10, Targets: targets,
			},
		},
		AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDefault,
	}
	raw, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		t.Fatalf("encode night.session payload: %v", err)
	}
	putConfig(t, st, config.NightSessionConfigKind, id, raw)
}

// TestExpectedAssetsForNodeNightBedFallbackBorrowsOtherTargetsRow proves
// ADR-049 decisions 7 and 9: a bed declares two targets, only one with its
// own registered row; the other must still expect the borrowed copy.
func TestExpectedAssetsForNodeNightBedFallbackBorrowsOtherTargetsRow(t *testing.T) {
	st := openTestStore(t)
	showID := "halloween-2026"
	nodeA, nodeB := "node-a", "node-b"
	putShow(t, st, showID, "Halloween 2026")
	declareNode(t, st, nodeA)
	declareNode(t, st, nodeB)
	putAudioNode(t, st, nodeA)
	putAudioNode(t, st, nodeB)
	putNightSessionBed(t, st, "halloween-main", showID, []string{nodeA, nodeB}, nodeA, []string{"bed-1"})
	createAssetWithMediaType(t, st, showID, "bed-1", store.AssetTargetKindNode, nodeA, "audio", "sha256:bed1", "bed1.mp3")

	gotB, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeB)
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(%s) error = %v", nodeB, err)
	}
	hashes := assetHashes(t, gotB, "bed-1")
	if len(hashes) != 1 || hashes[0] != "sha256:bed1" {
		t.Fatalf("ExpectedAssetsForNode(%s) hashes = %v, want [sha256:bed1] borrowed from %s", nodeB, hashes, nodeA)
	}

	gotA, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeA)
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(%s) error = %v", nodeA, err)
	}
	if hashes := assetHashes(t, gotA, "bed-1"); len(hashes) != 1 || hashes[0] != "sha256:bed1" {
		t.Fatalf("ExpectedAssetsForNode(%s) hashes = %v, want its own [sha256:bed1]", nodeA, hashes)
	}
}

// TestExpectedAssetsForNodeNightBedWithoutTargetsGetsNoFallback proves the
// regression rule: a bed with no declared Targets never lends a node
// anything beyond its own OutputNodeIDs-scoped rows - nightBedAudioFallbackAssets
// must find no bed with HasDeclaredTargets true and contribute nothing.
func TestExpectedAssetsForNodeNightBedWithoutTargetsGetsNoFallback(t *testing.T) {
	st := openTestStore(t)
	showID := "halloween-2026"
	nodeA, nodeB := "node-a", "node-b"
	putShow(t, st, showID, "Halloween 2026")
	declareNode(t, st, nodeA)
	declareNode(t, st, nodeB)
	putAudioNode(t, st, nodeA)
	putAudioNode(t, st, nodeB)
	putNightSessionBed(t, st, "halloween-main", showID, nil, nodeA, []string{"bed-1"})
	createAssetWithMediaType(t, st, showID, "bed-1", store.AssetTargetKindNode, nodeA, "audio", "sha256:bed1", "bed1.mp3")

	gotB, err := ExpectedAssetsForNode(context.Background(), st, showID, nodeB)
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(%s) error = %v", nodeB, err)
	}
	if hashes := assetHashes(t, gotB, "bed-1"); len(hashes) != 0 {
		t.Fatalf("ExpectedAssetsForNode(%s) hashes = %v, want none: this bed declares no targets, so decision 7's fallback must not apply", nodeB, hashes)
	}
}

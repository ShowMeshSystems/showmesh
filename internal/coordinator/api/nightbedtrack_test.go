package api

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func putShowWideAudioAsset(t *testing.T, st *store.Store, show, sequence, assetID string) {
	t.Helper()
	_, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: assetID, ShowID: show, SequenceID: sequence, TargetKind: store.AssetTargetKindShow,
		MediaType: "audio", ContentHash: "sha256:" + assetID, RuntimeFilename: assetID + ".mp3", SizeBytes: 12345,
		Backend: "volume", StorageKey: assetID, CreatedByPrincipalID: "test", CreatedByPrincipalName: "test",
	})
	if err != nil {
		t.Fatalf("create show-wide asset %q: %v", assetID, err)
	}
}

func trackOnlyBedConfig(targets ...string) *config.NightSessionBackgroundAudio {
	return multiNodeBedConfig("", targets...)
}

func TestNightBedTrackOnlyItemsResolveToShowWideUpload(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putShowWideAudioAsset(t, st, "halloween", "bg-1", "asset-1")
	putShowWideAudioAsset(t, st, "halloween", "bg-2", "asset-2")
	ba := trackOnlyBedConfig("node-a", "node-b")

	items, err := h.nightBedPlaylistItemsFor(context.Background(), "halloween", "node-b", ba, nil)
	if err != nil {
		t.Fatalf("build items: %v", err)
	}
	if len(items) != 2 || items[0].Media.AssetID != "asset-1" || items[1].Media.AssetID != "asset-2" {
		t.Fatalf("items = %+v, want the two show-wide uploads in order", items)
	}
}

func TestNightBedCoverageAcceptsShowWideUpload(t *testing.T) {
	for _, tc := range []struct {
		name string
		ba   *config.NightSessionBackgroundAudio
	}{
		{"track-only items", trackOnlyBedConfig("node-a", "node-b")},
		{"items pinned to a node with no upload of its own", multiNodeBedConfig("node-a", "node-a", "node-b")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st, _, _ := nightBackgroundAudioTestHandlers(t)
			putActiveShowForTest(t, st, "halloween")
			putShowWideAudioAsset(t, st, "halloween", "bg-1", "asset-1")
			putShowWideAudioAsset(t, st, "halloween", "bg-2", "asset-2")
			for _, node := range []string{"node-a", "node-b"} {
				putNodeInventoryForTest(t, st, node, testNow,
					store.NodeAssetInventoryRecord{ContentHash: "sha256:asset-1", RuntimeFilename: "asset-1.mp3", SizeBytes: 12345},
					store.NodeAssetInventoryRecord{ContentHash: "sha256:asset-2", RuntimeFilename: "asset-2.mp3", SizeBytes: 12345},
				)
			}

			check := h.nightCheckBackgroundAudioBedTargetCoverage(context.Background(), testNow, "halloween", tc.ba, tc.ba.PlaybackNodeIDs())
			if check.health != nightHealthHealthy() {
				t.Fatalf("health = %v, reason = %q, want healthy", check.health, nightCheckText(check))
			}
		})
	}
}

func TestNightCheckBackgroundAudioAssetsTrackOnlyItemNeedsNodes(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putShowWideAudioAsset(t, st, "halloween", "bg-1", "asset-1")
	putShowWideAudioAsset(t, st, "halloween", "bg-2", "asset-2")

	check := h.nightCheckBackgroundAudioAssets(context.Background(), "halloween", trackOnlyBedConfig())
	if check.health != nightHealthFailed() || !strings.Contains(nightCheckText(check), "Set the show's audio nodes") {
		t.Fatalf("health = %v, reason = %q, want failed asking for the show's audio nodes", check.health, nightCheckText(check))
	}

	check = h.nightCheckBackgroundAudioAssets(context.Background(), "halloween", trackOnlyBedConfig("node-a"))
	if check.health != nightHealthHealthy() {
		t.Fatalf("health = %v, reason = %q, want healthy with a listed node", check.health, nightCheckText(check))
	}
}

package assetsync

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// putAnnouncementAction writes a show.action bound to nodeIDs, carrying
// R6's own media reference (assetId/contentHash/filename/sizeBytes) in
// its apply params.
func putAnnouncementAction(t *testing.T, st *store.Store, id, showID string, nodeIDs []string, assetID, contentHash, filename string) {
	t.Helper()
	payload, err := config.EncodeShowActionPayload(config.ShowActionPayload{
		Show: showID, Label: "Thank you", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationAudio, AudioNodeIDs: config.AudioNodeIDList(nodeIDs),
			AudioSessionID: "announcement-1", AudioAction: "audio.session.apply",
			Params: map[string]any{"media": map[string]any{
				"assetId": assetID, "contentHash": contentHash, "filename": filename, "sizeBytes": float64(4096),
			}},
		},
	})
	if err != nil {
		t.Fatalf("encode show.action payload: %v", err)
	}
	putConfig(t, st, config.ShowActionConfigKind, id, payload)
}

// putAnnouncementNightSession writes a night.session object binding cue
// (an announcement-role cue naming actionID) into its own
// resting.enterResting.cues, for showID.
func putAnnouncementNightSession(t *testing.T, st *store.Store, id, showID, cueName, actionID string) {
	t.Helper()
	payload, err := config.EncodeNightSessionPayload(config.NightSessionPayload{
		Show: showID,
		EnterResting: config.NightSessionEnterResting{
			Cues: []config.NightSessionCue{{Name: cueName, Role: config.NightSessionCueRoleAnnouncement, Action: actionID}},
		},
	})
	if err != nil {
		t.Fatalf("encode night.session payload: %v", err)
	}
	putConfig(t, st, config.NightSessionConfigKind, id, payload)
}

// TestAnnouncementBindingsResolvesTheBoundActionsMediaAndNodes proves R6:
// an announcement-role cue's own bound show.action resolves to its
// AudioNodeIDs and its params.media reference, exactly as authored.
func TestAnnouncementBindingsResolvesTheBoundActionsMediaAndNodes(t *testing.T) {
	st := openTestStore(t)
	showID := "halloween-2026"
	putShow(t, st, showID, "Halloween 2026")
	putAnnouncementAction(t, st, "thank-you", showID, []string{"node-a", "node-b"}, "ann-1", "sha256:thankyou", "thankyou.mp3")
	putAnnouncementNightSession(t, st, "sess-1", showID, "thank-you", "thank-you")

	bindings, err := AnnouncementBindings(context.Background(), st, showID)
	if err != nil {
		t.Fatalf("AnnouncementBindings() error = %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("AnnouncementBindings() = %+v, want exactly one binding", bindings)
	}
	b := bindings[0]
	if b.CueName != "thank-you" || b.ActionID != "thank-you" {
		t.Fatalf("binding = %+v, want CueName/ActionID both %q", b, "thank-you")
	}
	if len(b.NodeIDs) != 2 || b.NodeIDs[0] != "node-a" || b.NodeIDs[1] != "node-b" {
		t.Fatalf("binding.NodeIDs = %v, want [node-a node-b]", b.NodeIDs)
	}
	want := AnnouncementMedia{AssetID: "ann-1", ContentHash: "sha256:thankyou", Filename: "thankyou.mp3", SizeBytes: 4096}
	if b.Media != want {
		t.Fatalf("binding.Media = %+v, want %+v", b.Media, want)
	}
}

// TestExpectedAssetsForNodeAnnouncementFallbackBorrowsSoleOtherTargetsRow
// is R7's own acceptance proof, mirroring
// TestExpectedAssetsForNodeAudioFallbackBorrowsSoleOtherTargetsRow one
// file over: node-b, a declared target of the announcement with no
// registered row of its own, receives node-a's own row for the SAME
// content - the announcement's own named asset, borrowed rather than
// resolved to nothing.
func TestExpectedAssetsForNodeAnnouncementFallbackBorrowsSoleOtherTargetsRow(t *testing.T) {
	st := openTestStore(t)
	showID := "halloween-2026"
	putShow(t, st, showID, "Halloween 2026")
	declareNode(t, st, "node-a")
	declareNode(t, st, "node-b")
	putAnnouncementAction(t, st, "thank-you", showID, []string{"node-a", "node-b"}, "ann-1", "sha256:thankyou", "thankyou.mp3")
	putAnnouncementNightSession(t, st, "sess-1", showID, "thank-you", "thank-you")
	createAssetWithMediaType(t, st, showID, "announcement", store.AssetTargetKindNode, "node-a", "audio", "sha256:thankyou", "thankyou.mp3")

	gotA, err := ExpectedAssetsForNode(context.Background(), st, showID, "node-a")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(node-a) error = %v", err)
	}
	if hashes := assetHashes(t, gotA, "announcement"); len(hashes) != 1 || hashes[0] != "sha256:thankyou" {
		t.Fatalf("ExpectedAssetsForNode(node-a) hashes = %v, want [sha256:thankyou] (its own row)", hashes)
	}

	gotB, err := ExpectedAssetsForNode(context.Background(), st, showID, "node-b")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(node-b) error = %v", err)
	}
	if hashes := assetHashes(t, gotB, "announcement"); len(hashes) != 1 || hashes[0] != "sha256:thankyou" {
		t.Fatalf("ExpectedAssetsForNode(node-b) hashes = %v, want [sha256:thankyou] borrowed from node-a", hashes)
	}
}

// TestExpectedAssetsForNodeAnnouncementFallbackSkipsANonListedNode proves
// the fallback never widens beyond the announcement's own declared
// targets: a node this announcement never lists gets nothing from it.
func TestExpectedAssetsForNodeAnnouncementFallbackSkipsANonListedNode(t *testing.T) {
	st := openTestStore(t)
	showID := "halloween-2026"
	putShow(t, st, showID, "Halloween 2026")
	declareNode(t, st, "node-a")
	declareNode(t, st, "node-c")
	putAnnouncementAction(t, st, "thank-you", showID, []string{"node-a"}, "ann-1", "sha256:thankyou", "thankyou.mp3")
	putAnnouncementNightSession(t, st, "sess-1", showID, "thank-you", "thank-you")
	createAssetWithMediaType(t, st, showID, "announcement", store.AssetTargetKindNode, "node-a", "audio", "sha256:thankyou", "thankyou.mp3")

	gotC, err := ExpectedAssetsForNode(context.Background(), st, showID, "node-c")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode(node-c) error = %v", err)
	}
	if hashes := assetHashes(t, gotC, "announcement"); len(hashes) != 0 {
		t.Fatalf("ExpectedAssetsForNode(node-c) hashes = %v, want none: node-c is not one of this announcement's own listed targets", hashes)
	}
}

// TestAnnouncementNodesWithoutCopy proves the all-or-nothing coverage
// rule: any one listed node (or a show-wide row) holding the content
// rescues every other listed node; none holding it fails every one of
// them.
func TestAnnouncementNodesWithoutCopy(t *testing.T) {
	st := openTestStore(t)
	showID := "halloween-2026"
	putShow(t, st, showID, "Halloween 2026")
	media := AnnouncementMedia{AssetID: "ann-1", ContentHash: "sha256:thankyou", Filename: "thankyou.mp3", SizeBytes: 4096}

	t.Run("no copy anywhere fails every listed node", func(t *testing.T) {
		missing, err := AnnouncementNodesWithoutCopy(context.Background(), st, showID, []string{"node-a", "node-b"}, media)
		if err != nil {
			t.Fatalf("AnnouncementNodesWithoutCopy() error = %v", err)
		}
		if len(missing) != 2 {
			t.Fatalf("missing = %v, want both node-a and node-b", missing)
		}
	})

	createAssetWithMediaType(t, st, showID, "announcement", store.AssetTargetKindNode, "node-a", "audio", "sha256:thankyou", "thankyou.mp3")

	t.Run("one listed node holding a copy rescues both", func(t *testing.T) {
		missing, err := AnnouncementNodesWithoutCopy(context.Background(), st, showID, []string{"node-a", "node-b"}, media)
		if err != nil {
			t.Fatalf("AnnouncementNodesWithoutCopy() error = %v", err)
		}
		if len(missing) != 0 {
			t.Fatalf("missing = %v, want none: node-a's own row covers node-b via the fallback", missing)
		}
	})

	t.Run("incomplete media reference errors rather than guessing", func(t *testing.T) {
		if _, err := AnnouncementNodesWithoutCopy(context.Background(), st, showID, []string{"node-a"}, AnnouncementMedia{}); err == nil {
			t.Fatal("AnnouncementNodesWithoutCopy() with an incomplete media reference = nil error, want one")
		}
	})
}

// TestAnnouncementNodesWithoutCopy_CopyOnANonListedNodeDoesNotCount proves
// coverage is scoped to the announcement's own listed nodes: a copy that
// exists only on a node this announcement never lists never rescues the
// nodes that are actually listed.
func TestAnnouncementNodesWithoutCopy_CopyOnANonListedNodeDoesNotCount(t *testing.T) {
	st := openTestStore(t)
	showID := "halloween-2026"
	putShow(t, st, showID, "Halloween 2026")
	media := AnnouncementMedia{AssetID: "ann-1", ContentHash: "sha256:thankyou", Filename: "thankyou.mp3", SizeBytes: 4096}
	createAssetWithMediaType(t, st, showID, "announcement", store.AssetTargetKindNode, "node-c", "audio", "sha256:thankyou", "thankyou.mp3")

	missing, err := AnnouncementNodesWithoutCopy(context.Background(), st, showID, []string{"node-a", "node-b"}, media)
	if err != nil {
		t.Fatalf("AnnouncementNodesWithoutCopy() error = %v", err)
	}
	if len(missing) != 2 {
		t.Fatalf("missing = %v, want both node-a and node-b: node-c's own copy is not one of this announcement's own listed targets", missing)
	}
}

package fppreconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// TestPlaylistReadinessAssetsMissingResolvesBorrowedAssetOncePiHoldsIt
// proves ADR-049 decision 5's third precedence tier all the way through
// [PlaylistReadiness]: a Cue names two audio targets, only m4 has a
// node-scoped row, and pi has neither its own row nor a show-scoped one.
// Before pi's own inventory actually holds the borrowed content hash,
// readiness must name pi as missing it (the asset sync service dispatches
// against exactly this Missing entry -- see [assetsync.Service.syncNode]);
// once pi's inventory catches up, the SAME two-target Cue reads Ready,
// never naming pi. Reverting the ExpectedAssetsForNode fallback under test
// collapses BOTH phases to a silent "Ready" (nothing was ever expected of
// pi), which is exactly the bug the owner's rig hit: the coordinator
// refused to deliver the file while readiness said nothing was wrong.
func TestPlaylistReadinessAssetsMissingResolvesBorrowedAssetOncePiHoldsIt(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")

	p := twoTargetAudioPlaylist(t, st, "show-1", "cue-1", "wake-up-mh-test-audio", []string{"m4", "pi"})

	// Only m4 has a node-scoped row; pi has neither its own nor a
	// show-scoped one -- deliberately no asset row created for pi. pi
	// still needs a fresh, complete (but empty) inventory report of its
	// own, or [assetsync.ComputeNodeManifest] reads it as Unknown/
	// NeverReported rather than NotReady, and assetsMissingReadiness
	// skips an Unknown node exactly like a Ready one.
	putNodeAudioAsset(t, ctx, st, "show-1", "m4", "wake-up-mh-test-audio", "sha256:borrowed", true)
	if err := st.ReplaceNodeAssetInventory(ctx, "pi", nil, store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true}); err != nil {
		t.Fatalf("replace node asset inventory for pi: %v", err)
	}

	ackNodeCatalog(t, ctx, st, "m4")
	ackNodeCatalog(t, ctx, st, "pi")

	report, err := PlaylistReadiness(ctx, st, nil, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatalf("Ready = true, want false: pi has not fetched the borrowed asset yet")
	}
	if report.FailingCondition != ReadinessAssetsMissing {
		t.Fatalf("FailingCondition = %q, want %q (reason: %s)", report.FailingCondition, ReadinessAssetsMissing, report.Reason)
	}
	if !strings.Contains(report.Reason, "pi") {
		t.Fatalf("Reason = %q, want it to name pi", report.Reason)
	}

	// pi's own inventory now genuinely reports holding the SAME content
	// hash m4's row carries -- the state the sync service's own dispatch
	// (driven by this identical Missing entry) is meant to produce.
	if err := st.ReplaceNodeAssetInventory(ctx, "pi",
		[]store.NodeAssetInventoryRecord{{NodeID: "pi", ContentHash: "sha256:borrowed", RuntimeFilename: "wake-up-mh-test-audio.wav", SizeBytes: 2048, VerifiedAt: time.Now()}},
		store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true},
	); err != nil {
		t.Fatalf("replace node asset inventory for pi: %v", err)
	}

	report, err = PlaylistReadiness(ctx, st, nil, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true now that pi holds the borrowed asset (failing condition %q: %s)", report.FailingCondition, report.Reason)
	}
}

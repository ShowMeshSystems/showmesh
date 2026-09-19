package assetsync

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func TestEnsureAssetOnNodeSkipsANodeThatAlreadyHoldsIt(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	declareNode(t, st, "audio-01")
	rec := createAsset(t, st, "halloween-2026", "alert", store.AssetTargetKindNode, "audio-01", "sha256:alert", "Alert.mp3")
	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)

	if err := svc.EnsureAssetOnNode(context.Background(), rec.ID, "audio-01"); err != nil {
		t.Fatalf("EnsureAssetOnNode: %v", err)
	}
	if n := pub.callCount(); n != 1 {
		t.Fatalf("asset.fetch dispatches for a missing asset = %d, want 1", n)
	}

	if err := st.ReplaceNodeAssetInventory(context.Background(), "audio-01",
		[]store.NodeAssetInventoryRecord{{ContentHash: rec.ContentHash, RuntimeFilename: rec.RuntimeFilename, SizeBytes: rec.SizeBytes, VerifiedAt: time.Now()}},
		store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true},
	); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	svc.mu.Lock()
	svc.inFlight = map[dispatchKey]dispatchRecord{}
	svc.mu.Unlock()

	if err := svc.EnsureAssetOnNode(context.Background(), rec.ID, "audio-01"); err != nil {
		t.Fatalf("EnsureAssetOnNode: %v", err)
	}
	if n := pub.callCount(); n != 1 {
		t.Fatalf("asset.fetch dispatches after the node reported holding it = %d, want still 1", n)
	}
}

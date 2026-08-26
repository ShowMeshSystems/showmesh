package fppreconcile

import (
	"context"
	"testing"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func TestNodeCatalogAckStatusNeverAcknowledged(t *testing.T) {
	st := openTestStore(t)
	status, rev, at, err := NodeCatalogAckStatus(context.Background(), st, "node-1", "some-revision")
	if err != nil {
		t.Fatalf("NodeCatalogAckStatus: %v", err)
	}
	if status != v1.CueCatalogStatusNeverAcknowledged {
		t.Fatalf("status = %q, want %q", status, v1.CueCatalogStatusNeverAcknowledged)
	}
	if rev != "" {
		t.Fatalf("ackRevision = %q, want empty", rev)
	}
	if !at.IsZero() {
		t.Fatalf("ackAt = %v, want zero", at)
	}
}

func TestNodeCatalogAckStatusCurrent(t *testing.T) {
	st := openTestStore(t)
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "node-1", Revision: "rev-a", ShowID: "show-1", Generation: 1,
	}); err != nil {
		t.Fatalf("put ack: %v", err)
	}
	status, rev, _, err := NodeCatalogAckStatus(context.Background(), st, "node-1", "rev-a")
	if err != nil {
		t.Fatalf("NodeCatalogAckStatus: %v", err)
	}
	if status != v1.CueCatalogStatusCurrent {
		t.Fatalf("status = %q, want %q", status, v1.CueCatalogStatusCurrent)
	}
	if rev != "rev-a" {
		t.Fatalf("ackRevision = %q, want %q", rev, "rev-a")
	}
}

func TestNodeCatalogAckStatusStaleWhenRevisionDiffers(t *testing.T) {
	st := openTestStore(t)
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "node-1", Revision: "rev-old", ShowID: "show-1", Generation: 1,
	}); err != nil {
		t.Fatalf("put ack: %v", err)
	}
	status, _, _, err := NodeCatalogAckStatus(context.Background(), st, "node-1", "rev-new")
	if err != nil {
		t.Fatalf("NodeCatalogAckStatus: %v", err)
	}
	if status != v1.CueCatalogStatusStale {
		t.Fatalf("status = %q, want %q", status, v1.CueCatalogStatusStale)
	}
}

func TestNodeCatalogAckStatusStaleWhenCurrentRevisionEmpty(t *testing.T) {
	st := openTestStore(t)
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "node-1", Revision: "rev-a", ShowID: "show-1", Generation: 1,
	}); err != nil {
		t.Fatalf("put ack: %v", err)
	}
	status, _, _, err := NodeCatalogAckStatus(context.Background(), st, "node-1", "")
	if err != nil {
		t.Fatalf("NodeCatalogAckStatus: %v", err)
	}
	if status != v1.CueCatalogStatusStale {
		t.Fatalf("status = %q, want %q: an empty currentRevision has nothing for any acknowledgement to match", status, v1.CueCatalogStatusStale)
	}
}

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestGetNodeAssetReportNotFoundBeforeAnyReport is the load-bearing
// distinction spec §4.2/§4.3 requires: a node that has never reported must
// read as ErrNodeAssetReportNotFound ("I have never heard from this
// node"), never as a report with Complete == false or an empty inventory
// standing in for silence.
func TestGetNodeAssetReportNotFoundBeforeAnyReport(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.GetNodeAssetReport(ctx, "render-01"); !errors.Is(err, ErrNodeAssetReportNotFound) {
		t.Errorf("err = %v, want ErrNodeAssetReportNotFound", err)
	}

	inv, err := st.GetNodeAssetInventory(ctx, "render-01")
	if err != nil {
		t.Fatalf("get inventory for a node that has never reported: %v", err)
	}
	if len(inv) != 0 {
		t.Errorf("inventory = %+v, want empty", inv)
	}
}

// TestReplaceNodeAssetInventoryDistinctFromNeverReported proves the OTHER
// half of the same distinction: a node that reported and holds nothing
// must be distinguishable from a node that never reported at all — both
// produce an empty GetNodeAssetInventory result, but only the former has a
// GetNodeAssetReport row.
func TestReplaceNodeAssetInventoryDistinctFromNeverReported(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	report := NodeAssetReportRecord{ReportedAt: time.Now().UTC(), Complete: true, Reason: ""}
	if err := st.ReplaceNodeAssetInventory(ctx, "render-01", nil, report); err != nil {
		t.Fatalf("replace with empty inventory: %v", err)
	}

	got, err := st.GetNodeAssetReport(ctx, "render-01")
	if err != nil {
		t.Fatalf("get report for a node that reported empty: %v", err)
	}
	if !got.Complete {
		t.Errorf("Complete = false, want true (the node completed a walk and found nothing)")
	}

	inv, err := st.GetNodeAssetInventory(ctx, "render-01")
	if err != nil {
		t.Fatalf("get inventory: %v", err)
	}
	if len(inv) != 0 {
		t.Errorf("inventory = %+v, want empty", inv)
	}

	// A DIFFERENT node that has never reported must still read as not-found,
	// proving the two states are not conflated globally.
	if _, err := st.GetNodeAssetReport(ctx, "render-02"); !errors.Is(err, ErrNodeAssetReportNotFound) {
		t.Errorf("render-02 err = %v, want ErrNodeAssetReportNotFound", err)
	}
}

// TestReplaceNodeAssetInventoryIsDeleteThenInsert proves a second replace
// wholly supersedes the first: a hash present in the first report and
// absent from the second must not survive.
func TestReplaceNodeAssetInventoryIsDeleteThenInsert(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	firstItems := []NodeAssetInventoryRecord{
		{ContentHash: "sha256:aaa", RuntimeFilename: "Opening.fseq", SizeBytes: 100, VerifiedAt: time.Now().UTC()},
		{ContentHash: "sha256:bbb", RuntimeFilename: "Closing.fseq", SizeBytes: 200, VerifiedAt: time.Now().UTC()},
	}
	firstReport := NodeAssetReportRecord{ReportedAt: time.Now().UTC(), Complete: true}
	if err := st.ReplaceNodeAssetInventory(ctx, "render-01", firstItems, firstReport); err != nil {
		t.Fatalf("first replace: %v", err)
	}

	inv, err := st.GetNodeAssetInventory(ctx, "render-01")
	if err != nil {
		t.Fatalf("get inventory after first replace: %v", err)
	}
	if len(inv) != 2 {
		t.Fatalf("inventory after first replace = %d items, want 2", len(inv))
	}

	// Second replace drops sha256:bbb entirely (the file no longer exists on
	// the node) and adds a new one.
	secondItems := []NodeAssetInventoryRecord{
		{ContentHash: "sha256:aaa", RuntimeFilename: "Opening.fseq", SizeBytes: 100, VerifiedAt: time.Now().UTC()},
		{ContentHash: "sha256:ccc", RuntimeFilename: "NewSequence.fseq", SizeBytes: 300, VerifiedAt: time.Now().UTC()},
	}
	secondReport := NodeAssetReportRecord{ReportedAt: time.Now().UTC(), Complete: true}
	if err := st.ReplaceNodeAssetInventory(ctx, "render-01", secondItems, secondReport); err != nil {
		t.Fatalf("second replace: %v", err)
	}

	inv, err = st.GetNodeAssetInventory(ctx, "render-01")
	if err != nil {
		t.Fatalf("get inventory after second replace: %v", err)
	}
	if len(inv) != 2 {
		t.Fatalf("inventory after second replace = %d items, want exactly 2: %+v", len(inv), inv)
	}
	byHash := make(map[string]NodeAssetInventoryRecord, len(inv))
	for _, item := range inv {
		byHash[item.ContentHash] = item
	}
	if _, stillThere := byHash["sha256:bbb"]; stillThere {
		t.Errorf("sha256:bbb still present after a replace that omitted it, want deleted")
	}
	if _, present := byHash["sha256:ccc"]; !present {
		t.Errorf("sha256:ccc missing after a replace that included it")
	}
	if _, present := byHash["sha256:aaa"]; !present {
		t.Errorf("sha256:aaa missing — it was present in both replaces and must survive")
	}
}

// TestReplaceNodeAssetInventoryUpsertsReportFields proves the report row's
// Complete/Reason/ReportedAt reflect the latest call, not the first —
// necessary for a node that failed a walk once and later succeeds (or vice
// versa) to be readable as its CURRENT state rather than its first-ever one.
func TestReplaceNodeAssetInventoryUpsertsReportFields(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	if err := st.ReplaceNodeAssetInventory(ctx, "render-01", nil, NodeAssetReportRecord{
		ReportedAt: first, Complete: false, Reason: "could not enumerate asset directory",
	}); err != nil {
		t.Fatalf("first replace: %v", err)
	}

	got, err := st.GetNodeAssetReport(ctx, "render-01")
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if got.Complete {
		t.Errorf("Complete = true, want false")
	}
	if got.Reason != "could not enumerate asset directory" {
		t.Errorf("Reason = %q, want the failure reason", got.Reason)
	}

	second := first.Add(2 * time.Minute)
	if err := st.ReplaceNodeAssetInventory(ctx, "render-01", nil, NodeAssetReportRecord{
		ReportedAt: second, Complete: true, Reason: "",
	}); err != nil {
		t.Fatalf("second replace: %v", err)
	}

	got, err = st.GetNodeAssetReport(ctx, "render-01")
	if err != nil {
		t.Fatalf("get report after second replace: %v", err)
	}
	if !got.Complete {
		t.Errorf("Complete = false, want true (the second, more recent report)")
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q, want empty", got.Reason)
	}
	if !got.ReportedAt.Equal(second) {
		t.Errorf("ReportedAt = %v, want %v (the latest report's own timestamp)", got.ReportedAt, second)
	}
}

// TestListNodeAssetReportsOrderedAndOnlyReportedNodes proves the listing
// only ever contains nodes that have actually reported, never a
// manufactured row for a declared-but-silent node.
func TestListNodeAssetReportsOrderedAndOnlyReportedNodes(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := st.ReplaceNodeAssetInventory(ctx, "render-02", nil, NodeAssetReportRecord{ReportedAt: now, Complete: true}); err != nil {
		t.Fatalf("replace render-02: %v", err)
	}
	if err := st.ReplaceNodeAssetInventory(ctx, "render-01", nil, NodeAssetReportRecord{ReportedAt: now, Complete: true}); err != nil {
		t.Fatalf("replace render-01: %v", err)
	}

	reports, err := st.ListNodeAssetReports(ctx)
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("len(reports) = %d, want 2", len(reports))
	}
	if reports[0].NodeID != "render-01" || reports[1].NodeID != "render-02" {
		t.Errorf("reports = %+v, want ordered [render-01, render-02]", reports)
	}
}

// TestReplaceNodeAssetInventoryTxForm proves the Tx form composes into a
// caller-supplied transaction and commits with it.
func TestReplaceNodeAssetInventoryTxForm(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	items := []NodeAssetInventoryRecord{
		{ContentHash: "sha256:aaa", RuntimeFilename: "Opening.fseq", SizeBytes: 100, VerifiedAt: time.Now().UTC()},
	}
	report := NodeAssetReportRecord{ReportedAt: time.Now().UTC(), Complete: true}

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.ReplaceNodeAssetInventory(ctx, "render-01", items, report)
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	inv, err := st.GetNodeAssetInventory(ctx, "render-01")
	if err != nil {
		t.Fatalf("get inventory after commit: %v", err)
	}
	if len(inv) != 1 || inv[0].ContentHash != "sha256:aaa" {
		t.Errorf("inventory after commit = %+v, want exactly [sha256:aaa]", inv)
	}
}

// TestReplaceNodeAssetInventoryEmptyNodeIDFails proves the method rejects
// an empty node id rather than silently deleting/upserting against "" —
// which would be a real, matchable row under this schema (node_id has no
// NOT NULL-empty check), so this must be enforced in Go.
func TestReplaceNodeAssetInventoryEmptyNodeIDFails(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.ReplaceNodeAssetInventory(ctx, "", nil, NodeAssetReportRecord{ReportedAt: time.Now().UTC(), Complete: true})
	if err == nil {
		t.Errorf("ReplaceNodeAssetInventory with empty nodeID succeeded, want an error")
	}
}

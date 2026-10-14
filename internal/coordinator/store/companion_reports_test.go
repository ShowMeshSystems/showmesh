package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func companionReportFixture(id, companion, sha string, received time.Time) CompanionReportRecord {
	return CompanionReportRecord{
		ID: id, CompanionID: companion, Kind: "crash_report", Source: "resolume-arena",
		FileName: "Arena-2026-10-14-205842.ips", CapturedAt: received.Add(-time.Minute), ReceivedAt: received,
		ContentSHA256: sha, SizeBytes: 5, Content: "crash", PrincipalID: "p-companion",
	}
}

func TestMigrationV49AppliesOnADatabaseAtV48(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	buildStoreAtVersion(t, dir, 48)

	st, err := open(ctx, dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 49): %v", err)
	}
	defer func() { _ = st.Close() }()

	got, err := st.ListCompanionReports(ctx, "", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("freshly migrated list = %v err %v, want empty", got, err)
	}
	if _, _, err := st.PutCompanionReport(ctx, companionReportFixture("r1", "arena-mac", "aa", time.Now().UTC())); err != nil {
		t.Fatalf("put after migration: %v", err)
	}
}

func TestPutCompanionReportDedupesAndRoundTrips(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, nil)
	at := time.Date(2026, 10, 14, 21, 0, 0, 0, time.UTC)

	first, created, err := st.PutCompanionReport(ctx, companionReportFixture("r1", "arena-mac", "aa", at))
	if err != nil || !created || first.ID != "r1" {
		t.Fatalf("first put = %+v created %v err %v", first, created, err)
	}
	again, created, err := st.PutCompanionReport(ctx, companionReportFixture("r2", "arena-mac", "aa", at.Add(time.Minute)))
	if err != nil || created || again.ID != "r1" {
		t.Fatalf("duplicate put = %+v created %v err %v, want the stored r1 and created false", again, created, err)
	}
	if _, created, err := st.PutCompanionReport(ctx, companionReportFixture("r3", "other-mac", "aa", at)); err != nil || !created {
		t.Fatalf("same content from another companion: created %v err %v, want stored", created, err)
	}

	full, err := st.GetCompanionReport(ctx, "r1")
	if err != nil || full.Content != "crash" || !full.CapturedAt.Equal(at.Add(-time.Minute)) {
		t.Fatalf("get = %+v err %v", full, err)
	}
	if _, err := st.GetCompanionReport(ctx, "missing"); !errors.Is(err, ErrCompanionReportNotFound) {
		t.Fatalf("get missing err = %v, want not found", err)
	}

	mine, err := st.ListCompanionReports(ctx, "arena-mac", 10)
	if err != nil || len(mine) != 1 || mine[0].Content != "" {
		t.Fatalf("list one companion = %+v err %v, want one summary without content", mine, err)
	}
	all, err := st.ListCompanionReports(ctx, "", 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("list all = %+v err %v, want 2", all, err)
	}
}

func TestPutCompanionReportPrunesOldestBeyondRetention(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, nil)
	base := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)

	total := CompanionReportRetainPerCompanion + 3
	for i := 0; i < total; i++ {
		rec := companionReportFixture(fmt.Sprintf("r%04d", i), "arena-mac", fmt.Sprintf("sha%04d", i), base.Add(time.Duration(i)*time.Second))
		if _, _, err := st.PutCompanionReport(ctx, rec); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if _, _, err := st.PutCompanionReport(ctx, companionReportFixture("x", "other-mac", "x", base)); err != nil {
		t.Fatalf("put other: %v", err)
	}

	kept, err := st.ListCompanionReports(ctx, "arena-mac", total)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(kept) != CompanionReportRetainPerCompanion {
		t.Fatalf("kept %d reports, want %d", len(kept), CompanionReportRetainPerCompanion)
	}
	if kept[len(kept)-1].ID != "r0003" || kept[0].ID != fmt.Sprintf("r%04d", total-1) {
		t.Fatalf("kept range %s..%s, want r0003..r%04d", kept[len(kept)-1].ID, kept[0].ID, total-1)
	}
	if _, err := st.GetCompanionReport(ctx, "x"); err != nil {
		t.Fatalf("another companion's report was pruned: %v", err)
	}
}

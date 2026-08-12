package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestFinishDiscoveryRunIncompleteRecordsReason proves Step 5's rule
// applied to discovery: an incomplete run is a row with complete=0 and a
// reason, never a missing row and never silence.
func TestFinishDiscoveryRunIncompleteRecordsReason(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: "run-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := st.FinishDiscoveryRun(ctx, "run-1", false, "broker unreachable", 0); err != nil {
		t.Fatalf("finish: %v", err)
	}

	rec, err := st.GetDiscoveryRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.Complete {
		t.Errorf("Complete = true, want false")
	}
	if rec.Reason != "broker unreachable" {
		t.Errorf("Reason = %q, want %q", rec.Reason, "broker unreachable")
	}
	if rec.FinishedAt == nil {
		t.Errorf("FinishedAt = nil, want set — an incomplete run still finished, it just found nothing conclusive")
	}
}

// TestFinishDiscoveryRunCompleteRecordsFoundCount proves the successful
// path stores what a caller (seam B) will actually read to decide which
// declared nodes it may claim anything about the absence of.
func TestFinishDiscoveryRunCompleteRecordsFoundCount(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: "run-2"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := st.FinishDiscoveryRun(ctx, "run-2", true, "", 3); err != nil {
		t.Fatalf("finish: %v", err)
	}

	rec, err := st.GetDiscoveryRun(ctx, "run-2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !rec.Complete {
		t.Errorf("Complete = false, want true")
	}
	if rec.FoundCount != 3 {
		t.Errorf("FoundCount = %d, want 3", rec.FoundCount)
	}
}

// TestGetDiscoveryRunNotFound proves the sentinel error path.
func TestGetDiscoveryRunNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	if _, err := st.GetDiscoveryRun(context.Background(), "no-such-run"); !errors.Is(err, ErrDiscoveryRunNotFound) {
		t.Errorf("err = %v, want ErrDiscoveryRunNotFound", err)
	}
}

// TestPruneDiscoveryRunsByRowCount is F8's fix: nothing referenced
// WithMaxDiscoveryRunRows/WithMaxDiscoveryRunAge/pruneDiscoveryRuns before
// this test, so neither DELETE statement in pruneDiscoveryRuns had ever
// executed against a populated discovery_runs table in any test. Mirrors
// [TestPruneCommandsByRowCount]'s identical two-trigger interaction
// (commands_test.go, this same package): pruneEveryNDiscoveryRuns is 100,
// far more than the 3 runs this test starts, so the row-count trigger
// alone would never fire the prune pass on insert-count grounds within
// this test — advancing the clock past pruneCheckInterval between runs is
// what makes the AGE trigger fire and actually run pruneDiscoveryRuns,
// which then enforces maxDiscoveryRunRows.
func TestPruneDiscoveryRunsByRowCount(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxDiscoveryRunRows(2), WithMaxDiscoveryRunAge(0))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: idFor(i)}); err != nil {
			t.Fatalf("start run %d: %v", i, err)
		}
		clock.advance(2 * time.Hour)
	}

	got, err := st.ListDiscoveryRuns(ctx, 0)
	if err != nil {
		t.Fatalf("list discovery runs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (maxDiscoveryRunRows=2 keeps only the newest two — a prune pass must actually have fired)", len(got))
	}
}

// TestPruneDiscoveryRunsByAge is TestPruneDiscoveryRunsByRowCount's
// age-bound sibling, mirroring TestPruneCommandsByAge exactly.
func TestPruneDiscoveryRunsByAge(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxDiscoveryRunAge(24*time.Hour), WithMaxDiscoveryRunRows(1_000_000))
	ctx := context.Background()

	if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: "run-old"}); err != nil {
		t.Fatalf("start old: %v", err)
	}
	// Advance well past both maxDiscoveryRunAge and pruneCheckInterval so
	// the next run's age-triggered prune pass actually evicts the old row.
	clock.advance(48 * time.Hour)
	if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: "run-new"}); err != nil {
		t.Fatalf("start new: %v", err)
	}

	got, err := st.ListDiscoveryRuns(ctx, 0)
	if err != nil {
		t.Fatalf("list discovery runs: %v", err)
	}
	if len(got) != 1 || got[0].ID != "run-new" {
		t.Fatalf("got = %+v, want only the run younger than maxDiscoveryRunAge", got)
	}
}

// TestListDiscoveryRunsNewestFirst proves the ordering contract.
func TestListDiscoveryRunsNewestFirst(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-08-12T00:00:00Z")}
	st := openTestStore(t, clock)
	ctx := context.Background()

	if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: "run-a"}); err != nil {
		t.Fatalf("start a: %v", err)
	}
	clock.advance(1 * time.Hour)
	if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: "run-b"}); err != nil {
		t.Fatalf("start b: %v", err)
	}

	runs, err := st.ListDiscoveryRuns(ctx, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("len(runs) = %d, want 2", len(runs))
	}
	if runs[0].ID != "run-b" {
		t.Errorf("runs[0].ID = %q, want %q (newest first)", runs[0].ID, "run-b")
	}
}

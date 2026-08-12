package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestInsertCommandDuplicateIdempotencyKeyReturnsDuplicateError proves the
// single-goroutine half of acceptance criterion 5: a second insert with an
// already-used idempotency_key never creates a second row and returns a
// *DuplicateCommandError carrying the pre-existing row, never a bare
// generic error a caller could not distinguish from any other failure.
func TestInsertCommandDuplicateIdempotencyKeyReturnsDuplicateError(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first, err := st.InsertCommand(ctx, CommandRecord{
		ID: "cmd-1", IdempotencyKey: "idem-1", Action: "stop_playlist", State: "dispatched",
	})
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	_, err = st.InsertCommand(ctx, CommandRecord{
		ID: "cmd-2", IdempotencyKey: "idem-1", Action: "stop_playlist", State: "dispatched",
	})
	if err == nil {
		t.Fatalf("second insert with the same idempotency key succeeded, want a duplicate error")
	}
	if !errors.Is(err, ErrCommandIdempotencyKeyExists) {
		t.Fatalf("error = %v, want it to wrap ErrCommandIdempotencyKeyExists", err)
	}
	var dup *DuplicateCommandError
	if !errors.As(err, &dup) {
		t.Fatalf("error = %v, want errors.As to find a *DuplicateCommandError", err)
	}
	if dup.Existing.ID != first.ID {
		t.Errorf("DuplicateCommandError.Existing.ID = %q, want %q (the original row)", dup.Existing.ID, first.ID)
	}

	all, err := st.ListCommands(ctx, 0)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(all) = %d, want exactly 1 row after a duplicate insert attempt", len(all))
	}
}

// TestInsertCommandConcurrentDuplicateProducesExactlyOneRow is acceptance
// criterion 5, proven under real concurrency rather than only sequentially:
// "two concurrent inserts of the same idempotency_key produce exactly one
// row and a distinguishable duplicate error carrying the existing row."
// This package's connection pool is capped at 1 (store.go's open()), so
// the two goroutines' inserts are serialized by the driver itself — this
// test exists to prove the OUTCOME (one row, one error, no hang, no silent
// data loss) rather than to exercise any particular interleaving.
//
// F12 review finding: an earlier version of this test asserted only the
// sentinel (errors.Is(err, ErrCommandIdempotencyKeyExists)) for every
// duplicate, never that the carried [DuplicateCommandError.Existing] row
// actually names the command that won — acceptance criterion 5 asks for
// both halves ("a distinguishable duplicate error carrying the existing
// row"), and a caller (seam C) needs Existing to return the ORIGINAL
// result rather than a description of nothing in particular. Every
// goroutine's own returned CommandRecord is captured (not just its error),
// so whichever one succeeded is known by ID, and every duplicate's
// Existing.ID is checked against it — this cannot pass by chance the way
// checking only errors.Is could.
func TestInsertCommandConcurrentDuplicateProducesExactlyOneRow(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	results := make([]error, n)
	records := make([]CommandRecord, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec, err := st.InsertCommand(ctx, CommandRecord{
				ID: idFor(i), IdempotencyKey: "idem-race", Action: "stop_playlist", State: "dispatched",
			})
			results[i] = err
			records[i] = rec
		}(i)
	}
	wg.Wait()

	var succeeded, duplicated int
	var winnerID string
	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
			winnerID = records[i].ID
		case errors.Is(err, ErrCommandIdempotencyKeyExists):
			duplicated++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("succeeded = %d, want exactly 1", succeeded)
	}
	if duplicated != n-1 {
		t.Errorf("duplicated = %d, want %d", duplicated, n-1)
	}

	for i, err := range results {
		if err == nil {
			continue
		}
		var dup *DuplicateCommandError
		if !errors.As(err, &dup) {
			t.Errorf("goroutine %d: error = %v, want errors.As to find a *DuplicateCommandError", i, err)
			continue
		}
		if dup.Existing.ID != winnerID {
			t.Errorf("goroutine %d: DuplicateCommandError.Existing.ID = %q, want %q (the command that actually won the insert)", i, dup.Existing.ID, winnerID)
		}
		if dup.Existing.IdempotencyKey != "idem-race" {
			t.Errorf("goroutine %d: DuplicateCommandError.Existing.IdempotencyKey = %q, want %q", i, dup.Existing.IdempotencyKey, "idem-race")
		}
	}

	all, err := st.ListCommands(ctx, 0)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(all) = %d, want exactly 1 row after %d concurrent inserts of the same idempotency key", len(all), n)
	}
}

// idFor generates cmd-race-a, cmd-race-b, ... for
// TestInsertCommandConcurrentDuplicateProducesExactlyOneRow. F12 nit:
// string(rune('a'+i)) degrades silently past i=25 (wraps into punctuation
// and non-letter runes rather than erroring) — harmless today because the
// test's own n=8 never gets close, but worth flagging here rather than
// leaving a future reader to discover it by raising n past 26 and getting
// confusing IDs instead of a clear failure.
func idFor(i int) string {
	return "cmd-race-" + string(rune('a'+i))
}

// TestInsertCommandDispatchedResolvedNilOnInsert proves ARCHITECTURE
// §8.1's envelope shape: a freshly-inserted command has never been
// dispatched or resolved, REGARDLESS OF WHAT A CALLER SETS THOSE FIELDS TO
// ON INPUT — see [Store.InsertCommand]'s doc comment. That "regardless of"
// clause is the entire point of this test, so it passes non-nil
// DispatchedAt/ResolvedAt on input, not nil: a version of this test that
// passed nil for both (as an earlier version of this file did) passes
// whether or not InsertCommand actually clears them, because a caller
// that never set them in the first place trivially "gets nil back" either
// way — confirmed by mutation: with the caller-supplied DispatchedAt left
// on the returned rec (the exact bug a reviewer found by running this
// code, not by reading it — see [Store.InsertCommand]'s own doc comment),
// this test failed exactly as expected, and only then was the code fixed.
func TestInsertCommandDispatchedResolvedNilOnInsert(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	callerDispatchedAt := mustTime(t, "2020-01-01T00:00:00Z")
	callerResolvedAt := mustTime(t, "2020-01-02T00:00:00Z")
	rec, err := st.InsertCommand(ctx, CommandRecord{
		ID: "cmd-1", IdempotencyKey: "idem-1", Action: "stop_playlist", State: "dispatched",
		DispatchedAt: &callerDispatchedAt, ResolvedAt: &callerResolvedAt,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if rec.DispatchedAt != nil {
		t.Errorf("DispatchedAt = %v, want nil on insert (even though the caller passed a non-nil value in)", rec.DispatchedAt)
	}
	if rec.ResolvedAt != nil {
		t.Errorf("ResolvedAt = %v, want nil on insert (even though the caller passed a non-nil value in)", rec.ResolvedAt)
	}

	got, err := st.GetCommand(ctx, "cmd-1")
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if got.DispatchedAt != nil || got.ResolvedAt != nil {
		t.Errorf("stored row has DispatchedAt=%v ResolvedAt=%v, want both nil", got.DispatchedAt, got.ResolvedAt)
	}
}

// TestUpdateCommandOutcomeOnlyTouchesGivenFields proves
// [CommandOutcomeUpdate]'s partial-update contract: setting only
// DispatchedAt must leave State/ResolvedAt/etc. exactly as they were.
// TestPruneCommandsByRowCount is F8's fix: nothing referenced
// WithMaxCommandRows/WithMaxCommandAge/pruneCommands before this test, so
// neither DELETE statement in pruneCommands had ever executed against a
// populated commands table in any test. Mirrors
// TestPruneAuditByRowCount's identical two-trigger interaction exactly
// (identity_test.go's audit-retention coverage in this same package):
// pruneEveryNCommands is 100, far more than the 3 rows this test inserts,
// so the row-count trigger alone would never fire the prune pass on
// insert-count grounds within this test — advancing the clock past
// pruneCheckInterval between inserts is what makes the AGE trigger fire
// and actually run pruneCommands, which then enforces maxCommandRows.
func TestPruneCommandsByRowCount(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxCommandRows(2), WithMaxCommandAge(0))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		id := idFor(i)
		if _, err := st.InsertCommand(ctx, CommandRecord{
			ID: id, IdempotencyKey: "idem-" + id, Action: "stop_playlist", State: "dispatched",
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		clock.advance(2 * time.Hour)
	}

	got, err := st.ListCommands(ctx, 0)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (maxCommandRows=2 keeps only the newest two — a prune pass must actually have fired)", len(got))
	}
}

// TestPruneCommandsByAge is TestPruneCommandsByRowCount's age-bound
// sibling, mirroring TestPruneAuditByAge exactly.
func TestPruneCommandsByAge(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxCommandAge(24*time.Hour), WithMaxCommandRows(1_000_000))
	ctx := context.Background()

	if _, err := st.InsertCommand(ctx, CommandRecord{
		ID: "cmd-old", IdempotencyKey: "idem-old", Action: "stop_playlist", State: "dispatched",
	}); err != nil {
		t.Fatalf("insert old: %v", err)
	}
	// Advance well past both maxCommandAge and pruneCheckInterval so the
	// next insert's age-triggered prune pass actually evicts the old row.
	clock.advance(48 * time.Hour)
	if _, err := st.InsertCommand(ctx, CommandRecord{
		ID: "cmd-new", IdempotencyKey: "idem-new", Action: "stop_playlist", State: "dispatched",
	}); err != nil {
		t.Fatalf("insert new: %v", err)
	}

	got, err := st.ListCommands(ctx, 0)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(got) != 1 || got[0].ID != "cmd-new" {
		t.Fatalf("got = %+v, want only the command younger than maxCommandAge", got)
	}
}

func TestUpdateCommandOutcomeOnlyTouchesGivenFields(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.InsertCommand(ctx, CommandRecord{
		ID: "cmd-1", IdempotencyKey: "idem-1", Action: "stop_playlist", State: "pending",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	now := st.now()
	if err := st.UpdateCommandOutcome(ctx, "cmd-1", CommandOutcomeUpdate{DispatchedAt: &now}); err != nil {
		t.Fatalf("update outcome: %v", err)
	}

	got, err := st.GetCommand(ctx, "cmd-1")
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if got.DispatchedAt == nil {
		t.Fatalf("DispatchedAt still nil after update")
	}
	if got.State != "pending" {
		t.Errorf("State = %q, want it unchanged (\"pending\")", got.State)
	}
	if got.ResolvedAt != nil {
		t.Errorf("ResolvedAt = %v, want nil (not part of this update)", got.ResolvedAt)
	}
}

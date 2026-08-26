package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestListAuditEntriesNewestFirstOpensOnTheMostRecentEntry is the whole
// point of the descending read: the first entry of an unbounded
// newest-first page is the newest retained row, reached in ONE query
// rather than after walking retained history forward.
func TestListAuditEntriesNewestFirstOpensOnTheMostRecentEntry(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxAuditRows(1_000_000), WithMaxAuditAge(0))
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: fmt.Sprintf("action-%02d", i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	got, err := st.ListAuditEntriesNewestFirst(ctx, 0, 2)
	if err != nil {
		t.Fatalf("list newest first: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Action != "action-04" || got[1].Action != "action-03" {
		t.Fatalf("got = [%s %s], want the two newest entries in descending order", got[0].Action, got[1].Action)
	}
	if got[0].ID <= got[1].ID {
		t.Errorf("ids = %d, %d; want strictly descending", got[0].ID, got[1].ID)
	}
}

// TestListAuditEntriesNewestFirstWalksAPrunedLogWithoutDuplicatesOrSkips
// is the load-bearing case: retention has already trimmed the oldest rows,
// so the lowest retained id is greater than zero. A backward walk that
// advances `before` on real ids must visit every retained entry exactly
// once and stop at the oldest retained id.
func TestListAuditEntriesNewestFirstWalksAPrunedLogWithoutDuplicatesOrSkips(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxAuditRows(30), WithMaxAuditAge(0))
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: fmt.Sprintf("action-%02d", i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		clock.advance(2 * time.Hour)
	}

	oldest, retained, err := st.OldestAuditID(ctx)
	if err != nil {
		t.Fatalf("oldest audit id: %v", err)
	}
	if !retained || oldest <= 1 {
		t.Fatalf("oldest retained id = %d (retained=%v), want a pruned log whose lowest id is above 1", oldest, retained)
	}

	forward, err := st.ListAuditEntries(ctx, 0, MaxAuditPageSize)
	if err != nil {
		t.Fatalf("list ascending: %v", err)
	}
	if len(forward) != 30 {
		t.Fatalf("retained rows = %d, want 30", len(forward))
	}

	var walked []AuditRecord
	seen := map[int64]bool{}
	before := int64(0)
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatalf("backward walk did not terminate after %d pages", page)
		}
		got, err := st.ListAuditEntriesNewestFirst(ctx, before, 7)
		if err != nil {
			t.Fatalf("list newest first (before=%d): %v", before, err)
		}
		if len(got) == 0 {
			break
		}
		for _, rec := range got {
			if seen[rec.ID] {
				t.Fatalf("id %d returned twice: a backward walk on real ids must never duplicate", rec.ID)
			}
			seen[rec.ID] = true
			walked = append(walked, rec)
		}
		before = got[len(got)-1].ID
	}

	if len(walked) != len(forward) {
		t.Fatalf("walked %d entries, want %d: the backward walk skipped or duplicated retained history", len(walked), len(forward))
	}
	for i, rec := range walked {
		want := forward[len(forward)-1-i]
		if rec.ID != want.ID || rec.Action != want.Action {
			t.Fatalf("walked[%d] = (id %d, %s), want (id %d, %s)", i, rec.ID, rec.Action, want.ID, want.Action)
		}
	}
	if walked[len(walked)-1].ID != oldest {
		t.Errorf("walk ended at id %d, want the oldest retained id %d", walked[len(walked)-1].ID, oldest)
	}
}

// TestAuditCountDerivedCursorRepeatsItselfOnAPrunedLog pins WHY the id is
// required rather than optional. It reproduces the abandoned approach
// (advance the cursor by the number of entries received) against the same
// pruned log and shows it re-reads entries it has already seen, forever:
// ids are never reused, so after a prune the count lags the true id
// permanently.
func TestAuditCountDerivedCursorRepeatsItselfOnAPrunedLog(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxAuditRows(30), WithMaxAuditAge(0))
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: fmt.Sprintf("action-%02d", i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		clock.advance(2 * time.Hour)
	}

	first, err := st.ListAuditEntries(ctx, 0, 10)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first) != 10 {
		t.Fatalf("len(first) = %d, want 10", len(first))
	}

	countCursor := int64(len(first))
	second, err := st.ListAuditEntries(ctx, countCursor, 10)
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second) == 0 {
		t.Fatalf("second page was empty; expected the count-derived cursor to land back inside retained history")
	}
	if second[0].ID > first[len(first)-1].ID {
		t.Fatalf("count-derived cursor advanced correctly (%d past %d); this test is meant to demonstrate that it does NOT",
			second[0].ID, first[len(first)-1].ID)
	}

	// The id cursor, from the same first page, is the fix: it advances
	// strictly past everything already returned.
	idCursor := first[len(first)-1].ID
	next, err := st.ListAuditEntries(ctx, idCursor, 10)
	if err != nil {
		t.Fatalf("list with id cursor: %v", err)
	}
	if len(next) > 0 && next[0].ID <= idCursor {
		t.Fatalf("id cursor returned id %d, want strictly greater than %d", next[0].ID, idCursor)
	}
}

func TestListAuditEntriesNewestFirstRejectsNegativeBefore(t *testing.T) {
	st := openTestStore(t, nil)
	if _, err := st.ListAuditEntriesNewestFirst(context.Background(), -1, 10); err == nil {
		t.Fatal("ListAuditEntriesNewestFirst(-1) = nil error, want a refusal")
	}
}

func TestListAuditEntriesNewestFirstClampsLimit(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxAuditRows(1_000_000), WithMaxAuditAge(0))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "x"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	got, err := st.ListAuditEntriesNewestFirst(ctx, 0, MaxAuditPageSize+1_000)
	if err != nil {
		t.Fatalf("list newest first: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (an over-large limit is clamped, not refused)", len(got))
	}
}

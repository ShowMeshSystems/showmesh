package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func mustEvent(t *testing.T, mutate func(*EventRecord)) EventRecord {
	t.Helper()
	ev := EventRecord{
		Source:   "mqtt-inventory",
		Resource: observation.ResourceRef{Kind: observation.ResourceNode, ID: "node-a"},
		Category: "control_plane",
		Severity: "informational",
		Summary:  "node control-plane state changed to offline",
	}
	if mutate != nil {
		mutate(&ev)
	}
	return ev
}

func TestAppendEventAssignsIncreasingSeqAndPreservesFields(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	correlation := "incident-1"

	occurredAt := mustTime(t, "2026-08-10T12:00:00Z")
	ev := mustEvent(t, func(e *EventRecord) {
		e.OccurredAt = &occurredAt
		e.Details = json.RawMessage(`{"from":"online","to":"offline"}`)
		e.CorrelationID = &correlation
		// Seq and RecordedAt are caller-ignored inputs; set them to prove
		// AppendEvent does not trust either.
		e.Seq = 999
	})

	seq1, err := st.AppendEvent(ctx, ev)
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	seq2, err := st.AppendEvent(ctx, mustEvent(t, nil))
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}
	if seq2 <= seq1 {
		t.Fatalf("seq2 = %d, want it strictly greater than seq1 = %d", seq2, seq1)
	}

	events, gap, err := st.ListEvents(ctx, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if gap {
		t.Errorf("gap = true, want false: since=0 on a fresh store")
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}

	got := events[0]
	if got.Seq != seq1 {
		t.Errorf("Seq = %d, want the assigned %d, not the caller-supplied 999", got.Seq, seq1)
	}
	if got.RecordedAt.IsZero() {
		t.Errorf("RecordedAt is zero, want the store's own clock stamped")
	}
	if got.OccurredAt == nil || !got.OccurredAt.Equal(occurredAt) {
		t.Errorf("OccurredAt = %v, want %v", got.OccurredAt, occurredAt)
	}
	if string(got.Details) != `{"from":"online","to":"offline"}` {
		t.Errorf("Details = %s, want the stored JSON preserved exactly", got.Details)
	}
	if got.CorrelationID == nil || *got.CorrelationID != correlation {
		t.Errorf("CorrelationID = %v, want %q", got.CorrelationID, correlation)
	}
}

// TestAppendEventDefaultsEmptyDetailsToJSONObject proves the wire contract
// (section 6.10: an event's details always renders as a JSON object, never
// null) is upheld at the storage layer: an EventRecord with no Details set
// at all must still read back as "{}", not an empty string and not SQL
// NULL.
func TestAppendEventDefaultsEmptyDetailsToJSONObject(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.AppendEvent(ctx, mustEvent(t, nil)); err != nil {
		t.Fatalf("append event: %v", err)
	}

	events, _, err := st.ListEvents(ctx, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if string(events[0].Details) != "{}" {
		t.Errorf("Details = %q, want {}", events[0].Details)
	}
}

func TestAppendEventRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*EventRecord)
		wantErr error
	}{
		{"missing source", func(e *EventRecord) { e.Source = "" }, ErrEventMissingSource},
		{"missing resource kind", func(e *EventRecord) { e.Resource.Kind = "" }, ErrEventMissingResource},
		{"missing resource id", func(e *EventRecord) { e.Resource.ID = "" }, ErrEventMissingResource},
		{"missing category", func(e *EventRecord) { e.Category = "" }, ErrEventMissingCategory},
		{"missing severity", func(e *EventRecord) { e.Severity = "" }, ErrEventMissingSeverity},
		{"missing summary", func(e *EventRecord) { e.Summary = "" }, ErrEventMissingSummary},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := openTestStore(t, nil)
			ctx := context.Background()

			_, err := st.AppendEvent(ctx, mustEvent(t, tc.mutate))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want it to wrap %v", err, tc.wantErr)
			}

			events, _, err := st.ListEvents(ctx, 0, 10)
			if err != nil {
				t.Fatalf("list events: %v", err)
			}
			if len(events) != 0 {
				t.Errorf("len(events) = %d, want 0: a rejected event must not be stored", len(events))
			}
		})
	}
}

func TestAppendEventRejectsOversizedOrInvalidDetails(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	oversized := make([]byte, MaxEventDetailsBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	_, err := st.AppendEvent(ctx, mustEvent(t, func(e *EventRecord) { e.Details = json.RawMessage(oversized) }))
	if !errors.Is(err, ErrEventDetailsTooLarge) {
		t.Errorf("oversized details: error = %v, want it to wrap ErrEventDetailsTooLarge", err)
	}

	_, err = st.AppendEvent(ctx, mustEvent(t, func(e *EventRecord) { e.Details = json.RawMessage(`{not valid json`) }))
	if !errors.Is(err, ErrEventDetailsInvalidJSON) {
		t.Errorf("invalid json details: error = %v, want it to wrap ErrEventDetailsInvalidJSON", err)
	}
}

func TestListEventsSinceExcludesEarlierEventsAndRespectsLimit(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	var seqs []int64
	for i := 0; i < 5; i++ {
		seq, err := st.AppendEvent(ctx, mustEvent(t, nil))
		if err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
		seqs = append(seqs, seq)
	}

	events, gap, err := st.ListEvents(ctx, seqs[1], 2)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if gap {
		t.Errorf("gap = true, want false")
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2 (limit enforced)", len(events))
	}
	if events[0].Seq != seqs[2] || events[1].Seq != seqs[3] {
		t.Errorf("events = %+v, want seq %d then %d", events, seqs[2], seqs[3])
	}
}

func TestListEventsClampsLimitToMaxEventsPageSize(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.AppendEvent(ctx, mustEvent(t, nil)); err != nil {
		t.Fatalf("append event: %v", err)
	}

	// Requesting far more than MaxEventsPageSize must not error; it is
	// silently clamped (see ListEvents's doc comment for why clamping,
	// not rejecting, is the chosen behavior).
	events, _, err := st.ListEvents(ctx, 0, MaxEventsPageSize*10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
}

func TestListEventsRejectsNegativeSince(t *testing.T) {
	st := openTestStore(t, nil)
	if _, _, err := st.ListEvents(context.Background(), -1, 10); err == nil {
		t.Errorf("ListEvents with since=-1 succeeded, want an error")
	}
}

// TestListEventsReportsGapAfterRowCountPruning is the store-level proof of
// the Step 3 task spec's requirement that pruning "must never delete an
// event that a caller is mid-page through in a way that produces a
// silent gap." Retention here is squeezed to 2 rows, and exactly
// pruneEveryNEvents events are appended so the final append is itself the
// one that triggers the amortized prune pass (see pruneEveryNEvents in
// retention.go) — deliberately an exact multiple, so the row count right
// after the loop is deterministic rather than "somewhere between
// maxEventRows and maxEventRows+pruneEveryNEvents-1", which is what it
// would be for a non-multiple total; amortized pruning only trims the
// table when it runs, not continuously, so a total a few events past a
// multiple would still legitimately hold more than WithMaxEventRows rows
// until the next pass. Once pruned, a caller resuming from the very first
// seq must see gap=true rather than a response indistinguishable from "no
// new events happened."
func TestListEventsReportsGapAfterRowCountPruning(t *testing.T) {
	st := openTestStore(t, nil, WithMaxEventRows(2), WithMaxEventAge(0))
	ctx := context.Background()

	const total = pruneEveryNEvents
	var firstSeq int64
	for i := 0; i < total; i++ {
		seq, err := st.AppendEvent(ctx, mustEvent(t, nil))
		if err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
		if i == 0 {
			firstSeq = seq
		}
	}

	events, gap, err := st.ListEvents(ctx, firstSeq, MaxEventsPageSize)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if !gap {
		t.Errorf("gap = false, want true: events after seq %d were pruned before this call", firstSeq)
	}
	if len(events) > 2 {
		t.Errorf("len(events) = %d, want at most 2 (WithMaxEventRows(2), and total is an exact multiple of pruneEveryNEvents)", len(events))
	}

	// since=0 must never report a gap, regardless of how much history has
	// already been pruned away: a caller with no prior cursor has nothing
	// that was "torn out from under it".
	_, gapFromZero, err := st.ListEvents(ctx, 0, MaxEventsPageSize)
	if err != nil {
		t.Fatalf("list events from zero: %v", err)
	}
	if gapFromZero {
		t.Errorf("gap = true for since=0, want false")
	}
}

// TestPruneEventsEnforcesRowCountBound proves the row-count bound is
// actually enforced across many inserts, not merely accepted as
// configuration: after appending several exact multiples of
// pruneEveryNEvents (so, as in TestListEventsReportsGapAfterRowCountPruning
// above, the final append is itself the triggering one and the resulting
// row count is deterministic rather than "up to pruneEveryNEvents-1 rows
// over the bound"), the table must never be observed holding more than
// that bound (checked via LatestEventSeq/ListEvents, the only surface this
// package exposes — no direct SQL from the test).
func TestPruneEventsEnforcesRowCountBound(t *testing.T) {
	const maxRows = 3
	st := openTestStore(t, nil, WithMaxEventRows(maxRows), WithMaxEventAge(0))
	ctx := context.Background()

	const total = pruneEveryNEvents * 3
	for i := 0; i < total; i++ {
		if _, err := st.AppendEvent(ctx, mustEvent(t, nil)); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	events, _, err := st.ListEvents(ctx, 0, MaxEventsPageSize)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) > maxRows {
		t.Errorf("len(events) = %d, want at most %d after pruning", len(events), maxRows)
	}

	latest, err := st.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("latest event seq: %v", err)
	}
	if latest != int64(total) {
		t.Errorf("LatestEventSeq = %d, want %d (the true high-water mark, unaffected by pruning)", latest, total)
	}
}

// TestLatestEventSeqSurvivesEventsBeingFullyDeleted proves LatestEventSeq
// reads sqlite_sequence rather than MAX(seq): if it read MAX(seq), an
// empty events table would make it silently report 0, understating the
// true high-water mark and letting a client that resumes from 0 replay
// events it has already seen. This package's own amortized pruning never
// actually empties the table (see LatestEventSeq's doc comment in
// events.go for why), so this test empties it directly to exercise the
// invariant LatestEventSeq's implementation depends on, rather than
// assuming the current pruning code path is the only way that could ever
// happen.
func TestLatestEventSeqSurvivesEventsBeingFullyDeleted(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	const total = 5
	var lastSeq int64
	for i := 0; i < total; i++ {
		seq, err := st.AppendEvent(ctx, mustEvent(t, nil))
		if err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
		lastSeq = seq
	}

	if _, err := st.db.ExecContext(ctx, `DELETE FROM events`); err != nil {
		t.Fatalf("delete all events: %v", err)
	}

	var remaining int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&remaining); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining = %d, want 0 (test setup failed to empty the table)", remaining)
	}

	latest, err := st.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("latest event seq: %v", err)
	}
	if latest != lastSeq {
		t.Errorf("LatestEventSeq = %d, want %d (the pre-deletion high-water mark)", latest, lastSeq)
	}
}

func TestLatestEventSeqZeroBeforeAnyEvent(t *testing.T) {
	st := openTestStore(t, nil)
	latest, err := st.LatestEventSeq(context.Background())
	if err != nil {
		t.Fatalf("latest event seq: %v", err)
	}
	if latest != 0 {
		t.Errorf("LatestEventSeq = %d, want 0 before any event has been appended", latest)
	}
}

func TestOldestEventSeqEmptyBeforeAnyEvent(t *testing.T) {
	st := openTestStore(t, nil)
	oldest, ok, err := st.OldestEventSeq(context.Background())
	if err != nil {
		t.Fatalf("oldest event seq: %v", err)
	}
	if ok || oldest != 0 {
		t.Errorf("OldestEventSeq = (%d, %v), want (0, false) before any event has been appended", oldest, ok)
	}
}

func TestOldestEventSeqReflectsPruning(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	var firstSeq, lastSeq int64
	for i := 0; i < 5; i++ {
		seq, err := st.AppendEvent(ctx, mustEvent(t, nil))
		if err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
		if i == 0 {
			firstSeq = seq
		}
		lastSeq = seq
	}

	oldest, ok, err := st.OldestEventSeq(ctx)
	if err != nil {
		t.Fatalf("oldest event seq: %v", err)
	}
	if !ok || oldest != firstSeq {
		t.Errorf("OldestEventSeq = (%d, %v), want (%d, true) before any pruning", oldest, ok, firstSeq)
	}

	// Simulate pruning down to the newest row only, the same shape
	// pruneEvents' row-count bound leaves behind.
	if _, err := st.db.ExecContext(ctx, `DELETE FROM events WHERE seq < ?`, lastSeq); err != nil {
		t.Fatalf("delete older events: %v", err)
	}

	oldest, ok, err = st.OldestEventSeq(ctx)
	if err != nil {
		t.Fatalf("oldest event seq after pruning: %v", err)
	}
	if !ok || oldest != lastSeq {
		t.Errorf("OldestEventSeq = (%d, %v), want (%d, true) after pruning down to the newest row", oldest, ok, lastSeq)
	}

	if _, err := st.db.ExecContext(ctx, `DELETE FROM events`); err != nil {
		t.Fatalf("delete all remaining events: %v", err)
	}
	oldest, ok, err = st.OldestEventSeq(ctx)
	if err != nil {
		t.Fatalf("oldest event seq after emptying the table: %v", err)
	}
	if ok || oldest != 0 {
		t.Errorf("OldestEventSeq = (%d, %v), want (0, false) once the table is fully empty", oldest, ok)
	}
}

// TestAppendEventEnforcesAgeBoundAcrossSparseInserts is the regression
// guard for Step 3 review finding 3.5: pruneEveryNEvents alone ties every
// prune pass to insert volume, so a coordinator receiving only a handful
// of events over a long stretch of wall-clock time — restarted between
// shows, exactly the scenario the finding names — could go a very long
// time before pruneEveryNEvents more inserts ever accumulated, during
// which every event past DefaultMaxEventAge just sat there unpruned. This
// proves the second, time-based trigger (pruneCheckInterval) actually
// closes that gap: two inserts, far apart in wall-clock time and nowhere
// near pruneEveryNEvents apart in count, and the second one must still
// prune the first once it has aged past maxEventAge — the row-count
// trigger can never fire here (there is only ever one more insert after
// the first), so if anything prunes it, it can only be the time-based one.
func TestAppendEventEnforcesAgeBoundAcrossSparseInserts(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	const maxAge = 30 * 24 * time.Hour
	st := openTestStore(t, clock, WithMaxEventAge(maxAge), WithMaxEventRows(1_000_000))
	ctx := context.Background()

	if _, err := st.AppendEvent(ctx, mustEvent(t, nil)); err != nil {
		t.Fatalf("append first event: %v", err)
	}

	clock.advance(maxAge + pruneCheckInterval + time.Hour)

	if _, err := st.AppendEvent(ctx, mustEvent(t, nil)); err != nil {
		t.Fatalf("append second event: %v", err)
	}

	events, _, err := st.ListEvents(ctx, 0, MaxEventsPageSize)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want exactly 1: the first event aged past maxEventAge and should have been pruned by the second append, well before pruneEveryNEvents more inserts ever happened", len(events))
	}
	if events[0].Seq != 2 {
		t.Errorf("surviving event Seq = %d, want 2 (the second, not-yet-aged event)", events[0].Seq)
	}
}

// TestAppendEventTimeBasedPruneTriggerSurvivesRestart proves the
// restart-forgets-lastPruneAtNanos behavior store.go's field doc comment
// describes as safe actually is safe: re-opening a Store against the same
// data directory (simulating a coordinator restart between shows) resets
// lastPruneAtNanos to zero, and the very first AppendEvent call after such
// a restart must still enforce the age bound immediately — not wait out a
// fresh pruneCheckInterval measured from the new process's own start,
// which would just reproduce the bug finding 3.5 describes one level down.
func TestAppendEventTimeBasedPruneTriggerSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	const maxAge = 30 * 24 * time.Hour
	ctx := context.Background()

	st1, err := open(ctx, dir, nil, clock.now, WithMaxEventAge(maxAge), WithMaxEventRows(1_000_000))
	if err != nil {
		t.Fatalf("open before restart: %v", err)
	}
	if _, err := st1.AppendEvent(ctx, mustEvent(t, nil)); err != nil {
		t.Fatalf("append event before restart: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}

	// Simulate the coordinator being down for a while, then restarted: a
	// brand new *Store against the same data directory (so
	// eventAppendCount and lastPruneAtNanos are both back to their zero
	// values), well past maxAge later.
	clock.advance(maxAge + time.Hour)
	st2, err := open(ctx, dir, nil, clock.now, WithMaxEventAge(maxAge), WithMaxEventRows(1_000_000))
	if err != nil {
		t.Fatalf("re-open after restart: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	if _, err := st2.AppendEvent(ctx, mustEvent(t, nil)); err != nil {
		t.Fatalf("append event after restart: %v", err)
	}

	events, _, err := st2.ListEvents(ctx, 0, MaxEventsPageSize)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want exactly 1: the pre-restart event was already past maxEventAge and the first post-restart append must prune it immediately", len(events))
	}
	if events[0].Seq != 2 {
		t.Errorf("surviving event Seq = %d, want 2 (the post-restart event)", events[0].Seq)
	}
}

// TestAppendEventAndPruneShareOneTransaction is a narrower regression
// guard than a full crash-injection test (which this package has no way
// to do against modernc.org/sqlite): it at least proves a failing prune
// pass does not leave a "half applied" AppendEvent behind by checking the
// ordinary, successful path commits both together — a pruning bug that
// silently skipped rollback on error would still need a fault injection
// harness to catch directly, which is out of scope for this task; noted
// in the task report rather than asserted here as tested.
func TestAppendEventAndPruneShareOneTransaction(t *testing.T) {
	st := openTestStore(t, nil, WithMaxEventRows(1), WithMaxEventAge(0))
	ctx := context.Background()

	for i := 0; i < pruneEveryNEvents; i++ {
		if _, err := st.AppendEvent(ctx, mustEvent(t, nil)); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	// The pruneEveryNEvents-th append must have triggered a prune pass
	// (WithMaxEventRows(1)) and still have committed its own row.
	events, _, err := st.ListEvents(ctx, 0, MaxEventsPageSize)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want exactly 1 (the row that triggered pruning must survive its own prune pass)", len(events))
	}

	latest, err := st.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("latest event seq: %v", err)
	}
	if latest != pruneEveryNEvents {
		t.Errorf("LatestEventSeq = %d, want %d", latest, pruneEveryNEvents)
	}
}

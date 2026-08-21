package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestNightCueOutboxDuplicateIdentityFails is RESTING-MODE.md §7.1.1's own
// proof requirement: "the UNIQUE constraint on the invocation identity is
// the mechanism, not a nicety — write the test that proves a second insert
// with the same identity fails." This test fails if
// night_cue_outbox_identity's UNIQUE index is ever dropped or narrowed —
// verified by temporarily removing the index from schemaV10 and confirming
// this test then fails (the second insert succeeds). Phase is part of the
// identity (review finding: enterShow and enterResting are separately
// validated lists that may legitimately share a cue name, and a shared
// name without phase in the key let one list silently resolve the other's
// row) — the same-name-different-phase case below proves that half.
func TestNightCueOutboxDuplicateIdentityFails(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	now := time.Now()

	rec := NightCueOutboxRecord{
		ID: "cue-1", SessionID: "session-1", Cycle: 1, Phase: "enterShow", CueName: "lighting-fade-in",
		ActionRevision: 1, State: "pending",
	}
	if err := st.InsertNightCueOutboxRow(ctx, rec, now); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	dup := rec
	dup.ID = "cue-2" // different row id, SAME (session, cycle, phase, cue) identity
	err := st.InsertNightCueOutboxRow(ctx, dup, now)
	if err == nil {
		t.Fatalf("second insert with the same (session, cycle, phase, cue) identity succeeded, want ErrNightCueOutboxDuplicate")
	}
	if !errors.Is(err, ErrNightCueOutboxDuplicate) {
		t.Fatalf("error = %v, want it to wrap ErrNightCueOutboxDuplicate", err)
	}

	// A different cue name in the same session/cycle/phase, a different
	// cycle for the same cue name, or the SAME cue name in a DIFFERENT
	// phase, are all DIFFERENT identities and must succeed — the index
	// guards the full (session, cycle, phase, cue) tuple, not any subset.
	otherCue := rec
	otherCue.ID = "cue-3"
	otherCue.CueName = "audio-fade-in"
	if err := st.InsertNightCueOutboxRow(ctx, otherCue, now); err != nil {
		t.Fatalf("insert with a different cue name in the same session/cycle/phase: %v", err)
	}
	otherCycle := rec
	otherCycle.ID = "cue-4"
	otherCycle.Cycle = 2
	if err := st.InsertNightCueOutboxRow(ctx, otherCycle, now); err != nil {
		t.Fatalf("insert with the same cue name in a different cycle: %v", err)
	}
	otherPhase := rec
	otherPhase.ID = "cue-5"
	otherPhase.Phase = "enterResting"
	if err := st.InsertNightCueOutboxRow(ctx, otherPhase, now); err != nil {
		t.Fatalf("insert with the same cue name in a different phase: %v", err)
	}
}

func TestNightSessionCreateGetUpdateRoundTrip(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	rec := NightSessionRecord{
		ID: "session-1", ConfigObjectID: "christmas-2026", ConfigRevision: 3,
		State: "preparing", StateEnteredAt: now,
	}
	if err := st.CreateNightSession(ctx, rec, now); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetNightSession(ctx, "session-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != "preparing" || got.ConfigObjectID != "christmas-2026" || got.ConfigRevision != 3 {
		t.Fatalf("got = %+v, want the just-created fields back", got)
	}
	if got.FinalShowRequestedAt != nil || got.AdmissionClosedAt != nil {
		t.Fatalf("got = %+v, want both nullable timestamps nil on a freshly created row", got)
	}

	current, ok, err := st.GetCurrentNightSession(ctx)
	if err != nil || !ok {
		t.Fatalf("get current: ok=%v err=%v", ok, err)
	}
	if current.ID != "session-1" {
		t.Fatalf("current.ID = %q, want session-1", current.ID)
	}

	got.State = "preshow"
	got.AdmissionClosed = true
	closedAt := now.Add(time.Minute)
	got.AdmissionClosedAt = &closedAt
	if err := st.UpdateNightSession(ctx, got, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("update: %v", err)
	}

	updated, err := st.GetNightSession(ctx, "session-1")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if updated.State != "preshow" || !updated.AdmissionClosed {
		t.Fatalf("updated = %+v, want state=preshow admission_closed=true", updated)
	}
	if updated.AdmissionClosedAt == nil || !updated.AdmissionClosedAt.Equal(closedAt) {
		t.Fatalf("updated.AdmissionClosedAt = %v, want %v", updated.AdmissionClosedAt, closedAt)
	}
}

func TestGetCurrentNightSessionNoSessionReturnsFalse(t *testing.T) {
	st := openTestStore(t, nil)
	_, ok, err := st.GetCurrentNightSession(context.Background())
	if err != nil {
		t.Fatalf("get current with no session ever created: %v", err)
	}
	if ok {
		t.Fatalf("ok = true with no session ever created, want false")
	}
}

func TestUpdateNightSessionUnknownIDReturnsNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	err := st.UpdateNightSession(context.Background(), NightSessionRecord{ID: "does-not-exist", State: "preparing"}, time.Now())
	if !errors.Is(err, ErrNightSessionNotFound) {
		t.Fatalf("error = %v, want ErrNightSessionNotFound", err)
	}
}

func TestNightReadinessLatestWinsOverEarlier(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	if err := st.CreateNightReadiness(ctx, NightReadinessRecord{
		ID: "r1", SessionID: "session-1", EpochID: "session-1", CompletedAt: base, Outcome: "unknown", ChecksJSON: "[]",
	}); err != nil {
		t.Fatalf("create r1: %v", err)
	}
	if err := st.CreateNightReadiness(ctx, NightReadinessRecord{
		ID: "r2", SessionID: "session-1", EpochID: "session-1", CompletedAt: base.Add(time.Minute), Outcome: "ready", ChecksJSON: "[]",
	}); err != nil {
		t.Fatalf("create r2: %v", err)
	}

	latest, err := st.GetLatestNightReadiness(ctx, "session-1")
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest.ID != "r2" || latest.Outcome != "ready" {
		t.Fatalf("latest = %+v, want r2/ready (the newer completed_at)", latest)
	}
}

func TestGetLatestNightReadinessNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.GetLatestNightReadiness(context.Background(), "no-such-session")
	if !errors.Is(err, ErrNightReadinessNotFound) {
		t.Fatalf("error = %v, want ErrNightReadinessNotFound", err)
	}
}

func TestGetNightSessionByIdempotencyKeyRoundTrip(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	now := time.Now()

	rec := NightSessionRecord{
		ID: "session-1", State: "preparing", StateEnteredAt: now,
		PrepareSiteIdempotencyKey: "retry-key-1",
	}
	if err := st.CreateNightSession(ctx, rec, now); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetNightSessionByIdempotencyKey(ctx, "retry-key-1")
	if err != nil {
		t.Fatalf("get by idempotency key: %v", err)
	}
	if got.ID != "session-1" {
		t.Fatalf("got.ID = %q, want session-1", got.ID)
	}

	if _, err := st.GetNightSessionByIdempotencyKey(ctx, "no-such-key"); !errors.Is(err, ErrNightSessionNotFound) {
		t.Fatalf("error for unknown key = %v, want ErrNightSessionNotFound", err)
	}
}

func TestNightSessionAttributionDegradedRoundTrips(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	now := time.Now()

	rec := NightSessionRecord{ID: "session-1", State: "preparing", StateEnteredAt: now}
	if err := st.CreateNightSession(ctx, rec, now); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.GetNightSession(ctx, "session-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AttributionDegraded {
		t.Fatalf("AttributionDegraded on a fresh session = true, want false")
	}

	got.AttributionDegraded = true
	if err := st.UpdateNightSession(ctx, got, now); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, err := st.GetNightSession(ctx, "session-1")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if !got2.AttributionDegraded {
		t.Fatalf("AttributionDegraded after update = false, want true")
	}
}

// TestNightSessionTxMethodsShareOneTransaction proves the [Tx] forms this
// seam's review finding 2 depends on: a read, a decision, and a write
// inside one [Store.InTx] call, using the SAME session id throughout.
func TestNightSessionTxMethodsShareOneTransaction(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	now := time.Now()

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		_, ok, err := tx.GetCurrentNightSession(ctx)
		if err != nil {
			return err
		}
		if ok {
			t.Fatalf("GetCurrentNightSession inside a fresh transaction found a session that was never created")
		}
		rec := NightSessionRecord{ID: "session-1", State: "preparing", StateEnteredAt: now}
		if err := tx.CreateNightSession(ctx, rec, now); err != nil {
			return err
		}
		current, ok, err := tx.GetCurrentNightSession(ctx)
		if err != nil {
			return err
		}
		if !ok || current.ID != "session-1" {
			t.Fatalf("GetCurrentNightSession inside the SAME transaction did not see the just-created row: ok=%v current=%+v", ok, current)
		}
		current.State = "preshow"
		return tx.UpdateNightSession(ctx, current, now)
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	got, err := st.GetNightSession(ctx, "session-1")
	if err != nil {
		t.Fatalf("get after commit: %v", err)
	}
	if got.State != "preshow" {
		t.Fatalf("state after committed transaction = %q, want preshow", got.State)
	}
}

// TestNightSessionTxRollsBackOnError proves the OTHER half: a non-nil
// return from the InTx closure must roll back everything, including a
// CreateNightSession the same closure already ran.
func TestNightSessionTxRollsBackOnError(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	now := time.Now()

	injected := errors.New("injected failure")
	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		rec := NightSessionRecord{ID: "session-1", State: "preparing", StateEnteredAt: now}
		if err := tx.CreateNightSession(ctx, rec, now); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("InTx error = %v, want it to wrap the injected error", err)
	}

	if _, ok, err := st.GetCurrentNightSession(ctx); err != nil || ok {
		t.Fatalf("session created inside a ROLLED-BACK transaction is still visible: ok=%v err=%v", ok, err)
	}
}

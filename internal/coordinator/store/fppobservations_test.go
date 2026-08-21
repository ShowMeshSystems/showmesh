package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestFPPPlaylistEntryObservationSchemaVersionIsV14 pins this seam's own
// migration number against a hardcoded expectation (not just "agrees with
// maxMigrationVersion()", which store_test.go's general tests already
// cover): versions 9 through 13 are reserved by other, not-yet-merged
// branches per docs/build/IDENTIFIER-REGISTER.md, so this fails loudly if
// schemaV14's entry is ever renumbered to close that gap.
func TestFPPPlaylistEntryObservationSchemaVersionIsV14(t *testing.T) {
	st := openTestStore(t, nil)
	var version int
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 14 {
		t.Fatalf("user_version = %d, want 14", version)
	}
	if got := maxMigrationVersion(); got != 14 {
		t.Fatalf("maxMigrationVersion() = %d, want 14", got)
	}
}

func fppObservationFixture(instanceUUID string, sequence int64) FPPPlaylistEntryObservationRecord {
	return FPPPlaylistEntryObservationRecord{
		InstanceUUID:                       instanceUUID,
		SchemaVersion:                      1,
		Sequence:                           sequence,
		BodyHash:                           "hash-" + instanceUUID,
		ObservationJSON:                    `{"instanceUuid":"` + instanceUUID + `"}`,
		PlaylistName:                       "Christmas 2026",
		PlaylistHash:                       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Section:                            "mainPlaylist",
		Position:                           3,
		EntryKey:                           "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SequenceFilename:                   "show.fseq",
		MediaFilename:                      "show.mp4",
		Action:                             "playing",
		Unavailable:                        "",
		ObservedAt:                         time.UnixMilli(1_700_000_000_123).UTC(),
		CoalescedSincePreviousAcknowledged: 0,
		ReceivedAt:                         time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC),
	}
}

func TestFPPPlaylistEntryObservationPutGetRoundTrip(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	rec := fppObservationFixture("instance-1", 1)

	if err := st.PutFPPPlaylistEntryObservation(ctx, rec); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != rec {
		t.Fatalf("round trip mismatch:\n got  = %+v\n want = %+v", got, rec)
	}
}

func TestFPPPlaylistEntryObservationHigherSequenceReplaces(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first := fppObservationFixture("instance-1", 1)
	if err := st.PutFPPPlaylistEntryObservation(ctx, first); err != nil {
		t.Fatalf("put first: %v", err)
	}

	second := fppObservationFixture("instance-1", 2)
	second.Action = "stop"
	second.BodyHash = "hash-second"
	if err := st.PutFPPPlaylistEntryObservation(ctx, second); err != nil {
		t.Fatalf("put second: %v", err)
	}

	got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Sequence != 2 || got.Action != "stop" || got.BodyHash != "hash-second" {
		t.Fatalf("got = %+v, want the second (higher-sequence) observation", got)
	}
}

func TestFPPPlaylistEntryObservationLowerSequenceIsStaleAndLeavesRowUntouched(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first := fppObservationFixture("instance-1", 5)
	if err := st.PutFPPPlaylistEntryObservation(ctx, first); err != nil {
		t.Fatalf("put first: %v", err)
	}

	lower := fppObservationFixture("instance-1", 3)
	err := st.PutFPPPlaylistEntryObservation(ctx, lower)
	if !errors.Is(err, ErrFPPPlaylistEntryObservationStale) {
		t.Fatalf("error = %v, want ErrFPPPlaylistEntryObservationStale", err)
	}

	got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != first {
		t.Fatalf("stored row changed after a stale put:\n got  = %+v\n want = %+v", got, first)
	}
}

func TestFPPPlaylistEntryObservationEqualSequenceIsConflictAndLeavesRowUntouched(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first := fppObservationFixture("instance-1", 5)
	if err := st.PutFPPPlaylistEntryObservation(ctx, first); err != nil {
		t.Fatalf("put first: %v", err)
	}

	same := fppObservationFixture("instance-1", 5)
	same.Action = "stop" // a different body at the SAME sequence
	err := st.PutFPPPlaylistEntryObservation(ctx, same)
	if !errors.Is(err, ErrFPPPlaylistEntryObservationSequenceConflict) {
		t.Fatalf("error = %v, want ErrFPPPlaylistEntryObservationSequenceConflict", err)
	}

	got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != first {
		t.Fatalf("stored row changed after a same-sequence conflict:\n got  = %+v\n want = %+v", got, first)
	}
}

func TestGetFPPPlaylistEntryObservationUnknownInstanceIsNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.GetFPPPlaylistEntryObservation(context.Background(), "no-such-instance")
	if !errors.Is(err, ErrFPPPlaylistEntryObservationNotFound) {
		t.Fatalf("error = %v, want ErrFPPPlaylistEntryObservationNotFound", err)
	}
}

func TestListFPPPlaylistEntryObservationsOrderedAndEmptyOnFreshStore(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	empty, err := st.ListFPPPlaylistEntryObservations(ctx)
	if err != nil {
		t.Fatalf("list on fresh store: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("list on fresh store = %+v, want empty", empty)
	}

	// Inserted out of instance_uuid order, so a correct ORDER BY is what
	// actually proves the ordering rather than insertion order coinciding
	// with it by accident.
	for _, uuid := range []string{"instance-c", "instance-a", "instance-b"} {
		if err := st.PutFPPPlaylistEntryObservation(ctx, fppObservationFixture(uuid, 1)); err != nil {
			t.Fatalf("put %q: %v", uuid, err)
		}
	}

	list, err := st.ListFPPPlaylistEntryObservations(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len(list) = %d, want 3", len(list))
	}
	want := []string{"instance-a", "instance-b", "instance-c"}
	for i, w := range want {
		if list[i].InstanceUUID != w {
			t.Fatalf("list[%d].InstanceUUID = %q, want %q (list not ordered by instance_uuid)", i, list[i].InstanceUUID, w)
		}
	}
}

func TestDeleteFPPPlaylistEntryObservation(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if deleted, err := st.DeleteFPPPlaylistEntryObservation(ctx, "no-such-instance"); err != nil || deleted {
		t.Fatalf("delete unknown instance: deleted=%v err=%v, want deleted=false err=nil", deleted, err)
	}

	if err := st.PutFPPPlaylistEntryObservation(ctx, fppObservationFixture("instance-1", 1)); err != nil {
		t.Fatalf("put: %v", err)
	}
	deleted, err := st.DeleteFPPPlaylistEntryObservation(ctx, "instance-1")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatalf("deleted = false, want true")
	}

	if _, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-1"); !errors.Is(err, ErrFPPPlaylistEntryObservationNotFound) {
		t.Fatalf("get after delete: error = %v, want ErrFPPPlaylistEntryObservationNotFound", err)
	}

	// The recovery path §1.5 describes: once cleared, a plugin restarting
	// its own sequence at 0 (or anything else) is accepted again, because
	// there is no longer a stored sequence to regress against.
	restarted := fppObservationFixture("instance-1", 0)
	if err := st.PutFPPPlaylistEntryObservation(ctx, restarted); err != nil {
		t.Fatalf("put after delete (simulated plugin restart): %v", err)
	}
}

func TestFPPPlaylistEntryObservationInstancesAreIndependent(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if err := st.PutFPPPlaylistEntryObservation(ctx, fppObservationFixture("instance-a", 10)); err != nil {
		t.Fatalf("put instance-a: %v", err)
	}
	// instance-b starts at a LOWER sequence than instance-a's current one;
	// this must succeed, because monotonicity is scoped per instance_uuid,
	// never global.
	if err := st.PutFPPPlaylistEntryObservation(ctx, fppObservationFixture("instance-b", 1)); err != nil {
		t.Fatalf("put instance-b with a sequence lower than instance-a's: %v", err)
	}

	// A stale/conflicting put against instance-b must not touch instance-a.
	stale := fppObservationFixture("instance-b", 0)
	if err := st.PutFPPPlaylistEntryObservation(ctx, stale); !errors.Is(err, ErrFPPPlaylistEntryObservationStale) {
		t.Fatalf("put stale instance-b: error = %v, want ErrFPPPlaylistEntryObservationStale", err)
	}

	a, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-a")
	if err != nil {
		t.Fatalf("get instance-a: %v", err)
	}
	if a.Sequence != 10 {
		t.Fatalf("instance-a.Sequence = %d, want 10 (unaffected by instance-b's write)", a.Sequence)
	}

	if _, err := st.DeleteFPPPlaylistEntryObservation(ctx, "instance-a"); err != nil {
		t.Fatalf("delete instance-a: %v", err)
	}
	b, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-b")
	if err != nil {
		t.Fatalf("instance-b still findable after deleting instance-a: %v", err)
	}
	if b.Sequence != 1 {
		t.Fatalf("instance-b.Sequence = %d, want 1 (unaffected by instance-a's delete)", b.Sequence)
	}
}

// TestFPPPlaylistEntryObservationTxAndStoreFormsAgree proves the [Tx] form
// runs the identical SQL its [Store] sibling does: a put through the Tx
// form inside [Store.InTx], visible to a read through the SAME Tx before
// commit, and to the plain Store form after commit, including the
// monotonicity refusal reproduced identically through the Tx form.
func TestFPPPlaylistEntryObservationTxAndStoreFormsAgree(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	rec := fppObservationFixture("instance-1", 1)

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.PutFPPPlaylistEntryObservation(ctx, rec); err != nil {
			return err
		}
		got, err := tx.GetFPPPlaylistEntryObservation(ctx, "instance-1")
		if err != nil {
			return err
		}
		if got != rec {
			t.Fatalf("read inside the same transaction did not see the just-written row: got = %+v", got)
		}
		list, err := tx.ListFPPPlaylistEntryObservations(ctx)
		if err != nil {
			return err
		}
		if len(list) != 1 || list[0] != rec {
			t.Fatalf("list inside the same transaction = %+v, want exactly [rec]", list)
		}
		stale := fppObservationFixture("instance-1", 0)
		if err := tx.PutFPPPlaylistEntryObservation(ctx, stale); !errors.Is(err, ErrFPPPlaylistEntryObservationStale) {
			t.Fatalf("stale put through the Tx form: error = %v, want ErrFPPPlaylistEntryObservationStale", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-1")
	if err != nil {
		t.Fatalf("get after commit through the Store form: %v", err)
	}
	if got != rec {
		t.Fatalf("Store-form read after commit = %+v, want %+v", got, rec)
	}

	err = st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.DeleteFPPPlaylistEntryObservation(ctx, "instance-1")
		return err
	})
	if err != nil {
		t.Fatalf("delete through Tx form: %v", err)
	}
	if _, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-1"); !errors.Is(err, ErrFPPPlaylistEntryObservationNotFound) {
		t.Fatalf("get after Tx-form delete committed: error = %v, want ErrFPPPlaylistEntryObservationNotFound", err)
	}
}

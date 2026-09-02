package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestFPPPlaylistEntryObservationSchemaVersionIsV14 pins this seam's own
// migration number against a hardcoded expectation (not just "exists in
// [migrations]", which store_test.go's general tests already cover):
// versions 11 through 13 are reserved by other, not-yet-merged branches
// per docs/build/IDENTIFIER-REGISTER.md, so this fails loudly if
// schemaV14's entry is ever renumbered to close that gap. It no longer
// asserts schemaV14 is the newest migration — Track H seam H2 took v15
// on top of it (see fppplaylistdefinitions_test.go's identical pin) — a
// database's stamped user_version is a maximum over every migration that
// has ever shipped, not this one seam's own number.
func TestFPPPlaylistEntryObservationSchemaVersionIsV14(t *testing.T) {
	found := false
	for _, m := range migrations {
		if m.version == 14 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no migration entry has version 14 — schemaV14 was renumbered")
	}

	// Being LISTED in [migrations] is not the same as having actually
	// applied against a real database: probe sqlite_master and
	// pragma_table_info on a freshly opened store, which applies every
	// migration in order, so this pins that the table exists and has this
	// shape after the full migration chain, mirroring store_test.go's own
	// sqlite_master/pragma_table_info migration-pin pattern. It does not
	// pin that migration 14 specifically is what created the table.
	st := openTestStore(t, nil)
	var tableName string
	if err := st.db.QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='table' AND name = 'fpp_playlist_entry_observations'`,
	).Scan(&tableName); err != nil {
		t.Fatalf("table fpp_playlist_entry_observations missing: %v", err)
	}

	for _, col := range []string{
		"instance_uuid", "schema_version", "sequence", "body_hash",
		"observation_json", "playlist_name", "playlist_hash", "section",
		"position", "entry_key", "sequence_filename", "media_filename",
		"action", "unavailable", "observed_at_millis",
		"coalesced_since_previous_acknowledged", "received_at",
	} {
		var name string
		err := st.db.QueryRowContext(context.Background(),
			`SELECT name FROM pragma_table_info('fpp_playlist_entry_observations') WHERE name = ?`, col).Scan(&name)
		if err != nil {
			t.Errorf("fpp_playlist_entry_observations.%s missing: %v", col, err)
		}
	}

	var pk int
	if err := st.db.QueryRowContext(context.Background(),
		`SELECT pk FROM pragma_table_info('fpp_playlist_entry_observations') WHERE name = 'instance_uuid'`).Scan(&pk); err != nil {
		t.Fatalf("read instance_uuid pk flag: %v", err)
	}
	if pk != 1 {
		t.Errorf("instance_uuid pk = %d, want 1 (PRIMARY KEY)", pk)
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

// TestFPPPlaylistEntryObservationConcurrentSameSequenceProducesExactlyOneWinner
// is finding 4's own regression test, modeled on commands_test.go's
// TestInsertCommandConcurrentDuplicateProducesExactlyOneRow: it proves the
// step 9 sequence read and the step 10 write it gates share one
// transaction, rather than merely being called from the same handler.
// SetMaxOpenConns(1) (store.go's open()) currently serializes every writer
// through the driver itself, which is why this cannot fail today even if
// the read and write were split across two transactions - the point of
// this test is to PIN that property so a future connection-pool change
// cannot silently reopen the TOCTOU: N goroutines race a Put for the SAME
// instance at the SAME sequence with different bodies, and exactly one may
// win. Each goroutine's own returned error is captured, not just whether
// the eventual stored row looks right, so this cannot pass by chance the
// way asserting only the final row could.
func TestFPPPlaylistEntryObservationConcurrentSameSequenceProducesExactlyOneWinner(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	results := make([]error, n)
	bodies := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := fppObservationFixture("instance-race", 5)
			rec.BodyHash = "hash-race-" + string(rune('a'+i))
			rec.ObservationJSON = `{"instanceUuid":"instance-race","goroutine":` + string(rune('0'+i)) + `}`
			bodies[i] = rec.BodyHash
			results[i] = st.PutFPPPlaylistEntryObservation(ctx, rec)
		}(i)
	}
	wg.Wait()

	var succeeded, conflicted int
	var winnerBodyHash string
	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
			winnerBodyHash = bodies[i]
		case errors.Is(err, ErrFPPPlaylistEntryObservationSequenceConflict):
			conflicted++
		default:
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("succeeded = %d, want exactly 1", succeeded)
	}
	if conflicted != n-1 {
		t.Errorf("conflicted = %d, want %d", conflicted, n-1)
	}

	got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-race")
	if err != nil {
		t.Fatalf("get after the race: %v", err)
	}
	if got.Sequence != 5 {
		t.Fatalf("stored Sequence = %d, want 5", got.Sequence)
	}
	if got.BodyHash != winnerBodyHash {
		t.Fatalf("stored BodyHash = %q, want %q (the goroutine that actually won the Put)", got.BodyHash, winnerBodyHash)
	}
}

// TestFPPPlaylistEntryObservationConcurrentAscendingSequencesEndsAtHighest
// is the ascending-sequence variant: N goroutines Put the SAME instance at
// DISTINCT, increasing sequences with different bodies. Monotonicity
// (§1.5) must still hold under concurrency: the store ends at the highest
// sequence any goroutine attempted, and no lower sequence is ever allowed
// to overwrite a higher one that already landed, regardless of the order
// the driver actually serializes them in.
// TestMarkFPPPlaylistEntryObservationEvidenceBrokenSetsMarker is schemaV29's
// own set path: owner ruling 2026-09-02, the sequence-regression marker
// lives on the instance's own row, read back exactly like every other
// field.
func TestMarkFPPPlaylistEntryObservationEvidenceBrokenSetsMarker(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	rec := fppObservationFixture("instance-1", 5)
	if err := st.PutFPPPlaylistEntryObservation(ctx, rec); err != nil {
		t.Fatalf("put: %v", err)
	}

	brokenAt := time.Date(2026, 9, 2, 3, 4, 5, 0, time.UTC)
	if err := st.MarkFPPPlaylistEntryObservationEvidenceBroken(ctx, "instance-1", brokenAt); err != nil {
		t.Fatalf("mark evidence broken: %v", err)
	}

	got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.EvidenceBrokenAt == nil {
		t.Fatal("EvidenceBrokenAt = nil, want set")
	}
	if !got.EvidenceBrokenAt.Equal(brokenAt) {
		t.Fatalf("EvidenceBrokenAt = %v, want %v", got.EvidenceBrokenAt, brokenAt)
	}
	// Marking must never touch any other field on the row.
	got.EvidenceBrokenAt = nil
	if got != rec {
		t.Fatalf("marking evidence broken changed other fields:\n got  = %+v\n want = %+v", got, rec)
	}
}

// TestMarkFPPPlaylistEntryObservationEvidenceBrokenUnknownInstanceErrors
// proves the "no row to mark" case is reported, never silently treated as a
// no-op — see [ErrFPPPlaylistEntryObservationNotFoundForEvidenceBroken]'s
// own doc comment for why a caller's stale view of an instance must be
// visible rather than swallowed.
func TestMarkFPPPlaylistEntryObservationEvidenceBrokenUnknownInstanceErrors(t *testing.T) {
	st := openTestStore(t, nil)
	err := st.MarkFPPPlaylistEntryObservationEvidenceBroken(context.Background(), "no-such-instance", time.Now())
	if !errors.Is(err, ErrFPPPlaylistEntryObservationNotFoundForEvidenceBroken) {
		t.Fatalf("error = %v, want ErrFPPPlaylistEntryObservationNotFoundForEvidenceBroken", err)
	}
}

// TestFPPPlaylistEntryObservationAcceptClearsEvidenceBrokenMarker is my own
// design decision from the owner ruling (cue-deactivate-on-jump proposal
// §0a): the marker is cleared by the instance's next ACCEPTED observation,
// unconditionally, never exclusively by the operator reset route. This
// proves BOTH shapes that accept can take: an ON CONFLICT DO UPDATE against
// an existing (broken) row, and a fresh INSERT after the row was deleted.
func TestFPPPlaylistEntryObservationAcceptClearsEvidenceBrokenMarker(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	t.Run("higher sequence accept against an existing broken row", func(t *testing.T) {
		if err := st.PutFPPPlaylistEntryObservation(ctx, fppObservationFixture("instance-a", 5)); err != nil {
			t.Fatalf("put: %v", err)
		}
		if err := st.MarkFPPPlaylistEntryObservationEvidenceBroken(ctx, "instance-a", time.Now()); err != nil {
			t.Fatalf("mark evidence broken: %v", err)
		}
		broken, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-a")
		if err != nil {
			t.Fatalf("get after marking: %v", err)
		}
		if broken.EvidenceBrokenAt == nil {
			t.Fatal("precondition failed: EvidenceBrokenAt not set after marking")
		}

		if err := st.PutFPPPlaylistEntryObservation(ctx, fppObservationFixture("instance-a", 6)); err != nil {
			t.Fatalf("put accepted observation after marking: %v", err)
		}
		got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-a")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.EvidenceBrokenAt != nil {
			t.Fatalf("EvidenceBrokenAt = %v after an accepted observation, want nil", got.EvidenceBrokenAt)
		}
	})

	t.Run("fresh insert after delete", func(t *testing.T) {
		if err := st.PutFPPPlaylistEntryObservation(ctx, fppObservationFixture("instance-b", 5)); err != nil {
			t.Fatalf("put: %v", err)
		}
		if err := st.MarkFPPPlaylistEntryObservationEvidenceBroken(ctx, "instance-b", time.Now()); err != nil {
			t.Fatalf("mark evidence broken: %v", err)
		}
		if _, err := st.DeleteFPPPlaylistEntryObservation(ctx, "instance-b"); err != nil {
			t.Fatalf("delete (operator reset): %v", err)
		}
		// Simulates the plugin's own sequence restarting at 0 after fppd
		// restarted, exactly as contract §1.5 describes: with no row left
		// to compare against, this is accepted unconditionally.
		if err := st.PutFPPPlaylistEntryObservation(ctx, fppObservationFixture("instance-b", 0)); err != nil {
			t.Fatalf("put after reset: %v", err)
		}
		got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-b")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.EvidenceBrokenAt != nil {
			t.Fatalf("EvidenceBrokenAt = %v on a fresh row after reset, want nil", got.EvidenceBrokenAt)
		}
	})
}

// TestFPPPlaylistEntryObservationStaleOrConflictingPutLeavesEvidenceBrokenMarkerUntouched
// proves the marker is not disturbed by the two refusal paths that leave
// every OTHER field untouched too (mirroring
// TestFPPPlaylistEntryObservationLowerSequenceIsStaleAndLeavesRowUntouched/
// TestFPPPlaylistEntryObservationEqualSequenceIsConflictAndLeavesRowUntouched):
// only a genuinely ACCEPTED put clears it.
func TestFPPPlaylistEntryObservationStaleOrConflictingPutLeavesEvidenceBrokenMarkerUntouched(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if err := st.PutFPPPlaylistEntryObservation(ctx, fppObservationFixture("instance-1", 5)); err != nil {
		t.Fatalf("put: %v", err)
	}
	brokenAt := time.Date(2026, 9, 2, 3, 4, 5, 0, time.UTC)
	if err := st.MarkFPPPlaylistEntryObservationEvidenceBroken(ctx, "instance-1", brokenAt); err != nil {
		t.Fatalf("mark evidence broken: %v", err)
	}

	lower := fppObservationFixture("instance-1", 3)
	if err := st.PutFPPPlaylistEntryObservation(ctx, lower); !errors.Is(err, ErrFPPPlaylistEntryObservationStale) {
		t.Fatalf("stale put: error = %v, want ErrFPPPlaylistEntryObservationStale", err)
	}
	same := fppObservationFixture("instance-1", 5)
	same.Action = "stop"
	if err := st.PutFPPPlaylistEntryObservation(ctx, same); !errors.Is(err, ErrFPPPlaylistEntryObservationSequenceConflict) {
		t.Fatalf("same-sequence conflict put: error = %v, want ErrFPPPlaylistEntryObservationSequenceConflict", err)
	}

	got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.EvidenceBrokenAt == nil || !got.EvidenceBrokenAt.Equal(brokenAt) {
		t.Fatalf("EvidenceBrokenAt = %v after a refused (non-accepted) put, want unchanged at %v", got.EvidenceBrokenAt, brokenAt)
	}
}

// TestMarkFPPPlaylistEntryObservationEvidenceBrokenTxAndStoreFormsAgree
// mirrors TestFPPPlaylistEntryObservationTxAndStoreFormsAgree's own shape
// for the new marker method: the Tx form runs the identical SQL its Store
// sibling does, visible to a read through the same Tx before commit and
// through the plain Store form after commit.
func TestMarkFPPPlaylistEntryObservationEvidenceBrokenTxAndStoreFormsAgree(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if err := st.PutFPPPlaylistEntryObservation(ctx, fppObservationFixture("instance-1", 5)); err != nil {
		t.Fatalf("put: %v", err)
	}

	brokenAt := time.Date(2026, 9, 2, 3, 4, 5, 0, time.UTC)
	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.MarkFPPPlaylistEntryObservationEvidenceBroken(ctx, "instance-1", brokenAt); err != nil {
			return err
		}
		got, err := tx.GetFPPPlaylistEntryObservation(ctx, "instance-1")
		if err != nil {
			return err
		}
		if got.EvidenceBrokenAt == nil || !got.EvidenceBrokenAt.Equal(brokenAt) {
			t.Fatalf("read inside the same transaction did not see the just-written marker: got = %+v", got.EvidenceBrokenAt)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-1")
	if err != nil {
		t.Fatalf("get after commit: %v", err)
	}
	if got.EvidenceBrokenAt == nil || !got.EvidenceBrokenAt.Equal(brokenAt) {
		t.Fatalf("Store-form read after commit: EvidenceBrokenAt = %v, want %v", got.EvidenceBrokenAt, brokenAt)
	}
}

func TestFPPPlaylistEntryObservationConcurrentAscendingSequencesEndsAtHighest(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := fppObservationFixture("instance-race", int64(i+1))
			rec.BodyHash = "hash-ascending-" + string(rune('a'+i))
			results[i] = st.PutFPPPlaylistEntryObservation(ctx, rec)
		}(i)
	}
	wg.Wait()

	for i, err := range results {
		// Every attempted sequence here is distinct, so under monotonicity
		// each Put either accepts outright or is refused as stale by one
		// that already landed at a higher sequence; a same-sequence
		// conflict can never happen because no two goroutines share a
		// sequence.
		if err != nil && !errors.Is(err, ErrFPPPlaylistEntryObservationStale) {
			t.Errorf("goroutine %d (sequence %d): unexpected error: %v", i, i+1, err)
		}
	}

	got, err := st.GetFPPPlaylistEntryObservation(ctx, "instance-race")
	if err != nil {
		t.Fatalf("get after the race: %v", err)
	}
	if got.Sequence != n {
		t.Fatalf("stored Sequence = %d, want %d (the highest sequence attempted)", got.Sequence, n)
	}
	if got.BodyHash != "hash-ascending-"+string(rune('a'+n-1)) {
		t.Fatalf("stored BodyHash = %q, want the highest sequence's own body, not a lower sequence that raced past it", got.BodyHash)
	}
}

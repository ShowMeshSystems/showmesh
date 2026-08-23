package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestFPPPlaylistDefinitionSchemaVersionIsV15 pins this seam's own
// migration number, mirroring fppobservations_test.go's identical pin for
// v14: a fresh database must actually apply schemaV15.
func TestFPPPlaylistDefinitionSchemaVersionIsV15(t *testing.T) {
	st := openTestStore(t, nil)
	var version int
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() {
		t.Fatalf("user_version = %d, want %d (maxMigrationVersion())", version, maxMigrationVersion())
	}
	found := false
	for _, m := range migrations {
		if m.version == 15 {
			found = true
		}
	}
	if !found {
		t.Fatal("no migration entry has version 15 — schemaV15 was renumbered or removed")
	}

	// Being LISTED in [migrations] is not the same as having actually
	// applied against a real database: probe sqlite_master and
	// pragma_table_info so this pins that the table exists and has this
	// shape after the full migration chain, mirroring
	// fppobservations_test.go's identical v14 strengthening. It does not
	// pin that migration 15 specifically is what created the table.
	var tableName string
	if err := st.db.QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='table' AND name = 'fpp_playlist_definitions'`,
	).Scan(&tableName); err != nil {
		t.Fatalf("table fpp_playlist_definitions missing: %v", err)
	}

	for _, col := range []string{
		"instance_uuid", "playlist_hash", "playlist_name",
		"definition_json", "captured_at_millis", "received_at",
	} {
		var name string
		err := st.db.QueryRowContext(context.Background(),
			`SELECT name FROM pragma_table_info('fpp_playlist_definitions') WHERE name = ?`, col).Scan(&name)
		if err != nil {
			t.Errorf("fpp_playlist_definitions.%s missing: %v", col, err)
		}
	}

	// The (instance_uuid, playlist_hash) composite primary key: both
	// columns must carry a nonzero pk ordinal, contract §3's own "one row
	// per (instanceUuid, playlistHash)".
	for _, col := range []string{"instance_uuid", "playlist_hash"} {
		var pk int
		if err := st.db.QueryRowContext(context.Background(),
			`SELECT pk FROM pragma_table_info('fpp_playlist_definitions') WHERE name = ?`, col).Scan(&pk); err != nil {
			t.Fatalf("read %s pk flag: %v", col, err)
		}
		if pk == 0 {
			t.Errorf("%s pk = 0, want nonzero: it is part of the composite PRIMARY KEY", col)
		}
	}

	// The retention/listing index migration 15 creates alongside the
	// table.
	var indexName string
	if err := st.db.QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='index' AND name = 'fpp_playlist_definitions_by_instance_received'`,
	).Scan(&indexName); err != nil {
		t.Fatalf("index fpp_playlist_definitions_by_instance_received missing: %v", err)
	}
}

func fppDefinitionFixture(instanceUUID, playlistHash, name string, capturedAt time.Time) FPPPlaylistDefinitionRecord {
	return FPPPlaylistDefinitionRecord{
		InstanceUUID:   instanceUUID,
		PlaylistHash:   playlistHash,
		PlaylistName:   name,
		DefinitionJSON: `{"name":"` + name + `"}`,
		CapturedAt:     capturedAt.UTC(),
		ReceivedAt:     capturedAt.UTC(),
	}
}

func TestPutFPPPlaylistDefinitionInsertsAndReportsInserted(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	rec := fppDefinitionFixture("instance-1", "hash-a", "Halloween Main", time.Unix(1000, 0))
	inserted, err := st.PutFPPPlaylistDefinition(ctx, rec)
	if err != nil {
		t.Fatalf("PutFPPPlaylistDefinition: %v", err)
	}
	if !inserted {
		t.Fatal("inserted = false, want true for a brand new key")
	}

	got, err := st.GetFPPPlaylistDefinition(ctx, "instance-1", "hash-a")
	if err != nil {
		t.Fatalf("GetFPPPlaylistDefinition: %v", err)
	}
	if got.PlaylistName != "Halloween Main" || got.DefinitionJSON != rec.DefinitionJSON {
		t.Errorf("got = %+v, want name/definition to match what was put", got)
	}
}

// TestPutFPPPlaylistDefinitionRepeatIsIdempotentAndKeepsFirstProvenance
// exercises contract §3.4 step 8: a repeat of an already-held key stores
// nothing, and playlistName/capturedAt from the repeat are ignored rather
// than overwriting the row already there.
func TestPutFPPPlaylistDefinitionRepeatIsIdempotentAndKeepsFirstProvenance(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first := fppDefinitionFixture("instance-1", "hash-a", "First Name", time.Unix(1000, 0))
	if _, err := st.PutFPPPlaylistDefinition(ctx, first); err != nil {
		t.Fatalf("first put: %v", err)
	}

	repeat := fppDefinitionFixture("instance-1", "hash-a", "Second Name", time.Unix(2000, 0))
	inserted, err := st.PutFPPPlaylistDefinition(ctx, repeat)
	if err != nil {
		t.Fatalf("repeat put: %v", err)
	}
	if inserted {
		t.Fatal("inserted = true on a repeat of an already-held key, want false")
	}

	got, err := st.GetFPPPlaylistDefinition(ctx, "instance-1", "hash-a")
	if err != nil {
		t.Fatalf("GetFPPPlaylistDefinition: %v", err)
	}
	if got.PlaylistName != "First Name" {
		t.Errorf("PlaylistName = %q, want %q (first report keeps provenance)", got.PlaylistName, "First Name")
	}
	if !got.CapturedAt.Equal(first.CapturedAt) {
		t.Errorf("CapturedAt = %v, want the first put's %v", got.CapturedAt, first.CapturedAt)
	}
}

func TestGetFPPPlaylistDefinitionNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.GetFPPPlaylistDefinition(context.Background(), "no-such-instance", "no-such-hash")
	if !errors.Is(err, ErrFPPPlaylistDefinitionNotFound) {
		t.Fatalf("err = %v, want ErrFPPPlaylistDefinitionNotFound", err)
	}
}

func TestListFPPPlaylistDefinitionsOrdersNewestReceivedFirst(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	older := fppDefinitionFixture("instance-1", "hash-old", "Old", time.Unix(1000, 0))
	newer := fppDefinitionFixture("instance-1", "hash-new", "New", time.Unix(5000, 0))
	if _, err := st.PutFPPPlaylistDefinition(ctx, older); err != nil {
		t.Fatalf("put older: %v", err)
	}
	if _, err := st.PutFPPPlaylistDefinition(ctx, newer); err != nil {
		t.Fatalf("put newer: %v", err)
	}

	got, err := st.ListFPPPlaylistDefinitions(ctx)
	if err != nil {
		t.Fatalf("ListFPPPlaylistDefinitions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].PlaylistHash != "hash-new" || got[1].PlaylistHash != "hash-old" {
		t.Errorf("order = [%s, %s], want [hash-new, hash-old] (newest received first)", got[0].PlaylistHash, got[1].PlaylistHash)
	}
}

func TestDeleteFPPPlaylistDefinitionReportsWhetherARowWasRemoved(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	rec := fppDefinitionFixture("instance-1", "hash-a", "Halloween", time.Unix(1000, 0))
	if _, err := st.PutFPPPlaylistDefinition(ctx, rec); err != nil {
		t.Fatalf("put: %v", err)
	}

	deleted, err := st.DeleteFPPPlaylistDefinition(ctx, "instance-1", "hash-a")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatal("deleted = false, want true for an existing row")
	}

	deleted, err = st.DeleteFPPPlaylistDefinition(ctx, "instance-1", "hash-a")
	if err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if deleted {
		t.Fatal("deleted = true on a second delete of an already-gone row, want false")
	}
}

// TestPruneFPPPlaylistDefinitionsNeverEvictsReferenced exercises H2 spec
// §3's retention rule: a referenced hash survives pruning no matter how
// old it is, even with keepUnreferenced=0.
func TestPruneFPPPlaylistDefinitionsNeverEvictsReferenced(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	referenced := fppDefinitionFixture("instance-1", "hash-referenced", "Referenced", time.Unix(1000, 0))
	if _, err := st.PutFPPPlaylistDefinition(ctx, referenced); err != nil {
		t.Fatalf("put referenced: %v", err)
	}

	isReferenced := func(hash string) (bool, error) { return hash == "hash-referenced", nil }
	pruned, err := st.PruneFPPPlaylistDefinitions(ctx, "instance-1", 0, isReferenced)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d, want 0", pruned)
	}
	if _, err := st.GetFPPPlaylistDefinition(ctx, "instance-1", "hash-referenced"); err != nil {
		t.Errorf("referenced row was evicted: %v", err)
	}
}

// TestPruneFPPPlaylistDefinitionsKeepsNewestUnreferencedAndRemovesOlder
// exercises the "beyond those, the newest N are kept" half of §3's rule
// against unreferenced rows only.
func TestPruneFPPPlaylistDefinitionsKeepsNewestUnreferencedAndRemovesOlder(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	for i, sec := range []int64{1, 2, 3, 4} {
		rec := fppDefinitionFixture("instance-1", "hash-"+string(rune('a'+i)), "P", time.Unix(sec, 0))
		if _, err := st.PutFPPPlaylistDefinition(ctx, rec); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	// Newest received first is hash-d (sec=4), then hash-c, hash-b, hash-a.
	isReferenced := func(string) (bool, error) { return false, nil }
	pruned, err := st.PruneFPPPlaylistDefinitions(ctx, "instance-1", 2, isReferenced)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 2 {
		t.Fatalf("pruned = %d, want 2", pruned)
	}
	for _, want := range []string{"hash-d", "hash-c"} {
		if _, err := st.GetFPPPlaylistDefinition(ctx, "instance-1", want); err != nil {
			t.Errorf("%s should have survived pruning: %v", want, err)
		}
	}
	for _, want := range []string{"hash-b", "hash-a"} {
		if _, err := st.GetFPPPlaylistDefinition(ctx, "instance-1", want); !errors.Is(err, ErrFPPPlaylistDefinitionNotFound) {
			t.Errorf("%s should have been pruned, got err=%v", want, err)
		}
	}
}

// TestPruneFPPPlaylistDefinitionsAbortsOnReadFailureLeavingTableUnchanged
// proves the fail-closed contract: an isReferenced read failure aborts
// the whole prune and returns the error, and no row is deleted, not even
// one whose own isReferenced call already succeeded and reported
// unreferenced before the failing call was reached.
func TestPruneFPPPlaylistDefinitionsAbortsOnReadFailureLeavingTableUnchanged(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	for i, sec := range []int64{1, 2, 3, 4} {
		rec := fppDefinitionFixture("instance-1", "hash-"+string(rune('a'+i)), "P", time.Unix(sec, 0))
		if _, err := st.PutFPPPlaylistDefinition(ctx, rec); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	// Newest received first is hash-d, hash-c, hash-b, hash-a, so with
	// keepUnreferenced=0 both hash-d and hash-c are already classified as
	// deletable before the failure on hash-b is reached.
	wantErr := errors.New("boom: simulated read failure")
	isReferenced := func(hash string) (bool, error) {
		if hash == "hash-b" {
			return false, wantErr
		}
		return false, nil
	}
	pruned, err := st.PruneFPPPlaylistDefinitions(ctx, "instance-1", 0, isReferenced)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want wrapping %v", err, wantErr)
	}
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0 (abort must not delete anything)", pruned)
	}
	for _, want := range []string{"hash-a", "hash-b", "hash-c", "hash-d"} {
		if _, err := st.GetFPPPlaylistDefinition(ctx, "instance-1", want); err != nil {
			t.Errorf("%s should have survived an aborted prune: %v", want, err)
		}
	}
}

func TestPruneFPPPlaylistDefinitionsOnlyTouchesTheNamedInstance(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.PutFPPPlaylistDefinition(ctx, fppDefinitionFixture("instance-1", "hash-a", "A", time.Unix(1, 0))); err != nil {
		t.Fatalf("put instance-1: %v", err)
	}
	if _, err := st.PutFPPPlaylistDefinition(ctx, fppDefinitionFixture("instance-2", "hash-b", "B", time.Unix(1, 0))); err != nil {
		t.Fatalf("put instance-2: %v", err)
	}

	pruned, err := st.PruneFPPPlaylistDefinitions(ctx, "instance-1", 0, func(string) (bool, error) { return false, nil })
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if _, err := st.GetFPPPlaylistDefinition(ctx, "instance-2", "hash-b"); err != nil {
		t.Errorf("instance-2's row must survive pruning scoped to instance-1: %v", err)
	}
}

func TestFPPPlaylistDefinitionTxFormsMatchStoreForms(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		inserted, err := tx.PutFPPPlaylistDefinition(ctx, fppDefinitionFixture("instance-1", "hash-a", "A", time.Unix(1, 0)))
		if err != nil {
			return err
		}
		if !inserted {
			t.Error("Tx.PutFPPPlaylistDefinition: inserted = false, want true")
		}
		got, err := tx.GetFPPPlaylistDefinition(ctx, "instance-1", "hash-a")
		if err != nil {
			return err
		}
		if got.PlaylistName != "A" {
			t.Errorf("Tx.GetFPPPlaylistDefinition: PlaylistName = %q, want %q", got.PlaylistName, "A")
		}
		list, err := tx.ListFPPPlaylistDefinitions(ctx)
		if err != nil {
			return err
		}
		if len(list) != 1 {
			t.Errorf("Tx.ListFPPPlaylistDefinitions: len = %d, want 1", len(list))
		}
		byInstance, err := tx.ListFPPPlaylistDefinitionsByInstance(ctx, "instance-1")
		if err != nil {
			return err
		}
		if len(byInstance) != 1 {
			t.Errorf("Tx.ListFPPPlaylistDefinitionsByInstance: len = %d, want 1", len(byInstance))
		}
		pruned, err := tx.PruneFPPPlaylistDefinitions(ctx, "instance-1", 0, func(string) (bool, error) { return true, nil })
		if err != nil {
			return err
		}
		if pruned != 0 {
			t.Errorf("Tx.PruneFPPPlaylistDefinitions: pruned = %d, want 0 (referenced)", pruned)
		}
		deleted, err := tx.DeleteFPPPlaylistDefinition(ctx, "instance-1", "hash-a")
		if err != nil {
			return err
		}
		if !deleted {
			t.Error("Tx.DeleteFPPPlaylistDefinition: deleted = false, want true")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
}

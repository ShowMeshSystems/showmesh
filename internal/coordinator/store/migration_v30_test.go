package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// openDatabaseAtV29 mirrors migration_v28_test.go's openDatabaseAtV27: a
// database carrying every migration up to and including v29, stamped at
// that version, so a test can seed config_objects rows the way a pre-v30
// coordinator would have written them (no deleted_at column at all) and
// then watch v30 add it underneath them.
func openDatabaseAtV29(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pre-v30.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	for _, m := range migrations {
		if m.version > 29 {
			continue
		}
		if m.fn != nil {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin tx for migration %d: %v", m.version, err)
			}
			if err := m.fn(ctx, tx); err != nil {
				t.Fatalf("apply migration %d: %v", m.version, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit migration %d: %v", m.version, err)
			}
			continue
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 29`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	return db
}

func seedPreV30ConfigObject(t *testing.T, db *sql.DB, kind, id string, currentRevision int64) {
	t.Helper()
	now := timeToDB(time.Now())
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO config_objects (kind, id, current_revision, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, kind, id, currentRevision, now, now)
	if err != nil {
		t.Fatalf("seed pre-v30 config object %s/%s: %v", kind, id, err)
	}
}

// TestMigrateV30FromPreV30DatabaseWithExistingRows proves migrateV30AddConfigObjectDeletedAtColumn's own
// doc comment: it is a pure addition, so every config_objects row a
// pre-v30 database already holds survives migration unchanged, reading
// back as a live object (deleted_at NULL), with no data fix applied.
func TestMigrateV30FromPreV30DatabaseWithExistingRows(t *testing.T) {
	db := openDatabaseAtV29(t)
	seedPreV30ConfigObject(t, db, "audio.node", "node-1", 3)
	seedPreV30ConfigObject(t, db, "show", "halloween-2026", 1)

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	type row struct {
		currentRevision int64
		deletedAt       sql.NullString
	}
	readRow := func(kind, id string) row {
		t.Helper()
		var r row
		err := db.QueryRowContext(context.Background(),
			`SELECT current_revision, deleted_at FROM config_objects WHERE kind = ? AND id = ?`, kind, id).
			Scan(&r.currentRevision, &r.deletedAt)
		if err != nil {
			t.Fatalf("read migrated config object %s/%s: %v", kind, id, err)
		}
		return r
	}

	got := readRow("audio.node", "node-1")
	if got.currentRevision != 3 {
		t.Errorf("audio.node/node-1 current_revision = %d, want 3 (unchanged)", got.currentRevision)
	}
	if got.deletedAt.Valid {
		t.Errorf("audio.node/node-1 deleted_at = %q, want NULL: a pre-v30 row must read back live, not tombstoned", got.deletedAt.String)
	}

	got = readRow("show", "halloween-2026")
	if got.currentRevision != 1 {
		t.Errorf("show/halloween-2026 current_revision = %d, want 1 (unchanged)", got.currentRevision)
	}
	if got.deletedAt.Valid {
		t.Errorf("show/halloween-2026 deleted_at = %q, want NULL: a pre-v30 row must read back live, not tombstoned", got.deletedAt.String)
	}

	var total int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM config_objects`).Scan(&total); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if total != 2 {
		t.Fatalf("row count after migration = %d, want 2 (no row lost or duplicated)", total)
	}
}

// TestMigrateV30ThenStoreLayerCanTombstone proves the migration actually
// lands a usable column: once migrated, [Store.TombstoneConfigObject] on a
// pre-v30 row succeeds and [Store.GetConfigObject] then reports it absent,
// exactly as it would for an object created after v30 shipped.
func TestMigrateV30ThenStoreLayerCanTombstone(t *testing.T) {
	db := openDatabaseAtV29(t)
	seedPreV30ConfigObject(t, db, "audio.node", "node-1", 2)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	st := &Store{db: db, now: time.Now}
	ctx := context.Background()

	if _, err := st.TombstoneConfigObject(ctx, "audio.node", "node-1"); err != nil {
		t.Fatalf("tombstone pre-v30 config object: %v", err)
	}
	if _, err := st.GetConfigObject(ctx, "audio.node", "node-1"); err == nil {
		t.Fatalf("GetConfigObject after tombstone succeeded, want ErrConfigObjectNotFound")
	}
}

// TestMigrateV30ToleratesReplayAfterAUserVersionRewind mirrors
// TestMigrateV26ToleratesReplayAfterAUserVersionRewind (migration_v26_test.go)
// one migration over: internal/coordinator/audioconfigpush's own tests
// rewind PRAGMA user_version below 30 and reopen the store to force every
// later migration, including this one, to run a second time. A bare
// ALTER TABLE ... ADD COLUMN fails on that second pass with "duplicate
// column name", exactly the defect this test caught before
// migrateV30AddConfigObjectDeletedAtColumn added its own
// pragma_table_info check.
func TestMigrateV30ToleratesReplayAfterAUserVersionRewind(t *testing.T) {
	db := openDatabaseAtV29(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	// Mirrors audioconfigpush's own rewind target (18) exactly, so this
	// forces every migration above it, including v30, to run a second time
	// through the ordinary [migrate] loop rather than only calling this
	// function directly.
	if _, err := db.ExecContext(context.Background(), `PRAGMA user_version = 18`); err != nil {
		t.Fatalf("rewind user_version: %v", err)
	}
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("second migrate after rewind: %v", err)
	}

	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() {
		t.Errorf("user_version after replay = %d, want %d", version, maxMigrationVersion())
	}

	var deletedAtExists int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('config_objects') WHERE name = 'deleted_at'`).Scan(&deletedAtExists); err != nil {
		t.Fatalf("check deleted_at: %v", err)
	}
	if deletedAtExists != 1 {
		t.Errorf("config_objects.deleted_at exists (count) = %d, want 1 (exactly one column, not duplicated by replay)", deletedAtExists)
	}
}

// TestMigrateV30AdvancesTheSchemaVersion mirrors
// TestMigrateV28AdvancesTheSchemaVersion one migration over.
func TestMigrateV30AdvancesTheSchemaVersion(t *testing.T) {
	db := openDatabaseAtV29(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() || version < 30 {
		t.Errorf("user_version = %d, want %d and at least 30", version, maxMigrationVersion())
	}
}

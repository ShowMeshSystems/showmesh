package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openDatabaseAtV25 builds a database carrying every migration up to and
// including v25, stamped at that version, so a test can seed
// commands.requested_revision rows the way a pre-v26 coordinator would
// have written them and then watch v26's rename run against them. Some
// migrations in this range are Go functions rather than bare SQL (v19,
// v20, v24), so this mirrors migrate()'s own transaction-scoped loop
// rather than openDatabaseAtV18's simpler all-SQL version (migration_v19_test.go).
func openDatabaseAtV25(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pre-v26.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, m := range migrations {
		if m.version > 25 {
			continue
		}
		if m.fn != nil {
			if err := m.fn(ctx, tx); err != nil {
				t.Fatalf("apply migration %d: %v", m.version, err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit pre-v26 schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 25`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	return db
}

func seedPreV26Command(t *testing.T, db *sql.DB, id, action, requestedRevision string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO commands (
			id, idempotency_key, action, requested_revision, created_at, state
		) VALUES (?, ?, ?, ?, '2026-08-01T00:00:00Z', 'pending')
	`, id, "idem-"+id, action, requestedRevision)
	if err != nil {
		t.Fatalf("seed command %q: %v", id, err)
	}
}

// TestMigrateV26RenamesColumnPreservingEveryShape is the mandate this
// migration exists to satisfy: a database already holding rows of every
// shape commands.requested_revision has ever carried (untagged empty, a
// macro run's own pre-existing "macro:" tag, a bare action-configuration
// revision digit string, and a bare JSON request-identity struct) must
// come through the rename with every value preserved byte-for-byte under
// the new column name. RENAME COLUMN is metadata-only; this proves that
// rather than only asserting it.
func TestMigrateV26RenamesColumnPreservingEveryShape(t *testing.T) {
	db := openDatabaseAtV25(t)

	rows := []struct {
		id, action, value string
	}{
		{"cmd-untagged", "fpp.stop_playlist", ""},
		{"cmd-macro", "fpp.stop_playlist", "macro:begin-set@3"},
		{"cmd-revision", "action.invoke:fpp", "3"},
		{"cmd-identity", "render.apply", `{"action":"apply","node":"render-01","surface":"main","sequenceId":"seq-1"}`},
	}
	for _, r := range rows {
		seedPreV26Command(t, db, r.id, r.action, r.value)
	}

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The old column name must be gone: querying it is now a SQL error,
	// not a value.
	if _, err := db.QueryContext(context.Background(), `SELECT requested_revision FROM commands`); err == nil {
		t.Fatalf("requested_revision still queryable after v26; rename did not apply")
	}

	for _, r := range rows {
		var got string
		if err := db.QueryRowContext(context.Background(),
			`SELECT caller_intent FROM commands WHERE id = ?`, r.id).Scan(&got); err != nil {
			t.Fatalf("read caller_intent for %q: %v", r.id, err)
		}
		if got != r.value {
			t.Errorf("commands %q: caller_intent = %q, want %q (byte-for-byte, no backfill)", r.id, got, r.value)
		}
	}
}

// TestMigrateV26AdvancesTheSchemaVersion mirrors
// TestMigrateV19AdvancesTheSchemaVersion: a rename-only migration must
// still stamp PRAGMA user_version, or every restart re-runs it (and, for
// RENAME COLUMN specifically, fails the second time since the old name no
// longer exists).
func TestMigrateV26AdvancesTheSchemaVersion(t *testing.T) {
	db := openDatabaseAtV25(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() || version < 26 {
		t.Errorf("user_version = %d, want %d and at least 26", version, maxMigrationVersion())
	}

	// Re-running migrate against an already-migrated database must be a
	// no-op, not a second (failing) attempt at the rename.
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("re-running migrate against an already-migrated database: %v", err)
	}
}

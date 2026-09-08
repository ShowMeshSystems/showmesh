package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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

// TestMigrateV26ToleratesReplayAfterAUserVersionRewind proves the property
// this migration exists to satisfy: internal/coordinator/audioconfigpush's
// own tests rewind PRAGMA user_version below 26 and reopen the store to
// force later migrations to run again (schemaV25's own doc comment names
// the identical precedent for migrations 19 and 20). Before this
// migration checked for its own prior application, that replay failed
// with "no such column: requested_revision", because the rename had
// already happened and the column no longer existed under that name.
// Running v26 a second time against that state must now succeed.
func TestMigrateV26ToleratesReplayAfterAUserVersionRewind(t *testing.T) {
	db := openDatabaseAtV25(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	// Mirrors audioconfigpush's own rewind target (18) exactly, so this
	// forces every migration above it, including v26, to run a second
	// time through the ordinary [migrate] loop rather than only calling
	// this function directly.
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

	var callerIntentExists int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('commands') WHERE name = 'caller_intent'`).Scan(&callerIntentExists); err != nil {
		t.Fatalf("check caller_intent: %v", err)
	}
	if callerIntentExists != 1 {
		t.Errorf("commands.caller_intent missing after replay")
	}
}

// TestMigrateV26RejectsAnUnexpectedColumnState proves the replay tolerance
// above is narrow: a commands table with NEITHER requested_revision NOR
// caller_intent (simulating a broken or unrelated schema, not a replay)
// must fail loudly rather than being silently treated as "already
// renamed, nothing to do". Checking only "is requested_revision absent"
// cannot tell these two cases apart; this is what forces the migration to
// also confirm caller_intent's presence before tolerating anything.
func TestMigrateV26RejectsAnUnexpectedColumnState(t *testing.T) {
	db := openDatabaseAtV25(t)
	ctx := context.Background()

	// Simulate neither column existing: rename requested_revision to
	// something this migration does not recognize, so the table has a
	// commands table but not the shape v26 expects either before or
	// after its own rename.
	if _, err := db.ExecContext(ctx, `ALTER TABLE commands RENAME COLUMN requested_revision TO neither_name`); err != nil {
		t.Fatalf("simulate broken state: %v", err)
	}

	err := migrate(ctx, db)
	if err == nil {
		t.Fatalf("migrate: err = nil, want an error naming the unexpected column state")
	}
	if !strings.Contains(err.Error(), "unexpected state") {
		t.Errorf("migrate error = %q, want it to name the unexpected column state", err.Error())
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

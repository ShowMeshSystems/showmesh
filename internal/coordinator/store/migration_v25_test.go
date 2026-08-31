package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestV25AppliesAfterStoreStampedAtV24Only is the specific regression
// this migration's own numbering is meant to prevent: schemaV25 was
// originally added as schemaV23, taken while v24 did not yet exist on
// main. Once v24 (migrateV24AudioSettingsBackfillDuckFadeDurations)
// landed first, a migration still numbered 23 would never run for any
// store a v24-only binary had already stamped: migrate()'s own
// current == target short-circuit compares against maxMigrationVersion()
// (24, before this migration was renumbered), sees a stamp of 24 already
// equal to that target, and returns before the per-entry loop ever
// checks version 23 at all. Renumbering to 25, the first number above
// the already-shipped maximum, is what makes this migration reachable
// again; this test proves it, rather than only asserting it in a comment.
//
// The database is built up to v24 and stamped at user_version 24 directly
// through a raw connection, never via [Open]/[migrate]: calling Open first
// would satisfy both assertions below through that open, before any
// rewind. Every migration through v24 is applied for real (rather than
// only stamping the version, as this test did before Lane 17a SM-111's
// v26 landed) because v26's RENAME COLUMN, unlike v25's CREATE TABLE IF
// NOT EXISTS, depends on a table an earlier migration creates: a stamp
// with no schema behind it was never a state a real coordinator could
// reach, only a shortcut that happened to work while every migration
// above the stamp was self-contained.
func TestV25AppliesAfterStoreStampedAtV24Only(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	dbPath := filepath.Join(dir, dbFileName)

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite file: %v", err)
	}
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, m := range migrations {
		if m.version > 24 {
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
		t.Fatalf("commit schema through v24: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 24`); err != nil {
		t.Fatalf("stamp user_version to 24: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite file: %v", err)
	}

	// Open with the current binary (v25 in the migrations slice) against a
	// database that never ran ANY migration through this package: v25
	// must apply, not be silently skipped.
	st, err := Open(ctx, dir, nil)
	if err != nil {
		t.Fatalf("open a store stamped at v24 only, with no prior migration history: %v", err)
	}
	defer func() { _ = st.Close() }()

	var version int
	if err := st.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() {
		t.Fatalf("user_version after open = %d, want %d (maxMigrationVersion)", version, maxMigrationVersion())
	}

	// The real proof, not just the stamp: fallback_programs must actually
	// exist, since that is what v25 creates and a silently-skipped
	// migration would leave missing with no error anywhere.
	var tableName string
	if err := st.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='fallback_programs'`).Scan(&tableName); err != nil {
		t.Fatalf("fallback_programs table does not exist after opening a store stamped at v24 only: %v", err)
	}
}

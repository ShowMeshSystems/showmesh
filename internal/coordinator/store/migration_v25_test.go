package store

import (
	"context"
	"testing"
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
func TestV25AppliesAfterStoreStampedAtV24Only(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Simulate exactly that prior binary: open normally (which stamps the
	// CURRENT maximum, including v25), then force user_version back down
	// to 24, as if only migrations through v24 had ever actually run.
	st, err := Open(ctx, dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `PRAGMA user_version = 24`); err != nil {
		t.Fatalf("stamp user_version to 24: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen with the current binary (v25 in the migrations slice): v25
	// must apply, not be silently skipped.
	st2, err := Open(ctx, dir, nil)
	if err != nil {
		t.Fatalf("reopen a store stamped at v24 only: %v", err)
	}
	defer func() { _ = st2.Close() }()

	var version int
	if err := st2.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() {
		t.Fatalf("user_version after reopen = %d, want %d (maxMigrationVersion)", version, maxMigrationVersion())
	}

	// The real proof, not just the stamp: fallback_programs must actually
	// exist, since that is what v25 creates and a silently-skipped
	// migration would leave missing with no error anywhere.
	var tableName string
	if err := st2.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='fallback_programs'`).Scan(&tableName); err != nil {
		t.Fatalf("fallback_programs table does not exist after reopening a store stamped at v24 only: %v", err)
	}
}

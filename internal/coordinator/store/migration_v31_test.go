package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// buildV30Database applies every migration up through version 30 (the
// shipped maximum on main before this package's v31) directly against a
// fresh file, bypassing [open]/[migrate] (which always brings a database
// to the newest known version), so a test can seed rows in the schema
// shape and on-disk timestamp format that shipped before migration 31
// existed. Mirrors [migrate]'s own apply loop (migrations.go) filtered to
// version <= 30, and stamps PRAGMA user_version = 30 itself the same way
// [TestMigrationV5AddsIdentityTablesAndPreservesV4Data] stamps an
// intermediate version by hand.
func buildV30Database(t *testing.T, dir string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(dir, dbFileName)
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	for _, m := range migrations {
		if m.version > 30 {
			continue
		}
		if m.fn != nil {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin migration %d: %v", m.version, err)
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
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 30`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	return db
}

// TestMigrationV31RewritesTrimmedTimestampsToFixedWidth seeds two
// principal_tokens rows directly in the OLD on-disk format, trimmed
// time.RFC3339Nano text, exactly as every pre-v31 binary wrote it, using
// the report's own reproduction values: t-1 stored at ...02.122Z (trimmed
// from .122000000) and t-2 at ...02.122183Z. Under a plain string compare
// t-1 sorts AFTER t-2 despite being chronologically earlier, which is the
// defect ListTokens' ORDER BY created_at exposed. It then reopens the
// database (running migration 31) and checks both that the raw stored
// text is now fixed-width and that ListTokens returns true time order.
func TestMigrationV31RewritesTrimmedTimestampsToFixedWidth(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	db := buildV30Database(t, dir)

	const (
		oldFormatEarlier = "2026-09-05T10:00:02.122Z"
		oldFormatLater   = "2026-09-05T10:00:02.122183Z"
	)
	// Positive control: under the OLD trimmed format, the chronologically
	// EARLIER value must sort lexicographically AFTER the later one:
	// that inversion is the defect this migration exists to fix. If this
	// ever stops holding, these two literals no longer reproduce it.
	if oldFormatEarlier <= oldFormatLater {
		t.Fatalf("test setup: %q is not lexicographically after %q, so seeding these two values would not reproduce the reported defect", oldFormatEarlier, oldFormatLater)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO principals (id, name, kind, role, created_at, updated_at)
		VALUES ('p-1', 'operator', 'human', 'admin', ?, ?)
	`, oldFormatEarlier, oldFormatEarlier); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO principal_tokens (id, principal_id, digest, created_at)
		VALUES ('t-1', 'p-1', 'd1', ?)
	`, oldFormatEarlier); err != nil {
		t.Fatalf("seed old-format token t-1: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO principal_tokens (id, principal_id, digest, created_at)
		VALUES ('t-2', 'p-1', 'd2', ?)
	`, oldFormatLater); err != nil {
		t.Fatalf("seed old-format token t-2: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	st, err := open(ctx, dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 31): %v", err)
	}
	defer func() { _ = st.Close() }()

	var version int
	if err := st.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() {
		t.Errorf("user_version = %d, want %d (maxMigrationVersion())", version, maxMigrationVersion())
	}

	// The raw column value is now fixed nine-digit-fraction text, not the
	// trimmed format it was seeded with.
	rows := map[string]string{}
	raw, err := st.db.QueryContext(ctx, `SELECT id, created_at FROM principal_tokens ORDER BY id`)
	if err != nil {
		t.Fatalf("read migrated rows: %v", err)
	}
	for raw.Next() {
		var id, createdAt string
		if err := raw.Scan(&id, &createdAt); err != nil {
			t.Fatalf("scan migrated row: %v", err)
		}
		rows[id] = createdAt
	}
	if err := raw.Err(); err != nil {
		t.Fatalf("read migrated rows: %v", err)
	}
	wantT1 := "2026-09-05T10:00:02.122000000Z"
	wantT2 := "2026-09-05T10:00:02.122183000Z"
	if rows["t-1"] != wantT1 {
		t.Errorf("t-1 created_at = %q, want %q", rows["t-1"], wantT1)
	}
	if rows["t-2"] != wantT2 {
		t.Errorf("t-2 created_at = %q, want %q", rows["t-2"], wantT2)
	}
	if rows["t-1"] >= rows["t-2"] {
		t.Errorf("migrated created_at values still do not sort in time order: t-1 = %q, t-2 = %q", rows["t-1"], rows["t-2"])
	}

	got, err := st.ListTokens(ctx, "p-1")
	if err != nil {
		t.Fatalf("list tokens after migration 31: %v", err)
	}
	if len(got) != 2 || got[0].ID != "t-1" || got[1].ID != "t-2" {
		t.Fatalf("got = %+v, want [t-1, t-2] in true creation order", got)
	}
}

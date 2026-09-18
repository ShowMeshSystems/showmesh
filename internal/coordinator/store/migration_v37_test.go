package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// openDatabaseAtV36 builds a database carrying every migration up to and
// including v36 and stamped at that version, so a test can seed
// node_asset_inventory rows the way a pre-v37 coordinator would have
// written them (under schemaV8's bare `(node_id, content_hash)` primary
// key) and then watch v37 re-key the table underneath them. Nothing
// between v8 and v36 touches this table, so stamping directly at 36 seeds
// the identical pre-migration shape running every intermediate migration
// would.
func openDatabaseAtV36(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pre-v37.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	for _, m := range migrations {
		if m.version > 36 {
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
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 36`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	return db
}

func seedPreV37InventoryRow(t *testing.T, db *sql.DB, nodeID, contentHash, filename string, sizeBytes int64, verifiedAt string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO node_asset_inventory (node_id, content_hash, runtime_filename, size_bytes, verified_at)
		 VALUES (?, ?, ?, ?, ?)`,
		nodeID, contentHash, filename, sizeBytes, verifiedAt)
	if err != nil {
		t.Fatalf("seed inventory row (%q, %q, %q): %v", nodeID, contentHash, filename, err)
	}
}

// TestMigrateV37ReKeysNodeAssetInventoryByFilename is schemaV37's core
// migration property: every pre-v37 row survives with its size and
// verified_at intact, AND the composite (node_id, content_hash,
// runtime_filename) key is actually in force afterward - proven by
// inserting a second row that reuses a (node_id, content_hash) pair
// already present under a DIFFERENT runtime_filename (impossible before
// v37, since (node_id, content_hash) alone was the primary key) and
// confirming both rows coexist.
func TestMigrateV37ReKeysNodeAssetInventoryByFilename(t *testing.T) {
	db := openDatabaseAtV36(t)
	seedPreV37InventoryRow(t, db, "pi-audio-01", "sha256:aaa", "Opening.fseq", 100, "2026-09-01T00:00:00Z")
	seedPreV37InventoryRow(t, db, "pi-audio-01", "sha256:bbb", "Closing.fseq", 200, "2026-09-02T00:00:00Z")
	seedPreV37InventoryRow(t, db, "render-01", "sha256:aaa", "Opening.fseq", 100, "2026-09-03T00:00:00Z")

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	type row struct {
		nodeID, contentHash, filename, verifiedAt string
		sizeBytes                                 int64
	}
	readRow := func(nodeID, contentHash, filename string) row {
		t.Helper()
		var r row
		err := db.QueryRowContext(context.Background(),
			`SELECT node_id, content_hash, runtime_filename, size_bytes, verified_at
			 FROM node_asset_inventory WHERE node_id = ? AND content_hash = ? AND runtime_filename = ?`,
			nodeID, contentHash, filename).Scan(&r.nodeID, &r.contentHash, &r.filename, &r.sizeBytes, &r.verifiedAt)
		if err != nil {
			t.Fatalf("read migrated row (%q, %q, %q): %v", nodeID, contentHash, filename, err)
		}
		return r
	}

	// verified_at is untouched by any migration between v8 and v37 (unlike
	// audio_sessions' created_at/updated_at, which v31 rewrites on every
	// store's way past it — see migrationversions_test.go's identical
	// note for that table), so the seeded text survives byte-for-byte.
	got := readRow("pi-audio-01", "sha256:aaa", "Opening.fseq")
	want := row{nodeID: "pi-audio-01", contentHash: "sha256:aaa", filename: "Opening.fseq", sizeBytes: 100, verifiedAt: "2026-09-01T00:00:00Z"}
	if got != want {
		t.Errorf("pi-audio-01/sha256:aaa/Opening.fseq = %+v, want %+v", got, want)
	}
	got = readRow("pi-audio-01", "sha256:bbb", "Closing.fseq")
	want = row{nodeID: "pi-audio-01", contentHash: "sha256:bbb", filename: "Closing.fseq", sizeBytes: 200, verifiedAt: "2026-09-02T00:00:00Z"}
	if got != want {
		t.Errorf("pi-audio-01/sha256:bbb/Closing.fseq = %+v, want %+v", got, want)
	}
	got = readRow("render-01", "sha256:aaa", "Opening.fseq")
	want = row{nodeID: "render-01", contentHash: "sha256:aaa", filename: "Opening.fseq", sizeBytes: 100, verifiedAt: "2026-09-03T00:00:00Z"}
	if got != want {
		t.Errorf("render-01/sha256:aaa/Opening.fseq = %+v, want %+v", got, want)
	}

	var total int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM node_asset_inventory`).Scan(&total); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if total != 3 {
		t.Fatalf("row count after migration = %d, want 3 (no row lost or duplicated)", total)
	}

	// The composite key is actually in force: a second row reusing
	// pi-audio-01's sha256:aaa under a DIFFERENT filename must now be
	// insertable and coexist as its own row - under the pre-v37
	// (node_id, content_hash) primary key this INSERT would fail as a
	// UNIQUE constraint violation.
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO node_asset_inventory (node_id, content_hash, runtime_filename, size_bytes, verified_at)
		 VALUES ('pi-audio-01', 'sha256:aaa', 'BorrowedName.fseq', 100, '2026-09-04T00:00:00.000000000Z')`); err != nil {
		t.Fatalf("insert row reusing (node_id, content_hash) under a new runtime_filename: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO node_asset_inventory (node_id, content_hash, runtime_filename, size_bytes, verified_at)
		 VALUES ('pi-audio-01', 'sha256:aaa', 'Opening.fseq', 999, '2026-09-05T00:00:00.000000000Z')`); err == nil {
		t.Fatalf("inserting a second row for an EXISTING (node_id, content_hash, runtime_filename) triple must fail a PRIMARY KEY violation")
	}

	got = readRow("pi-audio-01", "sha256:aaa", "BorrowedName.fseq")
	if got.sizeBytes != 100 {
		t.Errorf("pi-audio-01/sha256:aaa/BorrowedName.fseq = %+v, want the freshly inserted row", got)
	}
	// pi-audio-01's original Opening.fseq row must still be exactly as
	// migrated.
	got = readRow("pi-audio-01", "sha256:aaa", "Opening.fseq")
	if got.sizeBytes != 100 || got.verifiedAt != "2026-09-01T00:00:00Z" {
		t.Errorf("pi-audio-01/sha256:aaa/Opening.fseq mutated by the BorrowedName.fseq insert: %+v", got)
	}
}

// TestMigrateV37AdvancesTheSchemaVersion proves migrate stamps the maximum
// migration version; v37 must advance PRAGMA user_version past 36 or every
// restart would re-run it.
func TestMigrateV37AdvancesTheSchemaVersion(t *testing.T) {
	db := openDatabaseAtV36(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() || version < 37 {
		t.Errorf("user_version = %d, want %d and at least 37", version, maxMigrationVersion())
	}
}

// TestMigrateV37ThenStoreLayerStoresTwoFilenamesForOneHash proves the
// migrated database is immediately usable through this package's own
// ReplaceNodeAssetInventory: a report carrying one content hash under two
// different filenames must store both rows and return no error -
// exercised through [Store.Open]/[migrate], the exact path a real
// coordinator restart takes against an on-disk pre-v37 database. This is
// the rehearsal-rig failure from the migration's own doc comment.
func TestMigrateV37ThenStoreLayerStoresTwoFilenamesForOneHash(t *testing.T) {
	dataDir := t.TempDir()
	rawDB, err := sql.Open("sqlite", filepath.Join(dataDir, dbFileName))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	ctx := context.Background()
	for _, m := range migrations {
		if m.version > 36 {
			continue
		}
		if m.fn != nil {
			tx, err := rawDB.BeginTx(ctx, nil)
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
		if _, err := rawDB.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
	}
	if _, err := rawDB.ExecContext(ctx, `PRAGMA user_version = 36`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	seedPreV37InventoryRow(t, rawDB, "pi-audio-01", "sha256:aaa", "Opening.fseq", 100, "2026-09-01T00:00:00Z")
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	st, err := Open(ctx, dataDir, nil)
	if err != nil {
		t.Fatalf("Open (runs migrate, including v37): %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	report := NodeAssetReportRecord{ReportedAt: now, Complete: true}
	items := []NodeAssetInventoryRecord{
		{ContentHash: "sha256:aaa", RuntimeFilename: "Opening.fseq", SizeBytes: 100, VerifiedAt: now},
		{ContentHash: "sha256:aaa", RuntimeFilename: "BorrowedFromRender01.fseq", SizeBytes: 100, VerifiedAt: now},
	}
	if err := st.ReplaceNodeAssetInventory(ctx, "pi-audio-01", items, report); err != nil {
		t.Fatalf("ReplaceNodeAssetInventory with one hash under two filenames: %v", err)
	}

	inv, err := st.GetNodeAssetInventory(ctx, "pi-audio-01")
	if err != nil {
		t.Fatalf("GetNodeAssetInventory: %v", err)
	}
	if len(inv) != 2 {
		t.Fatalf("inventory = %+v, want 2 rows (one per filename)", inv)
	}
	byFilename := make(map[string]NodeAssetInventoryRecord, len(inv))
	for _, item := range inv {
		byFilename[item.RuntimeFilename] = item
	}
	if _, ok := byFilename["Opening.fseq"]; !ok {
		t.Errorf("inventory missing Opening.fseq: %+v", inv)
	}
	if _, ok := byFilename["BorrowedFromRender01.fseq"]; !ok {
		t.Errorf("inventory missing BorrowedFromRender01.fseq: %+v", inv)
	}
}

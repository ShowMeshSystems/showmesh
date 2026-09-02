package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// openDatabaseAtV27 builds a database carrying every migration up to and
// including v27 and stamped at that version, so a test can seed assets rows
// the way a pre-v28 coordinator would have written them (under the
// pre-widening assets_current shape: unique on (show_id, sequence_id,
// target_kind, target_id) alone, with no media_type column in the index)
// and then watch v28 widen the index underneath them.
func openDatabaseAtV27(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pre-v28.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	for _, m := range migrations {
		if m.version > 27 {
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
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 27`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	return db
}

func seedPreV28Asset(t *testing.T, db *sql.DB, id, showID, sequenceID, targetKind, targetID, mediaType, contentHash, filename string) {
	t.Helper()
	now := timeToDB(time.Now())
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO assets (
			id, show_id, sequence_id, target_kind, target_id, media_type, content_hash,
			runtime_filename, size_bytes, backend, storage_key, created_at,
			created_by_principal_id, created_by_principal_name, superseded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1024, 'volume', ?, ?, 'principal-1', 'operator', NULL)
	`, id, showID, sequenceID, targetKind, targetID, mediaType, contentHash, filename, contentHash, now)
	if err != nil {
		t.Fatalf("seed pre-v28 asset %q: %v", id, err)
	}
}

// TestMigrateV28FromPreV28DatabaseWithExistingRows proves the migration's
// own doc comment: it is a pure widening, so every row a pre-v28 database
// already holds survives migration unchanged, with no data fix applied.
// Every pre-v28 current row necessarily holds exactly one media type per
// (show, sequence, target) tuple, since the OLD assets_current index never
// allowed a second one to become current regardless of media type — so
// nothing here needs backfilling, and this test seeds ordinary single-media-
// type rows to prove that state is untouched by the migration.
func TestMigrateV28FromPreV28DatabaseWithExistingRows(t *testing.T) {
	db := openDatabaseAtV27(t)
	seedPreV28Asset(t, db, "asset-1", "halloween-2026", "opening", "node", "render-01", "fseq", "sha256:aaa", "Opening.fseq")
	seedPreV28Asset(t, db, "asset-2", "halloween-2026", "closing", "node", "render-01", "fseq", "sha256:bbb", "Closing.fseq")

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	type row struct {
		mediaType, contentHash, filename string
	}
	readRow := func(id string) row {
		t.Helper()
		var r row
		err := db.QueryRowContext(context.Background(),
			`SELECT media_type, content_hash, runtime_filename FROM assets WHERE id = ? AND superseded_at IS NULL`, id).
			Scan(&r.mediaType, &r.contentHash, &r.filename)
		if err != nil {
			t.Fatalf("read migrated asset %q: %v", id, err)
		}
		return r
	}

	got := readRow("asset-1")
	want := row{mediaType: "fseq", contentHash: "sha256:aaa", filename: "Opening.fseq"}
	if got != want {
		t.Errorf("asset-1 after migration = %+v, want %+v", got, want)
	}
	got = readRow("asset-2")
	want = row{mediaType: "fseq", contentHash: "sha256:bbb", filename: "Closing.fseq"}
	if got != want {
		t.Errorf("asset-2 after migration = %+v, want %+v", got, want)
	}

	var total int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM assets`).Scan(&total); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if total != 2 {
		t.Fatalf("row count after migration = %d, want 2 (no row lost, duplicated, or backfilled)", total)
	}
}

// TestMigrateV28WidensAssetsCurrentToAllowBothMediaTypes proves the actual
// structural change: after migration, an FSEQ and an audio asset MAY both
// be current for the same (show, sequence, target) tuple at once — the
// exact case the pre-v28 index forbade and this migration exists to permit
// — while two current rows of the SAME media type for one tuple are still
// rejected.
func TestMigrateV28WidensAssetsCurrentToAllowBothMediaTypes(t *testing.T) {
	db := openDatabaseAtV27(t)
	seedPreV28Asset(t, db, "asset-1", "halloween-2026", "opening", "node", "render-01", "fseq", "sha256:aaa", "Opening.fseq")

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := timeToDB(time.Now())
	insert := `
		INSERT INTO assets (
			id, show_id, sequence_id, target_kind, target_id, media_type, content_hash,
			runtime_filename, size_bytes, backend, storage_key, created_at,
			created_by_principal_id, created_by_principal_name, superseded_at
		) VALUES (?, 'halloween-2026', 'opening', 'node', 'render-01', ?, ?, ?, 1024, 'volume', ?, ?, 'principal-1', 'operator', NULL)
	`
	// A second current row for the SAME tuple, DIFFERENT media type, must
	// now be insertable and coexist alongside asset-1 - impossible before
	// v28, since asset-1 (media_type='fseq') already holds the tuple under
	// the old index.
	if _, err := db.ExecContext(context.Background(), insert, "asset-2", "audio", "sha256:bbb", "Opening.wav", "sha256:bbb", now); err != nil {
		t.Fatalf("insert a second current row for the same tuple under a different media_type: %v (widened assets_current should allow this)", err)
	}
	// A second current row for the SAME tuple and the SAME media type must
	// still be rejected - the structural half of ADR-028 decision 1 is
	// narrower after this migration, not gone.
	if _, err := db.ExecContext(context.Background(), insert, "asset-3", "fseq", "sha256:ccc", "OpeningAgain.fseq", "sha256:ccc", now); err == nil {
		t.Fatalf("insert of a second CURRENT fseq row for the same tuple succeeded, want a UNIQUE constraint violation from the widened assets_current")
	} else if !isUniqueConstraintErr(err) {
		t.Errorf("error = %v, want modernc.org/sqlite's UNIQUE constraint failed text", err)
	}

	var current int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM assets WHERE show_id = 'halloween-2026' AND sequence_id = 'opening' AND target_kind = 'node' AND target_id = 'render-01' AND superseded_at IS NULL`,
	).Scan(&current); err != nil {
		t.Fatalf("count current rows for the tuple: %v", err)
	}
	if current != 2 {
		t.Fatalf("current rows for (halloween-2026, opening, node, render-01) = %d, want 2 (one fseq, one audio)", current)
	}
}

// TestMigrateV28ThenStoreLayerScopesSupersessionByMediaType proves the
// migrated database is immediately usable through this package's own
// CreateAsset: uploading an audio asset for a sequence that already holds a
// current FSEQ must not supersede the FSEQ - exercised through
// [Store.Open]/[migrate], the exact path a real coordinator restart takes
// against an on-disk pre-v28 database. This is the upload sequence from the
// issue: FSEQ registered current, then audio uploaded for the same sequence
// id, and the FSEQ must still be current afterward.
func TestMigrateV28ThenStoreLayerScopesSupersessionByMediaType(t *testing.T) {
	dataDir := t.TempDir()
	rawDB, err := sql.Open("sqlite", filepath.Join(dataDir, dbFileName))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	ctx := context.Background()
	for _, m := range migrations {
		if m.version > 27 {
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
	if _, err := rawDB.ExecContext(ctx, `PRAGMA user_version = 27`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	seedPreV28Asset(t, rawDB, "asset-1", "halloween-2026", "opening", "node", "render-01", "fseq", "sha256:aaa", "Opening.fseq")
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	st, err := Open(ctx, dataDir, nil)
	if err != nil {
		t.Fatalf("Open (runs migrate, including v28): %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, _, err := st.CreateAsset(ctx, AssetRecord{
		ID: "asset-2", ShowID: "halloween-2026", SequenceID: "opening",
		TargetKind: "node", TargetID: "render-01", MediaType: "audio",
		ContentHash: "sha256:bbb", RuntimeFilename: "Opening.wav", SizeBytes: 2048,
		Backend: "volume", StorageKey: "sha256:bbb",
		CreatedByPrincipalID: "principal-1", CreatedByPrincipalName: "operator",
	}); err != nil {
		t.Fatalf("CreateAsset audio for a sequence already holding a current fseq: %v", err)
	}

	fseq, err := st.GetAsset(ctx, "asset-1")
	if err != nil {
		t.Fatalf("GetAsset asset-1: %v", err)
	}
	if fseq.SupersededAt != nil {
		t.Fatalf("fseq asset-1 SupersededAt = %v, want nil (uploading an audio asset for the same sequence must not supersede it)", fseq.SupersededAt)
	}
	audio, err := st.GetAsset(ctx, "asset-2")
	if err != nil {
		t.Fatalf("GetAsset asset-2: %v", err)
	}
	if audio.SupersededAt != nil {
		t.Fatalf("audio asset-2 SupersededAt = %v, want nil (it is the current audio asset)", audio.SupersededAt)
	}
}

// migrate stamps the maximum migration version; v28 must advance PRAGMA
// user_version past 27 or every restart would re-run it.
func TestMigrateV28AdvancesTheSchemaVersion(t *testing.T) {
	db := openDatabaseAtV27(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() || version < 28 {
		t.Errorf("user_version = %d, want %d and at least 28", version, maxMigrationVersion())
	}
}

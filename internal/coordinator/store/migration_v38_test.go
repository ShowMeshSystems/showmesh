package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// openDatabaseAtV36 mirrors openDatabaseAtV27/openDatabaseAtV29 one
// migration further: a database carrying every migration up to and
// including v36, stamped at that version, so a test can watch v38 add
// audio_renditions underneath a store that predates it.
func openDatabaseAtV36(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pre-v38.db"))
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

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatalf("check table %q exists: %v", name, err)
	}
	return n == 1
}

// TestMigrateV38AddsAudioRenditionsTable proves a pre-v38 database has no
// audio_renditions table and that migrating it forward creates one, with no
// effect on any existing table.
func TestMigrateV38AddsAudioRenditionsTable(t *testing.T) {
	db := openDatabaseAtV36(t)
	if tableExists(t, db, "audio_renditions") {
		t.Fatal("audio_renditions exists before migrating to v38")
	}

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if !tableExists(t, db, "audio_renditions") {
		t.Fatal("audio_renditions does not exist after migrating to v38")
	}

	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() || version < 38 {
		t.Errorf("user_version = %d, want %d and at least 38", version, maxMigrationVersion())
	}
}

// TestMigrateV38ThenStoreLayerCanReadAndWriteRenditions proves the
// migration lands a usable table: once migrated, the Store-layer
// audio_renditions methods work against a pre-v38 database exactly as
// they would against one created fresh at v38 or later.
func TestMigrateV38ThenStoreLayerCanReadAndWriteRenditions(t *testing.T) {
	db := openDatabaseAtV36(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	st := &Store{db: db, now: time.Now}
	ctx := context.Background()

	if _, err := st.GetAudioRendition(ctx, "sha256:pre-v38"); !errors.Is(err, ErrAudioRenditionNotFound) {
		t.Fatalf("GetAudioRendition on an unseeded hash = %v, want ErrAudioRenditionNotFound", err)
	}
	if err := st.SetAudioRenditionReady(ctx, "sha256:pre-v38", AudioRenditionReady{
		ContentHash: "sha256:rendition", SizeBytes: 1024, DurationMillis: 500, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("SetAudioRenditionReady on a pre-v38 database: %v", err)
	}
	rec, err := st.GetAudioRendition(ctx, "sha256:pre-v38")
	if err != nil {
		t.Fatalf("GetAudioRendition after write: %v", err)
	}
	if rec.Status != AudioRenditionStatusReady || rec.ContentHash != "sha256:rendition" {
		t.Errorf("rendition record = %+v, want status ready and content hash sha256:rendition", rec)
	}
}

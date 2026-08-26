package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// openDatabaseAtV19 builds a database carrying every migration up to and
// including v19 and stamped at that version, so a test can seed
// audio_sessions rows the way a pre-v20 coordinator would have written
// them (under the bare `id TEXT PRIMARY KEY` schemaV9 shape) and then
// watch v20 re-key the table underneath them.
func openDatabaseAtV19(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pre-v20.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	for _, m := range migrations {
		if m.version > 19 {
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
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 19`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	return db
}

func seedPreV20AudioSession(t *testing.T, db *sql.DB, id, nodeID, desiredJSON string, revision int64, createdAt, updatedAt string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO audio_sessions (id, node_id, desired_json, revision, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, nodeID, desiredJSON, revision, createdAt, updatedAt)
	if err != nil {
		t.Fatalf("seed audio session %q/%q: %v", nodeID, id, err)
	}
}

// TestMigrateV20ReKeysAudioSessionsByNode is schemaV20's core migration
// property: every pre-v20 row survives with its desired_json, revision,
// and timestamps intact, AND the composite (node_id, id) key is actually
// in force afterward - proven by inserting a second row that reuses an
// id already present under a DIFFERENT node_id (impossible before v20,
// since id alone was the primary key) and confirming both rows coexist.
func TestMigrateV20ReKeysAudioSessionsByNode(t *testing.T) {
	db := openDatabaseAtV19(t)
	seedPreV20AudioSession(t, db, "cue", "node-a", `{"sourceRole":"show","node":"a"}`, 5,
		"2026-08-01T00:00:00Z", "2026-08-01T00:05:00Z")
	seedPreV20AudioSession(t, db, "blackAndSilence", "node-a", `{"sourceRole":"blackAndSilence"}`, 2,
		"2026-08-02T00:00:00Z", "2026-08-02T00:00:00Z")
	seedPreV20AudioSession(t, db, "cue-b", "node-b", `{"sourceRole":"show","node":"b-only"}`, 9,
		"2026-08-03T00:00:00Z", "2026-08-03T01:00:00Z")

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Every pre-migration row survives with every column intact.
	type row struct {
		nodeID, id, desiredJSON, createdAt, updatedAt string
		revision                                      int64
	}
	readRow := func(nodeID, id string) row {
		t.Helper()
		var r row
		err := db.QueryRowContext(context.Background(),
			`SELECT node_id, id, desired_json, revision, created_at, updated_at FROM audio_sessions WHERE node_id = ? AND id = ?`,
			nodeID, id).Scan(&r.nodeID, &r.id, &r.desiredJSON, &r.revision, &r.createdAt, &r.updatedAt)
		if err != nil {
			t.Fatalf("read migrated row (%q, %q): %v", nodeID, id, err)
		}
		return r
	}

	got := readRow("node-a", "cue")
	want := row{nodeID: "node-a", id: "cue", desiredJSON: `{"sourceRole":"show","node":"a"}`, revision: 5,
		createdAt: "2026-08-01T00:00:00Z", updatedAt: "2026-08-01T00:05:00Z"}
	if got != want {
		t.Errorf("node-a/cue = %+v, want %+v", got, want)
	}

	got = readRow("node-a", "blackAndSilence")
	want = row{nodeID: "node-a", id: "blackAndSilence", desiredJSON: `{"sourceRole":"blackAndSilence"}`, revision: 2,
		createdAt: "2026-08-02T00:00:00Z", updatedAt: "2026-08-02T00:00:00Z"}
	if got != want {
		t.Errorf("node-a/blackAndSilence = %+v, want %+v", got, want)
	}

	got = readRow("node-b", "cue-b")
	want = row{nodeID: "node-b", id: "cue-b", desiredJSON: `{"sourceRole":"show","node":"b-only"}`, revision: 9,
		createdAt: "2026-08-03T00:00:00Z", updatedAt: "2026-08-03T01:00:00Z"}
	if got != want {
		t.Errorf("node-b/cue-b = %+v, want %+v", got, want)
	}

	var total int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audio_sessions`).Scan(&total); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if total != 3 {
		t.Fatalf("row count after migration = %d, want 3 (no row lost or duplicated)", total)
	}

	// The composite key is actually in force: a second row reusing an id
	// already present under a DIFFERENT node_id must now be insertable
	// and coexist as its own row - under the pre-v20 `id`-only primary
	// key this INSERT would fail as a UNIQUE constraint violation.
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO audio_sessions (node_id, id, desired_json, revision, created_at, updated_at)
		 VALUES ('node-c', 'cue', '{"sourceRole":"show","node":"c"}', 1, '2026-08-04T00:00:00Z', '2026-08-04T00:00:00Z')`); err != nil {
		t.Fatalf("insert row reusing id %q under a new node_id: %v", "cue", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO audio_sessions (node_id, id, desired_json, revision, created_at, updated_at)
		 VALUES ('node-a', 'cue', '{"sourceRole":"show","node":"a-dup"}', 1, '2026-08-04T00:00:00Z', '2026-08-04T00:00:00Z')`); err == nil {
		t.Fatalf("inserting a second row for an EXISTING (node_id, id) pair must fail a PRIMARY KEY violation")
	}

	got = readRow("node-c", "cue")
	if got.desiredJSON != `{"sourceRole":"show","node":"c"}` || got.revision != 1 {
		t.Errorf("node-c/cue = %+v, want the freshly inserted row", got)
	}
	// node-a's original "cue" row must still be exactly as migrated.
	got = readRow("node-a", "cue")
	if got.revision != 5 || got.desiredJSON != `{"sourceRole":"show","node":"a"}` {
		t.Errorf("node-a/cue mutated by node-c's insert of the same id: %+v", got)
	}
}

// migrate stamps the maximum migration version; v20 must advance
// PRAGMA user_version past 19 or every restart would re-run it.
func TestMigrateV20AdvancesTheSchemaVersion(t *testing.T) {
	db := openDatabaseAtV19(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() || version < 20 {
		t.Errorf("user_version = %d, want %d and at least 20", version, maxMigrationVersion())
	}
}

// TestMigrateV20ThenStoreLayerSeesCompositeKeyBehavior proves the migrated
// database is not just structurally correct but immediately usable
// through this package's own repository methods: the store layer's own
// (node_id, id) scoping (audiosessions.go) must see a migrated
// row exactly as GetAudioSession/PutAudioSession expect it, and a second
// node writing the SAME session id afterward must not disturb it -
// exercised through [Store.Open]/[migrate], the exact path a real
// coordinator restart takes against an on-disk pre-v20 database.
func TestMigrateV20ThenStoreLayerSeesCompositeKeyBehavior(t *testing.T) {
	dataDir := t.TempDir()
	rawDB, err := sql.Open("sqlite", filepath.Join(dataDir, dbFileName))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	ctx := context.Background()
	for _, m := range migrations {
		if m.version > 19 {
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
	if _, err := rawDB.ExecContext(ctx, `PRAGMA user_version = 19`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	seedPreV20AudioSession(t, rawDB, "cue", "node-a", `{"sourceRole":"show"}`, 5,
		"2026-08-01T00:00:00Z", "2026-08-01T00:05:00Z")
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	st, err := Open(ctx, dataDir, nil)
	if err != nil {
		t.Fatalf("Open (runs migrate, including v20): %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	got, err := st.GetAudioSession(ctx, "node-a", "cue")
	if err != nil {
		t.Fatalf("GetAudioSession node-a/cue after migration: %v", err)
	}
	if got.Revision != 5 || got.DesiredJSON != `{"sourceRole":"show"}` {
		t.Fatalf("migrated row read through the store layer = %+v", got)
	}

	// A second node writing the SAME session id afterward must be its own
	// row, independent of node-a's migrated one.
	if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "cue", NodeID: "node-b", DesiredJSON: `{"sourceRole":"show","node":"b"}`, Revision: 1}); err != nil {
		t.Fatalf("PutAudioSession node-b: %v", err)
	}
	gotA, err := st.GetAudioSession(ctx, "node-a", "cue")
	if err != nil {
		t.Fatalf("GetAudioSession node-a/cue after node-b write: %v", err)
	}
	if gotA.Revision != 5 {
		t.Fatalf("node-a's migrated row disturbed by node-b's write: %+v", gotA)
	}
	gotB, err := st.GetAudioSession(ctx, "node-b", "cue")
	if err != nil {
		t.Fatalf("GetAudioSession node-b/cue: %v", err)
	}
	if gotB.Revision != 1 {
		t.Fatalf("node-b's own write did not persist: %+v", gotB)
	}
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// openDatabaseAtV18 builds a database carrying every migration up to and
// including v18 and stamped at that version, so a test can seed rows the
// way a pre-v19 coordinator would have written them and then watch v19
// run against them.
func openDatabaseAtV18(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pre-v19.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	for _, m := range migrations {
		if m.version > 18 {
			continue
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 18`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	return db
}

func seedAudioSettingsRevision(t *testing.T, db *sql.DB, revision int64, payload string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO config_revisions (kind, object_id, revision, payload_json, created_at, source)
		 VALUES (?, 'default', ?, ?, '2026-08-01T00:00:00Z', 'api')`,
		audioSettingsKind, revision, payload)
	if err != nil {
		t.Fatalf("seed revision %d: %v", revision, err)
	}
}

func readAudioSettingsRevision(t *testing.T, db *sql.DB, revision int64) map[string]any {
	t.Helper()
	var raw string
	err := db.QueryRowContext(context.Background(),
		`SELECT payload_json FROM config_revisions WHERE kind = ? AND object_id = 'default' AND revision = ?`,
		audioSettingsKind, revision).Scan(&raw)
	if err != nil {
		t.Fatalf("read revision %d: %v", revision, err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode revision %d: %v", revision, err)
	}
	return out
}

// The acceptance property: a revision stored before the change reads back
// at the same audible level after it. The stored numbers change unit and
// name; the level an operator would hear does not.
func TestMigrateV19KeepsStoredRevisionsAtTheSameLevel(t *testing.T) {
	db := openDatabaseAtV18(t)
	seedAudioSettingsRevision(t, db, 1,
		`{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,`+
			`"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.25,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`)
	seedAudioSettingsRevision(t, db, 2,
		`{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,`+
			`"defaultMaxBackgroundGain":1,"duckTargetGain":0,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`)

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, tc := range []struct {
		revision            int64
		wantMax, wantDuck   float64
		wantDuckExactSilent bool
	}{
		{revision: 1, wantMax: 0.6, wantDuck: 0.25},
		{revision: 2, wantMax: 1.0, wantDuck: 0, wantDuckExactSilent: true},
	} {
		got := readAudioSettingsRevision(t, db, tc.revision)
		for _, gone := range []string{"defaultMaxBackgroundGain", "duckTargetGain"} {
			if _, present := got[gone]; present {
				t.Fatalf("revision %d still carries %s after migration", tc.revision, gone)
			}
		}

		maxDb, ok := got["defaultMaxBackgroundGainDb"].(float64)
		if !ok {
			t.Fatalf("revision %d has no numeric defaultMaxBackgroundGainDb: %v", tc.revision, got["defaultMaxBackgroundGainDb"])
		}
		if back := float64(pkgaudio.CeilingFromDb(maxDb)); math.Abs(back-tc.wantMax) > tc.wantMax*0.001 {
			t.Errorf("revision %d ceiling converts back to %v, want %v (the level it was stored at)", tc.revision, back, tc.wantMax)
		}

		duckDb, ok := got["duckTargetGainDb"].(float64)
		if !ok {
			t.Fatalf("revision %d has no numeric duckTargetGainDb: %v", tc.revision, got["duckTargetGainDb"])
		}
		back := float64(pkgaudio.GainFromDb(duckDb))
		if tc.wantDuckExactSilent {
			if back != 0 {
				t.Errorf("revision %d duck was stored as full silence and converts back to %v, want exactly 0", tc.revision, back)
			}
			continue
		}
		if math.Abs(back-tc.wantDuck) > tc.wantDuck*0.001 {
			t.Errorf("revision %d duck converts back to %v, want %v (the level it was stored at)", tc.revision, back, tc.wantDuck)
		}
	}
}

// The migrated payload must not just round-trip through the arithmetic:
// it must still decode through the shipped validator. The old bound
// allowed a ceiling of exactly 4.0, which is +12.04 dB and just past the
// new +12 dB bound, so that one is clamped rather than left unreadable.
func TestMigrateV19ClampsTheOldCeilingBoundIntoRange(t *testing.T) {
	db := openDatabaseAtV18(t)
	seedAudioSettingsRevision(t, db, 1,
		`{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,`+
			`"defaultMaxBackgroundGain":4,"duckTargetGain":0.25,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`)

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got := readAudioSettingsRevision(t, db, 1)
	if maxDb := got["defaultMaxBackgroundGainDb"].(float64); maxDb != v19MaxBackgroundGainDbCeiling {
		t.Errorf("defaultMaxBackgroundGainDb = %v, want it clamped to %v", maxDb, v19MaxBackgroundGainDbCeiling)
	}
}

// A revision already written in decibels is left exactly as it is: this
// migration owns two key names and nothing else, and re-running it must
// never convert a value twice.
func TestMigrateV19LeavesAlreadyMigratedRevisionsAlone(t *testing.T) {
	const already = `{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,` +
		`"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-12.04,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`

	db := openDatabaseAtV18(t)
	seedAudioSettingsRevision(t, db, 1, already)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var raw string
	if err := db.QueryRowContext(context.Background(),
		`SELECT payload_json FROM config_revisions WHERE kind = ? AND revision = 1`, audioSettingsKind).Scan(&raw); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if raw != already {
		t.Errorf("payload = %s, want it byte-for-byte unchanged:\n%s", raw, already)
	}
}

// Every OTHER configuration kind is untouched: the two key names this
// migration rewrites are not reserved words, and a kind that happens to
// use one must not be rewritten under audio.settings' rules.
func TestMigrateV19TouchesOnlyAudioSettings(t *testing.T) {
	const other = `{"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.25}`

	db := openDatabaseAtV18(t)
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO config_revisions (kind, object_id, revision, payload_json, created_at, source)
		 VALUES ('render.settings', 'default', 1, ?, '2026-08-01T00:00:00Z', 'api')`, other)
	if err != nil {
		t.Fatalf("seed other kind: %v", err)
	}

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var raw string
	if err := db.QueryRowContext(context.Background(),
		`SELECT payload_json FROM config_revisions WHERE kind = 'render.settings' AND revision = 1`).Scan(&raw); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if raw != other {
		t.Errorf("render.settings payload = %s, want it unchanged: %s", raw, other)
	}
}

// migrate stamps the maximum migration version, and v19 is now that
// maximum. A version-only migration whose work is a value rewrite must
// still advance PRAGMA user_version, or every restart re-runs it.
func TestMigrateV19AdvancesTheSchemaVersion(t *testing.T) {
	db := openDatabaseAtV18(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() || version < 19 {
		t.Errorf("user_version = %d, want %d and at least 19", version, maxMigrationVersion())
	}
}

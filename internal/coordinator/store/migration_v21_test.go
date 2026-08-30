package store

import (
	"context"
	"testing"
)

// The acceptance property: a revision written before
// duckFadeDurationMs/duckRestoreFadeDurationMs existed still decodes
// after this migration, carrying each field's own default.
func TestMigrateV21BackfillsMissingDuckFadeDurations(t *testing.T) {
	db := openDatabaseAtV18(t)
	seedAudioSettingsRevision(t, db, 1,
		`{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,`+
			`"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-12.04,`+
			`"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`)

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got := readAudioSettingsRevision(t, db, 1)
	if fade, ok := got["duckFadeDurationMs"].(float64); !ok || fade != 200 {
		t.Errorf("duckFadeDurationMs = %v, want the backfilled default 200", got["duckFadeDurationMs"])
	}
	if restore, ok := got["duckRestoreFadeDurationMs"].(float64); !ok || restore != 800 {
		t.Errorf("duckRestoreFadeDurationMs = %v, want the backfilled default 800", got["duckRestoreFadeDurationMs"])
	}
}

// A revision that already carries both keys is left byte-for-byte alone.
func TestMigrateV21LeavesCompletePayloadsAlone(t *testing.T) {
	// Values deliberately differ from v21's own backfill defaults, so a
	// mutation that overwrote a present key rather than skipping it would
	// change this payload and fail the byte-for-byte check below.
	const complete = `{"driftIgnoreThresholdMs":500,"defaultFadeCurve":"linear","defaultFadeDurationMs":2500,` +
		`"defaultMaxBackgroundGainDb":-6.0,"duckTargetGainDb":-8.5,"duckFadeDurationMs":150,` +
		`"duckRestoreFadeDurationMs":900,"ltcFrameRate":"25","ltcDefaultStartOffset":"01:02:03:04"}`

	db := openDatabaseAtV18(t)
	seedAudioSettingsRevision(t, db, 1, complete)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var raw string
	if err := db.QueryRowContext(context.Background(),
		`SELECT payload_json FROM config_revisions WHERE kind = ? AND revision = 1`, audioSettingsKind).Scan(&raw); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if raw != complete {
		t.Errorf("payload = %s, want it byte-for-byte unchanged:\n%s", raw, complete)
	}
}

// A stored payload_json of the JSON literal null decodes to a nil map,
// not an error, exactly as v20's own equivalent test proves; this
// migration must handle it without panicking too.
func TestMigrateV21HandlesNullPayloadWithoutPanicking(t *testing.T) {
	db := openDatabaseAtV18(t)
	seedAudioSettingsRevision(t, db, 1, `null`)

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var raw string
	if err := db.QueryRowContext(context.Background(),
		`SELECT payload_json FROM config_revisions WHERE kind = ? AND revision = 1`, audioSettingsKind).Scan(&raw); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if raw != "null" {
		t.Errorf("payload = %s, want it left unchanged: null", raw)
	}
}

// migrate stamps the maximum migration version, and v21 is now that
// maximum.
func TestMigrateV21AdvancesTheSchemaVersion(t *testing.T) {
	db := openDatabaseAtV18(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() || version < 21 {
		t.Errorf("user_version = %d, want %d and at least 21", version, maxMigrationVersion())
	}
}

package store

import (
	"context"
	"testing"
)

// The acceptance property: a revision written before any of
// ltcFrameRate, ltcDefaultStartOffset, or duckTargetGainDb existed still
// decodes after this migration, carrying the value the field's own
// default already states.
func TestMigrateV20BackfillsMissingRequiredFields(t *testing.T) {
	db := openDatabaseAtV18(t)
	// A revision from before ltcFrameRate/ltcDefaultStartOffset were ever
	// added to the required set.
	seedAudioSettingsRevision(t, db, 1,
		`{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,`+
			`"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-12.04}`)
	// A revision from before duckTargetGain (by any name) was ever added.
	seedAudioSettingsRevision(t, db, 2,
		`{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,`+
			`"defaultMaxBackgroundGainDb":-4.44,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`)

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got1 := readAudioSettingsRevision(t, db, 1)
	if got1["ltcFrameRate"] != "30" {
		t.Errorf("revision 1 ltcFrameRate = %v, want the backfilled default \"30\"", got1["ltcFrameRate"])
	}
	if got1["ltcDefaultStartOffset"] != "00:00:00:00" {
		t.Errorf("revision 1 ltcDefaultStartOffset = %v, want the backfilled default", got1["ltcDefaultStartOffset"])
	}

	got2 := readAudioSettingsRevision(t, db, 2)
	if duck, ok := got2["duckTargetGainDb"].(float64); !ok || duck != -12.04 {
		t.Errorf("revision 2 duckTargetGainDb = %v, want the backfilled default -12.04", got2["duckTargetGainDb"])
	}
}

// A revision that already carries every required key is left
// byte-for-byte alone: this migration only ever adds an absent key, never
// touches one that is already present, however it got there.
func TestMigrateV20LeavesCompletePayloadsAlone(t *testing.T) {
	// Every value deliberately differs from v20's own backfill defaults,
	// so a mutation that overwrote a present key rather than skipping it
	// would change this payload and fail the byte-for-byte check below.
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
	// Byte-for-byte, not decode-and-compare: a decode-then-reencode
	// (map iteration reorders keys) would pass a decoded-map comparison
	// while still rewriting a payload this migration had no business
	// touching.
	if raw != complete {
		t.Errorf("payload = %s, want it byte-for-byte unchanged:\n%s", raw, complete)
	}
}

// Every OTHER configuration kind is untouched: none of the seven backfill
// keys are reserved words, and a kind that happens to use one must not be
// rewritten under audio.settings' rules.
func TestMigrateV20TouchesOnlyAudioSettings(t *testing.T) {
	const other = `{"driftIgnoreThresholdMs":20}`

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

// A stored payload_json of the JSON literal null decodes to a nil map,
// not an error: json.Unmarshal leaves top nil rather than failing. v19
// handles this cleanly (neither renamed field is present in a nil map, so
// it reports unchanged); this migration must too, rather than panicking
// with "assignment to entry in nil map" when it tries to backfill a key
// into that nil map. A coordinator meeting this payload hits it inside
// store.Open, so a panic here means the coordinator never starts.
func TestMigrateV20HandlesNullPayloadWithoutPanicking(t *testing.T) {
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

// migrate stamps the maximum migration version, and v20 is now that
// maximum.
func TestMigrateV20AdvancesTheSchemaVersion(t *testing.T) {
	db := openDatabaseAtV18(t)
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

package audioconfigpush

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"

	_ "modernc.org/sqlite" // the same pure-Go driver internal/coordinator/store registers
)

// This file is this issue's own acceptance: a stored audio.settings
// revision written before a field was added to DecodeAudioSettingsPayload's
// required set must not silently strand every node when a coordinator
// carrying the newer, stricter decoder opens that revision. ltcFrameRate,
// ltcDefaultStartOffset, and duckTargetGainDb (by name; duckTargetGain
// before it) were each added to the required set at a different point in
// this project's history with no migration backfilling the field into
// revisions written before that point — this is the resulting gap,
// reproduced directly against the decoder rather than hypothesised.

// writeDatabaseAtCurrentSchemaMissingField creates a coordinator data
// directory holding one audio.settings revision whose JSON never carried
// fieldName at all — the shape of a revision written before that field
// existed, not a revision that carried the old pre-rename key — then
// stamps the schema version back so that reopening it with the current
// binary runs every pending migration against the seeded row, exactly as
// an upgraded coordinator reopening its own real data directory would.
func writeDatabaseAtCurrentSchemaMissingField(t *testing.T, payloadJSON string) string {
	t.Helper()
	dir := t.TempDir()

	// Reaching the newest schema version is the store's own business:
	// open and close once with the current binary first, exactly as
	// writePreDecibelDatabase (migratedrevision_test.go) does.
	st, err := store.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("store.Open (initial): %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, "showmesh.db"))
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO config_objects (kind, id, current_revision, created_at, updated_at)
		 VALUES (?, ?, 1, '2026-08-01T00:00:00Z', '2026-08-01T00:00:00Z')`,
		config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID); err != nil {
		t.Fatalf("seed config object: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO config_revisions (kind, object_id, revision, payload_json, created_at, source)
		 VALUES (?, ?, 1, ?, '2026-08-01T00:00:00Z', ?)`,
		config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, payloadJSON,
		config.AudioSettingsSourceAPI); err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	// Stamp back to the version below the one that owns backfilling a
	// missing audio.settings field, so reopening below re-runs it against
	// this seeded row instead of finding the schema already current and
	// doing nothing: schemaV19 is the newest migration that predates this
	// issue's fix.
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 19`); err != nil {
		t.Fatalf("stamp user_version back to 19: %v", err)
	}
	return dir
}

// TestStoredRevisionMissingNewlyRequiredFieldDoesNotStrandNode is this
// issue's reproduction. Each case is a revision shaped the way a real coordinator
// wrote it before the named field was ever added to the required set: on
// the unmodified tree, ToNode returns a decode error and NO
// audio.settings.configure is ever published for the node — the node is
// stranded, silently, with nothing on the wire and nothing but a Warn log
// line an operator surface never shows.
func TestStoredRevisionMissingNewlyRequiredFieldDoesNotStrandNode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{
			name: "missing ltcFrameRate and ltcDefaultStartOffset (pre-Track-C5 revision)",
			payload: `{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,` +
				`"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-12.04}`,
		},
		{
			name: "missing duckTargetGainDb (pre-PR-88 revision)",
			payload: `{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,` +
				`"defaultMaxBackgroundGainDb":-4.44,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeDatabaseAtCurrentSchemaMissingField(t, tc.payload)

			st, err := store.Open(context.Background(), dir, nil)
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			defer func() { _ = st.Close() }()

			pub := &fakePublisher{}
			pushErr := ToNode(context.Background(), st, pub, time.Now, "node-a")

			_, published := pub.actionParams("audio.settings.configure")

			// This is the required acceptance property: the coordinator
			// must not both (a) fail to push and (b) leave nothing for an
			// operator to see. On the unmodified tree neither holds — ToNode
			// fails AND nothing is published, which is exactly the silent
			// strand this test exists to catch.
			if pushErr != nil && !published {
				t.Fatalf("node-a was silently stranded: ToNode returned %v and audio.settings.configure was never published; "+
					"the fix must either push with the missing field defaulted, or surface the refusal somewhere an operator sees it", pushErr)
			}
		})
	}
}

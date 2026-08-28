package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// v20AudioSettingsRequiredFieldDefaults is the value backfilled into a
// stored audio.settings revision for each of DecodeAudioSettingsPayload's
// currently-required top-level keys, when that key is entirely absent
// from the stored JSON rather than merely renamed (schemaV19's
// migrateV19AudioSettingsGainToDb already owns the rename case).
// ltcFrameRate, ltcDefaultStartOffset, and duckTargetGain (by its
// pre-decibel name) were each added to the required set after this
// project's first audio.settings writer shipped, with no migration ever
// backfilling the field into a revision written before that point — a
// coordinator upgraded across any one of those changes decoded its own
// stored revision, failed, and silently stopped pushing audio
// configuration to every node. Repeated here as literals for the same
// reason schemaV19's bounds are: a migration must keep applying the
// values it shipped with, even if [config.AudioSettingsDefaultPayload]
// moves later. This migration owns exactly these seven keys' PRESENCE; it
// never touches a key that is already there, however it got there.
var v20AudioSettingsRequiredFieldDefaults = map[string]json.RawMessage{
	"driftIgnoreThresholdMs":     json.RawMessage(`20`),
	"defaultFadeCurve":           json.RawMessage(`"linear"`),
	"defaultFadeDurationMs":      json.RawMessage(`1000`),
	"defaultMaxBackgroundGainDb": json.RawMessage(`-4.44`),
	"duckTargetGainDb":           json.RawMessage(`-12.04`),
	"ltcFrameRate":               json.RawMessage(`"30"`),
	"ltcDefaultStartOffset":      json.RawMessage(`"00:00:00:00"`),
}

// migrateV20AudioSettingsBackfillMissingRequiredFields makes every stored
// audio.settings revision decode under the validator shipping today,
// regardless of which of its required fields existed when the revision
// was originally written. It is a value migration, not a table change,
// matching migrateV19AudioSettingsGainToDb one migration below it: the
// payload column's shape is unchanged, and a revision that already
// carries every required key is left byte-for-byte alone.
//
// config_revisions is immutable under ADR-009 as an OPERATOR-facing rule
// (no API path edits a revision); this is the store's own forward-only
// schema change filling in a value the field's own default already
// states for every coordinator that predates the field, exactly as
// migrateV19AudioSettingsGainToDb's doc comment already establishes for
// this same table.
func migrateV20AudioSettingsBackfillMissingRequiredFields(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT object_id, revision, payload_json FROM config_revisions WHERE kind = ?`, audioSettingsKind)
	if err != nil {
		return fmt.Errorf("read audio.settings revisions: %w", err)
	}

	type rewrite struct {
		objectID string
		revision int64
		payload  string
	}
	var pending []rewrite
	for rows.Next() {
		var r rewrite
		if err := rows.Scan(&r.objectID, &r.revision, &r.payload); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan audio.settings revision: %w", err)
		}
		rewritten, changed, err := v20BackfillAudioSettingsPayload(r.payload)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("backfill audio.settings revision %d: %w", r.revision, err)
		}
		if !changed {
			continue
		}
		r.payload = rewritten
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read audio.settings revisions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("read audio.settings revisions: %w", err)
	}

	for _, r := range pending {
		if _, err := tx.ExecContext(ctx,
			`UPDATE config_revisions SET payload_json = ? WHERE kind = ? AND object_id = ? AND revision = ?`,
			r.payload, audioSettingsKind, r.objectID, r.revision); err != nil {
			return fmt.Errorf("write backfilled audio.settings revision %d: %w", r.revision, err)
		}
	}
	return nil
}

// v20BackfillAudioSettingsPayload adds any of
// [v20AudioSettingsRequiredFieldDefaults]'s keys missing from raw's
// top-level object, changing nothing else. It reports changed=false, with
// no error, for a payload that already carries all seven keys.
func v20BackfillAudioSettingsPayload(raw string) (string, bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return "", false, fmt.Errorf("stored payload is not a JSON object: %w", err)
	}
	if top == nil {
		// raw is the JSON literal null, not an object: json.Unmarshal
		// leaves top nil rather than failing. There is no top-level
		// object to backfill a key into, so report unchanged, matching
		// migrateV19AudioSettingsGainToDb's own handling of the same
		// input one migration below this one.
		return "", false, nil
	}

	changed := false
	for key, def := range v20AudioSettingsRequiredFieldDefaults {
		if _, present := top[key]; present {
			continue
		}
		top[key] = def
		changed = true
	}
	if !changed {
		return "", false, nil
	}

	out, err := json.Marshal(top)
	if err != nil {
		return "", false, fmt.Errorf("encode backfilled payload: %w", err)
	}
	return string(out), true, nil
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// v21AudioSettingsRequiredFieldDefaults is the value backfilled into a
// stored audio.settings revision for duckFadeDurationMs and
// duckRestoreFadeDurationMs when either key is entirely absent from the
// stored JSON: both joined the required set after this project's first
// audio.settings writer shipped, with no migration ever backfilling them
// into a revision written before that point — the same defect class
// [migrateV20AudioSettingsBackfillMissingRequiredFields] already closed
// for its own seven keys, reached again here because these two are new.
// Repeated here as literals for the same reason schemaV19's bounds are: a
// migration must keep applying the values it shipped with, even if
// [config.AudioSettingsDefaultPayload] moves later.
var v21AudioSettingsRequiredFieldDefaults = map[string]json.RawMessage{
	"duckFadeDurationMs":        json.RawMessage(`200`),
	"duckRestoreFadeDurationMs": json.RawMessage(`800`),
}

// migrateV21AudioSettingsBackfillDuckFadeDurations makes every stored
// audio.settings revision decode under the validator shipping today,
// regardless of whether it predates duckFadeDurationMs/
// duckRestoreFadeDurationMs. A value migration, not a table change,
// matching migrateV20AudioSettingsBackfillMissingRequiredFields one
// migration below it: the payload column's shape is unchanged, and a
// revision that already carries both keys is left byte-for-byte alone.
func migrateV21AudioSettingsBackfillDuckFadeDurations(ctx context.Context, tx *sql.Tx) error {
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
		rewritten, changed, err := v21BackfillAudioSettingsPayload(r.payload)
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

// v21BackfillAudioSettingsPayload adds any of
// [v21AudioSettingsRequiredFieldDefaults]'s keys missing from raw's
// top-level object, changing nothing else. It reports changed=false, with
// no error, for a payload that already carries both keys.
func v21BackfillAudioSettingsPayload(raw string) (string, bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return "", false, fmt.Errorf("stored payload is not a JSON object: %w", err)
	}
	if top == nil {
		// raw is the JSON literal null, not an object: json.Unmarshal
		// leaves top nil rather than failing. There is no top-level
		// object to backfill a key into, so report unchanged, matching
		// migrateV20AudioSettingsBackfillMissingRequiredFields's own
		// handling of the same input one migration below this one.
		return "", false, nil
	}

	changed := false
	for key, def := range v21AudioSettingsRequiredFieldDefaults {
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

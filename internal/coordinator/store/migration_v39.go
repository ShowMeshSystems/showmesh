package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// v39AudioSettingsMultisyncFallbackWindowDefault is the value backfilled
// into a stored audio.settings revision for multisyncFallbackWindowMs
// (ADR-051 decision 4), when that key is absent from the stored JSON.
//
// Repeated here as a literal rather than read from
// [config.AudioSettingsDefaultPayload], for the reason
// v20AudioSettingsRequiredFieldDefaults records: a migration must keep
// applying the value it shipped with, even after the package default
// moves.
var v39AudioSettingsMultisyncFallbackWindowDefault = json.RawMessage(`1500`)

// migrateV39AudioSettingsBackfillMultisyncFallbackWindow is
// migrateV34AudioSettingsBackfillScheduledStartFields's successor for the
// one key added after it: without it, a coordinator upgraded across this
// change decodes its own stored audio.settings revision, fails on a
// required key that did not exist when the revision was written, and
// silently stops pushing audio configuration to every node. That is the
// exact defect v20's own doc comment describes, and it is a new migration
// rather than another entry in v34's own map because a shipped migration
// keeps applying the values it shipped with.
//
// A revision that already carries the key is left byte-for-byte alone.
func migrateV39AudioSettingsBackfillMultisyncFallbackWindow(ctx context.Context, tx *sql.Tx) error {
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
		rewritten, changed, err := v39BackfillAudioSettingsMultisyncFallbackWindow(r.payload)
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

// v39BackfillAudioSettingsMultisyncFallbackWindow adds
// multisyncFallbackWindowMs to raw's top-level object when it is absent,
// changing nothing else. It reports changed=false, with no error, for a
// payload that already carries the key and for the JSON literal null,
// matching v34BackfillAudioSettingsScheduledStart exactly.
func v39BackfillAudioSettingsMultisyncFallbackWindow(raw string) (string, bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return "", false, fmt.Errorf("stored payload is not a JSON object: %w", err)
	}
	if top == nil {
		return "", false, nil
	}
	if _, present := top["multisyncFallbackWindowMs"]; present {
		return "", false, nil
	}
	top["multisyncFallbackWindowMs"] = v39AudioSettingsMultisyncFallbackWindowDefault

	out, err := json.Marshal(top)
	if err != nil {
		return "", false, fmt.Errorf("encode backfilled payload: %w", err)
	}
	return string(out), true, nil
}

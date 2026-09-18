package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// v39AudioSettingsDefaults is the value backfilled into a stored
// audio.settings revision for each of these two ADR-051 keys, when that
// key is absent from the stored JSON: multisyncFallbackWindowMs (decision
// 4, the coordinator's own fallback wait) and multisyncStartLeadMs
// (decision 1, the node's own fixed lead past a MultiSync START packet's
// arrival).
//
// Repeated here as literals rather than read from
// [config.AudioSettingsDefaultPayload], for the reason
// v20AudioSettingsRequiredFieldDefaults records: a migration must keep
// applying the values it shipped with, even after the package default
// moves.
var v39AudioSettingsDefaults = map[string]json.RawMessage{
	"multisyncFallbackWindowMs": json.RawMessage(`1500`),
	"multisyncStartLeadMs":      json.RawMessage(`100`),
}

// migrateV39AudioSettingsBackfillMultisyncFields is
// migrateV34AudioSettingsBackfillScheduledStartFields's successor for the
// two keys added after it: without it, a coordinator upgraded across
// this change decodes its own stored audio.settings revision, fails on a
// required key that did not exist when the revision was written, and
// silently stops pushing audio configuration to every node. That is the
// exact defect v20's own doc comment describes, and it is a new
// migration rather than another entry in v34's own map because a shipped
// migration keeps applying the values it shipped with.
//
// A revision that already carries both keys is left byte-for-byte alone.
func migrateV39AudioSettingsBackfillMultisyncFields(ctx context.Context, tx *sql.Tx) error {
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
		rewritten, changed, err := v39BackfillAudioSettingsMultisyncFields(r.payload)
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

// v39BackfillAudioSettingsMultisyncFields adds any of
// [v39AudioSettingsDefaults]'s keys missing from raw's top-level object,
// changing nothing else. It reports changed=false, with no error, for a
// payload that already carries both keys and for the JSON literal null,
// matching v34BackfillAudioSettingsScheduledStart exactly.
func v39BackfillAudioSettingsMultisyncFields(raw string) (string, bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return "", false, fmt.Errorf("stored payload is not a JSON object: %w", err)
	}
	if top == nil {
		return "", false, nil
	}

	changed := false
	for key, def := range v39AudioSettingsDefaults {
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

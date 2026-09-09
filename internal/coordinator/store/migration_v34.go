package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// v34AudioSettingsScheduledStartDefaults is the value backfilled into a
// stored audio.settings revision for each of the two scheduled-start keys
// DecodeAudioSettingsPayload requires as of Track I seam I2, when that
// key is absent from the stored JSON.
//
// Repeated here as literals rather than read from
// [config.AudioSettingsDefaultPayload], for the reason
// v20AudioSettingsRequiredFieldDefaults records: a migration must keep
// applying the values it shipped with, even after the package default
// moves. These two are guesses, and a later ruling that changes the
// default must not silently rewrite what an upgraded coordinator already
// stored.
var v34AudioSettingsScheduledStartDefaults = map[string]json.RawMessage{
	"scheduledStartDeliveryBoundMs": json.RawMessage(`2000`),
	"scheduledStartMarginMs":        json.RawMessage(`1000`),
}

// migrateV34AudioSettingsBackfillScheduledStartFields is
// migrateV20AudioSettingsBackfillMissingRequiredFields' successor for the
// two keys added after it: without it, a coordinator upgraded across this
// change decodes its own stored audio.settings revision, fails on a
// required key that did not exist when the revision was written, and
// silently stops pushing audio configuration to every node. That is the
// exact defect v20's own doc comment describes, and it is a new migration
// rather than two more entries in v20's map because a shipped migration
// keeps applying the values it shipped with.
//
// A revision that already carries both keys is left byte-for-byte alone.
func migrateV34AudioSettingsBackfillScheduledStartFields(ctx context.Context, tx *sql.Tx) error {
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
		rewritten, changed, err := v34BackfillAudioSettingsScheduledStart(r.payload)
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

// v34BackfillAudioSettingsScheduledStart adds any of
// [v34AudioSettingsScheduledStartDefaults]'s keys missing from raw's
// top-level object, changing nothing else. It reports changed=false, with
// no error, for a payload that already carries both keys and for the JSON
// literal null, matching v20BackfillAudioSettingsPayload exactly.
func v34BackfillAudioSettingsScheduledStart(raw string) (string, bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return "", false, fmt.Errorf("stored payload is not a JSON object: %w", err)
	}
	if top == nil {
		return "", false, nil
	}

	changed := false
	for key, def := range v34AudioSettingsScheduledStartDefaults {
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

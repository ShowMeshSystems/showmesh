package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// audioSettingsKind is config_revisions.kind for the audio.settings
// singleton. Spelled here rather than imported from
// internal/coordinator/config so this package keeps its one-way
// dependency on nothing above it.
const audioSettingsKind = "audio.settings"

// v19MaxBackgroundGainDbCeiling and v19DuckTargetGainDbCeiling are the
// bounds the post-change validator enforces, repeated as literals on
// purpose: a migration must keep applying the rule that was true when it
// shipped, even if a later revision of the validator moves the bound.
const (
	v19MaxBackgroundGainDbCeiling = 12.0
	v19DuckTargetGainDbCeiling    = 0.0
)

// migrateV19AudioSettingsGainToDb rewrites every stored audio.settings
// revision's two operator gain fields from a linear amplitude multiplier
// to decibels under their new names (operators enter dB
// everywhere, the engine keeps linear). It is a value migration, not a
// table change: the payload column's shape is unchanged and only the two
// gain members inside it move.
//
// Each revision is rewritten in place rather than reinterpreted, because
// the two units share a number range — a stored 0.5 meant a halving and
// would read as a barely audible half-decibel lift if the name were kept
// and the value left alone. Rewriting keeps every existing revision at
// the same audible level, which is the property this migration exists
// for.
//
// config_revisions is immutable under ADR-009 as an OPERATOR-facing rule:
// no API path edits a revision. A schema migration re-expressing a stored
// value in a new unit is the store's own forward-only change, and it
// preserves what the revision says rather than changing it.
//
// Conversion runs through [pkgaudio.GainToDb]/[pkgaudio.CeilingToDb],
// this project's single conversion, so a migrated value and a value the
// running coordinator computes can never disagree.
func migrateV19AudioSettingsGainToDb(ctx context.Context, tx *sql.Tx) error {
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
		rewritten, changed, err := v19RewriteAudioSettingsPayload(r.payload)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("rewrite audio.settings revision %d: %w", r.revision, err)
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
			return fmt.Errorf("write migrated audio.settings revision %d: %w", r.revision, err)
		}
	}
	return nil
}

// v19RewriteAudioSettingsPayload converts one stored payload. It reports
// changed=false, with no error, for a payload that carries neither old
// field name: a revision already written in the new unit, and a payload
// this migration has no business touching. Unrecognised members are
// carried through untouched — this migration owns two keys and nothing
// else.
func v19RewriteAudioSettingsPayload(raw string) (string, bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return "", false, fmt.Errorf("stored payload is not a JSON object: %w", err)
	}

	maxRaw, haveMax := top["defaultMaxBackgroundGain"]
	duckRaw, haveDuck := top["duckTargetGain"]
	if !haveMax && !haveDuck {
		return "", false, nil
	}

	if haveMax {
		var linear float64
		if err := json.Unmarshal(maxRaw, &linear); err != nil {
			return "", false, fmt.Errorf("defaultMaxBackgroundGain is not a number: %w", err)
		}
		db := pkgaudio.CeilingToDb(pkgaudio.Ceiling(linear))
		// The old bound allowed exactly 4.0, which is +12.04 dB and just
		// past the new +12 dB bound. Clamp rather than leave behind a
		// revision the post-change validator would refuse to read.
		if db > v19MaxBackgroundGainDbCeiling {
			db = v19MaxBackgroundGainDbCeiling
		}
		delete(top, "defaultMaxBackgroundGain")
		top["defaultMaxBackgroundGainDb"] = v19EncodeDb(db)
	}

	if haveDuck {
		var linear float64
		if err := json.Unmarshal(duckRaw, &linear); err != nil {
			return "", false, fmt.Errorf("duckTargetGain is not a number: %w", err)
		}
		db := pkgaudio.GainToDb(pkgaudio.Gain(linear))
		// The old bound was exclusive at 1.0, so every stored value is
		// already below unity; a value that somehow reached the store at
		// or above it is clamped just under 0 dB rather than left as a
		// revision that no longer reads back.
		if db >= v19DuckTargetGainDbCeiling {
			db = -0.01
		}
		delete(top, "duckTargetGain")
		top["duckTargetGainDb"] = v19EncodeDb(db)
	}

	out, err := json.Marshal(top)
	if err != nil {
		return "", false, fmt.Errorf("encode migrated payload: %w", err)
	}
	return string(out), true, nil
}

// v19EncodeDb renders a decibel value as JSON. Two decimal places: a
// hundredth of a decibel is far below audibility, and a full float64
// expansion would put a 17-digit number in front of an operator reading
// the revision back.
func v19EncodeDb(db float64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("%.2f", db))
}

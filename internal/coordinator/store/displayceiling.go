package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// display_brightness_ceiling holds the one brightness ceiling every FPP
// follows (ADR-062 decision 1) in one row, overwritten in place. It is live
// state, not configuration.

const displayCeilingRowID = "default"

// DisplayCeilingRecord is the desired display ceiling. FadeEndsAt is zero
// when no fade was asked for. ChangedBy is an operator principal id or the
// FPP instance id the ceiling was adopted from, as ChangedByKind says.
type DisplayCeilingRecord struct {
	TargetPercent int
	FadeEndsAt    time.Time
	ChangedAt     time.Time
	ChangedByKind string
	ChangedBy     string
	ChangedByName string
}

func getDisplayCeiling(ctx context.Context, q querier) (DisplayCeilingRecord, bool, error) {
	row := q.QueryRowContext(ctx, `
		SELECT target_percent, fade_ends_at, changed_at, changed_by_kind, changed_by, changed_by_name
		FROM display_brightness_ceiling WHERE id = ?
	`, displayCeilingRowID)
	var (
		rec       DisplayCeilingRecord
		fadeEnds  sql.NullString
		changedAt string
	)
	err := row.Scan(&rec.TargetPercent, &fadeEnds, &changedAt, &rec.ChangedByKind, &rec.ChangedBy, &rec.ChangedByName)
	switch {
	case err == sql.ErrNoRows:
		return DisplayCeilingRecord{}, false, nil
	case err != nil:
		return DisplayCeilingRecord{}, false, fmt.Errorf("store: get display ceiling: %w", err)
	}
	if rec.FadeEndsAt, err = dbToTimePtrValue(fadeEnds); err != nil {
		return DisplayCeilingRecord{}, false, fmt.Errorf("store: parse display ceiling fade_ends_at: %w", err)
	}
	if rec.ChangedAt, err = dbToTime(changedAt); err != nil {
		return DisplayCeilingRecord{}, false, fmt.Errorf("store: parse display ceiling changed_at: %w", err)
	}
	return rec, true, nil
}

// GetDisplayCeiling returns the stored display ceiling. ok is false until
// the first one is written.
func (s *Store) GetDisplayCeiling(ctx context.Context) (rec DisplayCeilingRecord, ok bool, err error) {
	guardNotInTx(ctx, "Store.GetDisplayCeiling")
	return getDisplayCeiling(ctx, s.db)
}

// SetDisplayCeiling overwrites the stored display ceiling.
func (s *Store) SetDisplayCeiling(ctx context.Context, rec DisplayCeilingRecord) error {
	guardNotInTx(ctx, "Store.SetDisplayCeiling")
	var fadeEnds sql.NullString
	if !rec.FadeEndsAt.IsZero() {
		fadeEnds = sql.NullString{String: timeToDB(rec.FadeEndsAt), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO display_brightness_ceiling (id, target_percent, fade_ends_at, changed_at, changed_by_kind, changed_by, changed_by_name)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			target_percent  = excluded.target_percent,
			fade_ends_at    = excluded.fade_ends_at,
			changed_at      = excluded.changed_at,
			changed_by_kind = excluded.changed_by_kind,
			changed_by      = excluded.changed_by,
			changed_by_name = excluded.changed_by_name
	`, displayCeilingRowID, rec.TargetPercent, fadeEnds, timeToDB(rec.ChangedAt), rec.ChangedByKind, rec.ChangedBy, rec.ChangedByName)
	if err != nil {
		return fmt.Errorf("store: set display ceiling: %w", err)
	}
	return nil
}

// schemaV50 adds display_brightness_ceiling. The CHECKs keep it to one row
// and a percent in range; a missing row means no ceiling has been set.
const schemaV50 = `
CREATE TABLE IF NOT EXISTS display_brightness_ceiling (
	id              TEXT PRIMARY KEY CHECK (id = 'default'),
	target_percent  INTEGER NOT NULL CHECK (target_percent BETWEEN 0 AND 100),
	fade_ends_at    TEXT,
	changed_at      TEXT NOT NULL,
	changed_by_kind TEXT NOT NULL DEFAULT '',
	changed_by      TEXT NOT NULL DEFAULT '',
	changed_by_name TEXT NOT NULL DEFAULT ''
);
`

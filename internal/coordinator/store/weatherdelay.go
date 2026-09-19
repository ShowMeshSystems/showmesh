package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// weather_delay_state holds the live weather delay state (ADR-053 decision
// 2) in one row, overwritten in place. It is live state, not configuration.

const weatherDelayStateRowID = "default"

// WeatherDelayStateRecord mirrors pkg/weatherdelay.State. StartedAt is zero
// and Kind and StartedBy are empty while Active is false.
type WeatherDelayStateRecord struct {
	Active    bool
	Kind      string
	StartedAt time.Time
	StartedBy string
	Revision  int64
}

func getWeatherDelayState(ctx context.Context, q querier) (WeatherDelayStateRecord, error) {
	row := q.QueryRowContext(ctx, `
		SELECT active, kind, started_at, started_by, revision
		FROM weather_delay_state WHERE id = ?
	`, weatherDelayStateRowID)

	var (
		rec       WeatherDelayStateRecord
		startedAt sql.NullString
	)
	err := row.Scan(&rec.Active, &rec.Kind, &startedAt, &rec.StartedBy, &rec.Revision)
	switch {
	case err == sql.ErrNoRows:
		return WeatherDelayStateRecord{}, nil
	case err != nil:
		return WeatherDelayStateRecord{}, fmt.Errorf("store: get weather delay state: %w", err)
	}
	if rec.StartedAt, err = dbToTimePtrValue(startedAt); err != nil {
		return WeatherDelayStateRecord{}, fmt.Errorf("store: parse weather delay state started_at: %w", err)
	}
	return rec, nil
}

// GetWeatherDelayState returns the stored state, or the zero (not active)
// record when nothing has been written.
func (s *Store) GetWeatherDelayState(ctx context.Context) (WeatherDelayStateRecord, error) {
	guardNotInTx(ctx, "Store.GetWeatherDelayState")
	return getWeatherDelayState(ctx, s.db)
}

// GetWeatherDelayState is [Store.GetWeatherDelayState]'s [Tx] form.
func (t *Tx) GetWeatherDelayState(ctx context.Context) (WeatherDelayStateRecord, error) {
	return getWeatherDelayState(ctx, t.tx)
}

// ErrWeatherDelayRevisionNotIncreasing reports a write whose Revision is not
// greater than the stored one; the stored row is left unchanged.
var ErrWeatherDelayRevisionNotIncreasing = errors.New("store: weather delay state revision must increase")

func setWeatherDelayState(ctx context.Context, q querier, rec WeatherDelayStateRecord) error {
	var startedAt sql.NullString
	if !rec.StartedAt.IsZero() {
		startedAt = sql.NullString{String: timeToDB(rec.StartedAt), Valid: true}
	}
	res, err := q.ExecContext(ctx, `
		INSERT INTO weather_delay_state (id, active, kind, started_at, started_by, revision)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			active     = excluded.active,
			kind       = excluded.kind,
			started_at = excluded.started_at,
			started_by = excluded.started_by,
			revision   = excluded.revision
		WHERE excluded.revision > weather_delay_state.revision
	`, weatherDelayStateRowID, rec.Active, rec.Kind, startedAt, rec.StartedBy, rec.Revision)
	if err != nil {
		return fmt.Errorf("store: set weather delay state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set weather delay state: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: revision %d", ErrWeatherDelayRevisionNotIncreasing, rec.Revision)
	}
	return nil
}

// SetWeatherDelayState overwrites the stored state. The caller supplies the
// next Revision; a Revision not above the stored one is refused with
// [ErrWeatherDelayRevisionNotIncreasing].
func (s *Store) SetWeatherDelayState(ctx context.Context, rec WeatherDelayStateRecord) error {
	guardNotInTx(ctx, "Store.SetWeatherDelayState")
	return setWeatherDelayState(ctx, s.db, rec)
}

// SetWeatherDelayState is [Store.SetWeatherDelayState]'s [Tx] form, so the
// state and its audit entry commit together.
func (t *Tx) SetWeatherDelayState(ctx context.Context, rec WeatherDelayStateRecord) error {
	return setWeatherDelayState(ctx, t.tx, rec)
}

// dbToTimePtrValue is [dbToTimePtr] with NULL read as the zero time.
func dbToTimePtrValue(s sql.NullString) (time.Time, error) {
	t, err := dbToTimePtr(s)
	if err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// schemaV41 adds weather_delay_state. The CHECK keeps it to one row; a
// missing row reads as not active.
const schemaV41 = `
CREATE TABLE IF NOT EXISTS weather_delay_state (
	id         TEXT PRIMARY KEY CHECK (id = 'default'),
	active     INTEGER NOT NULL DEFAULT 0,
	kind       TEXT NOT NULL DEFAULT '',
	started_at TEXT,
	started_by TEXT NOT NULL DEFAULT '',
	revision   INTEGER NOT NULL DEFAULT 0
);
`

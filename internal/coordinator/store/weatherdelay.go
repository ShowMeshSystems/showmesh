package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// This file holds schemaV41's weather_delay_state repository methods
// (ADR-053 decision 2: "A delay is a stored, system-wide state ... it
// survives a coordinator restart"). One fixed row, id "default", mirroring
// config_objects' own singleton-id convention — this is not a config
// kind (config_revisions is authored, immutable history; this is live,
// overwritten-in-place state, like night_sessions' own current row), so it
// gets its own table rather than riding config_revisions.payload_json.

// weatherDelayStateRowID is the one row this table ever holds.
const weatherDelayStateRowID = "default"

// WeatherDelayStateRecord is the persisted weather-delay state: the same
// fields pkg/weatherdelay.State carries, as this package's own scalar
// shadow type (mirroring FallbackProgramRecord's identical "no dependency
// on the shared wire package" posture). StartedAt is the zero time and
// Kind/StartedBy are empty while Active is false.
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

// GetWeatherDelayState returns the persisted weather-delay state, or the
// zero [WeatherDelayStateRecord] (not active) when nothing has ever been
// written, mirroring [config.ShowEmergencyStopDefaultPayload]'s identical
// "no row yet reads as the well-defined default" rule.
func (s *Store) GetWeatherDelayState(ctx context.Context) (WeatherDelayStateRecord, error) {
	guardNotInTx(ctx, "Store.GetWeatherDelayState")
	return getWeatherDelayState(ctx, s.db)
}

// GetWeatherDelayState is [Store.GetWeatherDelayState]'s [Tx] form.
func (t *Tx) GetWeatherDelayState(ctx context.Context) (WeatherDelayStateRecord, error) {
	return getWeatherDelayState(ctx, t.tx)
}

func setWeatherDelayState(ctx context.Context, q querier, rec WeatherDelayStateRecord) error {
	var startedAt sql.NullString
	if !rec.StartedAt.IsZero() {
		startedAt = sql.NullString{String: timeToDB(rec.StartedAt), Valid: true}
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO weather_delay_state (id, active, kind, started_at, started_by, revision)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			active     = excluded.active,
			kind       = excluded.kind,
			started_at = excluded.started_at,
			started_by = excluded.started_by,
			revision   = excluded.revision
	`, weatherDelayStateRowID, rec.Active, rec.Kind, startedAt, rec.StartedBy, rec.Revision); err != nil {
		return fmt.Errorf("store: set weather delay state: %w", err)
	}
	return nil
}

// SetWeatherDelayState overwrites the single stored weather-delay state
// row in place. The caller supplies Revision already incremented: this
// method performs no read-modify-write of its own, mirroring
// [Tx.ActivateConfigRevision]'s identical "the caller decided the next
// value, this just persists it" division of labor.
func (s *Store) SetWeatherDelayState(ctx context.Context, rec WeatherDelayStateRecord) error {
	guardNotInTx(ctx, "Store.SetWeatherDelayState")
	return setWeatherDelayState(ctx, s.db, rec)
}

// SetWeatherDelayState is [Store.SetWeatherDelayState]'s [Tx] form: the
// form ADR-053 decision 2's "the state is written before any stop is
// sent" requires, so a future caller can write the state and its own
// audit entry inside one transaction, [Tx.ActivateConfigRevision]'s
// identical shape.
func (t *Tx) SetWeatherDelayState(ctx context.Context, rec WeatherDelayStateRecord) error {
	return setWeatherDelayState(ctx, t.tx, rec)
}

// dbToTimePtrValue is [dbToTimePtr] dereferenced to time.Time's own zero
// value when absent, for a column this file treats as "zero means unset"
// rather than a nullable pointer field.
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

// schemaV41 adds weather_delay_state, a pure addition alongside
// night_sessions/night_cycle_outcomes/fallback_programs: no existing table
// is touched, and a coordinator that has never seen a weather delay simply
// has no row (getWeatherDelayState's own "not active" default).
const schemaV41 = `
CREATE TABLE IF NOT EXISTS weather_delay_state (
	id         TEXT PRIMARY KEY,
	active     INTEGER NOT NULL DEFAULT 0,
	kind       TEXT NOT NULL DEFAULT '',
	started_at TEXT,
	started_by TEXT NOT NULL DEFAULT '',
	revision   INTEGER NOT NULL DEFAULT 0
);
`

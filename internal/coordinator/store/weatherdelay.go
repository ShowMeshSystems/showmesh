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
// and the other fields empty while Active is false. StartedByName may be
// empty even while Active, for a row written before it existed.
type WeatherDelayStateRecord struct {
	Active        bool
	Kind          string
	StartedAt     time.Time
	StartedBy     string
	StartedByName string
	Revision      int64
}

func getWeatherDelayState(ctx context.Context, q querier) (WeatherDelayStateRecord, error) {
	row := q.QueryRowContext(ctx, `
		SELECT active, kind, started_at, started_by, started_by_name, revision
		FROM weather_delay_state WHERE id = ?
	`, weatherDelayStateRowID)

	var (
		rec       WeatherDelayStateRecord
		startedAt sql.NullString
	)
	err := row.Scan(&rec.Active, &rec.Kind, &startedAt, &rec.StartedBy, &rec.StartedByName, &rec.Revision)
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
		INSERT INTO weather_delay_state (id, active, kind, started_at, started_by, started_by_name, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			active          = excluded.active,
			kind            = excluded.kind,
			started_at      = excluded.started_at,
			started_by      = excluded.started_by,
			started_by_name = excluded.started_by_name,
			revision        = excluded.revision
		WHERE excluded.revision > weather_delay_state.revision
	`, weatherDelayStateRowID, rec.Active, rec.Kind, startedAt, rec.StartedBy, rec.StartedByName, rec.Revision)
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

// migrateV42AddWeatherDelayStateStartedByNameColumn adds
// weather_delay_state.started_by_name, skipping it when the column exists
// so a rerun never fails with "duplicate column name".
func migrateV42AddWeatherDelayStateStartedByNameColumn(ctx context.Context, tx *sql.Tx) error {
	var hasColumn int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('weather_delay_state') WHERE name = 'started_by_name'`,
	).Scan(&hasColumn); err != nil {
		return fmt.Errorf("check weather_delay_state.started_by_name exists: %w", err)
	}
	if hasColumn > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE weather_delay_state ADD COLUMN started_by_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add weather_delay_state.started_by_name: %w", err)
	}
	return nil
}

// weather_delay_pending_decision holds ADR-053 decision 12's one pending
// automatic-trigger question (one row, overwritten in place), and
// weather_delay_trigger_suppression holds one row per source recording the
// warning a dismiss last suppressed and until when. Both are live state,
// like weather_delay_state, not configuration.

const weatherDelayPendingDecisionRowID = "default"

// PendingWeatherDelayDecisionRecord is the one outstanding trigger question,
// or the zero value when none is pending.
type PendingWeatherDelayDecisionRecord struct {
	ID            string
	Source        string
	Reason        string
	Question      string
	DefaultAction string
	AskedAt       time.Time
	Deadline      time.Time
	// ExpiresAt is the warning's own expiry as its source reported it,
	// zero when the source reported none. It is carried so a late
	// deadline can be judged against it; nothing acts on it yet.
	ExpiresAt time.Time
	// WarningKey identifies "the same warning" this decision was raised
	// about (see weathertrigger.WarningKey), so a dismiss answer can
	// suppress it without this package needing the trigger's own fields.
	WarningKey string
}

func getPendingWeatherDelayDecision(ctx context.Context, q querier) (PendingWeatherDelayDecisionRecord, bool, error) {
	row := q.QueryRowContext(ctx, `
		SELECT id, source, reason, question, default_action, asked_at, deadline, expires_at, warning_key
		FROM weather_delay_pending_decision WHERE row_id = ?
	`, weatherDelayPendingDecisionRowID)

	var (
		rec                          PendingWeatherDelayDecisionRecord
		askedAt, deadline, expiresAt sql.NullString
	)
	err := row.Scan(&rec.ID, &rec.Source, &rec.Reason, &rec.Question, &rec.DefaultAction, &askedAt, &deadline, &expiresAt, &rec.WarningKey)
	switch {
	case err == sql.ErrNoRows:
		return PendingWeatherDelayDecisionRecord{}, false, nil
	case err != nil:
		return PendingWeatherDelayDecisionRecord{}, false, fmt.Errorf("store: get pending weather delay decision: %w", err)
	}
	if rec.ID == "" {
		return PendingWeatherDelayDecisionRecord{}, false, nil
	}
	if rec.AskedAt, err = dbToTimePtrValue(askedAt); err != nil {
		return PendingWeatherDelayDecisionRecord{}, false, fmt.Errorf("store: parse pending weather delay decision asked_at: %w", err)
	}
	if rec.Deadline, err = dbToTimePtrValue(deadline); err != nil {
		return PendingWeatherDelayDecisionRecord{}, false, fmt.Errorf("store: parse pending weather delay decision deadline: %w", err)
	}
	if rec.ExpiresAt, err = dbToTimePtrValue(expiresAt); err != nil {
		return PendingWeatherDelayDecisionRecord{}, false, fmt.Errorf("store: parse pending weather delay decision expires_at: %w", err)
	}
	return rec, true, nil
}

// GetPendingWeatherDelayDecision returns the pending decision, false when
// none is pending.
func (s *Store) GetPendingWeatherDelayDecision(ctx context.Context) (PendingWeatherDelayDecisionRecord, bool, error) {
	guardNotInTx(ctx, "Store.GetPendingWeatherDelayDecision")
	return getPendingWeatherDelayDecision(ctx, s.db)
}

// GetPendingWeatherDelayDecision is [Store.GetPendingWeatherDelayDecision]'s [Tx] form.
func (t *Tx) GetPendingWeatherDelayDecision(ctx context.Context) (PendingWeatherDelayDecisionRecord, bool, error) {
	return getPendingWeatherDelayDecision(ctx, t.tx)
}

// setPendingWeatherDelayDecision inserts rec only when no decision is
// pending. It never overwrites one: two sources reporting at the same
// moment must produce one question, not a second that silently replaces
// the first and resets its deadline.
func setPendingWeatherDelayDecision(ctx context.Context, q querier, rec PendingWeatherDelayDecisionRecord) (bool, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO weather_delay_pending_decision (row_id, id, source, reason, question, default_action, asked_at, deadline, expires_at, warning_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(row_id) DO NOTHING
	`, weatherDelayPendingDecisionRowID, rec.ID, rec.Source, rec.Reason, rec.Question, rec.DefaultAction,
		timeToDB(rec.AskedAt), timeToDB(rec.Deadline), timeToDB(rec.ExpiresAt), rec.WarningKey)
	if err != nil {
		return false, fmt.Errorf("store: set pending weather delay decision: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: set pending weather delay decision: %w", err)
	}
	return n > 0, nil
}

// SetPendingWeatherDelayDecision stores rec as the pending decision and
// reports whether it won: false means one was already pending and rec was
// not stored. Use [Store.ClearPendingWeatherDelayDecision] to clear one.
func (s *Store) SetPendingWeatherDelayDecision(ctx context.Context, rec PendingWeatherDelayDecisionRecord) (bool, error) {
	guardNotInTx(ctx, "Store.SetPendingWeatherDelayDecision")
	return setPendingWeatherDelayDecision(ctx, s.db, rec)
}

// SetPendingWeatherDelayDecision is [Store.SetPendingWeatherDelayDecision]'s [Tx] form.
func (t *Tx) SetPendingWeatherDelayDecision(ctx context.Context, rec PendingWeatherDelayDecisionRecord) (bool, error) {
	return setPendingWeatherDelayDecision(ctx, t.tx, rec)
}

// clearPendingWeatherDelayDecision deletes the pending decision only when
// it still carries id, and reports whether this caller is the one that
// removed it. That is what makes an answer and its own deadline race
// safely: exactly one of them claims the decision and runs its action.
func clearPendingWeatherDelayDecision(ctx context.Context, q querier, id string) (bool, error) {
	res, err := q.ExecContext(ctx,
		`DELETE FROM weather_delay_pending_decision WHERE row_id = ? AND id = ?`,
		weatherDelayPendingDecisionRowID, id)
	if err != nil {
		return false, fmt.Errorf("store: clear pending weather delay decision: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: clear pending weather delay decision: %w", err)
	}
	return n > 0, nil
}

// ClearPendingWeatherDelayDecision removes the pending decision carrying
// id and reports whether it was this call that removed it.
func (s *Store) ClearPendingWeatherDelayDecision(ctx context.Context, id string) (bool, error) {
	guardNotInTx(ctx, "Store.ClearPendingWeatherDelayDecision")
	return clearPendingWeatherDelayDecision(ctx, s.db, id)
}

// ClearPendingWeatherDelayDecision is [Store.ClearPendingWeatherDelayDecision]'s [Tx] form.
func (t *Tx) ClearPendingWeatherDelayDecision(ctx context.Context, id string) (bool, error) {
	return clearPendingWeatherDelayDecision(ctx, t.tx, id)
}

// WeatherDelayTriggerSuppressionRecord is one source's dismissed-warning
// quiet period: a new question about WarningKey from Source is suppressed
// until Until.
type WeatherDelayTriggerSuppressionRecord struct {
	Source     string
	WarningKey string
	Until      time.Time
}

// GetWeatherDelayTriggerSuppression returns source's suppression row, false
// when none is stored.
func (s *Store) GetWeatherDelayTriggerSuppression(ctx context.Context, source string) (WeatherDelayTriggerSuppressionRecord, bool, error) {
	guardNotInTx(ctx, "Store.GetWeatherDelayTriggerSuppression")
	row := s.db.QueryRowContext(ctx, `
		SELECT source, warning_key, until FROM weather_delay_trigger_suppression WHERE source = ?
	`, source)
	var (
		rec   WeatherDelayTriggerSuppressionRecord
		until sql.NullString
	)
	err := row.Scan(&rec.Source, &rec.WarningKey, &until)
	switch {
	case err == sql.ErrNoRows:
		return WeatherDelayTriggerSuppressionRecord{}, false, nil
	case err != nil:
		return WeatherDelayTriggerSuppressionRecord{}, false, fmt.Errorf("store: get weather delay trigger suppression: %w", err)
	}
	if rec.Until, err = dbToTimePtrValue(until); err != nil {
		return WeatherDelayTriggerSuppressionRecord{}, false, fmt.Errorf("store: parse weather delay trigger suppression until: %w", err)
	}
	return rec, true, nil
}

// SetWeatherDelayTriggerSuppression replaces source's suppression row.
func (s *Store) SetWeatherDelayTriggerSuppression(ctx context.Context, rec WeatherDelayTriggerSuppressionRecord) error {
	guardNotInTx(ctx, "Store.SetWeatherDelayTriggerSuppression")
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO weather_delay_trigger_suppression (source, warning_key, until)
		VALUES (?, ?, ?)
		ON CONFLICT(source) DO UPDATE SET
			warning_key = excluded.warning_key,
			until       = excluded.until
	`, rec.Source, rec.WarningKey, timeToDB(rec.Until))
	if err != nil {
		return fmt.Errorf("store: set weather delay trigger suppression: %w", err)
	}
	return nil
}

// schemaV43 adds weather_delay_pending_decision (one row, deleted when no
// question is pending) and weather_delay_trigger_suppression (one row per
// source). Neither is configuration: both are live trigger state (ADR-053
// decision 12), so a coordinator restart does not lose a deadline or a
// dismiss's quiet period.
const schemaV43 = `
CREATE TABLE IF NOT EXISTS weather_delay_pending_decision (
	row_id         TEXT PRIMARY KEY CHECK (row_id = 'default'),
	id             TEXT NOT NULL DEFAULT '',
	source         TEXT NOT NULL DEFAULT '',
	reason         TEXT NOT NULL DEFAULT '',
	question       TEXT NOT NULL DEFAULT '',
	default_action TEXT NOT NULL DEFAULT '',
	asked_at       TEXT,
	deadline       TEXT,
	expires_at     TEXT,
	warning_key    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS weather_delay_trigger_suppression (
	source      TEXT PRIMARY KEY,
	warning_key TEXT NOT NULL DEFAULT '',
	until       TEXT
);
`

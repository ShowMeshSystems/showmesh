package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// night_sessions / night_readiness_results / night_cue_outbox repository
// methods (schemaV10, Track F seam F2). Store and Tx forms share one SQL
// text each via the querier interface (tx.go), matching commands.go's
// pattern next door.

// NightSessionRecord is one row of night_sessions. Every string field
// other than ID/State is "" to mean "not set"; a session's own ID also
// serves as its preparation epoch identity for its whole lifetime.
type NightSessionRecord struct {
	ID                        string
	ConfigObjectID            string
	ConfigRevision            int64
	State                     string
	StateEnteredAt            time.Time
	ReadinessID               string
	FinalShowRequested        bool
	FinalShowRequestedAt      *time.Time
	AdmissionClosed           bool
	AdmissionClosedAt         *time.Time
	ShutdownIntent            string
	Cycle                     int64
	ContentAnchorJSON         string
	BoundaryJSON              string
	ArmedShowID               string
	ShowCommitted             bool
	PowerPhase                string
	Degraded                  bool
	DegradedReason            string
	PrepareSiteIdempotencyKey string
	AttributionDegraded       bool
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// ErrNightSessionNotFound is returned when no row matches.
var ErrNightSessionNotFound = errors.New("store: night session not found")

const nightSessionColumns = `
	id, config_object_id, config_revision, state, state_entered_at, readiness_id,
	final_show_requested, final_show_requested_at, admission_closed, admission_closed_at,
	shutdown_intent, cycle, content_anchor_json, boundary_json, armed_show_id,
	show_committed, power_phase, degraded, degraded_reason, prepare_site_idempotency_key,
	attribution_degraded, created_at, updated_at
`

func scanNightSession(row interface{ Scan(dest ...any) error }) (NightSessionRecord, error) {
	var (
		rec                                           NightSessionRecord
		stateEnteredAt, createdAt, updatedAt          string
		finalShowRequestedAt, admissionClosedAt       sql.NullString
		finalShowRequested, admissionClosed, degraded int64
		showCommitted, attributionDegraded            int64
	)
	if err := row.Scan(
		&rec.ID, &rec.ConfigObjectID, &rec.ConfigRevision, &rec.State, &stateEnteredAt, &rec.ReadinessID,
		&finalShowRequested, &finalShowRequestedAt, &admissionClosed, &admissionClosedAt,
		&rec.ShutdownIntent, &rec.Cycle, &rec.ContentAnchorJSON, &rec.BoundaryJSON, &rec.ArmedShowID,
		&showCommitted, &rec.PowerPhase, &degraded, &rec.DegradedReason, &rec.PrepareSiteIdempotencyKey,
		&attributionDegraded, &createdAt, &updatedAt,
	); err != nil {
		return NightSessionRecord{}, err
	}
	rec.FinalShowRequested = finalShowRequested != 0
	rec.AdmissionClosed = admissionClosed != 0
	rec.ShowCommitted = showCommitted != 0
	rec.Degraded = degraded != 0
	rec.AttributionDegraded = attributionDegraded != 0

	var err error
	if rec.StateEnteredAt, err = dbToTime(stateEnteredAt); err != nil {
		return NightSessionRecord{}, fmt.Errorf("store: parse night session state_entered_at: %w", err)
	}
	if rec.FinalShowRequestedAt, err = dbToTimePtr(finalShowRequestedAt); err != nil {
		return NightSessionRecord{}, fmt.Errorf("store: parse night session final_show_requested_at: %w", err)
	}
	if rec.AdmissionClosedAt, err = dbToTimePtr(admissionClosedAt); err != nil {
		return NightSessionRecord{}, fmt.Errorf("store: parse night session admission_closed_at: %w", err)
	}
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return NightSessionRecord{}, fmt.Errorf("store: parse night session created_at: %w", err)
	}
	if rec.UpdatedAt, err = dbToTime(updatedAt); err != nil {
		return NightSessionRecord{}, fmt.Errorf("store: parse night session updated_at: %w", err)
	}
	return rec, nil
}

func insertNightSession(ctx context.Context, q querier, rec NightSessionRecord, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO night_sessions (`+nightSessionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rec.ID, rec.ConfigObjectID, rec.ConfigRevision, rec.State, timeToDB(rec.StateEnteredAt), rec.ReadinessID,
		boolToDB(rec.FinalShowRequested), timePtrToDB(rec.FinalShowRequestedAt), boolToDB(rec.AdmissionClosed), timePtrToDB(rec.AdmissionClosedAt),
		rec.ShutdownIntent, rec.Cycle, rec.ContentAnchorJSON, rec.BoundaryJSON, rec.ArmedShowID,
		boolToDB(rec.ShowCommitted), rec.PowerPhase, boolToDB(rec.Degraded), rec.DegradedReason, rec.PrepareSiteIdempotencyKey,
		boolToDB(rec.AttributionDegraded), timeToDB(now), timeToDB(now),
	)
	if err != nil {
		return fmt.Errorf("store: insert night session %q: %w", rec.ID, err)
	}
	return nil
}

// CreateNightSession inserts a brand-new session row (rec.ID is also its
// preparation epoch's own identity).
func (s *Store) CreateNightSession(ctx context.Context, rec NightSessionRecord, now time.Time) error {
	guardNotInTx(ctx, "Store.CreateNightSession")
	return insertNightSession(ctx, s.db, rec, now)
}

// CreateNightSession is [Store.CreateNightSession]'s [Tx] form — the
// read-decide-write race fix requires the read, the decision, and this
// write to share one transaction.
func (t *Tx) CreateNightSession(ctx context.Context, rec NightSessionRecord, now time.Time) error {
	return insertNightSession(ctx, t.tx, rec, now)
}

func getCurrentNightSession(ctx context.Context, q querier) (NightSessionRecord, bool, error) {
	// ORDER BY rowid, never id: two sessions created within the same
	// clock tick tie on created_at, and id is a caller-minted UUID with no
	// relationship to insertion order; rowid alone always increases.
	row := q.QueryRowContext(ctx, `SELECT `+nightSessionColumns+` FROM night_sessions ORDER BY created_at DESC, rowid DESC LIMIT 1`)
	rec, err := scanNightSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NightSessionRecord{}, false, nil
	}
	if err != nil {
		return NightSessionRecord{}, false, fmt.Errorf("store: get current night session: %w", err)
	}
	return rec, true, nil
}

// GetCurrentNightSession returns the most recently created session row,
// and false if no session has ever been created.
func (s *Store) GetCurrentNightSession(ctx context.Context) (NightSessionRecord, bool, error) {
	guardNotInTx(ctx, "Store.GetCurrentNightSession")
	return getCurrentNightSession(ctx, s.db)
}

// GetCurrentNightSession is [Store.GetCurrentNightSession]'s [Tx] form.
func (t *Tx) GetCurrentNightSession(ctx context.Context) (NightSessionRecord, bool, error) {
	return getCurrentNightSession(ctx, t.tx)
}

func getNightSessionByIdempotencyKey(ctx context.Context, q querier, key string) (NightSessionRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT `+nightSessionColumns+` FROM night_sessions WHERE prepare_site_idempotency_key = ?`, key)
	rec, err := scanNightSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NightSessionRecord{}, ErrNightSessionNotFound
	}
	if err != nil {
		return NightSessionRecord{}, fmt.Errorf("store: get night session by idempotency key: %w", err)
	}
	return rec, nil
}

// GetNightSessionByIdempotencyKey returns the session prepare-site created
// under key, or [ErrNightSessionNotFound].
func (s *Store) GetNightSessionByIdempotencyKey(ctx context.Context, key string) (NightSessionRecord, error) {
	guardNotInTx(ctx, "Store.GetNightSessionByIdempotencyKey")
	return getNightSessionByIdempotencyKey(ctx, s.db, key)
}

// GetNightSessionByIdempotencyKey is [Store.GetNightSessionByIdempotencyKey]'s [Tx] form.
func (t *Tx) GetNightSessionByIdempotencyKey(ctx context.Context, key string) (NightSessionRecord, error) {
	return getNightSessionByIdempotencyKey(ctx, t.tx, key)
}

func getNightSession(ctx context.Context, q querier, id string) (NightSessionRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT `+nightSessionColumns+` FROM night_sessions WHERE id = ?`, id)
	rec, err := scanNightSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NightSessionRecord{}, ErrNightSessionNotFound
	}
	if err != nil {
		return NightSessionRecord{}, fmt.Errorf("store: get night session %q: %w", id, err)
	}
	return rec, nil
}

// GetNightSession returns the session row with the given id, or
// [ErrNightSessionNotFound].
func (s *Store) GetNightSession(ctx context.Context, id string) (NightSessionRecord, error) {
	guardNotInTx(ctx, "Store.GetNightSession")
	return getNightSession(ctx, s.db, id)
}

func updateNightSession(ctx context.Context, q querier, rec NightSessionRecord, now time.Time) error {
	res, err := q.ExecContext(ctx, `
		UPDATE night_sessions SET
			config_object_id = ?, config_revision = ?, state = ?, state_entered_at = ?, readiness_id = ?,
			final_show_requested = ?, final_show_requested_at = ?, admission_closed = ?, admission_closed_at = ?,
			shutdown_intent = ?, cycle = ?, content_anchor_json = ?, boundary_json = ?, armed_show_id = ?,
			show_committed = ?, power_phase = ?, degraded = ?, degraded_reason = ?,
			prepare_site_idempotency_key = ?, attribution_degraded = ?, updated_at = ?
		WHERE id = ?
	`,
		rec.ConfigObjectID, rec.ConfigRevision, rec.State, timeToDB(rec.StateEnteredAt), rec.ReadinessID,
		boolToDB(rec.FinalShowRequested), timePtrToDB(rec.FinalShowRequestedAt), boolToDB(rec.AdmissionClosed), timePtrToDB(rec.AdmissionClosedAt),
		rec.ShutdownIntent, rec.Cycle, rec.ContentAnchorJSON, rec.BoundaryJSON, rec.ArmedShowID,
		boolToDB(rec.ShowCommitted), rec.PowerPhase, boolToDB(rec.Degraded), rec.DegradedReason,
		rec.PrepareSiteIdempotencyKey, boolToDB(rec.AttributionDegraded), timeToDB(now),
		rec.ID,
	)
	if err != nil {
		return fmt.Errorf("store: update night session %q: %w", rec.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update night session %q: rows affected: %w", rec.ID, err)
	}
	if n == 0 {
		return ErrNightSessionNotFound
	}
	return nil
}

// UpdateNightSession overwrites every mutable column of the row rec.ID
// names with rec's own values (full-replace: the caller always has the
// complete, just-read record in hand).
func (s *Store) UpdateNightSession(ctx context.Context, rec NightSessionRecord, now time.Time) error {
	guardNotInTx(ctx, "Store.UpdateNightSession")
	return updateNightSession(ctx, s.db, rec, now)
}

// UpdateNightSession is [Store.UpdateNightSession]'s [Tx] form.
func (t *Tx) UpdateNightSession(ctx context.Context, rec NightSessionRecord, now time.Time) error {
	return updateNightSession(ctx, t.tx, rec, now)
}

// NightReadinessRecord is one row of night_readiness_results — never
// updated once inserted; a rerun of run-readiness inserts a new row.
type NightReadinessRecord struct {
	ID          string
	SessionID   string
	EpochID     string
	CompletedAt time.Time
	Outcome     string
	ChecksJSON  string
}

// ErrNightReadinessNotFound is returned when sessionID has no readiness
// result at all — distinct from a query error, which is returned as-is
// (never collapsed into this sentinel).
var ErrNightReadinessNotFound = errors.New("store: night readiness result not found")

func createNightReadiness(ctx context.Context, q querier, rec NightReadinessRecord) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO night_readiness_results (id, session_id, epoch_id, completed_at, outcome, checks_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, rec.ID, rec.SessionID, rec.EpochID, timeToDB(rec.CompletedAt), rec.Outcome, rec.ChecksJSON)
	if err != nil {
		return fmt.Errorf("store: insert night readiness result %q: %w", rec.ID, err)
	}
	return nil
}

func (s *Store) CreateNightReadiness(ctx context.Context, rec NightReadinessRecord) error {
	guardNotInTx(ctx, "Store.CreateNightReadiness")
	return createNightReadiness(ctx, s.db, rec)
}

// CreateNightReadiness is [Store.CreateNightReadiness]'s [Tx] form.
func (t *Tx) CreateNightReadiness(ctx context.Context, rec NightReadinessRecord) error {
	return createNightReadiness(ctx, t.tx, rec)
}

func getLatestNightReadiness(ctx context.Context, q querier, sessionID string) (NightReadinessRecord, error) {
	row := q.QueryRowContext(ctx, `
		SELECT id, session_id, epoch_id, completed_at, outcome, checks_json
		FROM night_readiness_results WHERE session_id = ? ORDER BY completed_at DESC, rowid DESC LIMIT 1
	`, sessionID)
	var rec NightReadinessRecord
	var completedAt string
	err := row.Scan(&rec.ID, &rec.SessionID, &rec.EpochID, &completedAt, &rec.Outcome, &rec.ChecksJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return NightReadinessRecord{}, ErrNightReadinessNotFound
	}
	if err != nil {
		return NightReadinessRecord{}, fmt.Errorf("store: get latest night readiness for session %q: %w", sessionID, err)
	}
	if rec.CompletedAt, err = dbToTime(completedAt); err != nil {
		return NightReadinessRecord{}, fmt.Errorf("store: parse night readiness completed_at: %w", err)
	}
	return rec, nil
}

// GetLatestNightReadiness returns the most recently completed readiness
// result for sessionID, or [ErrNightReadinessNotFound].
func (s *Store) GetLatestNightReadiness(ctx context.Context, sessionID string) (NightReadinessRecord, error) {
	guardNotInTx(ctx, "Store.GetLatestNightReadiness")
	return getLatestNightReadiness(ctx, s.db, sessionID)
}

// GetLatestNightReadiness is [Store.GetLatestNightReadiness]'s [Tx] form.
func (t *Tx) GetLatestNightReadiness(ctx context.Context, sessionID string) (NightReadinessRecord, error) {
	return getLatestNightReadiness(ctx, t.tx, sessionID)
}

// NightCueOutboxRecord is one row of night_cue_outbox — Track F seam F4's
// own table, created by this migration only. Phase (enterShow/enterResting)
// is part of the row's own identity alongside session/cycle/cue name: the
// two lists are separately validated and may legitimately share a cue
// name, and the outbox must never let one resolve the other's row.
type NightCueOutboxRecord struct {
	ID             string
	SessionID      string
	Cycle          int64
	Phase          string
	CueName        string
	ActionRevision int64
	State          string
	DispatchedAt   *time.Time
	ResolvedAt     *time.Time
	Outcome        string
	OutcomeReason  string
}

// ErrNightCueOutboxDuplicate is returned when (session, cycle, phase, cue)
// is reused — enforced by night_cue_outbox's own UNIQUE index.
var ErrNightCueOutboxDuplicate = errors.New("store: a cue outbox row with this session/cycle/phase/cue identity already exists")

// ErrNightCueOutboxNotFound is returned when no row matches.
var ErrNightCueOutboxNotFound = errors.New("store: night cue outbox row not found")

func insertNightCueOutboxRow(ctx context.Context, q querier, rec NightCueOutboxRecord, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO night_cue_outbox (id, session_id, cycle, phase, cue_name, action_revision, state, dispatched_at, resolved_at, outcome, outcome_reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.ID, rec.SessionID, rec.Cycle, rec.Phase, rec.CueName, rec.ActionRevision, rec.State,
		timePtrToDB(rec.DispatchedAt), timePtrToDB(rec.ResolvedAt), rec.Outcome, rec.OutcomeReason, timeToDB(now))
	if err != nil {
		if isUniqueConstraintErr(err) {
			return ErrNightCueOutboxDuplicate
		}
		return fmt.Errorf("store: insert night cue outbox row %q: %w", rec.ID, err)
	}
	return nil
}

// InsertNightCueOutboxRow is [Store]'s own-transaction form — used where
// the row's own identity is all that must be durable (no sibling write to
// commit alongside it).
func (s *Store) InsertNightCueOutboxRow(ctx context.Context, rec NightCueOutboxRecord, now time.Time) error {
	guardNotInTx(ctx, "Store.InsertNightCueOutboxRow")
	return insertNightCueOutboxRow(ctx, s.db, rec, now)
}

// InsertNightCueOutboxRow is [Store.InsertNightCueOutboxRow]'s [Tx] form —
// RESTING-MODE.md §7.1.1's own atomic commit needs this: the first
// outward-facing cue's outbox row and the session's own show_committed
// flag are written in the SAME transaction, before either is ever acted
// on, so a caller (api.nightCommitFirstCue) can compose the two.
func (t *Tx) InsertNightCueOutboxRow(ctx context.Context, rec NightCueOutboxRecord, now time.Time) error {
	return insertNightCueOutboxRow(ctx, t.tx, rec, now)
}

func scanNightCueOutboxRow(row interface{ Scan(dest ...any) error }) (NightCueOutboxRecord, error) {
	var (
		rec                      NightCueOutboxRecord
		dispatchedAt, resolvedAt sql.NullString
		createdAt                string
	)
	if err := row.Scan(&rec.ID, &rec.SessionID, &rec.Cycle, &rec.Phase, &rec.CueName, &rec.ActionRevision, &rec.State,
		&dispatchedAt, &resolvedAt, &rec.Outcome, &rec.OutcomeReason, &createdAt); err != nil {
		return NightCueOutboxRecord{}, err
	}
	var err error
	if rec.DispatchedAt, err = dbToTimePtr(dispatchedAt); err != nil {
		return NightCueOutboxRecord{}, fmt.Errorf("store: parse night cue outbox dispatched_at: %w", err)
	}
	if rec.ResolvedAt, err = dbToTimePtr(resolvedAt); err != nil {
		return NightCueOutboxRecord{}, fmt.Errorf("store: parse night cue outbox resolved_at: %w", err)
	}
	_ = createdAt
	return rec, nil
}

const nightCueOutboxColumns = `id, session_id, cycle, phase, cue_name, action_revision, state, dispatched_at, resolved_at, outcome, outcome_reason, created_at`

func getNightCueOutboxRow(ctx context.Context, q querier, sessionID string, cycle int64, phase, cueName string) (NightCueOutboxRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT `+nightCueOutboxColumns+` FROM night_cue_outbox WHERE session_id = ? AND cycle = ? AND phase = ? AND cue_name = ?`, sessionID, cycle, phase, cueName)
	rec, err := scanNightCueOutboxRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NightCueOutboxRecord{}, ErrNightCueOutboxNotFound
	}
	if err != nil {
		return NightCueOutboxRecord{}, fmt.Errorf("store: get night cue outbox row %s/%d/%s/%s: %w", sessionID, cycle, phase, cueName, err)
	}
	return rec, nil
}

// GetNightCueOutboxRow returns the outbox row for (sessionID, cycle,
// phase, cueName), or [ErrNightCueOutboxNotFound].
func (s *Store) GetNightCueOutboxRow(ctx context.Context, sessionID string, cycle int64, phase, cueName string) (NightCueOutboxRecord, error) {
	guardNotInTx(ctx, "Store.GetNightCueOutboxRow")
	return getNightCueOutboxRow(ctx, s.db, sessionID, cycle, phase, cueName)
}

// GetNightCueOutboxRow is [Store.GetNightCueOutboxRow]'s [Tx] form.
func (t *Tx) GetNightCueOutboxRow(ctx context.Context, sessionID string, cycle int64, phase, cueName string) (NightCueOutboxRecord, error) {
	return getNightCueOutboxRow(ctx, t.tx, sessionID, cycle, phase, cueName)
}

func listNightCueOutboxRows(ctx context.Context, q querier, sessionID string, cycle int64) ([]NightCueOutboxRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+nightCueOutboxColumns+` FROM night_cue_outbox WHERE session_id = ? AND cycle = ? ORDER BY rowid ASC`, sessionID, cycle)
	if err != nil {
		return nil, fmt.Errorf("store: list night cue outbox rows %s/%d: %w", sessionID, cycle, err)
	}
	defer func() { _ = rows.Close() }()
	var out []NightCueOutboxRecord
	for rows.Next() {
		rec, err := scanNightCueOutboxRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan night cue outbox row: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list night cue outbox rows %s/%d: %w", sessionID, cycle, err)
	}
	return out, nil
}

// ListNightCueOutboxRows returns every outbox row for (sessionID, cycle),
// in insertion order — recovery's own read (api.nightRecoverCueOutbox).
func (s *Store) ListNightCueOutboxRows(ctx context.Context, sessionID string, cycle int64) ([]NightCueOutboxRecord, error) {
	guardNotInTx(ctx, "Store.ListNightCueOutboxRows")
	return listNightCueOutboxRows(ctx, s.db, sessionID, cycle)
}

func updateNightCueOutboxRow(ctx context.Context, q querier, rec NightCueOutboxRecord) error {
	res, err := q.ExecContext(ctx, `
		UPDATE night_cue_outbox SET state = ?, dispatched_at = ?, resolved_at = ?, outcome = ?, outcome_reason = ?
		WHERE session_id = ? AND cycle = ? AND phase = ? AND cue_name = ?
	`, rec.State, timePtrToDB(rec.DispatchedAt), timePtrToDB(rec.ResolvedAt), rec.Outcome, rec.OutcomeReason,
		rec.SessionID, rec.Cycle, rec.Phase, rec.CueName)
	if err != nil {
		return fmt.Errorf("store: update night cue outbox row %s/%d/%s/%s: %w", rec.SessionID, rec.Cycle, rec.Phase, rec.CueName, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update night cue outbox row %s/%d/%s/%s: rows affected: %w", rec.SessionID, rec.Cycle, rec.Phase, rec.CueName, err)
	}
	if n == 0 {
		return ErrNightCueOutboxNotFound
	}
	return nil
}

// UpdateNightCueOutboxRow overwrites the mutable columns (state,
// dispatched_at, resolved_at, outcome, outcome_reason) of the row named by
// rec's own identity (session_id, cycle, phase, cue_name), which never
// changes.
func (s *Store) UpdateNightCueOutboxRow(ctx context.Context, rec NightCueOutboxRecord) error {
	guardNotInTx(ctx, "Store.UpdateNightCueOutboxRow")
	return updateNightCueOutboxRow(ctx, s.db, rec)
}

// UpdateNightCueOutboxRow is [Store.UpdateNightCueOutboxRow]'s [Tx] form.
func (t *Tx) UpdateNightCueOutboxRow(ctx context.Context, rec NightCueOutboxRecord) error {
	return updateNightCueOutboxRow(ctx, t.tx, rec)
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV40's night_cycle_outcomes repository methods: one
// row per (session, cycle) recording how that cycle's show ended, so a
// finished cycle can be reported instead of the placeholder "not reported"
// the operator UI showed before this table existed. A cycle that ran before
// this migration simply has no row; ListNightCycleOutcomes reports only
// what was recorded.

// Night cycle outcome values. "unknown" is a legitimate, intentional member
// of this vocabulary, not an error: the night loop's own evidence sometimes
// cannot say why a cycle ended, and reporting a guessed cause would be worse
// than reporting that none is available.
const (
	NightCycleOutcomeCompleted   = "completed"
	NightCycleOutcomeStopped     = "stopped"
	NightCycleOutcomeInterrupted = "interrupted"
	NightCycleOutcomeUnknown     = "unknown"
)

// NightCycleOutcomeRecord is one row: sessionID's cycle number, when its
// show started, and, once closed, when it ended and why. EndedAt is nil
// while the cycle is still open (its show is presumed still live); Outcome
// and Reason are only meaningful once EndedAt is set.
type NightCycleOutcomeRecord struct {
	SessionID     string
	Cycle         int64
	ShowStartedAt time.Time
	EndedAt       *time.Time
	Outcome       string
	Reason        string
}

// ErrNightCycleOutcomeNotFound is returned by
// [Store.CloseNightCycleOutcome]/[Tx.CloseNightCycleOutcome] when no open
// row exists for the given session and cycle to close.
var ErrNightCycleOutcomeNotFound = errors.New("store: night cycle outcome not found or already closed")

func openNightCycleOutcome(ctx context.Context, q querier, sessionID string, cycle int64, startedAt time.Time) error {
	if sessionID == "" {
		return fmt.Errorf("store: open night cycle outcome: sessionID is empty")
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO night_cycle_outcomes (session_id, cycle, show_started_at)
		VALUES (?, ?, ?)
		ON CONFLICT(session_id, cycle) DO NOTHING
	`, sessionID, cycle, timeToDB(startedAt)); err != nil {
		return fmt.Errorf("store: open night cycle outcome %s/%d: %w", sessionID, cycle, err)
	}
	return nil
}

// OpenNightCycleOutcome records that sessionID's cycle began playing its
// show at startedAt. A second call for the same (sessionID, cycle) is a
// no-op: the row already exists and is never overwritten by this method.
func (s *Store) OpenNightCycleOutcome(ctx context.Context, sessionID string, cycle int64, startedAt time.Time) error {
	guardNotInTx(ctx, "Store.OpenNightCycleOutcome")
	return openNightCycleOutcome(ctx, s.db, sessionID, cycle, startedAt)
}

// OpenNightCycleOutcome is [Store.OpenNightCycleOutcome]'s [Tx] form.
func (t *Tx) OpenNightCycleOutcome(ctx context.Context, sessionID string, cycle int64, startedAt time.Time) error {
	return openNightCycleOutcome(ctx, t.tx, sessionID, cycle, startedAt)
}

func closeNightCycleOutcome(ctx context.Context, q querier, sessionID string, cycle int64, endedAt time.Time, outcome, reason string) error {
	res, err := q.ExecContext(ctx, `
		UPDATE night_cycle_outcomes
		SET ended_at = ?, outcome = ?, reason = ?
		WHERE session_id = ? AND cycle = ? AND ended_at IS NULL
	`, timeToDB(endedAt), outcome, reason, sessionID, cycle)
	if err != nil {
		return fmt.Errorf("store: close night cycle outcome %s/%d: %w", sessionID, cycle, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: close night cycle outcome %s/%d: %w", sessionID, cycle, err)
	}
	if n == 0 {
		return ErrNightCycleOutcomeNotFound
	}
	return nil
}

// CloseNightCycleOutcome records how sessionID's cycle ended. A no-op
// (returning [ErrNightCycleOutcomeNotFound]) when the row does not exist or
// was already closed, so a caller racing another closer of the same cycle
// never overwrites a first recorded outcome with a second, different one.
func (s *Store) CloseNightCycleOutcome(ctx context.Context, sessionID string, cycle int64, endedAt time.Time, outcome, reason string) error {
	guardNotInTx(ctx, "Store.CloseNightCycleOutcome")
	return closeNightCycleOutcome(ctx, s.db, sessionID, cycle, endedAt, outcome, reason)
}

// CloseNightCycleOutcome is [Store.CloseNightCycleOutcome]'s [Tx] form.
func (t *Tx) CloseNightCycleOutcome(ctx context.Context, sessionID string, cycle int64, endedAt time.Time, outcome, reason string) error {
	return closeNightCycleOutcome(ctx, t.tx, sessionID, cycle, endedAt, outcome, reason)
}

func scanNightCycleOutcome(row interface{ Scan(dest ...any) error }) (NightCycleOutcomeRecord, error) {
	var (
		rec           NightCycleOutcomeRecord
		showStartedAt string
		endedAt       sql.NullString
	)
	if err := row.Scan(&rec.SessionID, &rec.Cycle, &showStartedAt, &endedAt, &rec.Outcome, &rec.Reason); err != nil {
		return NightCycleOutcomeRecord{}, err
	}
	var err error
	if rec.ShowStartedAt, err = dbToTime(showStartedAt); err != nil {
		return NightCycleOutcomeRecord{}, fmt.Errorf("store: parse night cycle outcome show_started_at: %w", err)
	}
	if rec.EndedAt, err = dbToTimePtr(endedAt); err != nil {
		return NightCycleOutcomeRecord{}, fmt.Errorf("store: parse night cycle outcome ended_at: %w", err)
	}
	return rec, nil
}

func listNightCycleOutcomes(ctx context.Context, q querier, sessionID string) ([]NightCycleOutcomeRecord, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT session_id, cycle, show_started_at, ended_at, outcome, reason
		FROM night_cycle_outcomes WHERE session_id = ? ORDER BY cycle ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list night cycle outcomes %q: %w", sessionID, err)
	}
	defer rows.Close()
	var out []NightCycleOutcomeRecord
	for rows.Next() {
		rec, err := scanNightCycleOutcome(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list night cycle outcomes %q: %w", sessionID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list night cycle outcomes %q: %w", sessionID, err)
	}
	return out, nil
}

// ListNightCycleOutcomes returns every recorded cycle for sessionID,
// ordered oldest first, including any still open (EndedAt nil).
func (s *Store) ListNightCycleOutcomes(ctx context.Context, sessionID string) ([]NightCycleOutcomeRecord, error) {
	guardNotInTx(ctx, "Store.ListNightCycleOutcomes")
	return listNightCycleOutcomes(ctx, s.db, sessionID)
}

// ListNightCycleOutcomes is [Store.ListNightCycleOutcomes]'s [Tx] form.
func (t *Tx) ListNightCycleOutcomes(ctx context.Context, sessionID string) ([]NightCycleOutcomeRecord, error) {
	return listNightCycleOutcomes(ctx, t.tx, sessionID)
}

func listOpenNightCycleOutcomes(ctx context.Context, q querier) ([]NightCycleOutcomeRecord, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT session_id, cycle, show_started_at, ended_at, outcome, reason
		FROM night_cycle_outcomes WHERE ended_at IS NULL ORDER BY session_id ASC, cycle ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list open night cycle outcomes: %w", err)
	}
	defer rows.Close()
	var out []NightCycleOutcomeRecord
	for rows.Next() {
		rec, err := scanNightCycleOutcome(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list open night cycle outcomes: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list open night cycle outcomes: %w", err)
	}
	return out, nil
}

// ListOpenNightCycleOutcomes returns every cycle across every session that
// was opened but never closed, oldest first. A coordinator restart is the
// only way this build produces one: [ReconcileNightSessionOnStartup] uses
// this to find and close each one as interrupted before serving requests.
func (s *Store) ListOpenNightCycleOutcomes(ctx context.Context) ([]NightCycleOutcomeRecord, error) {
	guardNotInTx(ctx, "Store.ListOpenNightCycleOutcomes")
	return listOpenNightCycleOutcomes(ctx, s.db)
}

// ListOpenNightCycleOutcomes is [Store.ListOpenNightCycleOutcomes]'s [Tx] form.
func (t *Tx) ListOpenNightCycleOutcomes(ctx context.Context) ([]NightCycleOutcomeRecord, error) {
	return listOpenNightCycleOutcomes(ctx, t.tx)
}

// schemaV40 adds night_cycle_outcomes, a pure addition alongside
// night_sessions/night_cue_outbox/night_readiness_results (schemaV10). IF
// NOT EXISTS matches schemaV25/schemaV28/schemaV33/schemaV36/schemaV38: a
// rewound PRAGMA user_version must be able to replay this. No FOREIGN KEY
// to night_sessions: a session row's own lifecycle (schemaV10's own
// doc comment) is independent of retention on this table, matching
// config_revisions' and node_declarations' deliberate FK absence
// (schemaV6's doc comment) rather than node_lwt's CASCADE style.
const schemaV40 = `
CREATE TABLE IF NOT EXISTS night_cycle_outcomes (
	session_id      TEXT NOT NULL,
	cycle           INTEGER NOT NULL,
	show_started_at TEXT NOT NULL,
	ended_at        TEXT,
	outcome         TEXT NOT NULL DEFAULT '',
	reason          TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (session_id, cycle)
);
`

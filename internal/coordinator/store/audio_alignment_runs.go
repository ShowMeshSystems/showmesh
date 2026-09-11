package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// schemaV35 creates audio_alignment_runs and audio_alignment_samples for
// the long-run program-to-LTC drift recording. IF NOT EXISTS matches
// schemaV33's replay-safety precedent.
const schemaV35 = `
CREATE TABLE IF NOT EXISTS audio_alignment_runs (
	id          TEXT PRIMARY KEY,
	node_id     TEXT NOT NULL,
	started_at  TEXT NOT NULL,
	stopped_at  TEXT,
	started_by  TEXT NOT NULL,
	stopped_by  TEXT,
	stop_reason TEXT
);
CREATE INDEX IF NOT EXISTS audio_alignment_runs_node_id ON audio_alignment_runs (node_id, started_at DESC);

CREATE TABLE IF NOT EXISTS audio_alignment_samples (
	run_id     TEXT NOT NULL,
	sampled_at TEXT NOT NULL,
	offset_ms  REAL NOT NULL,
	session_id TEXT NOT NULL,
	PRIMARY KEY (run_id, sampled_at)
);
`

// AlignmentRunRecord is one row of audio_alignment_runs.
type AlignmentRunRecord struct {
	ID         string
	NodeID     string
	StartedAt  time.Time
	StoppedAt  *time.Time
	StartedBy  string
	StoppedBy  *string
	StopReason *string
}

// AlignmentSampleRecord is one row of audio_alignment_samples.
type AlignmentSampleRecord struct {
	RunID     string
	SampledAt time.Time
	OffsetMs  float64
	SessionID string
}

// ErrAlignmentRunNotFound is returned by [Store.GetAlignmentRun] and
// [Store.StopAlignmentRun] when the run id does not exist.
var ErrAlignmentRunNotFound = errors.New("store: audio alignment run not found")

// ErrAlignmentRunAlreadyActive is the [errors.Is] sentinel wrapped by
// [AlignmentRunAlreadyActiveError]: a node may have at most one run with
// stopped_at NULL at a time.
var ErrAlignmentRunAlreadyActive = errors.New("store: an audio alignment run is already active for this node")

// AlignmentRunAlreadyActiveError wraps [ErrAlignmentRunAlreadyActive] with
// the run already active for the node a caller tried to start a second
// run against.
type AlignmentRunAlreadyActiveError struct {
	Active AlignmentRunRecord
}

func (e *AlignmentRunAlreadyActiveError) Error() string {
	return fmt.Sprintf("store: node %q already has an active audio alignment run (id %q)", e.Active.NodeID, e.Active.ID)
}

// Unwrap makes errors.Is(err, ErrAlignmentRunAlreadyActive) true for any
// *AlignmentRunAlreadyActiveError.
func (e *AlignmentRunAlreadyActiveError) Unwrap() error { return ErrAlignmentRunAlreadyActive }

const alignmentRunColumns = `id, node_id, started_at, stopped_at, started_by, stopped_by, stop_reason`

func scanAlignmentRun(row interface{ Scan(dest ...any) error }) (AlignmentRunRecord, error) {
	var (
		rec                            AlignmentRunRecord
		startedAt                      string
		stoppedAt, stoppedBy, stopReas sql.NullString
	)
	if err := row.Scan(&rec.ID, &rec.NodeID, &startedAt, &stoppedAt, &rec.StartedBy, &stoppedBy, &stopReas); err != nil {
		return AlignmentRunRecord{}, err
	}
	var err error
	if rec.StartedAt, err = dbToTime(startedAt); err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: parse audio alignment run started_at: %w", err)
	}
	if rec.StoppedAt, err = dbToTimePtr(stoppedAt); err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: parse audio alignment run stopped_at: %w", err)
	}
	rec.StoppedBy = dbToStringPtr(stoppedBy)
	rec.StopReason = dbToStringPtr(stopReas)
	return rec, nil
}

func findActiveAlignmentRun(ctx context.Context, q querier, nodeID string) (AlignmentRunRecord, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+alignmentRunColumns+` FROM audio_alignment_runs WHERE node_id = ? AND stopped_at IS NULL ORDER BY started_at LIMIT 1`,
		nodeID)
	rec, err := scanAlignmentRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AlignmentRunRecord{}, ErrAlignmentRunNotFound
	}
	if err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: find active audio alignment run for %q: %w", nodeID, err)
	}
	return rec, nil
}

// FindActiveAlignmentRun returns the run currently open (stopped_at NULL)
// for nodeID, or [ErrAlignmentRunNotFound] if none is active.
func (s *Store) FindActiveAlignmentRun(ctx context.Context, nodeID string) (AlignmentRunRecord, error) {
	guardNotInTx(ctx, "Store.FindActiveAlignmentRun")
	return findActiveAlignmentRun(ctx, s.db, nodeID)
}

// CreateAlignmentRun starts a new run for run.NodeID, stamping StartedAt
// from the store's own clock. Returns *[AlignmentRunAlreadyActiveError] if
// the node already has an open run: at most one active run per node.
func (s *Store) CreateAlignmentRun(ctx context.Context, run AlignmentRunRecord) (AlignmentRunRecord, error) {
	guardNotInTx(ctx, "Store.CreateAlignmentRun")
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: begin create audio alignment run: %w", err)
	}
	defer func() { _ = sqlTx.Rollback() }()

	if active, err := findActiveAlignmentRun(ctx, sqlTx, run.NodeID); err == nil {
		return AlignmentRunRecord{}, &AlignmentRunAlreadyActiveError{Active: active}
	} else if !errors.Is(err, ErrAlignmentRunNotFound) {
		return AlignmentRunRecord{}, err
	}

	run.StartedAt = s.now()
	if _, err := sqlTx.ExecContext(ctx,
		`INSERT INTO audio_alignment_runs (id, node_id, started_at, started_by) VALUES (?, ?, ?, ?)`,
		run.ID, run.NodeID, timeToDB(run.StartedAt), run.StartedBy,
	); err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: create audio alignment run: %w", err)
	}
	if err := sqlTx.Commit(); err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: commit create audio alignment run: %w", err)
	}
	return run, nil
}

// StopAlignmentRun sets stopped_at, stopped_by, and stop_reason on runID.
// Returns [ErrAlignmentRunNotFound] if runID does not exist or is already
// stopped.
func (s *Store) StopAlignmentRun(ctx context.Context, runID, stoppedBy, stopReason string) (AlignmentRunRecord, error) {
	guardNotInTx(ctx, "Store.StopAlignmentRun")
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: begin stop audio alignment run: %w", err)
	}
	defer func() { _ = sqlTx.Rollback() }()

	res, err := sqlTx.ExecContext(ctx,
		`UPDATE audio_alignment_runs SET stopped_at = ?, stopped_by = ?, stop_reason = ? WHERE id = ? AND stopped_at IS NULL`,
		timeToDB(s.now()), stoppedBy, stopReason, runID,
	)
	if err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: stop audio alignment run %q: %w", runID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: stop audio alignment run %q: %w", runID, err)
	} else if n == 0 {
		return AlignmentRunRecord{}, ErrAlignmentRunNotFound
	}
	rec, err := getAlignmentRun(ctx, sqlTx, runID)
	if err != nil {
		return AlignmentRunRecord{}, err
	}
	if err := sqlTx.Commit(); err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: commit stop audio alignment run: %w", err)
	}
	return rec, nil
}

func getAlignmentRun(ctx context.Context, q querier, id string) (AlignmentRunRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT `+alignmentRunColumns+` FROM audio_alignment_runs WHERE id = ?`, id)
	rec, err := scanAlignmentRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AlignmentRunRecord{}, ErrAlignmentRunNotFound
	}
	if err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: get audio alignment run %q: %w", id, err)
	}
	return rec, nil
}

func listAlignmentSamples(ctx context.Context, q querier, runID string) ([]AlignmentSampleRecord, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT run_id, sampled_at, offset_ms, session_id FROM audio_alignment_samples WHERE run_id = ? ORDER BY sampled_at`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list audio alignment samples for %q: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []AlignmentSampleRecord
	for rows.Next() {
		var (
			rec       AlignmentSampleRecord
			sampledAt string
		)
		if err := rows.Scan(&rec.RunID, &sampledAt, &rec.OffsetMs, &rec.SessionID); err != nil {
			return nil, fmt.Errorf("store: list audio alignment samples for %q: %w", runID, err)
		}
		if rec.SampledAt, err = dbToTime(sampledAt); err != nil {
			return nil, fmt.Errorf("store: parse audio alignment sample sampled_at: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list audio alignment samples for %q: %w", runID, err)
	}
	return out, nil
}

// GetAlignmentRun returns run and its samples in ascending sampled_at
// order, or [ErrAlignmentRunNotFound].
func (s *Store) GetAlignmentRun(ctx context.Context, id string) (AlignmentRunRecord, []AlignmentSampleRecord, error) {
	guardNotInTx(ctx, "Store.GetAlignmentRun")
	rec, err := getAlignmentRun(ctx, s.db, id)
	if err != nil {
		return AlignmentRunRecord{}, nil, err
	}
	samples, err := listAlignmentSamples(ctx, s.db, id)
	if err != nil {
		return AlignmentRunRecord{}, nil, err
	}
	return rec, samples, nil
}

// ListAlignmentRuns returns nodeID's runs, newest (by started_at) first.
func (s *Store) ListAlignmentRuns(ctx context.Context, nodeID string) ([]AlignmentRunRecord, error) {
	guardNotInTx(ctx, "Store.ListAlignmentRuns")
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+alignmentRunColumns+` FROM audio_alignment_runs WHERE node_id = ? ORDER BY started_at DESC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("store: list audio alignment runs for %q: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []AlignmentRunRecord
	for rows.Next() {
		rec, err := scanAlignmentRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list audio alignment runs for %q: %w", nodeID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list audio alignment runs for %q: %w", nodeID, err)
	}
	return out, nil
}

// AppendAlignmentSample is a no-op (nil error) when runID is stopped or
// (runID, sampledAt) already exists, so a caller can append a replayed
// or duplicated report blindly.
func (s *Store) AppendAlignmentSample(ctx context.Context, sample AlignmentSampleRecord) error {
	guardNotInTx(ctx, "Store.AppendAlignmentSample")
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin append audio alignment sample: %w", err)
	}
	defer func() { _ = sqlTx.Rollback() }()

	run, err := getAlignmentRun(ctx, sqlTx, sample.RunID)
	if err != nil {
		return err
	}
	if run.StoppedAt != nil {
		return nil
	}
	if _, err := sqlTx.ExecContext(ctx,
		`INSERT OR IGNORE INTO audio_alignment_samples (run_id, sampled_at, offset_ms, session_id) VALUES (?, ?, ?, ?)`,
		sample.RunID, timeToDB(sample.SampledAt), sample.OffsetMs, sample.SessionID,
	); err != nil {
		return fmt.Errorf("store: append audio alignment sample: %w", err)
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("store: commit append audio alignment sample: %w", err)
	}
	return nil
}

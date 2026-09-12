package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// schemaV35 creates audio_alignment_runs and audio_alignment_samples for
// the long-run program-to-LTC drift recording. IF NOT EXISTS matches
// schemaV33's replay-safety precedent.
const schemaV35 = `
CREATE TABLE IF NOT EXISTS audio_alignment_runs (
	id                       TEXT PRIMARY KEY,
	node_id                  TEXT NOT NULL,
	started_at               TEXT NOT NULL,
	stopped_at               TEXT,
	started_by               TEXT NOT NULL,
	started_by_principal_id  TEXT NOT NULL,
	stopped_by               TEXT,
	stopped_by_principal_id  TEXT,
	stop_reason              TEXT
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
	ID                   string
	NodeID               string
	StartedAt            time.Time
	StoppedAt            *time.Time
	StartedBy            string
	StartedByPrincipalID string
	StoppedBy            *string
	StoppedByPrincipalID *string
	StopReason           *string
}

// AlignmentSampleRecord is one row of audio_alignment_samples.
type AlignmentSampleRecord struct {
	RunID     string
	SampledAt time.Time
	OffsetMs  float64
	SessionID string
}

// AlignmentRunSummary is computed from a run's full sample series, streamed
// from the store rather than materialized from a fully loaded slice: see
// [Store.GetAlignmentRun]'s own doc comment. DriftRateMsPerHour is nil,
// with DriftRateUnavailableReason set, when fewer than two samples exist.
type AlignmentRunSummary struct {
	SampleCount           int
	FirstSampleAt         *time.Time
	LastSampleAt          *time.Time
	MaxExcursionOffsetMs  *float64
	MaxExcursionSampledAt *time.Time
	DriftRateMsPerHour    *float64

	// DriftRateUnavailableReason is set only when DriftRateMsPerHour is nil.
	DriftRateUnavailableReason string
}

// ErrAlignmentRunNotFound is returned by [Store.GetAlignmentRun] and
// [Store.StopAlignmentRun]/[Tx.StopAlignmentRun] when the run id does not
// exist, or (for stop) is already stopped, or (for both) belongs to a
// different node than the one named.
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

const alignmentRunColumns = `id, node_id, started_at, stopped_at, started_by, started_by_principal_id, stopped_by, stopped_by_principal_id, stop_reason`

func scanAlignmentRun(row interface{ Scan(dest ...any) error }) (AlignmentRunRecord, error) {
	var (
		rec                                        AlignmentRunRecord
		startedAt                                  string
		stoppedAt, stoppedBy, stoppedByPrincipalID sql.NullString
		stopReas                                   sql.NullString
	)
	if err := row.Scan(
		&rec.ID, &rec.NodeID, &startedAt, &stoppedAt, &rec.StartedBy, &rec.StartedByPrincipalID,
		&stoppedBy, &stoppedByPrincipalID, &stopReas,
	); err != nil {
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
	rec.StoppedByPrincipalID = dbToStringPtr(stoppedByPrincipalID)
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

// createAlignmentRun is the shared body of [Store.CreateAlignmentRun] and
// [Tx.CreateAlignmentRun]: starts a new run for run.NodeID, stamping
// StartedAt from now. Returns *[AlignmentRunAlreadyActiveError] if the node
// already has an open run: at most one active run per node.
func createAlignmentRun(ctx context.Context, q querier, run AlignmentRunRecord, now time.Time) (AlignmentRunRecord, error) {
	if active, err := findActiveAlignmentRun(ctx, q, run.NodeID); err == nil {
		return AlignmentRunRecord{}, &AlignmentRunAlreadyActiveError{Active: active}
	} else if !errors.Is(err, ErrAlignmentRunNotFound) {
		return AlignmentRunRecord{}, err
	}

	run.StartedAt = now
	if _, err := q.ExecContext(ctx,
		`INSERT INTO audio_alignment_runs (id, node_id, started_at, started_by, started_by_principal_id) VALUES (?, ?, ?, ?, ?)`,
		run.ID, run.NodeID, timeToDB(run.StartedAt), run.StartedBy, run.StartedByPrincipalID,
	); err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: create audio alignment run: %w", err)
	}
	return run, nil
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

	rec, err := createAlignmentRun(ctx, sqlTx, run, s.now())
	if err != nil {
		return AlignmentRunRecord{}, err
	}
	if err := sqlTx.Commit(); err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: commit create audio alignment run: %w", err)
	}
	return rec, nil
}

// CreateAlignmentRun is [Store.CreateAlignmentRun]'s [Tx] form, letting a
// caller (api.handleStartAlignmentRun, via identity.Service.AuditedWrite)
// compose this write with its ADR-024 decision 11 audit entry in one
// transaction.
func (t *Tx) CreateAlignmentRun(ctx context.Context, run AlignmentRunRecord) (AlignmentRunRecord, error) {
	return createAlignmentRun(ctx, t.tx, run, t.s.now())
}

// stopAlignmentRun is the shared body of [Store.StopAlignmentRun] and
// [Tx.StopAlignmentRun]: sets stopped_at/stopped_by/stopped_by_principal_id/
// stop_reason on runID, scoped to nodeID so a run cannot be stopped through
// any other node's path. Returns [ErrAlignmentRunNotFound] if runID does
// not exist under nodeID or is already stopped.
func stopAlignmentRun(ctx context.Context, q querier, runID, nodeID, stoppedBy, stoppedByPrincipalID, stopReason string, now time.Time) (AlignmentRunRecord, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE audio_alignment_runs SET stopped_at = ?, stopped_by = ?, stopped_by_principal_id = ?, stop_reason = ?
		 WHERE id = ? AND node_id = ? AND stopped_at IS NULL`,
		timeToDB(now), stoppedBy, stoppedByPrincipalID, stopReason, runID, nodeID,
	)
	if err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: stop audio alignment run %q: %w", runID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: stop audio alignment run %q: %w", runID, err)
	} else if n == 0 {
		return AlignmentRunRecord{}, ErrAlignmentRunNotFound
	}
	return getAlignmentRun(ctx, q, runID)
}

// StopAlignmentRun sets stopped_at, stopped_by, and stop_reason on runID,
// scoped to nodeID. Returns [ErrAlignmentRunNotFound] if runID does not
// exist under nodeID or is already stopped.
func (s *Store) StopAlignmentRun(ctx context.Context, runID, nodeID, stoppedBy, stoppedByPrincipalID, stopReason string) (AlignmentRunRecord, error) {
	guardNotInTx(ctx, "Store.StopAlignmentRun")
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: begin stop audio alignment run: %w", err)
	}
	defer func() { _ = sqlTx.Rollback() }()

	rec, err := stopAlignmentRun(ctx, sqlTx, runID, nodeID, stoppedBy, stoppedByPrincipalID, stopReason, s.now())
	if err != nil {
		return AlignmentRunRecord{}, err
	}
	if err := sqlTx.Commit(); err != nil {
		return AlignmentRunRecord{}, fmt.Errorf("store: commit stop audio alignment run: %w", err)
	}
	return rec, nil
}

// StopAlignmentRun is [Store.StopAlignmentRun]'s [Tx] form, letting a
// caller (api.handleStopAlignmentRun, via identity.Service.AuditedWrite)
// compose this write with its ADR-024 decision 11 audit entry in one
// transaction.
func (t *Tx) StopAlignmentRun(ctx context.Context, runID, nodeID, stoppedBy, stoppedByPrincipalID, stopReason string) (AlignmentRunRecord, error) {
	return stopAlignmentRun(ctx, t.tx, runID, nodeID, stoppedBy, stoppedByPrincipalID, stopReason, t.s.now())
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

// GetAlignmentRun returns run and up to limit of its samples in ascending
// sampled_at order, plus truncated (true when the run holds more than
// limit samples) and a summary computed over the FULL series. The
// summary is accumulated while streaming rows off the query (never by
// materializing every sample into a slice first): nothing bounds how long
// a run may stay active, so a forgotten run can hold hundreds of
// thousands of samples, and this must not hold all of them in memory just
// to answer a bounded request. Returns [ErrAlignmentRunNotFound] if id
// does not exist.
func (s *Store) GetAlignmentRun(ctx context.Context, id string, limit int) (AlignmentRunRecord, []AlignmentSampleRecord, bool, AlignmentRunSummary, error) {
	guardNotInTx(ctx, "Store.GetAlignmentRun")
	rec, err := getAlignmentRun(ctx, s.db, id)
	if err != nil {
		return AlignmentRunRecord{}, nil, false, AlignmentRunSummary{}, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT sampled_at, offset_ms, session_id FROM audio_alignment_samples WHERE run_id = ? ORDER BY sampled_at`, id)
	if err != nil {
		return AlignmentRunRecord{}, nil, false, AlignmentRunSummary{}, fmt.Errorf("store: list audio alignment samples for %q: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	var (
		samples []AlignmentSampleRecord

		count                    int
		firstAt, lastAt          time.Time
		haveFirst                bool
		maxAbsOffset, maxOffset  float64
		maxAt                    time.Time
		haveMax                  bool
		sumX, sumY, sumXY, sumXX float64
	)
	if limit > 0 {
		samples = make([]AlignmentSampleRecord, 0, minInt(limit, 1024))
	}
	for rows.Next() {
		var (
			sampledAtStr string
			offsetMs     float64
			sessionID    string
		)
		if err := rows.Scan(&sampledAtStr, &offsetMs, &sessionID); err != nil {
			return AlignmentRunRecord{}, nil, false, AlignmentRunSummary{}, fmt.Errorf("store: list audio alignment samples for %q: %w", id, err)
		}
		sampledAt, err := dbToTime(sampledAtStr)
		if err != nil {
			return AlignmentRunRecord{}, nil, false, AlignmentRunSummary{}, fmt.Errorf("store: parse audio alignment sample sampled_at: %w", err)
		}

		if !haveFirst {
			firstAt = sampledAt
			haveFirst = true
		}
		lastAt = sampledAt
		count++

		if count <= limit {
			samples = append(samples, AlignmentSampleRecord{RunID: id, SampledAt: sampledAt, OffsetMs: offsetMs, SessionID: sessionID})
		}

		if abs := math.Abs(offsetMs); !haveMax || abs > maxAbsOffset {
			maxAbsOffset, maxOffset, maxAt, haveMax = abs, offsetMs, sampledAt, true
		}

		x := sampledAt.Sub(firstAt).Hours()
		sumX += x
		sumY += offsetMs
		sumXY += x * offsetMs
		sumXX += x * x
	}
	if err := rows.Err(); err != nil {
		return AlignmentRunRecord{}, nil, false, AlignmentRunSummary{}, fmt.Errorf("store: list audio alignment samples for %q: %w", id, err)
	}
	if samples == nil {
		samples = []AlignmentSampleRecord{}
	}

	summary := AlignmentRunSummary{SampleCount: count}
	switch count {
	case 0:
		summary.DriftRateUnavailableReason = "no samples recorded"
	default:
		ft, lt, mo, ma := firstAt, lastAt, maxOffset, maxAt
		summary.FirstSampleAt = &ft
		summary.LastSampleAt = &lt
		summary.MaxExcursionOffsetMs = &mo
		summary.MaxExcursionSampledAt = &ma
		if count < 2 {
			summary.DriftRateUnavailableReason = "fewer than two samples: no slope can be computed"
		} else if denominator := float64(count)*sumXX - sumX*sumX; denominator == 0 {
			summary.DriftRateUnavailableReason = "every sample shares the same timestamp: no slope can be computed"
		} else {
			rate := (float64(count)*sumXY - sumX*sumY) / denominator
			summary.DriftRateMsPerHour = &rate
		}
	}

	return rec, samples, count > limit, summary, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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

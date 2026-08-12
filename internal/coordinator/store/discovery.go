package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV6's discovery_runs repository methods (Step 7
// seam 0). See migrations.go's schemaV6 doc comment: Complete exists so a
// caller (seam B) may apply Step 5's "only a complete poll may claim
// anything about absence" rule to node inventory, and Reason exists so an
// incomplete run states why per ADR-020's absent-evidence rule — a run
// that fails partway is a row with Complete=false and a Reason, never a
// missing row. This file knows nothing about what actually performs
// discovery (an mDNS probe, an MQTT presence scan, or anything else) —
// that mechanism is seam B's; this file only records that a run happened
// and what it found.

// DiscoveryRunRecord is one row of the discovery_runs table.
// FinishedAt is nil while a run is still in progress. Complete is false
// both while in progress and if the run ended without completing — Reason
// distinguishes "still running" (Reason == "" and FinishedAt == nil) from
// "ended incomplete" (Reason != "" and FinishedAt != nil); see
// [Store.FinishDiscoveryRun].
type DiscoveryRunRecord struct {
	ID                       string
	StartedAt                time.Time
	FinishedAt               *time.Time
	Complete                 bool
	Reason                   string
	FoundCount               int64
	InitiatedByPrincipalID   string
	InitiatedByPrincipalName string
}

// ErrDiscoveryRunNotFound is returned by [Store.GetDiscoveryRun]/
// [Tx.GetDiscoveryRun] when no row exists for id.
var ErrDiscoveryRunNotFound = errors.New("store: discovery run not found")

func startDiscoveryRun(ctx context.Context, q querier, s *Store, rec DiscoveryRunRecord, now time.Time) (DiscoveryRunRecord, error) {
	rec.StartedAt = now
	rec.FinishedAt = nil
	rec.Complete = false
	rec.FoundCount = 0
	_, err := q.ExecContext(ctx, `
		INSERT INTO discovery_runs (
			id, started_at, finished_at, complete, reason, found_count,
			initiated_by_principal_id, initiated_by_principal_name
		) VALUES (?, ?, NULL, 0, ?, 0, ?, ?)
	`, rec.ID, timeToDB(rec.StartedAt), rec.Reason, rec.InitiatedByPrincipalID, rec.InitiatedByPrincipalName)
	if err != nil {
		return DiscoveryRunRecord{}, fmt.Errorf("store: start discovery run %q: %w", rec.ID, err)
	}

	// Same two independent triggers as [appendAuditEntry] (audit.go): insert
	// volume and elapsed wall-clock time since the last prune pass. See
	// retention.go's pruneEveryNDiscoveryRuns/pruneCheckInterval doc comments.
	byCount := s.discoveryRunInsertCount.Add(1)%pruneEveryNDiscoveryRuns == 0
	byAge := false
	if !byCount {
		last := s.lastDiscoveryRunPruneAtNanos.Load()
		byAge = last == 0 || s.now().Sub(time.Unix(0, last)) >= pruneCheckInterval
	}
	if byCount || byAge {
		if err := s.pruneDiscoveryRuns(ctx, q); err != nil {
			return DiscoveryRunRecord{}, fmt.Errorf("store: start discovery run %q: %w", rec.ID, err)
		}
		s.lastDiscoveryRunPruneAtNanos.Store(s.now().UnixNano())
	}

	return rec, nil
}

// StartDiscoveryRun records a new, in-progress discovery run. rec.ID must
// already be set by the caller (a generated identifier, matching this
// package's convention elsewhere — e.g. principals.id — of the caller
// generating IDs rather than this package inventing one an audit trail
// would later need to explain).
func (s *Store) StartDiscoveryRun(ctx context.Context, rec DiscoveryRunRecord) (DiscoveryRunRecord, error) {
	guardNotInTx(ctx, "Store.StartDiscoveryRun")
	return startDiscoveryRun(ctx, s.db, s, rec, s.now())
}

// StartDiscoveryRun is [Store.StartDiscoveryRun]'s [Tx] form.
func (t *Tx) StartDiscoveryRun(ctx context.Context, rec DiscoveryRunRecord) (DiscoveryRunRecord, error) {
	return startDiscoveryRun(ctx, t.tx, t.s, rec, t.s.now())
}

func finishDiscoveryRun(ctx context.Context, q querier, id string, complete bool, reason string, foundCount int64, now time.Time) error {
	res, err := q.ExecContext(ctx, `
		UPDATE discovery_runs
		SET finished_at = ?, complete = ?, reason = ?, found_count = ?
		WHERE id = ?
	`, timeToDB(now), boolToDB(complete), reason, foundCount, id)
	if err != nil {
		return fmt.Errorf("store: finish discovery run %q: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: finish discovery run %q: %w", id, ErrDiscoveryRunNotFound)
	}
	return nil
}

// FinishDiscoveryRun records id's terminal state: complete (with
// foundCount nodes seen) or, if the run could not complete, complete=false
// with reason stating why — never a row left silently unfinished. A
// caller reporting an incomplete run must pass a non-empty reason; this
// method does not enforce that itself (matching this package's general
// posture of trusting a caller who has already validated its own domain
// rules — see e.g. [Store.AppendEvent]'s identical trust of its caller for
// Category/Severity's vocabulary), but ADR-020's absent-evidence rule
// depends on it being true in practice.
func (s *Store) FinishDiscoveryRun(ctx context.Context, id string, complete bool, reason string, foundCount int64) error {
	guardNotInTx(ctx, "Store.FinishDiscoveryRun")
	return finishDiscoveryRun(ctx, s.db, id, complete, reason, foundCount, s.now())
}

// FinishDiscoveryRun is [Store.FinishDiscoveryRun]'s [Tx] form.
func (t *Tx) FinishDiscoveryRun(ctx context.Context, id string, complete bool, reason string, foundCount int64) error {
	return finishDiscoveryRun(ctx, t.tx, id, complete, reason, foundCount, t.s.now())
}

const discoveryRunColumns = `
	id, started_at, finished_at, complete, reason, found_count,
	initiated_by_principal_id, initiated_by_principal_name
`

func scanDiscoveryRun(row interface{ Scan(dest ...any) error }) (DiscoveryRunRecord, error) {
	var (
		rec        DiscoveryRunRecord
		startedAt  string
		finishedAt sql.NullString
		complete   int64
	)
	if err := row.Scan(
		&rec.ID, &startedAt, &finishedAt, &complete, &rec.Reason, &rec.FoundCount,
		&rec.InitiatedByPrincipalID, &rec.InitiatedByPrincipalName,
	); err != nil {
		return DiscoveryRunRecord{}, err
	}
	rec.Complete = complete != 0
	var err error
	if rec.StartedAt, err = dbToTime(startedAt); err != nil {
		return DiscoveryRunRecord{}, fmt.Errorf("store: parse discovery run started_at: %w", err)
	}
	if rec.FinishedAt, err = dbToTimePtr(finishedAt); err != nil {
		return DiscoveryRunRecord{}, fmt.Errorf("store: parse discovery run finished_at: %w", err)
	}
	return rec, nil
}

func getDiscoveryRun(ctx context.Context, q querier, id string) (DiscoveryRunRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+discoveryRunColumns+`FROM discovery_runs WHERE id = ?`, id)
	rec, err := scanDiscoveryRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DiscoveryRunRecord{}, ErrDiscoveryRunNotFound
	}
	if err != nil {
		return DiscoveryRunRecord{}, fmt.Errorf("store: get discovery run %q: %w", id, err)
	}
	return rec, nil
}

// GetDiscoveryRun returns one discovery run, or [ErrDiscoveryRunNotFound].
func (s *Store) GetDiscoveryRun(ctx context.Context, id string) (DiscoveryRunRecord, error) {
	guardNotInTx(ctx, "Store.GetDiscoveryRun")
	return getDiscoveryRun(ctx, s.db, id)
}

// GetDiscoveryRun is [Store.GetDiscoveryRun]'s [Tx] form.
func (t *Tx) GetDiscoveryRun(ctx context.Context, id string) (DiscoveryRunRecord, error) {
	return getDiscoveryRun(ctx, t.tx, id)
}

// DefaultDiscoveryRunPageSize and MaxDiscoveryRunPageSize bound
// [Store.ListDiscoveryRuns]'s limit parameter, mirroring
// [DefaultEventsPageSize]/[MaxEventsPageSize] for the identical reason.
const (
	DefaultDiscoveryRunPageSize = 50
	MaxDiscoveryRunPageSize     = 200
)

func listDiscoveryRuns(ctx context.Context, q querier, limit int) ([]DiscoveryRunRecord, error) {
	switch {
	case limit <= 0:
		limit = DefaultDiscoveryRunPageSize
	case limit > MaxDiscoveryRunPageSize:
		limit = MaxDiscoveryRunPageSize
	}
	rows, err := q.QueryContext(ctx, `SELECT`+discoveryRunColumns+`FROM discovery_runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list discovery runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DiscoveryRunRecord
	for rows.Next() {
		rec, err := scanDiscoveryRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list discovery runs: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list discovery runs: %w", err)
	}
	return out, nil
}

// ListDiscoveryRuns returns the most recent discovery runs, newest first,
// capped at limit (limit <= 0 defaults to [DefaultDiscoveryRunPageSize];
// anything above [MaxDiscoveryRunPageSize] is clamped down to it).
func (s *Store) ListDiscoveryRuns(ctx context.Context, limit int) ([]DiscoveryRunRecord, error) {
	guardNotInTx(ctx, "Store.ListDiscoveryRuns")
	return listDiscoveryRuns(ctx, s.db, limit)
}

// ListDiscoveryRuns is [Store.ListDiscoveryRuns]'s [Tx] form.
func (t *Tx) ListDiscoveryRuns(ctx context.Context, limit int) ([]DiscoveryRunRecord, error) {
	return listDiscoveryRuns(ctx, t.tx, limit)
}

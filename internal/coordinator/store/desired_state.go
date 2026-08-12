package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file holds schemaV6's desired_state repository methods (Step 7
// seam 0; ADR-003's desired/observed split, expressed in storage for the
// first time). See migrations.go's schemaV6 doc comment: NOTHING
// RECONCILES THIS TABLE, and that is a standing constraint, not a gap —
// a background loop comparing desired to observed and re-issuing commands
// to close any gap would make ShowMesh a second scheduler, which ADR-001
// forbids as this project's first constraint. This table exists only so
// ADR-003's split is expressible in storage and so a command's
// confirmation has a recorded target to compare against.

// DesiredStateRecord is one row of the desired_state table: what an
// operator (or a command issued on their behalf) asked for, for one
// (resource_kind, resource_id, signal) — the same identifying triple
// pkg/observation.Observation uses for what was actually OBSERVED, so the
// two are directly comparable by a caller (never by this package). Value
// uses the identical discriminated bool | string | int64 | float64 | nil
// encoding [observation.Observation.Value] does and for the identical
// reason — see [encodeObservationValue]/[decodeObservationValue] in
// observations.go, reused here rather than duplicated.
type DesiredStateRecord struct {
	ResourceKind           string
	ResourceID             string
	Signal                 string
	Value                  any
	RequestedAt            time.Time
	RequestedByPrincipalID string
	CommandID              string
	DeadlineAt             *time.Time
}

// ErrDesiredStateNotFound is returned by [Store.GetDesiredState]/
// [Tx.GetDesiredState] when no row exists for the given triple.
var ErrDesiredStateNotFound = errors.New("store: desired state not found")

func setDesiredState(ctx context.Context, q querier, rec DesiredStateRecord) (DesiredStateRecord, error) {
	valueKind, valueText, err := encodeObservationValue(rec.Value)
	if err != nil {
		return DesiredStateRecord{}, fmt.Errorf("store: set desired state %s/%s/%s: %w", rec.ResourceKind, rec.ResourceID, rec.Signal, err)
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO desired_state (
			resource_kind, resource_id, signal, value_kind, value_text,
			requested_at, requested_by_principal_id, command_id, deadline_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(resource_kind, resource_id, signal) DO UPDATE SET
			value_kind                = excluded.value_kind,
			value_text                = excluded.value_text,
			requested_at              = excluded.requested_at,
			requested_by_principal_id = excluded.requested_by_principal_id,
			command_id                = excluded.command_id,
			deadline_at               = excluded.deadline_at
	`,
		rec.ResourceKind, rec.ResourceID, rec.Signal, valueKind, valueText,
		timeToDB(rec.RequestedAt), rec.RequestedByPrincipalID, rec.CommandID, timePtrToDB(rec.DeadlineAt),
	)
	if err != nil {
		return DesiredStateRecord{}, fmt.Errorf("store: set desired state %s/%s/%s: %w", rec.ResourceKind, rec.ResourceID, rec.Signal, err)
	}
	return rec, nil
}

// SetDesiredState upserts the desired value for one (resource_kind,
// resource_id, signal), replacing whatever was previously desired for that
// triple — this is a pointer at the LATEST request, never a history (the
// commands table, and audit_log via ADR-024 decision 11, are where
// history of "who asked for what, when" actually lives).
func (s *Store) SetDesiredState(ctx context.Context, rec DesiredStateRecord) (DesiredStateRecord, error) {
	guardNotInTx(ctx, "Store.SetDesiredState")
	return setDesiredState(ctx, s.db, rec)
}

// SetDesiredState is [Store.SetDesiredState]'s [Tx] form.
func (t *Tx) SetDesiredState(ctx context.Context, rec DesiredStateRecord) (DesiredStateRecord, error) {
	return setDesiredState(ctx, t.tx, rec)
}

const desiredStateColumns = `
	resource_kind, resource_id, signal, value_kind, value_text,
	requested_at, requested_by_principal_id, command_id, deadline_at
`

func scanDesiredState(row interface{ Scan(dest ...any) error }) (DesiredStateRecord, error) {
	var (
		rec                  DesiredStateRecord
		valueKind, valueText string
		requestedAt          string
		deadlineAt           sql.NullString
	)
	if err := row.Scan(
		&rec.ResourceKind, &rec.ResourceID, &rec.Signal, &valueKind, &valueText,
		&requestedAt, &rec.RequestedByPrincipalID, &rec.CommandID, &deadlineAt,
	); err != nil {
		return DesiredStateRecord{}, err
	}
	value, err := decodeObservationValue(valueKind, valueText)
	if err != nil {
		return DesiredStateRecord{}, fmt.Errorf("store: decode desired state value %s/%s/%s: %w", rec.ResourceKind, rec.ResourceID, rec.Signal, err)
	}
	rec.Value = value
	if rec.RequestedAt, err = dbToTime(requestedAt); err != nil {
		return DesiredStateRecord{}, fmt.Errorf("store: parse desired state requested_at: %w", err)
	}
	if rec.DeadlineAt, err = dbToTimePtr(deadlineAt); err != nil {
		return DesiredStateRecord{}, fmt.Errorf("store: parse desired state deadline_at: %w", err)
	}
	return rec, nil
}

func getDesiredState(ctx context.Context, q querier, resourceKind, resourceID, signal string) (DesiredStateRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+desiredStateColumns+`FROM desired_state WHERE resource_kind = ? AND resource_id = ? AND signal = ?`,
		resourceKind, resourceID, signal)
	rec, err := scanDesiredState(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DesiredStateRecord{}, ErrDesiredStateNotFound
	}
	if err != nil {
		return DesiredStateRecord{}, fmt.Errorf("store: get desired state %s/%s/%s: %w", resourceKind, resourceID, signal, err)
	}
	return rec, nil
}

// GetDesiredState returns the current desired value for one triple, or
// [ErrDesiredStateNotFound] if nothing has ever been requested for it.
func (s *Store) GetDesiredState(ctx context.Context, resourceKind, resourceID, signal string) (DesiredStateRecord, error) {
	guardNotInTx(ctx, "Store.GetDesiredState")
	return getDesiredState(ctx, s.db, resourceKind, resourceID, signal)
}

// GetDesiredState is [Store.GetDesiredState]'s [Tx] form.
func (t *Tx) GetDesiredState(ctx context.Context, resourceKind, resourceID, signal string) (DesiredStateRecord, error) {
	return getDesiredState(ctx, t.tx, resourceKind, resourceID, signal)
}

// DesiredStateFilter narrows [Store.ListDesiredState], mirroring
// [ObservationFilter]'s shape exactly (observations.go): every field is
// optional (empty means "match any").
type DesiredStateFilter struct {
	ResourceKind string
	ResourceID   string
	Signal       string
}

func listDesiredState(ctx context.Context, q querier, filter DesiredStateFilter) ([]DesiredStateRecord, error) {
	var clauses []string
	var args []any
	if filter.ResourceKind != "" {
		clauses = append(clauses, "resource_kind = ?")
		args = append(args, filter.ResourceKind)
	}
	if filter.ResourceID != "" {
		clauses = append(clauses, "resource_id = ?")
		args = append(args, filter.ResourceID)
	}
	if filter.Signal != "" {
		clauses = append(clauses, "signal = ?")
		args = append(args, filter.Signal)
	}

	query := "SELECT" + desiredStateColumns + "FROM desired_state"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY resource_kind, resource_id, signal"

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list desired state: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DesiredStateRecord
	for rows.Next() {
		rec, err := scanDesiredState(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list desired state: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list desired state: %w", err)
	}
	return out, nil
}

// ListDesiredState returns every desired_state row matching filter,
// ordered for a stable, deterministic result — mirroring
// [Store.ListObservations]'s convention exactly, since the two are meant
// to be read side by side.
func (s *Store) ListDesiredState(ctx context.Context, filter DesiredStateFilter) ([]DesiredStateRecord, error) {
	guardNotInTx(ctx, "Store.ListDesiredState")
	return listDesiredState(ctx, s.db, filter)
}

// ListDesiredState is [Store.ListDesiredState]'s [Tx] form.
func (t *Tx) ListDesiredState(ctx context.Context, filter DesiredStateFilter) ([]DesiredStateRecord, error) {
	return listDesiredState(ctx, t.tx, filter)
}

func deleteDesiredState(ctx context.Context, q querier, resourceKind, resourceID, signal string) error {
	res, err := q.ExecContext(ctx, `DELETE FROM desired_state WHERE resource_kind = ? AND resource_id = ? AND signal = ?`,
		resourceKind, resourceID, signal)
	if err != nil {
		return fmt.Errorf("store: delete desired state %s/%s/%s: %w", resourceKind, resourceID, signal, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: delete desired state %s/%s/%s: %w", resourceKind, resourceID, signal, ErrDesiredStateNotFound)
	}
	return nil
}

// DeleteDesiredState removes a triple's desired value entirely (distinct
// from setting it to nil, which is a desired value OF "nothing" and still
// a row — see [encodeObservationValue]'s valueKindNone case). Not expected
// to be a common operation in Step 7: named here because a resource being
// decommissioned needs a way to stop being compared against, and inventing
// that later as a second method would risk a second, drifted copy of this
// query's shape.
func (s *Store) DeleteDesiredState(ctx context.Context, resourceKind, resourceID, signal string) error {
	guardNotInTx(ctx, "Store.DeleteDesiredState")
	return deleteDesiredState(ctx, s.db, resourceKind, resourceID, signal)
}

// DeleteDesiredState is [Store.DeleteDesiredState]'s [Tx] form.
func (t *Tx) DeleteDesiredState(ctx context.Context, resourceKind, resourceID, signal string) error {
	return deleteDesiredState(ctx, t.tx, resourceKind, resourceID, signal)
}

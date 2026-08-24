package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file answers the gap recorded in
// docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 1.5: the
// coordinator already decodes FPP's SystemUUID off every
// /api/fppd/status poll (internal/coordinator/collector/fpp/signals.go's
// "fpp.uuid" signal) and off every plugin-posted playlist-entry
// observation (its instanceUuid field), but neither side ever persisted
// it against the operator-typed fpp.endpoints id or compared it to what
// that endpoint reported before. Without that, an observation cannot be
// attributed to a configured endpoint, and an SD-card clone, a restored
// backup, or a swapped controller (see ADR-025's own list of the
// operational failures a per-installation identity check exists to
// catch) looks identical to business as usual.
//
// This table is observed evidence, never desired state (ADR-003): it
// records what an endpoint has REPORTED, not what an operator declared,
// and nothing here ever mutates fpp.endpoints or any other
// operator-authored configuration. It follows node_declarations'
// declared-vs-observed split in spirit (schemaV6's own doc comment) but
// is itself entirely on the OBSERVED side of that split, closer in kind
// to the observations table than to node_declarations.
//
// The two rules this table exists to keep visible, never silent:
//
//  1. A UUID change on a known endpoint is never a silent re-association.
//     recordFPPInstanceUUIDObservation leaves the endpoint's PREVIOUS uuid
//     recorded (previous_uuid) alongside the new one until an operator
//     explicitly acknowledges the change (AcknowledgeFPPInstanceUUIDChange).
//     The row's current uuid still advances immediately, refusing to
//     record what an endpoint is ACTUALLY reporting would make every
//     other observation about it stale for no benefit, but
//     HasUnacknowledgedChange() stays true, and every reader (the API,
//     showmeshctl) surfaces that fact, until an operator clears it.
//  2. Two endpoints reporting the same uuid is never a silently
//     overwritten row: this table is keyed by endpoint_id, not by uuid,
//     so a second endpoint reporting a uuid already claimed by another
//     endpoint gets its OWN row rather than clobbering the first.
//     ListFPPInstanceUUIDDuplicates groups the current table contents by
//     uuid and returns every uuid claimed by more than one endpoint, as a
//     stated finding a caller renders rather than something this package
//     resolves on its own.
type FPPInstanceUUIDRecord struct {
	EndpointID      string
	UUID            string
	FirstObservedAt time.Time
	LastObservedAt  time.Time

	// PreviousUUID is "" when this endpoint has no unacknowledged UUID
	// change pending. When non-empty, it is the uuid this endpoint
	// reported immediately before UUID (the current value) was first
	// observed, and ChangedAt is when that change was first observed.
	PreviousUUID string
	ChangedAt    time.Time

	// ChangeAcknowledgedAt/By are the zero value / "" until an operator
	// calls AcknowledgeFPPInstanceUUIDChange; they are never cleared by a
	// later, different change (a fresh change re-populates PreviousUUID/
	// ChangedAt and blanks these again, since the earlier acknowledgment
	// spoke to a different transition).
	ChangeAcknowledgedAt              time.Time
	ChangeAcknowledgedByPrincipalID   string
	ChangeAcknowledgedByPrincipalName string

	UpdatedAt time.Time
}

// HasUnacknowledgedChange reports whether r's current UUID differs from
// what this endpoint most recently reported before that, with no operator
// acknowledgment recorded since. This is the field every caller renders
// as a visible conflict, not a boolean this package leaves to be
// recomputed from a nullable timestamp at every call site.
func (r FPPInstanceUUIDRecord) HasUnacknowledgedChange() bool {
	return r.PreviousUUID != ""
}

// ErrFPPInstanceUUIDNotFound is returned by [Store.GetFPPInstanceUUID] /
// [Tx.GetFPPInstanceUUID] when endpointID has never reported a uuid.
var ErrFPPInstanceUUIDNotFound = errors.New("store: fpp instance uuid not found")

// ErrFPPInstanceUUIDNoUnacknowledgedChange is returned by
// [Store.AcknowledgeFPPInstanceUUIDChange] / [Tx.AcknowledgeFPPInstanceUUIDChange]
// when endpointID currently has no unacknowledged change to acknowledge ,
// refusing rather than silently no-op-ing, so a caller cannot mistake a
// stale request for one that actually cleared a conflict.
var ErrFPPInstanceUUIDNoUnacknowledgedChange = errors.New("store: fpp instance has no unacknowledged uuid change")

const fppInstanceUUIDColumns = `
	endpoint_id, uuid, first_observed_at, last_observed_at,
	previous_uuid, changed_at,
	change_acknowledged_at, change_acknowledged_by_principal_id, change_acknowledged_by_principal_name,
	updated_at
`

func scanFPPInstanceUUID(row interface{ Scan(dest ...any) error }) (FPPInstanceUUIDRecord, error) {
	var (
		rec               FPPInstanceUUIDRecord
		firstObservedAt   string
		lastObservedAt    string
		changedAt         string
		changeAcknowledge string
		updatedAt         string
	)
	if err := row.Scan(
		&rec.EndpointID, &rec.UUID, &firstObservedAt, &lastObservedAt,
		&rec.PreviousUUID, &changedAt,
		&changeAcknowledge, &rec.ChangeAcknowledgedByPrincipalID, &rec.ChangeAcknowledgedByPrincipalName,
		&updatedAt,
	); err != nil {
		return FPPInstanceUUIDRecord{}, err
	}
	var err error
	if rec.FirstObservedAt, err = dbToTime(firstObservedAt); err != nil {
		return FPPInstanceUUIDRecord{}, fmt.Errorf("store: parse fpp instance uuid first_observed_at: %w", err)
	}
	if rec.LastObservedAt, err = dbToTime(lastObservedAt); err != nil {
		return FPPInstanceUUIDRecord{}, fmt.Errorf("store: parse fpp instance uuid last_observed_at: %w", err)
	}
	if changedAt != "" {
		if rec.ChangedAt, err = dbToTime(changedAt); err != nil {
			return FPPInstanceUUIDRecord{}, fmt.Errorf("store: parse fpp instance uuid changed_at: %w", err)
		}
	}
	if changeAcknowledge != "" {
		if rec.ChangeAcknowledgedAt, err = dbToTime(changeAcknowledge); err != nil {
			return FPPInstanceUUIDRecord{}, fmt.Errorf("store: parse fpp instance uuid change_acknowledged_at: %w", err)
		}
	}
	if rec.UpdatedAt, err = dbToTime(updatedAt); err != nil {
		return FPPInstanceUUIDRecord{}, fmt.Errorf("store: parse fpp instance uuid updated_at: %w", err)
	}
	return rec, nil
}

func getFPPInstanceUUID(ctx context.Context, q querier, endpointID string) (FPPInstanceUUIDRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+fppInstanceUUIDColumns+`FROM fpp_instance_uuid_observations WHERE endpoint_id = ?`, endpointID)
	rec, err := scanFPPInstanceUUID(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FPPInstanceUUIDRecord{}, ErrFPPInstanceUUIDNotFound
	}
	if err != nil {
		return FPPInstanceUUIDRecord{}, fmt.Errorf("store: get fpp instance uuid %q: %w", endpointID, err)
	}
	return rec, nil
}

// GetFPPInstanceUUID returns endpointID's most recently observed uuid
// record, or [ErrFPPInstanceUUIDNotFound] if this endpoint has never
// reported one.
func (s *Store) GetFPPInstanceUUID(ctx context.Context, endpointID string) (FPPInstanceUUIDRecord, error) {
	guardNotInTx(ctx, "Store.GetFPPInstanceUUID")
	return getFPPInstanceUUID(ctx, s.db, endpointID)
}

// GetFPPInstanceUUID is [Store.GetFPPInstanceUUID]'s [Tx] form.
func (t *Tx) GetFPPInstanceUUID(ctx context.Context, endpointID string) (FPPInstanceUUIDRecord, error) {
	return getFPPInstanceUUID(ctx, t.tx, endpointID)
}

func listFPPInstanceUUIDs(ctx context.Context, q querier) ([]FPPInstanceUUIDRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT`+fppInstanceUUIDColumns+`FROM fpp_instance_uuid_observations ORDER BY endpoint_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list fpp instance uuids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []FPPInstanceUUIDRecord
	for rows.Next() {
		rec, err := scanFPPInstanceUUID(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list fpp instance uuids: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list fpp instance uuids: %w", err)
	}
	return out, nil
}

// ListFPPInstanceUUIDs returns every endpoint that has ever reported a
// uuid, ordered by endpoint ID for a stable, deterministic result.
func (s *Store) ListFPPInstanceUUIDs(ctx context.Context) ([]FPPInstanceUUIDRecord, error) {
	guardNotInTx(ctx, "Store.ListFPPInstanceUUIDs")
	return listFPPInstanceUUIDs(ctx, s.db)
}

// ListFPPInstanceUUIDs is [Store.ListFPPInstanceUUIDs]'s [Tx] form.
func (t *Tx) ListFPPInstanceUUIDs(ctx context.Context) ([]FPPInstanceUUIDRecord, error) {
	return listFPPInstanceUUIDs(ctx, t.tx)
}

// FPPInstanceUUIDDuplicate is one uuid two or more configured endpoints
// have reported, the duplicate-uuid rule's stated finding, never silently resolved by
// this package in either direction.
type FPPInstanceUUIDDuplicate struct {
	UUID        string
	EndpointIDs []string
}

// ListFPPInstanceUUIDDuplicates groups every row in
// fpp_instance_uuid_observations by its CURRENT uuid and returns each
// group claimed by more than one endpoint, ordered by uuid. It reads the
// whole table rather than filtering by a caller-supplied endpoint list,
// because a duplicate is a fact about the table, not about any one
// endpoint's view of it; callers that only care about currently
// configured endpoints (the ordinary case, a removed endpoint's row is
// not pruned from this table, mirroring node_declarations surviving an
// absent observed row) filter the result against their own live
// fpp.endpoints list.
func listFPPInstanceUUIDDuplicates(ctx context.Context, q querier) ([]FPPInstanceUUIDDuplicate, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT uuid, endpoint_id
		FROM fpp_instance_uuid_observations
		WHERE uuid IN (
			SELECT uuid FROM fpp_instance_uuid_observations GROUP BY uuid HAVING COUNT(*) > 1
		)
		ORDER BY uuid, endpoint_id
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list fpp instance uuid duplicates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []FPPInstanceUUIDDuplicate
	for rows.Next() {
		var uuid, endpointID string
		if err := rows.Scan(&uuid, &endpointID); err != nil {
			return nil, fmt.Errorf("store: list fpp instance uuid duplicates: %w", err)
		}
		if len(out) == 0 || out[len(out)-1].UUID != uuid {
			out = append(out, FPPInstanceUUIDDuplicate{UUID: uuid})
		}
		out[len(out)-1].EndpointIDs = append(out[len(out)-1].EndpointIDs, endpointID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list fpp instance uuid duplicates: %w", err)
	}
	return out, nil
}

// ListFPPInstanceUUIDDuplicates is the [Store] form of
// [listFPPInstanceUUIDDuplicates].
func (s *Store) ListFPPInstanceUUIDDuplicates(ctx context.Context) ([]FPPInstanceUUIDDuplicate, error) {
	guardNotInTx(ctx, "Store.ListFPPInstanceUUIDDuplicates")
	return listFPPInstanceUUIDDuplicates(ctx, s.db)
}

// ListFPPInstanceUUIDDuplicates is [Store.ListFPPInstanceUUIDDuplicates]'s
// [Tx] form.
func (t *Tx) ListFPPInstanceUUIDDuplicates(ctx context.Context) ([]FPPInstanceUUIDDuplicate, error) {
	return listFPPInstanceUUIDDuplicates(ctx, t.tx)
}

// GetFPPInstanceUUIDByUUID returns every endpoint currently recorded as
// having reported uuid, ordinarily zero or one, more than one exactly
// when [ListFPPInstanceUUIDDuplicates] would also report uuid. This is
// the correlation direction TRACK-H's playlist-entry observations need:
// given an instanceUuid a plugin posted, which configured endpoint (if
// any, if only one) does it belong to.
func getFPPInstanceUUIDByUUID(ctx context.Context, q querier, uuid string) ([]FPPInstanceUUIDRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT`+fppInstanceUUIDColumns+`FROM fpp_instance_uuid_observations WHERE uuid = ? ORDER BY endpoint_id`, uuid)
	if err != nil {
		return nil, fmt.Errorf("store: get fpp instance uuid by uuid %q: %w", uuid, err)
	}
	defer func() { _ = rows.Close() }()

	var out []FPPInstanceUUIDRecord
	for rows.Next() {
		rec, err := scanFPPInstanceUUID(rows)
		if err != nil {
			return nil, fmt.Errorf("store: get fpp instance uuid by uuid %q: %w", uuid, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: get fpp instance uuid by uuid %q: %w", uuid, err)
	}
	return out, nil
}

// GetFPPInstanceUUIDByUUID is the [Store] form of [getFPPInstanceUUIDByUUID].
func (s *Store) GetFPPInstanceUUIDByUUID(ctx context.Context, uuid string) ([]FPPInstanceUUIDRecord, error) {
	guardNotInTx(ctx, "Store.GetFPPInstanceUUIDByUUID")
	return getFPPInstanceUUIDByUUID(ctx, s.db, uuid)
}

// GetFPPInstanceUUIDByUUID is [Store.GetFPPInstanceUUIDByUUID]'s [Tx] form.
func (t *Tx) GetFPPInstanceUUIDByUUID(ctx context.Context, uuid string) ([]FPPInstanceUUIDRecord, error) {
	return getFPPInstanceUUIDByUUID(ctx, t.tx, uuid)
}

func recordFPPInstanceUUIDObservation(ctx context.Context, q querier, endpointID, uuid string, observedAt, now time.Time) (FPPInstanceUUIDRecord, bool, error) {
	existing, err := getFPPInstanceUUID(ctx, q, endpointID)
	switch {
	case errors.Is(err, ErrFPPInstanceUUIDNotFound):
		// First-ever observation for this endpoint: nothing to compare
		// against, so this can never itself be the changed-uuid rule's "changed" event.
		if _, err := q.ExecContext(ctx, `
			INSERT INTO fpp_instance_uuid_observations (
				endpoint_id, uuid, first_observed_at, last_observed_at,
				previous_uuid, changed_at,
				change_acknowledged_at, change_acknowledged_by_principal_id, change_acknowledged_by_principal_name,
				updated_at
			) VALUES (?, ?, ?, ?, '', '', '', '', '', ?)
		`, endpointID, uuid, timeToDB(observedAt), timeToDB(observedAt), timeToDB(now)); err != nil {
			return FPPInstanceUUIDRecord{}, false, fmt.Errorf("store: record fpp instance uuid observation for %q: %w", endpointID, err)
		}
		rec, err := getFPPInstanceUUID(ctx, q, endpointID)
		return rec, false, err
	case err != nil:
		return FPPInstanceUUIDRecord{}, false, err
	}

	if existing.UUID == uuid {
		// Same uuid reported again: advance LastObservedAt only, leaving
		// any pending unacknowledged change (the changed-uuid rule) exactly as it was ,
		// re-observing the current value is not itself new evidence about
		// the earlier transition.
		if _, err := q.ExecContext(ctx, `
			UPDATE fpp_instance_uuid_observations
			SET last_observed_at = ?, updated_at = ?
			WHERE endpoint_id = ?
		`, timeToDB(observedAt), timeToDB(now), endpointID); err != nil {
			return FPPInstanceUUIDRecord{}, false, fmt.Errorf("store: record fpp instance uuid observation for %q: %w", endpointID, err)
		}
		rec, err := getFPPInstanceUUID(ctx, q, endpointID)
		return rec, false, err
	}

	// The endpoint is reporting a DIFFERENT uuid than it last reported.
	// Rule 1: record the new value as current (an observation this
	// package refused to store would just make every OTHER fact about
	// this endpoint stale for no benefit), but keep the prior uuid
	// visible as an unacknowledged change until an operator explicitly
	// clears it, never a silent re-association. A change while an
	// earlier change is still unacknowledged overwrites PreviousUUID/
	// ChangedAt with THIS transition (the most recent one is what an
	// operator needs to act on) and blanks any earlier acknowledgment,
	// since that acknowledgment spoke to a transition this one has
	// already superseded.
	if _, err := q.ExecContext(ctx, `
		UPDATE fpp_instance_uuid_observations
		SET uuid = ?, last_observed_at = ?,
		    previous_uuid = ?, changed_at = ?,
		    change_acknowledged_at = '', change_acknowledged_by_principal_id = '', change_acknowledged_by_principal_name = '',
		    updated_at = ?
		WHERE endpoint_id = ?
	`, uuid, timeToDB(observedAt), existing.UUID, timeToDB(observedAt), timeToDB(now), endpointID); err != nil {
		return FPPInstanceUUIDRecord{}, false, fmt.Errorf("store: record fpp instance uuid observation for %q: %w", endpointID, err)
	}
	rec, err := getFPPInstanceUUID(ctx, q, endpointID)
	return rec, true, err
}

// RecordFPPInstanceUUIDObservation records that endpointID reported uuid
// at observedAt. The returned bool is true exactly when this call just
// recorded a NEW unacknowledged change (the changed-uuid rule), the caller (the FPP
// collector sink) uses it only to decide whether to log the transition;
// nothing about persistence depends on it.
func (s *Store) RecordFPPInstanceUUIDObservation(ctx context.Context, endpointID, uuid string, observedAt time.Time) (FPPInstanceUUIDRecord, bool, error) {
	guardNotInTx(ctx, "Store.RecordFPPInstanceUUIDObservation")
	return recordFPPInstanceUUIDObservation(ctx, s.db, endpointID, uuid, observedAt, s.now())
}

// RecordFPPInstanceUUIDObservation is
// [Store.RecordFPPInstanceUUIDObservation]'s [Tx] form.
func (t *Tx) RecordFPPInstanceUUIDObservation(ctx context.Context, endpointID, uuid string, observedAt time.Time) (FPPInstanceUUIDRecord, bool, error) {
	return recordFPPInstanceUUIDObservation(ctx, t.tx, endpointID, uuid, observedAt, t.s.now())
}

func acknowledgeFPPInstanceUUIDChange(ctx context.Context, q querier, endpointID, principalID, principalName string, now time.Time) (FPPInstanceUUIDRecord, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE fpp_instance_uuid_observations
		SET previous_uuid = '', changed_at = '',
		    change_acknowledged_at = ?, change_acknowledged_by_principal_id = ?, change_acknowledged_by_principal_name = ?,
		    updated_at = ?
		WHERE endpoint_id = ? AND previous_uuid != ''
	`, timeToDB(now), principalID, principalName, timeToDB(now), endpointID)
	if err != nil {
		return FPPInstanceUUIDRecord{}, fmt.Errorf("store: acknowledge fpp instance uuid change for %q: %w", endpointID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return FPPInstanceUUIDRecord{}, fmt.Errorf("store: acknowledge fpp instance uuid change for %q: %w", endpointID, err)
	}
	if n == 0 {
		// Either endpointID has never reported a uuid at all, or it has
		// no PENDING change to acknowledge, [getFPPInstanceUUID]
		// distinguishes those for the caller's error message, but both
		// refuse identically: acknowledging a non-conflict is refused
		// rather than a silent no-op, so a caller cannot mistake a stale
		// or mistargeted request for one that actually cleared something.
		if _, err := getFPPInstanceUUID(ctx, q, endpointID); errors.Is(err, ErrFPPInstanceUUIDNotFound) {
			return FPPInstanceUUIDRecord{}, ErrFPPInstanceUUIDNotFound
		}
		return FPPInstanceUUIDRecord{}, ErrFPPInstanceUUIDNoUnacknowledgedChange
	}
	return getFPPInstanceUUID(ctx, q, endpointID)
}

// AcknowledgeFPPInstanceUUIDChange clears endpointID's pending
// unacknowledged UUID change (the changed-uuid rule) and records who cleared it and
// when. It refuses with [ErrFPPInstanceUUIDNoUnacknowledgedChange] when
// there is nothing to acknowledge, and with [ErrFPPInstanceUUIDNotFound]
// when endpointID has never reported a uuid at all. This never changes
// UUID itself, the current uuid this endpoint most recently reported is
// untouched, it only clears the conflict marker rule 1 raised.
func (s *Store) AcknowledgeFPPInstanceUUIDChange(ctx context.Context, endpointID, principalID, principalName string) (FPPInstanceUUIDRecord, error) {
	guardNotInTx(ctx, "Store.AcknowledgeFPPInstanceUUIDChange")
	return acknowledgeFPPInstanceUUIDChange(ctx, s.db, endpointID, principalID, principalName, s.now())
}

// AcknowledgeFPPInstanceUUIDChange is
// [Store.AcknowledgeFPPInstanceUUIDChange]'s [Tx] form.
func (t *Tx) AcknowledgeFPPInstanceUUIDChange(ctx context.Context, endpointID, principalID, principalName string) (FPPInstanceUUIDRecord, error) {
	return acknowledgeFPPInstanceUUIDChange(ctx, t.tx, endpointID, principalID, principalName, t.s.now())
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Observation value-kind discriminators. See schemaV3's doc comment in
// migrations.go for why the observations table needs a discriminator at
// all rather than one JSON or NUMERIC column, and [encodeObservationValue]/
// [decodeObservationValue] for the encoding these values select between.
// valueKindNone is the zero value and is never written for a row that
// carries a value — see [observation.Observation.Validate], which already
// guarantees Value and Absence are never both set, so a stored row's
// value_kind is either exactly one of the four below, or empty because the
// row is an absence.
const (
	valueKindNone    = ""
	valueKindBool    = "bool"
	valueKindString  = "string"
	valueKindInt64   = "int64"
	valueKindFloat64 = "float64"
)

// encodeObservationValue converts an [observation.Observation.Value] (nil,
// bool, string, int64, or float64 — [observation.Observation.Validate]
// rejects everything else before this is ever called) into the
// (value_kind, value_text) pair the observations table stores.
//
// Each case is chosen specifically to round-trip exactly back through
// [decodeObservationValue], which is the property this package's tests
// pin (see observations_test.go):
//
//   - int64 uses strconv.FormatInt/ParseInt in base 10, never a float
//     conversion, so a value like math.MaxInt64 (which cannot be
//     represented exactly as a float64 — 2^63-1 has no exact float64
//     representation) never loses a bit.
//   - float64 uses strconv.FormatFloat with precision -1 ('g' format),
//     which Go's strconv documents as using "the smallest number of
//     digits necessary such that ParseFloat will return f exactly" — the
//     specific guarantee this encoding depends on for an integral-valued
//     float (e.g. 1920.0) to decode back as a float64 and not silently
//     become an int64 (value_kind alone prevents that ambiguity: decoding
//     never infers the type from value_text's shape).
//   - bool and string need no numeric-precision care, but still go through
//     the same discriminated path for one reason: a string value of ""
//     must be distinguishable from "no value" (value_kind == "" for a
//     genuine absence), which storing only value_text could never tell
//     apart on its own.
func encodeObservationValue(v any) (kind, text string, err error) {
	switch x := v.(type) {
	case nil:
		return valueKindNone, "", nil
	case bool:
		return valueKindBool, strconv.FormatBool(x), nil
	case string:
		return valueKindString, x, nil
	case int64:
		return valueKindInt64, strconv.FormatInt(x, 10), nil
	case float64:
		return valueKindFloat64, strconv.FormatFloat(x, 'g', -1, 64), nil
	default:
		// Unreachable through UpsertObservation, which calls
		// observation.Observation.Validate first and Validate rejects
		// every other type. Handled anyway because this function's
		// signature does not know that, and a silent fallthrough to an
		// empty encoding would store a value as if it were an absence.
		return "", "", fmt.Errorf("store: observation value has unsupported type %T", v)
	}
}

// decodeObservationValue is [encodeObservationValue]'s inverse. It reads
// kind first and parses text according to that kind — it never infers a
// type from text's own shape — which is what lets an integral-valued
// float64 (kind "float64", text "1920") come back as a float64 rather than
// being mistaken for an int64, and what lets an empty string value (kind
// "string", text "") come back as "" rather than nil.
func decodeObservationValue(kind, text string) (any, error) {
	switch kind {
	case valueKindNone:
		return nil, nil
	case valueKindBool:
		v, err := strconv.ParseBool(text)
		if err != nil {
			return nil, fmt.Errorf("store: decode bool observation value %q: %w", text, err)
		}
		return v, nil
	case valueKindString:
		return text, nil
	case valueKindInt64:
		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("store: decode int64 observation value %q: %w", text, err)
		}
		return v, nil
	case valueKindFloat64:
		v, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("store: decode float64 observation value %q: %w", text, err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("store: observations row has unknown value_kind %q", kind)
	}
}

// UpsertObservation stores obs as the current evidence for its
// (Resource.Kind, Resource.ID, Signal, Source) — schemaV4's primary key,
// widened from schemaV3's (Resource.Kind, Resource.ID, Signal) specifically
// so two different collector sources reporting the same signal for the same
// resource coexist as two rows rather than one overwriting the other; see
// schemaV4's doc comment in migrations.go. A second UpsertObservation for
// the SAME source and the same (Resource.Kind, Resource.ID, Signal)
// replaces whatever that source previously stored, exactly as before —
// this is still an upsert target, never a history, per source. obs must
// satisfy [observation.Observation.Validate]; UpsertObservation calls
// Validate itself and returns its error, unwritten, rather than storing a
// caller-built Observation that violates the invariants pkg/observation
// exists to enforce — an invalid Observation reaching this method is
// always a caller bug (a collector that skipped the constructors and hand-
// built one wrong), never a condition this package should paper over.
//
// This method deliberately does not choose which source's row "wins" for a
// given (Resource.Kind, Resource.ID, Signal) — that resolution happens
// once, at read, via a documented pure function
// (internal/coordinator/api.ResolveObservations) over everything
// [Store.ListObservations] returns, per the Step 5 contract section 5.2.
// Discarding a losing source's evidence here, at write time, would destroy
// its provenance permanently and make that resolution rule untestable from
// outside the process — exactly what ADR-011 exists to prevent.
//
// obs.ObservedAt is stored exactly as given, including nil — see
// schemaV3's doc comment in migrations.go and contract section 3.3. Never
// "helpfully" default it to obs.CollectedAt here or anywhere else in this
// package.
func (s *Store) UpsertObservation(ctx context.Context, obs observation.Observation) error {
	guardNotInTx(ctx, "Store.UpsertObservation")
	if err := obs.Validate(); err != nil {
		return fmt.Errorf("store: upsert observation: %w", err)
	}
	if err := upsertObservation(ctx, s.db, obs, timeToDB(s.now())); err != nil {
		return fmt.Errorf("store: upsert observation %q for %s/%s: %w", obs.Signal, obs.Resource.Kind, obs.Resource.ID, err)
	}
	return nil
}

// upsertObservation is [Store.UpsertObservation]'s SQL, factored out so
// [Store.ReplaceObservations] can run the identical upsert for every
// observation it is given inside its own transaction (ex is a *sql.Tx
// there) rather than duplicating this statement. now is the caller's
// already-formatted [timeToDB] value, passed in rather than computed here,
// so every observation upserted within one ReplaceObservations call shares
// exactly the same updated_at, the same way a single UpsertObservation call
// always did.
//
// Callers must validate obs (via [observation.Observation.Validate])
// before calling this — it is not repeated here since ReplaceObservations
// validates every observation up front, before opening its transaction,
// and would rather fail the whole batch before touching the database than
// leave a transaction partially applied by a validation failure partway
// through.
func upsertObservation(ctx context.Context, ex execer, obs observation.Observation, now string) error {
	valueKind, valueText, err := encodeObservationValue(obs.Value)
	if err != nil {
		// Unreachable given Validate having already run (see callers), but
		// handled rather than ignored: see encodeObservationValue's doc
		// comment.
		return err
	}

	_, err = ex.ExecContext(ctx, `
		INSERT INTO observations (
			resource_kind, resource_id, signal,
			value_kind, value_text, unit,
			observed_at, collected_at, source, quality, valid_for_ns,
			absence, reason,
			first_seen_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(resource_kind, resource_id, signal, source) DO UPDATE SET
			value_kind   = excluded.value_kind,
			value_text   = excluded.value_text,
			unit         = excluded.unit,
			observed_at  = excluded.observed_at,
			collected_at = excluded.collected_at,
			quality      = excluded.quality,
			valid_for_ns = excluded.valid_for_ns,
			absence      = excluded.absence,
			reason       = excluded.reason,
			updated_at   = excluded.updated_at
	`,
		string(obs.Resource.Kind), obs.Resource.ID, string(obs.Signal),
		valueKind, valueText, obs.Unit,
		timePtrToDB(obs.ObservedAt), timeToDB(obs.CollectedAt), obs.Source, string(obs.Quality), int64(obs.ValidFor),
		string(obs.Absence), obs.Reason,
		now, now,
	)
	return err
}

// ReplaceObservations upserts every observation in observations (exactly as
// repeated [Store.UpsertObservation] calls would, and within one
// transaction so a caller never observes some of the batch applied and some
// not), and then, for each distinct (resource_kind, resource_id, source)
// triple actually present in observations, deletes any other stored row
// for that same triple whose signal is NOT among the ones just upserted.
//
// This is the mechanism behind the collector/Sink completeness contract
// (internal/coordinator/collector.Collector.Poll's complete return value,
// internal/coordinator/collector.Sink.RecordObservations's complete
// parameter): a caller must only pass this method a poll's FULL set of
// observations for whatever (resource, source) pairs it mentions —
// ReplaceObservations has no notion of "partial" and does not need one,
// because pruning is scoped per (resource_kind, resource_id, source)
// derived directly from what was actually delivered this call, never
// inferred from what was NOT delivered. A (resource, source) pair this
// call does not mention at all is left completely untouched: passing zero
// observations is a no-op, never "delete everything, for every resource
// and source, that this call did not re-assert" — a caller wanting a
// specific (resource, source) pair pruned must include at least one
// observation (an absence, if nothing else) naming it.
//
// This is what fixes the ghost-row defect a naive prune-on-absence would
// not: a previous poll's 48 per-port signals, followed by a poll reporting
// only 2 (the aggregate counts, ports now empty), leaves no per-port rows
// behind — the 2 delivered signals survive (they are in this call's
// upserted set) and the 46 undelivered ones are deleted, all inside one
// transaction, so no reader ever observes a half-pruned state.
//
// obs.Validate() is checked for every observation before ANY database work
// begins (see upsertObservation's doc comment for why): one invalid
// observation in the batch fails the whole call with nothing written,
// exactly as if UpsertObservation had been called for it directly.
func (s *Store) ReplaceObservations(ctx context.Context, observations []observation.Observation) error {
	guardNotInTx(ctx, "Store.ReplaceObservations")
	for i := range observations {
		if err := observations[i].Validate(); err != nil {
			return fmt.Errorf("store: replace observations: %w", err)
		}
	}
	if len(observations) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: replace observations: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit succeeds

	now := timeToDB(s.now())

	type resourceSource struct {
		kind, id, source string
	}
	// delivered tracks, per (resource_kind, resource_id, source), exactly
	// the signals this call is upserting for it — the set ReplaceObservations
	// then keeps, deleting everything else stored under that same triple.
	delivered := make(map[resourceSource]map[string]struct{})

	for _, obs := range observations {
		if err := upsertObservation(ctx, tx, obs, now); err != nil {
			return fmt.Errorf("store: replace observations: upsert %q for %s/%s: %w", obs.Signal, obs.Resource.Kind, obs.Resource.ID, err)
		}

		key := resourceSource{kind: string(obs.Resource.Kind), id: obs.Resource.ID, source: obs.Source}
		if delivered[key] == nil {
			delivered[key] = make(map[string]struct{})
		}
		delivered[key][string(obs.Signal)] = struct{}{}
	}

	for key, signals := range delivered {
		placeholders := make([]string, 0, len(signals))
		args := make([]any, 0, len(signals)+3)
		args = append(args, key.kind, key.id, key.source)
		for sig := range signals {
			placeholders = append(placeholders, "?")
			args = append(args, sig)
		}
		// signal NOT IN (...) over exactly the signals just upserted for
		// this (resource_kind, resource_id, source): every row this same
		// source previously stored for this resource that was NOT part of
		// this delivery is gone — a removed port, a removed sensor, or (per
		// the Step 5 review finding this fixes) an instance whose ports
		// dropped from 48 elements to none.
		query := fmt.Sprintf(`
			DELETE FROM observations
			WHERE resource_kind = ? AND resource_id = ? AND source = ?
			AND signal NOT IN (%s)
		`, strings.Join(placeholders, ","))
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("store: replace observations: prune %s/%s source %q: %w", key.kind, key.id, key.source, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: replace observations: commit: %w", err)
	}
	return nil
}

// ObservationFilter narrows [Store.ListObservations]. Every field is
// optional (empty means "match any"); a zero-value ObservationFilter
// matches every stored observation.
type ObservationFilter struct {
	ResourceKind observation.ResourceKind
	ResourceID   string
	Signal       observation.SignalID
}

const observationColumns = `
	resource_kind, resource_id, signal,
	value_kind, value_text, unit,
	observed_at, collected_at, source, quality, valid_for_ns,
	absence, reason
`

// scanObservation scans one row shaped by observationColumns into an
// [observation.Observation]. It builds the struct directly rather than
// through [observation.Measured]/[observation.MeasuredUnknownAge]/etc:
// those constructors stamp ObservedAt or CollectedAt from arguments the
// caller supplies, which is correct for a collector recording a fresh
// reading but wrong here — a stored row's ObservedAt (possibly nil) and
// CollectedAt are themselves the historical facts being restored, and
// restoring them through a constructor that has any opinion about "now"
// would risk exactly the restamping-on-read bug
// TestReopenedObservationPreservesObservedAtAndDerivesStale in
// observations_test.go exists to catch.
func scanObservation(row interface {
	Scan(dest ...any) error
}) (observation.Observation, error) {
	var (
		resourceKind, resourceID, signal string
		valueKind, valueText, unit       string
		observedAt                       sql.NullString
		collectedAt                      string
		source, quality                  string
		validForNS                       int64
		absence, reason                  string
	)
	if err := row.Scan(
		&resourceKind, &resourceID, &signal,
		&valueKind, &valueText, &unit,
		&observedAt, &collectedAt, &source, &quality, &validForNS,
		&absence, &reason,
	); err != nil {
		return observation.Observation{}, err
	}

	value, err := decodeObservationValue(valueKind, valueText)
	if err != nil {
		return observation.Observation{}, fmt.Errorf("store: scan observation %s/%s/%s: %w", resourceKind, resourceID, signal, err)
	}
	observedAtPtr, err := dbToTimePtr(observedAt)
	if err != nil {
		return observation.Observation{}, fmt.Errorf("store: scan observation %s/%s/%s: parse observed_at: %w", resourceKind, resourceID, signal, err)
	}
	collectedAtTime, err := dbToTime(collectedAt)
	if err != nil {
		return observation.Observation{}, fmt.Errorf("store: scan observation %s/%s/%s: parse collected_at: %w", resourceKind, resourceID, signal, err)
	}

	return observation.Observation{
		Resource:    observation.ResourceRef{Kind: observation.ResourceKind(resourceKind), ID: resourceID},
		Signal:      observation.SignalID(signal),
		Value:       value,
		Unit:        unit,
		ObservedAt:  observedAtPtr,
		CollectedAt: collectedAtTime,
		Source:      source,
		Quality:     observation.Quality(quality),
		ValidFor:    time.Duration(validForNS),
		Absence:     observation.State(absence),
		Reason:      reason,
	}, nil
}

// ListObservations returns every stored observation matching filter,
// ordered by resource kind, resource ID, then signal for a stable,
// deterministic result — the same ordering convention [Store.ListNodes]
// uses.
//
// Every returned Observation's ObservedAt is exactly what was last stored
// for it by [Store.UpsertObservation], including nil: ListObservations
// derives nothing and fabricates nothing. A caller that wants an
// [observation.Health] or an [observation.State] calls
// [observation.Observation.StateAt] (or
// [observation.DeriveHealth]) itself, against its own current time — this
// package has no clock opinion about what "current" means to an API
// response being rendered.
func (s *Store) ListObservations(ctx context.Context, filter ObservationFilter) ([]observation.Observation, error) {
	guardNotInTx(ctx, "Store.ListObservations")
	var clauses []string
	var args []any
	if filter.ResourceKind != "" {
		clauses = append(clauses, "resource_kind = ?")
		args = append(args, string(filter.ResourceKind))
	}
	if filter.ResourceID != "" {
		clauses = append(clauses, "resource_id = ?")
		args = append(args, filter.ResourceID)
	}
	if filter.Signal != "" {
		clauses = append(clauses, "signal = ?")
		args = append(args, string(filter.Signal))
	}

	query := "SELECT" + observationColumns + "FROM observations"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY resource_kind, resource_id, signal"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list observations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []observation.Observation
	for rows.Next() {
		obs, err := scanObservation(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list observations: %w", err)
		}
		out = append(out, obs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list observations: %w", err)
	}
	return out, nil
}

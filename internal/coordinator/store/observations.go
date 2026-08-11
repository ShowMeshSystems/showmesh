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
// (Resource.Kind, Resource.ID, Signal), replacing whatever was previously
// stored for that same triple. obs must satisfy
// [observation.Observation.Validate]; UpsertObservation calls Validate
// itself and returns its error, unwritten, rather than storing a
// caller-built Observation that violates the invariants pkg/observation
// exists to enforce — an invalid Observation reaching this method is
// always a caller bug (a collector that skipped the constructors and hand-
// built one wrong), never a condition this package should paper over.
//
// obs.ObservedAt is stored exactly as given, including nil — see
// schemaV3's doc comment in migrations.go and contract section 3.3. Never
// "helpfully" default it to obs.CollectedAt here or anywhere else in this
// package.
func (s *Store) UpsertObservation(ctx context.Context, obs observation.Observation) error {
	if err := obs.Validate(); err != nil {
		return fmt.Errorf("store: upsert observation: %w", err)
	}

	valueKind, valueText, err := encodeObservationValue(obs.Value)
	if err != nil {
		// Unreachable given the Validate call above, but handled rather
		// than ignored: see encodeObservationValue's doc comment.
		return fmt.Errorf("store: upsert observation: %w", err)
	}

	now := timeToDB(s.now())
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO observations (
			resource_kind, resource_id, signal,
			value_kind, value_text, unit,
			observed_at, collected_at, source, quality, valid_for_ns,
			absence, reason,
			first_seen_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(resource_kind, resource_id, signal) DO UPDATE SET
			value_kind   = excluded.value_kind,
			value_text   = excluded.value_text,
			unit         = excluded.unit,
			observed_at  = excluded.observed_at,
			collected_at = excluded.collected_at,
			source       = excluded.source,
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
	if err != nil {
		return fmt.Errorf("store: upsert observation %q for %s/%s: %w", obs.Signal, obs.Resource.Kind, obs.Resource.ID, err)
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

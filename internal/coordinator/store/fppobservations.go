package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// fpp_playlist_entry_observations repository methods (schemaV14, FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1).
// Store and Tx forms share one SQL text each via the querier interface
// (tx.go), matching nightsession.go's pattern next door. See
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1 for the wire contract this table
// serves and §1.5/§1.6 for the monotonicity rule enforced here.

// FPPPlaylistEntryObservationRecord is the latest accepted observation for
// one FPP instance, one row per instance_uuid, never a history (see
// schemaV14's doc comment). observed_at_millis is INTEGER because the
// wire carries epoch milliseconds (§1.2); ReceivedAt uses this package's
// usual TEXT/RFC3339Nano convention instead, since it is package
// bookkeeping, not a value carried on the wire.
type FPPPlaylistEntryObservationRecord struct {
	InstanceUUID                       string
	SchemaVersion                      int64
	Sequence                           int64
	BodyHash                           string
	ObservationJSON                    string
	PlaylistName                       string
	PlaylistHash                       string
	Section                            string
	Position                           int64
	EntryKey                           string
	SequenceFilename                   string
	MediaFilename                      string
	Action                             string
	Unavailable                        string
	ObservedAt                         time.Time
	CoalescedSincePreviousAcknowledged int64
	ReceivedAt                         time.Time

	// EntryOccurrenceSequence is entry-START identity (schemaV17's own doc
	// comment): stable across repeat ticks inside one entry occurrence,
	// and strictly newer on every genuine re-entry, including a playlist
	// looping back to an entry whose EntryKey is otherwise identical to
	// its first visit. Computed at ingestion
	// (fppobservations.go's handlePostFPPPlaylistEntryObservation), never
	// read off the wire — the plugin never sends it.
	// [cueactivate.activationID] hashes it precisely so two ticks inside
	// one occurrence dedup to one dispatch while a loop's second lap
	// dispatches again.
	EntryOccurrenceSequence int64
}

// ErrFPPPlaylistEntryObservationNotFound is returned when no row matches.
var ErrFPPPlaylistEntryObservationNotFound = errors.New("store: fpp playlist entry observation not found")

// ErrFPPPlaylistEntryObservationStale is returned by
// [Store.PutFPPPlaylistEntryObservation]/[Tx.PutFPPPlaylistEntryObservation]
// when rec.Sequence is lower than the instance's stored sequence (§1.5's
// regression case), the stored row is left untouched.
var ErrFPPPlaylistEntryObservationStale = errors.New("store: fpp playlist entry observation sequence is stale")

// ErrFPPPlaylistEntryObservationSequenceConflict is returned when
// rec.Sequence equals the instance's stored sequence (§1.6 step 9's
// "equal and the body differs" case, distinguishing an idempotent replay
// from a genuine conflict at the same sequence is the CALLER's job, by
// comparing BodyHash against the row [Store.GetFPPPlaylistEntryObservation]
// returns, before ever calling Put), the stored row is left untouched.
var ErrFPPPlaylistEntryObservationSequenceConflict = errors.New("store: fpp playlist entry observation sequence conflict")

const fppPlaylistEntryObservationColumns = `
	instance_uuid, schema_version, sequence, body_hash, observation_json,
	playlist_name, playlist_hash, section, position, entry_key,
	sequence_filename, media_filename, action, unavailable,
	observed_at_millis, coalesced_since_previous_acknowledged, received_at,
	entry_occurrence_sequence
`

func scanFPPPlaylistEntryObservation(row interface{ Scan(dest ...any) error }) (FPPPlaylistEntryObservationRecord, error) {
	var (
		rec              FPPPlaylistEntryObservationRecord
		observedAtMillis int64
		receivedAt       string
	)
	if err := row.Scan(
		&rec.InstanceUUID, &rec.SchemaVersion, &rec.Sequence, &rec.BodyHash, &rec.ObservationJSON,
		&rec.PlaylistName, &rec.PlaylistHash, &rec.Section, &rec.Position, &rec.EntryKey,
		&rec.SequenceFilename, &rec.MediaFilename, &rec.Action, &rec.Unavailable,
		&observedAtMillis, &rec.CoalescedSincePreviousAcknowledged, &receivedAt,
		&rec.EntryOccurrenceSequence,
	); err != nil {
		return FPPPlaylistEntryObservationRecord{}, err
	}
	rec.ObservedAt = time.UnixMilli(observedAtMillis).UTC()
	var err error
	if rec.ReceivedAt, err = dbToTime(receivedAt); err != nil {
		return FPPPlaylistEntryObservationRecord{}, fmt.Errorf("store: parse fpp playlist entry observation received_at: %w", err)
	}
	return rec, nil
}

func getFPPPlaylistEntryObservation(ctx context.Context, q querier, instanceUUID string) (FPPPlaylistEntryObservationRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT `+fppPlaylistEntryObservationColumns+` FROM fpp_playlist_entry_observations WHERE instance_uuid = ?`, instanceUUID)
	rec, err := scanFPPPlaylistEntryObservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FPPPlaylistEntryObservationRecord{}, ErrFPPPlaylistEntryObservationNotFound
	}
	if err != nil {
		return FPPPlaylistEntryObservationRecord{}, fmt.Errorf("store: get fpp playlist entry observation %q: %w", instanceUUID, err)
	}
	return rec, nil
}

// GetFPPPlaylistEntryObservation returns the latest accepted observation
// for one instance, or [ErrFPPPlaylistEntryObservationNotFound].
func (s *Store) GetFPPPlaylistEntryObservation(ctx context.Context, instanceUUID string) (FPPPlaylistEntryObservationRecord, error) {
	guardNotInTx(ctx, "Store.GetFPPPlaylistEntryObservation")
	return getFPPPlaylistEntryObservation(ctx, s.db, instanceUUID)
}

// GetFPPPlaylistEntryObservation is [Store.GetFPPPlaylistEntryObservation]'s [Tx] form.
func (t *Tx) GetFPPPlaylistEntryObservation(ctx context.Context, instanceUUID string) (FPPPlaylistEntryObservationRecord, error) {
	return getFPPPlaylistEntryObservation(ctx, t.tx, instanceUUID)
}

func listFPPPlaylistEntryObservations(ctx context.Context, q querier) ([]FPPPlaylistEntryObservationRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+fppPlaylistEntryObservationColumns+` FROM fpp_playlist_entry_observations ORDER BY instance_uuid ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list fpp playlist entry observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []FPPPlaylistEntryObservationRecord
	for rows.Next() {
		rec, err := scanFPPPlaylistEntryObservation(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan fpp playlist entry observation: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list fpp playlist entry observations: %w", err)
	}
	return out, nil
}

// ListFPPPlaylistEntryObservations returns every instance's latest
// observation, ordered by instance_uuid so the output is stable, this is
// §1.1's GET endpoint's own read.
func (s *Store) ListFPPPlaylistEntryObservations(ctx context.Context) ([]FPPPlaylistEntryObservationRecord, error) {
	guardNotInTx(ctx, "Store.ListFPPPlaylistEntryObservations")
	return listFPPPlaylistEntryObservations(ctx, s.db)
}

// ListFPPPlaylistEntryObservations is [Store.ListFPPPlaylistEntryObservations]'s [Tx] form.
func (t *Tx) ListFPPPlaylistEntryObservations(ctx context.Context) ([]FPPPlaylistEntryObservationRecord, error) {
	return listFPPPlaylistEntryObservations(ctx, t.tx)
}

// putFPPPlaylistEntryObservation reads the instance's currently stored
// sequence and decides whether to accept rec, inside the same querier q,
// never a read on one connection followed by a write on another. This
// package's connection pool is capped at exactly one connection, which is
// what makes that read-then-write serializable even for [Store]'s own
// transaction (a second, concurrent caller's BeginTx simply blocks until
// the first commits), matching queries.go's RecordHealth precedent.
func putFPPPlaylistEntryObservation(ctx context.Context, q querier, rec FPPPlaylistEntryObservationRecord) error {
	var existingSeq int64
	switch err := q.QueryRowContext(ctx, `SELECT sequence FROM fpp_playlist_entry_observations WHERE instance_uuid = ?`, rec.InstanceUUID).Scan(&existingSeq); {
	case errors.Is(err, sql.ErrNoRows):
		// No prior observation for this instance: nothing to compare against.
	case err != nil:
		return fmt.Errorf("store: read existing sequence for fpp playlist entry observation %q: %w", rec.InstanceUUID, err)
	case rec.Sequence < existingSeq:
		return ErrFPPPlaylistEntryObservationStale
	case rec.Sequence == existingSeq:
		return ErrFPPPlaylistEntryObservationSequenceConflict
	}

	_, err := q.ExecContext(ctx, `
		INSERT INTO fpp_playlist_entry_observations (`+fppPlaylistEntryObservationColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_uuid) DO UPDATE SET
			schema_version    = excluded.schema_version,
			sequence          = excluded.sequence,
			body_hash         = excluded.body_hash,
			observation_json  = excluded.observation_json,
			playlist_name     = excluded.playlist_name,
			playlist_hash     = excluded.playlist_hash,
			section           = excluded.section,
			position          = excluded.position,
			entry_key         = excluded.entry_key,
			sequence_filename = excluded.sequence_filename,
			media_filename    = excluded.media_filename,
			action            = excluded.action,
			unavailable       = excluded.unavailable,
			observed_at_millis = excluded.observed_at_millis,
			coalesced_since_previous_acknowledged = excluded.coalesced_since_previous_acknowledged,
			received_at       = excluded.received_at,
			entry_occurrence_sequence = excluded.entry_occurrence_sequence
	`,
		rec.InstanceUUID, rec.SchemaVersion, rec.Sequence, rec.BodyHash, rec.ObservationJSON,
		rec.PlaylistName, rec.PlaylistHash, rec.Section, rec.Position, rec.EntryKey,
		rec.SequenceFilename, rec.MediaFilename, rec.Action, rec.Unavailable,
		rec.ObservedAt.UnixMilli(), rec.CoalescedSincePreviousAcknowledged, timeToDB(rec.ReceivedAt),
		rec.EntryOccurrenceSequence,
	)
	if err != nil {
		return fmt.Errorf("store: put fpp playlist entry observation %q: %w", rec.InstanceUUID, err)
	}
	return nil
}

// PutFPPPlaylistEntryObservation upserts the latest observation for one
// instance. It refuses to move the stored sequence backwards or to
// overwrite a row at the same sequence, see
// [ErrFPPPlaylistEntryObservationStale]/[ErrFPPPlaylistEntryObservationSequenceConflict]
// so the monotonicity rule cannot be bypassed by a caller that forgot to
// check first.
func (s *Store) PutFPPPlaylistEntryObservation(ctx context.Context, rec FPPPlaylistEntryObservationRecord) error {
	guardNotInTx(ctx, "Store.PutFPPPlaylistEntryObservation")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin put fpp playlist entry observation %q: %w", rec.InstanceUUID, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit succeeds

	if err := putFPPPlaylistEntryObservation(ctx, tx, rec); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit put fpp playlist entry observation %q: %w", rec.InstanceUUID, err)
	}
	return nil
}

// PutFPPPlaylistEntryObservation is [Store.PutFPPPlaylistEntryObservation]'s
// [Tx] form, a caller inside identity.AuditedWrite's transaction (§1.6's
// refusal-audit requirement) composes its own write with this one.
func (t *Tx) PutFPPPlaylistEntryObservation(ctx context.Context, rec FPPPlaylistEntryObservationRecord) error {
	return putFPPPlaylistEntryObservation(ctx, t.tx, rec)
}

func deleteFPPPlaylistEntryObservation(ctx context.Context, q querier, instanceUUID string) (bool, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM fpp_playlist_entry_observations WHERE instance_uuid = ?`, instanceUUID)
	if err != nil {
		return false, fmt.Errorf("store: delete fpp playlist entry observation %q: %w", instanceUUID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: delete fpp playlist entry observation %q: rows affected: %w", instanceUUID, err)
	}
	return n > 0, nil
}

// DeleteFPPPlaylistEntryObservation removes one instance's row and reports
// whether a row was actually deleted. This is the recovery path §1.5
// names for a plugin that lost its persisted sequence: "the stored
// per-instance sequence is cleared only by an explicit, authenticated
// operator action."
func (s *Store) DeleteFPPPlaylistEntryObservation(ctx context.Context, instanceUUID string) (bool, error) {
	guardNotInTx(ctx, "Store.DeleteFPPPlaylistEntryObservation")
	return deleteFPPPlaylistEntryObservation(ctx, s.db, instanceUUID)
}

// DeleteFPPPlaylistEntryObservation is [Store.DeleteFPPPlaylistEntryObservation]'s [Tx] form.
func (t *Tx) DeleteFPPPlaylistEntryObservation(ctx context.Context, instanceUUID string) (bool, error) {
	return deleteFPPPlaylistEntryObservation(ctx, t.tx, instanceUUID)
}

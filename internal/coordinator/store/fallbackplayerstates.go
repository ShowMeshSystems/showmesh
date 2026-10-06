package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// schemaV48 adds fallback_player_states: the latest state report an FPP
// plugin sent for its player, one row per FPP instance (ADR-048 decision 4).
const schemaV48 = `
CREATE TABLE IF NOT EXISTS fallback_player_states (
    fpp_instance_uuid            TEXT PRIMARY KEY,
    boot_id                      TEXT NOT NULL,
    sequence                     INTEGER NOT NULL,
    state                        TEXT NOT NULL,
    since                        TEXT NOT NULL,
    playlist_name                TEXT NOT NULL DEFAULT '',
    package_id                   TEXT NOT NULL DEFAULT '',
    package_revision             TEXT NOT NULL DEFAULT '',
    cutoff_at                    TEXT NOT NULL DEFAULT '',
    received_at                  TEXT NOT NULL,
    state_changed_at             TEXT NOT NULL,
    observations_ignored_through TEXT,
    ack_wait_since               TEXT
);
`

// FallbackPlayerStateRecord is the latest state report from one FPP
// player's plugin, plus what the coordinator derived when it arrived.
type FallbackPlayerStateRecord struct {
	FPPInstanceUUID string
	BootID          string
	Sequence        int64
	State           string
	Since           time.Time
	PlaylistName    string
	PackageID       string
	PackageRevision string
	// CutoffAt is the reported program expiry, kept as the plugin wrote it.
	CutoffAt   string
	ReceivedAt time.Time
	// StateChangedAt is when the stored State last became a different word.
	StateChangedAt time.Time
	// ObservationsIgnoredThrough is zero when no observation is ignored.
	ObservationsIgnoredThrough time.Time
	// AckWaitSince is zero unless the coordinator is waiting for this
	// player's program acknowledgement after a hand-back.
	AckWaitSince time.Time
}

// ErrFallbackPlayerStateNotFound means the player's plugin never reported.
var ErrFallbackPlayerStateNotFound = errors.New("store: fallback player state not found")

const fallbackPlayerStateColumns = `fpp_instance_uuid, boot_id, sequence, state, since, playlist_name, package_id,
	package_revision, cutoff_at, received_at, state_changed_at, observations_ignored_through, ack_wait_since`

func nullableTimeToDB(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return timeToDB(t)
}

func nullableDBToTime(v sql.NullString) (time.Time, error) {
	if !v.Valid || v.String == "" {
		return time.Time{}, nil
	}
	return dbToTime(v.String)
}

func putFallbackPlayerState(ctx context.Context, q querier, rec FallbackPlayerStateRecord) error {
	if rec.FPPInstanceUUID == "" {
		return fmt.Errorf("store: put fallback player state: fppInstanceUUID is empty")
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO fallback_player_states (`+fallbackPlayerStateColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fpp_instance_uuid) DO UPDATE SET
			boot_id                      = excluded.boot_id,
			sequence                     = excluded.sequence,
			state                        = excluded.state,
			since                        = excluded.since,
			playlist_name                = excluded.playlist_name,
			package_id                   = excluded.package_id,
			package_revision             = excluded.package_revision,
			cutoff_at                    = excluded.cutoff_at,
			received_at                  = excluded.received_at,
			state_changed_at             = excluded.state_changed_at,
			observations_ignored_through = excluded.observations_ignored_through,
			ack_wait_since               = excluded.ack_wait_since
	`, rec.FPPInstanceUUID, rec.BootID, rec.Sequence, rec.State, timeToDB(rec.Since), rec.PlaylistName, rec.PackageID,
		rec.PackageRevision, rec.CutoffAt, timeToDB(rec.ReceivedAt), timeToDB(rec.StateChangedAt),
		nullableTimeToDB(rec.ObservationsIgnoredThrough), nullableTimeToDB(rec.AckWaitSince)); err != nil {
		return fmt.Errorf("store: put fallback player state %q: %w", rec.FPPInstanceUUID, err)
	}
	return nil
}

// PutFallbackPlayerState stores rec as the player's only state row.
func (s *Store) PutFallbackPlayerState(ctx context.Context, rec FallbackPlayerStateRecord) error {
	guardNotInTx(ctx, "Store.PutFallbackPlayerState")
	return putFallbackPlayerState(ctx, s.db, rec)
}

// PutFallbackPlayerState is the transaction-scoped form.
func (t *Tx) PutFallbackPlayerState(ctx context.Context, rec FallbackPlayerStateRecord) error {
	return putFallbackPlayerState(ctx, t.tx, rec)
}

func scanFallbackPlayerState(row interface{ Scan(dest ...any) error }) (FallbackPlayerStateRecord, error) {
	var (
		rec                                 FallbackPlayerStateRecord
		since, receivedAt, stateChangedAt   string
		observationsIgnoredThrough, ackWait sql.NullString
	)
	if err := row.Scan(&rec.FPPInstanceUUID, &rec.BootID, &rec.Sequence, &rec.State, &since, &rec.PlaylistName, &rec.PackageID,
		&rec.PackageRevision, &rec.CutoffAt, &receivedAt, &stateChangedAt, &observationsIgnoredThrough, &ackWait); err != nil {
		return FallbackPlayerStateRecord{}, err
	}
	var err error
	if rec.Since, err = dbToTime(since); err != nil {
		return FallbackPlayerStateRecord{}, fmt.Errorf("store: parse fallback player state since: %w", err)
	}
	if rec.ReceivedAt, err = dbToTime(receivedAt); err != nil {
		return FallbackPlayerStateRecord{}, fmt.Errorf("store: parse fallback player state received_at: %w", err)
	}
	if rec.StateChangedAt, err = dbToTime(stateChangedAt); err != nil {
		return FallbackPlayerStateRecord{}, fmt.Errorf("store: parse fallback player state state_changed_at: %w", err)
	}
	if rec.ObservationsIgnoredThrough, err = nullableDBToTime(observationsIgnoredThrough); err != nil {
		return FallbackPlayerStateRecord{}, fmt.Errorf("store: parse fallback player state observations_ignored_through: %w", err)
	}
	if rec.AckWaitSince, err = nullableDBToTime(ackWait); err != nil {
		return FallbackPlayerStateRecord{}, fmt.Errorf("store: parse fallback player state ack_wait_since: %w", err)
	}
	return rec, nil
}

func getFallbackPlayerState(ctx context.Context, q querier, instanceUUID string) (FallbackPlayerStateRecord, error) {
	rec, err := scanFallbackPlayerState(q.QueryRowContext(ctx,
		`SELECT `+fallbackPlayerStateColumns+` FROM fallback_player_states WHERE fpp_instance_uuid = ?`, instanceUUID))
	if errors.Is(err, sql.ErrNoRows) {
		return FallbackPlayerStateRecord{}, ErrFallbackPlayerStateNotFound
	}
	if err != nil {
		return FallbackPlayerStateRecord{}, fmt.Errorf("store: get fallback player state %q: %w", instanceUUID, err)
	}
	return rec, nil
}

// GetFallbackPlayerState returns the player's latest report, or
// [ErrFallbackPlayerStateNotFound].
func (s *Store) GetFallbackPlayerState(ctx context.Context, instanceUUID string) (FallbackPlayerStateRecord, error) {
	guardNotInTx(ctx, "Store.GetFallbackPlayerState")
	return getFallbackPlayerState(ctx, s.db, instanceUUID)
}

// GetFallbackPlayerState is the transaction-scoped form.
func (t *Tx) GetFallbackPlayerState(ctx context.Context, instanceUUID string) (FallbackPlayerStateRecord, error) {
	return getFallbackPlayerState(ctx, t.tx, instanceUUID)
}

// ListFallbackPlayerStates returns every player's latest report.
func (s *Store) ListFallbackPlayerStates(ctx context.Context) ([]FallbackPlayerStateRecord, error) {
	guardNotInTx(ctx, "Store.ListFallbackPlayerStates")
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+fallbackPlayerStateColumns+` FROM fallback_player_states ORDER BY fpp_instance_uuid ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list fallback player states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []FallbackPlayerStateRecord
	for rows.Next() {
		rec, err := scanFallbackPlayerState(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan fallback player state: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list fallback player states: %w", err)
	}
	return out, nil
}

// ClearFallbackPlayerStateAckWait ends the wait for a program
// acknowledgement, only while the row still carries the wait it was read with.
func (s *Store) ClearFallbackPlayerStateAckWait(ctx context.Context, instanceUUID string, waitSince time.Time) error {
	guardNotInTx(ctx, "Store.ClearFallbackPlayerStateAckWait")
	if _, err := s.db.ExecContext(ctx,
		`UPDATE fallback_player_states SET ack_wait_since = NULL WHERE fpp_instance_uuid = ? AND ack_wait_since = ?`,
		instanceUUID, timeToDB(waitSince)); err != nil {
		return fmt.Errorf("store: clear fallback player state ack wait %q: %w", instanceUUID, err)
	}
	return nil
}

// DeleteFallbackPlayerState forgets the player's report and says whether
// there was one.
func (t *Tx) DeleteFallbackPlayerState(ctx context.Context, instanceUUID string) (bool, error) {
	res, err := t.tx.ExecContext(ctx, `DELETE FROM fallback_player_states WHERE fpp_instance_uuid = ?`, instanceUUID)
	if err != nil {
		return false, fmt.Errorf("store: delete fallback player state %q: %w", instanceUUID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: delete fallback player state %q: %w", instanceUUID, err)
	}
	return n > 0, nil
}

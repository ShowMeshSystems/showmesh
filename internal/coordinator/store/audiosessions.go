package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV9's audio_sessions repository methods: the
// coordinator's own durable record of each session's last known desired
// state and revision, per that migration's doc comment.

// AudioSessionRecord is one row of the audio_sessions table.
// DesiredJSON is an opaque, caller-owned encoding of
// pkg/audio.SessionDesiredState — this package stores bytes, never
// decodes them, matching commands.go's identical treatment of
// ParamsJSON/ResultJSON.
type AudioSessionRecord struct {
	ID          string
	NodeID      string
	DesiredJSON string
	Revision    uint64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ErrAudioSessionNotFound is returned by [Store.GetAudioSession] when no
// row exists for the given id.
var ErrAudioSessionNotFound = errors.New("store: audio session not found")

const audioSessionColumns = `id, node_id, desired_json, revision, created_at, updated_at`

func scanAudioSession(row interface{ Scan(dest ...any) error }) (AudioSessionRecord, error) {
	var (
		rec                  AudioSessionRecord
		createdAt, updatedAt string
	)
	if err := row.Scan(&rec.ID, &rec.NodeID, &rec.DesiredJSON, &rec.Revision, &createdAt, &updatedAt); err != nil {
		return AudioSessionRecord{}, err
	}
	var err error
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return AudioSessionRecord{}, fmt.Errorf("store: parse audio session created_at: %w", err)
	}
	if rec.UpdatedAt, err = dbToTime(updatedAt); err != nil {
		return AudioSessionRecord{}, fmt.Errorf("store: parse audio session updated_at: %w", err)
	}
	return rec, nil
}

// PutAudioSession creates or replaces id's durable record (INSERT ... ON
// CONFLICT), setting UpdatedAt to now and preserving the original
// CreatedAt across an update. This is the coordinator's own mirror of
// [pkg/audio.RevisionState]'s "revision only advances" rule — enforced by
// the caller (the dispatch layer, which reads-before-writing under its
// own request serialization for one session), not by this method, which
// unconditionally stores whatever it is given.
func (s *Store) PutAudioSession(ctx context.Context, rec AudioSessionRecord) error {
	guardNotInTx(ctx, "Store.PutAudioSession")
	now := s.now()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audio_sessions (id, node_id, desired_json, revision, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			node_id = excluded.node_id,
			desired_json = excluded.desired_json,
			revision = excluded.revision,
			updated_at = excluded.updated_at
	`, rec.ID, rec.NodeID, rec.DesiredJSON, rec.Revision, timeToDB(now), timeToDB(now))
	if err != nil {
		return fmt.Errorf("store: put audio session %q: %w", rec.ID, err)
	}
	return nil
}

// GetAudioSession returns one session by id, or [ErrAudioSessionNotFound].
func (s *Store) GetAudioSession(ctx context.Context, id string) (AudioSessionRecord, error) {
	guardNotInTx(ctx, "Store.GetAudioSession")
	row := s.db.QueryRowContext(ctx, `SELECT `+audioSessionColumns+` FROM audio_sessions WHERE id = ?`, id)
	rec, err := scanAudioSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AudioSessionRecord{}, ErrAudioSessionNotFound
	}
	if err != nil {
		return AudioSessionRecord{}, fmt.Errorf("store: get audio session %q: %w", id, err)
	}
	return rec, nil
}

// ListAudioSessionsByNode returns every session record for nodeID, newest
// first.
func (s *Store) ListAudioSessionsByNode(ctx context.Context, nodeID string) ([]AudioSessionRecord, error) {
	guardNotInTx(ctx, "Store.ListAudioSessionsByNode")
	rows, err := s.db.QueryContext(ctx, `SELECT `+audioSessionColumns+` FROM audio_sessions WHERE node_id = ? ORDER BY updated_at DESC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("store: list audio sessions for node %q: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []AudioSessionRecord
	for rows.Next() {
		rec, err := scanAudioSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list audio sessions for node %q: %w", nodeID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list audio sessions for node %q: %w", nodeID, err)
	}
	return out, nil
}

// DeleteAudioSession removes id's record. Deleting an already-absent
// record is not an error, matching assets.go's and FileSessionStore's
// identical convention for a clear/stop-cleanup path.
func (s *Store) DeleteAudioSession(ctx context.Context, id string) error {
	guardNotInTx(ctx, "Store.DeleteAudioSession")
	if _, err := s.db.ExecContext(ctx, `DELETE FROM audio_sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete audio session %q: %w", id, err)
	}
	return nil
}

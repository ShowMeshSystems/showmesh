package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV38's audio_renditions table (owner ruling
// 2026-09-18, PCM show audio): one row per ORIGINAL audio asset content
// hash, never per asset row, so bytes uploaded identically for two
// different (show, sequence, target) identities are transcoded once and
// share the result. The operator's own upload is never touched by this
// table: it stays whatever the assets table's content_hash and
// storage_key already name; this table only ever ADDS a second,
// separately content-addressed blob.

// Audio rendition statuses. Rendering is the state a row is created in and
// left in if the coordinator crashes mid-transcode; the reconcile pass
// retries it exactly like a row that has never been attempted.
const (
	AudioRenditionStatusRendering = "rendering"
	AudioRenditionStatusReady     = "ready"
	AudioRenditionStatusFailed    = "failed"
)

// AudioRenditionRecord is one row of the audio_renditions table.
// ContentHash, SizeBytes, DurationMillis, and Format are meaningful only
// when Status == [AudioRenditionStatusReady]; FailureReason only when
// Status == [AudioRenditionStatusFailed].
type AudioRenditionRecord struct {
	OriginalContentHash string
	Status              string
	ContentHash         string
	SizeBytes           int64
	DurationMillis      int64
	Format              string
	FailureReason       string
	UpdatedAt           time.Time
}

// AudioRenditionReady is what [Store.SetAudioRenditionReady] writes.
type AudioRenditionReady struct {
	ContentHash    string
	SizeBytes      int64
	DurationMillis int64
	Format         string
}

// ErrAudioRenditionNotFound is returned when no row exists for a given
// original content hash: this asset has never been queued for a
// rendition.
var ErrAudioRenditionNotFound = errors.New("store: audio rendition not found")

const schemaV38 = `
CREATE TABLE IF NOT EXISTS audio_renditions (
    original_content_hash  TEXT PRIMARY KEY,
    status                 TEXT NOT NULL,
    content_hash           TEXT NOT NULL,
    size_bytes             INTEGER NOT NULL,
    duration_millis        INTEGER NOT NULL,
    format                 TEXT NOT NULL,
    failure_reason         TEXT NOT NULL,
    updated_at             TEXT NOT NULL
);
`

func setAudioRenditionRendering(ctx context.Context, q querier, originalHash string, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO audio_renditions (original_content_hash, status, content_hash, size_bytes, duration_millis, format, failure_reason, updated_at)
		VALUES (?, ?, '', 0, 0, '', '', ?)
		ON CONFLICT(original_content_hash) DO UPDATE SET
			status = excluded.status, failure_reason = '', updated_at = excluded.updated_at
	`, originalHash, AudioRenditionStatusRendering, timeToDB(now))
	if err != nil {
		return fmt.Errorf("store: set audio rendition %q rendering: %w", originalHash, err)
	}
	return nil
}

// SetAudioRenditionRendering marks originalHash as currently being
// transcoded, creating the row if none exists. A ready row's own ready
// fields are left in place until [Store.SetAudioRenditionReady] overwrites
// them, so a caller reading a "rendering" row mid-retry does not lose the
// previous rendition's identity.
func (s *Store) SetAudioRenditionRendering(ctx context.Context, originalHash string) error {
	guardNotInTx(ctx, "Store.SetAudioRenditionRendering")
	return setAudioRenditionRendering(ctx, s.db, originalHash, s.now())
}

func setAudioRenditionReady(ctx context.Context, q querier, originalHash string, ready AudioRenditionReady, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO audio_renditions (original_content_hash, status, content_hash, size_bytes, duration_millis, format, failure_reason, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?)
		ON CONFLICT(original_content_hash) DO UPDATE SET
			status = excluded.status, content_hash = excluded.content_hash, size_bytes = excluded.size_bytes,
			duration_millis = excluded.duration_millis, format = excluded.format, failure_reason = '', updated_at = excluded.updated_at
	`, originalHash, AudioRenditionStatusReady, ready.ContentHash, ready.SizeBytes, ready.DurationMillis, ready.Format, timeToDB(now))
	if err != nil {
		return fmt.Errorf("store: set audio rendition %q ready: %w", originalHash, err)
	}
	return nil
}

// SetAudioRenditionReady records a completed rendition for originalHash.
func (s *Store) SetAudioRenditionReady(ctx context.Context, originalHash string, ready AudioRenditionReady) error {
	guardNotInTx(ctx, "Store.SetAudioRenditionReady")
	return setAudioRenditionReady(ctx, s.db, originalHash, ready, s.now())
}

func setAudioRenditionFailed(ctx context.Context, q querier, originalHash, reason string, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO audio_renditions (original_content_hash, status, content_hash, size_bytes, duration_millis, format, failure_reason, updated_at)
		VALUES (?, ?, '', 0, 0, '', ?, ?)
		ON CONFLICT(original_content_hash) DO UPDATE SET
			status = excluded.status, failure_reason = excluded.failure_reason, updated_at = excluded.updated_at
	`, originalHash, AudioRenditionStatusFailed, reason, timeToDB(now))
	if err != nil {
		return fmt.Errorf("store: set audio rendition %q failed: %w", originalHash, err)
	}
	return nil
}

// SetAudioRenditionFailed records that the most recent transcode attempt
// for originalHash failed, naming reason. The asset's expected-set entry
// keeps naming the original file, see [Service] in
// internal/coordinator/audiorendition.
func (s *Store) SetAudioRenditionFailed(ctx context.Context, originalHash, reason string) error {
	guardNotInTx(ctx, "Store.SetAudioRenditionFailed")
	return setAudioRenditionFailed(ctx, s.db, originalHash, reason, s.now())
}

func getAudioRendition(ctx context.Context, q querier, originalHash string) (AudioRenditionRecord, error) {
	row := q.QueryRowContext(ctx, `
		SELECT original_content_hash, status, content_hash, size_bytes, duration_millis, format, failure_reason, updated_at
		FROM audio_renditions WHERE original_content_hash = ?
	`, originalHash)

	var rec AudioRenditionRecord
	var updatedAt string
	if err := row.Scan(&rec.OriginalContentHash, &rec.Status, &rec.ContentHash, &rec.SizeBytes,
		&rec.DurationMillis, &rec.Format, &rec.FailureReason, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AudioRenditionRecord{}, ErrAudioRenditionNotFound
		}
		return AudioRenditionRecord{}, fmt.Errorf("store: get audio rendition %q: %w", originalHash, err)
	}
	var err error
	if rec.UpdatedAt, err = dbToTime(updatedAt); err != nil {
		return AudioRenditionRecord{}, fmt.Errorf("store: parse audio rendition %q updated_at: %w", originalHash, err)
	}
	return rec, nil
}

// GetAudioRendition returns originalHash's rendition row, or
// [ErrAudioRenditionNotFound] if none has ever been queued.
func (s *Store) GetAudioRendition(ctx context.Context, originalHash string) (AudioRenditionRecord, error) {
	guardNotInTx(ctx, "Store.GetAudioRendition")
	return getAudioRendition(ctx, s.db, originalHash)
}

// GetAudioRendition is [Store.GetAudioRendition]'s [Tx] form: a caller
// deciding whether to remove a rendition alongside an asset row reads this
// inside the same transaction as that row's own delete, so the decision
// and the write it depends on cannot be split by a concurrent writer.
func (t *Tx) GetAudioRendition(ctx context.Context, originalHash string) (AudioRenditionRecord, error) {
	return getAudioRendition(ctx, t.tx, originalHash)
}

func deleteAudioRendition(ctx context.Context, q querier, originalHash string) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM audio_renditions WHERE original_content_hash = ?`, originalHash); err != nil {
		return fmt.Errorf("store: delete audio rendition %q: %w", originalHash, err)
	}
	return nil
}

// DeleteAudioRendition removes originalHash's rendition row, if any. A
// caller removes this only once it has confirmed no asset row still
// references originalHash; the row's own bytes are a different backend
// blob, keyed by the rendition's ContentHash, and are the caller's
// separate responsibility to remove.
func (s *Store) DeleteAudioRendition(ctx context.Context, originalHash string) error {
	guardNotInTx(ctx, "Store.DeleteAudioRendition")
	return deleteAudioRendition(ctx, s.db, originalHash)
}

// DeleteAudioRendition is [Store.DeleteAudioRendition]'s [Tx] form: the
// row is removed in the same transaction as the asset row that made it
// orphaned, so a caller never observes the two half-applied.
func (t *Tx) DeleteAudioRendition(ctx context.Context, originalHash string) error {
	return deleteAudioRendition(ctx, t.tx, originalHash)
}

func countAudioRenditionsByContentHash(ctx context.Context, q querier, hash string) (int, error) {
	row := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM audio_renditions WHERE content_hash = ?`, hash)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count audio renditions by content hash: %w", err)
	}
	return n, nil
}

// CountAudioRenditionsByContentHash counts every audio_renditions row
// (keyed by original content hash) whose OWN rendition ContentHash equals
// hash. A rendition blob is content-addressed the same way an asset blob
// is (ADR-028 decision 4), so more than one original file can transcode to
// identical bytes and share it.
func (t *Tx) CountAudioRenditionsByContentHash(ctx context.Context, hash string) (int, error) {
	return countAudioRenditionsByContentHash(ctx, t.tx, hash)
}

// ListAudioAssetContentHashesNeedingRendition returns every distinct
// CURRENT audio asset content hash with no READY audio_renditions row: no
// row at all, or a row still rendering or failed. Superseded audio assets
// are excluded: a running show's expected set never names a superseded
// asset, so there is nothing gained by transcoding one.
func (s *Store) ListAudioAssetContentHashesNeedingRendition(ctx context.Context) ([]string, error) {
	guardNotInTx(ctx, "Store.ListAudioAssetContentHashesNeedingRendition")
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT a.content_hash
		FROM assets a
		LEFT JOIN audio_renditions r ON r.original_content_hash = a.content_hash
		WHERE a.media_type = 'audio' AND a.superseded_at IS NULL
		  AND (r.original_content_hash IS NULL OR r.status != ?)
	`, AudioRenditionStatusReady)
	if err != nil {
		return nil, fmt.Errorf("store: list audio asset content hashes needing a rendition: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, fmt.Errorf("store: list audio asset content hashes needing a rendition: %w", err)
		}
		out = append(out, hash)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list audio asset content hashes needing a rendition: %w", err)
	}
	return out, nil
}

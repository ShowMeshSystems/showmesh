package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// schemaV47 adds fallback_executor_keys: the one public key a paired FPP
// plugin registered for its FPP instance (ADR-048 decision 3).
const schemaV47 = `
CREATE TABLE IF NOT EXISTS fallback_executor_keys (
    fpp_instance_uuid TEXT PRIMARY KEY,
    public_key_b64    TEXT NOT NULL,
    registered_at     TEXT NOT NULL
);
`

// FallbackExecutorKeyRecord is one FPP instance's registered executor
// public key. A public key is not a secret.
type FallbackExecutorKeyRecord struct {
	FPPInstanceUUID string
	PublicKeyB64    string
	RegisteredAt    time.Time
}

// ErrFallbackExecutorKeyNotFound means the instance has registered no key.
var ErrFallbackExecutorKeyNotFound = errors.New("store: fallback executor key not found")

// PutFallbackExecutorKey stores rec as the instance's only key and
// reports whether it differs from what was stored. Storing the same key
// again keeps the original RegisteredAt.
func (s *Store) PutFallbackExecutorKey(ctx context.Context, rec FallbackExecutorKeyRecord) (stored FallbackExecutorKeyRecord, changed bool, err error) {
	guardNotInTx(ctx, "Store.PutFallbackExecutorKey")
	if rec.FPPInstanceUUID == "" || rec.PublicKeyB64 == "" {
		return FallbackExecutorKeyRecord{}, false, fmt.Errorf("store: put fallback executor key: instance and key are both required")
	}
	err = s.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		existing, getErr := getFallbackExecutorKey(ctx, tx.tx, rec.FPPInstanceUUID)
		if getErr == nil && existing.PublicKeyB64 == rec.PublicKeyB64 {
			stored = existing
			return nil
		}
		if getErr != nil && !errors.Is(getErr, ErrFallbackExecutorKeyNotFound) {
			return getErr
		}
		if _, execErr := tx.tx.ExecContext(ctx, `
			INSERT INTO fallback_executor_keys (fpp_instance_uuid, public_key_b64, registered_at)
			VALUES (?, ?, ?)
			ON CONFLICT(fpp_instance_uuid) DO UPDATE SET
				public_key_b64 = excluded.public_key_b64,
				registered_at  = excluded.registered_at
		`, rec.FPPInstanceUUID, rec.PublicKeyB64, timeToDB(rec.RegisteredAt)); execErr != nil {
			return fmt.Errorf("store: put fallback executor key %q: %w", rec.FPPInstanceUUID, execErr)
		}
		stored, changed = rec, true
		return nil
	})
	return stored, changed, err
}

func getFallbackExecutorKey(ctx context.Context, q querier, instanceUUID string) (FallbackExecutorKeyRecord, error) {
	var (
		rec          FallbackExecutorKeyRecord
		registeredAt string
	)
	err := q.QueryRowContext(ctx, `
		SELECT fpp_instance_uuid, public_key_b64, registered_at
		FROM fallback_executor_keys WHERE fpp_instance_uuid = ?
	`, instanceUUID).Scan(&rec.FPPInstanceUUID, &rec.PublicKeyB64, &registeredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FallbackExecutorKeyRecord{}, ErrFallbackExecutorKeyNotFound
	}
	if err != nil {
		return FallbackExecutorKeyRecord{}, fmt.Errorf("store: get fallback executor key %q: %w", instanceUUID, err)
	}
	if rec.RegisteredAt, err = dbToTime(registeredAt); err != nil {
		return FallbackExecutorKeyRecord{}, fmt.Errorf("store: parse fallback executor key registered_at: %w", err)
	}
	return rec, nil
}

// GetFallbackExecutorKey returns the instance's registered key, or
// [ErrFallbackExecutorKeyNotFound].
func (s *Store) GetFallbackExecutorKey(ctx context.Context, instanceUUID string) (FallbackExecutorKeyRecord, error) {
	guardNotInTx(ctx, "Store.GetFallbackExecutorKey")
	return getFallbackExecutorKey(ctx, s.db, instanceUUID)
}

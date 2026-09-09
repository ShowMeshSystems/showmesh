package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV33's credentials table (owner ruling 2026-09-08,
// "credentials into SQLite"): a general-purpose, kind-agnostic secret store.
// It knows nothing about what any particular (kind, object_id, field) triple
// names, exactly like config.go's config_revisions treats payload_json as
// opaque, so a caller (the fpp.mqtt broker password today; a converted
// SHOWMESH_INTEGRATION_BROKERS tomorrow, if that gap is ever closed) supplies
// its own key, never a value this package interprets.
//
// A credential row is mutable and lives OUTSIDE config_revisions on purpose:
// ADR-009 makes a revision immutable, and an immutable copy of a rotatable
// secret is the one thing rotation exists to prevent (ADR-039 decision 7). A
// config_revisions row that carries a credential REFERENCES this table's
// (kind, object_id, field) key, typically as a boolean presence marker in
// its own payload_json (see config.FPPMQTTPayload.PasswordSet), and never
// embeds the value itself.

// CredentialRecord is one row of the credentials table.
type CredentialRecord struct {
	Kind      string
	ObjectID  string
	Field     string
	Value     string
	UpdatedAt time.Time
}

func setCredential(ctx context.Context, q querier, kind, objectID, field, value string, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO credentials (kind, object_id, field, value, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(kind, object_id, field) DO UPDATE SET
			value      = excluded.value,
			updated_at = excluded.updated_at
	`, kind, objectID, field, value, timeToDB(now))
	if err != nil {
		return fmt.Errorf("store: set credential %s/%s/%s: %w", kind, objectID, field, err)
	}
	return nil
}

// SetCredential stores value under (kind, objectID, field), overwriting any
// previous value. A single INSERT ... ON CONFLICT needs no transaction of
// its own beyond SQLite's per-statement atomicity, matching
// [Store.CreateConfigRevision]'s identical reasoning; a caller that needs
// this write to share a transaction with something else (an audit entry,
// e.g. a startup migration) uses [Tx.SetCredential] via
// [identity.Service.AuditedWrite].
func (s *Store) SetCredential(ctx context.Context, kind, objectID, field, value string) error {
	guardNotInTx(ctx, "Store.SetCredential")
	return setCredential(ctx, s.db, kind, objectID, field, value, s.now())
}

// SetCredential is [Store.SetCredential]'s [Tx] form.
func (t *Tx) SetCredential(ctx context.Context, kind, objectID, field, value string) error {
	return setCredential(ctx, t.tx, kind, objectID, field, value, t.s.now())
}

func clearCredential(ctx context.Context, q querier, kind, objectID, field string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM credentials WHERE kind = ? AND object_id = ? AND field = ?`, kind, objectID, field)
	if err != nil {
		return fmt.Errorf("store: clear credential %s/%s/%s: %w", kind, objectID, field, err)
	}
	return nil
}

// ClearCredential removes (kind, objectID, field). Clearing an
// already-absent credential is not an error, since there is nothing to
// clear, mirroring the file-era config.ClearFPPMQTTPassword's own precedent.
func (s *Store) ClearCredential(ctx context.Context, kind, objectID, field string) error {
	guardNotInTx(ctx, "Store.ClearCredential")
	return clearCredential(ctx, s.db, kind, objectID, field)
}

// ClearCredential is [Store.ClearCredential]'s [Tx] form.
func (t *Tx) ClearCredential(ctx context.Context, kind, objectID, field string) error {
	return clearCredential(ctx, t.tx, kind, objectID, field)
}

func hasCredential(ctx context.Context, q querier, kind, objectID, field string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM credentials WHERE kind = ? AND object_id = ? AND field = ?`,
		kind, objectID, field,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: check credential presence %s/%s/%s: %w", kind, objectID, field, err)
	}
	return n > 0, nil
}

// HasCredential reports whether (kind, objectID, field) currently has a
// value. Read LIVE against this table on every call, never cached, and
// never inferred from a config_revisions payload's own marker: this is
// what lets GET /api/v1/config/fpp.mqtt (and any future caller) answer
// presence honestly even in the window where a revision's stored marker and
// this table disagree (ADR-039 decision 7).
func (s *Store) HasCredential(ctx context.Context, kind, objectID, field string) (bool, error) {
	guardNotInTx(ctx, "Store.HasCredential")
	return hasCredential(ctx, s.db, kind, objectID, field)
}

// HasCredential is [Store.HasCredential]'s [Tx] form.
func (t *Tx) HasCredential(ctx context.Context, kind, objectID, field string) (bool, error) {
	return hasCredential(ctx, t.tx, kind, objectID, field)
}

func getCredential(ctx context.Context, q querier, kind, objectID, field string) (string, bool, error) {
	var value string
	err := q.QueryRowContext(ctx,
		`SELECT value FROM credentials WHERE kind = ? AND object_id = ? AND field = ?`,
		kind, objectID, field,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: get credential %s/%s/%s: %w", kind, objectID, field, err)
	}
	return value, true, nil
}

// GetCredential returns the value stored under (kind, objectID, field), or
// ("", false, nil) when none is set. Reach for this only where the actual
// value is genuinely needed: a collector authenticating to a broker, or the
// one-time legacy-file migration reading what it is about to move. Never
// from an HTTP response path, which must answer through [Store.HasCredential]
// instead: a credential is write-only over the API (ADR-039 decision 7).
func (s *Store) GetCredential(ctx context.Context, kind, objectID, field string) (value string, present bool, err error) {
	guardNotInTx(ctx, "Store.GetCredential")
	return getCredential(ctx, s.db, kind, objectID, field)
}

// GetCredential is [Store.GetCredential]'s [Tx] form.
func (t *Tx) GetCredential(ctx context.Context, kind, objectID, field string) (value string, present bool, err error) {
	return getCredential(ctx, t.tx, kind, objectID, field)
}

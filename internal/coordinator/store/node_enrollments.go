package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// schemaV45 adds node_enrollment_codes (ADR-055 decision 4): one row per
// minted enrollment code. Only the code's SHA-256 hash is stored. A row's
// state is derived from redeemed_at, cancelled_at and expires_at.
const schemaV45 = `
CREATE TABLE IF NOT EXISTS node_enrollment_codes (
	id              TEXT PRIMARY KEY,
	node_id         TEXT NOT NULL,
	code_hash       TEXT NOT NULL UNIQUE,
	reenroll        INTEGER NOT NULL DEFAULT 0,
	created_by      TEXT NOT NULL,
	created_by_name TEXT NOT NULL DEFAULT '',
	created_at      TEXT NOT NULL,
	expires_at      TEXT NOT NULL,
	redeemed_at     TEXT,
	cancelled_at    TEXT,
	principal_id    TEXT NOT NULL DEFAULT '',
	hostname        TEXT NOT NULL DEFAULT '',
	arch            TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS node_enrollment_codes_node_id ON node_enrollment_codes (node_id);
`

// NodeEnrollmentCodeRecord is one row of node_enrollment_codes.
// PrincipalID, Hostname and Arch are set when the code is redeemed.
type NodeEnrollmentCodeRecord struct {
	ID            string
	NodeID        string
	CodeHash      string
	Reenroll      bool
	CreatedBy     string
	CreatedByName string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	RedeemedAt    *time.Time
	CancelledAt   *time.Time
	PrincipalID   string
	Hostname      string
	Arch          string
}

// ErrNodeEnrollmentCodeNotFound is returned when no enrollment code row
// matches the id or hash asked for.
var ErrNodeEnrollmentCodeNotFound = errors.New("store: node enrollment code not found")

const nodeEnrollmentCodeColumns = `id, node_id, code_hash, reenroll, created_by, created_by_name, created_at, expires_at,
	redeemed_at, cancelled_at, principal_id, hostname, arch`

func scanNodeEnrollmentCode(row interface{ Scan(dest ...any) error }) (NodeEnrollmentCodeRecord, error) {
	var (
		rec                   NodeEnrollmentCodeRecord
		reenroll              int64
		createdAt, expiresAt  string
		redeemedAt, cancelled sql.NullString
	)
	if err := row.Scan(&rec.ID, &rec.NodeID, &rec.CodeHash, &reenroll, &rec.CreatedBy, &rec.CreatedByName,
		&createdAt, &expiresAt, &redeemedAt, &cancelled, &rec.PrincipalID, &rec.Hostname, &rec.Arch); err != nil {
		return NodeEnrollmentCodeRecord{}, err
	}
	rec.Reenroll = reenroll != 0
	var err error
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return NodeEnrollmentCodeRecord{}, fmt.Errorf("store: parse enrollment code created_at: %w", err)
	}
	if rec.ExpiresAt, err = dbToTime(expiresAt); err != nil {
		return NodeEnrollmentCodeRecord{}, fmt.Errorf("store: parse enrollment code expires_at: %w", err)
	}
	if rec.RedeemedAt, err = dbToTimePtr(redeemedAt); err != nil {
		return NodeEnrollmentCodeRecord{}, fmt.Errorf("store: parse enrollment code redeemed_at: %w", err)
	}
	if rec.CancelledAt, err = dbToTimePtr(cancelled); err != nil {
		return NodeEnrollmentCodeRecord{}, fmt.Errorf("store: parse enrollment code cancelled_at: %w", err)
	}
	return rec, nil
}

func getNodeEnrollmentCode(ctx context.Context, q querier, column, value string) (NodeEnrollmentCodeRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT `+nodeEnrollmentCodeColumns+` FROM node_enrollment_codes WHERE `+column+` = ?`, value)
	rec, err := scanNodeEnrollmentCode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeEnrollmentCodeRecord{}, ErrNodeEnrollmentCodeNotFound
	}
	if err != nil {
		return NodeEnrollmentCodeRecord{}, fmt.Errorf("store: get node enrollment code: %w", err)
	}
	return rec, nil
}

// GetNodeEnrollmentCode returns the enrollment code row with id.
func (s *Store) GetNodeEnrollmentCode(ctx context.Context, id string) (NodeEnrollmentCodeRecord, error) {
	guardNotInTx(ctx, "Store.GetNodeEnrollmentCode")
	return getNodeEnrollmentCode(ctx, s.db, "id", id)
}

// GetNodeEnrollmentCode is [Store.GetNodeEnrollmentCode]'s [Tx] form.
func (t *Tx) GetNodeEnrollmentCode(ctx context.Context, id string) (NodeEnrollmentCodeRecord, error) {
	return getNodeEnrollmentCode(ctx, t.tx, "id", id)
}

// GetNodeEnrollmentCodeByHash returns the enrollment code row whose code
// hashes to codeHash.
func (s *Store) GetNodeEnrollmentCodeByHash(ctx context.Context, codeHash string) (NodeEnrollmentCodeRecord, error) {
	guardNotInTx(ctx, "Store.GetNodeEnrollmentCodeByHash")
	return getNodeEnrollmentCode(ctx, s.db, "code_hash", codeHash)
}

// ListNodeEnrollmentCodes returns every enrollment code row, newest first.
func (s *Store) ListNodeEnrollmentCodes(ctx context.Context) ([]NodeEnrollmentCodeRecord, error) {
	guardNotInTx(ctx, "Store.ListNodeEnrollmentCodes")
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeEnrollmentCodeColumns+` FROM node_enrollment_codes ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list node enrollment codes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []NodeEnrollmentCodeRecord
	for rows.Next() {
		rec, err := scanNodeEnrollmentCode(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan node enrollment code: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list node enrollment codes: %w", err)
	}
	return out, nil
}

// LatestRedeemedNodeEnrollment returns nodeID's most recently redeemed
// enrollment code, and false when nodeID has never redeemed one.
func (s *Store) LatestRedeemedNodeEnrollment(ctx context.Context, nodeID string) (NodeEnrollmentCodeRecord, bool, error) {
	guardNotInTx(ctx, "Store.LatestRedeemedNodeEnrollment")
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeEnrollmentCodeColumns+` FROM node_enrollment_codes
		WHERE node_id = ? AND redeemed_at IS NOT NULL ORDER BY redeemed_at DESC LIMIT 1`, nodeID)
	rec, err := scanNodeEnrollmentCode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeEnrollmentCodeRecord{}, false, nil
	}
	if err != nil {
		return NodeEnrollmentCodeRecord{}, false, fmt.Errorf("store: latest redeemed enrollment for %q: %w", nodeID, err)
	}
	return rec, true, nil
}

// InsertNodeEnrollmentCode inserts a newly minted code.
func (t *Tx) InsertNodeEnrollmentCode(ctx context.Context, rec NodeEnrollmentCodeRecord) error {
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO node_enrollment_codes (id, node_id, code_hash, reenroll, created_by, created_by_name, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.ID, rec.NodeID, rec.CodeHash, boolToDB(rec.Reenroll), rec.CreatedBy, rec.CreatedByName,
		timeToDB(rec.CreatedAt), timeToDB(rec.ExpiresAt)); err != nil {
		return fmt.Errorf("store: insert node enrollment code for %q: %w", rec.NodeID, err)
	}
	return nil
}

// CancelPendingNodeEnrollmentCodes cancels every code for nodeID that is
// neither redeemed, cancelled, nor expired at now, and returns their ids.
func (t *Tx) CancelPendingNodeEnrollmentCodes(ctx context.Context, nodeID string, now time.Time) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT id FROM node_enrollment_codes
		WHERE node_id = ? AND redeemed_at IS NULL AND cancelled_at IS NULL AND expires_at > ?`, nodeID, timeToDB(now))
	if err != nil {
		return nil, fmt.Errorf("store: find pending enrollment codes for %q: %w", nodeID, err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("store: scan pending enrollment code id: %w", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	for _, id := range ids {
		if _, err := t.CancelNodeEnrollmentCode(ctx, id, now); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// CancelNodeEnrollmentCode cancels code id if it is still pending at now,
// and reports whether it did.
func (t *Tx) CancelNodeEnrollmentCode(ctx context.Context, id string, now time.Time) (bool, error) {
	res, err := t.tx.ExecContext(ctx, `UPDATE node_enrollment_codes SET cancelled_at = ?
		WHERE id = ? AND redeemed_at IS NULL AND cancelled_at IS NULL AND expires_at > ?`, timeToDB(now), id, timeToDB(now))
	if err != nil {
		return false, fmt.Errorf("store: cancel node enrollment code %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: cancel node enrollment code %q: %w", id, err)
	}
	return n == 1, nil
}

// MarkNodeEnrollmentCodeRedeemed records code id as redeemed by
// principalID, and reports false when the code was no longer pending.
func (t *Tx) MarkNodeEnrollmentCodeRedeemed(ctx context.Context, id string, now time.Time, principalID, hostname, arch string) (bool, error) {
	res, err := t.tx.ExecContext(ctx, `UPDATE node_enrollment_codes
		SET redeemed_at = ?, principal_id = ?, hostname = ?, arch = ?
		WHERE id = ? AND redeemed_at IS NULL AND cancelled_at IS NULL AND expires_at > ?`,
		timeToDB(now), principalID, hostname, arch, id, timeToDB(now))
	if err != nil {
		return false, fmt.Errorf("store: mark node enrollment code %q redeemed: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: mark node enrollment code %q redeemed: %w", id, err)
	}
	return n == 1, nil
}

// CreatePrincipal is [Store.CreatePrincipal]'s [Tx] form, so a principal
// and its first token commit together with the write that needs them.
func (t *Tx) CreatePrincipal(ctx context.Context, rec PrincipalRecord) (PrincipalRecord, error) {
	if rec.ID == ReservedPrincipalID || rec.Name == ReservedPrincipalID {
		return PrincipalRecord{}, ErrReservedPrincipal
	}
	now := t.s.now()
	rec.CreatedAt, rec.UpdatedAt, rec.Generation = now, now, 0
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO principals (id, name, kind, role, password_hash, disabled, generation, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.ID, rec.Name, rec.Kind, rec.Role, rec.PasswordHash, boolToDB(rec.Disabled), rec.Generation,
		timeToDB(rec.CreatedAt), timeToDB(rec.UpdatedAt)); err != nil {
		if isUniqueConstraintErr(err) {
			return PrincipalRecord{}, fmt.Errorf("store: create principal %q: %w", rec.Name, ErrPrincipalNameTaken)
		}
		return PrincipalRecord{}, fmt.Errorf("store: create principal %q: %w", rec.Name, err)
	}
	return rec, nil
}

// ResetEnrolledPrincipal sets principalID's role, enables it, and bumps its
// generation so every credential issued before this call stops working.
func (t *Tx) ResetEnrolledPrincipal(ctx context.Context, principalID, role string) error {
	if principalID == ReservedPrincipalID {
		return ErrReservedPrincipal
	}
	res, err := t.tx.ExecContext(ctx, `UPDATE principals
		SET role = ?, disabled = 0, generation = generation + 1, updated_at = ? WHERE id = ?`,
		role, timeToDB(t.s.now()), principalID)
	if err != nil {
		return fmt.Errorf("store: reset enrolled principal %q: %w", principalID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: reset enrolled principal %q: %w", principalID, ErrPrincipalNotFound)
	}
	return nil
}

// RevokeAllTokens revokes every unrevoked token principalID holds and
// returns how many it revoked.
func (t *Tx) RevokeAllTokens(ctx context.Context, principalID string) (int64, error) {
	res, err := t.tx.ExecContext(ctx, `UPDATE principal_tokens SET revoked_at = ? WHERE principal_id = ? AND revoked_at IS NULL`,
		timeToDB(t.s.now()), principalID)
	if err != nil {
		return 0, fmt.Errorf("store: revoke tokens of %q: %w", principalID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: revoke tokens of %q: %w", principalID, err)
	}
	return n, nil
}

// CreateToken is [Store.CreateToken]'s [Tx] form.
func (t *Tx) CreateToken(ctx context.Context, rec TokenRecord) (TokenRecord, error) {
	if rec.PrincipalID == ReservedPrincipalID {
		return TokenRecord{}, ErrReservedPrincipal
	}
	var generation uint64
	if err := t.tx.QueryRowContext(ctx, `SELECT generation FROM principals WHERE id = ?`, rec.PrincipalID).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TokenRecord{}, fmt.Errorf("store: create token for principal %q: %w", rec.PrincipalID, ErrPrincipalNotFound)
		}
		return TokenRecord{}, fmt.Errorf("store: read generation for %q: %w", rec.PrincipalID, err)
	}
	rec.Generation = generation
	rec.CreatedAt = t.s.now()
	rec.RevokedAt, rec.LastUsedAt = nil, nil
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO principal_tokens (id, principal_id, digest, hint, label, generation, created_at, expires_at, revoked_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.ID, rec.PrincipalID, rec.Digest, rec.Hint, rec.Label, rec.Generation,
		timeToDB(rec.CreatedAt), timePtrToDB(rec.ExpiresAt), nil, nil); err != nil {
		return TokenRecord{}, fmt.Errorf("store: create token for principal %q: %w", rec.PrincipalID, err)
	}
	return rec, nil
}

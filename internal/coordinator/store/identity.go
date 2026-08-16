package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file holds schemaV5's repository methods (principals,
// principal_tokens, principal_sessions, bootstrap). See migrations.go's
// schemaV5 doc comment for the three rules that apply across all of them
// (no cleartext credential ever stored, all excluded from a future ADR-009
// export bundle, and audit_log — audit.go, not this file — is append-only).
//
// Every type in this file is a store-shaped record, deliberately distinct
// from the domain types the identity package exports (identity.Principal,
// identity.Session, ...) — the same split this package already draws
// between store.HelloRecord and whatever internal/coordinator/inventory
// builds from it. The identity package imports this package and converts;
// this package does not import identity, because identity imports store
// and Go does not allow the cycle back — see identity's package doc
// comment for the fuller version of why the split exists here, not just
// that it does.

// PrincipalRecord is one row of the principals table.
type PrincipalRecord struct {
	ID           string
	Name         string
	Kind         string
	Role         string
	PasswordHash string
	Disabled     bool
	Generation   uint64

	// CreatedAt and UpdatedAt are store bookkeeping, exactly like
	// [NodeRecord.FirstSeenAt]/[NodeRecord.UpdatedAt]: [Store.CreatePrincipal]
	// always stamps CreatedAt from its own clock and ignores whatever a
	// caller set here on input, and every mutating method restamps
	// UpdatedAt the same way.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ErrPrincipalNotFound is returned by [Store.GetPrincipal] and
// [Store.GetPrincipalByName] when no matching row exists.
var ErrPrincipalNotFound = errors.New("store: principal not found")

// ErrPrincipalNameTaken is returned by [Store.CreatePrincipal] when name is
// already in use by another principal (principals.name UNIQUE).
var ErrPrincipalNameTaken = errors.New("store: principal name already in use")

// ReservedPrincipalID is Track D seam D-3a's built-in automatic-recovery
// principal id and name (identity.ReservedResolumeRecoveryPrincipalID
// duplicated by VALUE, not by import: this package must not import
// identity — see this file's own top comment). A mismatch between the two
// definitions would silently let a user-created principal claim the name
// this package's own guards are built to protect, so both are named
// explicitly here rather than left to be rediscovered by testing it.
const ReservedPrincipalID = "system-resolume-recovery"

// ErrReservedPrincipal is returned by [Store.CreatePrincipal],
// [Store.SetPrincipalDisabled], [Store.SetPrincipalRole], and
// [Store.SetPrincipalPasswordHash] for any attempt to create, disable,
// re-role, or re-credential [ReservedPrincipalID] through the ordinary
// path. [Store.EnsureReservedPrincipal] is the one path that may create it.
var ErrReservedPrincipal = errors.New("store: this is the reserved built-in Resolume recovery principal and cannot be created, disabled, re-roled, or re-credentialed through this path")

func insertPrincipal(ctx context.Context, s *Store, rec PrincipalRecord) (PrincipalRecord, error) {
	now := s.now()
	rec.CreatedAt = now
	rec.UpdatedAt = now
	rec.Generation = 0

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO principals (id, name, kind, role, password_hash, disabled, generation, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rec.ID, rec.Name, rec.Kind, rec.Role, rec.PasswordHash, boolToDB(rec.Disabled), rec.Generation,
		timeToDB(rec.CreatedAt), timeToDB(rec.UpdatedAt),
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return PrincipalRecord{}, fmt.Errorf("store: create principal %q: %w", rec.Name, ErrPrincipalNameTaken)
		}
		return PrincipalRecord{}, fmt.Errorf("store: create principal %q: %w", rec.Name, err)
	}
	return rec, nil
}

// CreatePrincipal inserts a new principal. rec.ID, rec.Name, rec.Kind, and
// rec.Role must already be set by the caller (the identity package
// generates IDs; this package invents nothing an audit trail would later
// need to explain). rec.CreatedAt/UpdatedAt are ignored on input and
// stamped from this Store's own clock, matching [Store.AppendEvent]'s
// ev.RecordedAt convention. rec.Generation is forced to 0 regardless of
// input: a brand-new principal has never had a session revoked out from
// under it, so there is no history to start counting from above zero.
//
// Refuses [ErrReservedPrincipal] when rec.ID or rec.Name equals
// [ReservedPrincipalID] — this is the ordinary, user-facing creation path
// (CLI create-principal, identity.Service.CreatePrincipal); only
// [Store.EnsureReservedPrincipal] may create that principal.
func (s *Store) CreatePrincipal(ctx context.Context, rec PrincipalRecord) (PrincipalRecord, error) {
	guardNotInTx(ctx, "Store.CreatePrincipal")
	if rec.ID == ReservedPrincipalID || rec.Name == ReservedPrincipalID {
		return PrincipalRecord{}, ErrReservedPrincipal
	}
	return insertPrincipal(ctx, s, rec)
}

// EnsureReservedPrincipal idempotently creates [ReservedPrincipalID] if it
// does not already exist — the one path in this package permitted to
// create it, called only at coordinator startup (identity.Service's own
// EnsureReservedRecoveryPrincipal). created reports whether this call
// actually inserted the row (false when it already existed).
func (s *Store) EnsureReservedPrincipal(ctx context.Context, rec PrincipalRecord) (result PrincipalRecord, created bool, err error) {
	guardNotInTx(ctx, "Store.EnsureReservedPrincipal")
	existing, err := s.GetPrincipal(ctx, rec.ID)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, ErrPrincipalNotFound) {
		return PrincipalRecord{}, false, err
	}
	inserted, err := insertPrincipal(ctx, s, rec)
	if err != nil {
		if errors.Is(err, ErrPrincipalNameTaken) {
			// Raced with a concurrent EnsureReservedPrincipal call (or, in
			// principle, the same startup path invoked twice): the row now
			// exists, created by whichever caller won.
			existing, gerr := s.GetPrincipal(ctx, rec.ID)
			if gerr != nil {
				return PrincipalRecord{}, false, gerr
			}
			return existing, false, nil
		}
		return PrincipalRecord{}, false, err
	}
	return inserted, true, nil
}

const principalColumns = `id, name, kind, role, password_hash, disabled, generation, created_at, updated_at`

func scanPrincipal(row interface{ Scan(dest ...any) error }) (PrincipalRecord, error) {
	var (
		rec                  PrincipalRecord
		disabled             int64
		createdAt, updatedAt string
	)
	if err := row.Scan(
		&rec.ID, &rec.Name, &rec.Kind, &rec.Role, &rec.PasswordHash,
		&disabled, &rec.Generation, &createdAt, &updatedAt,
	); err != nil {
		return PrincipalRecord{}, err
	}
	rec.Disabled = disabled != 0
	var err error
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return PrincipalRecord{}, fmt.Errorf("store: parse principal created_at: %w", err)
	}
	if rec.UpdatedAt, err = dbToTime(updatedAt); err != nil {
		return PrincipalRecord{}, fmt.Errorf("store: parse principal updated_at: %w", err)
	}
	return rec, nil
}

// GetPrincipal returns the principal with the given id, or
// [ErrPrincipalNotFound].
func (s *Store) GetPrincipal(ctx context.Context, id string) (PrincipalRecord, error) {
	guardNotInTx(ctx, "Store.GetPrincipal")
	row := s.db.QueryRowContext(ctx, `SELECT `+principalColumns+` FROM principals WHERE id = ?`, id)
	rec, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PrincipalRecord{}, ErrPrincipalNotFound
	}
	if err != nil {
		return PrincipalRecord{}, fmt.Errorf("store: get principal %q: %w", id, err)
	}
	return rec, nil
}

// GetPrincipalByName returns the principal with the given name (the login
// identifier [identity.Service.AuthenticatePassword] looks up), or
// [ErrPrincipalNotFound].
func (s *Store) GetPrincipalByName(ctx context.Context, name string) (PrincipalRecord, error) {
	guardNotInTx(ctx, "Store.GetPrincipalByName")
	row := s.db.QueryRowContext(ctx, `SELECT `+principalColumns+` FROM principals WHERE name = ?`, name)
	rec, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PrincipalRecord{}, ErrPrincipalNotFound
	}
	if err != nil {
		return PrincipalRecord{}, fmt.Errorf("store: get principal by name %q: %w", name, err)
	}
	return rec, nil
}

// ListPrincipals returns every principal, ordered by name for a stable,
// deterministic result (matching [Store.ListNodes]'s ordering convention).
func (s *Store) ListPrincipals(ctx context.Context) ([]PrincipalRecord, error) {
	guardNotInTx(ctx, "Store.ListPrincipals")
	rows, err := s.db.QueryContext(ctx, `SELECT `+principalColumns+` FROM principals ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list principals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []PrincipalRecord
	for rows.Next() {
		rec, err := scanPrincipal(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list principals: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list principals: %w", err)
	}
	return out, nil
}

// HasAnyPrincipal reports whether at least one principal row exists —
// first-run state per ADR-024 decision 9, which [identity.Service.HasAnyPrincipal]
// exposes directly.
func (s *Store) HasAnyPrincipal(ctx context.Context) (bool, error) {
	guardNotInTx(ctx, "Store.HasAnyPrincipal")
	var exists int64
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM principals)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("store: check any principal exists: %w", err)
	}
	return exists != 0, nil
}

// SetPrincipalPasswordHash replaces principalID's password hash and bumps
// its generation in one transaction — a password change is exactly the
// decision-5 case that must invalidate every session minted before it, and
// the two writes succeeding or failing together is what stops a change
// landing without the invalidation it depends on. Returns the new
// generation value.
func (s *Store) SetPrincipalPasswordHash(ctx context.Context, principalID, passwordHash string) (uint64, error) {
	guardNotInTx(ctx, "Store.SetPrincipalPasswordHash")
	if principalID == ReservedPrincipalID {
		return 0, ErrReservedPrincipal
	}
	return s.bumpPrincipalGenerationTx(ctx, principalID, func(tx *sql.Tx, now string) error {
		_, err := tx.ExecContext(ctx, `UPDATE principals SET password_hash = ? WHERE id = ?`, passwordHash, principalID)
		return err
	})
}

// SetPrincipalDisabled sets principalID's disabled flag. Disabling also
// bumps the generation counter, for the same reason a password change
// does: a disabled principal's already-open sessions and streams must stop
// working, not just future authentication attempts. Re-enabling does NOT
// bump it again — there is nothing stale to invalidate by turning access
// back on.
func (s *Store) SetPrincipalDisabled(ctx context.Context, principalID string, disabled bool) (uint64, error) {
	guardNotInTx(ctx, "Store.SetPrincipalDisabled")
	if principalID == ReservedPrincipalID {
		return 0, ErrReservedPrincipal
	}
	if !disabled {
		now := timeToDB(s.now())
		_, err := s.db.ExecContext(ctx, `UPDATE principals SET disabled = 0, updated_at = ? WHERE id = ?`, now, principalID)
		if err != nil {
			return 0, fmt.Errorf("store: enable principal %q: %w", principalID, err)
		}
		rec, err := s.GetPrincipal(ctx, principalID)
		if err != nil {
			return 0, err
		}
		return rec.Generation, nil
	}
	return s.bumpPrincipalGenerationTx(ctx, principalID, func(tx *sql.Tx, now string) error {
		_, err := tx.ExecContext(ctx, `UPDATE principals SET disabled = 1 WHERE id = ?`, principalID)
		return err
	})
}

// SetPrincipalRole replaces principalID's role and bumps its generation in
// one transaction, mirroring [Store.SetPrincipalPasswordHash] exactly: a
// role change is ADR-024 decision 12's own trigger for invalidating every
// session and closing every open change stream currently relying on the
// OLD role's scope set ("a role change... increments the generation
// counter in decision 5, which closes open streams and forces a
// re-fetch, so the stale window is bounded rather than indefinite"). A
// principal reading its own new, narrower scope list one bump later is
// exactly the property this exists to guarantee — unlike
// [Store.SetPrincipalDisabled]'s re-enable path, there is no
// "widening a role is safe not to bump" analogue here: even a role
// change that only ever WIDENS access must still force a re-fetch, since
// decision 12 also requires a stale scope list to read as unknown, never
// silently keep serving the principal's old (narrower OR wider) view
// from a cookie a client is still holding.
func (s *Store) SetPrincipalRole(ctx context.Context, principalID, role string) (uint64, error) {
	guardNotInTx(ctx, "Store.SetPrincipalRole")
	if principalID == ReservedPrincipalID {
		return 0, ErrReservedPrincipal
	}
	return s.bumpPrincipalGenerationTx(ctx, principalID, func(tx *sql.Tx, now string) error {
		_, err := tx.ExecContext(ctx, `UPDATE principals SET role = ? WHERE id = ?`, role, principalID)
		return err
	})
}

// BumpPrincipalGeneration increments principalID's generation with no
// other change — the "administrative revocation of all sessions" case
// decision 5 names explicitly, invoked with no accompanying credential
// change (unlike [Store.SetPrincipalPasswordHash]).
func (s *Store) BumpPrincipalGeneration(ctx context.Context, principalID string) (uint64, error) {
	guardNotInTx(ctx, "Store.BumpPrincipalGeneration")
	return s.bumpPrincipalGenerationTx(ctx, principalID, func(tx *sql.Tx, now string) error { return nil })
}

// bumpPrincipalGenerationTx runs extra (an additional UPDATE against
// principals, or a no-op) and the generation bump in one transaction,
// returning the new generation. Shared by SetPrincipalPasswordHash,
// SetPrincipalDisabled(true), and BumpPrincipalGeneration so the
// read-current-generation/increment/write sequence is written exactly
// once rather than three times with the chance for one copy to drift.
func (s *Store) bumpPrincipalGenerationTx(ctx context.Context, principalID string, extra func(tx *sql.Tx, now string) error) (uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin bump generation for %q: %w", principalID, err)
	}
	defer func() { _ = tx.Rollback() }()

	var generation uint64
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM principals WHERE id = ?`, principalID).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("store: bump generation for %q: %w", principalID, ErrPrincipalNotFound)
		}
		return 0, fmt.Errorf("store: read generation for %q: %w", principalID, err)
	}
	generation++
	now := timeToDB(s.now())

	if err := extra(tx, now); err != nil {
		return 0, fmt.Errorf("store: bump generation for %q: %w", principalID, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE principals SET generation = ?, updated_at = ? WHERE id = ?`, generation, now, principalID); err != nil {
		return 0, fmt.Errorf("store: bump generation for %q: %w", principalID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit bump generation for %q: %w", principalID, err)
	}
	return generation, nil
}

// --- principal_tokens ---

// TokenRecord is one row of the principal_tokens table. Digest is the
// SHA-256 hex digest of the full presented token string (identity
// package's token.go); the raw token is never stored here, matching
// migrations.go's schemaV5 rule 1. Hint is a short, non-secret slice of
// the token's random component, kept only so an operator can tell two
// tokens with the same Label apart in a listing.
type TokenRecord struct {
	ID          string
	PrincipalID string
	Digest      string
	Hint        string
	Label       string

	// Generation mirrors [SessionRecord.Generation] exactly: the
	// principal's generation counter at the moment this token was issued,
	// read from principals.generation inside the same transaction as the
	// insert (see [Store.CreateToken]), never caller-supplied on input.
	// [identity.Service.AuthenticateToken] rejects a token whose stored
	// Generation is less than the principal's current one, the same check
	// AuthenticateSession already made for sessions — see migrations.go's
	// schemaV5 doc comment for why a token needed this at all.
	Generation uint64

	CreatedAt  time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// CreateToken inserts a new token row. rec.ID, PrincipalID, Digest, Hint,
// and Label must already be set; CreatedAt is stamped from this Store's
// clock and ignored on input. ExpiresAt is stored exactly as given — nil
// means "no expiry", the ADR-024 decision 1 default for machine tokens —
// and RevokedAt/LastUsedAt start nil. Generation is NOT taken from rec —
// exactly like [Store.CreateSession], it is read from
// principals.generation for PrincipalID inside the same transaction as the
// insert, so the stored value is always the principal's true current
// generation at creation time regardless of what a caller guessed it to be.
func (s *Store) CreateToken(ctx context.Context, rec TokenRecord) (TokenRecord, error) {
	guardNotInTx(ctx, "Store.CreateToken")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TokenRecord{}, fmt.Errorf("store: begin create token for principal %q: %w", rec.PrincipalID, err)
	}
	defer func() { _ = tx.Rollback() }()

	var generation uint64
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM principals WHERE id = ?`, rec.PrincipalID).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TokenRecord{}, fmt.Errorf("store: create token for principal %q: %w", rec.PrincipalID, ErrPrincipalNotFound)
		}
		return TokenRecord{}, fmt.Errorf("store: read generation for %q: %w", rec.PrincipalID, err)
	}
	rec.Generation = generation
	rec.CreatedAt = s.now()
	rec.RevokedAt = nil
	rec.LastUsedAt = nil

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO principal_tokens (id, principal_id, digest, hint, label, generation, created_at, expires_at, revoked_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rec.ID, rec.PrincipalID, rec.Digest, rec.Hint, rec.Label, rec.Generation,
		timeToDB(rec.CreatedAt), timePtrToDB(rec.ExpiresAt), nil, nil,
	); err != nil {
		return TokenRecord{}, fmt.Errorf("store: create token for principal %q: %w", rec.PrincipalID, err)
	}
	if err := tx.Commit(); err != nil {
		return TokenRecord{}, fmt.Errorf("store: commit create token for principal %q: %w", rec.PrincipalID, err)
	}
	return rec, nil
}

const tokenColumns = `id, principal_id, digest, hint, label, generation, created_at, expires_at, revoked_at, last_used_at`

func scanToken(row interface{ Scan(dest ...any) error }) (TokenRecord, error) {
	var (
		rec        TokenRecord
		createdAt  string
		expiresAt  sql.NullString
		revokedAt  sql.NullString
		lastUsedAt sql.NullString
	)
	if err := row.Scan(
		&rec.ID, &rec.PrincipalID, &rec.Digest, &rec.Hint, &rec.Label, &rec.Generation,
		&createdAt, &expiresAt, &revokedAt, &lastUsedAt,
	); err != nil {
		return TokenRecord{}, err
	}
	var err error
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return TokenRecord{}, fmt.Errorf("store: parse token created_at: %w", err)
	}
	if rec.ExpiresAt, err = dbToTimePtr(expiresAt); err != nil {
		return TokenRecord{}, fmt.Errorf("store: parse token expires_at: %w", err)
	}
	if rec.RevokedAt, err = dbToTimePtr(revokedAt); err != nil {
		return TokenRecord{}, fmt.Errorf("store: parse token revoked_at: %w", err)
	}
	if rec.LastUsedAt, err = dbToTimePtr(lastUsedAt); err != nil {
		return TokenRecord{}, fmt.Errorf("store: parse token last_used_at: %w", err)
	}
	return rec, nil
}

// ErrTokenNotFound is returned by [Store.GetTokenByDigest] when no
// matching, non-revoked row exists for the given digest.
var ErrTokenNotFound = errors.New("store: token not found")

// GetTokenByDigest looks up a token by its SHA-256 digest. It does NOT
// filter by expiry or revocation — [identity.Service.AuthenticateToken]
// applies those checks itself against its own injected clock, the same
// separation of "stored evidence" from "computed verdict" this package's
// doc comment establishes for node liveness (see store.go's package
// comment) — but it DOES exclude a revoked token, because revocation is a
// permanent, non-time-dependent fact recorded once (unlike expiry, which
// is a live comparison against the current time) and there is no reason
// for any caller to ever need a revoked token's row back by digest.
func (s *Store) GetTokenByDigest(ctx context.Context, digest string) (TokenRecord, error) {
	guardNotInTx(ctx, "Store.GetTokenByDigest")
	row := s.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM principal_tokens WHERE digest = ? AND revoked_at IS NULL`, digest)
	rec, err := scanToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenRecord{}, ErrTokenNotFound
	}
	if err != nil {
		return TokenRecord{}, fmt.Errorf("store: get token by digest: %w", err)
	}
	return rec, nil
}

// TouchToken advances tokenID's last_used_at to now, matching
// [Store.TouchSession]'s reasoning: this is evidence (when a credential was
// last used) tied to the specific request that authenticated with it, so
// the caller's request-scoped now is threaded through explicitly rather
// than this package's own s.now() being trusted to match it exactly.
func (s *Store) TouchToken(ctx context.Context, tokenID string, now time.Time) error {
	guardNotInTx(ctx, "Store.TouchToken")
	_, err := s.db.ExecContext(ctx, `UPDATE principal_tokens SET last_used_at = ? WHERE id = ?`, timeToDB(now), tokenID)
	if err != nil {
		return fmt.Errorf("store: touch token %q: %w", tokenID, err)
	}
	return nil
}

// RevokeToken sets revoked_at on the token with the given (non-secret) row
// id.
func (s *Store) RevokeToken(ctx context.Context, tokenID string) error {
	guardNotInTx(ctx, "Store.RevokeToken")
	now := timeToDB(s.now())
	res, err := s.db.ExecContext(ctx, `UPDATE principal_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, tokenID)
	if err != nil {
		return fmt.Errorf("store: revoke token %q: %w", tokenID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: revoke token %q: %w", tokenID, ErrTokenNotFound)
	}
	return nil
}

// ListTokens returns every non-revoked token belonging to principalID,
// ordered by created_at. Digest is included (a caller inside this
// process, never an HTTP response — see identity's Service doc comment)
// because nothing downstream needs it hidden from Go code that already
// holds a *Store; what must never happen is a digest, let alone a raw
// token, leaving this process in a listing response, and that boundary is
// the API layer's responsibility, not this package's.
func (s *Store) ListTokens(ctx context.Context, principalID string) ([]TokenRecord, error) {
	guardNotInTx(ctx, "Store.ListTokens")
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+tokenColumns+` FROM principal_tokens
		WHERE principal_id = ? AND revoked_at IS NULL
		ORDER BY created_at
	`, principalID)
	if err != nil {
		return nil, fmt.Errorf("store: list tokens for %q: %w", principalID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []TokenRecord
	for rows.Next() {
		rec, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list tokens for %q: %w", principalID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tokens for %q: %w", principalID, err)
	}
	return out, nil
}

// --- principal_sessions ---

// SessionRecord is one row of the principal_sessions table. Digest is the
// SHA-256 hex digest of the session cookie value, never the value itself
// — see [TokenRecord]'s doc comment for the identical reasoning applied
// to tokens, and the identity package's Service doc comment for why
// [identity.Session.ID] is deliberately NOT this Digest and not the raw
// cookie value either.
type SessionRecord struct {
	ID          string
	PrincipalID string
	Digest      string
	DeviceLabel string
	Generation  uint64
	CreatedAt   time.Time
	LastUsedAt  time.Time
	RevokedAt   *time.Time
}

// CreateSession inserts a new session row. rec.ID, PrincipalID, Digest,
// and DeviceLabel must already be set. Generation is NOT taken from rec —
// it is read from principals.generation for PrincipalID inside the same
// transaction as the insert, so the stored value is always the
// principal's true current generation at creation time regardless of what
// a caller guessed it to be (closing the read-then-use race a
// caller-supplied value would otherwise leave open). CreatedAt and
// LastUsedAt both take the caller-supplied now — a session's creation
// time is evidence tied to the specific request that authenticated to
// create it, matching [ADR-024]'s decision 5 explicit now parameter on
// [identity.Service.CreateSession], not this package's own bookkeeping
// clock.
func (s *Store) CreateSession(ctx context.Context, rec SessionRecord, now time.Time) (SessionRecord, error) {
	guardNotInTx(ctx, "Store.CreateSession")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("store: begin create session for %q: %w", rec.PrincipalID, err)
	}
	defer func() { _ = tx.Rollback() }()

	rec, err = createSession(ctx, tx, rec, now)
	if err != nil {
		return SessionRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return SessionRecord{}, fmt.Errorf("store: commit create session for %q: %w", rec.PrincipalID, err)
	}
	return rec, nil
}

// CreateSession is [Store.CreateSession]'s [Tx] form: the identical
// read-generation-then-insert body, run against this already-open
// transaction instead of opening a new one — so a session it creates
// commits or rolls back together with whatever else t's caller composed
// it with (identity.Service's atomic login/bootstrap paths, Step 7 seam
// 0). See store/tx.go's [Tx] doc comment and [appendAuditEntry]'s
// identical querier-based pattern in audit.go.
func (t *Tx) CreateSession(ctx context.Context, rec SessionRecord, now time.Time) (SessionRecord, error) {
	return createSession(ctx, t.tx, rec, now)
}

// createSession is the shared body behind [Store.CreateSession] and
// [Tx.CreateSession]: read PrincipalID's current generation and insert rec
// stamped with it, both against q — written once against [querier] rather
// than twice, per this package's standing rule (see [appendAuditEntry]'s
// doc comment in audit.go for the fuller version of why). Generation is
// NOT taken from rec on input — see [SessionRecord]'s doc comment — so it
// is always the principal's true current generation at creation time
// regardless of what a caller guessed it to be, closing the read-then-use
// race a caller-supplied value would otherwise leave open.
func createSession(ctx context.Context, q querier, rec SessionRecord, now time.Time) (SessionRecord, error) {
	var generation uint64
	if err := q.QueryRowContext(ctx, `SELECT generation FROM principals WHERE id = ?`, rec.PrincipalID).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionRecord{}, fmt.Errorf("store: create session for %q: %w", rec.PrincipalID, ErrPrincipalNotFound)
		}
		return SessionRecord{}, fmt.Errorf("store: read generation for %q: %w", rec.PrincipalID, err)
	}
	rec.Generation = generation
	rec.CreatedAt = now
	rec.LastUsedAt = now
	rec.RevokedAt = nil

	if _, err := q.ExecContext(ctx, `
		INSERT INTO principal_sessions (id, principal_id, digest, device_label, generation, created_at, last_used_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rec.ID, rec.PrincipalID, rec.Digest, rec.DeviceLabel, rec.Generation,
		timeToDB(rec.CreatedAt), timeToDB(rec.LastUsedAt), nil,
	); err != nil {
		return SessionRecord{}, fmt.Errorf("store: create session for %q: %w", rec.PrincipalID, err)
	}
	return rec, nil
}

const sessionColumns = `id, principal_id, digest, device_label, generation, created_at, last_used_at, revoked_at`

func scanSession(row interface{ Scan(dest ...any) error }) (SessionRecord, error) {
	var (
		rec                   SessionRecord
		createdAt, lastUsedAt string
		revokedAt             sql.NullString
	)
	if err := row.Scan(
		&rec.ID, &rec.PrincipalID, &rec.Digest, &rec.DeviceLabel, &rec.Generation,
		&createdAt, &lastUsedAt, &revokedAt,
	); err != nil {
		return SessionRecord{}, err
	}
	var err error
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return SessionRecord{}, fmt.Errorf("store: parse session created_at: %w", err)
	}
	if rec.LastUsedAt, err = dbToTime(lastUsedAt); err != nil {
		return SessionRecord{}, fmt.Errorf("store: parse session last_used_at: %w", err)
	}
	if rec.RevokedAt, err = dbToTimePtr(revokedAt); err != nil {
		return SessionRecord{}, fmt.Errorf("store: parse session revoked_at: %w", err)
	}
	return rec, nil
}

// ErrSessionNotFound is returned by [Store.GetSessionByDigest] when no
// matching, non-revoked row exists.
var ErrSessionNotFound = errors.New("store: session not found")

// GetSessionByDigest looks up a session by its SHA-256 digest, excluding
// revoked rows for the same reason [Store.GetTokenByDigest] excludes
// them. It does NOT check generation against the owning principal or
// apply the 90-day sliding-idle rule (ADR-024 decision 5): both are
// verdicts computed by [identity.Service.AuthenticateSession] against its
// own injected clock, not stored facts this package derives — see this
// method's sibling GetTokenByDigest for the identical division of
// responsibility.
func (s *Store) GetSessionByDigest(ctx context.Context, digest string) (SessionRecord, error) {
	guardNotInTx(ctx, "Store.GetSessionByDigest")
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM principal_sessions WHERE digest = ? AND revoked_at IS NULL`, digest)
	rec, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRecord{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionRecord{}, fmt.Errorf("store: get session by digest: %w", err)
	}
	return rec, nil
}

// TouchSession advances sessionID's last_used_at to now. now is caller-
// supplied rather than this Store's own clock for the same reason
// [Store.CreateSession]'s CreatedAt/LastUsedAt are: ADR-024 decision 5
// slides a session's expiry "on any request that carries the cookie,
// including a read", and the request that triggered this touch is the
// one piece of evidence about when that use occurred.
func (s *Store) TouchSession(ctx context.Context, sessionID string, now time.Time) error {
	guardNotInTx(ctx, "Store.TouchSession")
	_, err := s.db.ExecContext(ctx, `UPDATE principal_sessions SET last_used_at = ? WHERE id = ?`, timeToDB(now), sessionID)
	if err != nil {
		return fmt.Errorf("store: touch session %q: %w", sessionID, err)
	}
	return nil
}

// RevokeSession sets revoked_at on the session with the given (non-secret)
// row id — see [identity.Session.ID]'s doc comment for why this id is
// never the cookie value.
func (s *Store) RevokeSession(ctx context.Context, sessionID string) error {
	guardNotInTx(ctx, "Store.RevokeSession")
	now := timeToDB(s.now())
	res, err := s.db.ExecContext(ctx, `UPDATE principal_sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, sessionID)
	if err != nil {
		return fmt.Errorf("store: revoke session %q: %w", sessionID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: revoke session %q: %w", sessionID, ErrSessionNotFound)
	}
	return nil
}

// ListSessions returns every non-revoked session belonging to principalID,
// ordered by created_at. Digest is included for the identical reason
// [Store.ListTokens] includes token digests — see that method's doc
// comment — and never leaves this process as anything but the
// non-secret [SessionRecord.ID].
func (s *Store) ListSessions(ctx context.Context, principalID string) ([]SessionRecord, error) {
	guardNotInTx(ctx, "Store.ListSessions")
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+sessionColumns+` FROM principal_sessions
		WHERE principal_id = ? AND revoked_at IS NULL
		ORDER BY created_at
	`, principalID)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions for %q: %w", principalID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []SessionRecord
	for rows.Next() {
		rec, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list sessions for %q: %w", principalID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list sessions for %q: %w", principalID, err)
	}
	return out, nil
}

// --- bootstrap ---

// BootstrapRecord is the singleton bootstrap row (id=1; see schemaV5's
// CHECK constraint). CodeDigest is the SHA-256 hex digest of the raw
// bootstrap code — the raw code itself lives only in the file the
// identity package writes to the data volume (ADR-024 decision 9), never
// in this database; see migrations.go's schemaV5 doc comment rule 1.
type BootstrapRecord struct {
	CodeDigest string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ClaimedAt  *time.Time
}

// PutBootstrap replaces the singleton bootstrap row with rec, discarding
// whatever was there before (there is at most ever one unclaimed
// bootstrap code outstanding — see identity package's bootstrap.go for
// when this is called: only when no valid, unclaimed, unexpired row
// already exists, so this never invalidates a code an operator might
// currently be holding). CreatedAt is stamped from this Store's clock and
// ignored on input; ExpiresAt is caller-supplied.
func (s *Store) PutBootstrap(ctx context.Context, rec BootstrapRecord) (BootstrapRecord, error) {
	guardNotInTx(ctx, "Store.PutBootstrap")
	rec.CreatedAt = s.now()
	rec.ClaimedAt = nil
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO bootstrap (id, code_digest, created_at, expires_at, claimed_at)
		VALUES (1, ?, ?, ?, NULL)
		ON CONFLICT(id) DO UPDATE SET
			code_digest = excluded.code_digest,
			created_at  = excluded.created_at,
			expires_at  = excluded.expires_at,
			claimed_at  = NULL
	`, rec.CodeDigest, timeToDB(rec.CreatedAt), timeToDB(rec.ExpiresAt))
	if err != nil {
		return BootstrapRecord{}, fmt.Errorf("store: put bootstrap: %w", err)
	}
	return rec, nil
}

// ErrBootstrapNotFound is returned by [Store.GetBootstrap] when no
// bootstrap row has ever been written (a fresh database before its first
// startup-time generation, or — theoretically — one deleted outside this
// package).
var ErrBootstrapNotFound = errors.New("store: no bootstrap code recorded")

// GetBootstrap returns the current singleton bootstrap row.
func (s *Store) GetBootstrap(ctx context.Context) (BootstrapRecord, error) {
	guardNotInTx(ctx, "Store.GetBootstrap")
	var (
		rec                  BootstrapRecord
		createdAt, expiresAt string
		claimedAt            sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `SELECT code_digest, created_at, expires_at, claimed_at FROM bootstrap WHERE id = 1`).
		Scan(&rec.CodeDigest, &createdAt, &expiresAt, &claimedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BootstrapRecord{}, ErrBootstrapNotFound
	}
	if err != nil {
		return BootstrapRecord{}, fmt.Errorf("store: get bootstrap: %w", err)
	}
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return BootstrapRecord{}, fmt.Errorf("store: parse bootstrap created_at: %w", err)
	}
	if rec.ExpiresAt, err = dbToTime(expiresAt); err != nil {
		return BootstrapRecord{}, fmt.Errorf("store: parse bootstrap expires_at: %w", err)
	}
	if rec.ClaimedAt, err = dbToTimePtr(claimedAt); err != nil {
		return BootstrapRecord{}, fmt.Errorf("store: parse bootstrap claimed_at: %w", err)
	}
	return rec, nil
}

// ClaimBootstrapAndCreatePrincipal marks the bootstrap row claimed and
// creates principal in one transaction — ADR-024 decision 9 requires the
// code to be "invalidated on first successful admin creation", and doing
// both writes together is what stops a crash between them leaving the
// code claimed with no admin to show for it, or an admin created with the
// code still claimable by a second racing request.
//
// The caller (identity.Service.ClaimBootstrap) is expected to fetch the
// bootstrap row via [Store.GetBootstrap] first and validate the presented
// code's digest and expiry against its own clock BEFORE ever calling this
// method — that is the primary validation path, and this method does not
// repeat it: it does not check the code, does not check expiry, and does
// not distinguish "no bootstrap row has ever existed" from "one exists
// but is already claimed". It re-checks only claimed_at IS NULL, via the
// UPDATE's WHERE clause, and returns [ErrBootstrapClaimedRace] wrapped for
// EITHER of those two cases if it affects zero rows — the one thing this
// method exists to close is the race between the caller's GetBootstrap
// check and this write, not to be a second, weaker copy of that check.
func (s *Store) ClaimBootstrapAndCreatePrincipal(ctx context.Context, principal PrincipalRecord) (PrincipalRecord, error) {
	guardNotInTx(ctx, "Store.ClaimBootstrapAndCreatePrincipal")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PrincipalRecord{}, fmt.Errorf("store: begin claim bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rec, err := claimBootstrapAndCreatePrincipal(ctx, tx, s.now(), principal)
	if err != nil {
		return PrincipalRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return PrincipalRecord{}, fmt.Errorf("store: commit claim bootstrap: %w", err)
	}
	return rec, nil
}

// ClaimBootstrapAndCreatePrincipal is [Store.ClaimBootstrapAndCreatePrincipal]'s
// [Tx] form: the identical claim-then-create body, run against this
// already-open transaction instead of opening a new one — this is what
// lets identity.Service.AuditedWrite (Step 7 seam 0) put the bootstrap
// row's claim, the new administrator's creation, and its audit entry in
// ONE transaction, closing the defect ADR-024 names by name: "an audit
// failure on a bootstrap claim leaves the first administrator existing
// with no record of its creation." See store/tx.go's [Tx] doc comment.
func (t *Tx) ClaimBootstrapAndCreatePrincipal(ctx context.Context, principal PrincipalRecord) (PrincipalRecord, error) {
	return claimBootstrapAndCreatePrincipal(ctx, t.tx, t.s.now(), principal)
}

// claimBootstrapAndCreatePrincipal is the shared body behind
// [Store.ClaimBootstrapAndCreatePrincipal] and
// [Tx.ClaimBootstrapAndCreatePrincipal] — written once against [querier]
// rather than twice, per this package's standing rule (see
// [appendAuditEntry]'s doc comment in audit.go). now is passed in rather
// than read from a Store here, since a [Tx] has no independent clock of
// its own beyond its parent Store's (see [Tx]'s doc comment) and both
// callers already have one in hand.
//
// The caller (identity.Service.ClaimBootstrap) is expected to fetch the
// bootstrap row via [Store.GetBootstrap] first and validate the presented
// code's digest and expiry against its own clock BEFORE ever calling this
// — that is the primary validation path, and this function does not
// repeat it: it does not check the code, does not check expiry, and does
// not distinguish "no bootstrap row has ever existed" from "one exists
// but is already claimed". It re-checks only claimed_at IS NULL, via the
// UPDATE's WHERE clause, and returns [ErrBootstrapClaimedRace] wrapped for
// EITHER of those two cases if it affects zero rows — the one thing this
// exists to close is the race between the caller's GetBootstrap check and
// this write, not to be a second, weaker copy of that check.
func claimBootstrapAndCreatePrincipal(ctx context.Context, q querier, now time.Time, principal PrincipalRecord) (PrincipalRecord, error) {
	if principal.ID == ReservedPrincipalID || principal.Name == ReservedPrincipalID {
		return PrincipalRecord{}, ErrReservedPrincipal
	}
	res, err := q.ExecContext(ctx, `UPDATE bootstrap SET claimed_at = ? WHERE id = 1 AND claimed_at IS NULL`, timeToDB(now))
	if err != nil {
		return PrincipalRecord{}, fmt.Errorf("store: claim bootstrap: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return PrincipalRecord{}, fmt.Errorf("store: claim bootstrap: %w", err)
	}
	if n == 0 {
		return PrincipalRecord{}, fmt.Errorf("store: claim bootstrap: %w", ErrBootstrapClaimedRace)
	}

	principal.CreatedAt = now
	principal.UpdatedAt = now
	principal.Generation = 0
	if _, err := q.ExecContext(ctx, `
		INSERT INTO principals (id, name, kind, role, password_hash, disabled, generation, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		principal.ID, principal.Name, principal.Kind, principal.Role, principal.PasswordHash,
		boolToDB(principal.Disabled), principal.Generation, timeToDB(principal.CreatedAt), timeToDB(principal.UpdatedAt),
	); err != nil {
		if isUniqueConstraintErr(err) {
			return PrincipalRecord{}, fmt.Errorf("store: claim bootstrap: %w", ErrPrincipalNameTaken)
		}
		return PrincipalRecord{}, fmt.Errorf("store: claim bootstrap: create principal: %w", err)
	}
	return principal, nil
}

// ErrBootstrapClaimedRace is returned by
// [Store.ClaimBootstrapAndCreatePrincipal] when the bootstrap row's
// claimed_at was no longer NULL at the moment of the UPDATE — a second
// request won a race against the first. Distinct from
// [identity.ErrBootstrapClaimed] (the identity package's own sentinel for
// its GetBootstrap-then-validate path) so a caller can tell "you lost a
// race with another concurrent claim" apart from "you checked staleness
// yourself and it was already claimed", though both describe the same
// user-visible outcome.
var ErrBootstrapClaimedRace = errors.New("store: bootstrap code was claimed by a concurrent request")

// isUniqueConstraintErr reports whether err is modernc.org/sqlite's
// representation of a UNIQUE constraint violation. modernc.org/sqlite
// (unlike mattn/go-sqlite3, which ADR-012 forbids) does not export a typed
// sentinel for this the way database/sql's own errors work, so this
// checks the driver's error string for SQLite's own constraint-violation
// text — brittle in the abstract, but pinned by
// TestCreatePrincipalDuplicateNameIsErrPrincipalNameTaken and
// TestCreateTokenDuplicateDigestIsError in identity_test.go, so a driver
// upgrade that changes this text fails a test rather than silently
// starting to report every duplicate insert as a generic error.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV6's config_objects/config_revisions repository
// methods (Step 7 seam 0; RES-008 D1's generic storage). It knows nothing
// about what any particular config kind's payload_json actually contains
// — that belongs to whichever seam owns a kind (e.g. seam A's FPP
// endpoints config, migrated out of SHOWMESH_FPP_ENDPOINTS) — this file
// only ever treats payload_json as an opaque string. See migrations.go's
// schemaV6 doc comment for why config_revisions is immutable (no
// UpdateConfigRevision method, and there must never be one) and never
// pruned.

// ConfigObjectRecord is one row of the config_objects table: the mutable
// pointer at a configuration object's currently-active revision.
// CurrentRevision == 0 means "no revision has ever been activated": see
// migrations.go's schemaV6 doc comment. DeletedAt is nil for a live
// object, and set the moment [Store.TombstoneConfigObject]/
// [Tx.TombstoneConfigObject] runs: see migrations.go's schemaV30 doc
// comment. [Store.GetConfigObject]/[Tx.GetConfigObject] and
// [Store.ListConfigObjects]/[Tx.ListConfigObjects] never return a
// tombstoned row (DeletedAt is always nil on whatever they do return);
// only [Store.GetConfigObjectIncludingDeleted]/
// [Tx.GetConfigObjectIncludingDeleted] can observe it.
type ConfigObjectRecord struct {
	Kind            string
	ID              string
	CurrentRevision int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// ConfigRevisionRecord is one row of the config_revisions table: an
// immutable, numbered snapshot of a configuration object's payload.
// Revision is caller-assigned (typically the object's current highest
// revision + 1) rather than auto-incremented by this package, because a
// caller (identity.Service.AuditedWrite's closure) computing it as part of
// composing an atomic read-then-write is exactly the case
// [store.Tx] exists for.
type ConfigRevisionRecord struct {
	Kind                   string
	ObjectID               string
	Revision               int64
	PayloadJSON            string
	CreatedAt              time.Time
	CreatedByPrincipalID   string
	CreatedByPrincipalName string
	Source                 string // "api" | "env_migration"
	Note                   string
}

// ErrConfigRevisionExists is returned when (Kind, ObjectID, Revision)
// already has a row — config_revisions' PRIMARY KEY, per ADR-009's
// immutability rule: a second write of an existing revision is always a
// caller bug (a race on computing the next revision number, or a retried
// request that should have used a fresh number), never a condition this
// package silently overwrites.
var ErrConfigRevisionExists = errors.New("store: config revision already exists")

// ErrConfigObjectNotFound is returned by [Store.GetConfigObject]/
// [Tx.GetConfigObject] when no row exists for (kind, id).
var ErrConfigObjectNotFound = errors.New("store: config object not found")

// ErrConfigObjectExists is returned by [Store.CreateConfigObject]/
// [Tx.CreateConfigObject] when (kind, id) already has a row — the config
// object equivalent of [ErrPrincipalNameTaken]/[ErrConfigRevisionExists]:
// a second creation attempt is always a caller bug (a retried request, or
// code that forgot it already created this object), never a condition
// this package silently no-ops or overwrites.
var ErrConfigObjectExists = errors.New("store: config object already exists")

// ErrConfigRevisionNotFound is returned by [Store.GetConfigRevision]/
// [Tx.GetConfigRevision] when no row exists for (kind, objectID, revision).
var ErrConfigRevisionNotFound = errors.New("store: config revision not found")

// --- config_revisions ---

func createConfigRevision(ctx context.Context, q querier, rec ConfigRevisionRecord, now time.Time) (ConfigRevisionRecord, error) {
	rec.CreatedAt = now
	_, err := q.ExecContext(ctx, `
		INSERT INTO config_revisions (
			kind, object_id, revision, payload_json, created_at,
			created_by_principal_id, created_by_principal_name, source, note
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rec.Kind, rec.ObjectID, rec.Revision, rec.PayloadJSON, timeToDB(rec.CreatedAt),
		rec.CreatedByPrincipalID, rec.CreatedByPrincipalName, rec.Source, rec.Note,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return ConfigRevisionRecord{}, fmt.Errorf("store: create config revision %s/%s/%d: %w",
				rec.Kind, rec.ObjectID, rec.Revision, ErrConfigRevisionExists)
		}
		return ConfigRevisionRecord{}, fmt.Errorf("store: create config revision %s/%s/%d: %w", rec.Kind, rec.ObjectID, rec.Revision, err)
	}
	return rec, nil
}

// CreateConfigRevision inserts a new, immutable revision. A single INSERT
// needs no transaction of its own beyond SQLite's own per-statement
// atomicity, so — unlike [Store.AppendAuditEntry] — this never opens one;
// a caller that needs this write to share a transaction with something
// else (activating it, writing an audit entry) uses [Tx.CreateConfigRevision]
// via [Store.InTx] or [identity.Service.AuditedWrite].
func (s *Store) CreateConfigRevision(ctx context.Context, rec ConfigRevisionRecord) (ConfigRevisionRecord, error) {
	guardNotInTx(ctx, "Store.CreateConfigRevision")
	return createConfigRevision(ctx, s.db, rec, s.now())
}

// CreateConfigRevision is [Store.CreateConfigRevision]'s [Tx] form.
func (t *Tx) CreateConfigRevision(ctx context.Context, rec ConfigRevisionRecord) (ConfigRevisionRecord, error) {
	return createConfigRevision(ctx, t.tx, rec, t.s.now())
}

const configRevisionColumns = `
	kind, object_id, revision, payload_json, created_at,
	created_by_principal_id, created_by_principal_name, source, note
`

func scanConfigRevision(row interface{ Scan(dest ...any) error }) (ConfigRevisionRecord, error) {
	var (
		rec       ConfigRevisionRecord
		createdAt string
	)
	if err := row.Scan(
		&rec.Kind, &rec.ObjectID, &rec.Revision, &rec.PayloadJSON, &createdAt,
		&rec.CreatedByPrincipalID, &rec.CreatedByPrincipalName, &rec.Source, &rec.Note,
	); err != nil {
		return ConfigRevisionRecord{}, err
	}
	var err error
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return ConfigRevisionRecord{}, fmt.Errorf("store: parse config revision created_at: %w", err)
	}
	return rec, nil
}

func getConfigRevision(ctx context.Context, q querier, kind, objectID string, revision int64) (ConfigRevisionRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+configRevisionColumns+`FROM config_revisions WHERE kind = ? AND object_id = ? AND revision = ?`,
		kind, objectID, revision)
	rec, err := scanConfigRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigRevisionRecord{}, ErrConfigRevisionNotFound
	}
	if err != nil {
		return ConfigRevisionRecord{}, fmt.Errorf("store: get config revision %s/%s/%d: %w", kind, objectID, revision, err)
	}
	return rec, nil
}

// GetConfigRevision returns one immutable revision, or [ErrConfigRevisionNotFound].
func (s *Store) GetConfigRevision(ctx context.Context, kind, objectID string, revision int64) (ConfigRevisionRecord, error) {
	guardNotInTx(ctx, "Store.GetConfigRevision")
	return getConfigRevision(ctx, s.db, kind, objectID, revision)
}

// GetConfigRevision is [Store.GetConfigRevision]'s [Tx] form.
func (t *Tx) GetConfigRevision(ctx context.Context, kind, objectID string, revision int64) (ConfigRevisionRecord, error) {
	return getConfigRevision(ctx, t.tx, kind, objectID, revision)
}

func listConfigRevisions(ctx context.Context, q querier, kind, objectID string) ([]ConfigRevisionRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT`+configRevisionColumns+`FROM config_revisions WHERE kind = ? AND object_id = ? ORDER BY revision`,
		kind, objectID)
	if err != nil {
		return nil, fmt.Errorf("store: list config revisions %s/%s: %w", kind, objectID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []ConfigRevisionRecord
	for rows.Next() {
		rec, err := scanConfigRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list config revisions %s/%s: %w", kind, objectID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list config revisions %s/%s: %w", kind, objectID, err)
	}
	return out, nil
}

// ListConfigRevisions returns every revision of (kind, objectID), oldest
// first — the full, immutable history ADR-009 requires be available for
// rollback. Never paginated or bounded: nothing in this package ever
// prunes config_revisions (see migrations.go's schemaV6 doc comment), so
// there is no retention-driven gap to report the way [Store.ListEvents]
// must.
func (s *Store) ListConfigRevisions(ctx context.Context, kind, objectID string) ([]ConfigRevisionRecord, error) {
	guardNotInTx(ctx, "Store.ListConfigRevisions")
	return listConfigRevisions(ctx, s.db, kind, objectID)
}

// ListConfigRevisions is [Store.ListConfigRevisions]'s [Tx] form.
func (t *Tx) ListConfigRevisions(ctx context.Context, kind, objectID string) ([]ConfigRevisionRecord, error) {
	return listConfigRevisions(ctx, t.tx, kind, objectID)
}

// --- config_objects ---

const configObjectColumns = `kind, id, current_revision, created_at, updated_at, deleted_at`

func scanConfigObject(row interface{ Scan(dest ...any) error }) (ConfigObjectRecord, error) {
	var (
		rec                  ConfigObjectRecord
		createdAt, updatedAt string
		deletedAt            sql.NullString
	)
	if err := row.Scan(&rec.Kind, &rec.ID, &rec.CurrentRevision, &createdAt, &updatedAt, &deletedAt); err != nil {
		return ConfigObjectRecord{}, err
	}
	var err error
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return ConfigObjectRecord{}, fmt.Errorf("store: parse config object created_at: %w", err)
	}
	if rec.UpdatedAt, err = dbToTime(updatedAt); err != nil {
		return ConfigObjectRecord{}, fmt.Errorf("store: parse config object updated_at: %w", err)
	}
	if rec.DeletedAt, err = dbToTimePtr(deletedAt); err != nil {
		return ConfigObjectRecord{}, fmt.Errorf("store: parse config object deleted_at: %w", err)
	}
	return rec, nil
}

func createConfigObject(ctx context.Context, q querier, kind, id string, now time.Time) (ConfigObjectRecord, error) {
	nowStr := timeToDB(now)
	_, err := q.ExecContext(ctx, `
		INSERT INTO config_objects (kind, id, current_revision, created_at, updated_at)
		VALUES (?, ?, 0, ?, ?)
	`, kind, id, nowStr, nowStr)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return ConfigObjectRecord{}, fmt.Errorf("store: create config object %s/%s: %w", kind, id, ErrConfigObjectExists)
		}
		return ConfigObjectRecord{}, fmt.Errorf("store: create config object %s/%s: %w", kind, id, err)
	}
	return getConfigObject(ctx, q, kind, id)
}

// CreateConfigObject establishes (kind, id) at current_revision = 0 — "no
// revision activated yet", the state migrations.go's schemaV6 doc comment
// documents but that [ActivateConfigRevision] alone can never reach,
// because ActivateConfigRevision always sets a caller-supplied revision
// (F11 review finding: config_objects.current_revision = 0 was documented
// but unreachable — the only writer of that table always wrote a non-zero
// value on its very first write, via the ON CONFLICT upsert's INSERT
// branch). This is the creation path seam A needs: declare a config
// object exists, with nothing active yet, before any revision has been
// written for it. Calling ActivateConfigRevision on an object that has
// never been created still works exactly as before (it creates the row
// implicitly with the caller's revision already active) — this method
// exists so "declared, nothing active" is also a state a caller can
// deliberately produce, not so it becomes the only path to a
// config_objects row.
func (s *Store) CreateConfigObject(ctx context.Context, kind, id string) (ConfigObjectRecord, error) {
	guardNotInTx(ctx, "Store.CreateConfigObject")
	return createConfigObject(ctx, s.db, kind, id, s.now())
}

// CreateConfigObject is [Store.CreateConfigObject]'s [Tx] form.
func (t *Tx) CreateConfigObject(ctx context.Context, kind, id string) (ConfigObjectRecord, error) {
	return createConfigObject(ctx, t.tx, kind, id, t.s.now())
}

// getConfigObject is the LIVE read: a tombstoned row (deleted_at NOT NULL)
// reads back identically to no row at all, [ErrConfigObjectNotFound]. This
// is deliberately the DEFAULT so that every present and future caller of
// [Store.GetConfigObject]/[Tx.GetConfigObject] excludes a deleted object
// without having to know this feature exists: see
// [getConfigObjectIncludingDeleted] for the two call sites (the PUT
// re-create path, and TombstoneConfigObject's own precondition/audit read)
// that must see the true row instead.
func getConfigObject(ctx context.Context, q querier, kind, id string) (ConfigObjectRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT `+configObjectColumns+` FROM config_objects WHERE kind = ? AND id = ? AND deleted_at IS NULL`, kind, id)
	rec, err := scanConfigObject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigObjectRecord{}, ErrConfigObjectNotFound
	}
	if err != nil {
		return ConfigObjectRecord{}, fmt.Errorf("store: get config object %s/%s: %w", kind, id, err)
	}
	return rec, nil
}

// GetConfigObject returns (kind, id)'s pointer row, or
// [ErrConfigObjectNotFound], including when the row exists but is
// tombstoned (schemaV30's doc comment): a deleted object is absent from
// every existence check and resolution path that already treats
// [ErrConfigObjectNotFound] as "nothing here", with no per-caller change
// required. See [Store.GetConfigObjectIncludingDeleted] for the narrow
// exception.
func (s *Store) GetConfigObject(ctx context.Context, kind, id string) (ConfigObjectRecord, error) {
	guardNotInTx(ctx, "Store.GetConfigObject")
	return getConfigObject(ctx, s.db, kind, id)
}

// GetConfigObject is [Store.GetConfigObject]'s [Tx] form.
func (t *Tx) GetConfigObject(ctx context.Context, kind, id string) (ConfigObjectRecord, error) {
	return getConfigObject(ctx, t.tx, kind, id)
}

// getConfigObjectIncludingDeleted is [getConfigObject] without the
// deleted_at filter: the TRUE row, tombstoned or not. Named so nobody
// reaches for it by accident in place of the live-only default.
func getConfigObjectIncludingDeleted(ctx context.Context, q querier, kind, id string) (ConfigObjectRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT `+configObjectColumns+` FROM config_objects WHERE kind = ? AND id = ?`, kind, id)
	rec, err := scanConfigObject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigObjectRecord{}, ErrConfigObjectNotFound
	}
	if err != nil {
		return ConfigObjectRecord{}, fmt.Errorf("store: get config object (including deleted) %s/%s: %w", kind, id, err)
	}
	return rec, nil
}

// GetConfigObjectIncludingDeleted is [Store.GetConfigObject] without the
// tombstone filter. Reach for this ONLY when the caller genuinely needs to
// see a deleted row: today that is the PUT re-create path (which must
// compute its next revision number from the object's true current_revision,
// not from an object that looks absent) and [Store.TombstoneConfigObject]'s
// own precondition/audit read. Every other read of "is this object live"
// wants the plain [Store.GetConfigObject] instead.
func (s *Store) GetConfigObjectIncludingDeleted(ctx context.Context, kind, id string) (ConfigObjectRecord, error) {
	guardNotInTx(ctx, "Store.GetConfigObjectIncludingDeleted")
	return getConfigObjectIncludingDeleted(ctx, s.db, kind, id)
}

// GetConfigObjectIncludingDeleted is [Store.GetConfigObjectIncludingDeleted]'s [Tx] form.
func (t *Tx) GetConfigObjectIncludingDeleted(ctx context.Context, kind, id string) (ConfigObjectRecord, error) {
	return getConfigObjectIncludingDeleted(ctx, t.tx, kind, id)
}

// listConfigObjects excludes a tombstoned row, for the identical reason
// [getConfigObject] does: every list endpoint in this codebase already
// treats this result as the full membership of a kind, with no per-caller
// filter of its own to remember to add.
func listConfigObjects(ctx context.Context, q querier, kind string) ([]ConfigObjectRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if kind == "" {
		rows, err = q.QueryContext(ctx, `SELECT `+configObjectColumns+` FROM config_objects WHERE deleted_at IS NULL ORDER BY kind, id`)
	} else {
		rows, err = q.QueryContext(ctx, `SELECT `+configObjectColumns+` FROM config_objects WHERE kind = ? AND deleted_at IS NULL ORDER BY id`, kind)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list config objects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ConfigObjectRecord
	for rows.Next() {
		rec, err := scanConfigObject(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list config objects: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list config objects: %w", err)
	}
	return out, nil
}

// ListConfigObjects returns every config_objects row, or (if kind is
// non-empty) every row of that kind, ordered for a stable, deterministic
// result.
func (s *Store) ListConfigObjects(ctx context.Context, kind string) ([]ConfigObjectRecord, error) {
	guardNotInTx(ctx, "Store.ListConfigObjects")
	return listConfigObjects(ctx, s.db, kind)
}

// ListConfigObjects is [Store.ListConfigObjects]'s [Tx] form.
func (t *Tx) ListConfigObjects(ctx context.Context, kind string) ([]ConfigObjectRecord, error) {
	return listConfigObjects(ctx, t.tx, kind)
}

// activateConfigRevision's upsert unconditionally clears deleted_at, on
// both the INSERT branch (a fresh row has no tombstone to begin with) and
// the ON CONFLICT UPDATE branch (an existing row's deleted_at is set back
// to NULL). This is what makes re-creating a tombstoned object a plain PUT
// with no special-casing anywhere in the API layer: activating ANY
// revision, including the very first one after a delete, is what "this
// object is live again" means, and current_revision keeps climbing from
// wherever it last stood (schemaV30's doc comment), never resetting to 1
// and never colliding with a revision this object already used.
func activateConfigRevision(ctx context.Context, q querier, kind, id string, revision int64, now time.Time) (ConfigObjectRecord, error) {
	nowStr := timeToDB(now)
	_, err := q.ExecContext(ctx, `
		INSERT INTO config_objects (kind, id, current_revision, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, NULL)
		ON CONFLICT(kind, id) DO UPDATE SET
			current_revision = excluded.current_revision,
			updated_at       = excluded.updated_at,
			deleted_at       = NULL
	`, kind, id, revision, nowStr, nowStr)
	if err != nil {
		return ConfigObjectRecord{}, fmt.Errorf("store: activate config revision %s/%s/%d: %w", kind, id, revision, err)
	}
	return getConfigObject(ctx, q, kind, id)
}

// ActivateConfigRevision sets (kind, id)'s current_revision to revision,
// creating the config_objects row first (current_revision's prior value
// implicitly 0, "no revision activated yet") if it does not already
// exist. This is the ONLY mutable value schemaV6 defines for
// configuration — see migrations.go's schemaV6 doc comment: there is no
// UpdateConfigRevision method, and there must never be one.
// ActivateConfigRevision does not itself verify that a config_revisions
// row for revision exists; the caller is expected to activate only a
// revision it just created via [Store.CreateConfigRevision]/
// [Tx.CreateConfigRevision] or already knows exists via
// [Store.GetConfigRevision].
//
// F10: this means config_objects.current_revision CAN point at a revision
// that does not exist in config_revisions — a caller-error case this
// method trusts its caller not to hit, matching this package's general
// posture elsewhere (e.g. [Store.AppendEvent]'s identical trust of its
// caller for Category/Severity's vocabulary), not a runtime check this
// method performs. Seam A, reading config_objects to resolve "what's
// currently active", must not assume [Store.GetConfigRevision] against
// that pointer will succeed — it must handle [ErrConfigRevisionNotFound]
// as a real outcome and report it per ADR-020's absent-evidence rule,
// never treat GetConfigRevision's success as guaranteed by
// current_revision's mere existence.
func (s *Store) ActivateConfigRevision(ctx context.Context, kind, id string, revision int64) (ConfigObjectRecord, error) {
	guardNotInTx(ctx, "Store.ActivateConfigRevision")
	return activateConfigRevision(ctx, s.db, kind, id, revision, s.now())
}

// ActivateConfigRevision is [Store.ActivateConfigRevision]'s [Tx] form —
// this is what lets a caller create a revision, activate it, and write
// its audit entry as one atomic unit via [identity.Service.AuditedWrite].
func (t *Tx) ActivateConfigRevision(ctx context.Context, kind, id string, revision int64) (ConfigObjectRecord, error) {
	return activateConfigRevision(ctx, t.tx, kind, id, revision, t.s.now())
}

// tombstoneConfigObject sets (kind, id)'s deleted_at to now, refusing (with
// [ErrConfigObjectNotFound]) when no live row exists, whether because
// nothing was ever created for (kind, id), or because it is already
// tombstoned. Both read identically to the caller: a second DELETE of an
// already-deleted object is refused the same way a DELETE of an object
// that never existed is, matching [handleDeleteNodeDeclaration]'s existing
// precedent for this codebase's one other hard-delete-shaped operation.
// config_revisions is never touched: see migrations.go's schemaV30 doc
// comment.
func tombstoneConfigObject(ctx context.Context, q querier, kind, id string, now time.Time) (ConfigObjectRecord, error) {
	nowStr := timeToDB(now)
	res, err := q.ExecContext(ctx, `
		UPDATE config_objects SET deleted_at = ?, updated_at = ?
		WHERE kind = ? AND id = ? AND deleted_at IS NULL
	`, nowStr, nowStr, kind, id)
	if err != nil {
		return ConfigObjectRecord{}, fmt.Errorf("store: tombstone config object %s/%s: %w", kind, id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ConfigObjectRecord{}, fmt.Errorf("store: tombstone config object %s/%s: %w", kind, id, err)
	}
	if n == 0 {
		return ConfigObjectRecord{}, ErrConfigObjectNotFound
	}
	return getConfigObjectIncludingDeleted(ctx, q, kind, id)
}

// TombstoneConfigObject deletes (kind, id) by tombstone. config_objects'
// row is marked deleted_at, config_revisions is never touched, and the
// object is immediately absent from [Store.GetConfigObject]/
// [Store.ListConfigObjects] and from every resolution path built on them.
// Returns [ErrConfigObjectNotFound] when (kind, id) has no live row to
// delete. See migrations.go's schemaV30 doc comment.
func (s *Store) TombstoneConfigObject(ctx context.Context, kind, id string) (ConfigObjectRecord, error) {
	guardNotInTx(ctx, "Store.TombstoneConfigObject")
	return tombstoneConfigObject(ctx, s.db, kind, id, s.now())
}

// TombstoneConfigObject is [Store.TombstoneConfigObject]'s [Tx] form: the
// form the API layer actually calls, so the tombstone and its audit entry
// land in one transaction via [identity.Service.AuditedWrite], exactly
// like every other config write in this codebase (ADR-024 decision 11).
func (t *Tx) TombstoneConfigObject(ctx context.Context, kind, id string) (ConfigObjectRecord, error) {
	return tombstoneConfigObject(ctx, t.tx, kind, id, t.s.now())
}

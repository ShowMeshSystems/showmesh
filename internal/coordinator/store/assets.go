package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file holds schemaV8's assets table repository methods (Track E,
// seams E3/E4; ADR-028). It knows nothing about backend bytes — the
// assetstore package addresses those — and treats every column here as
// metadata only, per ADR-028 decision 4.

// Asset target kinds. Not enforced as a closed vocabulary by this package
// (matching commands.go's Action/TargetKind and config.go's Source
// precedent), but exported so callers share one spelling.
const (
	AssetTargetKindNode = "node"
	AssetTargetKindShow = "show"
)

// AssetRecord is one row of the assets table: an artifact's metadata,
// never its bytes (ADR-028 decision 4). SupersededAt is nil for the
// current asset serving (ShowID, SequenceID, TargetKind, TargetID) — the
// schemaV8 assets_current partial unique index makes a second such row for
// the same tuple structurally impossible. RuntimeFilename is preserved but
// is never part of any lookup this package performs (ADR-028 decision 1):
// three different targets' artifacts for one xLights sequence share a
// filename, and identity here is the (show, sequence, target, hash) tuple,
// never the name.
type AssetRecord struct {
	ID              string
	ShowID          string
	SequenceID      string
	TargetKind      string // AssetTargetKindNode | AssetTargetKindShow
	TargetID        string // node id, or "" when TargetKind == AssetTargetKindShow
	MediaType       string // "fseq" | "audio" | "media"
	ContentHash     string // "sha256:<hex>"
	RuntimeFilename string
	SizeBytes       int64
	Backend         string // "volume"
	StorageKey      string
	// CreatedAt is ingestion time only, never a currentness signal: a
	// rollback (rollbackAsset) restores a row without touching it, so the
	// current row can hold an older CreatedAt than the row it superseded.
	// SupersededAt nil is the sole test for current.
	CreatedAt              time.Time
	CreatedByPrincipalID   string
	CreatedByPrincipalName string
	SupersededAt           *time.Time
}

// ErrAssetNotFound is returned by [Store.GetAsset]/[Tx.GetAsset] when no
// row exists for the given id.
var ErrAssetNotFound = errors.New("store: asset not found")

// ErrAssetExists is the sentinel [errors.Is] matches against any
// [*AssetIdentityExistsError] — see that type's doc comment.
var ErrAssetExists = errors.New("store: asset identity already exists")

// AssetIdentityExistsError is returned by [Store.CreateAsset]/
// [Tx.CreateAsset], carrying the pre-existing row, when (ShowID,
// SequenceID, TargetKind, TargetID, ContentHash) already has a row — the
// schemaV8 assets_identity unique index's key. Re-uploading identical
// bytes for an identity already registered is the idempotent case spec
// §3.3 requires ("200 with the existing asset, no new row, no new blob"):
// the caller reads Existing off this error rather than issuing a second
// lookup, mirroring [DuplicateMacroRunError]'s exact shape in
// macro_runs.go.
type AssetIdentityExistsError struct {
	Existing AssetRecord
}

func (e *AssetIdentityExistsError) Error() string {
	return fmt.Sprintf("store: asset identity %s/%s/%s/%s/%s already exists as %q",
		e.Existing.ShowID, e.Existing.SequenceID, e.Existing.TargetKind, e.Existing.TargetID, e.Existing.ContentHash, e.Existing.ID)
}

// Unwrap makes errors.Is(err, ErrAssetExists) true for any
// *AssetIdentityExistsError.
func (e *AssetIdentityExistsError) Unwrap() error { return ErrAssetExists }

const assetColumns = `
	id, show_id, sequence_id, target_kind, target_id, media_type, content_hash,
	runtime_filename, size_bytes, backend, storage_key, created_at,
	created_by_principal_id, created_by_principal_name, superseded_at
`

func scanAsset(row interface{ Scan(dest ...any) error }) (AssetRecord, error) {
	var (
		rec          AssetRecord
		createdAt    string
		supersededAt sql.NullString
	)
	if err := row.Scan(
		&rec.ID, &rec.ShowID, &rec.SequenceID, &rec.TargetKind, &rec.TargetID, &rec.MediaType, &rec.ContentHash,
		&rec.RuntimeFilename, &rec.SizeBytes, &rec.Backend, &rec.StorageKey, &createdAt,
		&rec.CreatedByPrincipalID, &rec.CreatedByPrincipalName, &supersededAt,
	); err != nil {
		return AssetRecord{}, err
	}
	var err error
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return AssetRecord{}, fmt.Errorf("store: parse asset created_at: %w", err)
	}
	if rec.SupersededAt, err = dbToTimePtr(supersededAt); err != nil {
		return AssetRecord{}, fmt.Errorf("store: parse asset superseded_at: %w", err)
	}
	return rec, nil
}

func getAssetByIdentity(ctx context.Context, q querier, showID, sequenceID, targetKind, targetID, contentHash string) (AssetRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+assetColumns+`FROM assets
		WHERE show_id = ? AND sequence_id = ? AND target_kind = ? AND target_id = ? AND content_hash = ?`,
		showID, sequenceID, targetKind, targetID, contentHash)
	rec, err := scanAsset(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetRecord{}, ErrAssetNotFound
	}
	if err != nil {
		return AssetRecord{}, fmt.Errorf("store: get asset by identity: %w", err)
	}
	return rec, nil
}

// createAsset is [Store.CreateAsset]/[Tx.CreateAsset]'s shared body; the
// caller guarantees it runs inside a transaction. A still-current identity
// match is an idempotent no-op ([AssetIdentityExistsError]); a superseded
// match rolls back ([rollbackAsset]); otherwise it supersedes any current
// row for the tuple and inserts the new row as current.
func createAsset(ctx context.Context, q querier, rec AssetRecord, now time.Time) (AssetRecord, bool, error) {
	switch {
	case rec.ID == "":
		return AssetRecord{}, false, fmt.Errorf("store: create asset: ID is empty")
	case rec.ShowID == "":
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: ShowID is empty", rec.ID)
	case rec.SequenceID == "":
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: SequenceID is empty", rec.ID)
	case rec.TargetKind == "":
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: TargetKind is empty", rec.ID)
	case rec.MediaType == "":
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: MediaType is empty", rec.ID)
	case rec.ContentHash == "":
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: ContentHash is empty", rec.ID)
	case rec.RuntimeFilename == "":
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: RuntimeFilename is empty", rec.ID)
	case rec.Backend == "":
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: Backend is empty", rec.ID)
	case rec.StorageKey == "":
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: StorageKey is empty", rec.ID)
	}

	// 1. Identity lookup — see this function's doc comment.
	existing, err := getAssetByIdentity(ctx, q, rec.ShowID, rec.SequenceID, rec.TargetKind, rec.TargetID, rec.ContentHash)
	switch {
	case err == nil && existing.SupersededAt == nil:
		return AssetRecord{}, false, &AssetIdentityExistsError{Existing: existing}
	case err == nil:
		return rollbackAsset(ctx, q, existing, now)
	case !errors.Is(err, ErrAssetNotFound):
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: check identity: %w", rec.ID, err)
	}

	// 2. Supersede whatever is current for this (show, sequence, target), if
	// anything — before the insert, so the schemaV8 assets_current partial
	// unique index never sees two current rows even transiently.
	if err := supersedeCurrentAsset(ctx, q, rec.ShowID, rec.SequenceID, rec.TargetKind, rec.TargetID, now); err != nil {
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: supersede prior current asset: %w", rec.ID, err)
	}

	// 3. Insert the new row as current.
	rec.CreatedAt = now
	rec.SupersededAt = nil
	_, err = q.ExecContext(ctx, `
		INSERT INTO assets (
			id, show_id, sequence_id, target_kind, target_id, media_type, content_hash,
			runtime_filename, size_bytes, backend, storage_key, created_at,
			created_by_principal_id, created_by_principal_name, superseded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`,
		rec.ID, rec.ShowID, rec.SequenceID, rec.TargetKind, rec.TargetID, rec.MediaType, rec.ContentHash,
		rec.RuntimeFilename, rec.SizeBytes, rec.Backend, rec.StorageKey, timeToDB(rec.CreatedAt),
		rec.CreatedByPrincipalID, rec.CreatedByPrincipalName,
	)
	if err != nil {
		// The identity check above already ruled out an assets_identity
		// collision on this single connection (store.go caps the pool at 1,
		// exactly as macro_runs.go's createMacroRun relies on for its
		// identical two-guards-then-insert shape), so a UNIQUE violation
		// reaching here is an id collision: a caller-supplied duplicate ID,
		// never a legitimate identity re-upload.
		if isUniqueConstraintErr(err) {
			return AssetRecord{}, false, fmt.Errorf("store: create asset: id %q already exists: %w", rec.ID, err)
		}
		return AssetRecord{}, false, fmt.Errorf("store: create asset %q: %w", rec.ID, err)
	}
	return rec, false, nil
}

// supersedeCurrentAsset marks whatever row currently holds (showID,
// sequenceID, targetKind, targetID), if any, as superseded at now. A no-op,
// not an error, when nothing is current for the tuple.
func supersedeCurrentAsset(ctx context.Context, q querier, showID, sequenceID, targetKind, targetID string, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		UPDATE assets SET superseded_at = ?
		WHERE show_id = ? AND sequence_id = ? AND target_kind = ? AND target_id = ? AND superseded_at IS NULL
	`, timeToDB(now), showID, sequenceID, targetKind, targetID)
	return err
}

// rollbackAsset (ADR-028 decision 10) un-supersedes existing and supersedes
// whatever is current now, in one transaction the caller guarantees.
// existing is never itself current.
func rollbackAsset(ctx context.Context, q querier, existing AssetRecord, now time.Time) (AssetRecord, bool, error) {
	if err := supersedeCurrentAsset(ctx, q, existing.ShowID, existing.SequenceID, existing.TargetKind, existing.TargetID, now); err != nil {
		return AssetRecord{}, false, fmt.Errorf("store: rollback asset %q: supersede current asset: %w", existing.ID, err)
	}
	if _, err := q.ExecContext(ctx, `UPDATE assets SET superseded_at = NULL WHERE id = ?`, existing.ID); err != nil {
		return AssetRecord{}, false, fmt.Errorf("store: rollback asset %q: un-supersede: %w", existing.ID, err)
	}
	existing.SupersededAt = nil
	return existing, true, nil
}

func getCurrentAssetForTuple(ctx context.Context, q querier, showID, sequenceID, targetKind, targetID string) (AssetRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+assetColumns+`FROM assets
		WHERE show_id = ? AND sequence_id = ? AND target_kind = ? AND target_id = ? AND superseded_at IS NULL`,
		showID, sequenceID, targetKind, targetID)
	rec, err := scanAsset(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetRecord{}, ErrAssetNotFound
	}
	if err != nil {
		return AssetRecord{}, fmt.Errorf("store: get current asset for tuple: %w", err)
	}
	return rec, nil
}

// GetCurrentAssetForTuple returns the current (superseded_at IS NULL) asset
// for one (showID, sequenceID, targetKind, targetID), or [ErrAssetNotFound]
// when nothing is current yet. Callers needing the row a write is about to
// displace — e.g. a rollback audit entry — must read this INSIDE the same
// transaction as the write, before it runs.
func (s *Store) GetCurrentAssetForTuple(ctx context.Context, showID, sequenceID, targetKind, targetID string) (AssetRecord, error) {
	guardNotInTx(ctx, "Store.GetCurrentAssetForTuple")
	return getCurrentAssetForTuple(ctx, s.db, showID, sequenceID, targetKind, targetID)
}

// GetCurrentAssetForTuple is [Store.GetCurrentAssetForTuple]'s [Tx] form.
func (t *Tx) GetCurrentAssetForTuple(ctx context.Context, showID, sequenceID, targetKind, targetID string) (AssetRecord, error) {
	return getCurrentAssetForTuple(ctx, t.tx, showID, sequenceID, targetKind, targetID)
}

// CreateAsset registers a new asset, superseding any existing current
// asset for the same (ShowID, SequenceID, TargetKind, TargetID) in the same
// transaction. A still-current identity match returns
// [*AssetIdentityExistsError] instead; a superseded match rolls back and
// returns (existing row, rolledBack=true, nil).
func (s *Store) CreateAsset(ctx context.Context, rec AssetRecord) (AssetRecord, bool, error) {
	guardNotInTx(ctx, "Store.CreateAsset")
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssetRecord{}, false, fmt.Errorf("store: begin create asset: %w", err)
	}
	defer func() { _ = sqlTx.Rollback() }() // no-op once Commit succeeds

	out, rolledBack, err := createAsset(ctx, sqlTx, rec, s.now())
	if err != nil {
		return AssetRecord{}, false, err
	}
	if err := sqlTx.Commit(); err != nil {
		return AssetRecord{}, false, fmt.Errorf("store: commit create asset: %w", err)
	}
	return out, rolledBack, nil
}

// CreateAsset is [Store.CreateAsset]'s [Tx] form — lets a caller (the
// upload handler) compose the metadata write with its audit entry in one
// transaction, per ADR-024 decision 11, and per spec §3.3's requirement
// that the metadata row lands only after the bytes are whole and hashed.
func (t *Tx) CreateAsset(ctx context.Context, rec AssetRecord) (AssetRecord, bool, error) {
	return createAsset(ctx, t.tx, rec, t.s.now())
}

func getAsset(ctx context.Context, q querier, id string) (AssetRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+assetColumns+`FROM assets WHERE id = ?`, id)
	rec, err := scanAsset(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetRecord{}, ErrAssetNotFound
	}
	if err != nil {
		return AssetRecord{}, fmt.Errorf("store: get asset %q: %w", id, err)
	}
	return rec, nil
}

// GetAsset returns one asset by id, or [ErrAssetNotFound].
func (s *Store) GetAsset(ctx context.Context, id string) (AssetRecord, error) {
	guardNotInTx(ctx, "Store.GetAsset")
	return getAsset(ctx, s.db, id)
}

// GetAsset is [Store.GetAsset]'s [Tx] form.
func (t *Tx) GetAsset(ctx context.Context, id string) (AssetRecord, error) {
	return getAsset(ctx, t.tx, id)
}

// AssetFilter narrows [Store.ListAssets], mirroring [DesiredStateFilter]'s
// shape (desired_state.go): every field is optional (empty means "match
// any"). NodeID filters to TargetKind == AssetTargetKindNode AND
// TargetID == NodeID — it never matches a show-targeted asset, since a
// node-scoped listing is what spec §3.3's `?node=` query parameter means.
type AssetFilter struct {
	ShowID     string
	SequenceID string
	NodeID     string
}

func listAssets(ctx context.Context, q querier, filter AssetFilter) ([]AssetRecord, error) {
	var clauses []string
	var args []any
	if filter.ShowID != "" {
		clauses = append(clauses, "show_id = ?")
		args = append(args, filter.ShowID)
	}
	if filter.SequenceID != "" {
		clauses = append(clauses, "sequence_id = ?")
		args = append(args, filter.SequenceID)
	}
	if filter.NodeID != "" {
		clauses = append(clauses, "target_kind = ? AND target_id = ?")
		args = append(args, AssetTargetKindNode, filter.NodeID)
	}

	query := "SELECT" + assetColumns + "FROM assets"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY show_id, sequence_id, target_kind, target_id, created_at"

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list assets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AssetRecord
	for rows.Next() {
		rec, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list assets: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list assets: %w", err)
	}
	return out, nil
}

// ListAssets returns every asset matching filter (current and superseded
// alike), ordered for a stable, deterministic result.
func (s *Store) ListAssets(ctx context.Context, filter AssetFilter) ([]AssetRecord, error) {
	guardNotInTx(ctx, "Store.ListAssets")
	return listAssets(ctx, s.db, filter)
}

// ListAssets is [Store.ListAssets]'s [Tx] form.
func (t *Tx) ListAssets(ctx context.Context, filter AssetFilter) ([]AssetRecord, error) {
	return listAssets(ctx, t.tx, filter)
}

func listCurrentAssetsForTarget(ctx context.Context, q querier, showID, targetKind, targetID string) ([]AssetRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT`+assetColumns+`FROM assets
		WHERE show_id = ? AND target_kind = ? AND target_id = ? AND superseded_at IS NULL
		ORDER BY sequence_id`,
		showID, targetKind, targetID)
	if err != nil {
		return nil, fmt.Errorf("store: list current assets for target %s/%s/%s: %w", showID, targetKind, targetID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []AssetRecord
	for rows.Next() {
		rec, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list current assets for target %s/%s/%s: %w", showID, targetKind, targetID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list current assets for target %s/%s/%s: %w", showID, targetKind, targetID, err)
	}
	return out, nil
}

// ListCurrentAssetsForTarget returns every CURRENT (superseded_at IS NULL)
// asset for one (showID, targetKind, targetID), across every sequence —
// the primitive spec §4.1's manifest computation is built from: a caller
// reads this once with (show, AssetTargetKindNode, nodeID) and once with
// (show, AssetTargetKindShow, "") to get a node's full expected set.
func (s *Store) ListCurrentAssetsForTarget(ctx context.Context, showID, targetKind, targetID string) ([]AssetRecord, error) {
	guardNotInTx(ctx, "Store.ListCurrentAssetsForTarget")
	return listCurrentAssetsForTarget(ctx, s.db, showID, targetKind, targetID)
}

// ListCurrentAssetsForTarget is [Store.ListCurrentAssetsForTarget]'s [Tx] form.
func (t *Tx) ListCurrentAssetsForTarget(ctx context.Context, showID, targetKind, targetID string) ([]AssetRecord, error) {
	return listCurrentAssetsForTarget(ctx, t.tx, showID, targetKind, targetID)
}

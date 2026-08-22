package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// fpp_playlist_entry_definitions repository methods (schemaV15,
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3, TRACK-H-H2-SPEC.md §3). Store
// and Tx forms share one SQL text each via the querier interface (tx.go),
// mirroring fppobservations.go's own pattern next door.
//
// One row per (instance_uuid, playlist_hash): the key IS the content
// (§3.3's own framing — "a caller cannot install a definition under
// someone else's hash"), so unlike fpp_playlist_entry_observations this
// table never overwrites a row, it only ever inserts a new one or leaves
// an existing one untouched.

// FPPPlaylistDefinitionRecord is one stored playlist definition.
// DefinitionJSON holds the CANONICAL bytes the coordinator itself
// produced by re-canonicalizing what it received (H2 spec §3: "it is what
// the hash is over, it is what a later re-verification must reproduce,
// and it removes any question of which of two byte sequences the row
// represents"), never the raw bytes the caller posted. CapturedAt is
// INTEGER epoch milliseconds because that is the wire shape
// (capturedAtMillis, contract §3.3); ReceivedAt uses this package's usual
// TEXT/RFC3339Nano convention, matching FPPPlaylistEntryObservationRecord's
// identical split.
type FPPPlaylistDefinitionRecord struct {
	InstanceUUID   string
	PlaylistHash   string
	PlaylistName   string
	DefinitionJSON string
	CapturedAt     time.Time
	ReceivedAt     time.Time
}

// ErrFPPPlaylistDefinitionNotFound is returned when no row matches.
var ErrFPPPlaylistDefinitionNotFound = errors.New("store: fpp playlist definition not found")

const fppPlaylistDefinitionColumns = `
	instance_uuid, playlist_hash, playlist_name, definition_json,
	captured_at_millis, received_at
`

func scanFPPPlaylistDefinition(row interface{ Scan(dest ...any) error }) (FPPPlaylistDefinitionRecord, error) {
	var (
		rec              FPPPlaylistDefinitionRecord
		capturedAtMillis int64
		receivedAt       string
	)
	if err := row.Scan(
		&rec.InstanceUUID, &rec.PlaylistHash, &rec.PlaylistName, &rec.DefinitionJSON,
		&capturedAtMillis, &receivedAt,
	); err != nil {
		return FPPPlaylistDefinitionRecord{}, err
	}
	rec.CapturedAt = time.UnixMilli(capturedAtMillis).UTC()
	var err error
	if rec.ReceivedAt, err = dbToTime(receivedAt); err != nil {
		return FPPPlaylistDefinitionRecord{}, fmt.Errorf("store: parse fpp playlist definition received_at: %w", err)
	}
	return rec, nil
}

func getFPPPlaylistDefinition(ctx context.Context, q querier, instanceUUID, playlistHash string) (FPPPlaylistDefinitionRecord, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+fppPlaylistDefinitionColumns+` FROM fpp_playlist_definitions WHERE instance_uuid = ? AND playlist_hash = ?`,
		instanceUUID, playlistHash)
	rec, err := scanFPPPlaylistDefinition(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FPPPlaylistDefinitionRecord{}, ErrFPPPlaylistDefinitionNotFound
	}
	if err != nil {
		return FPPPlaylistDefinitionRecord{}, fmt.Errorf("store: get fpp playlist definition %q/%q: %w", instanceUUID, playlistHash, err)
	}
	return rec, nil
}

// GetFPPPlaylistDefinition returns the stored definition for one
// (instanceUUID, playlistHash), or [ErrFPPPlaylistDefinitionNotFound].
func (s *Store) GetFPPPlaylistDefinition(ctx context.Context, instanceUUID, playlistHash string) (FPPPlaylistDefinitionRecord, error) {
	guardNotInTx(ctx, "Store.GetFPPPlaylistDefinition")
	return getFPPPlaylistDefinition(ctx, s.db, instanceUUID, playlistHash)
}

// GetFPPPlaylistDefinition is [Store.GetFPPPlaylistDefinition]'s [Tx] form.
func (t *Tx) GetFPPPlaylistDefinition(ctx context.Context, instanceUUID, playlistHash string) (FPPPlaylistDefinitionRecord, error) {
	return getFPPPlaylistDefinition(ctx, t.tx, instanceUUID, playlistHash)
}

func listFPPPlaylistDefinitions(ctx context.Context, q querier) ([]FPPPlaylistDefinitionRecord, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+fppPlaylistDefinitionColumns+` FROM fpp_playlist_definitions ORDER BY received_at DESC, instance_uuid ASC, playlist_hash ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list fpp playlist definitions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []FPPPlaylistDefinitionRecord
	for rows.Next() {
		rec, err := scanFPPPlaylistDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan fpp playlist definition: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list fpp playlist definitions: %w", err)
	}
	return out, nil
}

// ListFPPPlaylistDefinitions returns every stored definition, newest
// received first (H2 spec §4 step 1: "newest first, so the operator can
// see ... which revision of each"). Callers that only need metadata
// still get DefinitionJSON on every row; the list HTTP handler is the one
// that trims it before it reaches the wire, this method itself is not
// the boundary that keeps a big definition off the network (see its own
// doc comment).
func (s *Store) ListFPPPlaylistDefinitions(ctx context.Context) ([]FPPPlaylistDefinitionRecord, error) {
	guardNotInTx(ctx, "Store.ListFPPPlaylistDefinitions")
	return listFPPPlaylistDefinitions(ctx, s.db)
}

// ListFPPPlaylistDefinitions is [Store.ListFPPPlaylistDefinitions]'s [Tx] form.
func (t *Tx) ListFPPPlaylistDefinitions(ctx context.Context) ([]FPPPlaylistDefinitionRecord, error) {
	return listFPPPlaylistDefinitions(ctx, t.tx)
}

func listFPPPlaylistDefinitionsByInstance(ctx context.Context, q querier, instanceUUID string) ([]FPPPlaylistDefinitionRecord, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+fppPlaylistDefinitionColumns+` FROM fpp_playlist_definitions WHERE instance_uuid = ? ORDER BY received_at DESC`,
		instanceUUID)
	if err != nil {
		return nil, fmt.Errorf("store: list fpp playlist definitions for %q: %w", instanceUUID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []FPPPlaylistDefinitionRecord
	for rows.Next() {
		rec, err := scanFPPPlaylistDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan fpp playlist definition: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list fpp playlist definitions for %q: %w", instanceUUID, err)
	}
	return out, nil
}

// putFPPPlaylistDefinition inserts rec if (InstanceUUID, PlaylistHash) is
// not already held, and reports whether it actually inserted. A repeat of
// an already-held key is a no-op that leaves the stored row exactly as
// it was: contract §3.4 step 8, "playlistName and capturedAtMillis on a
// repeat are ignored rather than overwriting the stored ones: the first
// report of a given content is the one with provenance."
func putFPPPlaylistDefinition(ctx context.Context, q querier, rec FPPPlaylistDefinitionRecord) (bool, error) {
	res, err := q.ExecContext(ctx, `
		INSERT INTO fpp_playlist_definitions (`+fppPlaylistDefinitionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_uuid, playlist_hash) DO NOTHING
	`,
		rec.InstanceUUID, rec.PlaylistHash, rec.PlaylistName, rec.DefinitionJSON,
		rec.CapturedAt.UnixMilli(), timeToDB(rec.ReceivedAt),
	)
	if err != nil {
		return false, fmt.Errorf("store: put fpp playlist definition %q/%q: %w", rec.InstanceUUID, rec.PlaylistHash, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: put fpp playlist definition %q/%q: rows affected: %w", rec.InstanceUUID, rec.PlaylistHash, err)
	}
	return n > 0, nil
}

// PutFPPPlaylistDefinition upserts idempotently on (InstanceUUID,
// PlaylistHash) and reports whether this call actually inserted a new
// row (false on an idempotent repeat). The caller decides what "actually
// inserted" means for auditing (contract §3.4: "a store that actually
// inserted is audited ... an idempotent repeat is not audited").
func (s *Store) PutFPPPlaylistDefinition(ctx context.Context, rec FPPPlaylistDefinitionRecord) (bool, error) {
	guardNotInTx(ctx, "Store.PutFPPPlaylistDefinition")
	return putFPPPlaylistDefinition(ctx, s.db, rec)
}

// PutFPPPlaylistDefinition is [Store.PutFPPPlaylistDefinition]'s [Tx] form.
func (t *Tx) PutFPPPlaylistDefinition(ctx context.Context, rec FPPPlaylistDefinitionRecord) (bool, error) {
	return putFPPPlaylistDefinition(ctx, t.tx, rec)
}

func deleteFPPPlaylistDefinition(ctx context.Context, q querier, instanceUUID, playlistHash string) (bool, error) {
	res, err := q.ExecContext(ctx,
		`DELETE FROM fpp_playlist_definitions WHERE instance_uuid = ? AND playlist_hash = ?`,
		instanceUUID, playlistHash)
	if err != nil {
		return false, fmt.Errorf("store: delete fpp playlist definition %q/%q: %w", instanceUUID, playlistHash, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: delete fpp playlist definition %q/%q: rows affected: %w", instanceUUID, playlistHash, err)
	}
	return n > 0, nil
}

// DeleteFPPPlaylistDefinition removes one (instanceUUID, playlistHash)
// row and reports whether a row was actually deleted.
func (s *Store) DeleteFPPPlaylistDefinition(ctx context.Context, instanceUUID, playlistHash string) (bool, error) {
	guardNotInTx(ctx, "Store.DeleteFPPPlaylistDefinition")
	return deleteFPPPlaylistDefinition(ctx, s.db, instanceUUID, playlistHash)
}

// DeleteFPPPlaylistDefinition is [Store.DeleteFPPPlaylistDefinition]'s [Tx] form.
func (t *Tx) DeleteFPPPlaylistDefinition(ctx context.Context, instanceUUID, playlistHash string) (bool, error) {
	return deleteFPPPlaylistDefinition(ctx, t.tx, instanceUUID, playlistHash)
}

// pruneFPPPlaylistDefinitions implements H2 spec §3's retention rule for
// one instance: a definition isReferenced reports true for is NEVER
// evicted, regardless of age; beyond those, the newest keepUnreferenced
// UNREFERENCED rows are kept and any OLDER unreferenced row is deleted.
// isReferenced is a caller-supplied callback rather than a query this
// package builds itself, deliberately: whether a hash is referenced is a
// fact about config_objects/config_revisions' show.playlist payloads,
// and a decision this package's own doc comment (config.go) already
// draws a line around — "this file only ever treats payload_json as an
// opaque string" — this method does not cross that line, its caller (the
// api package, which already knows show.playlist's JSON shape) does.
func pruneFPPPlaylistDefinitions(ctx context.Context, q querier, instanceUUID string, keepUnreferenced int, isReferenced func(playlistHash string) bool) (int, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT playlist_hash FROM fpp_playlist_definitions WHERE instance_uuid = ? ORDER BY received_at DESC`,
		instanceUUID)
	if err != nil {
		return 0, fmt.Errorf("store: prune fpp playlist definitions for %q: list: %w", instanceUUID, err)
	}
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("store: prune fpp playlist definitions for %q: scan: %w", instanceUUID, err)
		}
		hashes = append(hashes, h)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("store: prune fpp playlist definitions for %q: %w", instanceUUID, err)
	}
	_ = rows.Close()

	unreferencedSeen := 0
	pruned := 0
	for _, h := range hashes {
		if isReferenced(h) {
			continue
		}
		unreferencedSeen++
		if unreferencedSeen <= keepUnreferenced {
			continue
		}
		res, err := q.ExecContext(ctx,
			`DELETE FROM fpp_playlist_definitions WHERE instance_uuid = ? AND playlist_hash = ?`,
			instanceUUID, h)
		if err != nil {
			return pruned, fmt.Errorf("store: prune fpp playlist definitions for %q: delete %q: %w", instanceUUID, h, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return pruned, fmt.Errorf("store: prune fpp playlist definitions for %q: rows affected: %w", instanceUUID, err)
		}
		pruned += int(n)
	}
	return pruned, nil
}

// PruneFPPPlaylistDefinitions is [pruneFPPPlaylistDefinitions]'s [Store] form.
func (s *Store) PruneFPPPlaylistDefinitions(ctx context.Context, instanceUUID string, keepUnreferenced int, isReferenced func(playlistHash string) bool) (int, error) {
	guardNotInTx(ctx, "Store.PruneFPPPlaylistDefinitions")
	return pruneFPPPlaylistDefinitions(ctx, s.db, instanceUUID, keepUnreferenced, isReferenced)
}

// PruneFPPPlaylistDefinitions is [Store.PruneFPPPlaylistDefinitions]'s [Tx] form.
func (t *Tx) PruneFPPPlaylistDefinitions(ctx context.Context, instanceUUID string, keepUnreferenced int, isReferenced func(playlistHash string) bool) (int, error) {
	return pruneFPPPlaylistDefinitions(ctx, t.tx, instanceUUID, keepUnreferenced, isReferenced)
}

// ListFPPPlaylistDefinitionsByInstance is [listFPPPlaylistDefinitionsByInstance]'s [Store] form: every
// definition held for one instance, newest received first. This is what
// PruneFPPPlaylistDefinitions' caller needs to build its isReferenced
// closure once per instance rather than per row.
func (s *Store) ListFPPPlaylistDefinitionsByInstance(ctx context.Context, instanceUUID string) ([]FPPPlaylistDefinitionRecord, error) {
	guardNotInTx(ctx, "Store.ListFPPPlaylistDefinitionsByInstance")
	return listFPPPlaylistDefinitionsByInstance(ctx, s.db, instanceUUID)
}

// ListFPPPlaylistDefinitionsByInstance is [Store.ListFPPPlaylistDefinitionsByInstance]'s [Tx] form.
func (t *Tx) ListFPPPlaylistDefinitionsByInstance(ctx context.Context, instanceUUID string) ([]FPPPlaylistDefinitionRecord, error) {
	return listFPPPlaylistDefinitionsByInstance(ctx, t.tx, instanceUUID)
}

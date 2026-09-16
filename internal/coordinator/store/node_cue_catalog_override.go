package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV36's node_cue_catalog_override repository
// methods: one row per node recording that an operator accepted an H0.5
// exclusive-claim conflict for exactly one catalog revision, mirroring
// node_cue_catalog_ack.go's upsert shape.

// StoredCatalogConflict is one accepted H0.5 conflict, projected from
// assetsync.CatalogConflict so this package need not import assetsync.
// Claim is already rendered to a display string by the caller.
type StoredCatalogConflict struct {
	CueA  string `json:"cueA"`
	CueB  string `json:"cueB"`
	Claim string `json:"claim"`
}

// NodeCueCatalogOverrideRecord is one row: nodeID's accepted H0.5
// conflict(s) for exactly Revision. OverriddenBy* is always a real
// operator principal; cuecatalogdeploy.go's dispatch path is the only writer.
type NodeCueCatalogOverrideRecord struct {
	NodeID     string
	Revision   string
	ShowID     string
	Generation int64
	Conflicts  []StoredCatalogConflict

	OverriddenByPrincipalID   string
	OverriddenByPrincipalName string
	OverriddenAt              time.Time
}

// ErrNodeCueCatalogOverrideNotFound is returned by
// [Store.GetNodeCueCatalogOverride]/[Tx.GetNodeCueCatalogOverride] when
// nodeID has never had a deploy overridden.
var ErrNodeCueCatalogOverrideNotFound = errors.New("store: node cue-catalog override not found")

func putNodeCueCatalogOverride(ctx context.Context, q querier, rec NodeCueCatalogOverrideRecord) error {
	if rec.NodeID == "" {
		return fmt.Errorf("store: put node cue-catalog override: nodeID is empty")
	}
	conflictsJSON, err := json.Marshal(rec.Conflicts)
	if err != nil {
		return fmt.Errorf("store: put node cue-catalog override: encode conflicts: %w", err)
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO node_cue_catalog_override (
			node_id, revision, show_id, generation, conflicts_json,
			overridden_by_principal_id, overridden_by_principal_name, overridden_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			revision                     = excluded.revision,
			show_id                      = excluded.show_id,
			generation                   = excluded.generation,
			conflicts_json               = excluded.conflicts_json,
			overridden_by_principal_id   = excluded.overridden_by_principal_id,
			overridden_by_principal_name = excluded.overridden_by_principal_name,
			overridden_at                = excluded.overridden_at
	`, rec.NodeID, rec.Revision, rec.ShowID, rec.Generation, string(conflictsJSON),
		rec.OverriddenByPrincipalID, rec.OverriddenByPrincipalName, timeToDB(rec.OverriddenAt)); err != nil {
		return fmt.Errorf("store: put node cue-catalog override %q: %w", rec.NodeID, err)
	}
	return nil
}

// PutNodeCueCatalogOverride upserts nodeID's cue-catalog deploy override:
// a wholesale replacement of its one row, matching
// [Store.PutNodeCueCatalogAck]'s "no partial state" posture.
func (s *Store) PutNodeCueCatalogOverride(ctx context.Context, rec NodeCueCatalogOverrideRecord) error {
	guardNotInTx(ctx, "Store.PutNodeCueCatalogOverride")
	return putNodeCueCatalogOverride(ctx, s.db, rec)
}

// PutNodeCueCatalogOverride is [Store.PutNodeCueCatalogOverride]'s [Tx] form.
func (t *Tx) PutNodeCueCatalogOverride(ctx context.Context, rec NodeCueCatalogOverrideRecord) error {
	return putNodeCueCatalogOverride(ctx, t.tx, rec)
}

func scanNodeCueCatalogOverride(row interface{ Scan(dest ...any) error }) (NodeCueCatalogOverrideRecord, error) {
	var (
		rec           NodeCueCatalogOverrideRecord
		conflictsJSON string
		overriddenAt  string
	)
	if err := row.Scan(
		&rec.NodeID, &rec.Revision, &rec.ShowID, &rec.Generation, &conflictsJSON,
		&rec.OverriddenByPrincipalID, &rec.OverriddenByPrincipalName, &overriddenAt,
	); err != nil {
		return NodeCueCatalogOverrideRecord{}, err
	}
	if err := json.Unmarshal([]byte(conflictsJSON), &rec.Conflicts); err != nil {
		return NodeCueCatalogOverrideRecord{}, fmt.Errorf("store: parse node cue-catalog override conflicts: %w", err)
	}
	var err error
	if rec.OverriddenAt, err = dbToTime(overriddenAt); err != nil {
		return NodeCueCatalogOverrideRecord{}, fmt.Errorf("store: parse node cue-catalog override overridden_at: %w", err)
	}
	return rec, nil
}

func getNodeCueCatalogOverride(ctx context.Context, q querier, nodeID string) (NodeCueCatalogOverrideRecord, error) {
	row := q.QueryRowContext(ctx, `
		SELECT node_id, revision, show_id, generation, conflicts_json,
			overridden_by_principal_id, overridden_by_principal_name, overridden_at
		FROM node_cue_catalog_override WHERE node_id = ?
	`, nodeID)
	rec, err := scanNodeCueCatalogOverride(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeCueCatalogOverrideRecord{}, ErrNodeCueCatalogOverrideNotFound
	}
	if err != nil {
		return NodeCueCatalogOverrideRecord{}, fmt.Errorf("store: get node cue-catalog override %q: %w", nodeID, err)
	}
	return rec, nil
}

// GetNodeCueCatalogOverride returns nodeID's most recently recorded
// cue-catalog deploy override, or [ErrNodeCueCatalogOverrideNotFound] if
// it has never had one.
func (s *Store) GetNodeCueCatalogOverride(ctx context.Context, nodeID string) (NodeCueCatalogOverrideRecord, error) {
	guardNotInTx(ctx, "Store.GetNodeCueCatalogOverride")
	return getNodeCueCatalogOverride(ctx, s.db, nodeID)
}

// GetNodeCueCatalogOverride is [Store.GetNodeCueCatalogOverride]'s [Tx] form.
func (t *Tx) GetNodeCueCatalogOverride(ctx context.Context, nodeID string) (NodeCueCatalogOverrideRecord, error) {
	return getNodeCueCatalogOverride(ctx, t.tx, nodeID)
}

// schemaV36 adds node_cue_catalog_override, a pure addition alongside
// node_cue_catalog_ack (schemaV17). IF NOT EXISTS matches schemaV25/
// schemaV28/schemaV33: a rewound PRAGMA user_version must be able to replay this.
const schemaV36 = `
CREATE TABLE IF NOT EXISTS node_cue_catalog_override (
    node_id                       TEXT PRIMARY KEY,
    revision                      TEXT NOT NULL,
    show_id                       TEXT NOT NULL,
    generation                    INTEGER NOT NULL,
    conflicts_json                TEXT NOT NULL,
    overridden_by_principal_id    TEXT NOT NULL,
    overridden_by_principal_name  TEXT NOT NULL,
    overridden_at                 TEXT NOT NULL
);
`

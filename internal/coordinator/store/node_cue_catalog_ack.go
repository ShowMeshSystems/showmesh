package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV16's node_cue_catalog_ack repository methods
// (Track H seam H3, TRACK-H-H3-SPEC.md section 4). One row per node: the
// catalog revision that node most recently reported holding, stored beside
// (never merged with) node_asset_reports — see node_assets.go's own doc
// comment for the identical "what SHOULD versus what DOES" split this
// mirrors, one level up: assetsync answers "does this node hold the bytes
// it should", this table answers "does this node hold the AUTHORIZATION it
// should".

// NodeCueCatalogAckRecord is one row of the node_cue_catalog_ack table:
// the fact that a node acknowledged holding a specific catalog revision,
// for a specific show and generation, at a specific time. It is the node's
// own evidence, never re-derived from the coordinator's current state.
type NodeCueCatalogAckRecord struct {
	NodeID         string
	Revision       string
	ShowID         string
	Generation     int64
	AcknowledgedAt time.Time
}

// ErrNodeCueCatalogAckNotFound is returned by [Store.GetNodeCueCatalogAck]/
// [Tx.GetNodeCueCatalogAck] when nodeID has never acknowledged a catalog.
// Distinct from a stored acknowledgement whose revision happens to differ
// from what the coordinator resolves now — that is "stale", not "absent",
// and a caller (the read route) must not collapse the two, matching this
// package's node_asset_reports precedent for the identical class of
// mistake.
var ErrNodeCueCatalogAckNotFound = errors.New("store: node cue-catalog acknowledgement not found")

func putNodeCueCatalogAck(ctx context.Context, q querier, rec NodeCueCatalogAckRecord) error {
	if rec.NodeID == "" {
		return fmt.Errorf("store: put node cue-catalog ack: nodeID is empty")
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO node_cue_catalog_ack (node_id, revision, show_id, generation, acknowledged_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			revision        = excluded.revision,
			show_id         = excluded.show_id,
			generation      = excluded.generation,
			acknowledged_at = excluded.acknowledged_at
	`, rec.NodeID, rec.Revision, rec.ShowID, rec.Generation, timeToDB(rec.AcknowledgedAt)); err != nil {
		return fmt.Errorf("store: put node cue-catalog ack %q: %w", rec.NodeID, err)
	}
	return nil
}

// PutNodeCueCatalogAck upserts nodeID's cue-catalog acknowledgement: a
// wholesale replacement of its one row, never an incremental merge — a
// node holds exactly one catalog at a time (TRACK-H-H3-SPEC.md section 4:
// "there is no partial state").
func (s *Store) PutNodeCueCatalogAck(ctx context.Context, rec NodeCueCatalogAckRecord) error {
	guardNotInTx(ctx, "Store.PutNodeCueCatalogAck")
	return putNodeCueCatalogAck(ctx, s.db, rec)
}

// PutNodeCueCatalogAck is [Store.PutNodeCueCatalogAck]'s [Tx] form.
func (t *Tx) PutNodeCueCatalogAck(ctx context.Context, rec NodeCueCatalogAckRecord) error {
	return putNodeCueCatalogAck(ctx, t.tx, rec)
}

func scanNodeCueCatalogAck(row interface{ Scan(dest ...any) error }) (NodeCueCatalogAckRecord, error) {
	var (
		rec            NodeCueCatalogAckRecord
		acknowledgedAt string
	)
	if err := row.Scan(&rec.NodeID, &rec.Revision, &rec.ShowID, &rec.Generation, &acknowledgedAt); err != nil {
		return NodeCueCatalogAckRecord{}, err
	}
	var err error
	if rec.AcknowledgedAt, err = dbToTime(acknowledgedAt); err != nil {
		return NodeCueCatalogAckRecord{}, fmt.Errorf("store: parse node cue-catalog ack acknowledged_at: %w", err)
	}
	return rec, nil
}

func getNodeCueCatalogAck(ctx context.Context, q querier, nodeID string) (NodeCueCatalogAckRecord, error) {
	row := q.QueryRowContext(ctx, `
		SELECT node_id, revision, show_id, generation, acknowledged_at
		FROM node_cue_catalog_ack WHERE node_id = ?
	`, nodeID)
	rec, err := scanNodeCueCatalogAck(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeCueCatalogAckRecord{}, ErrNodeCueCatalogAckNotFound
	}
	if err != nil {
		return NodeCueCatalogAckRecord{}, fmt.Errorf("store: get node cue-catalog ack %q: %w", nodeID, err)
	}
	return rec, nil
}

// GetNodeCueCatalogAck returns nodeID's last cue-catalog acknowledgement,
// or [ErrNodeCueCatalogAckNotFound] if it has never acknowledged one.
func (s *Store) GetNodeCueCatalogAck(ctx context.Context, nodeID string) (NodeCueCatalogAckRecord, error) {
	guardNotInTx(ctx, "Store.GetNodeCueCatalogAck")
	return getNodeCueCatalogAck(ctx, s.db, nodeID)
}

// GetNodeCueCatalogAck is [Store.GetNodeCueCatalogAck]'s [Tx] form.
func (t *Tx) GetNodeCueCatalogAck(ctx context.Context, nodeID string) (NodeCueCatalogAckRecord, error) {
	return getNodeCueCatalogAck(ctx, t.tx, nodeID)
}

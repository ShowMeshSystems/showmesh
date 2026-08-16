package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV8's node_asset_inventory and node_asset_reports
// repository methods (Track E, seam E5/E6; ADR-028 decision 5's "playback
// is always from node-local storage" made observable). Together the two
// tables answer "what does this node actually hold" as evidence, never as
// a copy of what it was told to hold — see assets.go for the separate
// "what SHOULD this node hold" side of that comparison.

// NodeAssetInventoryRecord is one row of the node_asset_inventory table:
// one artifact an agent has verified present on its own disk, by content
// hash. RuntimeFilename is carried here too because that is what a reader
// resolving "does this node hold sequence X's asset" needs to report
// alongside the hash — it is not a second identity, only a label.
type NodeAssetInventoryRecord struct {
	NodeID          string
	ContentHash     string
	RuntimeFilename string
	SizeBytes       int64
	VerifiedAt      time.Time
}

// NodeAssetReportRecord is one row of the node_asset_reports table: the
// fact that a node reported ITS INVENTORY at all, independent of what that
// inventory contained. See [ErrNodeAssetReportNotFound]'s doc comment for
// why this table exists as a separate fact from node_asset_inventory
// rather than being inferred from it.
type NodeAssetReportRecord struct {
	NodeID     string
	ReportedAt time.Time
	Complete   bool
	Reason     string
}

// ErrNodeAssetReportNotFound is returned by [Store.GetNodeAssetReport]/
// [Tx.GetNodeAssetReport] when nodeID has never submitted an inventory
// report. This is the state spec §4.3's manifest reads as "no inventory
// report has ever been received" — distinct from a report existing with
// Complete == false (the agent tried and could not finish) and distinct
// from a report existing with Complete == true and zero
// node_asset_inventory rows (the agent finished and found nothing). A
// caller must not collapse "never reported" into either of the other two;
// that collapse is exactly the "complete: true is the licence to assert
// absence" defect this project has already shipped and fixed three times
// (see migrations.go's schemaV6 doc comment on node_declarations).
var ErrNodeAssetReportNotFound = errors.New("store: node asset report not found")

// replaceNodeAssetInventory is [Store.ReplaceNodeAssetInventory]/
// [Tx.ReplaceNodeAssetInventory]'s shared body. It always runs inside a
// transaction — the caller guarantees that — because a delete-then-insert
// pair must never be observed half-applied, matching
// [Store.ReplaceObservations]'s exact reasoning in observations.go for the
// identical shape applied to a different table. The node_asset_reports
// upsert lands in the same transaction as the delete-then-insert: a reader
// must never see a fresh report timestamp paired with a stale or
// half-replaced inventory, or vice versa.
//
// The delete-then-insert only runs when report.Complete is true. An
// incomplete report (the agent could not fully enumerate its own asset
// directory — spec §4.2) is NOT evidence the node holds nothing: it is
// evidence the node could not be read. Clearing node_asset_inventory on an
// incomplete report was the P4 defect — a node whose asset directory went
// transiently unreadable would publish complete:false with an empty items
// slice, this method would delete every held row it knew about, and
// assetsync would see nothing held and re-dispatch every expected asset on
// every tick until the mount returned. The report row itself is always
// upserted, complete or not, so "the node is struggling" is visible
// evidence rather than being lost along with the inventory it must now
// leave untouched.
func replaceNodeAssetInventory(ctx context.Context, q querier, nodeID string, items []NodeAssetInventoryRecord, report NodeAssetReportRecord, now time.Time) error {
	if nodeID == "" {
		return fmt.Errorf("store: replace node asset inventory: nodeID is empty")
	}

	if report.Complete {
		if _, err := q.ExecContext(ctx, `DELETE FROM node_asset_inventory WHERE node_id = ?`, nodeID); err != nil {
			return fmt.Errorf("store: replace node asset inventory %q: delete: %w", nodeID, err)
		}
		for _, item := range items {
			if _, err := q.ExecContext(ctx, `
				INSERT INTO node_asset_inventory (node_id, content_hash, runtime_filename, size_bytes, verified_at)
				VALUES (?, ?, ?, ?, ?)
			`, nodeID, item.ContentHash, item.RuntimeFilename, item.SizeBytes, timeToDB(item.VerifiedAt)); err != nil {
				return fmt.Errorf("store: replace node asset inventory %q: insert %q: %w", nodeID, item.ContentHash, err)
			}
		}
	}

	// nodeID (the parameter), never report.NodeID, is what is written here —
	// this method owns which node's report this is; report only supplies
	// the report's own facts (ReportedAt/Complete/Reason).
	if _, err := q.ExecContext(ctx, `
		INSERT INTO node_asset_reports (node_id, reported_at, complete, reason)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			reported_at = excluded.reported_at,
			complete    = excluded.complete,
			reason      = excluded.reason
	`, nodeID, timeToDB(report.ReportedAt), boolToDB(report.Complete), report.Reason); err != nil {
		return fmt.Errorf("store: replace node asset inventory %q: upsert report: %w", nodeID, err)
	}
	return nil
}

// ReplaceNodeAssetInventory replaces nodeID's entire node_asset_inventory
// with items (delete-then-insert, per spec §3) and upserts its
// node_asset_reports row, all in one transaction — but ONLY when
// report.Complete is true; see [replaceNodeAssetInventory]'s doc comment for
// why an incomplete report leaves node_asset_inventory untouched. A node's
// own agent is this method's only intended caller (on every sync operation
// and on SHOWMESH_ASSET_INVENTORY_INTERVAL, per spec §4.2): a COMPLETE
// report is a wholesale replacement of what the node holds RIGHT NOW, never
// an incremental merge, so a file the node no longer holds silently
// disappears from its inventory on the very next complete report — exactly
// mirroring [Store.ReplaceObservations]'s per-source pruning contract for
// the identical reason (a stale ghost row is worse than a momentarily empty
// one, and a COMPLETE call always carries the node's own complete current
// view).
func (s *Store) ReplaceNodeAssetInventory(ctx context.Context, nodeID string, items []NodeAssetInventoryRecord, report NodeAssetReportRecord) error {
	guardNotInTx(ctx, "Store.ReplaceNodeAssetInventory")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin replace node asset inventory: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit succeeds

	if err := replaceNodeAssetInventory(ctx, tx, nodeID, items, report, s.now()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit replace node asset inventory: %w", err)
	}
	return nil
}

// ReplaceNodeAssetInventory is [Store.ReplaceNodeAssetInventory]'s [Tx] form.
func (t *Tx) ReplaceNodeAssetInventory(ctx context.Context, nodeID string, items []NodeAssetInventoryRecord, report NodeAssetReportRecord) error {
	return replaceNodeAssetInventory(ctx, t.tx, nodeID, items, report, t.s.now())
}

func scanNodeAssetInventory(row interface{ Scan(dest ...any) error }) (NodeAssetInventoryRecord, error) {
	var (
		rec        NodeAssetInventoryRecord
		verifiedAt string
	)
	if err := row.Scan(&rec.NodeID, &rec.ContentHash, &rec.RuntimeFilename, &rec.SizeBytes, &verifiedAt); err != nil {
		return NodeAssetInventoryRecord{}, err
	}
	var err error
	if rec.VerifiedAt, err = dbToTime(verifiedAt); err != nil {
		return NodeAssetInventoryRecord{}, fmt.Errorf("store: parse node asset inventory verified_at: %w", err)
	}
	return rec, nil
}

func getNodeAssetInventory(ctx context.Context, q querier, nodeID string) ([]NodeAssetInventoryRecord, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT node_id, content_hash, runtime_filename, size_bytes, verified_at
		FROM node_asset_inventory WHERE node_id = ? ORDER BY content_hash
	`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("store: get node asset inventory %q: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []NodeAssetInventoryRecord
	for rows.Next() {
		rec, err := scanNodeAssetInventory(rows)
		if err != nil {
			return nil, fmt.Errorf("store: get node asset inventory %q: %w", nodeID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: get node asset inventory %q: %w", nodeID, err)
	}
	return out, nil
}

// GetNodeAssetInventory returns every artifact nodeID's last report said it
// holds. An empty, nil-error result is a legitimate answer (a node that
// reported holding nothing) and must not be treated as "no report exists"
// — use [Store.GetNodeAssetReport] to tell the two apart.
func (s *Store) GetNodeAssetInventory(ctx context.Context, nodeID string) ([]NodeAssetInventoryRecord, error) {
	guardNotInTx(ctx, "Store.GetNodeAssetInventory")
	return getNodeAssetInventory(ctx, s.db, nodeID)
}

// GetNodeAssetInventory is [Store.GetNodeAssetInventory]'s [Tx] form.
func (t *Tx) GetNodeAssetInventory(ctx context.Context, nodeID string) ([]NodeAssetInventoryRecord, error) {
	return getNodeAssetInventory(ctx, t.tx, nodeID)
}

func scanNodeAssetReport(row interface{ Scan(dest ...any) error }) (NodeAssetReportRecord, error) {
	var (
		rec        NodeAssetReportRecord
		reportedAt string
		complete   int64
	)
	if err := row.Scan(&rec.NodeID, &reportedAt, &complete, &rec.Reason); err != nil {
		return NodeAssetReportRecord{}, err
	}
	var err error
	if rec.ReportedAt, err = dbToTime(reportedAt); err != nil {
		return NodeAssetReportRecord{}, fmt.Errorf("store: parse node asset report reported_at: %w", err)
	}
	rec.Complete = complete != 0
	return rec, nil
}

func getNodeAssetReport(ctx context.Context, q querier, nodeID string) (NodeAssetReportRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT node_id, reported_at, complete, reason FROM node_asset_reports WHERE node_id = ?`, nodeID)
	rec, err := scanNodeAssetReport(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeAssetReportRecord{}, ErrNodeAssetReportNotFound
	}
	if err != nil {
		return NodeAssetReportRecord{}, fmt.Errorf("store: get node asset report %q: %w", nodeID, err)
	}
	return rec, nil
}

// GetNodeAssetReport returns nodeID's inventory-report evidence, or
// [ErrNodeAssetReportNotFound] if it has never reported.
func (s *Store) GetNodeAssetReport(ctx context.Context, nodeID string) (NodeAssetReportRecord, error) {
	guardNotInTx(ctx, "Store.GetNodeAssetReport")
	return getNodeAssetReport(ctx, s.db, nodeID)
}

// GetNodeAssetReport is [Store.GetNodeAssetReport]'s [Tx] form.
func (t *Tx) GetNodeAssetReport(ctx context.Context, nodeID string) (NodeAssetReportRecord, error) {
	return getNodeAssetReport(ctx, t.tx, nodeID)
}

func listNodeAssetReports(ctx context.Context, q querier) ([]NodeAssetReportRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT node_id, reported_at, complete, reason FROM node_asset_reports ORDER BY node_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list node asset reports: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []NodeAssetReportRecord
	for rows.Next() {
		rec, err := scanNodeAssetReport(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list node asset reports: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list node asset reports: %w", err)
	}
	return out, nil
}

// ListNodeAssetReports returns every node's report row, ordered by node ID
// for a stable, deterministic result. A node with no row here has never
// reported — it simply does not appear, rather than appearing with a zero
// value that would misrepresent silence as an empty-but-real report.
func (s *Store) ListNodeAssetReports(ctx context.Context) ([]NodeAssetReportRecord, error) {
	guardNotInTx(ctx, "Store.ListNodeAssetReports")
	return listNodeAssetReports(ctx, s.db)
}

// ListNodeAssetReports is [Store.ListNodeAssetReports]'s [Tx] form.
func (t *Tx) ListNodeAssetReports(ctx context.Context) ([]NodeAssetReportRecord, error) {
	return listNodeAssetReports(ctx, t.tx)
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV6's node_declarations repository methods (Step 7
// seam 0; RES-008 D2's declared-vs-observed inventory split). See
// migrations.go's schemaV6 doc comment for why node_id carries no foreign
// key to nodes: a declared node must survive its observed row being
// absent, because powered-off equipment is normal outside display hours.
// Discovery itself (turning an observed nodes row into a declared one) is
// seam B's; this file lands only the table and its generic CRUD.

// NodeDeclarationRecord is one row of the node_declarations table: an
// operator's durable decision that a node belongs in this installation's
// inventory, independent of whether it currently reports in.
// LastDiscoveredAt is nullable and means "no complete discovery run has
// ever seen this node" — never "seen at time zero"; see this package's
// standing rule on nullable evidence timestamps (schemaV1/schemaV3's
// ObservedAt columns, restated in migrations.go's schemaV6 doc comment).
type NodeDeclarationRecord struct {
	NodeID                  string
	Label                   string
	Notes                   string
	DeclaredAt              time.Time
	DeclaredByPrincipalID   string
	DeclaredByPrincipalName string
	LastDiscoveryRunID      string
	LastDiscoveredAt        *time.Time
	UpdatedAt               time.Time
}

// ErrNodeDeclarationNotFound is returned by [Store.GetNodeDeclaration]/
// [Tx.GetNodeDeclaration] when no row exists for nodeID.
var ErrNodeDeclarationNotFound = errors.New("store: node declaration not found")

func declareNode(ctx context.Context, q querier, rec NodeDeclarationRecord, now time.Time) (NodeDeclarationRecord, error) {
	nowStr := timeToDB(now)
	// rec.LastDiscoveryRunID/LastDiscoveredAt are ONLY ever consulted on the
	// INSERT branch below (a brand-new declaration) — the ON CONFLICT branch
	// deliberately does not touch these two columns at all, exactly as it
	// already left them alone before this parameter existed. That matters
	// for DEFECT 6 (an update must never disturb discovery evidence it was
	// not asked to change) and for DEFECT 1: a caller promoting a node from
	// a LIVE proposal (api/discovery.go's handlePromoteNode) may pass the
	// discovery run that evidenced it, so the very first read of a freshly
	// promoted declaration reports it seen rather than defaulting to '' /
	// NULL and immediately rendering not_seen — see that caller's own doc
	// comment for why this is done there (a fresh presence re-check) rather
	// than trusting a client-supplied run id blindly. A caller with no such
	// evidence (the ordinary declare-with-no-prior-sighting path) simply
	// leaves NodeDeclarationRecord's zero values here (""/nil), identical to
	// the hardcoded '' / NULL this replaces.
	var lastDiscoveredAt any
	if rec.LastDiscoveredAt != nil {
		lastDiscoveredAt = timeToDB(*rec.LastDiscoveredAt)
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO node_declarations (
			node_id, label, notes, declared_at, declared_by_principal_id,
			declared_by_principal_name, last_discovery_run_id, last_discovered_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			label      = excluded.label,
			notes      = excluded.notes,
			updated_at = excluded.updated_at
	`, rec.NodeID, rec.Label, rec.Notes, nowStr, rec.DeclaredByPrincipalID, rec.DeclaredByPrincipalName,
		rec.LastDiscoveryRunID, lastDiscoveredAt, nowStr)
	if err != nil {
		return NodeDeclarationRecord{}, fmt.Errorf("store: declare node %q: %w", rec.NodeID, err)
	}
	return getNodeDeclaration(ctx, q, rec.NodeID)
}

// DeclareNode records nodeID as belonging to this installation's declared
// inventory (RES-008 D2's "discovery proposing, an operator action
// promoting"). Idempotent by node_id: declaring an already-declared node
// again updates Label/Notes but leaves DeclaredAt/DeclaredByPrincipal*
// exactly as originally recorded — the attribution of WHO first declared
// this node is not overwritten by a later edit to its label. rec's
// LastDiscoveryRunID/LastDiscoveredAt are honored only when this call
// creates a brand-new row — see declareNode's own doc comment above.
func (s *Store) DeclareNode(ctx context.Context, rec NodeDeclarationRecord) (NodeDeclarationRecord, error) {
	guardNotInTx(ctx, "Store.DeclareNode")
	return declareNode(ctx, s.db, rec, s.now())
}

// DeclareNode is [Store.DeclareNode]'s [Tx] form.
func (t *Tx) DeclareNode(ctx context.Context, rec NodeDeclarationRecord) (NodeDeclarationRecord, error) {
	return declareNode(ctx, t.tx, rec, t.s.now())
}

const nodeDeclarationColumns = `
	node_id, label, notes, declared_at, declared_by_principal_id,
	declared_by_principal_name, last_discovery_run_id, last_discovered_at, updated_at
`

func scanNodeDeclaration(row interface{ Scan(dest ...any) error }) (NodeDeclarationRecord, error) {
	var (
		rec              NodeDeclarationRecord
		declaredAt       string
		lastDiscoveredAt sql.NullString
		updatedAt        string
	)
	if err := row.Scan(
		&rec.NodeID, &rec.Label, &rec.Notes, &declaredAt, &rec.DeclaredByPrincipalID,
		&rec.DeclaredByPrincipalName, &rec.LastDiscoveryRunID, &lastDiscoveredAt, &updatedAt,
	); err != nil {
		return NodeDeclarationRecord{}, err
	}
	var err error
	if rec.DeclaredAt, err = dbToTime(declaredAt); err != nil {
		return NodeDeclarationRecord{}, fmt.Errorf("store: parse node declaration declared_at: %w", err)
	}
	if rec.LastDiscoveredAt, err = dbToTimePtr(lastDiscoveredAt); err != nil {
		return NodeDeclarationRecord{}, fmt.Errorf("store: parse node declaration last_discovered_at: %w", err)
	}
	if rec.UpdatedAt, err = dbToTime(updatedAt); err != nil {
		return NodeDeclarationRecord{}, fmt.Errorf("store: parse node declaration updated_at: %w", err)
	}
	return rec, nil
}

func getNodeDeclaration(ctx context.Context, q querier, nodeID string) (NodeDeclarationRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+nodeDeclarationColumns+`FROM node_declarations WHERE node_id = ?`, nodeID)
	rec, err := scanNodeDeclaration(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeDeclarationRecord{}, ErrNodeDeclarationNotFound
	}
	if err != nil {
		return NodeDeclarationRecord{}, fmt.Errorf("store: get node declaration %q: %w", nodeID, err)
	}
	return rec, nil
}

// GetNodeDeclaration returns nodeID's declaration, or [ErrNodeDeclarationNotFound].
func (s *Store) GetNodeDeclaration(ctx context.Context, nodeID string) (NodeDeclarationRecord, error) {
	guardNotInTx(ctx, "Store.GetNodeDeclaration")
	return getNodeDeclaration(ctx, s.db, nodeID)
}

// GetNodeDeclaration is [Store.GetNodeDeclaration]'s [Tx] form.
func (t *Tx) GetNodeDeclaration(ctx context.Context, nodeID string) (NodeDeclarationRecord, error) {
	return getNodeDeclaration(ctx, t.tx, nodeID)
}

func listNodeDeclarations(ctx context.Context, q querier) ([]NodeDeclarationRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT`+nodeDeclarationColumns+`FROM node_declarations ORDER BY node_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list node declarations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []NodeDeclarationRecord
	for rows.Next() {
		rec, err := scanNodeDeclaration(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list node declarations: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list node declarations: %w", err)
	}
	return out, nil
}

// ListNodeDeclarations returns every declared node, ordered by node ID for
// a stable, deterministic result (matching [Store.ListNodes]'s convention).
func (s *Store) ListNodeDeclarations(ctx context.Context) ([]NodeDeclarationRecord, error) {
	guardNotInTx(ctx, "Store.ListNodeDeclarations")
	return listNodeDeclarations(ctx, s.db)
}

// ListNodeDeclarations is [Store.ListNodeDeclarations]'s [Tx] form.
func (t *Tx) ListNodeDeclarations(ctx context.Context) ([]NodeDeclarationRecord, error) {
	return listNodeDeclarations(ctx, t.tx)
}

func recordNodeDiscoverySeen(ctx context.Context, q querier, nodeID, runID string, seenAt, now time.Time) error {
	res, err := q.ExecContext(ctx, `
		UPDATE node_declarations
		SET last_discovery_run_id = ?, last_discovered_at = ?, updated_at = ?
		WHERE node_id = ?
	`, runID, timeToDB(seenAt), timeToDB(now), nodeID)
	if err != nil {
		return fmt.Errorf("store: record node discovery seen for %q: %w", nodeID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: record node discovery seen for %q: %w", nodeID, ErrNodeDeclarationNotFound)
	}
	return nil
}

// RecordNodeDiscoverySeen updates a declared node's last-seen-by-discovery
// evidence. It never creates a row (a discovery run seeing an
// UNDECLARED node is a candidate to propose, not something this method
// touches — see this file's doc comment on discovery proposing vs. an
// operator promoting) and it never deletes one: Step 5's rule, applied to
// inventory instead of observations, is that only a caller (seam B) may
// decide what an absence means, and this method only ever records
// PRESENCE.
func (s *Store) RecordNodeDiscoverySeen(ctx context.Context, nodeID, runID string, seenAt time.Time) error {
	guardNotInTx(ctx, "Store.RecordNodeDiscoverySeen")
	return recordNodeDiscoverySeen(ctx, s.db, nodeID, runID, seenAt, s.now())
}

// RecordNodeDiscoverySeen is [Store.RecordNodeDiscoverySeen]'s [Tx] form.
func (t *Tx) RecordNodeDiscoverySeen(ctx context.Context, nodeID, runID string, seenAt time.Time) error {
	return recordNodeDiscoverySeen(ctx, t.tx, nodeID, runID, seenAt, t.s.now())
}

func deleteNodeDeclaration(ctx context.Context, q querier, nodeID string) error {
	res, err := q.ExecContext(ctx, `DELETE FROM node_declarations WHERE node_id = ?`, nodeID)
	if err != nil {
		return fmt.Errorf("store: delete node declaration %q: %w", nodeID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: delete node declaration %q: %w", nodeID, ErrNodeDeclarationNotFound)
	}
	return nil
}

// DeleteNodeDeclaration removes nodeID from the declared inventory. Per
// BUILD-PLAN Step 7's acceptance criteria, this is deliberately never
// called by discovery itself (RecordNodeDiscoverySeen never deletes) —
// only by an explicit, confirmed operator action, which is seam B's
// endpoint to build. The primitive lands here because it is a generic
// write on this table, not because this seam decides when it fires.
func (s *Store) DeleteNodeDeclaration(ctx context.Context, nodeID string) error {
	guardNotInTx(ctx, "Store.DeleteNodeDeclaration")
	return deleteNodeDeclaration(ctx, s.db, nodeID)
}

// DeleteNodeDeclaration is [Store.DeleteNodeDeclaration]'s [Tx] form.
func (t *Tx) DeleteNodeDeclaration(ctx context.Context, nodeID string) error {
	return deleteNodeDeclaration(ctx, t.tx, nodeID)
}

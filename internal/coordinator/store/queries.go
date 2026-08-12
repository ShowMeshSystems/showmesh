package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// timeLayout is the on-disk representation for every timestamp column;
// see schemaV1's doc comment for why this package owns the format itself
// rather than relying on the driver's time.Time conversion.
const timeLayout = time.RFC3339Nano

func timeToDB(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// timePtrToDB returns nil (which the driver binds as SQL NULL) for a nil
// t, and the formatted time otherwise. Used for the ObservedAt columns
// that are nullable per the shared contract's retained-freshness rule.
func timePtrToDB(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timeToDB(*t)
}

func dbToTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// dbToTimePtr converts a nullable TEXT column back to *time.Time: nil when
// the column was SQL NULL, matching timePtrToDB's encoding.
func dbToTimePtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid {
		return nil, nil
	}
	t, err := dbToTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func boolToDB(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// execer is the subset of *sql.DB and *sql.Tx that upsertNodeStub needs,
// so it can run either as its own statement or inside a caller's
// transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// upsertNodeStub guarantees a nodes row exists for nodeID without
// disturbing any hello content already stored there. node_lwt and
// node_health both have a foreign key on nodes(node_id) (see schemaV1),
// but a health or LWT message can arrive before that node's hello does —
// nothing in ADR-008 orders the three topics relative to each other — so
// RecordLWT and RecordHealth call this first, inside the same transaction,
// to satisfy the FK without waiting for a hello that may not have arrived
// yet. UpsertHello uses its own INSERT ... ON CONFLICT DO UPDATE instead
// of this helper, since it always has real content to write.
func (s *Store) upsertNodeStub(ctx context.Context, ex execer, nodeID string) error {
	now := timeToDB(s.now())
	_, err := ex.ExecContext(ctx, `
		INSERT INTO nodes (node_id, first_seen_at, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(node_id) DO NOTHING
	`, nodeID, now, now)
	if err != nil {
		return fmt.Errorf("store: upsert node stub for %q: %w", nodeID, err)
	}
	return nil
}

// UpsertHello stores nodeID's capability advertisement and evidence
// metadata, replacing any previously stored hello for that node. It does
// not touch first_seen_at once a row exists (see the ON CONFLICT clause
// below, which omits it deliberately).
func (s *Store) UpsertHello(ctx context.Context, nodeID string, h HelloRecord) error {
	guardNotInTx(ctx, "Store.UpsertHello")
	capsJSON, err := json.Marshal(h.Capabilities)
	if err != nil {
		return fmt.Errorf("store: encode capabilities for %q: %w", nodeID, err)
	}

	now := timeToDB(s.now())
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO nodes (
			node_id, label, platform, agent_version, boot_id, started_at,
			capabilities_json, hello_observed_at, hello_provenance, hello_retained,
			first_seen_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			label             = excluded.label,
			platform          = excluded.platform,
			agent_version     = excluded.agent_version,
			boot_id           = excluded.boot_id,
			started_at        = excluded.started_at,
			capabilities_json = excluded.capabilities_json,
			hello_observed_at = excluded.hello_observed_at,
			hello_provenance  = excluded.hello_provenance,
			hello_retained    = excluded.hello_retained,
			updated_at        = excluded.updated_at
	`,
		nodeID, h.Label, h.Platform, h.AgentVersion, h.BootID, timeToDB(h.StartedAt),
		string(capsJSON), timePtrToDB(h.ObservedAt), string(h.Provenance), boolToDB(h.Retained),
		now, now,
	)
	if err != nil {
		return fmt.Errorf("store: upsert hello for %q: %w", nodeID, err)
	}
	return nil
}

// RecordLWT stores nodeID's last-will/online-state evidence. Per
// [LWTRecord]'s doc comment, ObservedAt and Provenance are stored exactly as
// given by the caller (see internal/coordinator/inventory.Manager.classify,
// which derives them from the delivery's retained/live status the same way
// it does for hello and health).
func (s *Store) RecordLWT(ctx context.Context, nodeID string, l LWTRecord) error {
	guardNotInTx(ctx, "Store.RecordLWT")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin record lwt for %q: %w", nodeID, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.upsertNodeStub(ctx, tx, nodeID); err != nil {
		return err
	}

	now := timeToDB(s.now())
	_, err = tx.ExecContext(ctx, `
		INSERT INTO node_lwt (node_id, online, reason, observed_at, provenance, retained, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			online      = excluded.online,
			reason      = excluded.reason,
			observed_at = excluded.observed_at,
			provenance  = excluded.provenance,
			retained    = excluded.retained,
			updated_at  = excluded.updated_at
	`,
		nodeID, boolToDB(l.Online), l.Reason, timePtrToDB(l.ObservedAt),
		string(l.Provenance), boolToDB(l.Retained), now,
	)
	if err != nil {
		return fmt.Errorf("store: record lwt for %q: %w", nodeID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit record lwt for %q: %w", nodeID, err)
	}
	return nil
}

// refreshHealthObservedAt updates only node_health.observed_at (and
// bookkeeping's updated_at) for nodeID, leaving boot_id, sequence,
// agent_state, and uptime_ms exactly as already stored. See RecordHealth's
// doc comment for why this exists: a live duplicate/reorder is proof the
// node is alive right now even though its content is not new evidence, and
// the two must be tracked separately so dedup semantics are preserved
// while liveness freshness still advances.
func (s *Store) refreshHealthObservedAt(ctx context.Context, tx *sql.Tx, nodeID string, h HealthRecord) error {
	now := timeToDB(s.now())
	if _, err := tx.ExecContext(ctx, `
		UPDATE node_health SET observed_at = ?, updated_at = ? WHERE node_id = ?
	`, timePtrToDB(h.ObservedAt), now, nodeID); err != nil {
		return fmt.Errorf("store: refresh observed_at for live duplicate health from %q: %w", nodeID, err)
	}
	return nil
}

// RecordHealth applies the boot ID/sequence acceptance rule from the Step
// 2 round 2 shared contract atomically with the write — the read (current
// stored boot ID and sequence) and the conditional write happen in one
// transaction, which is exactly what Store.SetMaxOpenConns(1) in open()
// exists to make safe against a second concurrent RecordHealth call for
// the same node:
//
//   - No existing row, or a different boot ID: accept unconditionally (a
//     new agent session, or the first evidence ever for this node) and
//     store h as the new evidence.
//   - Same boot ID, sequence <= the currently stored sequence: a duplicate
//     or reordered delivery. QoS 1 makes redelivery normal, so the content
//     (boot ID, sequence, agent state, uptime) is silently ignored
//     (accepted=false, err=nil), never logged as an anomaly by this method
//     — a caller that wants to log it may use the returned accepted value
//     to decide that for itself, at whatever level it judges appropriate.
//     If h is a LIVE delivery (h.Retained == false), observed_at is still
//     advanced to h.ObservedAt before returning, without touching boot_id
//     or sequence: a RETAIN=0 delivery only reaches this process because
//     the node is currently connected and publishing, which is genuine
//     proof of life regardless of whether its sequence number happens to
//     be old news. Without this, a single forged or otherwise pinned
//     maximum sequence for a boot ID would permanently deny every later
//     genuine heartbeat from that boot session the chance to ever count as
//     fresh, degrading a live node to unknown after the staleness window
//     and leaving it there until the agent restarts with a new boot ID —
//     see the Step 2 round 2 review's forged-health-sequence finding. A
//     RETAINED duplicate/reorder gets no such treatment: its age is
//     unknown by definition (see classify in
//     internal/coordinator/inventory), so it is not proof of anything
//     happening now and is ignored exactly as before.
//   - Same boot ID, sequence > the currently stored sequence: accept and
//     store h.
//
// RecordHealth returns (false, nil) for an ignored duplicate/reorder
// (whether or not observed_at was refreshed), (true, nil) on a genuine
// write of new boot ID/sequence content, and (false, err) on any error.
func (s *Store) RecordHealth(ctx context.Context, nodeID string, h HealthRecord) (accepted bool, err error) {
	guardNotInTx(ctx, "Store.RecordHealth")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: begin record health for %q: %w", nodeID, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.upsertNodeStub(ctx, tx, nodeID); err != nil {
		return false, err
	}

	var existingBootID string
	var existingSeq uint64
	switch err := tx.QueryRowContext(ctx, `SELECT boot_id, sequence FROM node_health WHERE node_id = ?`, nodeID).
		Scan(&existingBootID, &existingSeq); {
	case err == sql.ErrNoRows:
		// No prior evidence for this node: fall through and accept.
	case err != nil:
		return false, fmt.Errorf("store: read existing health for %q: %w", nodeID, err)
	default:
		if existingBootID == h.BootID && h.Sequence <= existingSeq {
			if h.Retained {
				return false, nil // defer rolls back; nothing was written
			}
			if err := s.refreshHealthObservedAt(ctx, tx, nodeID, h); err != nil {
				return false, err
			}
			if err := tx.Commit(); err != nil {
				return false, fmt.Errorf("store: commit refresh observed_at for %q: %w", nodeID, err)
			}
			return false, nil
		}
	}

	now := timeToDB(s.now())
	_, err = tx.ExecContext(ctx, `
		INSERT INTO node_health (node_id, boot_id, sequence, agent_state, uptime_ms, observed_at, provenance, retained, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			boot_id     = excluded.boot_id,
			sequence    = excluded.sequence,
			agent_state = excluded.agent_state,
			uptime_ms   = excluded.uptime_ms,
			observed_at = excluded.observed_at,
			provenance  = excluded.provenance,
			retained    = excluded.retained,
			updated_at  = excluded.updated_at
	`,
		nodeID, h.BootID, h.Sequence, h.AgentState, h.UptimeMS,
		timePtrToDB(h.ObservedAt), string(h.Provenance), boolToDB(h.Retained), now,
	)
	if err != nil {
		return false, fmt.Errorf("store: record health for %q: %w", nodeID, err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: commit record health for %q: %w", nodeID, err)
	}
	return true, nil
}

// nodeQueryColumns is the column list shared by GetNode and ListNodes:
// nodes' own columns, followed by node_lwt's (via LEFT JOIN, so all
// nullable), followed by node_health's (via LEFT JOIN, so all nullable).
const nodeQueryColumns = `
	n.node_id, n.label, n.platform, n.agent_version, n.boot_id, n.started_at,
	n.capabilities_json, n.hello_observed_at, n.hello_provenance, n.hello_retained,
	n.first_seen_at, n.updated_at,
	l.online, l.reason, l.observed_at, l.provenance, l.retained,
	h.boot_id, h.sequence, h.agent_state, h.uptime_ms, h.observed_at, h.provenance, h.retained
`

const nodeQueryFrom = `
	FROM nodes n
	LEFT JOIN node_lwt l ON l.node_id = n.node_id
	LEFT JOIN node_health h ON h.node_id = n.node_id
`

// scanNode scans one row shaped by nodeQueryColumns into a NodeRecord.
// hello_provenance == "" is the sentinel for "no hello ever observed" (a
// real hello's provenance is always ProvenanceAgentReport or
// ProvenanceRetainedBrokerState, never empty) — see upsertNodeStub, which
// is the only way a nodes row can exist without hello content. Whether LWT
// and Health are present is instead read directly off the LEFT JOINed
// columns' own SQL NULL-ness (l.online and h.boot_id respectively), since
// those tables have no equivalent stub state to disambiguate.
func scanNode(row interface {
	Scan(dest ...any) error
}) (NodeRecord, error) {
	var rec NodeRecord
	var (
		label, platform, agentVersion, bootID string
		startedAt                             sql.NullString
		capsJSON, helloProvenance             string
		helloObservedAt                       sql.NullString
		helloRetained                         int64
		firstSeenAt, updatedAt                string

		lwtOnline     sql.NullInt64
		lwtReason     sql.NullString
		lwtObservedAt sql.NullString
		lwtProvenance sql.NullString
		lwtRetained   sql.NullInt64

		healthBootID     sql.NullString
		healthSequence   sql.NullInt64
		healthAgentState sql.NullString
		healthUptimeMS   sql.NullInt64
		healthObservedAt sql.NullString
		healthProvenance sql.NullString
		healthRetained   sql.NullInt64
	)

	if err := row.Scan(
		&rec.NodeID, &label, &platform, &agentVersion, &bootID, &startedAt,
		&capsJSON, &helloObservedAt, &helloProvenance, &helloRetained,
		&firstSeenAt, &updatedAt,
		&lwtOnline, &lwtReason, &lwtObservedAt, &lwtProvenance, &lwtRetained,
		&healthBootID, &healthSequence, &healthAgentState, &healthUptimeMS, &healthObservedAt, &healthProvenance, &healthRetained,
	); err != nil {
		return NodeRecord{}, err
	}

	var err error
	if rec.FirstSeenAt, err = dbToTime(firstSeenAt); err != nil {
		return NodeRecord{}, fmt.Errorf("store: parse first_seen_at: %w", err)
	}
	if rec.UpdatedAt, err = dbToTime(updatedAt); err != nil {
		return NodeRecord{}, fmt.Errorf("store: parse updated_at: %w", err)
	}

	if helloProvenance != "" {
		var caps capability.Set
		if err := json.Unmarshal([]byte(capsJSON), &caps); err != nil {
			return NodeRecord{}, fmt.Errorf("store: decode capabilities_json for %q: %w", rec.NodeID, err)
		}
		var started time.Time
		if startedAt.Valid {
			if started, err = dbToTime(startedAt.String); err != nil {
				return NodeRecord{}, fmt.Errorf("store: parse started_at: %w", err)
			}
		}
		observedAt, err := dbToTimePtr(helloObservedAt)
		if err != nil {
			return NodeRecord{}, fmt.Errorf("store: parse hello_observed_at: %w", err)
		}
		rec.Hello = &HelloRecord{
			Label: label, Platform: platform, AgentVersion: agentVersion, BootID: bootID,
			StartedAt: started, Capabilities: caps,
			ObservedAt: observedAt, Provenance: Provenance(helloProvenance), Retained: helloRetained != 0,
		}
	}

	if lwtOnline.Valid {
		observedAt, err := dbToTimePtr(lwtObservedAt)
		if err != nil {
			return NodeRecord{}, fmt.Errorf("store: parse lwt observed_at: %w", err)
		}
		rec.LWT = &LWTRecord{
			Online: lwtOnline.Int64 != 0, Reason: lwtReason.String,
			ObservedAt: observedAt, Provenance: Provenance(lwtProvenance.String), Retained: lwtRetained.Int64 != 0,
		}
	}

	if healthBootID.Valid {
		observedAt, err := dbToTimePtr(healthObservedAt)
		if err != nil {
			return NodeRecord{}, fmt.Errorf("store: parse health observed_at: %w", err)
		}
		rec.Health = &HealthRecord{
			BootID: healthBootID.String, Sequence: uint64(healthSequence.Int64),
			AgentState: healthAgentState.String, UptimeMS: healthUptimeMS.Int64,
			ObservedAt: observedAt, Provenance: Provenance(healthProvenance.String), Retained: healthRetained.Int64 != 0,
		}
	}

	return rec, nil
}

// ErrNodeNotFound is returned by GetNode when nodeID has no row in the
// store.
var ErrNodeNotFound = fmt.Errorf("store: node not found")

// GetNode returns the stored record for nodeID.
func (s *Store) GetNode(ctx context.Context, nodeID string) (NodeRecord, error) {
	guardNotInTx(ctx, "Store.GetNode")
	row := s.db.QueryRowContext(ctx, `SELECT`+nodeQueryColumns+nodeQueryFrom+` WHERE n.node_id = ?`, nodeID)
	rec, err := scanNode(row)
	if err == sql.ErrNoRows {
		return NodeRecord{}, ErrNodeNotFound
	}
	if err != nil {
		return NodeRecord{}, fmt.Errorf("store: get node %q: %w", nodeID, err)
	}
	return rec, nil
}

// ListNodes returns every node the store currently knows about, ordered by
// node ID for a stable, deterministic result.
func (s *Store) ListNodes(ctx context.Context) ([]NodeRecord, error) {
	guardNotInTx(ctx, "Store.ListNodes")
	rows, err := s.db.QueryContext(ctx, `SELECT`+nodeQueryColumns+nodeQueryFrom+` ORDER BY n.node_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []NodeRecord
	for rows.Next() {
		rec, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list nodes: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list nodes: %w", err)
	}
	return out, nil
}

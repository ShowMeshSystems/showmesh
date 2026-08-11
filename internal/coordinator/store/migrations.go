package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrSchemaTooNew is wrapped by the error [migrate] returns when the
// database's PRAGMA user_version is higher than this binary's newest known
// migration. Per ADR-009 ("downgrade is refused"), an older binary must
// refuse to touch a newer database rather than silently run against a
// schema it does not fully understand.
var ErrSchemaTooNew = errors.New("store: database schema version is newer than this binary supports")

// migration is one forward-only, transactionally-applied schema change.
// Migrations are numbered contiguously from 1; the target schema version
// (what migrate writes to PRAGMA user_version once every pending migration
// has applied) is len(migrations).
type migration struct {
	version int
	sql     string
}

// migrations is the full ordered history of this package's schema. Append,
// never edit or remove, an existing entry: modifying a migration that has
// already shipped would silently diverge two coordinators running the same
// binary version but whose databases reached this migration at different
// git revisions.
var migrations = []migration{
	{version: 1, sql: schemaV1},
	{version: 2, sql: schemaV2},
}

// schemaV1 creates the three tables the Step 2 round 2 store task
// requires: nodes (identity, hello contents, and capabilities), node_lwt
// (last-will/online-state evidence), and node_health (heartbeat evidence,
// including the boot ID and sequence RecordHealth uses for duplicate/
// reorder detection). node_lwt and node_health both use node_id as their
// own primary key (one row per node — only the most recently accepted
// evidence is kept, never a history) and reference nodes(node_id), so a
// health or LWT message for a node that has never sent hello still needs a
// row in nodes first; see upsertNodeStub in queries.go.
//
// All timestamp columns are TEXT, RFC3339Nano in UTC (see timeToDB/
// dbToTime in queries.go), not SQLite's own datetime affinity: this
// package parses and formats every one of them itself, rather than
// depending on the driver's time.Time conversion, so the on-disk
// representation is stable regardless of driver version.
const schemaV1 = `
CREATE TABLE nodes (
	node_id           TEXT PRIMARY KEY,
	label             TEXT NOT NULL DEFAULT '',
	platform          TEXT NOT NULL DEFAULT '',
	agent_version     TEXT NOT NULL DEFAULT '',
	boot_id           TEXT NOT NULL DEFAULT '',
	started_at        TEXT,
	capabilities_json TEXT NOT NULL DEFAULT '[]',
	hello_observed_at TEXT,
	hello_provenance  TEXT NOT NULL DEFAULT '',
	hello_retained    INTEGER NOT NULL DEFAULT 0,
	first_seen_at     TEXT NOT NULL,
	updated_at        TEXT NOT NULL
);

CREATE TABLE node_lwt (
	node_id     TEXT PRIMARY KEY REFERENCES nodes(node_id) ON DELETE CASCADE,
	online      INTEGER NOT NULL,
	reason      TEXT NOT NULL DEFAULT '',
	observed_at TEXT NOT NULL,
	provenance  TEXT NOT NULL,
	retained    INTEGER NOT NULL DEFAULT 0,
	updated_at  TEXT NOT NULL
);

CREATE TABLE node_health (
	node_id     TEXT PRIMARY KEY REFERENCES nodes(node_id) ON DELETE CASCADE,
	boot_id     TEXT NOT NULL,
	sequence    INTEGER NOT NULL,
	agent_state TEXT NOT NULL DEFAULT '',
	uptime_ms   INTEGER NOT NULL DEFAULT 0,
	observed_at TEXT,
	provenance  TEXT NOT NULL DEFAULT '',
	retained    INTEGER NOT NULL DEFAULT 0,
	updated_at  TEXT NOT NULL
);
`

// schemaV2 makes node_lwt.observed_at nullable, so an LWT delivery can
// express "age unknown" the same way node_health.observed_at already does
// (that column was nullable from schemaV1; only node_lwt was missed — see
// the Step 2 round 2 review's LWT-freshness fix). SQLite's ALTER TABLE
// cannot drop a NOT NULL constraint directly, so this follows SQLite's
// documented "12 steps to altering a table" pattern: create the new table
// shape, copy every row across unchanged (existing rows already satisfy
// NOT NULL, so this copy can never violate the relaxed constraint), drop
// the old table, and rename the new one into its place. Per migrations.go's
// append-only rule, schemaV1 above is left untouched; this is a new,
// separate migration.
const schemaV2 = `
CREATE TABLE node_lwt_v2 (
	node_id     TEXT PRIMARY KEY REFERENCES nodes(node_id) ON DELETE CASCADE,
	online      INTEGER NOT NULL,
	reason      TEXT NOT NULL DEFAULT '',
	observed_at TEXT,
	provenance  TEXT NOT NULL,
	retained    INTEGER NOT NULL DEFAULT 0,
	updated_at  TEXT NOT NULL
);

INSERT INTO node_lwt_v2 (node_id, online, reason, observed_at, provenance, retained, updated_at)
	SELECT node_id, online, reason, observed_at, provenance, retained, updated_at FROM node_lwt;

DROP TABLE node_lwt;

ALTER TABLE node_lwt_v2 RENAME TO node_lwt;
`

// migrate applies every pending migration inside one transaction and
// refuses (returning an error wrapping [ErrSchemaTooNew]) if db's current
// schema version is already newer than this binary's newest known
// migration. It is idempotent: calling it again once the database is
// already at the newest known version is a no-op that opens no
// transaction at all.
func migrate(ctx context.Context, db *sql.DB) error {
	target := len(migrations)

	var current int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}

	if current > target {
		return fmt.Errorf("%w: database is at schema version %d, this binary only knows up to %d",
			ErrSchemaTooNew, current, target)
	}
	if current == target {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit succeeds

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("store: apply migration %d: %w", m.version, err)
		}
	}

	// PRAGMA statements do not accept bound parameters in SQLite; target is
	// this package's own len(migrations), never external input, so building
	// the statement with fmt.Sprintf is safe here.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, target)); err != nil {
		return fmt.Errorf("store: set schema version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration transaction: %w", err)
	}
	return nil
}

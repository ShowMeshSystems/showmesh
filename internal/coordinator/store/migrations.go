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
	{version: 3, sql: schemaV3},
	{version: 4, sql: schemaV4},
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

// schemaV3 adds the Step 3 observability tables this package's
// UpsertObservation/ListObservations (observations.go) and
// AppendEvent/ListEvents/LatestEventSeq (events.go) operate on.
//
// Neither table stores a derived verdict — no state/health/is_stale
// column. schemaV1's doc comment already states this package's central
// rule (evidence is stored, a verdict is computed on read against the
// caller's clock); observations and events are bound by exactly the same
// rule, just for pkg/observation's evidence model instead of node liveness.
// observation.Observation.StateAt is what does that computation once a row
// comes back out.
//
// observations is an upsert target, never a history — one row per
// (resource_kind, resource_id, signal), which is its primary key — per the
// Step 3 contract's "Observations are stored latest-only in Step 3; RES-013
// owns time-series retention." value_kind/value_text is a discriminated
// encoding of observation.Observation.Value (bool | string | int64 |
// float64), not a single JSON or NUMERIC column: SQLite's NUMERIC affinity
// (and a naive round-trip through JSON) would silently convert an int64
// above 2^53 to a float64 and lose precision, and would not distinguish an
// integral-valued float64 from an int64 on the way back out, since both
// would decode to the same JSON number. Reading value_kind first and
// parsing value_text accordingly (encodeObservationValue/
// decodeObservationValue in observations.go) makes the round trip exact
// instead of approximate. observed_at is nullable for the identical reason
// node_health.observed_at is (see schemaV2 and HelloRecord.ObservedAt's doc
// comment in model.go): a NOT NULL DEFAULT here would silently manufacture
// a false freshness claim, which is the one thing ADR-011 exists to
// prevent.
//
// events is append-only history, ordered by seq. seq is INTEGER PRIMARY
// KEY AUTOINCREMENT specifically, not a bare INTEGER PRIMARY KEY: SQLite
// treats a bare INTEGER PRIMARY KEY as an alias for rowid and is free to
// reuse a rowid once the row with the highest value has been deleted,
// while AUTOINCREMENT keeps its own monotonic high-water mark in the
// sqlite_sequence table that survives every row being deleted. That
// distinction is the entire reason AUTOINCREMENT is spelled out here: a
// reused seq would let a client's `since` cursor silently replay events it
// has already seen, which is indistinguishable, from the wire, from a
// duplicate delivery of a change the client would then act on twice.
// LatestEventSeq reads sqlite_sequence directly rather than MAX(seq), so it
// keeps reporting the true high-water mark even if every row were ever
// deleted, by pruning or any other means — see LatestEventSeq's own doc
// comment in events.go for why this package does not rely on its own
// pruning behavior to guarantee that case is ever actually reached.
//
// idx_events_recorded_at exists for the pruner's age-based deletion
// (`DELETE FROM events WHERE recorded_at < ?` in events.go): seq order and
// recorded_at order coincide for any coordinator whose clock does not jump
// backwards, but nothing here relies on that coincidence to avoid a full
// table scan on every prune. ListEvents itself needs no index beyond the
// seq primary key: it only ever filters on `seq > ?` ordered by seq, which
// the primary key already serves.
const schemaV3 = `
CREATE TABLE observations (
	resource_kind TEXT NOT NULL,
	resource_id   TEXT NOT NULL,
	signal        TEXT NOT NULL,
	value_kind    TEXT NOT NULL DEFAULT '',
	value_text    TEXT NOT NULL DEFAULT '',
	unit          TEXT NOT NULL DEFAULT '',
	observed_at   TEXT,
	collected_at  TEXT NOT NULL,
	source        TEXT NOT NULL DEFAULT '',
	quality       TEXT NOT NULL DEFAULT '',
	valid_for_ns  INTEGER NOT NULL DEFAULT 0,
	absence       TEXT NOT NULL DEFAULT '',
	reason        TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	updated_at    TEXT NOT NULL,
	PRIMARY KEY (resource_kind, resource_id, signal)
);

CREATE TABLE events (
	seq            INTEGER PRIMARY KEY AUTOINCREMENT,
	recorded_at    TEXT NOT NULL,
	occurred_at    TEXT,
	source         TEXT NOT NULL,
	resource_kind  TEXT NOT NULL,
	resource_id    TEXT NOT NULL,
	category       TEXT NOT NULL,
	severity       TEXT NOT NULL,
	summary        TEXT NOT NULL,
	details        TEXT NOT NULL DEFAULT '{}',
	correlation_id TEXT
);

CREATE INDEX idx_events_recorded_at ON events(recorded_at);
`

// schemaV4 adds source to observations' primary key, so two collector
// sources recording the same (resource_kind, resource_id, signal) coexist as
// two rows instead of silently overwriting one another at whichever
// collector's poll/publish cadence happens to write last.
//
// Step 5 is what makes this a live defect rather than a theoretical one:
// internal/coordinator/collector/fpp (source "fpp-rest") and
// internal/coordinator/collector/fppmqtt (source "fpp-mqtt") both produce
// the identically-named signal for the same FPP instance — see the Step 5
// contract section 4.3's "signal IDs are deliberately identical" — so under
// schemaV3's (resource_kind, resource_id, signal) key, whichever source
// upserted most recently would silently erase the other's evidence, and an
// operator watching health flicker between a 1s MQTT value and a 15s REST
// value would have no way to tell the two apart, because only one row could
// ever exist. Provenance destroyed at write time this way is also
// impossible to recover: the Step 5 contract's precedence rule (resolving
// which source wins, once, at read) is meaningless if there is only ever
// one row to resolve from — see ResolveObservations in
// internal/coordinator/api for that rule; this migration is what makes both
// candidates available for it to choose between in the first place.
//
// Follows the same SQLite "12 steps to altering a table" pattern schemaV2
// already established (SQLite's ALTER TABLE cannot add a column to an
// existing composite PRIMARY KEY in place): create the new table shape,
// copy every existing row across unchanged, drop the old table, rename the
// new one into its place. Every column keeps its exact schemaV3 name, type,
// and default; only the PRIMARY KEY clause changes, so the copy can never
// violate the new (strictly wider) key — a row that was unique under
// (resource_kind, resource_id, signal) is trivially still unique once
// source is appended to that same tuple.
const schemaV4 = `
CREATE TABLE observations_v4 (
	resource_kind TEXT NOT NULL,
	resource_id   TEXT NOT NULL,
	signal        TEXT NOT NULL,
	value_kind    TEXT NOT NULL DEFAULT '',
	value_text    TEXT NOT NULL DEFAULT '',
	unit          TEXT NOT NULL DEFAULT '',
	observed_at   TEXT,
	collected_at  TEXT NOT NULL,
	source        TEXT NOT NULL DEFAULT '',
	quality       TEXT NOT NULL DEFAULT '',
	valid_for_ns  INTEGER NOT NULL DEFAULT 0,
	absence       TEXT NOT NULL DEFAULT '',
	reason        TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	updated_at    TEXT NOT NULL,
	PRIMARY KEY (resource_kind, resource_id, signal, source)
);

INSERT INTO observations_v4 (
	resource_kind, resource_id, signal,
	value_kind, value_text, unit,
	observed_at, collected_at, source, quality, valid_for_ns,
	absence, reason,
	first_seen_at, updated_at
)
SELECT
	resource_kind, resource_id, signal,
	value_kind, value_text, unit,
	observed_at, collected_at, source, quality, valid_for_ns,
	absence, reason,
	first_seen_at, updated_at
FROM observations;

DROP TABLE observations;

ALTER TABLE observations_v4 RENAME TO observations;
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

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
	{version: 5, sql: schemaV5},
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

// schemaV5 adds Step 6's identity, authorization, and audit tables
// (ADR-024): principals, principal_tokens, principal_sessions, audit_log,
// and bootstrap. This is a pure-addition migration — nothing from schemaV1
// through schemaV4 is touched — so unlike schemaV2 and schemaV4 it needs
// none of SQLite's "12 steps to altering a table" dance; every statement
// below is a plain CREATE.
//
// Every table here holds a credential-adjacent secret or the evidence of
// one, so three rules apply across all five and are recorded once here
// rather than repeated per table:
//
//  1. No column in this migration ever stores a credential in the clear.
//     principals.password_hash is an argon2id PHC string (identity
//     package's password.go), never the password itself.
//     principal_tokens.digest and principal_sessions.digest are SHA-256
//     hex digests of the bearer token / session cookie value (identity
//     package's token.go and session.go), never the raw value — the same
//     reasoning ADR-009 already applies to node health evidence applies
//     here to credentials: what is stored is what survives a backup or an
//     export bundle, and a raw secret surviving either is the failure this
//     schema is built to avoid. bootstrap.code_digest follows the same
//     rule for the single-use bootstrap code (ADR-024 decision 9); the raw
//     code exists only in the file the identity package writes to the
//     data volume, never in this database.
//  2. None of these five tables may be included in a future ADR-009 YAML
//     export bundle by omission. A password hash, a token digest, a
//     session row, or the bootstrap file all being "just data" to a naive
//     exporter is exactly the failure ADR-009's "excluded explicitly
//     rather than by omission" rule exists to catch; whoever implements
//     export must add all five to its exclusion list explicitly, and this
//     comment is what a future contributor grepping for "export" from this
//     migration should find.
//  3. audit_log is append-only. No repository method in this package may
//     ever UPDATE a row in it — see audit.go, where every write is an
//     INSERT and the only other statement audit.go issues against this
//     table is retention's bounded DELETE (pruneAudit, mirroring
//     pruneEvents in retention.go). ADR-024 decision 11 requires dispatch
//     and outcome to be separate correlated entries rather than one row
//     mutated in place — command_id is what correlates them — precisely so
//     that no code path ever needs to UPDATE this table at all.
//
// principals.generation is decision 5's per-principal generation counter:
// principal_sessions.generation is stamped with the principal's current
// generation at the moment a session is created (see identity package's
// CreateSession), and [Store.AuthenticateSession] rejects any session whose
// stored generation is less than the principal's current one. A password
// change, an admin revoke-all, or a database restore is implemented as
// bumping principals.generation — see [Store.BumpPrincipalGeneration] — not
// as touching every existing session row, which is what makes revoke-all
// O(1) instead of O(sessions).
//
// principal_tokens.hint and principal_sessions.id are both deliberately
// NOT secrets, unlike digest: hint is a short, non-secret slice of the
// token's random component (enough for an operator to tell two tokens with
// the same label apart in a listing, nowhere near enough to reconstruct the
// token — see identity package's token.go), and principal_sessions.id is an
// opaque row identifier distinct from the session's own secret value,
// specifically so that listing or revoking a session never has to return
// or accept the bearer secret again after creation — see the identity
// package's Service doc comment for why Session.ID is not treated as
// interchangeable with the cookie value the way the Step 6 contract's
// literal type comment first suggested, and why this migration stores only
// digest, never the value ID would need to equal if it were.
//
// ON DELETE CASCADE on principal_id in principal_tokens and
// principal_sessions matches the FK style schemaV1 already established for
// node_lwt/node_health referencing nodes: deleting a principal (there is no
// repository method for this yet — Step 6 adds no delete-principal
// endpoint — but the constraint is cheap to have correct now rather than
// discovered missing later) takes its tokens and sessions with it rather
// than leaving orphaned rows an unrelated future principal's row could
// collide with if ids were ever reused.
const schemaV5 = `
CREATE TABLE principals (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL UNIQUE,
	kind          TEXT NOT NULL,
	role          TEXT NOT NULL,
	password_hash TEXT NOT NULL DEFAULT '',
	disabled      INTEGER NOT NULL DEFAULT 0,
	generation    INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);

CREATE TABLE principal_tokens (
	id           TEXT PRIMARY KEY,
	principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
	digest       TEXT NOT NULL UNIQUE,
	hint         TEXT NOT NULL DEFAULT '',
	label        TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	expires_at   TEXT,
	revoked_at   TEXT,
	last_used_at TEXT
);

CREATE INDEX idx_principal_tokens_principal_id ON principal_tokens(principal_id);

CREATE TABLE principal_sessions (
	id           TEXT PRIMARY KEY,
	principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
	digest       TEXT NOT NULL UNIQUE,
	device_label TEXT NOT NULL DEFAULT '',
	generation   INTEGER NOT NULL DEFAULT 0,
	created_at   TEXT NOT NULL,
	last_used_at TEXT NOT NULL,
	revoked_at   TEXT
);

CREATE INDEX idx_principal_sessions_principal_id ON principal_sessions(principal_id);

CREATE TABLE audit_log (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	recorded_at     TEXT NOT NULL,
	principal_id    TEXT NOT NULL DEFAULT '',
	principal_name  TEXT NOT NULL DEFAULT '',
	form            TEXT NOT NULL DEFAULT '',
	credential_id   TEXT NOT NULL DEFAULT '',
	client_addr     TEXT NOT NULL DEFAULT '',
	action          TEXT NOT NULL DEFAULT '',
	target          TEXT NOT NULL DEFAULT '',
	params_json     TEXT NOT NULL DEFAULT '{}',
	idempotency_key TEXT NOT NULL DEFAULT '',
	kind            TEXT NOT NULL,
	command_id      TEXT NOT NULL DEFAULT '',
	outcome         TEXT NOT NULL DEFAULT '',
	outcome_state   TEXT NOT NULL DEFAULT '',
	outcome_reason  TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_audit_log_recorded_at ON audit_log(recorded_at);
CREATE INDEX idx_audit_log_command_id ON audit_log(command_id);

CREATE TABLE bootstrap (
	id          INTEGER PRIMARY KEY CHECK (id = 1),
	code_digest TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	expires_at  TEXT NOT NULL,
	claimed_at  TEXT
);
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

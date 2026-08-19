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
	{version: 6, sql: schemaV6},
	{version: 7, sql: schemaV7},
	{version: 8, sql: schemaV8},
	{version: 9, sql: schemaV9},
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
//
//  2. None of these five tables may be included in a future ADR-009 YAML
//     export bundle by omission. A password hash, a token digest, a
//     session row, or the bootstrap file all being "just data" to a naive
//     exporter is exactly the failure ADR-009's "excluded explicitly
//     rather than by omission" rule exists to catch; whoever implements
//     export must add all five to its exclusion list explicitly, and this
//     comment is what a future contributor grepping for "export" from this
//     migration should find.
//
//     Attribution, because this comment previously blurred it: ADR-024's
//     consequences name the specific things to exclude, which are password
//     hashes, token hashes, session records, the broker credential
//     mapping, and the bootstrap file. The whole-table rule above is
//     wider than that, since it also covers audit_log and the non-secret
//     columns of principals, and it is this package's own recommendation
//     rather than a quotation of the ADR. It is kept because an export
//     bundle is configuration and none of these five tables is
//     configuration, so excluding them wholesale costs nothing and
//     removes a judgement call from whoever writes the exporter. See
//     RES-008 section 6a decision D5, and note that D4 makes secret
//     export an explicit opt-in elsewhere in the bundle, which does not
//     reopen these five.
//
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
// principal_tokens.generation mirrors principal_sessions.generation exactly,
// stamped at issue time the same way ([Store.CreateToken]) and checked the
// same way ([identity.Service.AuthenticateToken]) — a review finding on this
// step's own implementation caught that a bearer token carried no
// generation at all, so a SetRole/SetDisabled/RevokeAllSessions generation
// bump closed a cookie-backed stream within one revalidation tick and did
// nothing to a token-backed one (decision 12's stale-scope bound did not
// hold for tokens, which includes the UI's own break-glass path). A token
// is exactly as sensitive as a session — it authenticates the identical set
// of actions — so it gets the identical treatment rather than a
// token-specific exception.
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
	generation   INTEGER NOT NULL DEFAULT 0,
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

// schemaV6 adds Step 7's foundation tables (BUILD-PLAN Step 7, seam 0):
// configuration objects and their immutable revisions (RES-008 D1),
// declared node inventory (RES-008 D2), discovery runs, the command
// journal (ARCHITECTURE §8.1's envelope), and desired state (ADR-003's
// split, expressed in storage). This is a pure-addition migration —
// nothing from schemaV1 through schemaV5 is touched — so, like schemaV5,
// it needs none of SQLite's "12 steps to altering a table" dance; every
// statement below is a plain CREATE. This creates SIX tables, not five —
// config_objects and config_revisions are two separate tables sharing one
// file — with repository methods living in five files: config.go (both
// config tables), nodes_declared.go, discovery.go, commands.go, and
// desired_state.go, each with a *Store form and a *Tx form built over the
// identical querier-based body — see store/tx.go's [Tx] doc comment and
// [appendAuditEntry]'s pattern in audit.go, which every writer here
// follows.
//
// config_objects / config_revisions (RES-008 D1). ADR-009 makes revisions
// immutable: config_revisions has no UpdateConfigRevision method and must
// never grow one, exactly as audit_log (schemaV5) has no update path — the
// only mutable value in either table is config_objects.current_revision,
// which activates an already-written, never-edited revision. Seam A owns
// the payload schema (what payload_json actually contains per config kind)
// and the SHOWMESH_FPP_ENDPOINTS migration; this migration lands only the
// generic tables and the generic revision repository.
//
// config_revisions is NEVER pruned, by this migration or by any retention
// pass this package runs — pruning revisions would delete the rollback
// ADR-009 requires, so its unbounded growth is a recorded open item for
// RES-013 rather than a bound this package quietly imposes. node_declarations
// is the same: never pruned, for the reason its own paragraph below gives.
//
// node_declarations (RES-008 D2). node_id deliberately carries NO foreign
// key to nodes, and that absence is the point: nodes rows are observations
// from agent hellos (schemaV1) and disappear/reappear as agents connect and
// disconnect, while a declared node is an operator's durable inventory
// decision that must survive its observed row being absent — an
// ON DELETE CASCADE here would silently implement the auto-deletion
// RES-008 D6 forbids, exactly at the schema layer, the moment a node's
// nodes row happened to be purged or never existed yet. Powered-off
// equipment is normal outside display hours; this is the fourth time this
// codebase has needed "absence of evidence is not evidence of absence"
// recorded against a concrete schema decision (see schemaV3's ObservedAt
// nullability, schemaV2's LWT freshness fix, and the events/audit gap
// reporting), and the next person should inherit that reasoning rather
// than rediscover it. last_discovered_at is nullable for the identical
// reason every other "when was this last seen" column in this package is
// (schemaV1's health/LWT ObservedAt, schemaV3's observations.observed_at):
// NULL means "no complete discovery run has ever seen this node", never
// "seen at time zero" — a NOT NULL DEFAULT here would manufacture a false
// freshness claim, which is the one thing ADR-011 exists to prevent.
//
// discovery_runs. complete exists so seam B can apply Step 5's
// only-a-complete-poll-may-prune rule to node inventory instead of
// observations: only a discovery run with complete=1 may say anything
// about a node's absence, and reason exists so an incomplete run states
// WHY per ADR-020's absent-evidence rule, rather than a missing row
// standing in for "did not finish" — a run that fails partway is a row
// with complete=0 and a reason, never silence.
//
// commands. Columns are fixed by ARCHITECTURE §8.1's envelope — identifier,
// target, parameters, idempotency key, deadline, issuer, requested
// revision, confirmation method, result — so seam C's pkg/command types map
// onto this table without changing it. idempotency_key is UNIQUE, and
// replay detection is that constraint violation, never a SELECT followed
// by an INSERT: a read-then-write is a race by construction (this project
// has already shipped one test that passed or failed on scheduling rather
// than on correctness — see CLAUDE.md's Step 4/CI lessons), so
// [Store.InsertCommand]/[Tx.InsertCommand] surface a duplicate key as the
// distinguishable [ErrCommandIdempotencyKeyExists], carrying the existing
// row, rather than ever checking first. outcome_state uses
// pkg/observation's state vocabulary, matching audit_log.outcome_state
// (schemaV5): an unresolved command carries a state and a reason, never a
// null that renders as blank.
//
// desired_state (ADR-003's split, expressed in storage). value_kind/
// value_text is the identical discriminated encoding schemaV3's
// observations table uses and for the identical reason its doc comment
// gives: a single JSON or NUMERIC column loses an int64 above 2^53 and
// cannot tell an integral float from an integer on the way back —
// [encodeObservationValue]/[decodeObservationValue] (observations.go) are
// reused as-is rather than duplicated (desired_state.go has no second
// encode/decode pair). NOTHING RECONCILES THIS TABLE, and that is a
// standing constraint this migration records rather than a gap: a
// background loop comparing desired to observed and re-issuing commands to
// close any gap would make ShowMesh a second scheduler, which ADR-001
// forbids as this project's very first constraint. This table exists only
// so ADR-003's desired/observed split is expressible in storage and so a
// command's confirmation has a recorded target to compare against — that
// is the whole of what it is for in Step 7.
const schemaV6 = `
CREATE TABLE config_objects (
	kind             TEXT NOT NULL,
	id               TEXT NOT NULL,
	current_revision INTEGER NOT NULL DEFAULT 0,
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL,
	PRIMARY KEY (kind, id)
);

CREATE TABLE config_revisions (
	kind                      TEXT NOT NULL,
	object_id                 TEXT NOT NULL,
	revision                  INTEGER NOT NULL,
	payload_json              TEXT NOT NULL,
	created_at                TEXT NOT NULL,
	created_by_principal_id   TEXT NOT NULL DEFAULT '',
	created_by_principal_name TEXT NOT NULL DEFAULT '',
	source                    TEXT NOT NULL DEFAULT '',
	note                      TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (kind, object_id, revision)
);

CREATE TABLE node_declarations (
	node_id                    TEXT PRIMARY KEY,
	label                      TEXT NOT NULL DEFAULT '',
	notes                      TEXT NOT NULL DEFAULT '',
	declared_at                TEXT NOT NULL,
	declared_by_principal_id   TEXT NOT NULL DEFAULT '',
	declared_by_principal_name TEXT NOT NULL DEFAULT '',
	last_discovery_run_id      TEXT NOT NULL DEFAULT '',
	last_discovered_at         TEXT,
	updated_at                 TEXT NOT NULL
);

CREATE TABLE discovery_runs (
	id                          TEXT PRIMARY KEY,
	started_at                  TEXT NOT NULL,
	finished_at                 TEXT,
	complete                    INTEGER NOT NULL DEFAULT 0,
	reason                      TEXT NOT NULL DEFAULT '',
	found_count                 INTEGER NOT NULL DEFAULT 0,
	initiated_by_principal_id   TEXT NOT NULL DEFAULT '',
	initiated_by_principal_name TEXT NOT NULL DEFAULT ''
);

CREATE TABLE commands (
	id                    TEXT PRIMARY KEY,
	idempotency_key       TEXT NOT NULL UNIQUE,
	action                TEXT NOT NULL,
	target_kind           TEXT NOT NULL DEFAULT '',
	target_id             TEXT NOT NULL DEFAULT '',
	params_json           TEXT NOT NULL DEFAULT '{}',
	issuer_principal_id   TEXT NOT NULL DEFAULT '',
	issuer_principal_name TEXT NOT NULL DEFAULT '',
	requested_revision    TEXT NOT NULL DEFAULT '',
	confirmation_method   TEXT NOT NULL DEFAULT '',
	deadline_at           TEXT,
	created_at            TEXT NOT NULL,
	dispatched_at         TEXT,
	resolved_at           TEXT,
	state                 TEXT NOT NULL,
	result_json           TEXT NOT NULL DEFAULT '{}',
	outcome_state         TEXT NOT NULL DEFAULT '',
	outcome_reason        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_commands_created_at ON commands(created_at);

CREATE TABLE desired_state (
	resource_kind             TEXT NOT NULL,
	resource_id               TEXT NOT NULL,
	signal                    TEXT NOT NULL,
	value_kind                TEXT NOT NULL DEFAULT '',
	value_text                TEXT NOT NULL DEFAULT '',
	requested_at              TEXT NOT NULL,
	requested_by_principal_id TEXT NOT NULL DEFAULT '',
	command_id                TEXT NOT NULL DEFAULT '',
	deadline_at               TEXT,
	PRIMARY KEY (resource_kind, resource_id, signal)
);
`

// schemaV7 adds Step 9's macro execution history (Wave 1a; ADR-031, the
// "macro execution model" record). This is a pure-addition migration —
// nothing from schemaV1 through schemaV6 is touched — so, like schemaV5
// and schemaV6, it needs none of SQLite's "12 steps to altering a table"
// dance; every statement below is a plain CREATE. Repository methods live
// in macro_runs.go, following the *Store/*Tx-form-over-one-querier-body
// pattern every writer since schemaV6 uses (see tx.go's [Tx] doc comment).
//
// STEP-9-SPEC.md §3 is explicit that this is new storage, not a config
// kind: "show.macro" and "show.action" (Track E's namespace, owned by this
// step per STEP-9-SPEC.md §5.1) are DEFINITIONS, stored as ordinary
// config_objects/config_revisions rows (schemaV6) with no new table of
// their own. A macro RUN is different — it is execution history with
// per-step outcomes, timestamps, and a link to a dispatched commands row
// — so it needs the two tables below.
//
// macro_runs. One row per submitted run (ADR-031 decision 1: a run is
// asynchronous — POST creates this row and returns immediately; nothing
// ever blocks waiting for it to reach state='finished').
//
//   - macro_revision is the macro's config_revisions.revision PINNED AT
//     SUBMISSION, never re-read from config_objects.current_revision once
//     the run exists: ADR-031's own text is "a run pins the macro revision
//     and each action revision at submission, so editing a macro at 16:58
//     cannot change what the 17:00 run does halfway through." Same reason
//     macro_run_steps.action_revision below is pinned per step.
//
//   - idempotency_key is UNIQUE, exactly like commands.idempotency_key
//     (schemaV6): replay detection for a run is that constraint, and
//     macro_runs.go's createMacroRun follows commands.go's insertCommand
//     precedent of never trusting a SELECT-then-INSERT as the actual
//     race-free source of truth for it, for the identical reason
//     (CLAUDE.md's Step 4 lesson: a read-then-write is a race by
//     construction). What is NEW here relative to insertCommand is a
//     second guard layered on top — see the next paragraph — which *is*
//     implemented as a SELECT-then-conditional-INSERT, but is safe for a
//     reason specific to this package rather than in spite of it.
//
//   - ADR-031 decision 6's overlap refusal ("a run submitted while another
//     run of the same macro is running is refused") is NOT a second UNIQUE
//     constraint (e.g. a partial unique index on (macro_object_id) WHERE
//     state='running'). It is checked by macro_runs.go's createMacroRun
//     reading macro_runs for an existing running row for the same
//     macro_object_id, then conditionally inserting, ALL inside one
//     transaction. That read-then-conditionally-write shape is exactly
//     queries.go's RecordHealth precedent (its own doc comment: "the read
//     ... and the conditional write happen in one transaction, which is
//     exactly what Store.SetMaxOpenConns(1) in open() exists to make safe
//     against a second concurrent call") — this package's connection pool
//     is capped at exactly one connection, so two concurrent
//     Store.CreateMacroRun calls cannot interleave their reads and writes
//     no matter how the Go scheduler orders them; the second call's
//     BeginTx blocks until the first's commits or rolls back. A second,
//     redundant UNIQUE index was deliberately not added: it would either
//     duplicate a guarantee the connection cap already provides, or (if
//     the cap were ever relaxed) fail in a way a partial index's ambiguous
//     "UNIQUE constraint failed" error string could not itself distinguish
//     from an idempotency_key collision without a second round of
//     substring matching layered on isUniqueConstraintErr's existing one
//     (identity.go) — see macro_runs.go's createMacroRun doc comment for
//     why idempotency lookup deliberately runs BEFORE the overlap check:
//     a legitimate retry of an already-accepted submission (same
//     idempotency_key) must return the existing run — even one still
//     state='running' — never a spurious "already in flight" refusal of
//     itself.
//
//   - completed and confirmed are INTEGER with NO "NOT NULL DEFAULT",
//     deliberately, and are the single most important columns in this
//     migration. ADR-031 decision 3 requires them kept as two separate
//     facts, never collapsed into one another or derived from each other,
//     and CLAUDE.md's own recurring defect across this project's history
//     is exactly a value that means "unknown" rendering as a value that
//     means "no" — an observations.observed_at NOT NULL DEFAULT would have
//     manufactured a false freshness claim (schemaV3), a bare boolean
//     health column would have done the identical thing for liveness
//     (ADR-011), and a NOT NULL DEFAULT 0 on either of these two columns
//     would do it again here: a run still in flight would report
//     "completed: false, confirmed: false" — indistinguishable from a run
//     that finished and failed both — rather than "not decided yet",
//     which is the true state of a running run per ADR-031 decision 3's
//     "before a run finishes, neither is known." NULL here means exactly
//     that, decoded by macro_runs.go's dbToBoolPtr into a Go *bool (nil ==
//     unknown), matching the *time.Time-for-"genuinely unknown" pattern
//     this package already uses throughout (schemaV1's HelloRecord.
//     ObservedAt, schemaV6's CommandRecord.DeadlineAt/DispatchedAt/
//     ResolvedAt). [Store.FinishMacroRun] is the only writer that ever
//     sets either column, and it always sets both to a definite value in
//     the same UPDATE — there is no code path in this package that leaves
//     a finished run with one of the pair still NULL.
//
//   - attribution_degraded IS NOT NULL DEFAULT 0, unlike completed/
//     confirmed, because ADR-031 decision 5's cost ("a macro containing
//     both a stop and a start dispatches the start with degraded
//     attribution too... it must be recorded on the wire as
//     attributionDegraded: true") is a fact about how the run's steps were
//     actually dispatched, not an outcome verdict — it is well-defined
//     (false) from the moment the row is created, unlike completed/
//     confirmed which are meaningless until the run is over.
//     [Store.SetMacroRunAttributionDegraded] flips it to true mid-run, the
//     moment the executor (Wave 2) actually hits an audit-write failure on
//     an exempt run — a capability this migration's column supports beyond
//     STEP-9-SPEC.md §6.1's literal ask of "recorded at finish", so an
//     operator watching a STILL-RUNNING macro through the read API sees
//     degraded attribution as soon as it happens rather than only once the
//     run completes; see decision 5's own text, "it must be surfaced,"
//     which this migration reads as not limited to post-hoc surfacing.
//
//   - trigger records who/what caused the run to start ("api" | "plugin" |
//     "cli" | "ui" per STEP-9-SPEC.md §6.1), which is what lets Wave 3's
//     plugin-vs-human-operator distinction exist on the wire at all — a
//     column this package stores as an opaque string and does not validate
//     the vocabulary of, matching commands.go's Action/TargetKind and
//     config.go's Source, none of which this package enforces an enum on
//     either.
//
// macro_run_steps. One row per step of a run, written once at creation
// time (all of them, in [Store.CreateMacroRun]/[Tx.CreateMacroRun]'s one
// transaction) and updated in place as each step dispatches and resolves
// — never inserted incrementally as the executor works through the run,
// so a caller reading a run mid-execution always sees every step it will
// ever have, distinguishing "not reached yet" (state carries whatever the
// executor initializes an unstarted step to) from "this run only has 3
// steps" structurally rather than by the row simply not existing yet.
//
//   - PRIMARY KEY (run_id, step_index): step_index is the step's fixed
//     position in the macro's step list at the pinned revision, assigned
//     once at creation and never renumbered.
//
//   - run_id REFERENCES macro_runs(id) ON DELETE CASCADE, matching
//     schemaV1's node_lwt/node_health FK-to-nodes style (never
//     node_declarations' or config_revisions' deliberate FK *absence* —
//     see schemaV6's doc comment for why those two are different): a
//     macro_run_steps row has no meaning independent of the run it
//     belongs to, so retention.go's pruneMacroRuns only ever issues a
//     DELETE against macro_runs itself and relies on this CASCADE (which
//     requires PRAGMA foreign_keys=on, already set unconditionally by
//     store.go's open() and proven enforced by TestOpenAppliesPragmas) to
//     take that run's steps with it — see STEP-9-SPEC.md's Wave 1a brief,
//     "deleting a run's steps with it."
//
//   - action_revision is pinned at submission for the identical reason
//     macro_runs.macro_revision is (see above): resolved once, against the
//     action object's config_revisions row current at creation time, and
//     never re-read from config_objects.current_revision thereafter.
//
//   - dispatched_at/resolved_at are nullable TEXT, matching
//     commands.dispatched_at/resolved_at (schemaV6) exactly and for the
//     identical reason (CommandRecord's doc comment in commands.go): nil
//     means "not yet", never a zero time standing in for it.
//
//   - outcome is the ADR-029-decision-4-preserving five-value vocabulary
//     STEP-9-SPEC.md §6.4 names — confirmed | unconfirmed | unconfirmable |
//     failed | skipped — plus "" for a step that has not resolved yet, which
//     is a legitimate, intentional member of THIS column's own vocabulary
//     (a step's final classification genuinely does not exist until it
//     resolves) and is why outcome alone keeps its DEFAULT ”.
//
//     outcome_state and outcome_reason are a DIFFERENT claim, corrected
//     2026-08-14 by this step's own review (finding 8): the paragraph
//     that used to stand here asserted "outcome_state/outcome_reason mirror
//     commands.outcome_state/outcome_reason... an unresolved step carries a
//     state and a reason, never a null that renders as blank" as a property
//     of this schema, and nothing enforced it. TEXT NOT NULL DEFAULT ”
//     prevents SQL NULL but permits ”, which renders identically blank —
//     createMacroRun validated every other required per-step field
//     (StepID, ActionObjectID, Integration, SafetyClass,
//     LocalFallbackClass, State) but not these two, so a step created
//     "pending" and never touched by [Store.UpdateMacroRunStepOutcome]
//     (because the process handling its run died first — a coordinator
//     restart mid-run, ADR-031 decision 4) kept outcome_state and
//     outcome_reason at ” forever: the run itself gets finished
//     completed:false by the startup reconciler, but nothing ever touched
//     macro_run_steps, so any step past the point of interruption rendered
//     as a permanently blank row. That is fppcommand_reconcile.go's own
//     original defect (see that file's doc comment: "the UI rendered
//     'Pending: this command has not yet resolved' forever"), reintroduced
//     one table over, one step down.
//
//     Fixed three ways, all in macro_runs.go: (1) createMacroRun now
//     requires OutcomeState and OutcomeReason non-empty at step creation,
//     the same way SafetyClass/LocalFallbackClass already were — see
//     [MacroRunStepOutcomeStatePending]/[MacroRunStepOutcomeReasonPending]
//     for the stated "not yet resolved" values a caller (Wave 1b/2) is
//     expected to create every step with, so "" is never even the
//     as-created value; (2) [Store.ListUnresolvedMacroRunSteps] and
//     [Store.ResolveUnresolvedMacroRunSteps] give the startup reconciler
//     (Wave 2, ADR-031 decision 4) the affordance
//     [api.ReconcileStrandedFPPCommands]'s own "resolve rather than retry"
//     shape needs one level down, at the step rather than the command, so a
//     run finished by the reconciler leaves no step behind still carrying
//     its creation-time placeholder; (3) safety_class/local_fallback_class's
//     DEFAULT ” was ALSO removed from the schema below, for the identical
//     "the Go layer already requires non-empty, so a schema DEFAULT that
//     nothing can reach is a shape that gets copied" reasoning this
//     paragraph itself is proof of (see the safety_class/local_fallback_class
//     paragraph above and STEP-9-SPEC review finding 9). outcome_state and
//     outcome_reason follow the same schema change, below.
//
//     What this migration does NOT do: validate outcome_state against
//     pkg/observation's State vocabulary, or outcome_reason against
//     anything at all — matching every other "not validated by this
//     package" precedent in this file. Presence, not vocabulary, is what
//     was missing and is what is fixed.
//
//   - command_id is nullable TEXT (not TEXT NOT NULL DEFAULT ”, unlike
//     e.g. node_declarations.last_discovery_run_id's empty-string
//     sentinel for "no discovery run yet") because "" is not a real
//     ambiguity risk for a discovery run id (an empty string could never
//     collide with a real one, so the sentinel is safe there), but this
//     column specifically needs to distinguish two genuinely different
//     reasons for having no value: an MQTT step (STEP-9-SPEC.md §7) never
//     dispatches through commands at all and will NEVER have one, versus
//     an FPP step that has not dispatched YET and will get one once it
//     does. NULL states the former; the latter round-trips through NULL
//     too until dispatch, but only ever transitions NULL -> a real id,
//     never NULL -> "" -> a real id, so there is exactly one "no value"
//     representation for both, decoded via macro_runs.go's
//     dbToStringPtr/stringPtrToDB the same *string-means-nullable pattern
//     completed/confirmed use for bool.
//
//     command_id going missing out from under a step is not a bug: commands
//     (schemaV6) is pruned by retention.go's pruneCommands while this
//     table is not individually pruned (it only disappears via CASCADE when
//     its own run is pruned), so a run older than the command retention
//     window can point at a command_id that no longer resolves. Nothing in
//     this migration or in macro_runs.go papers over that with a foreign
//     key: there is deliberately none here, matching node_declarations'
//     and config_revisions' precedent of a bare id column with no
//     REFERENCES clause where the referenced row's own lifecycle is allowed
//     to outlive or be pruned independently of the pointer to it (see
//     schemaV6's doc comment). macro_runs.go's ResolveMacroRunStepCommand
//     is the read-path primitive that turns "command_id is set but GetCommand
//     returns ErrCommandNotFound" into a named, distinguishable state
//     (CommandDetailNotRetained) rather than making a caller reinvent that
//     interpretation, or worse, silently treat a failed lookup the same as
//     command_id having been NULL all along.
//
//   - safety_class and local_fallback_class are TEXT NOT NULL, with NO
//     DEFAULT (corrected 2026-08-14 by this step's own review, finding 9: a
//     caller must supply both non-empty — see createMacroRun's validation —
//     and the only INSERT this package ever issues against this table binds
//     both columns explicitly, so a DEFAULT here was reachable by nothing;
//     it was also the wrong shape to leave in place even though nothing
//     could reach it, because "optional, defaults to the value the Go layer
//     treats as an error" is exactly the copy-pasted-into-the-next-migration
//     risk this package's schema comments exist to head off. This migration
//     itself still does not constrain either column's VOCABULARY, matching
//     integration/state's existing "not validated by this package"
//     precedent below — only presence is enforced at the schema layer now,
//     not the closed-enum membership STEP-9-SPEC.md §5.3/§5.4 define, which
//     stays Wave 2's job.) STEP-9-SPEC.md §2.5, corrected
//     2026-08-14, moved ADR-024 decision 11's audit exemption from a
//     per-RUN property to a per-STEP one: the first draft made a run exempt
//     if any step was, which let a `stopPlaylist` step launder an
//     unattributable `startPlaylist` elsewhere in the same run when the
//     audit store was down. Recording the run's OWN single
//     attribution_degraded column (below) is no longer sufficient once the
//     exemption itself is decided per step: a reader needs to know which
//     step's dispatch was exempt and which safety class earned it, not just
//     that the run as a whole was touched by degraded attribution
//     somewhere. safety_class is the pinned value of the step's action's
//     own required safetyClass field (STEP-9-SPEC.md §5.3: none | blackout
//     | stop | powerOff) at ActionRevision; local_fallback_class is the
//     macro step's own required localFallback.class (§5.4: none |
//     coordinator-required | silence). Both are resolved and pinned once,
//     at CreateMacroRun time, for the identical reason ActionRevision
//     itself is pinned (see macro_runs.macro_revision's paragraph above):
//     an operator editing the action's safety class or the macro's fallback
//     label at 16:58 must not change what a run already in flight reports
//     about itself.
//
//   - This table's own attribution_degraded (INTEGER NOT NULL DEFAULT 0,
//     not nullable, identical reasoning to macro_runs.attribution_degraded
//     above: it is well-defined false from creation, unlike completed/
//     confirmed) is the per-step half of that same correction: flipped by
//     [Store.SetMacroRunStepAttributionDegraded] the moment THIS step
//     specifically dispatched with degraded attribution, independent of
//     macro_runs.attribution_degraded, which the executor (Wave 2) is
//     expected to also set on the run per ADR-031 decision 5's "recorded on
//     the step and raised onto the run": this package stores both facts
//     and computes neither from the other.
const schemaV7 = `
CREATE TABLE macro_runs (
	id                    TEXT PRIMARY KEY,
	macro_object_id       TEXT NOT NULL,
	macro_revision        INTEGER NOT NULL,
	show                  TEXT NOT NULL,
	trigger               TEXT NOT NULL,
	issuer_principal_id   TEXT NOT NULL DEFAULT '',
	issuer_principal_name TEXT NOT NULL DEFAULT '',
	idempotency_key       TEXT NOT NULL UNIQUE,
	created_at            TEXT NOT NULL,
	finished_at           TEXT,
	state                 TEXT NOT NULL,
	completed             INTEGER,
	confirmed             INTEGER,
	reason                TEXT NOT NULL DEFAULT '',
	attribution_degraded  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_macro_runs_created_at ON macro_runs(created_at);
CREATE INDEX idx_macro_runs_macro_object_id_state ON macro_runs(macro_object_id, state);

CREATE TABLE macro_run_steps (
	run_id                TEXT NOT NULL REFERENCES macro_runs(id) ON DELETE CASCADE,
	step_index            INTEGER NOT NULL,
	step_id               TEXT NOT NULL,
	action_object_id      TEXT NOT NULL,
	action_revision       INTEGER NOT NULL,
	integration           TEXT NOT NULL,
	safety_class          TEXT NOT NULL,
	local_fallback_class  TEXT NOT NULL,
	state                 TEXT NOT NULL,
	dispatched_at         TEXT,
	resolved_at           TEXT,
	outcome               TEXT NOT NULL DEFAULT '',
	outcome_state         TEXT NOT NULL,
	outcome_reason        TEXT NOT NULL,
	command_id            TEXT,
	attribution_degraded  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (run_id, step_index)
);
`

// schemaV8 adds Track E's asset store tables (ADR-028): assets holds one
// row of metadata per artifact — never bytes, which live in the pluggable
// backend the assetstore package addresses (decision 4) — and
// node_asset_inventory/node_asset_reports hold what a node actually
// reports it holds. Repository methods live in assets.go and
// node_assets.go, following the *Store/*Tx-form-over-one-querier-body
// pattern every writer since schemaV6 uses (see tx.go's [Tx] doc comment).
// This is a pure-addition migration — nothing from schemaV1 through
// schemaV7 is touched.
//
// assets_identity (ADR-028 decision 1: "identity is show plus logical
// sequence plus target plus content hash") is the permanent, never-pruned
// identity of an artifact — it has no WHERE clause, so re-registering a
// hash already seen under that identity never inserts a second row.
// assets.go's createAsset resolves the hit two ways: still current is the
// idempotent no-op (ErrAssetExists); superseded is ADR-028 decision 10's
// rollback, which un-supersedes that row instead of inserting a new one.
//
// assets_current is the structural half of that same decision: at most one
// row per (show_id, sequence_id, target_kind, target_id) may have
// superseded_at IS NULL, so "two current assets for one target" cannot
// exist even transiently — a query bug or a missed supersede on some
// future write path fails the INSERT instead of silently serving two
// answers. runtime_filename is deliberately outside both indexes and every
// lookup this package performs: ADR-028 decision 1's whole point is that
// xLights gives three different artifacts for three different targets the
// identical filename, so a store keyed on it would resolve one node's
// asset to another node's content.
//
// node_asset_inventory is evidence, not desired state — what a node's own
// agent last reported actually being on its disk, replaced wholesale on
// every sync report (assets.go's ReplaceNodeAssetInventory: delete this
// node's rows, insert the new set, all in one transaction, so a reader
// never observes a half-replaced inventory).
//
// node_asset_reports exists so "we have never heard from this node" (no
// row) is distinguishable from "this node reported holding nothing"
// (a row with zero corresponding node_asset_inventory rows) — the fourth
// time this package has needed a table whose mere absence of a row is
// itself the meaningful, distinct state (see migrations.go's schemaV6
// doc comment on node_declarations for the earlier three). complete is
// INTEGER NOT NULL, unlike macro_runs.completed's deliberately nullable
// pair (schemaV7): an inventory report is never "not yet decided" the way
// an in-flight run's outcome is — the agent either finished a directory
// walk and hashed every file, or it did not, and reason states which, per
// spec §4.2 ("never reports complete: true off a partial walk").
const schemaV8 = `
CREATE TABLE assets (
    id                         TEXT PRIMARY KEY,
    show_id                    TEXT NOT NULL,
    sequence_id                TEXT NOT NULL,
    target_kind                TEXT NOT NULL,   -- 'node' | 'show'
    target_id                  TEXT NOT NULL,   -- node id, or '' when target_kind='show'
    media_type                 TEXT NOT NULL,   -- 'fseq' | 'audio' | 'media'
    content_hash               TEXT NOT NULL,   -- 'sha256:<hex>'
    runtime_filename           TEXT NOT NULL,
    size_bytes                 INTEGER NOT NULL,
    backend                    TEXT NOT NULL,   -- 'volume'
    storage_key                TEXT NOT NULL,
    created_at                 TEXT NOT NULL,
    created_by_principal_id    TEXT NOT NULL,
    created_by_principal_name  TEXT NOT NULL,
    superseded_at              TEXT
);

-- ADR-028 decision 1: identity is show + logical sequence + target + content hash.
CREATE UNIQUE INDEX assets_identity
    ON assets (show_id, sequence_id, target_kind, target_id, content_hash);

-- Exactly one CURRENT asset per (show, sequence, target), enforced structurally
-- rather than by a convention a later query could forget.
CREATE UNIQUE INDEX assets_current
    ON assets (show_id, sequence_id, target_kind, target_id)
    WHERE superseded_at IS NULL;

CREATE INDEX assets_by_target ON assets (target_kind, target_id);

-- What a node reports it actually holds. Evidence, not bookkeeping.
CREATE TABLE node_asset_inventory (
    node_id          TEXT NOT NULL,
    content_hash     TEXT NOT NULL,
    runtime_filename TEXT NOT NULL,
    size_bytes       INTEGER NOT NULL,
    verified_at      TEXT NOT NULL,
    PRIMARY KEY (node_id, content_hash)
);

-- The report ITSELF, so "we have never heard from this node" is distinguishable
-- from "this node holds nothing".
CREATE TABLE node_asset_reports (
    node_id     TEXT PRIMARY KEY,
    reported_at TEXT NOT NULL,
    complete    INTEGER NOT NULL,
    reason      TEXT NOT NULL
);
`

// schemaV9 holds one durable row per audio playback session, mirroring
// the coordinator's own view of desired state (pkg/audio.
// SessionDesiredState) so a coordinator restart can still tell a stale
// command replay from a fresh one without asking the node. The node's own
// agent is a session's actual authority: a running session must survive
// coordinator loss, so this table is the coordinator's durable RECORD of
// what it last told a session to be, not a second engine.
const schemaV9 = `
CREATE TABLE audio_sessions (
    id           TEXT PRIMARY KEY,
    node_id      TEXT NOT NULL,
    desired_json TEXT NOT NULL,
    revision     INTEGER NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE INDEX audio_sessions_by_node ON audio_sessions (node_id);
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

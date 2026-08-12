package store

import (
	"context"
	"fmt"
	"time"
)

// DefaultMaxEventAge and DefaultMaxEventRows are the two event-retention
// bounds applied when [Open] is called with no [WithMaxEventAge]/
// [WithMaxEventRows] option.
//
// SHOWMESH HYPOTHESES, NOT DERIVED FROM ANY MEASUREMENT — labeled here
// exactly as the Step 3 task spec requires, the same way
// internal/coordinator/inventory.StalenessWindow labels its own guess.
// Step 2 deferred event retention entirely, and ADR-009 names it as a
// necessary consequence of storing observed-state history without ever
// pruning it ("needs retention/pruning policy to bound database growth"),
// but nothing has measured what a real show's event volume looks like or
// how much history an operator actually wants to look back through.
// RES-013 (telemetry storage and alerting) owns the real answer; these two
// numbers exist only so an appliance with a small disk does not grow an
// unbounded events table between now and whenever RES-013 lands, and both
// are expected to change once it does.
const (
	// DefaultMaxEventAge is how long an event is kept regardless of row
	// count: 30 days, picked as "long enough to review last weekend's
	// show, short enough that a forgotten coordinator does not fill its
	// disk over a season."
	DefaultMaxEventAge = 30 * 24 * time.Hour

	// DefaultMaxEventRows is how many events are kept regardless of age:
	// 50,000, picked as "large enough that a single busy show night could
	// not plausibly evict a quiet week's worth of history, small enough
	// that pruneEvents's `ORDER BY seq DESC LIMIT ?` subquery stays cheap."
	DefaultMaxEventRows int64 = 50_000
)

// DefaultMaxAuditAge and DefaultMaxAuditRows bound audit_log the same way
// DefaultMaxEventAge/DefaultMaxEventRows bound events, when [Open] is
// called with no [WithMaxAuditAge]/[WithMaxAuditRows] option.
//
// SHOWMESH HYPOTHESES, NOT DERIVED FROM ANY MEASUREMENT — labeled exactly
// as DefaultMaxEventAge/DefaultMaxEventRows already are above, for the
// identical reason: nothing has measured a real season's write-attempt
// volume or how far back an operator investigating an incident actually
// needs to look. Unlike event retention, though, ADR-024 decision 11 does
// NOT leave this to RES-013: "audit retention is bounded before the first
// write endpoint ships... because an unbounded table that gates commands
// is a scheduled outage." That is why this bound exists at all in Step 6
// rather than being deferred like event retention's tiers were — it is
// not a claim that these particular numbers are right, only that some
// finite bound must exist before decision 11's "a write that cannot be
// attributed does not proceed" rule can be enforced safely.
//
// The two values differ from the event defaults on purpose. 180 days
// (double DefaultMaxEventAge's 30) because an audit trail is read
// retrospectively far less often but is exactly the record an operator
// reaches for when something is disputed months later — "who changed the
// schedule before the November show" — where an event history is read
// day-to-day. 200,000 rows (four times DefaultMaxEventRows) because every
// write attempt produces at least a dispatch entry and often a correlated
// outcome or a replay too (ADR-024 decision 11), so the write volume
// behind one row of "the operator did something" is structurally higher
// than one event.
const (
	DefaultMaxAuditAge        = 180 * 24 * time.Hour
	DefaultMaxAuditRows int64 = 200_000
)

// DefaultMaxCommandAge and DefaultMaxCommandRows bound the commands table
// (schemaV6, Step 7 seam 0), and DefaultMaxDiscoveryRunAge/
// DefaultMaxDiscoveryRunRows bound discovery_runs, the identical pattern
// applied to a second table that also grows only by insert.
//
// SHOWMESH HYPOTHESES, NOT DERIVED FROM ANY MEASUREMENT — labeled exactly
// as every other retention bound in this file is, for the identical
// reason: nothing has measured a real season's command-issue rate or
// discovery-run frequency. RES-013 owns the real answer. These exist now,
// rather than being deferred, for the same reason [DefaultMaxAuditAge]
// is: ADR-024 decision 11 names disk exhaustion as a real trigger rather
// than a theoretical one, and an unbounded table that a fail-closed write
// rule depends on being writable is a scheduled outage. commands shares
// audit_log's 180-day/200,000-row shape (a command is exactly the kind of
// record an operator investigating a dispute months later wants intact);
// discovery_runs is bounded far smaller — 90 days / 5,000 rows — because a
// discovery run is triggered by an operator action, not a per-request
// write, so its volume is structurally lower by orders of magnitude, and
// there is no config_revisions-style rollback requirement pinning any of
// its history in place (see migrations.go's schemaV6 doc comment: unlike
// config_revisions and node_declarations, discovery_runs IS pruned).
const (
	DefaultMaxCommandAge        = 180 * 24 * time.Hour
	DefaultMaxCommandRows int64 = 200_000

	DefaultMaxDiscoveryRunAge        = 90 * 24 * time.Hour
	DefaultMaxDiscoveryRunRows int64 = 5_000
)

// storeConfig holds every [Option]'s target. It exists only inside Open —
// the resolved values it produces live on *Store (maxEventAge,
// maxEventRows) — so a caller never sees storeConfig itself.
type storeConfig struct {
	maxEventAge  time.Duration
	maxEventRows int64

	// maxAuditAge and maxAuditRows are audit.go's equivalent bounds for
	// audit_log, following the identical pattern for the identical reason
	// (see DefaultMaxAuditAge/DefaultMaxAuditRows in audit.go); kept in
	// this same storeConfig/Option machinery rather than a parallel one so
	// [Open]'s call sites gain audit retention control the same way they
	// already gained event retention control.
	maxAuditAge  time.Duration
	maxAuditRows int64

	// maxCommandAge/maxCommandRows and maxDiscoveryRunAge/maxDiscoveryRunRows
	// are commands.go's and discovery.go's equivalent bounds, following the
	// identical pattern for the identical reason (see
	// DefaultMaxCommandAge/DefaultMaxDiscoveryRunAge above).
	maxCommandAge       time.Duration
	maxCommandRows      int64
	maxDiscoveryRunAge  time.Duration
	maxDiscoveryRunRows int64

	// clock overrides [Open]'s hardcoded time.Now, when set by [WithClock].
	// nil (the default) leaves Open's existing time.Now behavior
	// unchanged for every pre-Step-6 call site.
	clock func() time.Time
}

func defaultConfig() storeConfig {
	return storeConfig{
		maxEventAge:         DefaultMaxEventAge,
		maxEventRows:        DefaultMaxEventRows,
		maxAuditAge:         DefaultMaxAuditAge,
		maxAuditRows:        DefaultMaxAuditRows,
		maxCommandAge:       DefaultMaxCommandAge,
		maxCommandRows:      DefaultMaxCommandRows,
		maxDiscoveryRunAge:  DefaultMaxDiscoveryRunAge,
		maxDiscoveryRunRows: DefaultMaxDiscoveryRunRows,
	}
}

// Option adjusts a [Store]'s optional configuration at [Open]. The only
// options today bound event retention; see [WithMaxEventAge] and
// [WithMaxEventRows]. Modeled as a functional option (matching
// pkg/observation's Option for the identical reason) rather than a config
// struct parameter, so a future option never requires every existing Open
// call site to change.
type Option func(*storeConfig)

// WithMaxEventAge overrides [DefaultMaxEventAge]. A non-positive d disables
// the age bound entirely (only [WithMaxEventRows]'s row-count bound
// applies) — this is deliberately allowed, unlike a non-positive
// [WithMaxEventRows], because "keep everything, forever, regardless of
// age" is a coherent operator choice for a deployment with disk to spare,
// while a store with no row-count ceiling at all has no bound on disk
// growth left, which no deployment should choose by accident.
func WithMaxEventAge(d time.Duration) Option {
	return func(c *storeConfig) { c.maxEventAge = d }
}

// WithMaxEventRows overrides [DefaultMaxEventRows]. n must be positive;
// a non-positive n is ignored (the default is kept) rather than disabling
// the row-count bound, since an events table with neither bound would grow
// without limit — see [WithMaxEventAge]'s doc comment for why the age
// bound alone is allowed to be disabled but this one is not.
func WithMaxEventRows(n int64) Option {
	return func(c *storeConfig) {
		if n > 0 {
			c.maxEventRows = n
		}
	}
}

// WithMaxAuditAge and WithMaxAuditRows override [DefaultMaxAuditAge] and
// [DefaultMaxAuditRows] respectively, mirroring [WithMaxEventAge] and
// [WithMaxEventRows] exactly, including the asymmetry (a non-positive
// WithMaxAuditAge disables the age bound; a non-positive WithMaxAuditRows
// is ignored) and its reasoning — see those two doc comments.
func WithMaxAuditAge(d time.Duration) Option {
	return func(c *storeConfig) { c.maxAuditAge = d }
}

func WithMaxAuditRows(n int64) Option {
	return func(c *storeConfig) {
		if n > 0 {
			c.maxAuditRows = n
		}
	}
}

// WithMaxCommandAge and WithMaxCommandRows override [DefaultMaxCommandAge]/
// [DefaultMaxCommandRows]; WithMaxDiscoveryRunAge and
// WithMaxDiscoveryRunRows override [DefaultMaxDiscoveryRunAge]/
// [DefaultMaxDiscoveryRunRows]. All four mirror [WithMaxAuditAge]/
// [WithMaxAuditRows] exactly, including the asymmetry (a non-positive age
// disables that bound; a non-positive row count is ignored) — see those
// two doc comments.
func WithMaxCommandAge(d time.Duration) Option {
	return func(c *storeConfig) { c.maxCommandAge = d }
}

func WithMaxCommandRows(n int64) Option {
	return func(c *storeConfig) {
		if n > 0 {
			c.maxCommandRows = n
		}
	}
}

func WithMaxDiscoveryRunAge(d time.Duration) Option {
	return func(c *storeConfig) { c.maxDiscoveryRunAge = d }
}

func WithMaxDiscoveryRunRows(n int64) Option {
	return func(c *storeConfig) {
		if n > 0 {
			c.maxDiscoveryRunRows = n
		}
	}
}

// WithClock overrides the clock [Open] uses for this package's own
// bookkeeping timestamps (see [Store.now]'s doc comment for exactly which
// columns that is and, just as importantly, which it is not — evidence
// timestamps a caller supplies are never affected by this option). Every
// pre-Step-6 call site left this unset and keeps using real time.Now
// unchanged; this exists because the identity package (Step 6) needs a
// single injected clock shared between its own domain logic and the rows
// this package stamps on its behalf (principals.created_at,
// principal_sessions.created_at/last_used_at, audit_log.recorded_at, and
// so on), so a test can advance one fake clock and see both move together,
// the same way store's own test suite already does internally via the
// unexported open(...) helper's explicit now parameter — WithClock is
// that same seam, made reachable through the public [Open] constructor
// for the first time because identity is the first caller outside this
// package that needs it.
func WithClock(now func() time.Time) Option {
	return func(c *storeConfig) { c.clock = now }
}

// pruneEveryNEvents is how many [Store.AppendEvent] calls elapse between
// pruning passes triggered by insert volume. A SHOWMESH HYPOTHESIS, not a
// measured value: it only trades off two costs neither of which has been
// measured against a real show's event rate — pruning on every insert is
// write amplification (two extra DELETE scans per event, per the Step 3
// task spec's explicit "do not prune on every insert" instruction), while
// pruning too rarely lets the table overshoot maxEventRows by up to this
// many rows between passes. 100 was chosen only as "clearly more than 1,
// clearly less than DefaultMaxEventRows" — RES-013 owns tuning this for
// real, if amortized on-insert pruning is even the design that survives
// contact with a real show (see AppendEvent's doc comment in events.go for
// the alternative this rejected).
//
// This alone bounds row-count growth correctly (maxEventRows can only grow
// via an insert, so checking every Nth insert is checking often enough),
// but it does NOT, on its own, bound event AGE correctly: an events table
// that receives only a few inserts a week could go years between
// pruneEveryNEvents-triggered passes, during which every event more than
// DefaultMaxEventAge old just sits there — the age bound is a promise about
// wall-clock time, and nothing here was watching wall-clock time. See
// [pruneCheckInterval] for the second, independent trigger that closes
// that gap (Step 3 review finding 3.5).
const pruneEveryNEvents = 100

// pruneCheckInterval is AppendEvent's second, independent prune trigger,
// alongside pruneEveryNEvents: a prune pass also runs whenever at least
// this much wall-clock time has passed since one last ran in this process
// (or none has run yet this process — see [Store.lastPruneAtNanos]),
// regardless of how many events have been appended in between. A
// SHOWMESH HYPOTHESIS, chosen only as "short enough that a coordinator
// appending a handful of events a week still enforces DefaultMaxEventAge
// on a human timescale instead of a multi-year one." Because pruning stays
// coupled to a write (see AppendEvent's own doc comment for why there is
// deliberately no separate background goroutine), this trigger cannot
// prune anything on its own between inserts — it only changes what the
// VERY NEXT insert, whenever it happens, is checked against: a coordinator
// that receives literally no events for a year still has nothing prune
// until something is next appended, which is correct, since nothing new
// exists to bound. What this fixes is the case Step 3 review finding 3.5
// found: a coordinator that DOES keep receiving occasional events, just
// too infrequently for pruneEveryNEvents to ever fire, previously never
// re-checked the age bound at all.
const pruneCheckInterval = 1 * time.Hour

// pruneEveryNCommands and pruneEveryNDiscoveryRuns mirror
// [pruneEveryNEvents]/[pruneEveryNAuditEntries] applied to commands and
// discovery_runs respectively; see those constants' doc comments for the
// tradeoff they encode. Kept identical to the events/audit value
// deliberately, for the identical reason: neither number is derived from
// a measured write rate.
//
// (F7 review finding: this const block used to sit between pruneEvents's
// own doc comment and its declaration below, which made godoc attach
// three paragraphs about EVENT pruning to these two integer constants
// instead — every statement in that misplaced comment was false when read
// as documentation for pruneEveryNCommands/pruneEveryNDiscoveryRuns, and
// pruneEvents itself had no documentation at all. Moved here, next to
// pruneCheckInterval and ahead of pruneCommands/pruneDiscoveryRuns, which
// actually use these two constants.)
const (
	pruneEveryNCommands      = 100
	pruneEveryNDiscoveryRuns = 100
)

// pruneCommands deletes command rows older than maxCommandAge (if
// positive) and, of whatever remains, all but the newest maxCommandRows —
// see [pruneAudit]'s identical shape and doc comment in audit.go, applied
// here to the commands table via the same querier abstraction so it runs
// correctly whether q is *Store's own owned transaction or an already-open
// [Tx]'s *sql.Tx.
func (s *Store) pruneCommands(ctx context.Context, q querier) error {
	if s.maxCommandAge > 0 {
		cutoff := timeToDB(s.now().Add(-s.maxCommandAge))
		if _, err := q.ExecContext(ctx, `DELETE FROM commands WHERE created_at < ?`, cutoff); err != nil {
			return fmt.Errorf("store: prune commands by age: %w", err)
		}
	}
	if s.maxCommandRows > 0 {
		if _, err := q.ExecContext(ctx, `
			DELETE FROM commands WHERE id NOT IN (
				SELECT id FROM commands ORDER BY created_at DESC LIMIT ?
			)
		`, s.maxCommandRows); err != nil {
			return fmt.Errorf("store: prune commands by row count: %w", err)
		}
	}
	return nil
}

// pruneDiscoveryRuns is [pruneCommands]'s identical shape applied to
// discovery_runs — see migrations.go's schemaV6 doc comment for why this
// table, unlike config_revisions and node_declarations, IS pruned.
//
// F10 caveat: node_declarations.last_discovery_run_id (nodes_declared.go)
// points at a discovery_runs row by id, with no foreign key between them
// (matching node_declarations' own deliberate absence of a FK to nodes —
// see migrations.go's schemaV6 doc comment). This method can therefore
// orphan that pointer: a declaration's last_discovery_run_id can name a
// run this method has already deleted. That is not itself a bug — it is
// the same "absence of evidence is not evidence of absence" shape this
// codebase has already recorded three times over (schemaV3's ObservedAt
// nullability, schemaV2's LWT freshness fix, node_declarations' own FK
// absence) — but it means a reader resolving that pointer must be able to
// fail: [Store.GetDiscoveryRun] already returns [ErrDiscoveryRunNotFound]
// for exactly this case. A caller rendering a declaration whose
// last_discovery_run_id no longer resolves MUST render that evidence as
// `unknown` with a reason, per ADR-020's absent-evidence rule, never as
// blank or as if the discovery run had simply never happened — seam B, the
// owner of node_declarations' read surface, is where that rule needs to be
// honored; this method only needs to not lie about what it deleted.
func (s *Store) pruneDiscoveryRuns(ctx context.Context, q querier) error {
	if s.maxDiscoveryRunAge > 0 {
		cutoff := timeToDB(s.now().Add(-s.maxDiscoveryRunAge))
		if _, err := q.ExecContext(ctx, `DELETE FROM discovery_runs WHERE started_at < ?`, cutoff); err != nil {
			return fmt.Errorf("store: prune discovery runs by age: %w", err)
		}
	}
	if s.maxDiscoveryRunRows > 0 {
		if _, err := q.ExecContext(ctx, `
			DELETE FROM discovery_runs WHERE id NOT IN (
				SELECT id FROM discovery_runs ORDER BY started_at DESC LIMIT ?
			)
		`, s.maxDiscoveryRunRows); err != nil {
			return fmt.Errorf("store: prune discovery runs by row count: %w", err)
		}
	}
	return nil
}

// pruneEvents deletes events older than maxEventAge (if positive) and, of
// whatever remains, all but the newest maxEventRows (see [WithMaxEventAge]/
// [WithMaxEventRows] for why the two bounds are not symmetric). It is
// always called from inside the same transaction as the AppendEvent write
// that triggered it — either [Store.AppendEvent]'s own owned transaction,
// or an already-open [Tx]'s (see [Tx.AppendEvent]) — via the identical
// [querier] abstraction every other writer in this package shares, so a
// caller either observes both the new event and its consequent pruning,
// or (on any error) neither, never a partial state where an event was
// appended but a failed prune silently never ran.
//
// This is the one place ListEvents's `gap` return value (events.go)
// becomes possible: pruneEvents deleting a still-unread event is the exact
// condition eventsGapBefore detects and reports, rather than the two
// pretending to a caller mid-page that nothing happened in the range that
// was actually deleted out from under it.
func (s *Store) pruneEvents(ctx context.Context, q querier) error {
	if s.maxEventAge > 0 {
		cutoff := timeToDB(s.now().Add(-s.maxEventAge))
		if _, err := q.ExecContext(ctx, `DELETE FROM events WHERE recorded_at < ?`, cutoff); err != nil {
			return fmt.Errorf("store: prune events by age: %w", err)
		}
	}
	if s.maxEventRows > 0 {
		if _, err := q.ExecContext(ctx, `
			DELETE FROM events WHERE seq NOT IN (
				SELECT seq FROM events ORDER BY seq DESC LIMIT ?
			)
		`, s.maxEventRows); err != nil {
			return fmt.Errorf("store: prune events by row count: %w", err)
		}
	}
	return nil
}

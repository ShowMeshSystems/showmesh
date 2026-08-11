package store

import (
	"context"
	"database/sql"
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

// storeConfig holds every [Option]'s target. It exists only inside Open —
// the resolved values it produces live on *Store (maxEventAge,
// maxEventRows) — so a caller never sees storeConfig itself.
type storeConfig struct {
	maxEventAge  time.Duration
	maxEventRows int64
}

func defaultConfig() storeConfig {
	return storeConfig{
		maxEventAge:  DefaultMaxEventAge,
		maxEventRows: DefaultMaxEventRows,
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

// pruneEvents deletes events older than maxEventAge (if positive) and, of
// whatever remains, all but the newest maxEventRows (see [WithMaxEventAge]/
// [WithMaxEventRows] for why the two bounds are not symmetric). It is
// always called from inside the same transaction as the [Store.AppendEvent]
// write that triggered it — see that method — so a caller either observes
// both the new event and its consequent pruning, or (on any error) neither,
// never a partial state where an event was appended but a failed prune
// silently never ran.
//
// This is the one place ListEvents's `gap` return value (events.go)
// becomes possible: pruneEvents deleting a still-unread event is the exact
// condition eventsGapBefore detects and reports, rather than the two
// pretending to a caller mid-page that nothing happened in the range that
// was actually deleted out from under it.
func (s *Store) pruneEvents(ctx context.Context, tx *sql.Tx) error {
	if s.maxEventAge > 0 {
		cutoff := timeToDB(s.now().Add(-s.maxEventAge))
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE recorded_at < ?`, cutoff); err != nil {
			return fmt.Errorf("store: prune events by age: %w", err)
		}
	}
	if s.maxEventRows > 0 {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM events WHERE seq NOT IN (
				SELECT seq FROM events ORDER BY seq DESC LIMIT ?
			)
		`, s.maxEventRows); err != nil {
			return fmt.Errorf("store: prune events by row count: %w", err)
		}
	}
	return nil
}

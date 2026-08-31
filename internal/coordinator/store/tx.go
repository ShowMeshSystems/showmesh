package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This file is Step 7 seam 0's answer to ADR-024 decision 11's
// same-transaction rule: "for a coordinator-local state change the audit
// entry is written in the same transaction, so the two succeed or fail
// together." Before this file existed, every repository method opened and
// committed its own transaction, so a caller composing a state change with
// its audit entry (identity.Service.AuditedWrite, and every v6 writer this
// seam adds) had no way to make the two land atomically — see ADR-024's
// "What implementation and research proved this record got wrong", last
// bullet.

// querier is the subset of *sql.DB and *sql.Tx that every repository
// method not itself managing a transaction boundary is written against, so
// its SQL is defined exactly once and both [Store] and [Tx] run it as thin
// wrappers over the identical statement — never two hand-copied INSERTs
// that can silently stop agreeing (this project has already found a bug
// that shape produced once; see CLAUDE.md's Step 5 lesson). Both *sql.DB
// and *sql.Tx already implement this — it is a subset of database/sql's
// own method set on each — so no adapter type is needed on either side.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Tx is a handle bound to one *sql.Tx, obtained only from inside a
// [Store.InTx] closure. Every method on Tx runs the identical SQL its
// *Store sibling does (see e.g. [Tx.AppendAuditEntry] next to
// [Store.AppendAuditEntry]) against the transaction's own *sql.Tx instead
// of the Store's shared *sql.DB connection, so a caller can compose several
// writes — a state change and its audit entry — that commit or roll back
// together.
//
// Tx carries a reference back to its parent [Store] for the bookkeeping
// every method already depends on (s.now, the audit-retention counters,
// s.logger): nothing here duplicates that state, and a Tx is worthless
// once the [Store.InTx] call that produced it has returned (its underlying
// *sql.Tx is already committed or rolled back by then). Nothing outside
// this package can CONSTRUCT one — the unexported fields see to that — but
// a caller-supplied fn (see [Store.InTx]) receives a live *Tx and is free
// to stash it somewhere that outlives the call: [identity.Service.AuditedWrite]
// hands its fn a *store.Tx, and fn is arbitrary caller code in a different
// package. That escape is real, unlike an earlier version of this comment
// claimed ("not reachable from any other package") — it is merely benign:
// every method on a Tx used after its transaction has committed or rolled
// back fails with "sql: transaction has already been committed or rolled
// back" (database/sql's own guard on the underlying *sql.Tx), never silent
// or undefined behavior.
type Tx struct {
	tx *sql.Tx
	s  *Store
}

// inTxMarkerKey is the unexported context key [Store.InTx] stamps onto the
// context it passes to its closure, and [guardNotInTx] checks for.
// Unexported so no other package can construct a colliding key or forge
// the marker from outside this package.
type inTxMarkerKey struct{}

// InTx opens one transaction, runs fn with a [Tx] bound to it and a
// context carrying the in-transaction marker [guardNotInTx] checks,
// commits on a nil return from fn, and rolls back on any error fn returns
// OR a panic inside fn (the deferred rollback below runs before a panic
// continues propagating up the call stack; Commit is only ever reached
// after fn returns normally with a nil error).
//
// This is what makes ADR-024 decision 11's same-transaction rule real
// rather than aspirational: identity.Service.AuditedWrite (Step 7 seam 0)
// is its first caller, closing the specific defect ADR-024 names — "an
// audit failure on a bootstrap claim leaves the first administrator
// existing with no record of its creation" — and every later seam that
// needs a coordinator-local write and its audit entry to land atomically
// calls InTx the same way, directly or through AuditedWrite.
//
// The rule every fn must follow, for every seam that will ever call this:
// no slow work and no network I/O inside fn. This package's connection
// pool is capped at exactly one connection (store.go's open()), so fn runs
// with that single connection checked out for its entire duration — every
// OTHER goroutine wanting the database, including this same goroutine
// calling back into any *Store method (see [guardNotInTx]), blocks for as
// long as fn takes to return. A dispatch to an agent over MQTT, a call to
// an FPP host, or any operation whose latency this package does not
// control has no business running inside fn: it would turn a slow remote
// peer into a stall on every OTHER database access this coordinator is
// trying to make, which is a substantially worse failure than the slow
// peer's own timeout. This is also why a command dispatched outward uses
// decision 11's write-before-dispatch rule (insert the row, commit, THEN
// dispatch) instead of InTx: the dispatch itself must never be inside one.
//
// InTx is itself guarded by [guardNotInTx] — see that function's doc
// comment for why calling it from inside an already-open InTx closure is
// the one case this package cannot let error its way out: a caller
// composing two coordinator-local writes must open exactly one
// transaction and pass its own [Tx] to both writers (or their [Tx] forms)
// directly, never nest a second InTx call to "reuse" the first one's
// connection.
func (s *Store) InTx(ctx context.Context, fn func(context.Context, *Tx) error) (err error) {
	guardNotInTx(ctx, "Store.InTx")

	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = sqlTx.Rollback() // no-op once Commit has succeeded; also fires on a panic in fn
		}
	}()

	txCtx := context.WithValue(ctx, inTxMarkerKey{}, true)
	tx := &Tx{tx: sqlTx, s: s}

	if ferr := fn(txCtx, tx); ferr != nil {
		return ferr
	}
	if cerr := sqlTx.Commit(); cerr != nil {
		return fmt.Errorf("%w: %v", ErrCommitFailed, cerr)
	}
	committed = true
	return nil
}

// ErrCommitFailed wraps a failure of the COMMIT statement itself, after fn
// already returned nil (so every write fn made was accepted by the
// database up to that point). This is a distinct failure mode from an
// error fn returns directly: fn's own error means fn decided not to
// proceed, or hit a problem making one specific write; ErrCommitFailed
// means every write fn made was individually fine and the failure is in
// making them durable together, atomically, e.g. a disk that fills
// between the last write and the fsync COMMIT performs. A caller that
// treats "the append inside fn failed" and "the whole transaction could
// not be made durable at commit" as the same class of event (ADR-024
// decision 11: neither can be attributed) checks for this with errors.Is,
// the same way it already checks for a sentinel fn itself might return.
var ErrCommitFailed = errors.New("store: commit failed")

// guardNotInTx panics if ctx carries the in-transaction marker [Store.InTx]
// stamps. See store.go's [Store.open] doc comment: db.SetMaxOpenConns(1)
// caps this package's connection pool at exactly one connection, which is
// what RecordHealth's read-then-conditionally-write already depends on to
// be race-free. That same cap means a *Store method called with a context
// from inside an already-open InTx closure does not error — it BLOCKS
// FOREVER waiting for the one connection the open transaction is already
// holding, which presents as the coordinator wedging with no log line
// pointing at the cause, not as a bug report anyone could file. This makes
// that mistake loud instead: every exported *Store method that touches the
// database calls this first — including [Store.InTx] itself, against a
// second, NESTED InTx call, which would deadlock on the identical single
// connection exactly like any other guarded method would; a caller
// composing two coordinator-local writes must open exactly one
// transaction and pass its own [Tx] to both, never call InTx a second time
// to "reuse" the first one's connection. [Store.Readiness] is this
// package's one deliberate exception to being GUARDED: it takes no context
// at all (see its own doc comment), so guardNotInTx has nothing to check it
// against. That does NOT mean it cannot be called from inside an InTx
// closure — it takes no ctx, so nothing stops a closure from calling it
// directly, and this is a known, pre-existing issue this package has not
// fixed. What actually happens: Readiness builds its own
// context.Background() and pings with a two-second deadline, and the
// transaction is holding this package's single connection (open()'s
// db.SetMaxOpenConns(1)) for the closure's entire duration, so the ping
// blocks until that deadline expires and Readiness reports
// Ready: false with a Reason beginning "sqlite store is not reachable" —
// indistinguishable from the store actually being unreachable. That is an
// ADR-011 falsehood in its own right (busy is not unreachable), not merely
// a missing guard.
//
// method names the exact method that panicked and the equivalent [Tx]
// method to use instead, so the panic message alone is enough to fix the
// call site — matching the precedent the FPP collector already set (it
// panics if anything ever calls the settings endpoint): this is a
// programming error, not a runtime condition a caller could sensibly
// recover from.
func guardNotInTx(ctx context.Context, method string) {
	if v, _ := ctx.Value(inTxMarkerKey{}).(bool); v {
		hint := "use the equivalent (*Tx) method instead"
		if method == "Store.InTx" {
			hint = "compose your write using the *Tx already passed to your fn instead of opening a second, nested transaction"
		}
		panic(fmt.Sprintf(
			"store: %s called with a context derived from inside Store.InTx; this package's connection pool "+
				"is capped at 1 (see store.go's open()), so this call would block forever waiting for the "+
				"connection the open transaction already holds — %s",
			method, hint))
	}
}

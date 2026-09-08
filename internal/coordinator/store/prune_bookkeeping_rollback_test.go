package store

import (
	"context"
	"errors"
	"testing"
)

// This file proves the central negative claim behind this task's fix: the
// prune-trigger bookkeeping each append/insert path maintains (an
// in-memory counter and a last-prune timestamp, one pair per table, see
// store.go) must stay exactly where it was when the transaction that
// produced a write rolls back, whether that rollback is the caller's own
// choice (fn returns an error) or a failure of COMMIT itself after every
// write inside fn already succeeded (identity.AuditedWrite's confirmed
// [ErrCommitFailed] case). Before this fix, every one of the five methods
// below mutated its counter and timestamp at append/insert time, inside
// the transaction but not as part of it (a process-wide atomic, not a SQL
// write), so a rollback undid the row and any prune it triggered while
// leaving the bookkeeping advanced, consuming a prune trigger for work
// that never happened.

var errSentinelRollback = errors.New("prune_bookkeeping_rollback_test: deliberate rollback")

// TestTxAppendAuditEntryLeavesCountersUnmovedOnCallerRollback covers the
// audit table's caller-rollback shape: a caller composing an audit entry
// with another write inside one [Store.InTx] closure decides not to
// commit.
func TestTxAppendAuditEntryLeavesCountersUnmovedOnCallerRollback(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "x"}); err != nil {
			return err
		}
		return errSentinelRollback
	})
	if !errors.Is(err, errSentinelRollback) {
		t.Fatalf("InTx error = %v, want it to wrap the sentinel", err)
	}

	if got := st.auditAppendCount.Load(); got != 0 {
		t.Errorf("auditAppendCount = %d, want 0: a rolled-back transaction must not advance the prune-trigger counter", got)
	}
	if got := st.lastAuditPruneAtNanos.Load(); got != 0 {
		t.Errorf("lastAuditPruneAtNanos = %d, want 0", got)
	}
	entries, err := st.ListAuditEntries(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("audit entries = %+v, want none (the transaction that appended it rolled back)", entries)
	}
}

// TestTxAppendAuditEntryLeavesCountersUnmovedOnRealCommitFailure is the
// exact scenario this task's spec confirms by direct read:
// identity.AuditedWrite and identity.WriteAudit call appendAuditEntry
// before their enclosing transaction commits, so an [ErrCommitFailed]
// leaves the append (and any prune it triggered) undone. Reproduced with a
// REAL SQLite commit failure, matching [TestInTxCommitFailureIsWrappedInErrCommitFailed]'s
// technique in tx_test.go: PRAGMA defer_foreign_keys=ON defers this
// transaction's FK enforcement to COMMIT, so an audit append that succeeds
// inside fn is followed by an insert that only fails at COMMIT: every
// write in fn was individually fine, and the transaction still could not
// be made durable, which is precisely ErrCommitFailed's contract.
func TestTxAppendAuditEntryLeavesCountersUnmovedOnRealCommitFailure(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
			return err
		}
		if _, err := tx.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "x"}); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx,
			"INSERT INTO node_lwt (node_id, online, provenance, updated_at) VALUES (?, ?, ?, ?)",
			"no-such-node", 1, "test", "2026-01-01T00:00:00Z",
		); err != nil {
			t.Fatalf("INSERT into node_lwt failed immediately (want it deferred to COMMIT): %v", err)
		}
		return nil
	})
	if !errors.Is(err, ErrCommitFailed) {
		t.Fatalf("InTx error = %v, want it to wrap ErrCommitFailed", err)
	}

	if got := st.auditAppendCount.Load(); got != 0 {
		t.Errorf("auditAppendCount = %d, want 0 after a failed COMMIT: the append inside fn succeeded but was undone with everything else", got)
	}
	if got := st.lastAuditPruneAtNanos.Load(); got != 0 {
		t.Errorf("lastAuditPruneAtNanos = %d, want 0", got)
	}
	entries, err := st.ListAuditEntries(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("audit entries = %+v, want none (COMMIT failed)", entries)
	}
}

// TestTxAppendEventLeavesCountersUnmovedOnCallerRollback is events'
// counterpart to the audit test above: [TestTxAppendEventRollsBackWithTheOuterTransaction]
// (events_test.go) already proves the row itself does not survive a
// rollback; this proves the prune-trigger bookkeeping does not either.
func TestTxAppendEventLeavesCountersUnmovedOnCallerRollback(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.AppendEvent(ctx, mustEvent(t, nil)); err != nil {
			return err
		}
		return errSentinelRollback
	})
	if !errors.Is(err, errSentinelRollback) {
		t.Fatalf("InTx error = %v, want it to wrap the sentinel", err)
	}

	if got := st.eventAppendCount.Load(); got != 0 {
		t.Errorf("eventAppendCount = %d, want 0: a rolled-back transaction must not advance the prune-trigger counter", got)
	}
	if got := st.lastPruneAtNanos.Load(); got != 0 {
		t.Errorf("lastPruneAtNanos = %d, want 0", got)
	}
	events, _, err := st.ListEvents(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("events = %+v, want none (the transaction that appended it rolled back)", events)
	}
}

// TestTxInsertCommandLeavesCountersUnmovedOnCallerRollback is commands'
// counterpart.
func TestTxInsertCommandLeavesCountersUnmovedOnCallerRollback(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.InsertCommand(ctx, CommandRecord{
			ID: "cmd-rollback", IdempotencyKey: "idem-rollback", Action: "stop_playlist", State: "dispatched",
		}); err != nil {
			return err
		}
		return errSentinelRollback
	})
	if !errors.Is(err, errSentinelRollback) {
		t.Fatalf("InTx error = %v, want it to wrap the sentinel", err)
	}

	if got := st.commandInsertCount.Load(); got != 0 {
		t.Errorf("commandInsertCount = %d, want 0: a rolled-back transaction must not advance the prune-trigger counter", got)
	}
	if got := st.lastCommandPruneAtNanos.Load(); got != 0 {
		t.Errorf("lastCommandPruneAtNanos = %d, want 0", got)
	}
	if _, err := st.GetCommand(ctx, "cmd-rollback"); !errors.Is(err, ErrCommandNotFound) {
		t.Errorf("GetCommand error = %v, want ErrCommandNotFound (the transaction that inserted it rolled back)", err)
	}
}

// TestTxStartDiscoveryRunLeavesCountersUnmovedOnCallerRollback is
// discovery_runs' counterpart.
func TestTxStartDiscoveryRunLeavesCountersUnmovedOnCallerRollback(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: "run-rollback"}); err != nil {
			return err
		}
		return errSentinelRollback
	})
	if !errors.Is(err, errSentinelRollback) {
		t.Fatalf("InTx error = %v, want it to wrap the sentinel", err)
	}

	if got := st.discoveryRunInsertCount.Load(); got != 0 {
		t.Errorf("discoveryRunInsertCount = %d, want 0: a rolled-back transaction must not advance the prune-trigger counter", got)
	}
	if got := st.lastDiscoveryRunPruneAtNanos.Load(); got != 0 {
		t.Errorf("lastDiscoveryRunPruneAtNanos = %d, want 0", got)
	}
	if _, err := st.GetDiscoveryRun(ctx, "run-rollback"); !errors.Is(err, ErrDiscoveryRunNotFound) {
		t.Errorf("GetDiscoveryRun error = %v, want ErrDiscoveryRunNotFound (the transaction that started it rolled back)", err)
	}
}

// TestTxCreateMacroRunLeavesCountersUnmovedOnCallerRollback is
// macro_runs' counterpart.
func TestTxCreateMacroRunLeavesCountersUnmovedOnCallerRollback(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	run, steps := testMacroRun("run-rollback", "idem-rollback", "macro-a")
	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, _, err := tx.CreateMacroRun(ctx, run, steps); err != nil {
			return err
		}
		return errSentinelRollback
	})
	if !errors.Is(err, errSentinelRollback) {
		t.Fatalf("InTx error = %v, want it to wrap the sentinel", err)
	}

	if got := st.macroRunInsertCount.Load(); got != 0 {
		t.Errorf("macroRunInsertCount = %d, want 0: a rolled-back transaction must not advance the prune-trigger counter", got)
	}
	if got := st.lastMacroRunPruneAtNanos.Load(); got != 0 {
		t.Errorf("lastMacroRunPruneAtNanos = %d, want 0", got)
	}
	if _, _, err := st.GetMacroRun(ctx, "run-rollback"); !errors.Is(err, ErrMacroRunNotFound) {
		t.Errorf("GetMacroRun error = %v, want ErrMacroRunNotFound (the transaction that created it rolled back)", err)
	}
}

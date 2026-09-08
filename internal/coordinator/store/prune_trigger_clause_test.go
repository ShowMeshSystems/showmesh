package store

import (
	"context"
	"testing"
)

// This file closes a gap the existing "ByRowCount" tests for commands,
// discovery_runs, and macro_runs (commands_test.go, discovery_test.go,
// macro_runs_test.go) leave open: each of those advances a fake clock by
// 2 hours between every insert, which is itself enough to cross
// pruneCheckInterval (1 hour) and fire the AGE-based trigger on every
// insert after the first, so none of them actually exercise the
// COUNT-based trigger (byCount in appendAuditEntry/appendEvent/
// insertCommand/startDiscoveryRun/createMacroRun) in isolation; they only
// prove the prune query itself enforces the row bound once SOMETHING
// triggers it. [TestPruneEventsEnforcesRowCountBound] (events_test.go) is
// the one existing test in this package that already isolates the count
// trigger correctly (real wall-clock time between inserts, never a fake
// clock, so pruneCheckInterval cannot elapse after the first insert); the
// four tests below give commands, discovery_runs, macro_runs, and audit_log
// the identical, genuinely count-only proof.
func TestInsertCommandEnforcesRowCountBoundByCountTriggerAlone(t *testing.T) {
	const maxRows = 3
	st := openTestStore(t, nil, WithMaxCommandRows(maxRows), WithMaxCommandAge(0))
	ctx := context.Background()

	const total = pruneEveryNCommands * 2
	for i := 0; i < total; i++ {
		id := idFor(i)
		if _, err := st.InsertCommand(ctx, CommandRecord{
			ID: id, IdempotencyKey: "idem-" + id, Action: "stop_playlist", State: "dispatched",
		}); err != nil {
			t.Fatalf("insert command %d: %v", i, err)
		}
	}

	got, err := st.ListCommands(ctx, 0)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(got) > maxRows {
		t.Errorf("len(got) = %d, want at most %d: the count trigger, the only one that could have fired here (no clock advance between inserts), must still enforce the row bound", len(got), maxRows)
	}
}

func TestStartDiscoveryRunEnforcesRowCountBoundByCountTriggerAlone(t *testing.T) {
	const maxRows = 3
	st := openTestStore(t, nil, WithMaxDiscoveryRunRows(maxRows), WithMaxDiscoveryRunAge(0))
	ctx := context.Background()

	const total = pruneEveryNDiscoveryRuns * 2
	for i := 0; i < total; i++ {
		if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: idFor(i)}); err != nil {
			t.Fatalf("start discovery run %d: %v", i, err)
		}
	}

	got, err := st.ListDiscoveryRuns(ctx, 0)
	if err != nil {
		t.Fatalf("list discovery runs: %v", err)
	}
	if len(got) > maxRows {
		t.Errorf("len(got) = %d, want at most %d: the count trigger, the only one that could have fired here (no clock advance between inserts), must still enforce the row bound", len(got), maxRows)
	}
}

func TestCreateMacroRunEnforcesRowCountBoundByCountTriggerAlone(t *testing.T) {
	const maxRows = 3
	st := openTestStore(t, nil, WithMaxMacroRunRows(maxRows), WithMaxMacroRunAge(0))
	ctx := context.Background()

	const total = pruneEveryNMacroRuns
	for i := 0; i < total; i++ {
		id := idFor(i)
		run, steps := testMacroRun("run-"+id, "idem-"+id, "macro-"+id)
		if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
			t.Fatalf("create macro run %d: %v", i, err)
		}
	}

	got, err := st.ListMacroRuns(ctx, "", 0)
	if err != nil {
		t.Fatalf("list macro runs: %v", err)
	}
	if len(got) > maxRows {
		t.Errorf("len(got) = %d, want at most %d: the count trigger, the only one that could have fired here (no clock advance between inserts), must still enforce the row bound", len(got), maxRows)
	}
}

func TestAppendAuditEntryEnforcesRowCountBoundByCountTriggerAlone(t *testing.T) {
	const maxRows = 3
	st := openTestStore(t, nil, WithMaxAuditRows(maxRows), WithMaxAuditAge(0))
	ctx := context.Background()

	const total = pruneEveryNAuditEntries * 2
	for i := 0; i < total; i++ {
		if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "x"}); err != nil {
			t.Fatalf("append audit entry %d: %v", i, err)
		}
	}

	got, err := st.ListAuditEntries(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(got) > maxRows {
		t.Errorf("len(got) = %d, want at most %d: the count trigger, the only one that could have fired here (no clock advance between inserts), must still enforce the row bound", len(got), maxRows)
	}
}

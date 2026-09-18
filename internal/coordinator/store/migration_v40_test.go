package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// openDatabaseAtV39 mirrors openDatabaseAtV36 one migration further: a
// database carrying every migration up to and including v39, stamped at
// that version, so a test can watch v40 add night_cycle_outcomes underneath
// a store that predates it.
func openDatabaseAtV39(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pre-v40.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	for _, m := range migrations {
		if m.version > 39 {
			continue
		}
		if m.fn != nil {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin tx for migration %d: %v", m.version, err)
			}
			if err := m.fn(ctx, tx); err != nil {
				t.Fatalf("apply migration %d: %v", m.version, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit migration %d: %v", m.version, err)
			}
			continue
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 39`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	return db
}

// TestMigrateV40AddsNightCycleOutcomesTable proves a pre-v40 database has no
// night_cycle_outcomes table and that migrating it forward creates one, with
// no effect on any existing table.
func TestMigrateV40AddsNightCycleOutcomesTable(t *testing.T) {
	db := openDatabaseAtV39(t)
	if tableExists(t, db, "night_cycle_outcomes") {
		t.Fatal("night_cycle_outcomes exists before migrating to v40")
	}

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if !tableExists(t, db, "night_cycle_outcomes") {
		t.Fatal("night_cycle_outcomes does not exist after migrating to v40")
	}

	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() || version < 40 {
		t.Errorf("user_version = %d, want %d and at least 40", version, maxMigrationVersion())
	}
}

// TestMigrateV40ThenStoreLayerCanRoundTripNightCycleOutcomes proves the
// migration lands a usable table: open, list, close, and re-close (a no-op)
// all work against a pre-v40 database exactly as they would against one
// created fresh at v40 or later.
func TestMigrateV40ThenStoreLayerCanRoundTripNightCycleOutcomes(t *testing.T) {
	db := openDatabaseAtV39(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	st := &Store{db: db, now: time.Now}
	ctx := context.Background()

	started := time.Date(2026, 9, 18, 20, 0, 0, 0, time.UTC)
	if err := st.OpenNightCycleOutcome(ctx, "session-pre-v40", 1, started); err != nil {
		t.Fatalf("OpenNightCycleOutcome on a pre-v40 database: %v", err)
	}
	// A second open for the same (session, cycle) is a no-op.
	if err := st.OpenNightCycleOutcome(ctx, "session-pre-v40", 1, started.Add(time.Minute)); err != nil {
		t.Fatalf("OpenNightCycleOutcome (second call): %v", err)
	}

	open, err := st.ListOpenNightCycleOutcomes(ctx)
	if err != nil {
		t.Fatalf("ListOpenNightCycleOutcomes: %v", err)
	}
	if len(open) != 1 || open[0].SessionID != "session-pre-v40" || open[0].Cycle != 1 {
		t.Fatalf("ListOpenNightCycleOutcomes = %+v, want one open row for session-pre-v40/1", open)
	}
	if !open[0].ShowStartedAt.Equal(started) {
		t.Errorf("ShowStartedAt = %v, want %v (the first open call, not the no-op second)", open[0].ShowStartedAt, started)
	}

	ended := started.Add(45 * time.Minute)
	if err := st.CloseNightCycleOutcome(ctx, "session-pre-v40", 1, ended, NightCycleOutcomeCompleted, ""); err != nil {
		t.Fatalf("CloseNightCycleOutcome: %v", err)
	}

	rows, err := st.ListNightCycleOutcomes(ctx, "session-pre-v40")
	if err != nil {
		t.Fatalf("ListNightCycleOutcomes: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListNightCycleOutcomes = %+v, want exactly one row", rows)
	}
	rec := rows[0]
	if rec.EndedAt == nil || !rec.EndedAt.Equal(ended) {
		t.Errorf("EndedAt = %v, want %v", rec.EndedAt, ended)
	}
	if rec.Outcome != NightCycleOutcomeCompleted {
		t.Errorf("Outcome = %q, want %q", rec.Outcome, NightCycleOutcomeCompleted)
	}

	if err := st.CloseNightCycleOutcome(ctx, "session-pre-v40", 1, ended, NightCycleOutcomeInterrupted, "should not apply"); err != ErrNightCycleOutcomeNotFound {
		t.Fatalf("CloseNightCycleOutcome on an already-closed row = %v, want ErrNightCycleOutcomeNotFound", err)
	}

	open, err = st.ListOpenNightCycleOutcomes(ctx)
	if err != nil {
		t.Fatalf("ListOpenNightCycleOutcomes after close: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("ListOpenNightCycleOutcomes after close = %+v, want none open", open)
	}
}

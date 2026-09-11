package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// buildStoreAtVersion applies every migration up to version, in order,
// so a test can reopen the result through [open] to prove the next
// migration applies on top.
func buildStoreAtVersion(t *testing.T, dir string, version int) {
	t.Helper()
	dbPath := filepath.Join(dir, dbFileName)
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, m := range migrations {
		if m.version > version {
			continue
		}
		if m.fn != nil {
			if err := m.fn(ctx, tx); err != nil {
				t.Fatalf("apply migration %d: %v", m.version, err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestMigrationV35AppliesOnAFreshDatabase(t *testing.T) {
	st := openTestStore(t, nil)
	var version int
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() {
		t.Errorf("user_version = %d, want %d", version, maxMigrationVersion())
	}
	if _, err := st.CreateAlignmentRun(context.Background(), AlignmentRunRecord{ID: "run-1", NodeID: "node-a", StartedBy: "op"}); err != nil {
		t.Fatalf("create run on fresh db: %v", err)
	}
}

func TestMigrationV35AppliesOnADatabaseAtV34(t *testing.T) {
	dir := t.TempDir()
	buildStoreAtVersion(t, dir, 34)

	st, err := open(context.Background(), dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 35): %v", err)
	}
	defer func() { _ = st.Close() }()

	var version int
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() {
		t.Errorf("user_version = %d, want %d", version, maxMigrationVersion())
	}
	if _, err := st.CreateAlignmentRun(context.Background(), AlignmentRunRecord{ID: "run-1", NodeID: "node-a", StartedBy: "op"}); err != nil {
		t.Fatalf("create run after migrating from v34: %v", err)
	}
}

func TestCreateAlignmentRunAndGet(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-09-11T00:00:00Z")}
	st := openTestStore(t, clock)
	ctx := context.Background()

	run, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-1", NodeID: "node-a", StartedBy: "op-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !run.StartedAt.Equal(clock.t) {
		t.Errorf("StartedAt = %v, want %v", run.StartedAt, clock.t)
	}

	got, samples, err := st.GetAlignmentRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NodeID != "node-a" || got.StartedBy != "op-1" {
		t.Errorf("got = %+v, want node-a/op-1", got)
	}
	if len(samples) != 0 {
		t.Errorf("samples = %v, want none", samples)
	}
}

func TestCreateAlignmentRunConflictsWithActiveRun(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-1", NodeID: "node-a", StartedBy: "op"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-2", NodeID: "node-a", StartedBy: "op"})
	if err == nil {
		t.Fatalf("second create for the same node succeeded, want a conflict")
	}
	if !errors.Is(err, ErrAlignmentRunAlreadyActive) {
		t.Fatalf("error = %v, want it to wrap ErrAlignmentRunAlreadyActive", err)
	}
	var conflict *AlignmentRunAlreadyActiveError
	if !errors.As(err, &conflict) || conflict.Active.ID != "run-1" {
		t.Fatalf("error = %v, want *AlignmentRunAlreadyActiveError naming run-1", err)
	}

	// A different node is unaffected.
	if _, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-3", NodeID: "node-b", StartedBy: "op"}); err != nil {
		t.Fatalf("create for a different node: %v", err)
	}
}

func TestCreateAlignmentRunAllowedAfterStop(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-1", NodeID: "node-a", StartedBy: "op"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.StopAlignmentRun(ctx, "run-1", "op", "operator stop"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-2", NodeID: "node-a", StartedBy: "op"}); err != nil {
		t.Fatalf("create after stop: %v", err)
	}
}

func TestStopAlignmentRunNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	if _, err := st.StopAlignmentRun(context.Background(), "missing", "op", "reason"); !errors.Is(err, ErrAlignmentRunNotFound) {
		t.Fatalf("error = %v, want ErrAlignmentRunNotFound", err)
	}
}

func TestStopAlignmentRunTwiceIsNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-1", NodeID: "node-a", StartedBy: "op"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.StopAlignmentRun(ctx, "run-1", "op", "reason"); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	if _, err := st.StopAlignmentRun(ctx, "run-1", "op", "reason"); !errors.Is(err, ErrAlignmentRunNotFound) {
		t.Fatalf("error = %v, want ErrAlignmentRunNotFound on a second stop", err)
	}
}

func TestListAlignmentRunsNewestFirst(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-09-11T00:00:00Z")}
	st := openTestStore(t, clock)
	ctx := context.Background()

	if _, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-1", NodeID: "node-a", StartedBy: "op"}); err != nil {
		t.Fatalf("create run-1: %v", err)
	}
	if _, err := st.StopAlignmentRun(ctx, "run-1", "op", "done"); err != nil {
		t.Fatalf("stop run-1: %v", err)
	}
	clock.advance(time.Minute)
	if _, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-2", NodeID: "node-a", StartedBy: "op"}); err != nil {
		t.Fatalf("create run-2: %v", err)
	}

	runs, err := st.ListAlignmentRuns(ctx, "node-a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != "run-2" || runs[1].ID != "run-1" {
		t.Fatalf("runs = %+v, want [run-2, run-1]", runs)
	}
}

func TestAppendAlignmentSampleDedupsBySampleTime(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-1", NodeID: "node-a", StartedBy: "op"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	sampledAt := mustTime(t, "2026-09-11T00:00:01Z")
	sample := AlignmentSampleRecord{RunID: "run-1", SampledAt: sampledAt, OffsetMs: 3.5, SessionID: "sess-1"}
	if err := st.AppendAlignmentSample(ctx, sample); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Same sample time reported twice (a republished tick): appends once.
	if err := st.AppendAlignmentSample(ctx, sample); err != nil {
		t.Fatalf("second append: %v", err)
	}

	_, samples, err := st.GetAlignmentRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("samples = %+v, want exactly one", samples)
	}
}

func TestAppendAlignmentSampleNoOpOnStoppedRun(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.CreateAlignmentRun(ctx, AlignmentRunRecord{ID: "run-1", NodeID: "node-a", StartedBy: "op"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.StopAlignmentRun(ctx, "run-1", "op", "done"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := st.AppendAlignmentSample(ctx, AlignmentSampleRecord{RunID: "run-1", SampledAt: time.Now(), OffsetMs: 1, SessionID: "s"}); err != nil {
		t.Fatalf("append to stopped run: %v", err)
	}
	_, samples, err := st.GetAlignmentRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("samples = %+v, want none appended after stop", samples)
	}
}

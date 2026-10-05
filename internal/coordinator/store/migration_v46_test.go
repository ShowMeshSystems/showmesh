package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrationV46AppliesOnADatabaseAtV45 proves a bed row written before v46
// reads back with no node time, that a rerun is harmless, and that the new
// column round-trips.
func TestMigrationV46AppliesOnADatabaseAtV45(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	buildStoreAtVersion(t, dir, 45)
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFileName))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	at := time.Date(2026, 10, 5, 18, 0, 47, 0, time.UTC)
	if _, err := raw.ExecContext(ctx, `INSERT INTO night_cue_outbox (id, session_id, cycle, phase, cue_name, action_revision, state, dispatched_at, resolved_at, outcome, outcome_reason, created_at)
		VALUES ('r1', 's1', 1, 'p', 'c', 1, 'resolved', ?, ?, 'confirmed', '', ?)`, timeToDB(at), timeToDB(at), timeToDB(at)); err != nil {
		t.Fatalf("insert pre-v46 row: %v", err)
	}
	_ = raw.Close()

	st, err := open(ctx, dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 46): %v", err)
	}
	defer func() { _ = st.Close() }()
	row, err := st.GetNightCueOutboxRow(ctx, "s1", 1, "p", "c")
	if err != nil {
		t.Fatalf("read pre-v46 row: %v", err)
	}
	if row.NodeRespondedAt != nil || row.ResolvedAt == nil || !row.ResolvedAt.Equal(at) {
		t.Fatalf("pre-v46 row = %+v, want resolved_at kept and no node time", row)
	}
	responded := at.Add(time.Second)
	row.NodeRespondedAt = &responded
	if err := st.UpdateNightCueOutboxRow(ctx, row); err != nil {
		t.Fatalf("update: %v", err)
	}
	row, _ = st.GetNightCueOutboxRow(ctx, "s1", 1, "p", "c")
	if row.NodeRespondedAt == nil || !row.NodeRespondedAt.Equal(responded) {
		t.Fatalf("node time after update = %v, want %v", row.NodeRespondedAt, responded)
	}

	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := migrateV46AddNightCueOutboxNodeRespondedAt(ctx, tx); err != nil {
		t.Fatalf("second run of migration 46: %v", err)
	}
}

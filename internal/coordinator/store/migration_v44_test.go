package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrationV44AppliesOnADatabaseAtV43 proves an existing night session
// reads back with no hold after v44, and that a rerun is harmless.
func TestMigrationV44AppliesOnADatabaseAtV43(t *testing.T) {
	dir := t.TempDir()
	buildStoreAtVersion(t, dir, 43)
	st, err := open(context.Background(), dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 44): %v", err)
	}
	_ = st.Close()

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFileName))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = raw.Close() }()
	tx, err := raw.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := migrateV44AddNightSessionStopHoldColumns(context.Background(), tx); err != nil {
		t.Fatalf("second run of migration 44: %v", err)
	}
}

// TestNightSessionStopHoldRoundTrips proves the hold persists on update and
// clears back to absent.
func TestNightSessionStopHoldRoundTrips(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 20, 0, 0, 0, time.UTC)
	rec := NightSessionRecord{ID: "s1", State: "live", StateEnteredAt: now}
	if err := st.CreateNightSession(ctx, rec, now); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, _, err := st.GetCurrentNightSession(ctx)
	if err != nil || got.StopHold != nil {
		t.Fatalf("new session hold = %+v, err %v; want none", got.StopHold, err)
	}
	got.StopHold = &NightSessionStopHold{Reason: "stopped", At: now, Principal: "bench"}
	if err := st.UpdateNightSession(ctx, got, now); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _, _ = st.GetCurrentNightSession(ctx)
	if got.StopHold == nil || got.StopHold.Reason != "stopped" || !got.StopHold.At.Equal(now) || got.StopHold.Principal != "bench" {
		t.Fatalf("hold after update = %+v", got.StopHold)
	}
	got.StopHold = nil
	if err := st.UpdateNightSession(ctx, got, now); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _, _ = st.GetCurrentNightSession(ctx)
	if got.StopHold != nil {
		t.Fatalf("hold after clear = %+v, want none", got.StopHold)
	}
}

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrationV42AppliesOnADatabaseAtV41 proves v42 adds started_by_name
// under an existing active row, which then reads back with an empty name.
func TestMigrationV42AppliesOnADatabaseAtV41(t *testing.T) {
	dir := t.TempDir()
	buildStoreAtVersion(t, dir, 41)
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFileName))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO weather_delay_state (id, active, kind, started_at, started_by, revision) VALUES ('default', 1, 'cancelNight', '2026-10-31T20:00:00.000000000Z', 'p1', 3)`); err != nil {
		t.Fatalf("insert v41 row: %v", err)
	}
	_ = raw.Close()

	st, err := open(context.Background(), dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 42): %v", err)
	}
	defer func() { _ = st.Close() }()

	rec, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState after v42: %v", err)
	}
	if !rec.Active || rec.Kind != "cancelNight" || rec.Revision != 3 || rec.StartedByName != "" {
		t.Fatalf("state after v42 = %+v, want the v41 row with an empty name", rec)
	}

	tx, err := st.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := migrateV42AddWeatherDelayStateStartedByNameColumn(context.Background(), tx); err != nil {
		t.Fatalf("second run of migration 42: %v", err)
	}
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// openDatabaseAtV40 returns a database migrated to v40 and stamped there.
func openDatabaseAtV40(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pre-v41.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	for _, m := range migrations {
		if m.version > 40 {
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
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 40`); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	return db
}

// TestMigrateV41AddsWeatherDelayStateTable proves a pre-v41 database has no
// weather_delay_state table and that migrating it forward creates one, with
// no effect on any existing table.
func TestMigrateV41AddsWeatherDelayStateTable(t *testing.T) {
	db := openDatabaseAtV40(t)
	if tableExists(t, db, "weather_delay_state") {
		t.Fatal("weather_delay_state exists before migrating to v41")
	}

	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if !tableExists(t, db, "weather_delay_state") {
		t.Fatal("weather_delay_state does not exist after migrating to v41")
	}

	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != maxMigrationVersion() || version < 41 {
		t.Errorf("user_version = %d, want %d and at least 41", version, maxMigrationVersion())
	}
}

// TestMigrateV41ThenStoreLayerCanRoundTripWeatherDelayState proves the
// migration lands a usable table: Get on an empty table reports "not
// active", Set persists a state, and a later Get returns it back exactly.
func TestMigrateV41ThenStoreLayerCanRoundTripWeatherDelayState(t *testing.T) {
	db := openDatabaseAtV40(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	st := &Store{db: db, now: time.Now}
	ctx := context.Background()

	got, err := st.GetWeatherDelayState(ctx)
	if err != nil {
		t.Fatalf("GetWeatherDelayState on an empty table: %v", err)
	}
	if got != (WeatherDelayStateRecord{}) {
		t.Fatalf("GetWeatherDelayState on an empty table = %+v, want the zero (not active) record", got)
	}

	startedAt := time.Date(2026, 9, 19, 21, 0, 0, 0, time.UTC)
	want := WeatherDelayStateRecord{Active: true, Kind: "delay", StartedAt: startedAt, StartedBy: "op-1", Revision: 1}
	if err := st.SetWeatherDelayState(ctx, want); err != nil {
		t.Fatalf("SetWeatherDelayState: %v", err)
	}

	got, err = st.GetWeatherDelayState(ctx)
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if got.Active != want.Active || got.Kind != want.Kind || got.StartedBy != want.StartedBy || got.Revision != want.Revision {
		t.Fatalf("GetWeatherDelayState = %+v, want %+v", got, want)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}

	// Resume: a second Set overwrites the row in place, clearing it back
	// to not-active with the next revision.
	cleared := WeatherDelayStateRecord{Revision: 2}
	if err := st.SetWeatherDelayState(ctx, cleared); err != nil {
		t.Fatalf("SetWeatherDelayState (clear): %v", err)
	}
	got, err = st.GetWeatherDelayState(ctx)
	if err != nil {
		t.Fatalf("GetWeatherDelayState after clear: %v", err)
	}
	if got.Active || got.Kind != "" || got.StartedBy != "" || got.Revision != 2 {
		t.Fatalf("GetWeatherDelayState after clear = %+v, want a cleared record at revision 2", got)
	}
	if !got.StartedAt.IsZero() {
		t.Errorf("StartedAt after clear = %v, want zero", got.StartedAt)
	}
}

func TestWeatherDelayStateRefusesASecondRow(t *testing.T) {
	db := openDatabaseAtV40(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO weather_delay_state (id) VALUES ('other')`); err == nil {
		t.Fatal("inserting a second weather_delay_state row succeeded, want a constraint failure")
	}
}

func TestSetWeatherDelayStateRefusesARevisionThatDoesNotIncrease(t *testing.T) {
	db := openDatabaseAtV40(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := &Store{db: db, now: time.Now}
	ctx := context.Background()

	if err := st.SetWeatherDelayState(ctx, WeatherDelayStateRecord{Revision: 3}); err != nil {
		t.Fatalf("SetWeatherDelayState(3): %v", err)
	}
	for _, rev := range []int64{3, 0} {
		if err := st.SetWeatherDelayState(ctx, WeatherDelayStateRecord{Revision: rev}); !errors.Is(err, ErrWeatherDelayRevisionNotIncreasing) {
			t.Errorf("SetWeatherDelayState(%d) = %v, want ErrWeatherDelayRevisionNotIncreasing", rev, err)
		}
	}
	got, err := st.GetWeatherDelayState(ctx)
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if got.Revision != 3 {
		t.Fatalf("Revision = %d, want 3", got.Revision)
	}
}

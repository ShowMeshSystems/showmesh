package store

import (
	"context"
	"database/sql"
	"fmt"
)

// migrateV44AddNightSessionStopHoldColumns adds the level 1 stop hold
// (ADR-054) to night_sessions, skipping any column that already exists so
// a rerun never fails with "duplicate column name".
func migrateV44AddNightSessionStopHoldColumns(ctx context.Context, tx *sql.Tx) error {
	for _, col := range []struct{ name, ddl string }{
		{"stop_hold_reason", `ALTER TABLE night_sessions ADD COLUMN stop_hold_reason TEXT NOT NULL DEFAULT ''`},
		{"stop_hold_at", `ALTER TABLE night_sessions ADD COLUMN stop_hold_at TEXT`},
		{"stop_hold_principal", `ALTER TABLE night_sessions ADD COLUMN stop_hold_principal TEXT NOT NULL DEFAULT ''`},
	} {
		var has int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('night_sessions') WHERE name = ?`, col.name,
		).Scan(&has); err != nil {
			return fmt.Errorf("check night_sessions.%s exists: %w", col.name, err)
		}
		if has > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, col.ddl); err != nil {
			return fmt.Errorf("add night_sessions.%s: %w", col.name, err)
		}
	}
	return nil
}

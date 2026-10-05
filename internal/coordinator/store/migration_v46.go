package store

import (
	"context"
	"database/sql"
	"fmt"
)

// migrateV46AddNightCueOutboxNodeRespondedAt adds the node-clock answer time
// to night_cue_outbox, skipping the column if it already exists so a rerun
// never fails with "duplicate column name".
func migrateV46AddNightCueOutboxNodeRespondedAt(ctx context.Context, tx *sql.Tx) error {
	var has int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('night_cue_outbox') WHERE name = 'node_responded_at'`,
	).Scan(&has); err != nil {
		return fmt.Errorf("check night_cue_outbox.node_responded_at exists: %w", err)
	}
	if has > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE night_cue_outbox ADD COLUMN node_responded_at TEXT`); err != nil {
		return fmt.Errorf("add night_cue_outbox.node_responded_at: %w", err)
	}
	return nil
}

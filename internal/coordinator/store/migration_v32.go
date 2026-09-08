package store

import (
	"context"
	"database/sql"
	"fmt"
)

// migrateV32AddFPPPlaylistEntryObservationPlaylistLoopColumn adds
// fpp_playlist_entry_observations.playlist_loop: FPP's own mainPlaylist pass
// counter for the running playlist, as the plugin reported it
// (FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1.2 and §1.8), NULL when the plugin
// sent none.
//
// NULL and 0 are different values here and the distinction is the point. 0 is
// a real first pass; NULL is a plugin that predates the field. Ingestion
// compares this column between the incoming observation and the stored one to
// decide whether a playlist looped back into an entry it already visited, and
// a plugin sending nothing must compare equal to itself rather than to a
// plugin reporting its first lap.
//
// A pure addition: every existing row's playlist_loop is implicitly NULL, so
// every row this store already holds reads as "no pass counter reported",
// which is exactly what those rows meant. No data fix follows.
//
// A Go function rather than a bare ALTER TABLE, for the reason
// migrateV30AddConfigObjectDeletedAtColumn records: some tests rewind
// PRAGMA user_version and reopen the store to force every later migration to
// run again, and a bare ALTER TABLE ... ADD COLUMN fails outright on that
// second pass with "duplicate column name" once the column exists. Checking
// first and no-opping when it is already present is what makes replay safe.
func migrateV32AddFPPPlaylistEntryObservationPlaylistLoopColumn(ctx context.Context, tx *sql.Tx) error {
	var hasColumn int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('fpp_playlist_entry_observations') WHERE name = 'playlist_loop'`,
	).Scan(&hasColumn); err != nil {
		return fmt.Errorf("check fpp_playlist_entry_observations.playlist_loop exists: %w", err)
	}
	if hasColumn > 0 {
		// Already added: the one replay shape this function exists to
		// tolerate.
		return nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE fpp_playlist_entry_observations ADD COLUMN playlist_loop INTEGER`); err != nil {
		return fmt.Errorf("add fpp_playlist_entry_observations.playlist_loop: %w", err)
	}
	return nil
}

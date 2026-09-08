package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// migrateV31FixedWidthTimestamps rewrites every TEXT timestamp column's
// on-disk value from the trimmed time.RFC3339Nano format schemaV1
// established to timeLayout's fixed nine-digit-fraction format
// (queries.go), so a plain string ORDER BY on any of them sorts in true
// chronological order. See timeLayout's own doc comment for the failure
// this closes: two rows written in the same second whose fractions trim
// to different lengths under the old format (".122" vs ".122183")
// compared with the shorter, EARLIER value sorting AFTER the longer,
// LATER one.
//
// This walks every user table's own schema rather than naming tables and
// columns by hand: this package's own convention (queries.go's timeLayout
// and dbToTime doc comments) is that a timestamp column is always TEXT and
// always named ending in "_at", so a generic sweep stays correct for every
// table this migration reaches, including one a later migration adds,
// without this migration needing an update every time a new timestamp
// column is added. A column holding the empty string, this package's
// existing "unset" sentinel for a handful of TEXT NOT NULL DEFAULT ”
// columns, e.g. fpp_instance_uuid_observations.changed_at (schemaV16's own
// doc comment), is left untouched, exactly like a SQL NULL one: neither
// is a timestamp to reformat.
func migrateV31FixedWidthTimestamps(ctx context.Context, tx *sql.Tx) error {
	tables, err := v31TableNames(ctx, tx)
	if err != nil {
		return err
	}
	for _, table := range tables {
		cols, err := v31TimestampColumns(ctx, tx, table)
		if err != nil {
			return err
		}
		for _, col := range cols {
			if err := v31RewriteColumn(ctx, tx, table, col); err != nil {
				return fmt.Errorf("rewrite %s.%s: %w", table, col, err)
			}
		}
	}
	return nil
}

// v31TableNames returns every user table's name (excluding sqlite's own
// internal tables).
func v31TableNames(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\'`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("list tables: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	return tables, nil
}

// v31TimestampColumns returns table's own TEXT columns whose name ends in
// "_at", this package's own timestamp-column naming convention (see
// [migrateV31FixedWidthTimestamps]'s doc comment).
func v31TimestampColumns(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, v31QuoteIdent(table)))
	if err != nil {
		return nil, fmt.Errorf("read schema of %s: %w", table, err)
	}
	var cols []string
	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultVal, &pk); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan column of %s: %w", table, err)
		}
		if strings.EqualFold(colType, "TEXT") && strings.HasSuffix(name, "_at") {
			cols = append(cols, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read schema of %s: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("read schema of %s: %w", table, err)
	}
	return cols, nil
}

// v31RewriteColumn reformats every non-NULL, non-empty value of table's
// col from the old trimmed time.RFC3339Nano format to timeLayout's fixed
// nine-digit-fraction format. A value already in the new format (a
// replayed migration, see [migration]'s own append-only doc comment on
// idempotence) or that is unset (NULL or ”, this package's two "no
// timestamp here" spellings) is left untouched.
func v31RewriteColumn(ctx context.Context, tx *sql.Tx, table, col string) error {
	ident := v31QuoteIdent(table)
	colIdent := v31QuoteIdent(col)
	rows, err := tx.QueryContext(ctx,
		fmt.Sprintf(`SELECT rowid, %s FROM %s WHERE %s IS NOT NULL AND %s <> ''`, colIdent, ident, colIdent, colIdent))
	if err != nil {
		return fmt.Errorf("read values: %w", err)
	}
	type pendingRewrite struct {
		rowid int64
		value string
	}
	var toUpdate []pendingRewrite
	for rows.Next() {
		var p pendingRewrite
		if err := rows.Scan(&p.rowid, &p.value); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan value: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, p.value)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("parse stored value %q: %w", p.value, err)
		}
		reformatted := timeToDB(parsed)
		if reformatted == p.value {
			continue
		}
		p.value = reformatted
		toUpdate = append(toUpdate, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read values: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("read values: %w", err)
	}

	for _, p := range toUpdate {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET %s = ? WHERE rowid = ?`, ident, colIdent),
			p.value, p.rowid,
		); err != nil {
			return fmt.Errorf("write reformatted value: %w", err)
		}
	}
	return nil
}

// v31QuoteIdent double-quotes a SQLite identifier for interpolation into a
// PRAGMA or DDL statement, neither of which accepts a bound parameter for
// a table or column name. Safe here because every identifier this
// migration interpolates comes from sqlite_master or PRAGMA table_info
// itself, never from external input.
func v31QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

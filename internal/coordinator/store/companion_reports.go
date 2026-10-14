package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// schemaV49 adds companion_reports: files a ShowMesh Companion collected on
// its own machine and uploaded, such as an application crash report.
const schemaV49 = `
CREATE TABLE IF NOT EXISTS companion_reports (
    id             TEXT PRIMARY KEY,
    companion_id   TEXT NOT NULL,
    kind           TEXT NOT NULL,
    source         TEXT NOT NULL,
    file_name      TEXT NOT NULL,
    captured_at    TEXT NOT NULL,
    received_at    TEXT NOT NULL,
    content_sha256 TEXT NOT NULL,
    size_bytes     INTEGER NOT NULL,
    content        TEXT NOT NULL,
    principal_id   TEXT NOT NULL,
    UNIQUE (companion_id, content_sha256)
);
CREATE INDEX IF NOT EXISTS companion_reports_received ON companion_reports (received_at DESC, id DESC);
`

// CompanionReportRetainPerCompanion is how many reports each companion
// keeps. The oldest beyond it are deleted when a new one is stored.
const CompanionReportRetainPerCompanion = 200

// CompanionReportRecord is one uploaded report. Content is empty on rows
// returned by [Store.ListCompanionReports].
type CompanionReportRecord struct {
	ID            string
	CompanionID   string
	Kind          string
	Source        string
	FileName      string
	CapturedAt    time.Time
	ReceivedAt    time.Time
	ContentSHA256 string
	SizeBytes     int64
	Content       string
	PrincipalID   string
}

// ErrCompanionReportNotFound means no report has the requested id.
var ErrCompanionReportNotFound = errors.New("store: companion report not found")

const companionReportSummaryColumns = `id, companion_id, kind, source, file_name, captured_at, received_at,
	content_sha256, size_bytes, principal_id`

func scanCompanionReport(row interface{ Scan(dest ...any) error }, withContent bool) (CompanionReportRecord, error) {
	var (
		rec                  CompanionReportRecord
		capturedAt, received string
	)
	dest := []any{&rec.ID, &rec.CompanionID, &rec.Kind, &rec.Source, &rec.FileName, &capturedAt, &received,
		&rec.ContentSHA256, &rec.SizeBytes, &rec.PrincipalID}
	if withContent {
		dest = append(dest, &rec.Content)
	}
	if err := row.Scan(dest...); err != nil {
		return CompanionReportRecord{}, err
	}
	var err error
	if rec.CapturedAt, err = dbToTime(capturedAt); err != nil {
		return CompanionReportRecord{}, fmt.Errorf("store: parse companion report captured_at: %w", err)
	}
	if rec.ReceivedAt, err = dbToTime(received); err != nil {
		return CompanionReportRecord{}, fmt.Errorf("store: parse companion report received_at: %w", err)
	}
	return rec, nil
}

// PutCompanionReport stores rec unless its companion already uploaded the
// same content, in which case it returns the stored row and created false.
// Storing a new row deletes that companion's reports beyond
// [CompanionReportRetainPerCompanion], oldest first.
func (s *Store) PutCompanionReport(ctx context.Context, rec CompanionReportRecord) (stored CompanionReportRecord, created bool, err error) {
	guardNotInTx(ctx, "Store.PutCompanionReport")
	if rec.ID == "" || rec.CompanionID == "" || rec.ContentSHA256 == "" {
		return CompanionReportRecord{}, false, fmt.Errorf("store: put companion report: id, companion id and content hash are required")
	}
	err = s.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		res, err := tx.tx.ExecContext(ctx, `
			INSERT INTO companion_reports (id, companion_id, kind, source, file_name, captured_at, received_at,
				content_sha256, size_bytes, content, principal_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (companion_id, content_sha256) DO NOTHING
		`, rec.ID, rec.CompanionID, rec.Kind, rec.Source, rec.FileName, timeToDB(rec.CapturedAt), timeToDB(rec.ReceivedAt),
			rec.ContentSHA256, rec.SizeBytes, rec.Content, rec.PrincipalID)
		if err != nil {
			return fmt.Errorf("store: insert companion report: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: insert companion report: %w", err)
		}
		created = n == 1
		row := tx.tx.QueryRowContext(ctx, `SELECT `+companionReportSummaryColumns+`
			FROM companion_reports WHERE companion_id = ? AND content_sha256 = ?`, rec.CompanionID, rec.ContentSHA256)
		if stored, err = scanCompanionReport(row, false); err != nil {
			return fmt.Errorf("store: read stored companion report: %w", err)
		}
		if !created {
			return nil
		}
		if _, err := tx.tx.ExecContext(ctx, `
			DELETE FROM companion_reports WHERE companion_id = ? AND id NOT IN (
				SELECT id FROM companion_reports WHERE companion_id = ?
				ORDER BY received_at DESC, id DESC LIMIT ?)
		`, rec.CompanionID, rec.CompanionID, CompanionReportRetainPerCompanion); err != nil {
			return fmt.Errorf("store: prune companion reports: %w", err)
		}
		return nil
	})
	if err != nil {
		return CompanionReportRecord{}, false, err
	}
	return stored, created, nil
}

// ListCompanionReports returns report summaries newest first, without
// content. An empty companionID lists every companion's reports.
func (s *Store) ListCompanionReports(ctx context.Context, companionID string, limit int) ([]CompanionReportRecord, error) {
	guardNotInTx(ctx, "Store.ListCompanionReports")
	if limit <= 0 {
		return nil, fmt.Errorf("store: list companion reports: limit must be positive")
	}
	query := `SELECT ` + companionReportSummaryColumns + ` FROM companion_reports`
	args := []any{}
	if companionID != "" {
		query += ` WHERE companion_id = ?`
		args = append(args, companionID)
	}
	query += ` ORDER BY received_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list companion reports: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []CompanionReportRecord{}
	for rows.Next() {
		rec, err := scanCompanionReport(rows, false)
		if err != nil {
			return nil, fmt.Errorf("store: scan companion report: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list companion reports: %w", err)
	}
	return out, nil
}

// GetCompanionReport returns one report with its content, or
// [ErrCompanionReportNotFound].
func (s *Store) GetCompanionReport(ctx context.Context, id string) (CompanionReportRecord, error) {
	guardNotInTx(ctx, "Store.GetCompanionReport")
	row := s.db.QueryRowContext(ctx, `SELECT `+companionReportSummaryColumns+`, content
		FROM companion_reports WHERE id = ?`, id)
	rec, err := scanCompanionReport(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return CompanionReportRecord{}, ErrCompanionReportNotFound
	}
	if err != nil {
		return CompanionReportRecord{}, fmt.Errorf("store: get companion report %q: %w", id, err)
	}
	return rec, nil
}

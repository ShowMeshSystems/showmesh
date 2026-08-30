package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV23's fallback_programs and
// fallback_program_acknowledgements repository methods (ADR-048, Track J
// Track J's J1). See migrations.go's schemaV23 doc comment for the split
// between the two tables.

// FallbackProgramRecord is one stored, published fallback program: the
// coordinator's own record of the last package it compiled and signed
// for one FPP host. ProgramJSON is the complete, marshaled
// [fallbackprogram.SignedProgram], the exact bytes a re-fetch replays,
// never re-serialized at read time (see schemaV23's own doc comment).
// SignatureB64 duplicates the signature already embedded in ProgramJSON
// as its own column so a caller can filter or display it without
// decoding the whole payload; ProgramJSON remains the sole source of
// truth a verifier actually checks.
type FallbackProgramRecord struct {
	FPPInstanceUUID string
	PackageID       string
	Revision        string
	ShowID          string
	Generation      int64
	ProgramJSON     string
	SignatureB64    string
	ExpiresAt       time.Time
	CompiledAt      time.Time
}

// ErrFallbackProgramNotFound is returned by [Store.GetFallbackProgram]/
// [Tx.GetFallbackProgram] when instanceUUID has never had a program
// published for it.
var ErrFallbackProgramNotFound = errors.New("store: fallback program not found")

func putFallbackProgram(ctx context.Context, q querier, rec FallbackProgramRecord) error {
	if rec.FPPInstanceUUID == "" {
		return fmt.Errorf("store: put fallback program: fppInstanceUUID is empty")
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO fallback_programs (fpp_instance_uuid, package_id, revision, show_id, generation, program_json, signature_b64, expires_at, compiled_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fpp_instance_uuid) DO UPDATE SET
			package_id    = excluded.package_id,
			revision      = excluded.revision,
			show_id       = excluded.show_id,
			generation    = excluded.generation,
			program_json  = excluded.program_json,
			signature_b64 = excluded.signature_b64,
			expires_at    = excluded.expires_at,
			compiled_at   = excluded.compiled_at
	`, rec.FPPInstanceUUID, rec.PackageID, rec.Revision, rec.ShowID, rec.Generation, rec.ProgramJSON, rec.SignatureB64,
		timeToDB(rec.ExpiresAt), timeToDB(rec.CompiledAt)); err != nil {
		return fmt.Errorf("store: put fallback program %q: %w", rec.FPPInstanceUUID, err)
	}
	return nil
}

// PutFallbackProgram upserts instanceUUID's published fallback program: a
// wholesale replacement, never an incremental merge, ADR-048 decision 1:
// "It is a complete replacement, not a patch."
func (s *Store) PutFallbackProgram(ctx context.Context, rec FallbackProgramRecord) error {
	guardNotInTx(ctx, "Store.PutFallbackProgram")
	return putFallbackProgram(ctx, s.db, rec)
}

// PutFallbackProgram is [Store.PutFallbackProgram]'s [Tx] form.
func (t *Tx) PutFallbackProgram(ctx context.Context, rec FallbackProgramRecord) error {
	return putFallbackProgram(ctx, t.tx, rec)
}

func scanFallbackProgram(row interface{ Scan(dest ...any) error }) (FallbackProgramRecord, error) {
	var (
		rec        FallbackProgramRecord
		expiresAt  string
		compiledAt string
	)
	if err := row.Scan(&rec.FPPInstanceUUID, &rec.PackageID, &rec.Revision, &rec.ShowID, &rec.Generation,
		&rec.ProgramJSON, &rec.SignatureB64, &expiresAt, &compiledAt); err != nil {
		return FallbackProgramRecord{}, err
	}
	var err error
	if rec.ExpiresAt, err = dbToTime(expiresAt); err != nil {
		return FallbackProgramRecord{}, fmt.Errorf("store: parse fallback program expires_at: %w", err)
	}
	if rec.CompiledAt, err = dbToTime(compiledAt); err != nil {
		return FallbackProgramRecord{}, fmt.Errorf("store: parse fallback program compiled_at: %w", err)
	}
	return rec, nil
}

func getFallbackProgram(ctx context.Context, q querier, instanceUUID string) (FallbackProgramRecord, error) {
	row := q.QueryRowContext(ctx, `
		SELECT fpp_instance_uuid, package_id, revision, show_id, generation, program_json, signature_b64, expires_at, compiled_at
		FROM fallback_programs WHERE fpp_instance_uuid = ?
	`, instanceUUID)
	rec, err := scanFallbackProgram(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FallbackProgramRecord{}, ErrFallbackProgramNotFound
	}
	if err != nil {
		return FallbackProgramRecord{}, fmt.Errorf("store: get fallback program %q: %w", instanceUUID, err)
	}
	return rec, nil
}

// GetFallbackProgram returns instanceUUID's last published fallback
// program, or [ErrFallbackProgramNotFound].
func (s *Store) GetFallbackProgram(ctx context.Context, instanceUUID string) (FallbackProgramRecord, error) {
	guardNotInTx(ctx, "Store.GetFallbackProgram")
	return getFallbackProgram(ctx, s.db, instanceUUID)
}

// GetFallbackProgram is [Store.GetFallbackProgram]'s [Tx] form.
func (t *Tx) GetFallbackProgram(ctx context.Context, instanceUUID string) (FallbackProgramRecord, error) {
	return getFallbackProgram(ctx, t.tx, instanceUUID)
}

func listFallbackPrograms(ctx context.Context, q querier) ([]FallbackProgramRecord, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT fpp_instance_uuid, package_id, revision, show_id, generation, program_json, signature_b64, expires_at, compiled_at
		FROM fallback_programs ORDER BY fpp_instance_uuid ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list fallback programs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []FallbackProgramRecord
	for rows.Next() {
		rec, err := scanFallbackProgram(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan fallback program: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list fallback programs: %w", err)
	}
	return out, nil
}

// ListFallbackPrograms returns every host's last published fallback
// program, ordered by FPP instance id.
func (s *Store) ListFallbackPrograms(ctx context.Context) ([]FallbackProgramRecord, error) {
	guardNotInTx(ctx, "Store.ListFallbackPrograms")
	return listFallbackPrograms(ctx, s.db)
}

// ListFallbackPrograms is [Store.ListFallbackPrograms]'s [Tx] form.
func (t *Tx) ListFallbackPrograms(ctx context.Context) ([]FallbackProgramRecord, error) {
	return listFallbackPrograms(ctx, t.tx)
}

// FallbackProgramAckRecord is one FPP host's own evidence of the package
// it verified and installed (ADR-048 decision 1: "The plugin reports the
// package id, revision, verification result, installed time, and age.").
// Age is deliberately not stored: it is derived at read time from
// InstalledAt against the reader's own clock, never persisted as a value
// that would go stale the instant it was written.
type FallbackProgramAckRecord struct {
	FPPInstanceUUID    string
	PackageID          string
	Revision           string
	VerificationResult string
	InstalledAt        time.Time
	AcknowledgedAt     time.Time
}

// ErrFallbackProgramAckNotFound is returned by
// [Store.GetFallbackProgramAck]/[Tx.GetFallbackProgramAck] when
// instanceUUID has never acknowledged a package.
var ErrFallbackProgramAckNotFound = errors.New("store: fallback program acknowledgement not found")

func putFallbackProgramAck(ctx context.Context, q querier, rec FallbackProgramAckRecord) error {
	if rec.FPPInstanceUUID == "" {
		return fmt.Errorf("store: put fallback program ack: fppInstanceUUID is empty")
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO fallback_program_acknowledgements (fpp_instance_uuid, package_id, revision, verification_result, installed_at, acknowledged_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(fpp_instance_uuid) DO UPDATE SET
			package_id          = excluded.package_id,
			revision            = excluded.revision,
			verification_result = excluded.verification_result,
			installed_at        = excluded.installed_at,
			acknowledged_at     = excluded.acknowledged_at
	`, rec.FPPInstanceUUID, rec.PackageID, rec.Revision, rec.VerificationResult,
		timeToDB(rec.InstalledAt), timeToDB(rec.AcknowledgedAt)); err != nil {
		return fmt.Errorf("store: put fallback program ack %q: %w", rec.FPPInstanceUUID, err)
	}
	return nil
}

// PutFallbackProgramAck upserts instanceUUID's fallback-program
// acknowledgement: one row per host, wholesale replacement, matching
// [Store.PutNodeCueCatalogAck]'s identical "no partial state" posture.
func (s *Store) PutFallbackProgramAck(ctx context.Context, rec FallbackProgramAckRecord) error {
	guardNotInTx(ctx, "Store.PutFallbackProgramAck")
	return putFallbackProgramAck(ctx, s.db, rec)
}

// PutFallbackProgramAck is [Store.PutFallbackProgramAck]'s [Tx] form.
func (t *Tx) PutFallbackProgramAck(ctx context.Context, rec FallbackProgramAckRecord) error {
	return putFallbackProgramAck(ctx, t.tx, rec)
}

func scanFallbackProgramAck(row interface{ Scan(dest ...any) error }) (FallbackProgramAckRecord, error) {
	var (
		rec            FallbackProgramAckRecord
		installedAt    string
		acknowledgedAt string
	)
	if err := row.Scan(&rec.FPPInstanceUUID, &rec.PackageID, &rec.Revision, &rec.VerificationResult, &installedAt, &acknowledgedAt); err != nil {
		return FallbackProgramAckRecord{}, err
	}
	var err error
	if rec.InstalledAt, err = dbToTime(installedAt); err != nil {
		return FallbackProgramAckRecord{}, fmt.Errorf("store: parse fallback program ack installed_at: %w", err)
	}
	if rec.AcknowledgedAt, err = dbToTime(acknowledgedAt); err != nil {
		return FallbackProgramAckRecord{}, fmt.Errorf("store: parse fallback program ack acknowledged_at: %w", err)
	}
	return rec, nil
}

func getFallbackProgramAck(ctx context.Context, q querier, instanceUUID string) (FallbackProgramAckRecord, error) {
	row := q.QueryRowContext(ctx, `
		SELECT fpp_instance_uuid, package_id, revision, verification_result, installed_at, acknowledged_at
		FROM fallback_program_acknowledgements WHERE fpp_instance_uuid = ?
	`, instanceUUID)
	rec, err := scanFallbackProgramAck(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FallbackProgramAckRecord{}, ErrFallbackProgramAckNotFound
	}
	if err != nil {
		return FallbackProgramAckRecord{}, fmt.Errorf("store: get fallback program ack %q: %w", instanceUUID, err)
	}
	return rec, nil
}

// GetFallbackProgramAck returns instanceUUID's last fallback-program
// acknowledgement, or [ErrFallbackProgramAckNotFound].
func (s *Store) GetFallbackProgramAck(ctx context.Context, instanceUUID string) (FallbackProgramAckRecord, error) {
	guardNotInTx(ctx, "Store.GetFallbackProgramAck")
	return getFallbackProgramAck(ctx, s.db, instanceUUID)
}

// GetFallbackProgramAck is [Store.GetFallbackProgramAck]'s [Tx] form.
func (t *Tx) GetFallbackProgramAck(ctx context.Context, instanceUUID string) (FallbackProgramAckRecord, error) {
	return getFallbackProgramAck(ctx, t.tx, instanceUUID)
}

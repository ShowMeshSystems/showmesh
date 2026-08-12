package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds schemaV6's commands repository methods (Step 7 seam 0;
// the storage half of ARCHITECTURE §8.1's command envelope). It knows
// nothing about pkg/command's Go types (that package, and the FPP command
// client, are seam C's) — CommandRecord is this package's own
// store-shaped record, matching the split this package already draws
// between e.g. store.HelloRecord and whatever internal/coordinator/
// inventory builds from it.

// CommandRecord is one row of the commands table. IdempotencyKey is
// UNIQUE (schemaV6) and is ARCHITECTURE §8.1's required-on-every-command
// field; see [Store.InsertCommand] for why replay detection is that
// constraint, never a SELECT followed by an INSERT. DeadlineAt,
// DispatchedAt, and ResolvedAt are all nullable: nil means exactly what it
// says (no deadline was set; not yet dispatched; not yet resolved), never
// a zero time standing in for "unknown" — this package's standing rule on
// nullable evidence timestamps, applied to command lifecycle instead of
// telemetry. OutcomeState uses pkg/observation's state vocabulary,
// matching audit_log.outcome_state (schemaV5): an unresolved command
// carries a state and a reason, never a null that renders as blank.
type CommandRecord struct {
	ID                  string
	IdempotencyKey      string
	Action              string
	TargetKind          string
	TargetID            string
	ParamsJSON          string
	IssuerPrincipalID   string
	IssuerPrincipalName string
	RequestedRevision   string
	ConfirmationMethod  string
	DeadlineAt          *time.Time
	CreatedAt           time.Time
	DispatchedAt        *time.Time
	ResolvedAt          *time.Time
	State               string
	ResultJSON          string
	OutcomeState        string
	OutcomeReason       string
}

// ErrCommandNotFound is returned by [Store.GetCommand]/[Tx.GetCommand] and
// [Store.GetCommandByIdempotencyKey]/[Tx.GetCommandByIdempotencyKey] when
// no matching row exists.
var ErrCommandNotFound = errors.New("store: command not found")

// ErrCommandIdempotencyKeyExists is the [errors.Is] sentinel for a
// duplicate idempotency_key — see [DuplicateCommandError], which wraps it
// with the pre-existing row. ARCHITECTURE §8.1 requires an idempotency key
// on every command; replay detection MUST be this UNIQUE constraint
// violation, never a SELECT-then-INSERT, because a read-then-write is a
// race by construction and this project has already shipped one test that
// passed or failed on scheduling rather than on correctness (CLAUDE.md).
// SQLite's own single-writer serialization (this package's connection
// pool is capped at 1 — see store.go's open()) is what makes this
// constraint the actual, race-free source of truth for two concurrent
// inserts of the same key: exactly one wins the INSERT, and the other
// observes this error.
//
// F10 caveat this file's retention (see retention.go's pruneCommands)
// introduces and this comment previously did not name: once pruneCommands
// deletes a command row, its idempotency_key is no longer present in the
// table and is therefore re-insertable — a caller replaying that exact
// key after the original row has aged out (or been evicted by the row-count
// bound) is accepted as a NEW command rather than caught as a replay. This
// is harmless at the default bound (DefaultMaxCommandAge/
// DefaultMaxCommandRows in retention.go: 180 days / 200,000 rows — a
// replay arriving that much later than the original is operationally
// indistinguishable from a fresh request anyway), but it is a real,
// unrecorded-until-now exception to "the UNIQUE constraint is the
// race-free source of truth for replay detection": that claim is true only
// for keys still within the retention window, not forever.
var ErrCommandIdempotencyKeyExists = errors.New("store: a command with this idempotency key already exists")

// DuplicateCommandError wraps [ErrCommandIdempotencyKeyExists] with the
// pre-existing [CommandRecord] that owns the idempotency key, so a caller
// (seam C) can return the original result and write its own replay audit
// entry without a second round trip to look the row up.
type DuplicateCommandError struct {
	Existing CommandRecord
}

func (e *DuplicateCommandError) Error() string {
	return fmt.Sprintf("store: command with idempotency key %q already exists (id %q)", e.Existing.IdempotencyKey, e.Existing.ID)
}

// Unwrap makes errors.Is(err, ErrCommandIdempotencyKeyExists) true for any
// *DuplicateCommandError, so a caller that only wants to detect the
// condition does not have to type-assert.
func (e *DuplicateCommandError) Unwrap() error { return ErrCommandIdempotencyKeyExists }

const commandColumns = `
	id, idempotency_key, action, target_kind, target_id, params_json,
	issuer_principal_id, issuer_principal_name, requested_revision, confirmation_method,
	deadline_at, created_at, dispatched_at, resolved_at, state, result_json,
	outcome_state, outcome_reason
`

func scanCommand(row interface{ Scan(dest ...any) error }) (CommandRecord, error) {
	var (
		rec                                  CommandRecord
		deadlineAt, dispatchedAt, resolvedAt sql.NullString
		createdAt                            string
	)
	if err := row.Scan(
		&rec.ID, &rec.IdempotencyKey, &rec.Action, &rec.TargetKind, &rec.TargetID, &rec.ParamsJSON,
		&rec.IssuerPrincipalID, &rec.IssuerPrincipalName, &rec.RequestedRevision, &rec.ConfirmationMethod,
		&deadlineAt, &createdAt, &dispatchedAt, &resolvedAt, &rec.State, &rec.ResultJSON,
		&rec.OutcomeState, &rec.OutcomeReason,
	); err != nil {
		return CommandRecord{}, err
	}
	var err error
	if rec.DeadlineAt, err = dbToTimePtr(deadlineAt); err != nil {
		return CommandRecord{}, fmt.Errorf("store: parse command deadline_at: %w", err)
	}
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return CommandRecord{}, fmt.Errorf("store: parse command created_at: %w", err)
	}
	if rec.DispatchedAt, err = dbToTimePtr(dispatchedAt); err != nil {
		return CommandRecord{}, fmt.Errorf("store: parse command dispatched_at: %w", err)
	}
	if rec.ResolvedAt, err = dbToTimePtr(resolvedAt); err != nil {
		return CommandRecord{}, fmt.Errorf("store: parse command resolved_at: %w", err)
	}
	return rec, nil
}

func getCommandByIdempotencyKey(ctx context.Context, q querier, key string) (CommandRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+commandColumns+`FROM commands WHERE idempotency_key = ?`, key)
	rec, err := scanCommand(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandRecord{}, ErrCommandNotFound
	}
	if err != nil {
		return CommandRecord{}, fmt.Errorf("store: get command by idempotency key: %w", err)
	}
	return rec, nil
}

func insertCommand(ctx context.Context, q querier, s *Store, rec CommandRecord, now time.Time) (CommandRecord, error) {
	rec.CreatedAt = now
	if rec.State == "" {
		return CommandRecord{}, fmt.Errorf("store: insert command %q: State is empty", rec.ID)
	}
	if rec.ResultJSON == "" {
		rec.ResultJSON = "{}"
	}
	if rec.ParamsJSON == "" {
		rec.ParamsJSON = "{}"
	}

	_, err := q.ExecContext(ctx, `
		INSERT INTO commands (
			id, idempotency_key, action, target_kind, target_id, params_json,
			issuer_principal_id, issuer_principal_name, requested_revision, confirmation_method,
			deadline_at, created_at, dispatched_at, resolved_at, state, result_json,
			outcome_state, outcome_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?, ?)
	`,
		rec.ID, rec.IdempotencyKey, rec.Action, rec.TargetKind, rec.TargetID, rec.ParamsJSON,
		rec.IssuerPrincipalID, rec.IssuerPrincipalName, rec.RequestedRevision, rec.ConfirmationMethod,
		timePtrToDB(rec.DeadlineAt), timeToDB(rec.CreatedAt), rec.State, rec.ResultJSON,
		rec.OutcomeState, rec.OutcomeReason,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			existing, gerr := getCommandByIdempotencyKey(ctx, q, rec.IdempotencyKey)
			if gerr != nil {
				return CommandRecord{}, fmt.Errorf("store: insert command: idempotency key %q already exists but could not be re-read: %w", rec.IdempotencyKey, gerr)
			}
			return CommandRecord{}, &DuplicateCommandError{Existing: existing}
		}
		return CommandRecord{}, fmt.Errorf("store: insert command %q: %w", rec.ID, err)
	}

	// Same two independent triggers as [appendAuditEntry] (audit.go): insert
	// volume and elapsed wall-clock time since the last prune pass. See
	// retention.go's pruneEveryNCommands/pruneCheckInterval doc comments.
	byCount := s.commandInsertCount.Add(1)%pruneEveryNCommands == 0
	byAge := false
	if !byCount {
		last := s.lastCommandPruneAtNanos.Load()
		byAge = last == 0 || s.now().Sub(time.Unix(0, last)) >= pruneCheckInterval
	}
	if byCount || byAge {
		if err := s.pruneCommands(ctx, q); err != nil {
			return CommandRecord{}, fmt.Errorf("store: insert command %q: %w", rec.ID, err)
		}
		s.lastCommandPruneAtNanos.Store(s.now().UnixNano())
	}

	// The INSERT above hardcodes dispatched_at/resolved_at to NULL — see
	// [Store.InsertCommand]'s doc comment — but rec, at this point, is
	// still whatever the caller passed in on those two fields, unchanged.
	// Returning rec as-is would hand back a record that CONTRADICTS the
	// row it just wrote: a caller passing a non-nil DispatchedAt (a replay
	// of an already-dispatched command, or simply a caller that forgot the
	// contract) would receive a CommandRecord claiming it was already
	// dispatched when the stored row says otherwise — exactly the defect a
	// reviewer found by running this, not by reading it: seam C, taking
	// this returned record as the command's current state, would believe
	// a fresh insert had already been dispatched. Clear both explicitly so
	// the returned value can never disagree with the database.
	rec.DispatchedAt = nil
	rec.ResolvedAt = nil

	return rec, nil
}

// InsertCommand records a new command as ARCHITECTURE §8.1's envelope:
// dispatched_at and resolved_at always start nil regardless of what rec
// carries on input (a command is inserted before it is dispatched, per
// ADR-024 decision 11's write-before-dispatch rule for a command sent
// outward — see [identity.Service.WriteAudit]'s doc comment). On a
// duplicate IdempotencyKey, returns a *[DuplicateCommandError] wrapping
// [ErrCommandIdempotencyKeyExists] and carrying the pre-existing row —
// never a fabricated success and never an ambiguous generic error, so a
// caller (seam C) can return the original result and write its own replay
// audit entry rather than dispatching the command a second time.
func (s *Store) InsertCommand(ctx context.Context, rec CommandRecord) (CommandRecord, error) {
	guardNotInTx(ctx, "Store.InsertCommand")
	return insertCommand(ctx, s.db, s, rec, s.now())
}

// InsertCommand is [Store.InsertCommand]'s [Tx] form — needed because
// ADR-024 decision 11's write-before-dispatch rule for a command still
// wants that write's own audit entry (the dispatch entry) in the same
// transaction as the insert, exactly as the same-transaction rule requires
// for a coordinator-local change.
func (t *Tx) InsertCommand(ctx context.Context, rec CommandRecord) (CommandRecord, error) {
	return insertCommand(ctx, t.tx, t.s, rec, t.s.now())
}

func getCommand(ctx context.Context, q querier, id string) (CommandRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+commandColumns+`FROM commands WHERE id = ?`, id)
	rec, err := scanCommand(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandRecord{}, ErrCommandNotFound
	}
	if err != nil {
		return CommandRecord{}, fmt.Errorf("store: get command %q: %w", id, err)
	}
	return rec, nil
}

// GetCommand returns one command by its own id, or [ErrCommandNotFound].
func (s *Store) GetCommand(ctx context.Context, id string) (CommandRecord, error) {
	guardNotInTx(ctx, "Store.GetCommand")
	return getCommand(ctx, s.db, id)
}

// GetCommand is [Store.GetCommand]'s [Tx] form.
func (t *Tx) GetCommand(ctx context.Context, id string) (CommandRecord, error) {
	return getCommand(ctx, t.tx, id)
}

// GetCommandByIdempotencyKey returns the command that owns key, or
// [ErrCommandNotFound] if no command has ever used it.
func (s *Store) GetCommandByIdempotencyKey(ctx context.Context, key string) (CommandRecord, error) {
	guardNotInTx(ctx, "Store.GetCommandByIdempotencyKey")
	return getCommandByIdempotencyKey(ctx, s.db, key)
}

// GetCommandByIdempotencyKey is [Store.GetCommandByIdempotencyKey]'s [Tx] form.
func (t *Tx) GetCommandByIdempotencyKey(ctx context.Context, key string) (CommandRecord, error) {
	return getCommandByIdempotencyKey(ctx, t.tx, key)
}

// CommandOutcomeUpdate is [Store.UpdateCommandOutcome]'s input: every
// field a command's lifecycle mutates after insertion. Pointer fields left
// nil leave that column untouched — a dispatch-only update sets
// DispatchedAt and leaves ResolvedAt/State/etc. alone; a later resolution
// sets the rest.
type CommandOutcomeUpdate struct {
	DispatchedAt  *time.Time
	ResolvedAt    *time.Time
	State         *string
	ResultJSON    *string
	OutcomeState  *string
	OutcomeReason *string
}

func updateCommandOutcome(ctx context.Context, q querier, id string, upd CommandOutcomeUpdate) error {
	var (
		sets []string
		args []any
	)
	if upd.DispatchedAt != nil {
		sets = append(sets, "dispatched_at = ?")
		args = append(args, timeToDB(*upd.DispatchedAt))
	}
	if upd.ResolvedAt != nil {
		sets = append(sets, "resolved_at = ?")
		args = append(args, timeToDB(*upd.ResolvedAt))
	}
	if upd.State != nil {
		sets = append(sets, "state = ?")
		args = append(args, *upd.State)
	}
	if upd.ResultJSON != nil {
		sets = append(sets, "result_json = ?")
		args = append(args, *upd.ResultJSON)
	}
	if upd.OutcomeState != nil {
		sets = append(sets, "outcome_state = ?")
		args = append(args, *upd.OutcomeState)
	}
	if upd.OutcomeReason != nil {
		sets = append(sets, "outcome_reason = ?")
		args = append(args, *upd.OutcomeReason)
	}
	if len(sets) == 0 {
		return nil
	}
	query := "UPDATE commands SET "
	for i, s := range sets {
		if i > 0 {
			query += ", "
		}
		query += s
	}
	query += " WHERE id = ?"
	args = append(args, id)

	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: update command outcome %q: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: update command outcome %q: %w", id, ErrCommandNotFound)
	}
	return nil
}

// UpdateCommandOutcome applies a partial update to an existing command
// row — see [CommandOutcomeUpdate]. ADR-024 decision 11 requires dispatch
// and outcome to be recorded as separate, append-only AUDIT entries
// correlated by command id (audit_log, schemaV5); this method is the
// command JOURNAL's own lifecycle bookkeeping, a different table with a
// different, deliberately mutable shape — see this file's doc comment on
// CommandRecord for why dispatched_at/resolved_at/state/outcome_* are the
// only columns a command's own row ever has updated after insertion.
func (s *Store) UpdateCommandOutcome(ctx context.Context, id string, upd CommandOutcomeUpdate) error {
	guardNotInTx(ctx, "Store.UpdateCommandOutcome")
	return updateCommandOutcome(ctx, s.db, id, upd)
}

// UpdateCommandOutcome is [Store.UpdateCommandOutcome]'s [Tx] form.
func (t *Tx) UpdateCommandOutcome(ctx context.Context, id string, upd CommandOutcomeUpdate) error {
	return updateCommandOutcome(ctx, t.tx, id, upd)
}

// DefaultCommandPageSize and MaxCommandPageSize bound [Store.ListCommands]'s
// limit parameter, mirroring [DefaultEventsPageSize]/[MaxEventsPageSize].
const (
	DefaultCommandPageSize = 100
	MaxCommandPageSize     = 500
)

func listCommands(ctx context.Context, q querier, limit int) ([]CommandRecord, error) {
	switch {
	case limit <= 0:
		limit = DefaultCommandPageSize
	case limit > MaxCommandPageSize:
		limit = MaxCommandPageSize
	}
	rows, err := q.QueryContext(ctx, `SELECT`+commandColumns+`FROM commands ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list commands: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []CommandRecord
	for rows.Next() {
		rec, err := scanCommand(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list commands: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list commands: %w", err)
	}
	return out, nil
}

// ListCommands returns the most recently created commands, newest first,
// capped at limit (limit <= 0 defaults to [DefaultCommandPageSize];
// anything above [MaxCommandPageSize] is clamped down to it).
func (s *Store) ListCommands(ctx context.Context, limit int) ([]CommandRecord, error) {
	guardNotInTx(ctx, "Store.ListCommands")
	return listCommands(ctx, s.db, limit)
}

// ListCommands is [Store.ListCommands]'s [Tx] form.
func (t *Tx) ListCommands(ctx context.Context, limit int) ([]CommandRecord, error) {
	return listCommands(ctx, t.tx, limit)
}

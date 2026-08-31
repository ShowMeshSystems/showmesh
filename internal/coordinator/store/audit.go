package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds audit_log's repository methods. See migrations.go's
// schemaV5 doc comment, rule 3: no method in this file ever issues an
// UPDATE against audit_log. [appendAuditEntry] is the only write path
// other than pruneAudit's bounded DELETE, and there is no UpdateAuditEntry,
// no SetAuditOutcome-style mutator, and there must never be one — ADR-024
// decision 11 requires dispatch and outcome to be separate, append-only
// entries correlated by CommandID for exactly this reason.

// AuditRecord is one row of the append-only audit_log table. It mirrors
// the identity package's AuditEntry field-for-field; see that type's doc
// comment for what each field means and identity/audit.go for the
// identity <-> store conversion, which is the only place these two types
// meet.
type AuditRecord struct {
	// ID is assigned by [Store.AppendAuditEntry]/[Tx.AppendAuditEntry]; a
	// caller-set value is ignored on input, matching [EventRecord.Seq]'s
	// convention.
	ID int64

	// RecordedAt, on OUTPUT (a row read back from the table), is when the
	// insert actually happened.
	//
	// On INPUT this field is HONORED, unlike [EventRecord.RecordedAt]'s
	// identical-looking field, which really is caller-ignored — the two
	// diverge deliberately. A non-zero value is stored verbatim; a zero
	// value (every caller that does not set it — [appendAuditEntry] itself
	// checks with time.Time.IsZero) falls back to this Store's own clock,
	// exactly as before. Step 7 seam A review defect 5: every production
	// caller of [identity.Service.WriteAudit]/[AuditedWrite] already sets
	// [identity.AuditEntry.Timestamp] to a request-scoped "now" specifically
	// because a security record's timestamp is meant to mean something, and
	// before this fix that value was silently discarded on the way into
	// this table — a field every caller could see and set, with no effect,
	// is a trap for the next caller that genuinely needs a non-now
	// timestamp (e.g. a replayed or backfilled entry).
	//
	// [EventRecord.RecordedAt] is deliberately NOT changed by this fix: an
	// event is this store's own observation of when a fact became known,
	// which by definition cannot predate the write, while an audit entry
	// attributes an action to a principal at a moment the CALLER is often
	// better positioned to state than "whenever this INSERT happened to
	// run".
	RecordedAt time.Time

	PrincipalID    string
	PrincipalName  string
	Form           string
	CredentialID   string
	ClientAddr     string
	Action         string
	Target         string
	ParamsJSON     string // a JSON object; "{}" when there are no params
	IdempotencyKey string
	Kind           string
	CommandID      string
	Outcome        string
	OutcomeState   string
	OutcomeReason  string
}

// appendAuditEntry is the shared body behind [Store.AppendAuditEntry] and
// [Tx.AppendAuditEntry]: validate rec, insert it, and run pruneAudit's
// amortized on-insert check — written once against [querier] rather than
// twice (once for *Store's own *sql.DB, once for an already-open [Tx]'s
// *sql.Tx), per this file's — and store/tx.go's — standing rule against a
// second, hand-copied INSERT that can silently stop agreeing with the
// first. q is whichever connection the caller is writing through; s is
// only used for its clock and its retention counters/bounds, never for a
// second connection — appending through s.db here (instead of q) would
// silently defeat the whole point of a caller passing a [Tx] in.
func appendAuditEntry(ctx context.Context, q querier, s *Store, rec AuditRecord) (int64, error) {
	id, err := insertAuditRow(ctx, q, s, rec)
	if err != nil {
		return 0, err
	}

	// Same two independent triggers as AppendEvent (events.go): insert
	// volume, which alone bounds row count correctly, and elapsed
	// wall-clock time since the last prune pass, which alone bounds age
	// correctly under a low write rate. See pruneEveryNAuditEntries and
	// pruneCheckInterval's (shared with events) doc comments in
	// retention.go for why both are needed rather than either alone.
	//
	// auditAppendCount and lastAuditPruneAtNanos are process-wide,
	// in-memory, and NOT part of q's transaction: a caller whose
	// transaction later rolls back (review round 5 finding 2:
	// [Store.ProbeAuditWrite] always does) cannot undo either one. That
	// is exactly why the probe calls [insertAuditRow] directly instead of
	// this function: never move this bookkeeping to run before q's
	// caller has committed, and never let the probe reach it.
	byCount := s.auditAppendCount.Add(1)%pruneEveryNAuditEntries == 0
	byAge := false
	if !byCount {
		last := s.lastAuditPruneAtNanos.Load()
		byAge = last == 0 || s.now().Sub(time.Unix(0, last)) >= pruneCheckInterval
	}
	if byCount || byAge {
		if err := s.pruneAudit(ctx, q); err != nil {
			return 0, fmt.Errorf("store: append audit entry: %w", err)
		}
		s.lastAuditPruneAtNanos.Store(s.now().UnixNano())
	}

	return id, nil
}

// insertAuditRow is appendAuditEntry's own INSERT statement, factored out
// so [Store.ProbeAuditWrite] can exercise the identical write production
// uses without appendAuditEntry's retention bookkeeping riding along -
// see appendAuditEntry's own doc comment on auditAppendCount/
// lastAuditPruneAtNanos for why that bookkeeping cannot tolerate a
// caller whose transaction rolls back.
func insertAuditRow(ctx context.Context, q querier, s *Store, rec AuditRecord) (int64, error) {
	if rec.Kind == "" {
		return 0, fmt.Errorf("store: append audit entry: Kind is empty")
	}
	if rec.Action == "" {
		return 0, fmt.Errorf("store: append audit entry: Action is empty")
	}
	params := rec.ParamsJSON
	if params == "" {
		params = "{}"
	}

	// Step 7 seam A review defect 5: rec.RecordedAt is honored when the
	// caller set one (identity.AuditEntry.Timestamp, threaded through by
	// identity/audit.go), and falls back to this Store's own clock only
	// when it is the zero value — see AuditRecord.RecordedAt's doc
	// comment for why this diverges from EventRecord's identical-looking,
	// genuinely-caller-ignored field.
	recordedAt := s.now()
	if !rec.RecordedAt.IsZero() {
		recordedAt = rec.RecordedAt
	}

	res, err := q.ExecContext(ctx, `
		INSERT INTO audit_log (
			recorded_at, principal_id, principal_name, form, credential_id,
			client_addr, action, target, params_json, idempotency_key,
			kind, command_id, outcome, outcome_state, outcome_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		timeToDB(recordedAt), rec.PrincipalID, rec.PrincipalName, rec.Form, rec.CredentialID,
		rec.ClientAddr, rec.Action, rec.Target, params, rec.IdempotencyKey,
		rec.Kind, rec.CommandID, rec.Outcome, rec.OutcomeState, rec.OutcomeReason,
	)
	if err != nil {
		return 0, fmt.Errorf("store: append audit entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: append audit entry: read assigned id: %w", err)
	}
	return id, nil
}

// AppendAuditEntry validates the minimum shape (Kind and Action are always
// required; every other field may be empty, since e.g. an auth_failure
// entry has no PrincipalID yet) and records rec as the next append-only
// entry, returning its assigned ID.
//
// The insert and the amortized pruning pass it may trigger (see
// [pruneAudit] below) share one transaction, exactly matching
// [Store.AppendEvent]'s reasoning in events.go: either both happen, or on
// any error neither does — never a partial state where an audit row was
// written but a failed prune silently never ran. A caller that also needs
// this insert to share a transaction with a DIFFERENT state change (ADR-024
// decision 11's same-transaction rule) wants [Tx.AppendAuditEntry] instead,
// reached through [Store.InTx] or [identity.Service.AuditedWrite] — this
// method always opens and commits its own transaction, scoped to the audit
// write alone.
func (s *Store) AppendAuditEntry(ctx context.Context, rec AuditRecord) (int64, error) {
	guardNotInTx(ctx, "Store.AppendAuditEntry")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin append audit entry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	id, err := appendAuditEntry(ctx, tx, s, rec)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit append audit entry: %w", err)
	}
	return id, nil
}

// AppendAuditEntry is [Store.AppendAuditEntry]'s [Tx] form: the identical
// insert-plus-prune body, run against this already-open transaction
// instead of opening a new one — so this write commits or rolls back
// together with whatever state change t's caller composed it with. See
// store/tx.go's [Tx] doc comment.
func (t *Tx) AppendAuditEntry(ctx context.Context, rec AuditRecord) (int64, error) {
	return appendAuditEntry(ctx, t.tx, t.s, rec)
}

// errAuditProbeRollback is the sentinel [Store.ProbeAuditWrite]'s own
// InTx closure returns unconditionally after a successful insert,
// forcing InTx to roll back rather than commit: this probe must never
// leave a synthetic row in the append-only audit log.
var errAuditProbeRollback = errors.New("store: audit write probe (deliberately rolled back)")

// ProbeAuditWrite attempts a real INSERT into audit_log, inside a
// transaction it always rolls back, and reports whether that INSERT
// itself succeeded. Unlike [Store.Readiness]'s plain connection ping,
// this exercises the SAME INSERT statement [Store.AppendAuditEntry]/
// [Tx.AppendAuditEntry] run in production (via [insertAuditRow], the two
// paths' shared body), so it catches a failure mode a ping cannot: the
// connection is reachable and every other table can still be written,
// but this specific write fails (a full disk mid write, a corrupted
// index on this one table), which is ADR-024 decision 11's own named
// trigger for the condition [identity.Service.AuditWriteStatus] reports.
// Computed fresh on every call, never cached, matching this
// coordinator's own audioConfigPushStatus precedent (audiosettings.go,
// internal/coordinator/api) for a standing, request-time-computed health
// signal.
//
// Deliberately calls [insertAuditRow], never [Tx.AppendAuditEntry]:
// review round 5 finding 2. appendAuditEntry's retention bookkeeping
// (auditAppendCount, lastAuditPruneAtNanos) is process-wide, in-memory
// state a rolled-back transaction cannot undo. Going through
// AppendAuditEntry here would let every probe permanently consume a
// prune trigger while the prune itself (the DELETE, run inside this
// transaction) rolled back with it: the count trigger firing on
// probes that throw the prune away, and the age trigger defeated
// outright, since a probe would keep refreshing lastAuditPruneAtNanos
// after a prune that never happened. Net effect on a coordinator with
// an open dashboard (which polls this every 30s): audit_log grows
// unbounded, the exact failure this probe exists to help detect.
func (s *Store) ProbeAuditWrite(ctx context.Context) error {
	err := s.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, aerr := insertAuditRow(ctx, tx.tx, tx.s, AuditRecord{
			Kind: "probe", Action: "coordinator.audit.store.probe",
			OutcomeReason: "live audit-store write probe; always rolled back, never committed",
		}); aerr != nil {
			return aerr
		}
		return errAuditProbeRollback
	})
	if errors.Is(err, errAuditProbeRollback) {
		return nil
	}
	return err
}

const auditColumns = `
	id, recorded_at, principal_id, principal_name, form, credential_id,
	client_addr, action, target, params_json, idempotency_key,
	kind, command_id, outcome, outcome_state, outcome_reason
`

func scanAudit(row interface{ Scan(dest ...any) error }) (AuditRecord, error) {
	var (
		rec        AuditRecord
		recordedAt string
	)
	if err := row.Scan(
		&rec.ID, &recordedAt, &rec.PrincipalID, &rec.PrincipalName, &rec.Form, &rec.CredentialID,
		&rec.ClientAddr, &rec.Action, &rec.Target, &rec.ParamsJSON, &rec.IdempotencyKey,
		&rec.Kind, &rec.CommandID, &rec.Outcome, &rec.OutcomeState, &rec.OutcomeReason,
	); err != nil {
		return AuditRecord{}, err
	}
	var err error
	if rec.RecordedAt, err = dbToTime(recordedAt); err != nil {
		return AuditRecord{}, fmt.Errorf("store: parse audit recorded_at: %w", err)
	}
	return rec, nil
}

// DefaultAuditPageSize and MaxAuditPageSize bound [Store.ListAuditEntries]'s
// limit parameter, mirroring [DefaultEventsPageSize]/[MaxEventsPageSize] in
// events.go for the identical reason.
const (
	DefaultAuditPageSize = 100
	MaxAuditPageSize     = 500
)

// AuditKindDispatch mirrors identity.AuditKind's "dispatch" member by
// value. This package cannot import internal/coordinator/identity to reuse
// that constant directly — identity already imports store (identity/audit.go
// is the one place the two types meet) — so, exactly like
// [MacroRunStepRecord]'s own "not validated by this package" columns, the
// wire value is duplicated here rather than shared. Used by
// [Store.FindAuditDispatchEntry].
const AuditKindDispatch = "dispatch"

// FindAuditDispatchEntry returns the earliest "dispatch"-kind audit_log row
// recorded under idempotencyKey, and true — or a zero [AuditRecord] and
// false if none exists (never an error for "not found"; a real lookup
// failure is returned as err).
//
// This exists for the macro startup reconciler (internal/coordinator/macro,
// ADR-031 decision 4): an MQTT-integration step has no commands-table row
// the way an FPP step does, so a dispatch audit entry — written before the
// publish attempt, by [identity.Service.WriteAudit] — is the only durable
// evidence this coordinator ever records that a given step's dispatch
// began before a prior process stopped existing. Its presence does not
// prove the publish itself reached the broker (a crash can land between
// the audit write and the call to publish), only that the coordinator
// started that step; the reconciler states that distinction honestly
// rather than treating the entry's presence as confirmation.
func (s *Store) FindAuditDispatchEntry(ctx context.Context, idempotencyKey string) (AuditRecord, bool, error) {
	guardNotInTx(ctx, "Store.FindAuditDispatchEntry")
	row := s.db.QueryRowContext(ctx, `
		SELECT`+auditColumns+`
		FROM audit_log WHERE idempotency_key = ? AND kind = ? ORDER BY id ASC LIMIT 1
	`, idempotencyKey, AuditKindDispatch)
	rec, err := scanAudit(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditRecord{}, false, nil
	}
	if err != nil {
		return AuditRecord{}, false, fmt.Errorf("store: find audit dispatch entry: %w", err)
	}
	return rec, true, nil
}

// ListAuditEntries returns audit entries with id > since, ordered
// ascending, capped at limit (limit <= 0 defaults to
// [DefaultAuditPageSize]; anything above [MaxAuditPageSize] is clamped
// down to it). Unlike [Store.ListEvents], this reports no gap flag: the
// audit API this backs (`/api/v1/audit`, behind audit:read) is a
// paginated investigation tool, not a change-stream resumption cursor —
// nothing in ADR-024 promises a caller "no gap and no duplicate" across a
// prune the way ADR-020's events contract does for since. A caller that
// cares whether retention has already trimmed part of its requested range
// can compare its since against [Store.OldestAuditID].
func (s *Store) ListAuditEntries(ctx context.Context, since int64, limit int) ([]AuditRecord, error) {
	guardNotInTx(ctx, "Store.ListAuditEntries")
	if since < 0 {
		return nil, fmt.Errorf("store: list audit entries: since must be >= 0, got %d", since)
	}
	switch {
	case limit <= 0:
		limit = DefaultAuditPageSize
	case limit > MaxAuditPageSize:
		limit = MaxAuditPageSize
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT`+auditColumns+`
		FROM audit_log WHERE id > ? ORDER BY id ASC LIMIT ?
	`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AuditRecord
	for rows.Next() {
		rec, err := scanAudit(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list audit entries: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list audit entries: %w", err)
	}
	return out, nil
}

// ListAuditEntriesNewestFirst returns audit entries with id < before,
// ordered DESCENDING, capped at limit (the same defaulting and clamping
// [Store.ListAuditEntries] applies). before <= 0 means "start at the
// newest retained entry", which is the point of this method: an operator
// opening the audit log wants the most recent activity in one query, not
// after walking every retained row from the oldest one.
//
// The exact mirror of [Store.ListAuditEntries]: that one takes an
// exclusive LOWER bound and walks forward, this one takes an exclusive
// UPPER bound and walks backward, and both bounds are the same append-only
// row id (AUTOINCREMENT, never reused). A caller walking backward has
// reached the true beginning of retained history when its last returned id
// equals [Store.OldestAuditID]; a short page alone does not prove it,
// because retention can trim below the cursor between two pages.
//
// Read-only, like every other read here: see this file's header rule
// against a second write or mutate path against audit_log.
func (s *Store) ListAuditEntriesNewestFirst(ctx context.Context, before int64, limit int) ([]AuditRecord, error) {
	guardNotInTx(ctx, "Store.ListAuditEntriesNewestFirst")
	if before < 0 {
		return nil, fmt.Errorf("store: list audit entries newest first: before must be >= 0, got %d", before)
	}
	switch {
	case limit <= 0:
		limit = DefaultAuditPageSize
	case limit > MaxAuditPageSize:
		limit = MaxAuditPageSize
	}

	query := `
		SELECT` + auditColumns + `
		FROM audit_log ORDER BY id DESC LIMIT ?
	`
	args := []any{limit}
	if before > 0 {
		query = `
			SELECT` + auditColumns + `
			FROM audit_log WHERE id < ? ORDER BY id DESC LIMIT ?
		`
		args = []any{before, limit}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit entries newest first: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AuditRecord
	for rows.Next() {
		rec, err := scanAudit(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list audit entries newest first: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list audit entries newest first: %w", err)
	}
	return out, nil
}

// OldestAuditID returns the lowest id currently retained in audit_log, and
// true — or (0, false, nil) if the table currently retains no row.
// Mirrors [Store.OldestEventSeq] exactly; see that method's doc comment.
func (s *Store) OldestAuditID(ctx context.Context) (int64, bool, error) {
	guardNotInTx(ctx, "Store.OldestAuditID")
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(id) FROM audit_log`).Scan(&oldest); err != nil {
		return 0, false, fmt.Errorf("store: oldest audit id: %w", err)
	}
	if !oldest.Valid {
		return 0, false, nil
	}
	return oldest.Int64, true, nil
}

// pruneEveryNAuditEntries mirrors [pruneEveryNEvents] in retention.go,
// applied to audit_log; see that constant's doc comment for the tradeoff
// it encodes. 100 is kept identical to the events value deliberately —
// neither number is derived from a measured write rate for either table,
// so there is no basis yet for picking a different one.
const pruneEveryNAuditEntries = 100

// pruneAudit deletes audit entries older than maxAuditAge (if positive)
// and, of whatever remains, all but the newest maxAuditRows. Always called
// from inside the same connection as the write that triggered it (see
// [appendAuditEntry]) — q is either *Store.db's own owned transaction
// (from [Store.AppendAuditEntry]) or an already-open [Tx]'s *sql.Tx (from
// [Tx.AppendAuditEntry]); either way the delete lands in the same
// transaction as the insert that triggered it, mirroring [pruneEvents]
// exactly, including which bound is allowed to be disabled (age, via
// [WithMaxAuditAge] with a non-positive duration) and which is not (row
// count) — see those two Options' doc comments in retention.go.
func (s *Store) pruneAudit(ctx context.Context, q querier) error {
	if s.maxAuditAge > 0 {
		cutoff := timeToDB(s.now().Add(-s.maxAuditAge))
		if _, err := q.ExecContext(ctx, `DELETE FROM audit_log WHERE recorded_at < ?`, cutoff); err != nil {
			return fmt.Errorf("store: prune audit by age: %w", err)
		}
	}
	if s.maxAuditRows > 0 {
		if _, err := q.ExecContext(ctx, `
			DELETE FROM audit_log WHERE id NOT IN (
				SELECT id FROM audit_log ORDER BY id DESC LIMIT ?
			)
		`, s.maxAuditRows); err != nil {
			return fmt.Errorf("store: prune audit by row count: %w", err)
		}
	}
	return nil
}

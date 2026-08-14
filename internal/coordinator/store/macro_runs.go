package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file holds schemaV7's macro_runs/macro_run_steps repository methods
// (Step 9 Wave 1a; ADR-031, the macro execution model record). It knows
// nothing about what an action or a macro's payload_json actually
// contains, how a step is dispatched, or what "confirmed" means for any
// particular integration — that is Wave 1b's (internal/coordinator/api's
// dispatch seam) and Wave 2's (internal/coordinator/macro, the executor)
// job. This file only ever stores a run's shape and outcome, exactly the
// split commands.go already draws between the command journal's storage
// and seam C's dispatch/confirmation logic.
//
// See migrations.go's schemaV7 doc comment for the schema-level reasoning
// this file's methods depend on: why completed/confirmed are nullable and
// never collapsed (ADR-031 decision 3), why macro_revision/action_revision
// are pinned at submission and never re-read from config_objects, why the
// overlap guard (decision 6) is a read-then-conditionally-write inside one
// transaction rather than a second UNIQUE index, and why command_id is
// nullable rather than empty-string-sentineled.

// MacroRunRecord is one row of the macro_runs table.
//
// FinishedAt is nil while State == "running". Completed and Confirmed are
// both nil until FinishedAt is set — ADR-031 decision 3's "before a run
// finishes, neither is known" — and [Store.FinishMacroRun] is the only
// method that ever sets either, always both, always together. Reason is
// set whenever either is false (or, for a still-running run interrupted by
// a coordinator restart per ADR-031 decision 4, when the reconciler
// finishes it not-completed). AttributionDegraded starts false and is
// flipped by [Store.SetMacroRunAttributionDegraded] the moment ADR-031
// decision 5's cost is actually paid — see that method's doc comment for
// why this is tracked live rather than only decided at finish.
type MacroRunRecord struct {
	ID                  string
	MacroObjectID       string
	MacroRevision       int64
	Show                string
	Trigger             string // "api" | "plugin" | "cli" | "ui" — STEP-9-SPEC.md §6.1; not validated by this package.
	IssuerPrincipalID   string
	IssuerPrincipalName string
	IdempotencyKey      string
	CreatedAt           time.Time
	FinishedAt          *time.Time
	State               string // "running" | "finished" — not validated by this package.
	Completed           *bool  // nil until FinishedAt is set — see the type's doc comment.
	Confirmed           *bool  // nil until FinishedAt is set — see the type's doc comment.
	Reason              string
	AttributionDegraded bool
}

// MacroRunStepRecord is one row of the macro_run_steps table: one step of
// one run, at the ActionRevision pinned when the run was created.
// DispatchedAt and ResolvedAt are nil until the step reaches that point in
// its own lifecycle, matching [CommandRecord]'s identical nil-means-
// "not yet" contract in commands.go. CommandID is nil for a step that has
// not dispatched yet AND for an MQTT step, which never has one at all —
// see migrations.go's schemaV7 doc comment for why that ambiguity is
// deliberately left unresolved by this package (STEP-9-SPEC.md's §4 seam
// and §7 waiter, Wave 1b/1c, are what actually dispatch a step and are the
// only callers who know which case applies), and see
// [Store.ResolveMacroRunStepCommand] for the SEPARATE ambiguity a non-nil
// CommandID can still carry (a live command row versus one retention has
// since pruned), which this record's shape alone cannot answer.
//
// SafetyClass and LocalFallbackClass are the step's action's own required
// safetyClass (STEP-9-SPEC.md §5.3) and the step's own required
// localFallback.class (§5.4), both resolved and pinned at ActionRevision
// the moment [Store.CreateMacroRun] runs, never re-read later, for the
// identical reason ActionRevision itself is pinned. This package does not
// validate either against its documented enum (none/blackout/stop/powerOff
// for the former, none/coordinator-required/silence for the latter),
// matching Integration/State's existing "not validated by this package"
// precedent: the config write path (owned by Wave 2's config kinds) is
// where those enums are enforced, not storage.
//
// AttributionDegraded is this step's own half of ADR-031 decision 5,
// corrected 2026-08-14: the audit exemption is decided PER STEP, not once
// for the whole run, so a reader needs to know which step's dispatch
// actually paid decision 5's cost, not only that [MacroRunRecord]'s own
// AttributionDegraded was set somewhere in the run. Starts false; flipped
// permanently by [Store.SetMacroRunStepAttributionDegraded]; see that
// method's doc comment for why this is tracked independently of, not
// derived from, the run-level column.
type MacroRunStepRecord struct {
	RunID               string
	StepIndex           int
	StepID              string
	ActionObjectID      string
	ActionRevision      int64
	Integration         string // "fpp" | "mqtt" — STEP-9-SPEC.md §5.3; not validated by this package.
	SafetyClass         string // "none" | "blackout" | "stop" | "powerOff", STEP-9-SPEC.md §5.3; not validated by this package.
	LocalFallbackClass  string // "none" | "coordinator-required" | "silence", STEP-9-SPEC.md §5.4; not validated by this package.
	State               string
	DispatchedAt        *time.Time
	ResolvedAt          *time.Time
	Outcome             string // "" | "confirmed" | "unconfirmed" | "unconfirmable" | "failed" | "skipped" — STEP-9-SPEC.md §6.4; not validated by this package.
	OutcomeState        string
	OutcomeReason       string
	CommandID           *string
	AttributionDegraded bool
}

// MacroRunStepOutcomeStatePending and MacroRunStepOutcomeReasonPending are
// the OutcomeState/OutcomeReason value [Store.CreateMacroRun]/
// [Tx.CreateMacroRun] require every step to be created with (see
// createMacroRun's validation below) — the fix for STEP-9-SPEC review
// finding 8, "reconciliation leaves every unresolved step permanently
// blank." Before a step has ever been touched by
// [Store.UpdateMacroRunStepOutcome] or [Store.ResolveUnresolvedMacroRunSteps],
// this is what OutcomeState/OutcomeReason read as, so "" is never even the
// as-created value and a step interrupted before either of those runs
// still carries a stated state and a stated reason, never a blank that
// renders as fine — see migrations.go's schemaV7 doc comment for the
// defect this closes and [ReconcileStrandedFPPCommands]
// (internal/coordinator/api/fppcommand_reconcile.go) for the shape this
// mirrors one level up, at the command rather than the step.
//
// MacroRunStepOutcomeStatePending reuses [observation.StateNotCollected]
// rather than inventing a parallel vocabulary for "nothing has happened to
// this step yet": that state's own doc comment is "no attempt has been
// made," which is exactly true of a freshly created step, and
// fppcommand_reconcile.go already establishes the precedent of reusing it
// for the identical "never dispatched" case one layer down (see that
// file's DispatchedAt == nil branch). This package does not otherwise
// validate OutcomeState's vocabulary (see [MacroRunStepRecord]'s doc
// comment), so nothing stops a caller from writing a different value once
// the step actually resolves; this constant is only the required starting
// point, not an enforced invariant thereafter.
const (
	MacroRunStepOutcomeStatePending  = string(observation.StateNotCollected)
	MacroRunStepOutcomeReasonPending = "this step has not been dispatched or resolved yet"
)

// ErrMacroRunNotFound is returned by [Store.GetMacroRun]/[Tx.GetMacroRun],
// [Store.GetMacroRunByIdempotencyKey]/[Tx.GetMacroRunByIdempotencyKey], and
// [Store.FindRunningMacroRun]/[Tx.FindRunningMacroRun] when no matching row
// exists.
var ErrMacroRunNotFound = errors.New("store: macro run not found")

// ErrMacroRunStepNotFound is returned by
// [Store.UpdateMacroRunStepOutcome]/[Tx.UpdateMacroRunStepOutcome] when
// (runID, stepIndex) names no row.
var ErrMacroRunStepNotFound = errors.New("store: macro run step not found")

// ErrMacroRunIdempotencyKeyExists is the [errors.Is] sentinel for a
// duplicate macro_runs.idempotency_key that is a TRUE replay: same macro,
// same pinned revision. See [DuplicateMacroRunError], which wraps it with
// the pre-existing row. A key collision against a DIFFERENT macro or a
// DIFFERENT pinned revision of the same macro is NOT this sentinel; see
// [ErrMacroRunIdempotencyMacroMismatch] and
// [ErrMacroRunIdempotencyRevisionMismatch], STEP-9-SPEC.md §6.2's other two
// named outcomes, corrected 2026-08-14 into this package after the
// specification review found the original version collapsed all three into
// this one error. Mirrors [ErrCommandIdempotencyKeyExists] (commands.go)
// for the "the UNIQUE constraint is the actual race-free source of truth,
// never a SELECT-then-INSERT trusted alone" rule: [createMacroRun]'s
// idempotency-key pre-check exists to return the RIGHT one of the three
// outcomes economically in the common case, but the INSERT's own UNIQUE
// constraint is what this package actually depends on for correctness
// under real concurrency, and a violation reaching that constraint is
// classified identically to the pre-check finding one; see
// classifyMacroRunIdempotencyMatch, which both call sites share.
var ErrMacroRunIdempotencyKeyExists = errors.New("store: a macro run with this idempotency key already exists")

// ErrMacroRunIDExists is the [errors.Is] sentinel for a reused
// macro_runs.id PRIMARY KEY value, distinguished from
// [ErrMacroRunIdempotencyKeyExists] (STEP-9-SPEC review, Minor finding 1).
// Before this fix, createMacroRun's UNIQUE-constraint fallback treated ANY
// "UNIQUE constraint failed" on the macro_runs INSERT as an idempotency-key
// collision and unconditionally re-read the row by idempotency_key — id is
// also the table's PRIMARY KEY, and SQLite reports a PRIMARY KEY violation
// with the identical "UNIQUE constraint failed" text, so a caller that
// reused a run id (a UUID collision, or a bug generating ids) got
// re-queried by an idempotency_key that was never actually duplicated,
// which either found nothing (surfacing the misleading "idempotency key
// %q already exists but could not be re-read: ... macro run not found") or,
// worse, found and returned an unrelated existing run that happened to
// reuse that same key value by coincidence. See createMacroRun for how the
// two cases are now told apart before either re-read is attempted.
var ErrMacroRunIDExists = errors.New("store: a macro run with this id already exists")

// DuplicateMacroRunIDError wraps [ErrMacroRunIDExists] with the id that was
// reused, so a caller can log or report which run id collided without a
// second round trip. Unlike [DuplicateMacroRunError], this does NOT carry
// the pre-existing [MacroRunRecord]: a reused id is a caller bug (or an id
// generator collision), not a legitimate replay, so there is no "existing
// run" a caller should ever treat as the answer to its own submission —
// see this type's Unwrap for why errors.Is(err, ErrMacroRunIdempotencyKeyExists)
// is deliberately false for this error.
type DuplicateMacroRunIDError struct {
	ID string
}

func (e *DuplicateMacroRunIDError) Error() string {
	return fmt.Sprintf("store: macro run id %q already exists (this is a reused id, not an idempotency-key collision)", e.ID)
}

// Unwrap makes errors.Is(err, ErrMacroRunIDExists) true for any
// *DuplicateMacroRunIDError.
func (e *DuplicateMacroRunIDError) Unwrap() error { return ErrMacroRunIDExists }

// DuplicateMacroRunError wraps [ErrMacroRunIdempotencyKeyExists] with the
// pre-existing [MacroRunRecord] that owns the idempotency key, mirroring
// [DuplicateCommandError] exactly: a caller (the macro executor, Wave 2)
// returns the ORIGINAL run rather than dispatching a second one, whether
// that run is still running or long since finished — ADR-031's "idempotency
// keys are still required and still work" applies regardless of the
// existing run's current state.
type DuplicateMacroRunError struct {
	Existing MacroRunRecord
}

func (e *DuplicateMacroRunError) Error() string {
	return fmt.Sprintf("store: macro run with idempotency key %q already exists (id %q)", e.Existing.IdempotencyKey, e.Existing.ID)
}

// Unwrap makes errors.Is(err, ErrMacroRunIdempotencyKeyExists) true for any
// *DuplicateMacroRunError, so a caller that only wants to detect the
// condition does not have to type-assert.
func (e *DuplicateMacroRunError) Unwrap() error { return ErrMacroRunIdempotencyKeyExists }

// ErrMacroRunIdempotencyMacroMismatch is the [errors.Is] sentinel for
// STEP-9-SPEC.md §6.2's three-way idempotency-key replay rule, corrected
// 2026-08-14: the first version of this package treated ANY existing row
// with a matching idempotency_key as a replay and wrapped it in
// [DuplicateMacroRunError] regardless of what it actually named, which
// silently returned someone else's run to a caller who submitted the SAME
// key for a DIFFERENT macro. §6.2 requires that be a distinct, named
// conflict rather than a false replay: "Same key, different macro id: 409,
// a distinct problem type." See [MacroRunIdempotencyMacroMismatchError].
var ErrMacroRunIdempotencyMacroMismatch = errors.New("store: idempotency key already used for a different macro")

// MacroRunIdempotencyMacroMismatchError wraps
// [ErrMacroRunIdempotencyMacroMismatch] with the pre-existing run the
// caller's idempotency key actually belongs to, plus the macro object id
// the caller asked to run, so the caller (the macro executor, mapping this
// to a distinct 409 problem type per STEP-9-SPEC.md §6.2) never has to
// re-query to explain the mismatch.
type MacroRunIdempotencyMacroMismatchError struct {
	Existing               MacroRunRecord
	RequestedMacroObjectID string
}

func (e *MacroRunIdempotencyMacroMismatchError) Error() string {
	return fmt.Sprintf("store: idempotency key %q was already used for macro %q (run %q), not the requested macro %q",
		e.Existing.IdempotencyKey, e.Existing.MacroObjectID, e.Existing.ID, e.RequestedMacroObjectID)
}

// Unwrap makes errors.Is(err, ErrMacroRunIdempotencyMacroMismatch) true for
// any *MacroRunIdempotencyMacroMismatchError.
func (e *MacroRunIdempotencyMacroMismatchError) Unwrap() error {
	return ErrMacroRunIdempotencyMacroMismatch
}

// ErrMacroRunIdempotencyRevisionMismatch is the [errors.Is] sentinel for
// §6.2's OTHER new conflict case, distinct from both a true replay and
// [ErrMacroRunIdempotencyMacroMismatch]: "Same key, same macro, different
// pinned revision: 409, its own problem type, because the macro was edited
// between the two submissions and the caller asked for two different things
// under one key." See [MacroRunIdempotencyRevisionMismatchError].
var ErrMacroRunIdempotencyRevisionMismatch = errors.New("store: idempotency key already used for a different revision of this macro")

// MacroRunIdempotencyRevisionMismatchError wraps
// [ErrMacroRunIdempotencyRevisionMismatch] with the pre-existing run and the
// macro revision the caller's new submission actually pinned, so a caller
// can report both revisions in its 409 without a second read.
type MacroRunIdempotencyRevisionMismatchError struct {
	Existing               MacroRunRecord
	RequestedMacroRevision int64
}

func (e *MacroRunIdempotencyRevisionMismatchError) Error() string {
	return fmt.Sprintf("store: idempotency key %q was already used for macro %q at revision %d (run %q), not the requested revision %d",
		e.Existing.IdempotencyKey, e.Existing.MacroObjectID, e.Existing.MacroRevision, e.Existing.ID, e.RequestedMacroRevision)
}

// Unwrap makes errors.Is(err, ErrMacroRunIdempotencyRevisionMismatch) true
// for any *MacroRunIdempotencyRevisionMismatchError.
func (e *MacroRunIdempotencyRevisionMismatchError) Unwrap() error {
	return ErrMacroRunIdempotencyRevisionMismatch
}

// classifyMacroRunIdempotencyMatch is STEP-9-SPEC.md §6.2's three-way
// replay rule, applied once existing has already been found under
// requested's own idempotency key (by either createMacroRun's pre-check or
// its UNIQUE-constraint fallback, both call this identically, so the two
// paths can never disagree about which of the three cases applies):
//
//   - Same macro, same pinned revision: a true replay. Returns
//     *[DuplicateMacroRunError]; the caller returns the EXISTING run,
//     never dispatches a second one.
//   - Different macro: *[MacroRunIdempotencyMacroMismatchError].
//   - Same macro, different pinned revision: *[MacroRunIdempotencyRevisionMismatchError].
//
// A UNIQUE constraint on idempotency_key alone cannot express this: it can
// only say "this key is taken," never by which of the three cases, which
// is exactly the gap STEP-9-SPEC.md §6.2 named: "A unique-constraint
// violation is not a specified behaviour; it is whatever a builder maps it
// to." This function is that mapping, in one place, rather than left to
// whichever of the two call sites happens to run first.
func classifyMacroRunIdempotencyMatch(existing, requested MacroRunRecord) error {
	if existing.MacroObjectID != requested.MacroObjectID {
		return &MacroRunIdempotencyMacroMismatchError{Existing: existing, RequestedMacroObjectID: requested.MacroObjectID}
	}
	if existing.MacroRevision != requested.MacroRevision {
		return &MacroRunIdempotencyRevisionMismatchError{Existing: existing, RequestedMacroRevision: requested.MacroRevision}
	}
	return &DuplicateMacroRunError{Existing: existing}
}

// ErrMacroRunAlreadyInFlight is the [errors.Is] sentinel for ADR-031
// decision 6's overlap refusal — see [MacroRunAlreadyInFlightError], which
// wraps it with the in-flight run.
var ErrMacroRunAlreadyInFlight = errors.New("store: another run of this macro is already in flight")

// MacroRunAlreadyInFlightError wraps [ErrMacroRunAlreadyInFlight] with the
// [MacroRunRecord] that is currently state=="running" for the same
// macro_object_id as the rejected submission. A caller (the macro
// executor) uses InFlight.ID to name the in-flight run in its 409 response
// (STEP-9-SPEC.md §2.6: "refused with 409 naming the in-flight run's
// identifier"). See migrations.go's schemaV7 doc comment for why this
// guard is race-free without a second UNIQUE index, and why it is checked
// only AFTER the idempotency-key lookup finds nothing — a resubmission of
// an already-accepted key must return that run via
// [DuplicateMacroRunError], including a still-running one, never this
// error naming itself as "in flight."
type MacroRunAlreadyInFlightError struct {
	InFlight MacroRunRecord
}

func (e *MacroRunAlreadyInFlightError) Error() string {
	return fmt.Sprintf("store: macro %q already has a run in flight (id %q)", e.InFlight.MacroObjectID, e.InFlight.ID)
}

// Unwrap makes errors.Is(err, ErrMacroRunAlreadyInFlight) true for any
// *MacroRunAlreadyInFlightError.
func (e *MacroRunAlreadyInFlightError) Unwrap() error { return ErrMacroRunAlreadyInFlight }

const macroRunColumns = `
	id, macro_object_id, macro_revision, show, trigger,
	issuer_principal_id, issuer_principal_name, idempotency_key,
	created_at, finished_at, state, completed, confirmed, reason, attribution_degraded
`

func scanMacroRun(row interface{ Scan(dest ...any) error }) (MacroRunRecord, error) {
	var (
		rec                  MacroRunRecord
		createdAt            string
		finishedAt           sql.NullString
		completed, confirmed sql.NullInt64
		attributionDegraded  int64
	)
	if err := row.Scan(
		&rec.ID, &rec.MacroObjectID, &rec.MacroRevision, &rec.Show, &rec.Trigger,
		&rec.IssuerPrincipalID, &rec.IssuerPrincipalName, &rec.IdempotencyKey,
		&createdAt, &finishedAt, &rec.State, &completed, &confirmed, &rec.Reason, &attributionDegraded,
	); err != nil {
		return MacroRunRecord{}, err
	}
	var err error
	if rec.CreatedAt, err = dbToTime(createdAt); err != nil {
		return MacroRunRecord{}, fmt.Errorf("store: parse macro run created_at: %w", err)
	}
	if rec.FinishedAt, err = dbToTimePtr(finishedAt); err != nil {
		return MacroRunRecord{}, fmt.Errorf("store: parse macro run finished_at: %w", err)
	}
	rec.Completed = dbToBoolPtr(completed)
	rec.Confirmed = dbToBoolPtr(confirmed)
	rec.AttributionDegraded = attributionDegraded != 0
	return rec, nil
}

const macroRunStepColumns = `
	run_id, step_index, step_id, action_object_id, action_revision, integration,
	safety_class, local_fallback_class, state,
	dispatched_at, resolved_at, outcome, outcome_state, outcome_reason, command_id,
	attribution_degraded
`

func scanMacroRunStep(row interface{ Scan(dest ...any) error }) (MacroRunStepRecord, error) {
	var (
		rec                      MacroRunStepRecord
		dispatchedAt, resolvedAt sql.NullString
		commandID                sql.NullString
		attributionDegraded      int64
	)
	if err := row.Scan(
		&rec.RunID, &rec.StepIndex, &rec.StepID, &rec.ActionObjectID, &rec.ActionRevision, &rec.Integration,
		&rec.SafetyClass, &rec.LocalFallbackClass, &rec.State,
		&dispatchedAt, &resolvedAt, &rec.Outcome, &rec.OutcomeState, &rec.OutcomeReason, &commandID,
		&attributionDegraded,
	); err != nil {
		return MacroRunStepRecord{}, err
	}
	var err error
	if rec.DispatchedAt, err = dbToTimePtr(dispatchedAt); err != nil {
		return MacroRunStepRecord{}, fmt.Errorf("store: parse macro run step dispatched_at: %w", err)
	}
	if rec.ResolvedAt, err = dbToTimePtr(resolvedAt); err != nil {
		return MacroRunStepRecord{}, fmt.Errorf("store: parse macro run step resolved_at: %w", err)
	}
	rec.CommandID = dbToStringPtr(commandID)
	rec.AttributionDegraded = attributionDegraded != 0
	return rec, nil
}

// dbToBoolPtr converts a nullable INTEGER column into *bool: nil when the
// column was SQL NULL ("not yet known" — see [MacroRunRecord]'s doc
// comment), matching [dbToTimePtr]'s identical nil-means-unknown contract
// for timestamps, applied here to a boolean for the first time in this
// package.
func dbToBoolPtr(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Int64 != 0
	return &v
}

// boolPtrToDB is [dbToBoolPtr]'s inverse: nil binds as SQL NULL (which the
// driver accepts directly, matching [timePtrToDB]'s `any` return
// convention), a non-nil *bool binds as [boolToDB]'s existing 0/1 encoding.
func boolPtrToDB(b *bool) any {
	if b == nil {
		return nil
	}
	return boolToDB(*b)
}

// dbToStringPtr converts a nullable TEXT column into *string: nil when the
// column was SQL NULL. Used for macro_run_steps.command_id — see
// [MacroRunStepRecord]'s doc comment for why this column is nullable
// rather than empty-string-sentineled.
func dbToStringPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

// stringPtrToDB is [dbToStringPtr]'s inverse: nil binds as SQL NULL, a
// non-nil *string binds as its pointed-to value (including "", which is a
// real, distinct value from NULL for this column).
func stringPtrToDB(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// createMacroRun is [Store.CreateMacroRun]/[Tx.CreateMacroRun]'s shared
// body. It always runs inside a transaction — the caller (either
// Store.CreateMacroRun, which opens one itself, or Tx.CreateMacroRun,
// composing into a caller-supplied one) guarantees that — because the two
// guards below and the run+steps insert must all observe (and, for the
// insert, produce) one consistent snapshot.
//
// Order matters and is deliberate, matching STEP-9-SPEC.md §4's general
// rule for the dispatch seam applied here to storage: idempotency lookup
// runs BEFORE the overlap guard, so a legitimate retry of an
// already-accepted submission (same idempotency_key, arriving because a
// client gave up and retried per the Step 7 "a client that gives up before
// the server answers deletes an outcome from existence" lesson) returns
// the existing run — even a still-running one — rather than being told its
// own run is "already in flight."
func createMacroRun(ctx context.Context, q querier, s *Store, run MacroRunRecord, steps []MacroRunStepRecord, now time.Time) (MacroRunRecord, []MacroRunStepRecord, error) {
	switch {
	case run.ID == "":
		return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run: ID is empty")
	case run.MacroObjectID == "":
		return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run: MacroObjectID is empty")
	case run.IdempotencyKey == "":
		return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run: IdempotencyKey is empty")
	case run.State == "":
		return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run %q: State is empty", run.ID)
	}
	for i, st := range steps {
		switch {
		case st.StepID == "":
			return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run %q: step %d: StepID is empty", run.ID, i)
		case st.ActionObjectID == "":
			return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run %q: step %d: ActionObjectID is empty", run.ID, i)
		case st.Integration == "":
			return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run %q: step %d: Integration is empty", run.ID, i)
		case st.SafetyClass == "":
			// STEP-9-SPEC.md §5.3 makes safetyClass required on every
			// action, with no default: required precisely so a defaulted
			// "none" can never silently make a power-off action refusable
			// (§2.5). Requiring it non-empty here too, at the point the
			// pinned value is actually persisted, catches a caller (Wave
			// 1b/2) that forgot to resolve and carry it forward rather than
			// letting an unset safety class round-trip as a silent "none".
			return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run %q: step %d: SafetyClass is empty", run.ID, i)
		case st.LocalFallbackClass == "":
			// STEP-9-SPEC.md §5.4: "an unlabelled step is rejected at write
			// time"; the write in question is the macro definition's, but
			// the same rule has to hold here too, since a run pins this
			// value at submission and a step whose fallback class silently
			// defaulted to "" would misrepresent ADR-024 decision 7's
			// discharge as satisfied when it was never labelled at all.
			return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run %q: step %d: LocalFallbackClass is empty", run.ID, i)
		case st.State == "":
			return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run %q: step %d: State is empty", run.ID, i)
		case st.OutcomeState == "":
			// STEP-9-SPEC review finding 8: an unresolved step must carry an
			// explicit "not yet resolved" state, not an empty string that
			// renders identically to a genuinely blank column — see
			// [MacroRunStepOutcomeStatePending], which a caller (Wave 1b/2)
			// is expected to supply here, and migrations.go's schemaV7 doc
			// comment for the defect this closes.
			return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run %q: step %d: OutcomeState is empty (use MacroRunStepOutcomeStatePending for a step that has not resolved yet)", run.ID, i)
		case st.OutcomeReason == "":
			// Same rule, applied to the reason half of the identical pair —
			// see [MacroRunStepOutcomeReasonPending].
			return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run %q: step %d: OutcomeReason is empty (use MacroRunStepOutcomeReasonPending for a step that has not resolved yet)", run.ID, i)
		}
	}

	// 1. Idempotency lookup first — see this function's doc comment. Which
	// of the three §6.2 outcomes applies (true replay, macro mismatch, or
	// revision mismatch) is decided by classifyMacroRunIdempotencyMatch,
	// never by this call site assuming "found by key" always means replay.
	if existing, err := getMacroRunByIdempotencyKey(ctx, q, run.IdempotencyKey); err == nil {
		return MacroRunRecord{}, nil, classifyMacroRunIdempotencyMatch(existing, run)
	} else if !errors.Is(err, ErrMacroRunNotFound) {
		return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run: check idempotency key: %w", err)
	}

	// 2. Overlap guard (ADR-031 decision 6). Race-free against a second,
	// DIFFERENT idempotency key for the same macro arriving concurrently
	// because this whole function runs inside one transaction on this
	// package's single connection — see migrations.go's schemaV7 doc
	// comment for the full argument.
	if inFlight, err := findRunningMacroRun(ctx, q, run.MacroObjectID); err == nil {
		return MacroRunRecord{}, nil, &MacroRunAlreadyInFlightError{InFlight: inFlight}
	} else if !errors.Is(err, ErrMacroRunNotFound) {
		return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run: check for an in-flight run: %w", err)
	}

	run.CreatedAt = now
	_, err := q.ExecContext(ctx, `
		INSERT INTO macro_runs (
			id, macro_object_id, macro_revision, show, trigger,
			issuer_principal_id, issuer_principal_name, idempotency_key,
			created_at, finished_at, state, completed, confirmed, reason, attribution_degraded
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, NULL, NULL, ?, ?)
	`,
		run.ID, run.MacroObjectID, run.MacroRevision, run.Show, run.Trigger,
		run.IssuerPrincipalID, run.IssuerPrincipalName, run.IdempotencyKey,
		timeToDB(run.CreatedAt), run.State, run.Reason, boolToDB(run.AttributionDegraded),
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			// STEP-9-SPEC review, Minor finding 1: macro_runs has TWO unique
			// constraints — id (PRIMARY KEY) and idempotency_key (UNIQUE) —
			// and modernc.org/sqlite reports both kinds of violation with the
			// identical "UNIQUE constraint failed" text (see
			// [isUniqueConstraintErr]'s own doc comment on why this package
			// checks the string at all). Before this fix, EVERY violation on
			// this INSERT was treated as an idempotency-key collision and
			// unconditionally re-read by run.IdempotencyKey, so a reused run
			// id (a caller bug, or a UUID generator collision — the
			// idempotency key itself may be perfectly unique) produced the
			// misleading "idempotency key %q already exists but could not be
			// re-read: ... macro run not found", since no row with THAT
			// idempotency key actually exists to find.
			//
			// The driver's error text names the violated column
			// ("UNIQUE constraint failed: macro_runs.idempotency_key" or
			// "UNIQUE constraint failed: macro_runs.id"), so check for the
			// idempotency_key column specifically — checking for a bare
			// "macro_runs.id" substring instead would be wrong in the OTHER
			// direction, since that string is itself a prefix of
			// "macro_runs.idempotency_key" and would misclassify every real
			// idempotency collision as an id collision. Only these two
			// constraints exist on this INSERT (this statement touches no
			// other unique-constrained column), so "not idempotency_key"
			// safely means "id" without needing to positively match it.
			if !strings.Contains(err.Error(), "idempotency_key") {
				return MacroRunRecord{}, nil, &DuplicateMacroRunIDError{ID: run.ID}
			}
			// Belt-and-suspenders fallback: the pre-check above should make
			// this unreachable in practice, but the UNIQUE constraint
			// remains the package's actual race-free source of truth for
			// idempotency (see [ErrMacroRunIdempotencyKeyExists]'s doc
			// comment) — treat a constraint violation here identically to
			// one the pre-check already found, running it through the
			// SAME classifyMacroRunIdempotencyMatch so this path can never
			// disagree with the pre-check about which of §6.2's three
			// outcomes applies, exactly as commands.go's insertCommand does
			// for its own single-outcome version of this fallback.
			existing, gerr := getMacroRunByIdempotencyKey(ctx, q, run.IdempotencyKey)
			if gerr != nil {
				return MacroRunRecord{}, nil, fmt.Errorf("store: insert macro run: idempotency key %q already exists but could not be re-read: %w", run.IdempotencyKey, gerr)
			}
			return MacroRunRecord{}, nil, classifyMacroRunIdempotencyMatch(existing, run)
		}
		return MacroRunRecord{}, nil, fmt.Errorf("store: insert macro run %q: %w", run.ID, err)
	}
	run.FinishedAt = nil
	run.Completed = nil
	run.Confirmed = nil

	// STEP-9-SPEC review, Minor finding 2: copy steps into a fresh slice
	// before writing into it. steps[i].RunID/DispatchedAt/ResolvedAt used to
	// be assigned directly onto the caller's own slice (Go slices share
	// their backing array across a function call), so a caller reusing its
	// input steps slice after a successful CreateMacroRun — or inspecting it
	// to see what was submitted — observed this package's own bookkeeping
	// mutations layered onto data it thought it still owned. recSteps is
	// what this function returns and mutates from here on; steps itself is
	// never written to again.
	recSteps := make([]MacroRunStepRecord, len(steps))
	copy(recSteps, steps)

	for i := range recSteps {
		recSteps[i].RunID = run.ID
		st := recSteps[i]
		if _, err := q.ExecContext(ctx, `
			INSERT INTO macro_run_steps (
				run_id, step_index, step_id, action_object_id, action_revision, integration,
				safety_class, local_fallback_class, state,
				dispatched_at, resolved_at, outcome, outcome_state, outcome_reason, command_id,
				attribution_degraded
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?, ?, ?)
		`,
			st.RunID, st.StepIndex, st.StepID, st.ActionObjectID, st.ActionRevision, st.Integration,
			st.SafetyClass, st.LocalFallbackClass, st.State,
			st.Outcome, st.OutcomeState, st.OutcomeReason, stringPtrToDB(st.CommandID),
			boolToDB(st.AttributionDegraded),
		); err != nil {
			return MacroRunRecord{}, nil, fmt.Errorf("store: insert macro run step %d for run %q: %w", st.StepIndex, run.ID, err)
		}
		recSteps[i].DispatchedAt = nil
		recSteps[i].ResolvedAt = nil
	}

	// Same two independent prune triggers as insertCommand (commands.go):
	// insert volume and elapsed wall-clock time since the last pass.
	byCount := s.macroRunInsertCount.Add(1)%pruneEveryNMacroRuns == 0
	byAge := false
	if !byCount {
		last := s.lastMacroRunPruneAtNanos.Load()
		byAge = last == 0 || s.now().Sub(time.Unix(0, last)) >= pruneCheckInterval
	}
	if byCount || byAge {
		if err := s.pruneMacroRuns(ctx, q); err != nil {
			return MacroRunRecord{}, nil, fmt.Errorf("store: create macro run %q: %w", run.ID, err)
		}
		s.lastMacroRunPruneAtNanos.Store(s.now().UnixNano())
	}

	return run, recSteps, nil
}

// CreateMacroRun records a new run and every one of its steps atomically:
// either the run row and all step rows exist, or none of them do — never a
// run with a partial step list, which would leave a reader unable to tell
// "this macro genuinely has 3 steps" apart from "this run's insert was
// interrupted after step 2." On a duplicate IdempotencyKey, returns a
// *[DuplicateMacroRunError] (same macro, same pinned revision),
// *[MacroRunIdempotencyMacroMismatchError] (different macro), or
// *[MacroRunIdempotencyRevisionMismatchError] (same macro, different pinned
// revision) — see [classifyMacroRunIdempotencyMatch]. On a reused run ID
// that is NOT an idempotency-key collision, returns a
// *[DuplicateMacroRunIDError] instead (STEP-9-SPEC review, Minor finding
// 1) — this is a caller bug or an id-generator collision, never a
// legitimate replay. On ADR-031 decision 6's overlap refusal, returns a
// *[MacroRunAlreadyInFlightError] — see these types' doc comments for which
// is checked first and why. The returned steps slice is this function's
// own copy; the caller's input steps slice is never mutated (STEP-9-SPEC
// review, Minor finding 2).
func (s *Store) CreateMacroRun(ctx context.Context, run MacroRunRecord, steps []MacroRunStepRecord) (MacroRunRecord, []MacroRunStepRecord, error) {
	guardNotInTx(ctx, "Store.CreateMacroRun")
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MacroRunRecord{}, nil, fmt.Errorf("store: begin create macro run: %w", err)
	}
	defer func() { _ = sqlTx.Rollback() }() // no-op once Commit succeeds

	rec, recSteps, err := createMacroRun(ctx, sqlTx, s, run, steps, s.now())
	if err != nil {
		return MacroRunRecord{}, nil, err
	}
	if err := sqlTx.Commit(); err != nil {
		return MacroRunRecord{}, nil, fmt.Errorf("store: commit create macro run: %w", err)
	}
	return rec, recSteps, nil
}

// CreateMacroRun is [Store.CreateMacroRun]'s [Tx] form — lets a caller (the
// macro executor) compose run creation with another coordinator-local
// write (e.g. an audit entry) in one transaction, the same reason every
// other *Tx form in this package exists.
func (t *Tx) CreateMacroRun(ctx context.Context, run MacroRunRecord, steps []MacroRunStepRecord) (MacroRunRecord, []MacroRunStepRecord, error) {
	return createMacroRun(ctx, t.tx, t.s, run, steps, t.s.now())
}

func getMacroRunByIdempotencyKey(ctx context.Context, q querier, key string) (MacroRunRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+macroRunColumns+`FROM macro_runs WHERE idempotency_key = ?`, key)
	rec, err := scanMacroRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MacroRunRecord{}, ErrMacroRunNotFound
	}
	if err != nil {
		return MacroRunRecord{}, fmt.Errorf("store: get macro run by idempotency key: %w", err)
	}
	return rec, nil
}

// GetMacroRunByIdempotencyKey returns the run row (steps not included; see
// [Store.GetMacroRun] for a run with its steps) that owns key, or
// [ErrMacroRunNotFound] if no run has ever used it.
func (s *Store) GetMacroRunByIdempotencyKey(ctx context.Context, key string) (MacroRunRecord, error) {
	guardNotInTx(ctx, "Store.GetMacroRunByIdempotencyKey")
	return getMacroRunByIdempotencyKey(ctx, s.db, key)
}

// GetMacroRunByIdempotencyKey is [Store.GetMacroRunByIdempotencyKey]'s
// [Tx] form.
func (t *Tx) GetMacroRunByIdempotencyKey(ctx context.Context, key string) (MacroRunRecord, error) {
	return getMacroRunByIdempotencyKey(ctx, t.tx, key)
}

func findRunningMacroRun(ctx context.Context, q querier, macroObjectID string) (MacroRunRecord, error) {
	// ORDER BY created_at LIMIT 1: this package's own guarantee (see
	// migrations.go's schemaV7 doc comment) is that at most one running row
	// per macro_object_id ever exists, but this query does not itself
	// enforce that (there is deliberately no partial UNIQUE index — see the
	// same comment) — LIMIT 1 keeps the read deterministic rather than
	// depending on SQLite's unspecified row order if that invariant were
	// ever violated by a bug elsewhere.
	row := q.QueryRowContext(ctx, `SELECT`+macroRunColumns+`FROM macro_runs WHERE macro_object_id = ? AND state = 'running' ORDER BY created_at LIMIT 1`, macroObjectID)
	rec, err := scanMacroRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MacroRunRecord{}, ErrMacroRunNotFound
	}
	if err != nil {
		return MacroRunRecord{}, fmt.Errorf("store: find running macro run for %q: %w", macroObjectID, err)
	}
	return rec, nil
}

// FindRunningMacroRun returns the run currently state=="running" for
// macroObjectID, or [ErrMacroRunNotFound] if none is. This is ADR-031
// decision 6's overlap-refusal primitive, exposed directly for a caller
// (the macro executor) that wants to check without attempting a create —
// [Store.CreateMacroRun] already performs this same check internally, so a
// caller only needs this method for a read that is not itself submitting a
// run (e.g. rendering "is this macro currently running" in the API/UI).
func (s *Store) FindRunningMacroRun(ctx context.Context, macroObjectID string) (MacroRunRecord, error) {
	guardNotInTx(ctx, "Store.FindRunningMacroRun")
	return findRunningMacroRun(ctx, s.db, macroObjectID)
}

// FindRunningMacroRun is [Store.FindRunningMacroRun]'s [Tx] form.
func (t *Tx) FindRunningMacroRun(ctx context.Context, macroObjectID string) (MacroRunRecord, error) {
	return findRunningMacroRun(ctx, t.tx, macroObjectID)
}

func listMacroRunSteps(ctx context.Context, q querier, runID string) ([]MacroRunStepRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT`+macroRunStepColumns+`FROM macro_run_steps WHERE run_id = ? ORDER BY step_index`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list macro run steps for %q: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []MacroRunStepRecord
	for rows.Next() {
		rec, err := scanMacroRunStep(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list macro run steps for %q: %w", runID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list macro run steps for %q: %w", runID, err)
	}
	return out, nil
}

func getMacroRun(ctx context.Context, q querier, id string) (MacroRunRecord, []MacroRunStepRecord, error) {
	row := q.QueryRowContext(ctx, `SELECT`+macroRunColumns+`FROM macro_runs WHERE id = ?`, id)
	rec, err := scanMacroRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MacroRunRecord{}, nil, ErrMacroRunNotFound
	}
	if err != nil {
		return MacroRunRecord{}, nil, fmt.Errorf("store: get macro run %q: %w", id, err)
	}
	steps, err := listMacroRunSteps(ctx, q, id)
	if err != nil {
		return MacroRunRecord{}, nil, err
	}
	return rec, steps, nil
}

// GetMacroRun returns one run by its own id together with every one of its
// steps, in step_index order, or [ErrMacroRunNotFound].
func (s *Store) GetMacroRun(ctx context.Context, id string) (MacroRunRecord, []MacroRunStepRecord, error) {
	guardNotInTx(ctx, "Store.GetMacroRun")
	return getMacroRun(ctx, s.db, id)
}

// GetMacroRun is [Store.GetMacroRun]'s [Tx] form.
func (t *Tx) GetMacroRun(ctx context.Context, id string) (MacroRunRecord, []MacroRunStepRecord, error) {
	return getMacroRun(ctx, t.tx, id)
}

// DefaultMacroRunPageSize and MaxMacroRunPageSize bound
// [Store.ListMacroRuns]'s limit parameter, mirroring
// [DefaultCommandPageSize]/[MaxCommandPageSize].
const (
	DefaultMacroRunPageSize = 100
	MaxMacroRunPageSize     = 500
)

func listMacroRuns(ctx context.Context, q querier, macroObjectID string, limit int) ([]MacroRunRecord, error) {
	switch {
	case limit <= 0:
		limit = DefaultMacroRunPageSize
	case limit > MaxMacroRunPageSize:
		limit = MaxMacroRunPageSize
	}

	var (
		rows *sql.Rows
		err  error
	)
	if macroObjectID == "" {
		rows, err = q.QueryContext(ctx, `SELECT`+macroRunColumns+`FROM macro_runs ORDER BY created_at DESC LIMIT ?`, limit)
	} else {
		rows, err = q.QueryContext(ctx, `SELECT`+macroRunColumns+`FROM macro_runs WHERE macro_object_id = ? ORDER BY created_at DESC LIMIT ?`, macroObjectID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list macro runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []MacroRunRecord
	for rows.Next() {
		rec, err := scanMacroRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list macro runs: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list macro runs: %w", err)
	}
	return out, nil
}

// ListMacroRuns returns the most recently created runs, newest first,
// capped at limit (limit <= 0 defaults to [DefaultMacroRunPageSize];
// anything above [MaxMacroRunPageSize] is clamped down to it). If
// macroObjectID is non-empty, only that macro's runs are returned.
func (s *Store) ListMacroRuns(ctx context.Context, macroObjectID string, limit int) ([]MacroRunRecord, error) {
	guardNotInTx(ctx, "Store.ListMacroRuns")
	return listMacroRuns(ctx, s.db, macroObjectID, limit)
}

// ListMacroRuns is [Store.ListMacroRuns]'s [Tx] form.
func (t *Tx) ListMacroRuns(ctx context.Context, macroObjectID string, limit int) ([]MacroRunRecord, error) {
	return listMacroRuns(ctx, t.tx, macroObjectID, limit)
}

func listRunningMacroRuns(ctx context.Context, q querier) ([]MacroRunRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT`+macroRunColumns+`FROM macro_runs WHERE state = 'running' ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list running macro runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []MacroRunRecord
	for rows.Next() {
		rec, err := scanMacroRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list running macro runs: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list running macro runs: %w", err)
	}
	return out, nil
}

// ListRunningMacroRuns returns every run whose lifecycle never reached
// state=="finished", oldest first — ADR-031 decision 4's startup sweep
// primitive, mirroring [Store.ListUnresolvedCommands] (commands.go)
// exactly: a row can end up here only because the process handling it
// stopped existing before it finished (a crash or a kill), since a run
// still legitimately in progress from this SAME running coordinator is
// exactly what the caller (a startup reconciler, called once before the
// server starts serving — Wave 2's job to write, per STEP-9-SPEC.md §6.5)
// is proving cannot yet exist. See [Store.FinishMacroRun] for how the
// reconciler is expected to close out each row this returns.
func (s *Store) ListRunningMacroRuns(ctx context.Context) ([]MacroRunRecord, error) {
	guardNotInTx(ctx, "Store.ListRunningMacroRuns")
	return listRunningMacroRuns(ctx, s.db)
}

// ListRunningMacroRuns is [Store.ListRunningMacroRuns]'s [Tx] form.
func (t *Tx) ListRunningMacroRuns(ctx context.Context) ([]MacroRunRecord, error) {
	return listRunningMacroRuns(ctx, t.tx)
}

// MacroRunStepOutcomeUpdate is
// [Store.UpdateMacroRunStepOutcome]/[Tx.UpdateMacroRunStepOutcome]'s input:
// every field a step's lifecycle mutates after creation. Pointer fields
// left nil leave that column untouched — a dispatch-only update sets State
// and DispatchedAt and leaves the rest alone; a later resolution sets
// Outcome/OutcomeState/OutcomeReason/ResolvedAt — mirroring
// [CommandOutcomeUpdate] (commands.go) exactly.
type MacroRunStepOutcomeUpdate struct {
	State         *string
	DispatchedAt  *time.Time
	ResolvedAt    *time.Time
	Outcome       *string
	OutcomeState  *string
	OutcomeReason *string
	// CommandID, if non-nil, sets macro_run_steps.command_id to *CommandID
	// (which may itself point at ""; see [MacroRunStepRecord]'s doc
	// comment for why "" is a real, distinct value from the column's own
	// NULL/unset state). Left nil, the stored value is untouched — an MQTT
	// step's update never sets this at all, since it never has a command_id
	// in the first place.
	CommandID *string
}

func updateMacroRunStepOutcome(ctx context.Context, q querier, runID string, stepIndex int, upd MacroRunStepOutcomeUpdate) error {
	var (
		sets []string
		args []any
	)
	if upd.State != nil {
		sets = append(sets, "state = ?")
		args = append(args, *upd.State)
	}
	if upd.DispatchedAt != nil {
		sets = append(sets, "dispatched_at = ?")
		args = append(args, timeToDB(*upd.DispatchedAt))
	}
	if upd.ResolvedAt != nil {
		sets = append(sets, "resolved_at = ?")
		args = append(args, timeToDB(*upd.ResolvedAt))
	}
	if upd.Outcome != nil {
		sets = append(sets, "outcome = ?")
		args = append(args, *upd.Outcome)
	}
	if upd.OutcomeState != nil {
		sets = append(sets, "outcome_state = ?")
		args = append(args, *upd.OutcomeState)
	}
	if upd.OutcomeReason != nil {
		sets = append(sets, "outcome_reason = ?")
		args = append(args, *upd.OutcomeReason)
	}
	if upd.CommandID != nil {
		sets = append(sets, "command_id = ?")
		args = append(args, stringPtrToDB(upd.CommandID))
	}
	if len(sets) == 0 {
		return nil
	}
	query := "UPDATE macro_run_steps SET "
	for i, s := range sets {
		if i > 0 {
			query += ", "
		}
		query += s
	}
	query += " WHERE run_id = ? AND step_index = ?"
	args = append(args, runID, stepIndex)

	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: update macro run step outcome (run %q, step %d): %w", runID, stepIndex, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: update macro run step outcome (run %q, step %d): %w", runID, stepIndex, ErrMacroRunStepNotFound)
	}
	return nil
}

// UpdateMacroRunStepOutcome applies a partial update to one existing step
// row — see [MacroRunStepOutcomeUpdate]. Returns [ErrMacroRunStepNotFound]
// if (runID, stepIndex) names no row.
func (s *Store) UpdateMacroRunStepOutcome(ctx context.Context, runID string, stepIndex int, upd MacroRunStepOutcomeUpdate) error {
	guardNotInTx(ctx, "Store.UpdateMacroRunStepOutcome")
	return updateMacroRunStepOutcome(ctx, s.db, runID, stepIndex, upd)
}

// UpdateMacroRunStepOutcome is [Store.UpdateMacroRunStepOutcome]'s [Tx]
// form.
func (t *Tx) UpdateMacroRunStepOutcome(ctx context.Context, runID string, stepIndex int, upd MacroRunStepOutcomeUpdate) error {
	return updateMacroRunStepOutcome(ctx, t.tx, runID, stepIndex, upd)
}

// This is STEP-9-SPEC review finding 8's fix, part 1: the store-level
// affordance ADR-031 decision 4's startup reconciler (Wave 2,
// internal/coordinator/macro, called alongside
// [api.ReconcileStrandedFPPCommands] per STEP-9-SPEC.md §6.5) needs to
// close out a run's steps, not just the run itself. [Store.FinishMacroRun]
// closes the RUN; neither it nor anything else in this file, before this
// fix, ever touched macro_run_steps for a run that was never resumed — see
// migrations.go's schemaV7 doc comment for the failure that left
// unreachable ("three blank rows") and [ReconcileStrandedFPPCommands]
// (internal/coordinator/api/fppcommand_reconcile.go) for the
// list-then-resolve shape this mirrors one level down, at the step rather
// than the command.
//
// [Store.ListUnresolvedMacroRunSteps] is the read half (mirrors
// [Store.ListUnresolvedCommands] exactly, scoped to one run rather than
// global, since a caller here already has the run id from
// [Store.ListRunningMacroRuns] and does not need a cross-run scan);
// [Store.ResolveUnresolvedMacroRunSteps] is the write half (mirrors
// [ReconcileStrandedFPPCommands]'s own "resolve rather than retry" shape:
// one bulk UPDATE, not a per-step dispatch retry attempt, because a step
// stranded by a dead process is exactly as unrecoverable as a command
// stranded the same way — the process that would have carried it forward
// no longer exists).

func listUnresolvedMacroRunSteps(ctx context.Context, q querier, runID string) ([]MacroRunStepRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT`+macroRunStepColumns+`FROM macro_run_steps WHERE run_id = ? AND resolved_at IS NULL ORDER BY step_index`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list unresolved macro run steps for %q: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []MacroRunStepRecord
	for rows.Next() {
		rec, err := scanMacroRunStep(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list unresolved macro run steps for %q: %w", runID, err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list unresolved macro run steps for %q: %w", runID, err)
	}
	return out, nil
}

// ListUnresolvedMacroRunSteps returns every step of runID whose lifecycle
// never reached resolution (resolved_at IS NULL — not yet dispatched,
// dispatched but never confirmed, or confirmed but never durably
// recorded), in step_index order. Exactly like
// [Store.ListUnresolvedCommands], a row can end up here only because the
// process handling it stopped existing before it finished; a step still
// legitimately in progress from a LIVE run in THIS process never appears
// here except in the narrow instant right after this coordinator starts,
// before it has served a single request — see
// [Store.ResolveUnresolvedMacroRunSteps] for how the reconciler is expected
// to close out each row this returns, and [api.ReconcileStrandedFPPCommands]
// for why "call this once, at startup" is what makes that reasoning sound.
func (s *Store) ListUnresolvedMacroRunSteps(ctx context.Context, runID string) ([]MacroRunStepRecord, error) {
	guardNotInTx(ctx, "Store.ListUnresolvedMacroRunSteps")
	return listUnresolvedMacroRunSteps(ctx, s.db, runID)
}

// ListUnresolvedMacroRunSteps is [Store.ListUnresolvedMacroRunSteps]'s [Tx]
// form.
func (t *Tx) ListUnresolvedMacroRunSteps(ctx context.Context, runID string) ([]MacroRunStepRecord, error) {
	return listUnresolvedMacroRunSteps(ctx, t.tx, runID)
}

func resolveUnresolvedMacroRunSteps(ctx context.Context, q querier, runID string, resolvedAt time.Time, state, outcome, outcomeState, outcomeReason string) (int, error) {
	switch {
	case state == "":
		return 0, fmt.Errorf("store: resolve unresolved macro run steps for %q: state is empty", runID)
	case outcome == "":
		return 0, fmt.Errorf("store: resolve unresolved macro run steps for %q: outcome is empty", runID)
	case outcomeState == "":
		return 0, fmt.Errorf("store: resolve unresolved macro run steps for %q: outcomeState is empty", runID)
	case outcomeReason == "":
		return 0, fmt.Errorf("store: resolve unresolved macro run steps for %q: outcomeReason is empty", runID)
	}
	res, err := q.ExecContext(ctx, `
		UPDATE macro_run_steps SET
			state         = ?,
			resolved_at   = ?,
			outcome       = ?,
			outcome_state = ?,
			outcome_reason = ?
		WHERE run_id = ? AND resolved_at IS NULL
	`, state, timeToDB(resolvedAt), outcome, outcomeState, outcomeReason, runID)
	if err != nil {
		return 0, fmt.Errorf("store: resolve unresolved macro run steps for %q: %w", runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: resolve unresolved macro run steps for %q: rows affected: %w", runID, err)
	}
	return int(n), nil
}

// ResolveUnresolvedMacroRunSteps closes out every step of runID that has
// not yet resolved (resolved_at IS NULL), setting state, outcome,
// outcome_state and outcome_reason to the SAME caller-supplied values on
// every affected row and resolved_at to resolvedAt. It never dispatches,
// retries, or otherwise attempts to make further progress on a step —
// matching [ReconcileStrandedFPPCommands]'s own "resolves rather than
// retries" shape (that file's own doc comment: this is what "when the
// system cannot know, it must not act as though it does" — ADR-011's
// framing, applied here to a stranded step's confirmation state — actually
// means in code).
//
// All four string arguments are required non-empty, matching
// [createMacroRun]'s own validation of these same fields at creation: a
// caller resolving a step with a blank outcome_state or outcome_reason
// would silently reintroduce STEP-9-SPEC review finding 8 through this
// exact method, which exists to close that finding, not leave a second
// door open to it.
//
// Returns the number of steps actually resolved (0 if the run has none
// left unresolved, which is the ordinary case for a run that finished
// normally before this is ever called on it — calling this on an already-
// fully-resolved run is a harmless no-op, not an error).
func (s *Store) ResolveUnresolvedMacroRunSteps(ctx context.Context, runID string, resolvedAt time.Time, state, outcome, outcomeState, outcomeReason string) (int, error) {
	guardNotInTx(ctx, "Store.ResolveUnresolvedMacroRunSteps")
	return resolveUnresolvedMacroRunSteps(ctx, s.db, runID, resolvedAt, state, outcome, outcomeState, outcomeReason)
}

// ResolveUnresolvedMacroRunSteps is [Store.ResolveUnresolvedMacroRunSteps]'s
// [Tx] form — lets the startup reconciler (Wave 2) compose this with
// [Tx.FinishMacroRun] in one transaction, so a run's steps and the run
// itself are closed out atomically rather than leaving a window where the
// run row reads "finished" while a step row still reads "unresolved."
func (t *Tx) ResolveUnresolvedMacroRunSteps(ctx context.Context, runID string, resolvedAt time.Time, state, outcome, outcomeState, outcomeReason string) (int, error) {
	return resolveUnresolvedMacroRunSteps(ctx, t.tx, runID, resolvedAt, state, outcome, outcomeState, outcomeReason)
}

// MacroRunFinishUpdate is [Store.FinishMacroRun]/[Tx.FinishMacroRun]'s
// input. Unlike [MacroRunStepOutcomeUpdate] this is not a partial update:
// finishing a run always sets state=="finished" and always sets BOTH
// Completed and Confirmed to a definite value in the same UPDATE — ADR-031
// decision 3 has no state where a finished run leaves either unknown, so
// this type has no way to express "finish but leave one nil."
type MacroRunFinishUpdate struct {
	FinishedAt          time.Time
	Completed           bool
	Confirmed           bool
	Reason              string
	AttributionDegraded bool
}

func finishMacroRun(ctx context.Context, q querier, runID string, upd MacroRunFinishUpdate) error {
	res, err := q.ExecContext(ctx, `
		UPDATE macro_runs SET
			state                = 'finished',
			finished_at          = ?,
			completed             = ?,
			confirmed             = ?,
			reason                = ?,
			attribution_degraded  = ?
		WHERE id = ?
	`,
		timeToDB(upd.FinishedAt), boolToDB(upd.Completed), boolToDB(upd.Confirmed),
		upd.Reason, boolToDB(upd.AttributionDegraded), runID,
	)
	if err != nil {
		return fmt.Errorf("store: finish macro run %q: %w", runID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: finish macro run %q: %w", runID, ErrMacroRunNotFound)
	}
	return nil
}

// FinishMacroRun transitions a run to state=="finished", setting
// FinishedAt, Completed, Confirmed, Reason, and AttributionDegraded all in
// one UPDATE (see [MacroRunFinishUpdate]'s doc comment for why this is not
// a partial update the way [MacroRunStepOutcomeUpdate] is). This is the
// ONLY method in this package that ever sets Completed/Confirmed away from
// NULL — used both by the executor completing a run normally (ADR-031
// decisions 2/3) and by the startup reconciler closing out a run
// interrupted by a coordinator restart (ADR-031 decision 4: Completed:
// false, Reason naming the restart, Confirmed set to whatever had already
// been earned by the steps that resolved before the interruption).
// Returns [ErrMacroRunNotFound] if id names no row.
func (s *Store) FinishMacroRun(ctx context.Context, id string, upd MacroRunFinishUpdate) error {
	guardNotInTx(ctx, "Store.FinishMacroRun")
	return finishMacroRun(ctx, s.db, id, upd)
}

// FinishMacroRun is [Store.FinishMacroRun]'s [Tx] form.
func (t *Tx) FinishMacroRun(ctx context.Context, id string, upd MacroRunFinishUpdate) error {
	return finishMacroRun(ctx, t.tx, id, upd)
}

func setMacroRunAttributionDegraded(ctx context.Context, q querier, id string) error {
	res, err := q.ExecContext(ctx, `UPDATE macro_runs SET attribution_degraded = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: set macro run %q attribution degraded: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: set macro run %q attribution degraded: %w", id, ErrMacroRunNotFound)
	}
	return nil
}

// SetMacroRunAttributionDegraded flips AttributionDegraded to true for a
// still-running (or already-finished) run — ADR-031 decision 5's cost,
// recorded the moment the executor actually hits an audit-write failure on
// an audit-exempt run, rather than only at [Store.FinishMacroRun] time.
// This is deliberately idempotent (setting an already-true row is a no-op
// success, not an error) and one-directional: nothing in this package ever
// clears it back to false, matching decision 5's own framing — once a
// run's attribution has been degraded, that is a permanent fact about that
// run, not a transient status. Beyond STEP-9-SPEC.md §6.1's literal ask
// (a column set at finish time), this lets a caller reading a run through
// [Store.GetMacroRun] WHILE it is still running see degraded attribution
// as soon as it happens — decision 5's own text is "it must be surfaced,"
// which this method reads as not limited to post-hoc surfacing at finish.
func (s *Store) SetMacroRunAttributionDegraded(ctx context.Context, id string) error {
	guardNotInTx(ctx, "Store.SetMacroRunAttributionDegraded")
	return setMacroRunAttributionDegraded(ctx, s.db, id)
}

// SetMacroRunAttributionDegraded is
// [Store.SetMacroRunAttributionDegraded]'s [Tx] form.
func (t *Tx) SetMacroRunAttributionDegraded(ctx context.Context, id string) error {
	return setMacroRunAttributionDegraded(ctx, t.tx, id)
}

func setMacroRunStepAttributionDegraded(ctx context.Context, q querier, runID string, stepIndex int) error {
	res, err := q.ExecContext(ctx, `UPDATE macro_run_steps SET attribution_degraded = 1 WHERE run_id = ? AND step_index = ?`, runID, stepIndex)
	if err != nil {
		return fmt.Errorf("store: set macro run step (run %q, step %d) attribution degraded: %w", runID, stepIndex, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: set macro run step (run %q, step %d) attribution degraded: %w", runID, stepIndex, ErrMacroRunStepNotFound)
	}
	return nil
}

// SetMacroRunStepAttributionDegraded flips THIS STEP's own
// AttributionDegraded to true: the per-step half of ADR-031 decision 5,
// corrected 2026-08-14 when STEP-9-SPEC.md §2.5 moved the audit exemption
// from a per-run decision to a per-step one: a stop step dispatching with
// degraded attribution while a start step elsewhere in the same run is
// correctly refused (or vice versa) needs its OWN record of which step
// actually paid decision 5's cost, not only [Store.SetMacroRunAttributionDegraded]'s
// single run-wide flag, which cannot say which step earned it.
//
// This method sets ONLY the step's own column; it does not also call
// [Store.SetMacroRunAttributionDegraded]/[Tx.SetMacroRunAttributionDegraded]
// on the parent run, deliberately: ADR-031 decision 5's own text is
// "recorded on the step and raised onto the run," two separate facts this
// package stores independently rather than deriving one from the other (see
// [MacroRunStepRecord]'s doc comment). The caller (the executor, Wave 2) is
// the one that knows both need to happen together for a given step and
// calls both, composed inside one [Store.InTx]/[Tx] closure with whatever
// audit write is already degraded, so the two flags and that write commit
// or roll back together.
//
// Deliberately idempotent (setting an already-true row is a no-op success)
// and one-directional, exactly mirroring [Store.SetMacroRunAttributionDegraded]'s
// own framing: once a step's attribution has been degraded, that is a
// permanent fact about that step. Returns [ErrMacroRunStepNotFound] if
// (runID, stepIndex) names no row.
func (s *Store) SetMacroRunStepAttributionDegraded(ctx context.Context, runID string, stepIndex int) error {
	guardNotInTx(ctx, "Store.SetMacroRunStepAttributionDegraded")
	return setMacroRunStepAttributionDegraded(ctx, s.db, runID, stepIndex)
}

// SetMacroRunStepAttributionDegraded is
// [Store.SetMacroRunStepAttributionDegraded]'s [Tx] form.
func (t *Tx) SetMacroRunStepAttributionDegraded(ctx context.Context, runID string, stepIndex int) error {
	return setMacroRunStepAttributionDegraded(ctx, t.tx, runID, stepIndex)
}

// CommandDetailState is [Store.ResolveMacroRunStepCommand]'s answer to
// STEP-9-SPEC.md §6.1's "the commands.id reference is dangling by design
// and must be read as one": commands (schemaV6) is pruned by
// retention.go's pruneCommands while macro_run_steps is not individually
// pruned (see migrations.go's schemaV7 doc comment), so a step's non-nil
// CommandID can legitimately point at a row that no longer exists. A
// caller MUST be able to tell that case apart from "this step never had a
// command in the first place": the spec's own words are "it never renders
// blank and it never renders as though the step had no command", and this
// type is what makes that a named value instead of a convention a caller
// has to reconstruct by separately interpreting a nil CommandID and a
// [ErrCommandNotFound] from [Store.GetCommand] as two unrelated facts.
type CommandDetailState string

const (
	// CommandDetailNone is CommandID == nil: this step has never dispatched
	// through commands, either an MQTT step (STEP-9-SPEC.md §7), which
	// never gets one at all, or an FPP step that has not dispatched YET.
	// [MacroRunStepRecord]'s own doc comment already names this ambiguity
	// as deliberately unresolved by this package (only Wave 1b/1c's caller
	// knows which case applies); CommandDetailNone reports exactly that
	// same "no value" fact under its own name, not a new distinction.
	CommandDetailNone CommandDetailState = "none"

	// CommandDetailAvailable is CommandID != nil and [Store.GetCommand]
	// still returns that row: the step dispatched through commands and the
	// dispatched command's full record is still readable.
	CommandDetailAvailable CommandDetailState = "available"

	// CommandDetailNotRetained is CommandID != nil but [Store.GetCommand]
	// returns [ErrCommandNotFound] for it: the step DID dispatch through
	// commands, and retention has since pruned that row. This is the case
	// §6.1 requires a reader to render as "not retained, with a reason",
	// never as blank and never as though CommandDetailNone applied: the
	// step genuinely had a command; only the command's own detail is gone.
	CommandDetailNotRetained CommandDetailState = "not_retained"
)

func resolveMacroRunStepCommand(ctx context.Context, q querier, step MacroRunStepRecord) (CommandDetailState, CommandRecord, error) {
	if step.CommandID == nil {
		return CommandDetailNone, CommandRecord{}, nil
	}
	rec, err := getCommand(ctx, q, *step.CommandID)
	if errors.Is(err, ErrCommandNotFound) {
		return CommandDetailNotRetained, CommandRecord{}, nil
	}
	if err != nil {
		return "", CommandRecord{}, fmt.Errorf("store: resolve macro run step command %q: %w", *step.CommandID, err)
	}
	return CommandDetailAvailable, rec, nil
}

// ResolveMacroRunStepCommand answers, for one already-read
// [MacroRunStepRecord], which of [CommandDetailState]'s three cases
// applies right now, returning the live [CommandRecord] only for
// CommandDetailAvailable: CommandDetailNone and CommandDetailNotRetained
// both return a zero CommandRecord, which the caller must read only
// through the returned state, never as a value in its own right (a zero
// CommandRecord is exactly the "zero value that reads as absent" §6.1
// warns against treating as meaningful on its own).
//
// This is a plain read composed from [Store.GetCommand]'s own
// [ErrCommandNotFound] contract: it adds no new table, no join, and no
// cached knowledge of retention's own schedule; a caller could reconstruct
// this same three-way answer by calling GetCommand directly and
// interpreting a nil CommandID and an ErrCommandNotFound as two separate
// facts, and doing that in two places is exactly the "papering over it"
// STEP-9-SPEC.md's Wave 1a brief warns against, so this method is the one
// place that mapping is made rather than re-derived at each call site.
func (s *Store) ResolveMacroRunStepCommand(ctx context.Context, step MacroRunStepRecord) (CommandDetailState, CommandRecord, error) {
	guardNotInTx(ctx, "Store.ResolveMacroRunStepCommand")
	return resolveMacroRunStepCommand(ctx, s.db, step)
}

// ResolveMacroRunStepCommand is [Store.ResolveMacroRunStepCommand]'s [Tx]
// form.
func (t *Tx) ResolveMacroRunStepCommand(ctx context.Context, step MacroRunStepRecord) (CommandDetailState, CommandRecord, error) {
	return resolveMacroRunStepCommand(ctx, t.tx, step)
}

// macroRequestedRevisionPrefix marks a commands.requested_revision value as
// having been formatted by [FormatMacroRunRequestedRevision] rather than
// left at commands.go's own NOT NULL DEFAULT ” (what an operator-issued
// command's dispatch leaves this column at today; see commands.go's
// CommandRecord: nothing in this package has ever written a non-empty
// RequestedRevision before Step 9). Any value beginning with this prefix is
// a macro-issued dispatch; any other value (including "") is not.
const macroRequestedRevisionPrefix = "macro:"

// FormatMacroRunRequestedRevision is STEP-9-SPEC.md §6.1's store-side
// support for "a step's dispatch writes desired state exactly as a single
// command does, and requested_revision carries the pinned macro revision,
// formatted so a macro-issued command is distinguishable from an
// operator-issued one": it turns a run's pinned (macroObjectID,
// macroRevision), the SAME two values [MacroRunRecord] itself pins at
// submission, into the single string a caller (Wave 1b's dispatch seam,
// via [Store.InsertCommand]'s existing CommandRecord.RequestedRevision
// field, which needs no change to accept it) writes into commands so that
// table alone can answer "which macro revision caused this dispatch"
// without a join back to macro_run_steps.
//
// The format is "macro:<macroObjectID>@<macroRevision>". "@" was chosen as
// the id/revision separator specifically so this stays a genuinely
// invertible round trip even for a macroObjectID that itself contains "@":
// [ParseMacroRunRequestedRevision] splits on the LAST "@" in the string,
// never the first, and a config object id can be anything this package's
// generic (kind, id) config store accepts (see migrations.go's schemaV6 doc
// comment: config_objects has no CHECK on id's charset), so "split on the
// last separator, not the first" is the one rule that stays correct
// regardless of what a future id validator does or does not forbid.
func FormatMacroRunRequestedRevision(macroObjectID string, macroRevision int64) string {
	return fmt.Sprintf("%s%s@%d", macroRequestedRevisionPrefix, macroObjectID, macroRevision)
}

// ParseMacroRunRequestedRevision is [FormatMacroRunRequestedRevision]'s
// inverse. ok is false for any value that was not produced by that
// function, including "" (an operator-issued command's untouched default)
// and any value not beginning with macroRequestedRevisionPrefix, so a
// caller can tell "this command was not macro-issued" apart from "this
// command was macro-issued but the value is somehow malformed" only by
// checking ok; this function deliberately does not distinguish those two
// itself; a caller that needs to (e.g. to log a corrupted value as a
// distinct condition from an ordinary operator-issued command) checks
// strings.HasPrefix(s, macroRequestedRevisionPrefix); this file has no
// exported way to do that named separately, since no caller inside this
// package has needed one yet.
func ParseMacroRunRequestedRevision(s string) (macroObjectID string, macroRevision int64, ok bool) {
	rest, found := strings.CutPrefix(s, macroRequestedRevisionPrefix)
	if !found {
		return "", 0, false
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return "", 0, false
	}
	rev, err := strconv.ParseInt(rest[at+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return rest[:at], rev, true
}

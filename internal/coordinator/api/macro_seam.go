package api

import (
	"context"
	"errors"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the boundary between this package's HTTP surface and Step
// 9's macro executor, internal/coordinator/macro. It holds declarations
// only: no handler, no route, no behaviour.
//
// The import direction is forced, not chosen. Wave 1 exported
// [FPPCommandDispatcher] specifically so the macro executor could dispatch
// an FPP primitive in-process rather than issuing itself an HTTP request
// against its own coordinator (see fppcommand_dispatch.go's own top
// comment), which means internal/coordinator/macro imports THIS package.
// This package therefore must never import internal/coordinator/macro: the
// reverse edge is an import cycle. Everything that crosses the boundary is
// declared here, in the importee, and the executor satisfies
// [MacroRunner]. That is the same shape interfaces.go already uses for
// [NodeLister] and [ConfigStore], with the one difference that the
// implementer imports this package rather than the other way round.
//
// The types below deliberately carry store.MacroRunRecord and
// store.MacroRunStepRecord rather than a second parallel set of run
// structs. A run's storage shape and its in-process read shape are the
// same facts, and wave 1's records already encode the decisions a
// duplicate would have to restate (Completed/Confirmed nil until finished,
// CommandID nil for an MQTT step, revisions pinned at submission). Mapping
// them onto the v1 wire types is this package's own job at the route.

// MacroSubmitRequest is one macro-run submission that has already passed
// this package's own scope check. It carries no scope and no credential:
// per STEP-9-SPEC.md section 2.9, show:macro:run authorizes the run and is
// the only scope checked, and it is checked at the route before anything
// reaches the executor. The executor must not perform a second, per-step
// scope check — the scheduler principal holds show:macro:run and neither
// fpp:command nor device:power, so a per-step check would make every
// day-0 macro unrunnable by the principal the design is built around.
type MacroSubmitRequest struct {
	// MacroObjectID is the show.macro configuration object's id. The
	// executor pins its current revision at submission; nothing here
	// names a revision, because a caller asking to run "the macro as it
	// stands" is the only thing any client can honestly ask for.
	MacroObjectID string

	// IdempotencyKey is the caller's own key, governed by the three-way
	// replay rule in STEP-9-SPEC.md section 6.2 (same key + same macro +
	// same pinned revision replays; a different macro is one 409; the
	// same macro at a different pinned revision is a distinguishable
	// second 409). Required.
	IdempotencyKey string

	// Trigger is which surface submitted this run: "api", "plugin",
	// "cli" or "ui". It lands in store.MacroRunRecord.Trigger, which
	// does not validate it; validation belongs at the route.
	Trigger string

	// Issuer is the authenticated principal. RunID on this struct is set
	// by the executor, per step, not by the caller — see
	// [FPPCommandIssuer.RunID], which explains where a run id actually
	// lands in the audit trail.
	Issuer FPPCommandIssuer

	// PriorFailures are degraded outcomes the caller buffered locally
	// and is reporting now, per STEP-9-SPEC.md section 8.3 path 2. Only
	// the FPP plugin fills this today, and only wave 3 builds the
	// plugin; the field exists in wave 2 so that path has a landing
	// point that can be reached rather than one invented later. The
	// executor bounds what it accepts and coalesces what it records: an
	// unbounded write on a failure path is an eviction primitive aimed
	// at the coordinator's own event history.
	PriorFailures []MacroPriorFailure

	// PriorFailuresDropped is how many buffered entries the caller
	// discarded before sending, because its own buffer was full. It is
	// carried rather than inferred, since a silently truncated failure
	// history is a smaller version of the lie the buffer exists to
	// prevent. Zero means nothing was dropped.
	PriorFailuresDropped int
}

// MacroPriorFailure is one attempt a caller made that did not reach the
// coordinator successfully, reported after the fact. Class is the caller's
// own classification from STEP-9-SPEC.md section 8.2: "refused" (401 or
// 403, a healthy coordinator refusing this caller), "rejected" (another
// 4xx, the coordinator answering about the request rather than the
// credential), or "unreachable" (a transport failure or a 5xx). The three
// are kept apart because folding a 404 for an unknown macro into
// "refused" would send an operator to rotate a credential over a typo.
type MacroPriorFailure struct {
	MacroObjectID string
	Class         string
	// HTTPStatus is 0 when there was no response at all, which is the
	// ordinary case for "unreachable". Zero is a real, distinct value
	// here, not a missing one.
	HTTPStatus int
	At         time.Time
}

// MacroRunFilter is GET /api/v1/macro-runs' query, normalized. An empty
// MacroObjectID, Show, or State means no filter on that dimension; the
// route is responsible for rejecting a State outside {"running",
// "finished"} rather than passing an unknown value through as "no filter".
type MacroRunFilter struct {
	MacroObjectID string
	Show          string
	State         string
	Limit         int
}

// MacroRunResult is one run and its steps as the HTTP surface reads them.
type MacroRunResult struct {
	Run   store.MacroRunRecord
	Steps []store.MacroRunStepRecord

	// Replay is true when the submission's idempotency key named an
	// already-existing run: nothing new was created by this call, and
	// Run reports the original run's current state. It mirrors
	// [FPPCommandOutcome.Replay] exactly, for the same reason that type
	// carries it.
	Replay bool
}

// MacroRunner is Step 9's macro executor as this package needs it.
// internal/coordinator/macro implements it; see this file's top comment
// for why the interface is declared here rather than there.
type MacroRunner interface {
	// SubmitRun accepts a run, persists it with its macro and action
	// revisions pinned, and starts executing it in the background.
	// Exactly one of (a MacroRunResult with a non-empty Run.ID, problem,
	// err) is meaningful on return, matching
	// [FPPCommandDispatcher.Dispatch]'s existing contract: problem is a
	// caller-facing refusal (unknown macro, a replay conflict, an
	// overlapping run, an unwritable audit store on a run whose steps
	// are not all exempt), and err is this coordinator's own dependency
	// failing.
	//
	// It never returns a completed run. Per ADR-031 decision 1 the run
	// is asynchronous, so the route answers 202 with the run in its
	// initial state and the client learns the outcome by watching.
	SubmitRun(ctx context.Context, req MacroSubmitRequest) (MacroRunResult, *v1.Problem, error)

	// GetRun returns one run with its steps, or a wrapped
	// [ErrMacroRunNotFound].
	GetRun(ctx context.Context, runID string) (MacroRunResult, error)

	// ListRuns returns runs most recent first. Steps are not included:
	// a list of runs is a list of runs, and a client wanting step detail
	// fetches the run.
	ListRuns(ctx context.Context, f MacroRunFilter) ([]store.MacroRunRecord, error)

	// SnapshotRuns is what GET /api/v1/snapshot carries: every in-flight
	// run, plus a bounded window of recently finished ones.
	//
	// ADR-020 decision 3 makes this fatal to omit rather than merely
	// incomplete. The change stream emits no id, its sequence numbers
	// are per connection, and every interruption forces an authoritative
	// snapshot re-fetch, so a client connecting for the first time
	// during a run has no other way to learn the run exists. Without it
	// that client sees nothing, presses Run, receives the overlap 409
	// naming a run it cannot display, and the operator concludes the
	// system is stuck.
	SnapshotRuns(ctx context.Context) ([]store.MacroRunRecord, error)
}

// ErrMacroRunNotFound is what [MacroRunner.GetRun] returns, wrapped, for a
// run id that names nothing. It is a sentinel rather than a *v1.Problem
// because "no such run" on a read is this package's own 404 to render,
// exactly as store.ErrCommandNotFound is on the command read path.
var ErrMacroRunNotFound = errors.New("api: macro run not found")

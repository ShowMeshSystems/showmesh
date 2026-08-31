package macro

import (
	"context"
	"errors"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// SubmitRun implements [api.MacroRunner.SubmitRun]. See this method's own
// numbered steps for how STEP-9-SPEC.md section 2.5's submission-time
// audit posture check, section 6.2's three-way idempotency replay rule,
// and section 2.6's overlap guard fit together — all three ultimately rest
// on [store.Store.CreateMacroRun]/[store.Tx.CreateMacroRun], which already
// runs idempotency lookup BEFORE the overlap guard inside one transaction
// (macro_runs.go's createMacroRun, Wave 1a) — this method does not
// reimplement that ordering, only interprets the three-way error it
// returns.
func (e *Executor) SubmitRun(ctx context.Context, req api.MacroSubmitRequest) (api.MacroRunResult, *v1.Problem, error) {
	// --- 1. Validate the request shape itself. Never a store round trip. ---

	if req.MacroObjectID == "" {
		p := invalidParameterProblem("macroObjectId is required")
		return api.MacroRunResult{}, &p, nil
	}
	if req.IdempotencyKey == "" {
		p := invalidParameterProblem("idempotencyKey is required")
		return api.MacroRunResult{}, &p, nil
	}

	// --- 2. Resolve and pin the macro and every step's action. ---

	rm, err := e.resolveMacro(ctx, req.MacroObjectID)
	if err != nil {
		if errors.Is(err, store.ErrConfigObjectNotFound) || errors.Is(err, store.ErrConfigRevisionNotFound) {
			p := macroNotFoundProblem(req.MacroObjectID)
			return api.MacroRunResult{}, &p, nil
		}
		return api.MacroRunResult{}, nil, err
	}

	steps := buildStepRecords(rm)

	runID := e.newID()
	now := e.now()
	run := store.MacroRunRecord{
		ID:                  runID,
		MacroObjectID:       rm.ObjectID,
		MacroRevision:       rm.Revision,
		Show:                rm.Payload.Show,
		Trigger:             req.Trigger,
		IssuerPrincipalID:   req.Issuer.PrincipalID,
		IssuerPrincipalName: req.Issuer.PrincipalName,
		IdempotencyKey:      req.IdempotencyKey,
		State:               "running",
	}

	// --- 3. Submission-time audit posture check (ADR-031 decision 5,
	// STEP-9-SPEC.md section 2.5): create the run and write its own
	// "macro.run.submit" audit entry in ONE transaction, exactly mirroring
	// dispatchFPPCommand's step 5. On an audit-write failure specifically,
	// this run's OWN submission-time posture (are ALL its steps exempt)
	// decides fail-closed versus proceed-degraded — this is the
	// SUBMISSION-time check only; a step's own MID-RUN audit exemption is
	// decided per step, independently, when that step actually dispatches
	// (step_fpp.go / step_mqtt.go), never inherited from this check. ---

	submitEntry := identity.AuditEntry{
		Timestamp:      now,
		PrincipalID:    req.Issuer.PrincipalID,
		PrincipalName:  req.Issuer.PrincipalName,
		Form:           req.Issuer.Form,
		CredentialID:   req.Issuer.CredentialID,
		ClientAddr:     req.Issuer.ClientAddr,
		Action:         "macro.run.submit",
		Target:         rm.ObjectID,
		IdempotencyKey: req.IdempotencyKey,
		Kind:           identity.AuditDispatch,
		Params:         map[string]any{"runId": runID, "macroRevision": rm.Revision},
	}

	var createdRun store.MacroRunRecord
	var createdSteps []store.MacroRunStepRecord

	auditErr := e.identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		r, s, err := tx.CreateMacroRun(ctx, run, steps)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		createdRun, createdSteps = r, s
		return submitEntry, nil
	})

	switch {
	case auditErr == nil:
		// Fall through to background execution below.

	case isMacroRunConflict(auditErr):
		return e.conflictResult(ctx, auditErr)

	case errors.Is(auditErr, identity.ErrAuditWrite):
		// OWNER DECISION, 2026-08-14, and it replaces STEP-9-SPEC.md
		// section 2.5's submission gate outright: a macro run never
		// withholds a command because the audit store is down. In the
		// owner's words, "we cannot risk the show because a logging or
		// audit system is down, that's not how show critical
		// infrastructure works."
		//
		// What was here before, and why it was wrong for this system: the
		// run proceeded only if EVERY step was one of ADR-024 decision
		// 11's three exempt safety classes, and was refused 503 outright
		// otherwise. That is fail-closed reasoning applied to the wrong
		// failure direction, which is the exact error ADR-024 was written
		// to correct and which this project has now made four times. A
		// [stop, start] macro was refused in its entirety, so the STOP did
		// not run, on a coordinator that was healthy in every respect
		// except its ability to write a log line. The acceptance run
		// measured that against a real unwritable audit_log.
		//
		// Attribution is not abandoned, it is DOWNGRADED and said out
		// loud: the run carries AttributionDegraded, every step inherits
		// it, both clients render it, and the warning below names the
		// cause. The operator loses the audit trail for this run, which is
		// the cost of the decision, and keeps the show.
		//
		// Fail-closed is unchanged where it holds the right actor
		// accountable and costs no show: config:write and principal:write
		// (ADR-024 decision 11) still refuse, because nothing in a running
		// show depends on rewriting configuration mid-show.
		run.AttributionDegraded = true
		r, s, cErr := e.store.CreateMacroRun(ctx, run, steps)
		if cErr != nil {
			if isMacroRunConflict(cErr) {
				return e.conflictResult(ctx, cErr)
			}
			return api.MacroRunResult{}, nil, fmt.Errorf("macro: create run (degraded attribution): %w", cErr)
		}
		createdRun, createdSteps = r, s
		e.logWarn("macro run created with degraded attribution: audit store unwritable at submission and every step is exempt",
			"runId", runID, "macroId", rm.ObjectID, "cause", errString(auditErr))

	default:
		return api.MacroRunResult{}, nil, fmt.Errorf("macro: create run: %w", auditErr)
	}

	// --- 4. Buffered prior-failure reporting (STEP-9-SPEC.md section 8.3
	// path 2's landing point). Best-effort; never blocks acceptance of the
	// run itself. ---

	e.recordPriorFailures(ctx, req)

	// --- 5. Start background execution and answer 202-shaped: the run's
	// initial state, never a completed result (ADR-031 decision 1). ---

	e.notifyChange()

	e.wg.Add(1)
	go e.executeRun(context.WithoutCancel(ctx), rm, createdRun, createdSteps, req.Issuer)

	return api.MacroRunResult{Run: createdRun, Steps: createdSteps, Replay: false}, nil, nil
}

// isMacroRunConflict reports whether err is one of
// [store.CreateMacroRun]'s three named caller-facing conflicts
// (STEP-9-SPEC.md section 6.2's true replay, macro mismatch, or revision
// mismatch) or ADR-031 decision 6's overlap refusal — the set of
// store-layer conditions this method answers with a *v1.Problem (or, for a
// true replay, the existing run) rather than an internal error.
func isMacroRunConflict(err error) bool {
	return errors.Is(err, store.ErrMacroRunIdempotencyKeyExists) ||
		errors.Is(err, store.ErrMacroRunIdempotencyMacroMismatch) ||
		errors.Is(err, store.ErrMacroRunIdempotencyRevisionMismatch) ||
		errors.Is(err, store.ErrMacroRunAlreadyInFlight)
}

// conflictResult maps one of isMacroRunConflict's four conditions onto
// this method's own three-way return contract: a true replay returns the
// EXISTING run (Replay: true) rather than a problem; every other case
// returns a *v1.Problem naming which of STEP-9-SPEC.md section 6.2's two
// idempotency conflicts, or section 2.6's overlap guard, fired.
func (e *Executor) conflictResult(ctx context.Context, err error) (api.MacroRunResult, *v1.Problem, error) {
	var dup *store.DuplicateMacroRunError
	if errors.As(err, &dup) {
		result, rErr := e.replayResult(ctx, dup.Existing)
		return result, nil, rErr
	}
	var macroMismatch *store.MacroRunIdempotencyMacroMismatchError
	if errors.As(err, &macroMismatch) {
		p := macroRunIdempotencyMacroConflictProblem(macroMismatch)
		return api.MacroRunResult{}, &p, nil
	}
	var revMismatch *store.MacroRunIdempotencyRevisionMismatchError
	if errors.As(err, &revMismatch) {
		p := macroRunIdempotencyRevisionConflictProblem(revMismatch)
		return api.MacroRunResult{}, &p, nil
	}
	var inFlight *store.MacroRunAlreadyInFlightError
	if errors.As(err, &inFlight) {
		p := macroRunAlreadyInFlightProblem(inFlight)
		return api.MacroRunResult{}, &p, nil
	}
	return api.MacroRunResult{}, nil, fmt.Errorf("macro: conflictResult called on a non-conflict error: %w", err)
}

// replayResult reads existing's current, full state back (run plus steps)
// for STEP-9-SPEC.md section 6.2's true-replay case: "returns the existing
// run and its current state, Replay: true, not a new run." A replay never
// re-reads the macro/action definitions or recomputes anything — it
// reports exactly what the ORIGINAL submission already pinned and however
// far that original run has since progressed.
func (e *Executor) replayResult(ctx context.Context, existing store.MacroRunRecord) (api.MacroRunResult, error) {
	run, steps, err := e.store.GetMacroRun(ctx, existing.ID)
	if err != nil {
		return api.MacroRunResult{}, fmt.Errorf("macro: read replayed run %q: %w", existing.ID, err)
	}
	return api.MacroRunResult{Run: run, Steps: steps, Replay: true}, nil
}

// GetRun implements [api.MacroRunner.GetRun].
func (e *Executor) GetRun(ctx context.Context, runID string) (api.MacroRunResult, error) {
	run, steps, err := e.store.GetMacroRun(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrMacroRunNotFound) {
			return api.MacroRunResult{}, fmt.Errorf("%w: %s", api.ErrMacroRunNotFound, runID)
		}
		return api.MacroRunResult{}, err
	}
	return api.MacroRunResult{Run: run, Steps: steps}, nil
}

// ListRuns implements [api.MacroRunner.ListRuns].
//
// [store.Store.ListMacroRuns] takes only a macro id and a limit, with no
// state or show filter — [store.Store.ListRunningMacroRuns] covers
// "running" alone. This method filters "finished" (and re-filters
// "running") and Show in memory over whichever of the two store calls
// already narrows by macro id, rather than adding a third store method for
// this wave: see this builder's own report for whether a store-level
// filter is warranted once a real caller (Wave 3's clients) exercises this
// at scale. limit is applied AFTER the in-memory filtering, matching what
// a caller asking for "the last N finished runs of this show" actually
// wants — applying it before would silently return fewer than N once any
// non-matching row is filtered out.
func (e *Executor) ListRuns(ctx context.Context, f api.MacroRunFilter) ([]store.MacroRunRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = store.DefaultMacroRunPageSize
	}

	if f.State == "running" {
		runs, err := e.listRunningRuns(ctx, f.MacroObjectID, f.Show)
		if err != nil {
			return nil, err
		}
		return capMacroRuns(runs, limit), nil
	}

	// "finished", or no filter at all: read a superset from ListMacroRuns
	// (already newest-first, already narrowed by macro id) and filter
	// in-memory. store.MaxMacroRunPageSize bounds a single read the same
	// way it already bounds every other ListMacroRuns caller.
	fetchLimit := limit
	if (f.State != "" || f.Show != "") && fetchLimit < store.MaxMacroRunPageSize {
		// Over-fetch so a state or show filter does not silently return
		// fewer than limit rows just because some fetched rows did not
		// match — bounded at MaxMacroRunPageSize either way.
		fetchLimit = store.MaxMacroRunPageSize
	}
	runs, err := e.store.ListMacroRuns(ctx, f.MacroObjectID, fetchLimit)
	if err != nil {
		return nil, err
	}
	if f.State == "" && f.Show == "" {
		return capMacroRuns(runs, limit), nil
	}
	out := make([]store.MacroRunRecord, 0, len(runs))
	for _, r := range runs {
		if f.State != "" && r.State != f.State {
			continue
		}
		if f.Show != "" && r.Show != f.Show {
			continue
		}
		out = append(out, r)
	}
	return capMacroRuns(out, limit), nil
}

func (e *Executor) listRunningRuns(ctx context.Context, macroObjectID, show string) ([]store.MacroRunRecord, error) {
	all, err := e.store.ListRunningMacroRuns(ctx)
	if err != nil {
		return nil, err
	}
	if macroObjectID == "" && show == "" {
		return all, nil
	}
	out := make([]store.MacroRunRecord, 0, len(all))
	for _, r := range all {
		if macroObjectID != "" && r.MacroObjectID != macroObjectID {
			continue
		}
		if show != "" && r.Show != show {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func capMacroRuns(runs []store.MacroRunRecord, limit int) []store.MacroRunRecord {
	if limit > 0 && len(runs) > limit {
		return runs[:limit]
	}
	return runs
}

// SnapshotRuns implements [api.MacroRunner.SnapshotRuns]: every in-flight
// run, plus a bounded window of the most recently finished ones
// ([Executor.maxSnapshotFinishedRuns], see [DefaultMaxSnapshotFinishedRuns]'s
// own doc comment for the bound and its justification). ADR-020 decision
// 3 makes omitting in-flight runs here fatal (a client connecting mid-run
// would see nothing and have no way to learn one exists), which is why
// those are read with no limit and are never subject to the same bound
// finished runs are.
func (e *Executor) SnapshotRuns(ctx context.Context) ([]store.MacroRunRecord, error) {
	running, err := e.store.ListRunningMacroRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("macro: snapshot running runs: %w", err)
	}

	// ListMacroRuns is newest-first and unfiltered by macro id when
	// macroObjectID is "", so the first e.maxSnapshotFinishedRuns rows
	// after filtering out anything still running are exactly "the most
	// recently finished runs" this method promises.
	recent, err := e.store.ListMacroRuns(ctx, "", store.MaxMacroRunPageSize)
	if err != nil {
		return nil, fmt.Errorf("macro: snapshot recent runs: %w", err)
	}

	out := make([]store.MacroRunRecord, 0, len(running)+e.maxSnapshotFinishedRuns)
	out = append(out, running...)
	finishedCount := 0
	for _, r := range recent {
		if r.State != "finished" {
			continue
		}
		if finishedCount >= e.maxSnapshotFinishedRuns {
			break
		}
		out = append(out, r)
		finishedCount++
	}
	return out, nil
}

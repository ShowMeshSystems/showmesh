package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Step 9 wave 2's own run surface (STEP-9-SPEC.md section
// 6.6): the three routes that submit a macro run and read it back.
// [MacroRunner] (macro_seam.go) is this file's only dependency on the
// executor; internal/coordinator/macro is never imported here — see
// macro_seam.go's own top comment for why that direction is forced.

// maxMacroRunRequestBodyBytes bounds POST /macros/{id}/runs' request body.
// A run submission carries little (an idempotency key, a trigger, and an
// optionally-buffered priorFailures array — see [maxPriorFailuresInRequest]
// for that array's own, tighter bound), so this is smaller than
// [maxShowConfigRequestBodyBytes], not the same generous allowance a
// macro or action DEFINITION gets.
const maxMacroRunRequestBodyBytes = 64 * 1024

// maxPriorFailuresInRequest bounds how many entries
// [v1.CreateMacroRunRequest.PriorFailures] this route will even decode,
// independent of whatever bound the macro executor itself later applies
// when recording them (macro/submit.go's MaxPriorFailuresPerSubmit). Wave
// 2 shared contract section 2: "a caller sending ten thousand entries must
// be refused, not absorbed" — refused HERE, at the route, before a single
// byte of it reaches the executor, rather than trusting a second bound
// three calls deeper to be the only thing standing between an oversized
// array and this handler's own decode cost.
const maxPriorFailuresInRequest = 500

// validMacroRunTriggers is STEP-9-SPEC.md section 6.6's own closed set:
// "Trigger is validated at the route against {"api","plugin","cli","ui"};
// the store does not validate it."
var validMacroRunTriggers = map[string]bool{"api": true, "plugin": true, "cli": true, "ui": true}

// validMacroPriorFailureClasses is STEP-9-SPEC.md section 8.2's own closed
// set, validated at the route for the identical reason Trigger is: the
// executor's own recordPriorFailures (macro/submit.go) groups by Class
// with no validation of its own, so an unrecognized class would otherwise
// reach store.AppendEvent's category verbatim.
var validMacroPriorFailureClasses = map[string]bool{"refused": true, "rejected": true, "unreachable": true}

// --- POST /macros/{id}/runs ---

func (h *handlers) handleSubmitMacroRun(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	macroID := r.PathValue("id")

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxMacroRunRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read macro run request body", err)
		return
	}
	if len(raw) > maxMacroRunRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	var body v1.CreateMacroRunRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body must be valid JSON matching the macro run submission shape"))
		return
	}
	if body.IdempotencyKey == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("idempotencyKey is required"))
		return
	}
	if !validMacroRunTriggers[body.Trigger] {
		writeProblem(w, h.logger, now, invalidParameterProblem(`trigger must be one of "api", "plugin", "cli", or "ui"`))
		return
	}
	if len(body.PriorFailures) > maxPriorFailuresInRequest {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			fmt.Sprintf("priorFailures must not contain more than %d entries", maxPriorFailuresInRequest)))
		return
	}
	priorFailures := make([]MacroPriorFailure, 0, len(body.PriorFailures))
	for i, pf := range body.PriorFailures {
		if !validMacroPriorFailureClasses[pf.Class] {
			writeProblem(w, h.logger, now, invalidParameterProblem(
				fmt.Sprintf(`priorFailures[%d].class must be one of "refused", "rejected", or "unreachable"`, i)))
			return
		}
		at, err := parseTime(pf.At)
		if err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("priorFailures[%d].at must be a valid timestamp", i)))
			return
		}
		priorFailures = append(priorFailures, MacroPriorFailure{
			MacroObjectID: pf.MacroObjectID, Class: pf.Class, HTTPStatus: pf.HTTPStatus, At: at,
		})
	}

	ac := authFromContext(r.Context())
	issuer := FPPCommandIssuer{
		PrincipalID:   ac.result.Principal.ID,
		PrincipalName: ac.result.Principal.Name,
		Form:          ac.result.Form,
		CredentialID:  ac.result.CredentialID,
		ClientAddr:    h.clientAddr(r),
	}

	result, problem, err := h.deps.Macros.SubmitRun(r.Context(), MacroSubmitRequest{
		MacroObjectID:        macroID,
		IdempotencyKey:       body.IdempotencyKey,
		Trigger:              body.Trigger,
		Issuer:               issuer,
		PriorFailures:        priorFailures,
		PriorFailuresDropped: body.PriorFailuresDropped,
	})
	if err != nil {
		h.writeInternalError(w, now, "submit macro run", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	// ADR-031 decision 1 / STEP-9-SPEC.md section 6.6: 202, never 200 or
	// 201 — the run is accepted and not complete. The body is the full run
	// object in its initial state, so a client that never watches still
	// holds the run id and the pinned revisions. jsonWrite always answers
	// 200, so this route writes its own status line rather than reusing it
	// — the one route in this file that cannot use jsonWrite.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(v1.MacroRunSubmitResponse{
		ServerTime: formatTime(now),
		Run:        h.mapMacroRun(r.Context(), result.Run, result.Steps),
		Replay:     result.Replay,
	})
}

// --- GET /macro-runs ---

func (h *handlers) handleListMacroRuns(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	q := r.URL.Query()

	state := q.Get("state")
	if state != "" && state != "running" && state != "finished" {
		writeProblem(w, h.logger, now, invalidParameterProblem(`state must be "running" or "finished" when supplied`))
		return
	}

	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeProblem(w, h.logger, now, invalidParameterProblem("limit must be a non-negative integer"))
			return
		}
		limit = n
	}

	runs, err := h.deps.Macros.ListRuns(r.Context(), MacroRunFilter{
		MacroObjectID: q.Get("macroId"), Show: q.Get("show"), State: state, Limit: limit,
	})
	if err != nil {
		h.writeInternalError(w, now, "list macro runs", err)
		return
	}

	out := make([]v1.MacroRunSummary, 0, len(runs))
	for _, run := range runs {
		out = append(out, mapMacroRunSummary(run))
	}
	jsonWrite(w, v1.MacroRunsListResponse{ServerTime: formatTime(now), Runs: out})
}

// --- GET /macro-runs/{runId} ---

func (h *handlers) handleGetMacroRun(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	runID := r.PathValue("runId")

	result, err := h.deps.Macros.GetRun(r.Context(), runID)
	if errors.Is(err, ErrMacroRunNotFound) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(fmt.Sprintf("no macro run with id %q exists", runID)))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get macro run", err)
		return
	}

	jsonWrite(w, v1.MacroRunResponse{ServerTime: formatTime(now), Run: h.mapMacroRun(r.Context(), result.Run, result.Steps)})
}

// --- mapping: store.MacroRunRecord/MacroRunStepRecord -> v1 wire types ---

// macroRunChangedEventProjection renders run onto a "macroRun.changed"
// event's own fields (STEP-9-SPEC.md section 6.6: "carrying the run id,
// macro id, state, the two booleans, and the reason"), without Seq/ServerTime
// — stream.go's [pendingFrame.materialize] stamps those, exactly as every
// other event kind's own projection does. Used as [Hub.updateRendered]'s
// change-detection key too: byte-identical JSON on two consecutive render
// passes means nothing about this run has changed, so no frame is sent.
func macroRunChangedEventProjection(run store.MacroRunRecord) v1.MacroRunChangedEvent {
	return v1.MacroRunChangedEvent{
		RunID: run.ID, MacroObjectID: run.MacroObjectID, State: run.State,
		Completed: run.Completed, Confirmed: run.Confirmed,
		Reason: run.Reason, AttributionDegraded: run.AttributionDegraded,
	}
}

func mapMacroRunSummary(run store.MacroRunRecord) v1.MacroRunSummary {
	return v1.MacroRunSummary{
		ID: run.ID, MacroObjectID: run.MacroObjectID, MacroRevision: run.MacroRevision, Show: run.Show,
		Trigger: run.Trigger, IssuerPrincipalID: run.IssuerPrincipalID, IssuerPrincipalName: run.IssuerPrincipalName,
		CreatedAt: formatTime(run.CreatedAt), FinishedAt: formatTimePtr(run.FinishedAt),
		State: run.State, Completed: run.Completed, Confirmed: run.Confirmed,
		Reason: run.Reason, AttributionDegraded: run.AttributionDegraded,
	}
}

// mapMacroRun renders a full run plus its steps, resolving each FPP step's
// command detail (STEP-9-SPEC.md section 6.1's "not retained" rendering
// rule — see [MacroRunStepCommand]'s own doc comment) through
// [Dependencies.Commands]. A resolution failure for one step's command is
// logged and that step renders "not_retained" with a stated reason rather
// than failing the whole run read: a command-detail lookup problem must
// never make an otherwise-readable run invisible.
func (h *handlers) mapMacroRun(ctx context.Context, run store.MacroRunRecord, steps []store.MacroRunStepRecord) v1.MacroRun {
	out := v1.MacroRun{
		ID: run.ID, MacroObjectID: run.MacroObjectID, MacroRevision: run.MacroRevision, Show: run.Show,
		Trigger: run.Trigger, IssuerPrincipalID: run.IssuerPrincipalID, IssuerPrincipalName: run.IssuerPrincipalName,
		CreatedAt: formatTime(run.CreatedAt), FinishedAt: formatTimePtr(run.FinishedAt),
		State: run.State, Completed: run.Completed, Confirmed: run.Confirmed,
		Reason: run.Reason, AttributionDegraded: run.AttributionDegraded,
		Steps: make([]v1.MacroRunStep, 0, len(steps)),
	}
	for _, st := range steps {
		out.Steps = append(out.Steps, h.mapMacroRunStep(ctx, st))
	}
	return out
}

// mapMacroRunStep renders st onto the wire. Outcome uses nonEmptyStrPtr
// (mapping.go), the same "" -> null helper used everywhere else in this
// package an optional string must render null rather than blank: st.Outcome
// is "" at the store layer for a step that has not resolved yet
// (buildStepRecords, macro/resolve.go — this package has no store access
// to that constant and reads its own value, "", identically), and
// [v1.MacroRunStep.Outcome]'s own doc comment explains why the wire form
// must be null, not "".
func (h *handlers) mapMacroRunStep(ctx context.Context, st store.MacroRunStepRecord) v1.MacroRunStep {
	return v1.MacroRunStep{
		StepIndex: st.StepIndex, StepID: st.StepID, ActionObjectID: st.ActionObjectID, ActionRevision: st.ActionRevision,
		Integration: st.Integration, SafetyClass: st.SafetyClass, LocalFallbackClass: st.LocalFallbackClass,
		State: st.State, DispatchedAt: formatTimePtr(st.DispatchedAt), ResolvedAt: formatTimePtr(st.ResolvedAt),
		Outcome: nonEmptyStrPtr(st.Outcome), OutcomeState: st.OutcomeState, OutcomeReason: st.OutcomeReason,
		AttributionDegraded: st.AttributionDegraded,
		Command:             h.mapMacroRunStepCommand(ctx, st.CommandID, st.AttributionDegraded),
	}
}

// mapMacroRunStepCommand resolves st's own commandID into
// [v1.MacroRunStepCommand] — see that type's own doc comment for the three
// states. attributionDegraded is the STEP's own recorded value
// (store.MacroRunStepRecord.AttributionDegraded), not read from the
// commands table: a [store.CommandRecord] carries no queryable retained
// column for this fact (it is a property [FPPCommandOutcome] computes at
// dispatch time and this package writes onto the RUN's own step row, per
// ADR-031 decision 5's per-step exemption — see macro_seam.go), so the
// step's own copy is the only durable source for the detail object's
// identical field, and is authoritative for exactly the one dispatch this
// step describes.
func (h *handlers) mapMacroRunStepCommand(ctx context.Context, commandID *string, attributionDegraded bool) v1.MacroRunStepCommand {
	if commandID == nil {
		return v1.MacroRunStepCommand{State: "none", Reason: "this step has no dispatched command: either it is an mqtt step, which never has one, or it has not dispatched yet"}
	}
	id := *commandID
	cmd, err := h.deps.Commands.GetCommand(ctx, id)
	if errors.Is(err, store.ErrCommandNotFound) {
		return v1.MacroRunStepCommand{
			State: "not_retained", ID: &id,
			Reason: "this command's own row has been removed by this coordinator's retention policy; the step's own recorded outcome above is unaffected",
		}
	}
	if err != nil {
		h.logger.Warn("api: failed to resolve macro run step command detail", "commandId", id, "error", err)
		return v1.MacroRunStepCommand{
			State: "not_retained", ID: &id,
			Reason: "this command's detail could not be read; the step's own recorded outcome above is unaffected",
		}
	}
	detail := mapCommandRecordToResult(cmd, attributionDegraded)
	return v1.MacroRunStepCommand{State: "retained", ID: &id, Detail: &detail}
}

// mapCommandRecordToResult renders a stored [store.CommandRecord] as
// [v1.FPPCommandResult] for a HISTORICAL read (a macro run's step), never
// for a fresh dispatch response (fppcommand_handler.go's own mapping,
// which reads from [FPPCommandOutcome], is unaffected and unchanged).
// Replay is always false here: this is not answering a replayed
// submission, it is rendering a command this coordinator already knows
// happened. attributionDegraded is the caller's own value — see
// [handlers.mapMacroRunStepCommand]'s doc comment for why it does not come
// from cmd itself.
//
// cmd.State is this row's LIFECYCLE ("dispatched" | "resolved" —
// fppcommand_dispatch.go's dispatchState/resolvedState), never the
// confirmed/unconfirmed word: that word lives inside cmd.ResultJSON
// ([commandResultPayload], fppcommand_handler.go — the same struct
// dispatchFPPCommand itself writes there, decoded here rather than
// duplicated), which is why this function decodes ResultJSON instead of
// reading cmd.State for the wire's "outcome" field.
func mapCommandRecordToResult(cmd store.CommandRecord, attributionDegraded bool) v1.FPPCommandResult {
	var params map[string]any
	if cmd.ParamsJSON != "" {
		_ = json.Unmarshal([]byte(cmd.ParamsJSON), &params)
	}
	if params == nil {
		params = map[string]any{}
	}
	var result commandResultPayload
	if cmd.ResultJSON != "" {
		_ = json.Unmarshal([]byte(cmd.ResultJSON), &result)
	}
	return v1.FPPCommandResult{
		ID: cmd.ID, IdempotencyKey: cmd.IdempotencyKey, Action: cmd.Action, InstanceID: cmd.TargetID,
		Params: params, Replay: false,
		Outcome: result.Outcome, OutcomeState: cmd.OutcomeState, OutcomeReason: cmd.OutcomeReason,
		AttributionDegraded: attributionDegraded,
		DispatchedAt:        formatTimePtr(cmd.DispatchedAt), ResolvedAt: formatTimePtr(cmd.ResolvedAt),
	}
}

// parseTime parses an RFC 3339 timestamp from a request body field
// ([v1.MacroPriorFailureRequest.At]). time.RFC3339Nano's layout accepts a
// value with no fractional-second component too (Go's reference-time
// parsing treats the ".999999999" pattern as optional), so this one layout
// covers every timestamp this API itself ever produces via [formatTime].
func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

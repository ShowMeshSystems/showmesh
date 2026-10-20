package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// show.sitepower (ADR-065): GET/PUT/revisions, the shutdown sequence
// runner every caller shares, and the System power on/off test commands.

const (
	maxSitePowerConfigRequestBodyBytes = 64 * 1024

	auditActionSitePowerOn  = "show.sitepower.on"
	auditActionSitePowerOff = "show.sitepower.off"

	outcomeWordSkipped = "skipped"
)

func (h *handlers) resolveSitePowerConfig(ctx context.Context) (payload config.SitePowerPayload, obj store.ConfigObjectRecord, rev store.ConfigRevisionRecord, configured bool, err error) {
	obj, err = h.deps.Config.GetConfigObject(ctx, config.ShowSitePowerConfigKind, config.ShowSitePowerConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.SitePowerDefaultPayload, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, nil
	case err != nil:
		return config.SitePowerPayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: get show.sitepower config object: %w", err)
	case obj.CurrentRevision == 0:
		return config.SitePowerDefaultPayload, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, nil
	}
	rev, err = h.deps.Config.GetConfigRevision(ctx, config.ShowSitePowerConfigKind, config.ShowSitePowerConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.SitePowerPayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: get show.sitepower config revision %d: %w", obj.CurrentRevision, err)
	}
	// Read without the existence check: an action deleted since the write
	// is reported when its step runs, never by hiding the whole config.
	payload, verr := config.DecodeSitePowerPayload(rev.PayloadJSON, func(string) bool { return true })
	if verr != nil {
		return config.SitePowerPayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: decode show.sitepower payload: %s", verr.Error())
	}
	return payload, obj, rev, true, nil
}

// handleGetSitePowerConfig serves GET /api/v1/config/show.sitepower.
func (h *handlers) handleGetSitePowerConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	payload, obj, rev, configured, err := h.resolveSitePowerConfig(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "resolve show.sitepower", err)
		return
	}
	if !configured {
		jsonWrite(w, v1.SitePowerConfigResponse{
			ServerTime: formatTime(now), Kind: config.ShowSitePowerConfigKind,
			Payload: mapSitePowerPayload(payload), UpdatedAt: formatTime(now), Source: "default",
		})
		return
	}
	jsonWrite(w, mapSitePowerConfigResponse(now, rev, obj, payload))
}

// handleGetSitePowerConfigRevisions serves GET /api/v1/config/show.sitepower/revisions.
func (h *handlers) handleGetSitePowerConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	revs, err := h.deps.Config.ListConfigRevisions(r.Context(), config.ShowSitePowerConfigKind, config.ShowSitePowerConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list show.sitepower config revisions", err)
		return
	}
	activeRevision := int64(0)
	if n := len(revs); n > 0 {
		activeRevision = revs[n-1].Revision
	}
	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: config.ShowSitePowerConfigKind, Revisions: out})
}

// handlePutSitePowerConfig serves PUT /api/v1/config/show.sitepower, a
// full replacement written in the same transaction as its audit entry.
func (h *handlers) handlePutSitePowerConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	precondition, precondProblem := parseRevisionPrecondition(r)
	if precondProblem != nil {
		writeProblem(w, h.logger, now, *precondProblem)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSitePowerConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("reading request body: %v", err)))
		return
	}
	if len(body) > maxSitePowerConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body too large"))
		return
	}
	payload, verr := config.DecodeSitePowerPayload(string(body), h.emergencyStopActionResolver(ctx))
	if verr != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(verr.Detail))
		return
	}
	payloadJSON, err := config.EncodeSitePowerPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.sitepower config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		currentRevision := int64(0)
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.ShowSitePowerConfigKind, config.ShowSitePowerConfigObjectID); gerr == nil {
			currentRevision = obj.CurrentRevision
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}
		if err := checkRevisionPrecondition(config.ShowSitePowerConfigKind, config.ShowSitePowerConfigObjectID, precondition, currentRevision); err != nil {
			return identity.AuditEntry{}, err
		}
		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.ShowSitePowerConfigKind, ObjectID: config.ShowSitePowerConfigObjectID,
			Revision: nextRevisionNo, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.ShowSitePowerSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.ShowSitePowerConfigKind, config.ShowSitePowerConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.ShowSitePowerConfigKind,
			Params: map[string]any{"revision": nextRevisionNo},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write show.sitepower config revision", writeErr)
		return
	}
	jsonWrite(w, mapSitePowerConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowSitePowerConfigKind, ID: config.ShowSitePowerConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func mapShutdownSequences(seqs []config.ShutdownSequence) []v1.ConfigShutdownSequence {
	out := make([]v1.ConfigShutdownSequence, 0, len(seqs))
	for _, s := range seqs {
		steps := make([]v1.ConfigShutdownStep, 0, len(s.Steps))
		for _, st := range s.Steps {
			steps = append(steps, v1.ConfigShutdownStep(st))
		}
		out = append(out, v1.ConfigShutdownSequence{ID: s.ID, Label: s.Label, Steps: steps})
	}
	return out
}

func mapSitePowerPayload(p config.SitePowerPayload) v1.ConfigSitePowerPayload {
	return v1.ConfigSitePowerPayload{ShutdownSequences: mapShutdownSequences(p.ShutdownSequences), PowerOnActions: nonNilStrings(p.PowerOnActions)}
}

func mapSitePowerConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, payload config.SitePowerPayload) v1.SitePowerConfigResponse {
	return v1.SitePowerConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowSitePowerConfigKind, Revision: rev.Revision,
		Payload:                mapSitePowerPayload(payload),
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

// --- running actions and sequences ---

// dispatchActionByID resolves a show.action by id and dispatches it with
// the deterministic cmdID, so a retried caller does not send it twice.
func (h *handlers) dispatchActionByID(ctx context.Context, actionID, cmdID string, ac authContext, clientAddr string) (label, outcome, reason string) {
	rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.ShowActionConfigKind, actionID)
	if err != nil {
		h.logWarn("site action: failed to resolve action", "actionId", actionID, "error", err)
		return "", outcomeWordFailed, "The action did not run because of a ShowMesh error. Run it by hand."
	}
	if problem != nil {
		return "", outcomeWordFailed, fmt.Sprintf("Action %s no longer exists. Remove it from the list or create it again.", actionID)
	}
	payload, err := decodeShowActionPayloadForRead(rev.PayloadJSON)
	if err != nil {
		h.logWarn("site action: failed to decode action payload", "actionId", actionID, "error", err)
		return "", outcomeWordFailed, "The action's saved settings could not be read. Save the action again."
	}
	auditExempt := payload.SafetyClass != config.ShowSafetyClassNone
	outcome, _, reason, _, _ = h.dispatchActionTarget(ctx, payload, cmdID, strconv.FormatInt(rev.Revision, 10), rev.Revision, ac, clientAddr, auditExempt)
	return payload.Label, outcome, reason
}

// runShutdownSequenceLive runs seq in this process. With noWait every
// action is sent at once in step order and delays are skipped, which is
// hard stop's behaviour. Otherwise steps run in order and a step that
// fails, or does not confirm when it must, stops the sequence.
func (h *handlers) runShutdownSequenceLive(ctx context.Context, seq config.ShutdownSequence, noWait bool, cmdPrefix string, ac authContext, clientAddr string) []v1.ShutdownStepResult {
	results := make([]v1.ShutdownStepResult, len(seq.Steps))
	for i, step := range seq.Steps {
		results[i] = v1.ShutdownStepResult{Step: i + 1, Action: step.Action, DelayMs: step.DelayMs}
	}
	cmdID := func(i int) string { return fmt.Sprintf("%s:%s:%d", cmdPrefix, seq.ID, i) }

	if noWait {
		var wg sync.WaitGroup
		for i, step := range seq.Steps {
			if step.IsDelay() {
				results[i].Outcome = outcomeWordSkipped
				results[i].OutcomeReason = "Hard stop skips waits."
				continue
			}
			wg.Add(1)
			go func(i int, step config.ShutdownStep) {
				defer wg.Done()
				_, results[i].Outcome, results[i].OutcomeReason = h.dispatchActionByID(ctx, step.Action, cmdID(i), ac, clientAddr)
			}(i, step)
		}
		wg.Wait()
		return results
	}

	for i, step := range seq.Steps {
		if step.IsDelay() {
			select {
			case <-time.After(time.Duration(step.DelayMs) * time.Millisecond):
				results[i].Outcome = outcomeWordConfirmed
			case <-ctx.Done():
				results[i].Outcome = outcomeWordFailed
				results[i].OutcomeReason = "The wait was cut short, so the rest of this sequence did not run."
				markSkipped(results[i+1:], "An earlier step did not finish.")
				return results
			}
			continue
		}
		_, outcome, reason := h.dispatchActionByID(ctx, step.Action, cmdID(i), ac, clientAddr)
		results[i].Outcome, results[i].OutcomeReason = outcome, reason
		if shutdownStepStops(step, outcome) {
			markSkipped(results[i+1:], fmt.Sprintf("Step %d did not finish, so this step was not sent.", i+1))
			return results
		}
	}
	return results
}

// shutdownStepStops reports whether outcome ends the sequence: a failed or
// refused action always does, an unconfirmed one only when it must confirm.
func shutdownStepStops(step config.ShutdownStep, outcome string) bool {
	switch outcome {
	case outcomeWordConfirmed:
		return false
	case outcomeWordFailed, outcomeWordRefused:
		return true
	default:
		return step.RequireConfirmation
	}
}

func markSkipped(rest []v1.ShutdownStepResult, reason string) {
	for i := range rest {
		rest[i].Outcome = outcomeWordSkipped
		rest[i].OutcomeReason = reason
	}
}

// runShutdownSequencesLive runs every sequence side by side and returns
// each one's results keyed by sequence id.
func (h *handlers) runShutdownSequencesLive(ctx context.Context, seqs []config.ShutdownSequence, noWait bool, cmdPrefix string, ac authContext, clientAddr string) map[string][]v1.ShutdownStepResult {
	out := make(map[string][]v1.ShutdownStepResult, len(seqs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, seq := range seqs {
		wg.Add(1)
		go func(seq config.ShutdownSequence) {
			defer wg.Done()
			res := h.runShutdownSequenceLive(ctx, seq, noWait, cmdPrefix, ac, clientAddr)
			mu.Lock()
			out[seq.ID] = res
			mu.Unlock()
		}(seq)
	}
	wg.Wait()
	return out
}

// --- System power on / off (test commands) ---

// handleSitePowerOn serves POST /api/v1/site-power/on.
func (h *handlers) handleSitePowerOn(w http.ResponseWriter, r *http.Request) {
	h.startSitePowerRun(w, r, auditActionSitePowerOn)
}

// handleSitePowerOff serves POST /api/v1/site-power/off.
func (h *handlers) handleSitePowerOff(w http.ResponseWriter, r *http.Request) {
	h.startSitePowerRun(w, r, auditActionSitePowerOff)
}

func (h *handlers) startSitePowerRun(w http.ResponseWriter, r *http.Request, action string) {
	now := h.now()
	ctx := context.WithoutCancel(r.Context())

	current, hasCurrent, err := h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil {
		h.writeInternalError(w, now, "read the current night session", err)
		return
	}
	if hasCurrent && current.State != nightStateStopped {
		writeProblem(w, h.logger, now, v1.Problem{
			Type: ProblemTypeConflict, Title: "A night is running", Status: http.StatusConflict,
			Detail: "A night session is open, so the test power buttons are off. End the session first.",
		})
		return
	}
	payload, _, _, _, err := h.resolveSitePowerConfig(ctx)
	if err != nil {
		h.writeInternalError(w, now, "resolve show.sitepower", err)
		return
	}
	var started []string
	if action == auditActionSitePowerOn {
		started = nonNilStrings(payload.PowerOnActions)
	} else {
		started = payload.SequenceIDs()
	}
	if len(started) == 0 {
		writeProblem(w, h.logger, now, v1.Problem{
			Type: ProblemTypeConflict, Title: "Nothing to run", Status: http.StatusConflict,
			Detail: "No power actions are set up. Add them under Settings, Site settings.",
		})
		return
	}
	if !h.sitePowerBusy.CompareAndSwap(false, true) {
		writeProblem(w, h.logger, now, v1.Problem{
			Type: ProblemTypeConflict, Title: "Already running", Status: http.StatusConflict,
			Detail: "A power run is already going. Wait for it to finish.",
		})
		return
	}

	ac := authFromContext(r.Context())
	clientAddr := h.clientAddr(r)
	issuer := h.emergencyStopIssuer(now, ac, clientAddr)
	runID := uuid.NewString()
	cmdPrefix := "sitepower:" + runID

	h.sitePowerRuns.Add(1)
	go func() {
		defer h.sitePowerRuns.Done()
		defer h.sitePowerBusy.Store(false)
		params := map[string]any{"runId": runID}
		if action == auditActionSitePowerOn {
			results := make([]v1.ShutdownStepResult, 0, len(payload.PowerOnActions))
			for i, id := range payload.PowerOnActions {
				_, outcome, reason := h.dispatchActionByID(ctx, id, fmt.Sprintf("%s:on:%d", cmdPrefix, i), ac, clientAddr)
				results = append(results, v1.ShutdownStepResult{Step: i + 1, Action: id, Outcome: outcome, OutcomeReason: reason})
			}
			params["actions"] = results
		} else {
			params["sequences"] = h.runShutdownSequencesLive(ctx, payload.ShutdownSequences, false, cmdPrefix, ac, clientAddr)
		}
		entry := issuer
		entry.Action = action
		entry.Target = config.ShowSitePowerConfigKind
		entry.Kind = identity.AuditOutcome
		entry.Params = params
		h.writeBestEffortAuditBounded(ctx, h.now(), degradedAttributionReasonPostDispatch, entry)
	}()

	message := "Power on started. Results go to the activity log."
	if action == auditActionSitePowerOff {
		message = "Power off started. Results go to the activity log."
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	jsonEncode(w, v1.SitePowerRunResponse{ServerTime: formatTime(now), RunID: runID, Started: started, Message: message})
}

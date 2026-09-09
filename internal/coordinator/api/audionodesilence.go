package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the coordinator-side dispatch for "audio.node.silence"
// (internal/agent/audionodesilenceops.go): unlike every audio.session.*/
// audio.gain.*/audio.output.* action audiodispatch.go's own
// executeAudioSessionDispatch carries, audio.node.silence is node-scoped
// (no sessionId, no revision) and its evidence is a LIST of per-session
// outcomes plus a count, not audiodispatch.go's single sessionId/outcome
// pair (see internal/agent/audionodesilenceops.go's silenceNode). This
// dispatch reuses audiodispatch.go's shared helpers
// (audioResultCorrelates, the replay-conflict problem builders, the
// degraded-attribution machinery) but keeps its own execute function so a
// dispatch response reports the agent's real evidence shape rather than
// forcing it through mapResultOutcome's single-outcome extraction, which
// would read the sessions list's "outcome" key as absent and misreport
// every dispatch as unconfirmable.

const audioNodeSilenceAction = "audio.node.silence"

// audioNodeSilenceRequestFields is every top-level field
// [v1.AudioNodeSilenceRequest] accepts: audio.node.silence takes no
// params, so idempotencyKey is the only one, matching
// audioCommandRequestFields' identical role one file over.
var audioNodeSilenceRequestFields = map[string]bool{"idempotencyKey": true}

// decodeAudioNodeSilenceRequestBody decodes body into a
// [v1.AudioNodeSilenceRequest], rejecting any top-level key other than
// "idempotencyKey" and any content after the single JSON object. An
// empty body decodes to the zero value, matching
// decodeAudioSessionCommandRequestBody's identical tolerance.
func decodeAudioNodeSilenceRequestBody(body io.Reader) (v1.AudioNodeSilenceRequest, error) {
	dec := json.NewDecoder(io.LimitReader(body, maxAudioCommandRequestBodyBytes+1))

	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil {
		if errors.Is(err, io.EOF) {
			return v1.AudioNodeSilenceRequest{}, nil
		}
		return v1.AudioNodeSilenceRequest{}, fmt.Errorf(
			`request body must be a JSON object matching {"idempotencyKey":string?}: %w`, err)
	}
	for key := range top {
		if !audioNodeSilenceRequestFields[key] {
			return v1.AudioNodeSilenceRequest{}, fmt.Errorf(
				`unknown field %q; audio.node.silence takes no params, so the only accepted field is "idempotencyKey"`, key)
		}
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return v1.AudioNodeSilenceRequest{}, errors.New("request body must contain exactly one JSON object; unexpected data after it")
		}
		return v1.AudioNodeSilenceRequest{}, fmt.Errorf("reading request body: %w", err)
	}

	var req v1.AudioNodeSilenceRequest
	if raw, ok := top["idempotencyKey"]; ok {
		if err := json.Unmarshal(raw, &req.IdempotencyKey); err != nil {
			return v1.AudioNodeSilenceRequest{}, fmt.Errorf(`"idempotencyKey" must be a string: %w`, err)
		}
	}
	return req, nil
}

// handleAudioNodeSilence dispatches audio.node.silence to nodeId, guarded
// by audio:command (mux registration, api.go).
func (h *handlers) handleAudioNodeSilence(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(audioHandlerWriteDeadline()))

	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}

	body, err := decodeAudioNodeSilenceRequestBody(r.Body)
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	ac := authFromContext(ctx)
	issuerID := ac.result.Principal.ID
	issuerName := ac.result.Principal.Name
	if issuerID == "" {
		issuerID = "unknown"
	}

	idempotencyKey := body.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = uuid.NewString()
	}

	result, problem, err := h.executeAudioNodeSilenceDispatch(ctx, now, AudioNodeSilenceDispatchInput{
		NodeID: nodeID, IdempotencyKey: idempotencyKey,
		IssuerID: issuerID, IssuerName: issuerName,
		IssuerForm: ac.result.Form, IssuerCredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
	})
	if err != nil {
		h.writeInternalError(w, now, "dispatch audio node silence command", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	jsonWrite(w, v1.AudioNodeSilenceResponse{ServerTime: formatTime(h.now()), Command: result})
}

// AudioNodeSilenceDispatchInput is [handlers.executeAudioNodeSilenceDispatch]'s
// input, mirroring [AudioDispatchInput]'s identical exported-for-
// cross-package-dispatch role minus the fields audio.node.silence does
// not carry (SessionID, Params, Revision).
type AudioNodeSilenceDispatchInput struct {
	NodeID         string
	IdempotencyKey string
	IssuerID       string
	IssuerName     string

	IssuerForm         identity.CredentialForm
	IssuerCredentialID string
	ClientAddr         string
}

// audioNodeSilenceResultPayload is the JSON this file persists into
// store.CommandRecord.ResultJSON, mirroring audioSessionResultPayload's
// identical replay-without-redispatch role one file over, widened to
// carry the sessions list and count evidence.Value holds instead of a
// single outcome/reason pair.
type audioNodeSilenceResultPayload struct {
	Outcome       string                             `json:"outcome"`
	Reason        string                             `json:"reason"`
	SessionsFound int                                `json:"sessionsFound"`
	Sessions      []v1.AudioNodeSilenceSessionResult `json:"sessions"`
}

// executeAudioNodeSilenceDispatch records the command and its ADR-024
// dispatch audit entry atomically, publishes it to the node's cmd topic,
// and awaits the command's own result on its result topic - the same
// shape as executeAudioSessionDispatch (audiodispatch.go), narrowed to
// this action's own node scope: no SessionID, no Revision, no
// persistAudioSessionDesiredState call (audio.node.silence carries no
// desired state of its own to persist). A nil error with a non-nil
// problem means "the request was refused"; a non-nil error means an
// internal failure this coordinator cannot attribute to the caller.
func (h *handlers) executeAudioNodeSilenceDispatch(ctx context.Context, now time.Time, in AudioNodeSilenceDispatchInput) (v1.AudioNodeSilenceResult, *v1.Problem, error) {
	if h.deps.Commands == nil {
		return v1.AudioNodeSilenceResult{}, nil, errors.New("no command store is configured")
	}

	params := map[string]any{}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("encode params: %w", err)
	}

	commandID := uuid.NewString()
	rec := store.CommandRecord{
		ID: commandID, IdempotencyKey: in.IdempotencyKey, Action: audioNodeSilenceAction,
		TargetKind: "node", TargetID: in.NodeID, ParamsJSON: string(paramsJSON),
		IssuerPrincipalID: in.IssuerID, IssuerPrincipalName: in.IssuerName,
		ConfirmationMethod: "evidence", State: "pending",
	}
	dispatchEntry := identity.AuditEntry{
		Timestamp: now, PrincipalID: in.IssuerID, PrincipalName: in.IssuerName,
		Form: in.IssuerForm, CredentialID: in.IssuerCredentialID, ClientAddr: in.ClientAddr,
		Action: audioNodeSilenceAction, Target: in.NodeID, IdempotencyKey: in.IdempotencyKey,
		Kind: identity.AuditDispatch, CommandID: commandID,
	}

	auditErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if _, err := tx.InsertCommand(ctx, rec); err != nil {
			return identity.AuditEntry{}, err
		}
		return dispatchEntry, nil
	})

	var dispatchDegraded bool
	var dup *store.DuplicateCommandError
	switch {
	case errors.As(auditErr, &dup):
		result, problem := resolveAudioNodeSilenceReplay(dup.Existing, in.NodeID, string(paramsJSON))
		return result, problem, nil
	case errors.Is(auditErr, identity.ErrAuditWrite):
		// ADR-024 decision 11, amended 2026-08-26 (owner ruling): an
		// audit-store outage never blocks this dispatch. audio.node.silence
		// is a member of audioSafetyExemptActions (audiodispatch.go), so the
		// degraded reason reported is the safety-class one, not the generic
		// audit-never-blocks one.
		if _, err := h.deps.Commands.InsertCommand(ctx, rec); err != nil {
			if errors.As(err, &dup) {
				result, problem := resolveAudioNodeSilenceReplay(dup.Existing, in.NodeID, string(paramsJSON))
				return result, problem, nil
			}
			return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("insert audio command: %w", err)
		}
		degradedReason := degradedAttributionReasonAuditNeverBlocks
		if audioSafetyExemptActions[audioNodeSilenceAction] {
			degradedReason = degradedAttributionReasonSafetyClassExemption
		}
		h.reportDegradedAttribution(now, dispatchEntry, auditErr, degradedReason)
		dispatchDegraded = true
	case auditErr != nil:
		return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("insert audio command: %w", auditErr)
	}

	cmdTopic, err := mqttproto.CmdTopic(in.NodeID)
	if err != nil {
		return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("build cmd topic: %w", err)
	}
	resultTopic, err := mqttproto.ResultTopic(in.NodeID, commandID)
	if err != nil {
		return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("build result topic: %w", err)
	}

	// audio.node.silence is deliberately absent from
	// audioCommandDeadlineActions (audiodispatch.go): an unconditional
	// emergency stop must never carry a wire staleness deadline that could
	// give an agent grounds to refuse it.
	payload := mqttproto.CmdPayload{
		CommandID: commandID, IdempotencyKey: in.IdempotencyKey, Action: audioNodeSilenceAction,
		Target: mqttproto.CmdTarget{Kind: "node", ID: in.NodeID}, Params: params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: in.IssuerID, PrincipalName: in.IssuerName},
		ConfirmationMethod: "evidence",
	}
	env, err := mqttproto.NewCmdEnvelope(func() time.Time { return now }, in.NodeID, payload)
	if err != nil {
		return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("build cmd envelope: %w", err)
	}
	rawEnv, err := json.Marshal(env)
	if err != nil {
		return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("marshal cmd envelope: %w", err)
	}

	// From here on, every write is on bgCtx, matching
	// executeAudioSessionDispatch's identical bgCtx cutover reasoning.
	bgCtx := context.WithoutCancel(ctx)

	dispatchedAt := now
	markDispatched := func(state, outcomeState, outcomeReason string, resultJSON string) {
		s, os_, or := state, outcomeState, outcomeReason
		rj := resultJSON
		if err := h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{
			DispatchedAt: &dispatchedAt, ResolvedAt: &dispatchedAt,
			State: &s, ResultJSON: &rj, OutcomeState: &os_, OutcomeReason: &or,
		}); err != nil {
			h.logWarn("failed to record audio command outcome", "commandId", commandID, "error", err)
		}
	}
	markUndispatched := func(state, outcomeState, outcomeReason string, resultJSON string) {
		s, os_, or := state, outcomeState, outcomeReason
		rj := resultJSON
		resolvedAt := h.now()
		if err := h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{
			ResolvedAt: &resolvedAt,
			State:      &s, ResultJSON: &rj, OutcomeState: &os_, OutcomeReason: &or,
		}); err != nil {
			h.logWarn("failed to record audio command outcome", "commandId", commandID, "error", err)
		}
	}

	msg, err := h.deps.AudioPublisher.AwaitResponse(bgCtx, broker.ResponseRequest{
		PublishTopic: cmdTopic, PublishPayload: rawEnv,
		PublishQoS: mqttproto.CmdDeliveryPolicy.QoS, PublishRetain: mqttproto.CmdDeliveryPolicy.Retain,
		ResponseTopic: resultTopic, ResponseQoS: mqttproto.ResultDeliveryPolicy.QoS,
		Deadline: audioCommandConfirmDeadline,
		Match: func(m broker.Message) bool {
			return audioResultCorrelates(m.Payload, in.NodeID, commandID, in.IdempotencyKey, audioNodeSilenceAction)
		},
	})
	if err != nil {
		reason := err.Error()
		if errors.Is(err, broker.ErrResponseFailedBeforePublish) {
			markUndispatched("failed", "collection_failed", reason, "{}")
			return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("await result: %w", err)
		}
		markDispatched("dispatched", "collection_failed", reason, "{}")
		if errors.Is(err, broker.ErrResponseDeadlineExceeded) {
			result := v1.AudioNodeSilenceResult{
				CommandID: commandID, IdempotencyKey: in.IdempotencyKey, Action: audioNodeSilenceAction,
				NodeID:  in.NodeID,
				Outcome: "unconfirmable", Reason: "no result received from the node before the deadline",
				Sessions:     []v1.AudioNodeSilenceSessionResult{},
				DispatchedAt: formatTime(dispatchedAt),
			}
			outcomeDegraded := h.writeBestEffortAuditBounded(bgCtx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
				Timestamp: now, PrincipalID: in.IssuerID, PrincipalName: in.IssuerName,
				Form: in.IssuerForm, CredentialID: in.IssuerCredentialID, ClientAddr: in.ClientAddr,
				Action: audioNodeSilenceAction, Target: in.NodeID, IdempotencyKey: in.IdempotencyKey,
				Kind: identity.AuditOutcome, CommandID: commandID,
				Outcome: result.Outcome, OutcomeState: "collection_failed", OutcomeReason: result.Reason,
			})
			result.AttributionDegraded = dispatchDegraded || outcomeDegraded
			return result, nil, nil
		}
		return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("await result: %w", err)
	}

	env2, err := mqttproto.DecodeEnvelope(msg.Payload)
	if err != nil {
		markDispatched("dispatched", "collection_failed", "result payload did not decode", "{}")
		return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("decode result envelope: %w", err)
	}
	res, err := mqttproto.DecodeResultPayload(env2)
	if err != nil {
		markDispatched("dispatched", "collection_failed", "result payload did not decode", "{}")
		return v1.AudioNodeSilenceResult{}, nil, fmt.Errorf("decode result payload: %w", err)
	}

	// The wire-level outcome/reason (confirmed/unconfirmed/refused/failed)
	// is reported AS-IS, unlike mapResultOutcome's per-session extraction
	// one file over: a refusal (an agent that does not know this
	// operation) never carries Evidence at all, and res.Reason already IS
	// that agent's own refusal text, nothing here rewrites or discards it.
	sessionsFound, sessions := decodeAudioNodeSilenceEvidence(res)
	resultJSON, _ := json.Marshal(audioNodeSilenceResultPayload{
		Outcome: res.Outcome, Reason: res.Reason, SessionsFound: sessionsFound, Sessions: sessions,
	})
	resolvedAt := now
	state := "resolved"
	outcomeState := "current"
	outcomeReason := res.Reason
	if err := h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, ResolvedAt: &resolvedAt,
		State: &state, ResultJSON: strPtr(string(resultJSON)), OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	}); err != nil {
		h.logWarn("failed to record audio command outcome", "commandId", commandID, "error", err)
	}

	outcomeDegraded := h.writeBestEffortAuditBounded(bgCtx, resolvedAt, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: in.IssuerID, PrincipalName: in.IssuerName,
		Form: in.IssuerForm, CredentialID: in.IssuerCredentialID, ClientAddr: in.ClientAddr,
		Action: audioNodeSilenceAction, Target: in.NodeID, IdempotencyKey: in.IdempotencyKey,
		Kind: identity.AuditOutcome, CommandID: commandID,
		Outcome: res.Outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
	})

	resolvedStr := formatTime(resolvedAt)
	return v1.AudioNodeSilenceResult{
		CommandID: commandID, IdempotencyKey: in.IdempotencyKey, Action: audioNodeSilenceAction,
		NodeID:  in.NodeID,
		Outcome: res.Outcome, Reason: res.Reason,
		SessionsFound: sessionsFound, Sessions: sessions,
		DispatchedAt: formatTime(dispatchedAt), ResolvedAt: &resolvedStr,
		AttributionDegraded: dispatchDegraded || outcomeDegraded,
	}, nil, nil
}

// decodeAudioNodeSilenceEvidence extracts audio.node.silence's own
// evidence shape (internal/agent/audionodesilenceops.go's silenceNode):
// {"sessionsFound": N, "sessions": [{"sessionId","outcome","reason"}, ...]}.
// Absent or malformed evidence (a refusal never carries any) decodes to
// zero sessions, never fabricated ones.
func decodeAudioNodeSilenceEvidence(res mqttproto.ResultPayload) (int, []v1.AudioNodeSilenceSessionResult) {
	// []v1.AudioNodeSilenceSessionResult{}, never nil, on every return:
	// the wire contract's "sessions" is a required array (never null,
	// per api/openapi.yaml's AudioNodeSilenceResult schema), and a nil
	// Go slice encodes to JSON null.
	if res.Evidence == nil {
		return 0, []v1.AudioNodeSilenceSessionResult{}
	}
	v, ok := res.Evidence.Value.(map[string]any)
	if !ok {
		return 0, []v1.AudioNodeSilenceSessionResult{}
	}
	sessionsFound := 0
	if n, ok := v["sessionsFound"].(float64); ok {
		sessionsFound = int(n)
	}
	rawSessions, _ := v["sessions"].([]any)
	sessions := make([]v1.AudioNodeSilenceSessionResult, 0, len(rawSessions))
	for _, rs := range rawSessions {
		m, ok := rs.(map[string]any)
		if !ok {
			continue
		}
		sessionID, _ := m["sessionId"].(string)
		outcome, _ := m["outcome"].(string)
		reason, _ := m["reason"].(string)
		sessions = append(sessions, v1.AudioNodeSilenceSessionResult{SessionID: sessionID, Outcome: outcome, Reason: reason})
	}
	return sessionsFound, sessions
}

// resolveAudioNodeSilenceReplay decides what a reused idempotency key
// means, mirroring resolveAudioSessionReplay's identical decision one
// file over, minus the session-id check that dispatch does not apply here:
// a genuine replay (same node, same params - always "{}" for this action,
// since audio.node.silence takes none) returns the existing outcome
// flagged replay:true; a conflict (a different action, or the SAME
// action against a different node) is a 409, never silently answered.
func resolveAudioNodeSilenceReplay(existing store.CommandRecord, requestedNodeID, requestedParamsJSON string) (v1.AudioNodeSilenceResult, *v1.Problem) {
	if existing.Action != audioNodeSilenceAction || existing.TargetID != requestedNodeID {
		p := audioSessionReplayConflictProblem(existing.ID, existing.Action, existing.TargetID, audioNodeSilenceAction, requestedNodeID)
		return v1.AudioNodeSilenceResult{}, &p
	}
	existingParamsJSON := existing.ParamsJSON
	if existingParamsJSON == "" {
		existingParamsJSON = "{}"
	}
	if existingParamsJSON != requestedParamsJSON {
		p := audioSessionReplayParamsConflictProblem(existing.ID, existing.Action, existingParamsJSON, requestedParamsJSON)
		return v1.AudioNodeSilenceResult{}, &p
	}
	return audioNodeSilenceResultFromRecord(existing, true), nil
}

// audioNodeSilenceResultFromRecord mirrors audioSessionResultFromRecord's
// identical replay-rehydration role one file over, decoding
// audioNodeSilenceResultPayload instead of audioSessionResultPayload.
func audioNodeSilenceResultFromRecord(rec store.CommandRecord, replay bool) v1.AudioNodeSilenceResult {
	var resolvedAt *string
	if rec.ResolvedAt != nil {
		s := formatTime(*rec.ResolvedAt)
		resolvedAt = &s
	}
	dispatchedAt := ""
	if rec.DispatchedAt != nil {
		dispatchedAt = formatTime(*rec.DispatchedAt)
	}
	var payload audioNodeSilenceResultPayload
	_ = json.Unmarshal([]byte(rec.ResultJSON), &payload)
	sessions := payload.Sessions
	if sessions == nil {
		// A pending replay (rec.ResultJSON still "{}") or a corrupt record
		// must still encode "sessions" as [], never JSON null - see
		// decodeAudioNodeSilenceEvidence's identical guard.
		sessions = []v1.AudioNodeSilenceSessionResult{}
	}
	return v1.AudioNodeSilenceResult{
		CommandID: rec.ID, IdempotencyKey: rec.IdempotencyKey, Action: rec.Action,
		NodeID: rec.TargetID, Replay: replay,
		Outcome: payload.Outcome, Reason: payload.Reason,
		SessionsFound: payload.SessionsFound, Sessions: sessions,
		DispatchedAt: dispatchedAt, ResolvedAt: resolvedAt,
	}
}

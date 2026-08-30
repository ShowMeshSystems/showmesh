package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the coordinator-side dispatch for every audio.session.*,
// audio.gain.*, and audio.output.* operation the agent ships: apply,
// prepare, start, pause, resume, seek, advance, stop, clear, gain.set,
// gain.fade, output.mute, output.unmute. It is deliberately ONE shared
// dispatch core behind thin per-action routes, unlike renderdispatch.go's
// own richer per-action param resolution — a session command's params
// already arrive complete from the caller (no coordinator-side asset
// lookup the way render.surface.apply's sequenceId needs), so there is
// nothing this file needs to resolve on the operator's behalf.
//
// Confirmation here does NOT poll a collector the way render dispatch
// does: it waits on the dispatched command's own result topic
// ([mqttproto.ResultTopic]) via [AudioSessionPublisher.AwaitResponse],
// because the fake session engine's own state transitions are
// synchronous and the command's ResultPayload.Evidence — collected after
// dispatch inside internal/agent's own CommandHandler, per ADR-003 — is
// already the right evidence. A real pipeline backend whose transitions
// are genuinely asynchronous would need this reconsidered; see
// AUDIO-ENGINE.md.
//
// Insert-plus-dispatch-audit runs atomically via [identity.Service.
// AuditedWrite], mirroring resolumeaction.go's identical shape. ADR-024
// decision 11, amended 2026-08-26 (owner ruling): an audit-write failure
// never fails this dispatch closed for ANY audio.* action: every one
// proceeds with degraded attribution. audioSafetyExemptActions
// (audio.session.stop/clear, audio.output.mute) still exists because it
// names this subsystem's own blackout-equivalent set and still picks the
// degradedAttributionReasonSafetyClassExemption justification for those
// three actions specifically, distinct from
// degradedAttributionReasonAuditNeverBlocks for every other one; it no
// longer gates whether the dispatch proceeds at all.

var scopeAudioCommand = identity.ScopeAudioCommand

var audioSessionIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// audioSafetyExemptActions is this file's own audience-audio equivalent of
// ADR-024 decision 11's blackout/stop/power-off safety class: muting the
// output is this subsystem's blackout, so it is grouped alongside the two
// ways to silence a session. Since the 2026-08-26 amendment it no longer
// decides whether an audit-write failure blocks dispatch (nothing does,
// for any audio.* action); it only picks
// degradedAttributionReasonSafetyClassExemption over
// degradedAttributionReasonAuditNeverBlocks as the reported reason, so an
// investigator can still tell "this is the blackout-equivalent set" from
// "this is every other action" in the record.
var audioSafetyExemptActions = map[string]bool{
	"audio.session.stop":  true,
	"audio.session.clear": true,
	"audio.output.mute":   true,
}

const (
	// audioCommandConfirmDeadline bounds how long a dispatch waits, via
	// [broker.BrokerManager.AwaitResponse], for the dispatched command's
	// own result to arrive on its result topic. SHOWMESH HYPOTHESIS, NOT
	// MEASURED, matching renderCommandConfirmDeadline's identical
	// posture one file over — no bench data exists yet for this path.
	audioCommandConfirmDeadline = 15 * time.Second

	audioHandlerWriteDeadlineMargin = 10 * time.Second
	maxAudioCommandRequestBodyBytes = 16 << 10
)

func audioHandlerWriteDeadline() time.Duration {
	return audioCommandConfirmDeadline + audioHandlerWriteDeadlineMargin
}

// AudioSessionPublisher is the coordinator's MQTT publish-and-await
// capability this file depends on, declared at the consumer exactly as
// [RenderPublisher] is — *broker.BrokerManager already satisfies this
// with no adapter.
type AudioSessionPublisher interface {
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
	AwaitResponse(ctx context.Context, req broker.ResponseRequest) (broker.Message, error)
}

// AudioSessionStore is this file's durable-record dependency — see
// [Dependencies.AudioSessions]'s doc comment. GetAudioSession lets a
// dispatch merge a command's own params onto the session's prior desired
// state rather than replacing it outright with one command's own
// (commonly partial) params.
type AudioSessionStore interface {
	PutAudioSession(ctx context.Context, rec store.AudioSessionRecord) error
	GetAudioSession(ctx context.Context, id string) (store.AudioSessionRecord, error)
}

// noAudioSessionPublisher is [Dependencies.AudioPublisher]'s no-op
// default: every audio.session.* dispatch fails loudly rather than
// silently pretending a command reached a node.
type noAudioSessionPublisher struct{}

var errAudioPublisherNotConfigured = errors.New("api: no audio session command publisher is configured on this coordinator")

func (noAudioSessionPublisher) Publish(context.Context, string, byte, bool, []byte) error {
	return errAudioPublisherNotConfigured
}

func (noAudioSessionPublisher) AwaitResponse(context.Context, broker.ResponseRequest) (broker.Message, error) {
	return broker.Message{}, errAudioPublisherNotConfigured
}

// noAudioSessionStore is [Dependencies.AudioSessions]'s no-op default:
// every dispatch still runs (see that field's doc comment) with its
// durable-record write silently skipped.
type noAudioSessionStore struct{}

func (noAudioSessionStore) PutAudioSession(context.Context, store.AudioSessionRecord) error {
	return nil
}

func (noAudioSessionStore) GetAudioSession(context.Context, string) (store.AudioSessionRecord, error) {
	return store.AudioSessionRecord{}, store.ErrAudioSessionNotFound
}

// audioCommandRequestFields is every top-level field
// [v1.AudioSessionCommandRequest] accepts — used to reject an unknown
// field and, together with [decodeAudioSessionCommandRequestBody]'s own
// trailing-content check, to keep this endpoint's request body as
// strictly validated as this codebase's config PUT bodies (see e.g.
// decodeFPPEndpointsConfigPutBody).
var audioCommandRequestFields = map[string]bool{"revision": true, "idempotencyKey": true, "params": true}

// decodeAudioSessionCommandRequestBody decodes body into a
// [v1.AudioSessionCommandRequest], rejecting any top-level key other than
// "revision"/"idempotencyKey"/"params" and any content after the single
// JSON object. An empty body decodes to the zero value, matching this
// endpoint's pre-existing tolerance for an omitted body.
func decodeAudioSessionCommandRequestBody(body io.Reader) (v1.AudioSessionCommandRequest, error) {
	dec := json.NewDecoder(io.LimitReader(body, maxAudioCommandRequestBodyBytes+1))

	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil {
		if errors.Is(err, io.EOF) {
			return v1.AudioSessionCommandRequest{}, nil
		}
		return v1.AudioSessionCommandRequest{}, fmt.Errorf(
			`request body must be a JSON object matching {"revision":number,"idempotencyKey":string?,"params":object?}: %w`, err)
	}
	for key := range top {
		if !audioCommandRequestFields[key] {
			return v1.AudioSessionCommandRequest{}, fmt.Errorf(
				`unknown field %q; the accepted fields are "revision","idempotencyKey","params"`, key)
		}
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return v1.AudioSessionCommandRequest{}, errors.New("request body must contain exactly one JSON object; unexpected data after it")
		}
		return v1.AudioSessionCommandRequest{}, fmt.Errorf("reading request body: %w", err)
	}

	var req v1.AudioSessionCommandRequest
	if raw, ok := top["revision"]; ok {
		if err := json.Unmarshal(raw, &req.Revision); err != nil {
			return v1.AudioSessionCommandRequest{}, fmt.Errorf(`"revision" must be a non-negative integer: %w`, err)
		}
	}
	if raw, ok := top["idempotencyKey"]; ok {
		if err := json.Unmarshal(raw, &req.IdempotencyKey); err != nil {
			return v1.AudioSessionCommandRequest{}, fmt.Errorf(`"idempotencyKey" must be a string: %w`, err)
		}
	}
	if raw, ok := top["params"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &req.Params); err != nil {
			return v1.AudioSessionCommandRequest{}, fmt.Errorf(`"params" must be a JSON object: %w`, err)
		}
	}
	return req, nil
}

func (h *handlers) dispatchAudioSessionCommand(w http.ResponseWriter, r *http.Request, action string) {
	now := h.now()
	ctx := r.Context()
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(audioHandlerWriteDeadline()))

	nodeID := r.PathValue("nodeId")
	sessionID := r.PathValue("sessionId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}
	if !audioSessionIDPattern.MatchString(sessionID) {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("sessionId %q is not a safe identifier (must match %s)", sessionID, audioSessionIDPattern.String())))
		return
	}

	body, err := decodeAudioSessionCommandRequestBody(r.Body)
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	params := body.Params
	if params == nil {
		params = map[string]any{}
	}
	// The decibel boundary (audiogaindb.go): an operator sends dB, the
	// node receives the linear multiplier it has always received.
	if problem := convertAudioGainParamsToLinear(action, params); problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	params["sessionId"] = sessionID

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
	// invocationId travels through params so internal/agent's session
	// layer can key its own pkg/audio.RevisionState by it without this
	// coordinator needing a second identifier: by
	// convention it is the SAME value as the command's own idempotency
	// key, so a redelivered command and a redelivered session invocation
	// are the same event twice, never two different ones.
	params["invocationId"] = idempotencyKey
	// revision travels through params for the identical reason: the
	// node's own RevisionState.Apply is what actually enforces "revision
	// only advances" (pkg/audio/identity.go) — without this key present,
	// internal/agent/audiosessionops.go's parseAudioSessionCommon refuses
	// every dispatch outright with "params.revision is required".
	params["revision"] = body.Revision

	result, problem, err := h.executeAudioSessionDispatch(ctx, now, audioDispatchInput{
		Action: action, NodeID: nodeID, SessionID: sessionID, Params: params,
		Revision: body.Revision, IdempotencyKey: idempotencyKey,
		IssuerID: issuerID, IssuerName: issuerName,
		IssuerForm: ac.result.Form, IssuerCredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
	})
	if err != nil {
		h.writeInternalError(w, now, "dispatch audio session command", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	jsonWrite(w, v1.AudioSessionCommandResponse{ServerTime: formatTime(h.now()), Command: result})
}

type audioDispatchInput struct {
	Action         string
	NodeID         string
	SessionID      string
	Params         map[string]any
	Revision       uint64
	IdempotencyKey string
	IssuerID       string
	IssuerName     string

	IssuerForm         identity.CredentialForm
	IssuerCredentialID string
	ClientAddr         string
}

// executeAudioSessionDispatch records the command and its ADR-024
// dispatch audit entry atomically (idempotency-first, matching render's
// own rule), publishes it to the node's cmd topic, and awaits the
// command's own result on its result topic — see this file's doc comment
// for why that replaces render dispatch's collector poll here. A nil
// error with a non-nil problem means "the request was refused"; a
// non-nil error means an internal failure this coordinator cannot
// attribute to the caller.
func (h *handlers) executeAudioSessionDispatch(ctx context.Context, now time.Time, in audioDispatchInput) (v1.AudioSessionCommandResult, *v1.Problem, error) {
	if h.deps.Commands == nil {
		return v1.AudioSessionCommandResult{}, nil, errors.New("no command store is configured")
	}

	paramsJSON, err := json.Marshal(in.Params)
	if err != nil {
		return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("encode params: %w", err)
	}

	commandID := uuid.NewString()
	rec := store.CommandRecord{
		ID: commandID, IdempotencyKey: in.IdempotencyKey, Action: in.Action,
		TargetKind: "node", TargetID: in.NodeID, ParamsJSON: string(paramsJSON),
		IssuerPrincipalID: in.IssuerID, IssuerPrincipalName: in.IssuerName,
		ConfirmationMethod: "evidence", State: "pending",
	}
	dispatchEntry := identity.AuditEntry{
		Timestamp: now, PrincipalID: in.IssuerID, PrincipalName: in.IssuerName,
		Form: in.IssuerForm, CredentialID: in.IssuerCredentialID, ClientAddr: in.ClientAddr,
		Action: in.Action, Target: in.NodeID, IdempotencyKey: in.IdempotencyKey,
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
		result, problem := resolveAudioSessionReplay(dup.Existing, in.Action, in.NodeID, in.SessionID, string(paramsJSON))
		return result, problem, nil
	case errors.Is(auditErr, identity.ErrAuditWrite):
		// ADR-024 decision 11, amended 2026-08-26 (owner ruling): an
		// audit-store outage never blocks a command dispatch, for any
		// audio action, not only this file's own audioSafetyExemptActions
		// (audio.session.stop/clear, audio.output.mute). Redo the insert
		// through the plain, non-transactional store method and proceed
		// with degraded attribution — mirroring dispatchFPPCommand's/
		// resolumeaction.go's identical fallback.
		if _, err := h.deps.Commands.InsertCommand(ctx, rec); err != nil {
			if errors.As(err, &dup) {
				result, problem := resolveAudioSessionReplay(dup.Existing, in.Action, in.NodeID, in.SessionID, string(paramsJSON))
				return result, problem, nil
			}
			return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("insert audio command: %w", err)
		}
		degradedReason := degradedAttributionReasonAuditNeverBlocks
		if audioSafetyExemptActions[in.Action] {
			degradedReason = degradedAttributionReasonSafetyClassExemption
		}
		h.reportDegradedAttribution(now, dispatchEntry, auditErr, degradedReason)
		dispatchDegraded = true
	case auditErr != nil:
		return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("insert audio command: %w", auditErr)
	}

	cmdTopic, err := mqttproto.CmdTopic(in.NodeID)
	if err != nil {
		return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("build cmd topic: %w", err)
	}
	resultTopic, err := mqttproto.ResultTopic(in.NodeID, commandID)
	if err != nil {
		return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("build result topic: %w", err)
	}

	payload := mqttproto.CmdPayload{
		CommandID: commandID, IdempotencyKey: in.IdempotencyKey, Action: in.Action,
		Target: mqttproto.CmdTarget{Kind: "node", ID: in.NodeID}, Params: in.Params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: in.IssuerID, PrincipalName: in.IssuerName},
		ConfirmationMethod: "evidence",
	}
	env, err := mqttproto.NewCmdEnvelope(func() time.Time { return now }, in.NodeID, payload)
	if err != nil {
		return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("build cmd envelope: %w", err)
	}
	rawEnv, err := json.Marshal(env)
	if err != nil {
		return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("marshal cmd envelope: %w", err)
	}

	// From here on, every write is on bgCtx: the command is already
	// durably recorded and about to be dispatched, and a caller walking
	// away (an abandoned HTTP client) must not be able to abort the
	// dispatch or its post-dispatch bookkeeping — matching
	// dispatchFPPCommand's/the resolume action dispatch's identical
	// bgCtx cutover.
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
	// markUndispatched is markDispatched's sibling for
	// broker.ErrResponseFailedBeforePublish: nothing reached the wire, so
	// DispatchedAt must stay nil rather than claim a publish that never
	// happened.
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
		// The result topic already names this command id; Match still
		// decodes and checks node/commandId/idempotencyKey/action so a
		// message on the right topic but the wrong content is never
		// mistaken for this command's own result.
		Match: func(m broker.Message) bool {
			return audioResultCorrelates(m.Payload, in.NodeID, commandID, in.IdempotencyKey, in.Action)
		},
	})
	if err != nil {
		reason := err.Error()
		if errors.Is(err, broker.ErrResponseFailedBeforePublish) {
			// Nothing was ever published — a client that hung up now
			// costs nothing beyond this refusal, and the commands row
			// must not claim a dispatch that never reached the wire.
			markUndispatched("failed", "collection_failed", reason, "{}")
			return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("await result: %w", err)
		}
		markDispatched("dispatched", "collection_failed", reason, "{}")
		if errors.Is(err, broker.ErrResponseDeadlineExceeded) {
			result := v1.AudioSessionCommandResult{
				CommandID: commandID, IdempotencyKey: in.IdempotencyKey, Action: in.Action,
				NodeID: in.NodeID, SessionID: in.SessionID,
				Outcome: "unconfirmable", Reason: "no result received from the node before the deadline",
				DispatchedAt: formatTime(dispatchedAt),
			}
			outcomeDegraded := h.writeBestEffortAuditBounded(bgCtx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
				Timestamp: now, PrincipalID: in.IssuerID, PrincipalName: in.IssuerName,
				Form: in.IssuerForm, CredentialID: in.IssuerCredentialID, ClientAddr: in.ClientAddr,
				Action: in.Action, Target: in.NodeID, IdempotencyKey: in.IdempotencyKey,
				Kind: identity.AuditOutcome, CommandID: commandID,
				Outcome: result.Outcome, OutcomeState: "collection_failed", OutcomeReason: result.Reason,
			})
			result.AttributionDegraded = dispatchDegraded || outcomeDegraded
			// The command reached the node and only its confirmation is
			// missing, so what was commanded is still recorded.
			if h.deps.AudioSessions != nil && audioOutcomeShouldPersist(result.Outcome) {
				h.persistAudioSessionDesiredState(bgCtx, in)
			}
			return result, nil, nil
		}
		return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("await result: %w", err)
	}

	env2, err := mqttproto.DecodeEnvelope(msg.Payload)
	if err != nil {
		markDispatched("dispatched", "collection_failed", "result payload did not decode", "{}")
		return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("decode result envelope: %w", err)
	}
	res, err := mqttproto.DecodeResultPayload(env2)
	if err != nil {
		markDispatched("dispatched", "collection_failed", "result payload did not decode", "{}")
		return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("decode result payload: %w", err)
	}

	outcome, reason := mapResultOutcome(res)
	resultJSON, _ := json.Marshal(audioSessionResultPayload{Outcome: outcome, Reason: reason})
	resolvedAt := now
	state := "resolved"
	outcomeState := "current"
	outcomeReason := reason
	if err := h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, ResolvedAt: &resolvedAt,
		State: &state, ResultJSON: strPtr(string(resultJSON)), OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	}); err != nil {
		h.logWarn("failed to record audio command outcome", "commandId", commandID, "error", err)
	}

	outcomeDegraded := h.writeBestEffortAuditBounded(bgCtx, resolvedAt, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: in.IssuerID, PrincipalName: in.IssuerName,
		Form: in.IssuerForm, CredentialID: in.IssuerCredentialID, ClientAddr: in.ClientAddr,
		Action: in.Action, Target: in.NodeID, IdempotencyKey: in.IdempotencyKey,
		Kind: identity.AuditOutcome, CommandID: commandID,
		Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
	})

	if h.deps.AudioSessions != nil && audioOutcomeShouldPersist(outcome) {
		h.persistAudioSessionDesiredState(bgCtx, in)
	}

	resolvedStr := formatTime(resolvedAt)
	return v1.AudioSessionCommandResult{
		CommandID: commandID, IdempotencyKey: in.IdempotencyKey, Action: in.Action,
		NodeID: in.NodeID, SessionID: in.SessionID,
		Outcome: outcome, Reason: reason,
		DispatchedAt: formatTime(dispatchedAt), ResolvedAt: &resolvedStr,
		AttributionDegraded: dispatchDegraded || outcomeDegraded,
	}, nil, nil
}

// persistAudioSessionDesiredState merges in.Params onto the session's
// prior desired state (never replacing it outright — a pause or stop
// command's own params carry only sessionId/invocationId/revision, and
// overwriting with those would erase previously-applied media/playlist
// state) and stores the merged result at in.Revision. Called for any
// outcome that means the command actually reached the node — including
// unconfirmable — so desired state stays a durable record of what was
// commanded, never only of what was confirmed (see
// [audioOutcomeShouldPersist]). The write is bounded so a locked store
// cannot block this detached-completion path forever.
func (h *handlers) persistAudioSessionDesiredState(parent context.Context, in audioDispatchInput) {
	ctx, cancel := context.WithTimeout(parent, dbWriteTimeout)
	defer cancel()
	existing, err := h.deps.AudioSessions.GetAudioSession(ctx, in.SessionID)
	existingJSON := ""
	if err == nil {
		existingJSON = existing.DesiredJSON
	} else if !errors.Is(err, store.ErrAudioSessionNotFound) {
		h.logWarn("failed to read prior audio session desired state", "sessionId", in.SessionID, "error", err)
	}

	mergedJSON, err := mergeAudioDesiredJSON(existingJSON, in.Params)
	if err != nil {
		h.logWarn("failed to merge audio session desired state", "sessionId", in.SessionID, "error", err)
		return
	}
	if err := h.deps.AudioSessions.PutAudioSession(ctx, store.AudioSessionRecord{
		ID: in.SessionID, NodeID: in.NodeID, DesiredJSON: mergedJSON, Revision: in.Revision,
	}); err != nil {
		h.logWarn("failed to persist audio session desired state", "sessionId", in.SessionID, "error", err)
	}
}

// mergeAudioDesiredJSON merges newParams onto existingJSON's own decoded
// object (a key present in newParams always wins), returning the result
// re-encoded. A corrupt or absent existingJSON is treated as an empty
// object, never fatal.
func mergeAudioDesiredJSON(existingJSON string, newParams map[string]any) (string, error) {
	merged := map[string]any{}
	if existingJSON != "" {
		_ = json.Unmarshal([]byte(existingJSON), &merged) // best-effort; corrupt prior state starts fresh rather than blocking every future command
	}
	for k, v := range newParams {
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// audioOutcomeShouldPersist reports whether outcome means the command
// reached the node and is worth recording as desired state. refused and
// failed are this coordinator's own structural refusals, so nothing was
// commanded. unconfirmable persists, because desired state records what
// was commanded rather than only what was confirmed. An outcome this
// coordinator does not recognise is never persisted.
func audioOutcomeShouldPersist(outcome string) bool {
	switch pkgaudio.Outcome(outcome) {
	case pkgaudio.OutcomeStarted, pkgaudio.OutcomePosition, pkgaudio.OutcomeGain,
		pkgaudio.OutcomeFadeComplete, pkgaudio.OutcomeStopped, pkgaudio.OutcomeCompleted,
		pkgaudio.OutcomeUnconfirmable:
		return true
	default:
		return false
	}
}

// audioResultCorrelates reports whether payload is genuinely this
// command's own result: its envelope names nodeID, and its decoded
// [mqttproto.ResultPayload] names commandID, idempotencyKey, and action
// exactly. A message that fails to decode, or decodes to a mismatched
// identity, is never treated as this command's result — the result
// topic already scopes delivery to one command id, but this is the
// content-level check that a message ON that topic actually IS what it
// claims to be, rather than trusting topic scoping alone.
func audioResultCorrelates(payload []byte, nodeID, commandID, idempotencyKey, action string) bool {
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil || env.NodeID != nodeID {
		return false
	}
	res, err := mqttproto.DecodeResultPayload(env)
	if err != nil {
		return false
	}
	return res.CommandID == commandID && res.IdempotencyKey == idempotencyKey && res.Action == action
}

// audioSessionResultPayload is the JSON this file persists into
// store.CommandRecord.ResultJSON — just enough to answer a replay without
// re-dispatching (mapResultOutcome's own output), mirroring
// resolumeActionResultPayload's identical role one file over.
type audioSessionResultPayload struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

// audioSessionReplayConflictProblem mirrors
// resolumeActionReplayConflictProblem's identical reasoning: an
// idempotency key reused against a DIFFERENT action or a DIFFERENT
// target node is a conflict, never a replay.
func audioSessionReplayConflictProblem(existingID, existingAction, existingNodeID, requestedAction, requestedNodeID string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used for a different command",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (action %q, node %q); this request names a "+
				"different action %q or node %q. Mint a fresh idempotencyKey for a genuinely new request.",
			existingID, existingAction, existingNodeID, requestedAction, requestedNodeID),
	}
}

// audioSessionReplayParamsConflictProblem mirrors
// resolumeActionReplayParamsConflictProblem's identical reasoning: the
// SAME idempotency key, action, and target node, but DIFFERENT params
// (including a different sessionId or invocationId baked into params by
// dispatchAudioSessionCommand), is also a conflict, never a replay.
func audioSessionReplayParamsConflictProblem(existingID, action, existingParamsJSON, requestedParamsJSON string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used with different parameters",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (action %q) with params %s; this request has the SAME "+
				"action but DIFFERENT params: %s. Mint a fresh idempotencyKey for a genuinely new request.",
			existingID, action, existingParamsJSON, requestedParamsJSON),
	}
}

// resolveAudioSessionReplay decides what a reused idempotency key means:
// a genuine replay (same action, same target node, same params — the
// existing outcome is returned, flagged replay:true, nothing
// re-dispatched) or a conflict (different action, different node, or
// different params — a 409, never silently answered as if it belonged to
// whichever command first claimed the key), matching
// resolveResolumeActionReplay's identical decision one file over. The
// node check exists because an idempotency key reused after a session
// was reassigned to a different node must never replay the OLD node's
// result as if it answered a request to the new one.
func resolveAudioSessionReplay(existing store.CommandRecord, requestedAction, requestedNodeID, sessionID, requestedParamsJSON string) (v1.AudioSessionCommandResult, *v1.Problem) {
	if existing.Action != requestedAction || existing.TargetID != requestedNodeID {
		p := audioSessionReplayConflictProblem(existing.ID, existing.Action, existing.TargetID, requestedAction, requestedNodeID)
		return v1.AudioSessionCommandResult{}, &p
	}
	existingParamsJSON := existing.ParamsJSON
	if existingParamsJSON == "" {
		existingParamsJSON = "{}"
	}
	if existingParamsJSON != requestedParamsJSON {
		p := audioSessionReplayParamsConflictProblem(existing.ID, existing.Action, existingParamsJSON, requestedParamsJSON)
		return v1.AudioSessionCommandResult{}, &p
	}
	return audioSessionResultFromRecord(existing, sessionID, true), nil
}

// mapResultOutcome turns a node's [mqttproto.ResultPayload] into this
// endpoint's outcome/reason pair. mqttproto's own Outcome vocabulary
// (confirmed/unconfirmed/refused/failed) is coarser than
// pkg/audio.Outcome; Evidence.Value, when present, carries the session
// layer's own finer-grained outcome string (audiosessionops.go's
// sessionOp puts it there) and is preferred when it parses AND is one of
// [pkgaudio.Outcome]'s reserved members. Without genuine evidence
// carrying a recognized outcome, the result is always "unconfirmable" —
// never "started": a bare transport-level Confirmed with no evidence, or
// an unrecognized outcome string, must not be reported as if the node's
// own session layer had confirmed anything.
func mapResultOutcome(res mqttproto.ResultPayload) (outcome, reason string) {
	if res.Evidence != nil {
		if v, ok := res.Evidence.Value.(map[string]any); ok {
			if o, ok := v["outcome"].(string); ok && o != "" {
				if err := pkgaudio.Outcome(o).Validate(); err != nil {
					return string(pkgaudio.OutcomeUnconfirmable), fmt.Sprintf("node reported an outcome value this coordinator does not recognize: %q", o)
				}
				r, _ := v["reason"].(string)
				return o, r
			}
		}
	}
	reason = res.Reason
	if reason == "" {
		reason = "no confirmation evidence was reported"
	}
	return string(pkgaudio.OutcomeUnconfirmable), reason
}

func audioSessionResultFromRecord(rec store.CommandRecord, sessionID string, replay bool) v1.AudioSessionCommandResult {
	var resolvedAt *string
	if rec.ResolvedAt != nil {
		s := formatTime(*rec.ResolvedAt)
		resolvedAt = &s
	}
	dispatchedAt := ""
	if rec.DispatchedAt != nil {
		dispatchedAt = formatTime(*rec.DispatchedAt)
	}
	var payload audioSessionResultPayload
	_ = json.Unmarshal([]byte(rec.ResultJSON), &payload)
	return v1.AudioSessionCommandResult{
		CommandID: rec.ID, IdempotencyKey: rec.IdempotencyKey, Action: rec.Action,
		NodeID: rec.TargetID, SessionID: sessionID, Replay: replay,
		Outcome: payload.Outcome, Reason: payload.Reason,
		DispatchedAt: dispatchedAt, ResolvedAt: resolvedAt,
	}
}

func (h *handlers) handleAudioSessionApply(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.session.apply")
}
func (h *handlers) handleAudioSessionPrepare(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.session.prepare")
}
func (h *handlers) handleAudioSessionStart(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.session.start")
}
func (h *handlers) handleAudioSessionPause(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.session.pause")
}
func (h *handlers) handleAudioSessionResume(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.session.resume")
}
func (h *handlers) handleAudioSessionSeek(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.session.seek")
}
func (h *handlers) handleAudioSessionAdvance(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.session.advance")
}
func (h *handlers) handleAudioSessionStop(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.session.stop")
}
func (h *handlers) handleAudioSessionClear(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.session.clear")
}
func (h *handlers) handleAudioGainSet(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.gain.set")
}
func (h *handlers) handleAudioGainFade(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.gain.fade")
}
func (h *handlers) handleAudioOutputMute(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.output.mute")
}
func (h *handlers) handleAudioOutputUnmute(w http.ResponseWriter, r *http.Request) {
	h.dispatchAudioSessionCommand(w, r, "audio.output.unmute")
}

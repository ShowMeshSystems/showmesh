package api

import (
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
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the coordinator-side dispatch for the nine
// audio.session.* operations internal/agent/audiosessionops.go ships:
// apply, prepare, start, pause, resume, seek, advance, stop, clear. It is
// deliberately ONE shared dispatch core behind nine thin routes, unlike
// renderdispatch.go's own richer per-action param resolution — a session
// apply's params already arrive complete from the caller (no
// coordinator-side asset lookup the way render.surface.apply's sequenceId
// needs), so there is nothing this file needs to resolve on the
// operator's behalf.
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

var scopeAudioCommand = identity.ScopeAudioCommand

var audioSessionIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

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
// [Dependencies.AudioSessions]'s doc comment.
type AudioSessionStore interface {
	PutAudioSession(ctx context.Context, rec store.AudioSessionRecord) error
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

	var body v1.AudioSessionCommandRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxAudioCommandRequestBodyBytes+1))
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body must be a JSON object matching {\"revision\":number,\"idempotencyKey\":string?,\"params\":object?}"))
		return
	}

	params := body.Params
	if params == nil {
		params = map[string]any{}
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

	result, problem, err := h.executeAudioSessionDispatch(ctx, now, audioDispatchInput{
		Action: action, NodeID: nodeID, SessionID: sessionID, Params: params,
		Revision: body.Revision, IdempotencyKey: idempotencyKey,
		IssuerID: issuerID, IssuerName: issuerName,
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
}

// executeAudioSessionDispatch records the command (idempotency-first,
// matching render's own rule), publishes it to the node's cmd topic, and
// awaits the command's own result on its result topic — see this file's
// doc comment for why that replaces render dispatch's collector poll
// here. A nil error with a non-nil problem means "the request was
// refused"; a non-nil error means an internal failure this coordinator
// cannot attribute to the caller.
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
	if _, err := h.deps.Commands.InsertCommand(ctx, rec); err != nil {
		var dup *store.DuplicateCommandError
		if errors.As(err, &dup) {
			result, problem := resolveAudioSessionReplay(dup.Existing, in.Action, in.SessionID, string(paramsJSON))
			return result, problem, nil
		}
		return v1.AudioSessionCommandResult{}, nil, fmt.Errorf("insert command: %w", err)
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

	dispatchedAt := now
	markDispatched := func(state, outcomeState, outcomeReason string, resultJSON string) {
		s, os_, or := state, outcomeState, outcomeReason
		rj := resultJSON
		_ = h.deps.Commands.UpdateCommandOutcome(ctx, commandID, store.CommandOutcomeUpdate{
			DispatchedAt: &dispatchedAt, ResolvedAt: &dispatchedAt,
			State: &s, ResultJSON: &rj, OutcomeState: &os_, OutcomeReason: &or,
		})
	}

	msg, err := h.deps.AudioPublisher.AwaitResponse(ctx, broker.ResponseRequest{
		PublishTopic: cmdTopic, PublishPayload: rawEnv,
		PublishQoS: mqttproto.CmdDeliveryPolicy.QoS, PublishRetain: mqttproto.CmdDeliveryPolicy.Retain,
		ResponseTopic: resultTopic, ResponseQoS: mqttproto.ResultDeliveryPolicy.QoS,
		Deadline: audioCommandConfirmDeadline,
		Match:    func(broker.Message) bool { return true }, // the topic itself is unique to this command id.
	})
	if err != nil {
		reason := err.Error()
		markDispatched("dispatched", "collection_failed", reason, "{}")
		if errors.Is(err, broker.ErrResponseDeadlineExceeded) {
			return v1.AudioSessionCommandResult{
				CommandID: commandID, IdempotencyKey: in.IdempotencyKey, Action: in.Action,
				NodeID: in.NodeID, SessionID: in.SessionID,
				Outcome: "unconfirmable", Reason: "no result received from the node before the deadline",
				DispatchedAt: formatTime(dispatchedAt),
			}, nil, nil
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
	_ = h.deps.Commands.UpdateCommandOutcome(ctx, commandID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, ResolvedAt: &resolvedAt,
		State: &state, ResultJSON: strPtr(string(resultJSON)), OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	})

	if h.deps.AudioSessions != nil {
		desiredJSON, _ := json.Marshal(in.Params)
		_ = h.deps.AudioSessions.PutAudioSession(ctx, store.AudioSessionRecord{
			ID: in.SessionID, NodeID: in.NodeID, DesiredJSON: string(desiredJSON), Revision: in.Revision,
		})
	}

	resolvedStr := formatTime(resolvedAt)
	return v1.AudioSessionCommandResult{
		CommandID: commandID, IdempotencyKey: in.IdempotencyKey, Action: in.Action,
		NodeID: in.NodeID, SessionID: in.SessionID,
		Outcome: outcome, Reason: reason,
		DispatchedAt: formatTime(dispatchedAt), ResolvedAt: &resolvedStr,
	}, nil, nil
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
// idempotency key reused against a DIFFERENT action is a conflict, never
// a replay.
func audioSessionReplayConflictProblem(existingID, existingAction, requestedAction string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used for a different action",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (action %q); this request names a different action %q. "+
				"Mint a fresh idempotencyKey for a genuinely new request.",
			existingID, existingAction, requestedAction),
	}
}

// audioSessionReplayParamsConflictProblem mirrors
// resolumeActionReplayParamsConflictProblem's identical reasoning: the
// SAME idempotency key and the SAME action, but DIFFERENT params
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
// a genuine replay (same action, same params — the existing outcome is
// returned, flagged replay:true, nothing re-dispatched) or a conflict
// (different action or different params — a 409, never silently answered
// as if it belonged to whichever command first claimed the key), matching
// resolveResolumeActionReplay's identical decision one file over.
func resolveAudioSessionReplay(existing store.CommandRecord, requestedAction, sessionID, requestedParamsJSON string) (v1.AudioSessionCommandResult, *v1.Problem) {
	if existing.Action != requestedAction {
		p := audioSessionReplayConflictProblem(existing.ID, existing.Action, requestedAction)
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
// sessionOp puts it there) and is preferred when it parses.
func mapResultOutcome(res mqttproto.ResultPayload) (outcome, reason string) {
	if res.Evidence != nil {
		if v, ok := res.Evidence.Value.(map[string]any); ok {
			if o, ok := v["outcome"].(string); ok && o != "" {
				r, _ := v["reason"].(string)
				return o, r
			}
		}
	}
	switch res.Outcome {
	case mqttproto.OutcomeConfirmed:
		return "started", ""
	default:
		return "unconfirmable", res.Reason
	}
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

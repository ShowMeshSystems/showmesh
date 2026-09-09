package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// The operator-facing brightness transition-gain write:
// POST /api/v1/fpp/{instanceId}/brightness/transition-gain,
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 2.2. It is the hand-driven
// sibling of the night session's own write (nightlightinggain.go), on the
// same client and the same contract.
//
// It is NOT a dispatchable FPP command and must never become one: section
// 2.1 makes the gain the coordinator's alone, and an FPP Action would be
// discoverable and schedulable in FPP's own UI, handing every operator and
// every schedule entry a way to fight the coordinator over one value. This
// route keeps the coordinator the only writer while still giving an
// operator a way in, which is what the API-first rule requires.

// scopeFPPTransitionGain exists only so api.go's route registration can
// take its address - see scopeFPPCommand's identical pattern
// (fppcommand_handler.go).
//
// fpp:command, the same scope dispatching an FPP command needs: this is an
// operator writing a live output value to one FPP host, exactly the
// blast radius that scope already governs, and it is held by operator and
// admin. config:write would be wrong (nothing configured changes here) and
// a scope of its own would split one operator capability across two roles
// for no difference an operator could act on.
var scopeFPPTransitionGain = identity.ScopeFPPCommand

// auditActionFPPTransitionGain is this route's own audit action
// identifier, in the same "fpp.<verb>" shape as
// [auditActionFPPStopPlaylist].
const auditActionFPPTransitionGain = "fpp.set_transition_gain"

// maxFPPTransitionGainRequestBodyBytes bounds this endpoint's request
// body, mirroring [maxFPPCommandRequestBodyBytes]: three small fields have
// no legitimate reason to be large.
const maxFPPTransitionGainRequestBodyBytes = 4 << 10 // 4 KiB

// Contract section 2.2's bounds. Out of range is refused here rather than
// clamped, for the reason the plugin refuses rather than clamping: a
// mistyped value must stay visible. internal/coordinator/fppcommand
// refuses the same values again before spending a request; both checks are
// deliberate, because neither layer may assume the other ran.
const (
	fppTransitionGainMinPercent     = 0
	fppTransitionGainMaxPercent     = 100
	fppTransitionGainMaxFadeSeconds = 86400
)

// fppTransitionGainWriter performs one transition-gain write against
// baseURL. Only a test ever substitutes it; production leaves it nil and
// [writeFPPTransitionGain] runs.
type fppTransitionGainWriter func(ctx context.Context, baseURL string, targetPercent, fadeSeconds int, requestID string) (fppcommand.TransitionGainOutcome, error)

func writeFPPTransitionGain(ctx context.Context, baseURL string, targetPercent, fadeSeconds int, requestID string) (fppcommand.TransitionGainOutcome, error) {
	client, err := fppcommand.New(baseURL, fppcommand.Options{})
	if err != nil {
		return fppcommand.TransitionGainOutcome{}, fmt.Errorf("api: building the fpp client for the transition gain write: %w", err)
	}
	return client.SetTransitionGain(ctx, targetPercent, fadeSeconds, requestID)
}

// handleFPPTransitionGain serves POST
// /api/v1/fpp/{instanceId}/brightness/transition-gain, behind
// writeGuard(&scopeFPPTransitionGain, ...).
//
// The 200 body is the plugin's own applied state, never a bare success.
// applied=false is part of that success: it is what a repeated requestId
// returns, the idempotency key working, with the gain unchanged and the
// current state reported. Answering an error there would tell a caller to
// retry a write that already took.
func (h *handlers) handleFPPTransitionGain(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	req, problem := decodeFPPTransitionGainBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	targetPercent, fadeSeconds := *req.TargetPercent, *req.FadeSeconds
	if targetPercent < fppTransitionGainMinPercent || targetPercent > fppTransitionGainMaxPercent {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			"targetPercent %d is outside [%d, %d]; an out-of-range gain is refused, never clamped",
			targetPercent, fppTransitionGainMinPercent, fppTransitionGainMaxPercent)))
		return
	}
	if fadeSeconds < 0 || fadeSeconds > fppTransitionGainMaxFadeSeconds {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			"fadeSeconds %d is outside [0, %d]; an out-of-range fade is refused, never clamped",
			fadeSeconds, fppTransitionGainMaxFadeSeconds)))
		return
	}

	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "list fpp endpoints for a transition gain write", err)
		return
	}
	baseURL := ""
	for _, ep := range endpoints {
		if ep.ID == instanceID {
			baseURL = ep.URL
			break
		}
	}
	if baseURL == "" {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			fmt.Sprintf("no configured FPP endpoint has instance id %q", instanceID)))
		return
	}

	// A caller-supplied requestId is what makes a retry of an unanswered
	// request safe, so it is accepted verbatim. Absent, one is minted: a
	// hand-driven write with no key is a fresh operator action, the same
	// reading POST /cues/{id}/activate already takes, and the minted value
	// is echoed on the response so a caller can still retry with it.
	requestID := req.RequestID
	if requestID == "" {
		requestID = uuid.NewString()
	}

	ac := authFromContext(ctx)
	auditParams := map[string]any{"targetPercent": targetPercent, "fadeSeconds": fadeSeconds, "requestId": requestID}
	commandID := uuid.NewString()
	if !h.writeAuditOrFail(ctx, w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPTransitionGain, Target: instanceID,
		Kind: identity.AuditDispatch, CommandID: commandID, IdempotencyKey: requestID,
		Params: auditParams,
	}) {
		return
	}

	write := h.fppGainWriter
	if write == nil {
		write = writeFPPTransitionGain
	}
	outcome, writeErr := write(ctx, baseURL, targetPercent, fadeSeconds, requestID)
	h.auditFPPTransitionGainOutcome(r, ac, commandID, instanceID, auditParams, outcome, writeErr)
	if writeErr != nil {
		writeProblem(w, h.logger, h.now(), fppTransitionGainWriteFailedProblem(instanceID, writeErr))
		return
	}

	jsonWrite(w, v1.FPPTransitionGainResponse{
		ServerTime: formatTime(h.now()),
		TransitionGain: v1.FPPTransitionGainResult{
			InstanceID: instanceID, RequestID: requestID,
			Applied: outcome.Applied, GainStart: outcome.GainStart, GainTarget: outcome.GainTarget,
			FadeSeconds: outcome.FadeSeconds, Ceiling: outcome.Ceiling, EffectiveOutput: outcome.EffectiveOutput,
		},
	})
}

// auditFPPTransitionGainOutcome records what the write actually did.
// applied=false is recorded as its own outcome word rather than folded
// into "applied": an investigator reading the log must be able to see that
// a repeat changed nothing, which is exactly the case where an operator
// did not get their answer the first time.
func (h *handlers) auditFPPTransitionGainOutcome(r *http.Request, ac authContext, commandID, instanceID string, params map[string]any, outcome fppcommand.TransitionGainOutcome, writeErr error) {
	entry := identity.AuditEntry{
		Timestamp: h.now(), PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPTransitionGain, Target: instanceID,
		Kind: identity.AuditOutcome, CommandID: commandID,
		Params: params,
	}
	switch {
	case writeErr != nil:
		entry.Outcome = outcomeWordFailed
		entry.OutcomeReason = writeErr.Error()
	case outcome.Applied:
		entry.Outcome = outcomeWordConfirmed
		entry.OutcomeReason = fmt.Sprintf("gain %d to %d over %ds; ceiling %d, effective output %d",
			outcome.GainStart, outcome.GainTarget, outcome.FadeSeconds, outcome.Ceiling, outcome.EffectiveOutput)
	default:
		entry.Outcome = outcomeWordConfirmed
		entry.OutcomeReason = fmt.Sprintf("this requestId was already applied; nothing changed. gain %d, ceiling %d, effective output %d",
			outcome.GainTarget, outcome.Ceiling, outcome.EffectiveOutput)
	}
	if err := h.deps.Identity.WriteAudit(r.Context(), entry); err != nil {
		h.logWarn("failed to audit a transition gain outcome", "instanceId", instanceID, "commandId", commandID, "error", err)
	}
}

// decodeFPPTransitionGainBody decodes this route's body under the same
// three-way rule the FPP command endpoint enforces: an absent required
// field, an explicit null, and a value are three different things, and an
// unrecognized key is a 400 naming it rather than a silently ignored typo.
func decodeFPPTransitionGainBody(r *http.Request) (v1.FPPTransitionGainRequest, *v1.Problem) {
	var top map[string]json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFPPTransitionGainRequestBodyBytes+1))
	if err := dec.Decode(&top); err != nil {
		p := invalidParameterProblem(
			"request body must be a JSON object matching {\"targetPercent\":integer,\"fadeSeconds\":integer,\"requestId\":string?}")
		return v1.FPPTransitionGainRequest{}, &p
	}

	var unknown []string
	for key := range top {
		switch key {
		case "targetPercent", "fadeSeconds", "requestId":
		default:
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		p := invalidParameterProblem(fmt.Sprintf("unrecognized field(s) %s", strings.Join(unknown, ", ")))
		return v1.FPPTransitionGainRequest{}, &p
	}

	var out v1.FPPTransitionGainRequest
	for _, field := range []struct {
		name string
		into **int
	}{
		{"targetPercent", &out.TargetPercent},
		{"fadeSeconds", &out.FadeSeconds},
	} {
		raw, ok := top[field.name]
		if !ok {
			p := invalidParameterProblem(field.name + " is required")
			return v1.FPPTransitionGainRequest{}, &p
		}
		var value *int
		if err := json.Unmarshal(raw, &value); err != nil {
			p := invalidParameterProblem(field.name + " must be an integer")
			return v1.FPPTransitionGainRequest{}, &p
		}
		if value == nil {
			p := invalidParameterProblem(field.name + " must not be null")
			return v1.FPPTransitionGainRequest{}, &p
		}
		*field.into = value
	}

	if raw, ok := top["requestId"]; ok {
		if err := json.Unmarshal(raw, &out.RequestID); err != nil {
			p := invalidParameterProblem("requestId must be a string")
			return v1.FPPTransitionGainRequest{}, &p
		}
		if out.RequestID == "" {
			p := invalidParameterProblem("requestId must not be empty; omit it entirely to have the coordinator mint one")
			return v1.FPPTransitionGainRequest{}, &p
		}
	}
	return out, nil
}

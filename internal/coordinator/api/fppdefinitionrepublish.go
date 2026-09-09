package api

import (
	"context"
	"encoding/json"
	"errors"
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

// The operator-facing playlist definition republish:
// POST /api/v1/fpp/{instanceId}/playlist-definitions/republish,
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 3.9. It asks the resident
// plugin to drop its record of which definitions it has already published
// and to sweep now.
//
// This route accepts a request; it does not import anything. The plugin
// answers before it has attempted a single post, so a 200 here means the
// plugin agreed to resend and never that a definition arrived. What
// arrived is read from the coordinator's own stored definitions, which is
// the only surface that knows.

// scopeFPPDefinitionRepublish exists only so api.go's route registration
// can take its address, the same pattern scopeFPPTransitionGain follows.
//
// fpp:command, the scope an operator write to one FPP host already needs.
// A scope of its own would split one operator capability across two roles
// for no difference an operator could act on, and observation:read would
// be wrong: this posts to the host rather than reading coordinator state.
var scopeFPPDefinitionRepublish = identity.ScopeFPPCommand

// auditActionFPPDefinitionRepublish is this route's own audit action
// identifier, in the same "fpp.<verb>" shape as
// [auditActionFPPTransitionGain].
const auditActionFPPDefinitionRepublish = "fpp.republish_playlist_definitions"

// maxFPPDefinitionRepublishRequestBodyBytes bounds this endpoint's request
// body. One optional string field has no legitimate reason to be large.
const maxFPPDefinitionRepublishRequestBodyBytes = 4 << 10 // 4 KiB

// fppDefinitionRepublisher performs one republish against baseURL. Only a
// test ever substitutes it; production leaves it nil and
// [republishFPPDefinitions] runs.
type fppDefinitionRepublisher func(ctx context.Context, baseURL, requestID string) (fppcommand.RepublishDefinitionsOutcome, error)

func republishFPPDefinitions(ctx context.Context, baseURL, requestID string) (fppcommand.RepublishDefinitionsOutcome, error) {
	client, err := fppcommand.New(baseURL, fppcommand.Options{})
	if err != nil {
		return fppcommand.RepublishDefinitionsOutcome{}, fmt.Errorf("api: building the fpp client for the definition republish: %w", err)
	}
	return client.RepublishDefinitions(ctx, requestID)
}

// handleFPPDefinitionRepublish serves POST
// /api/v1/fpp/{instanceId}/playlist-definitions/republish, behind
// writeGuard(&scopeFPPDefinitionRepublish, ...).
//
// The 200 body is the plugin's own evidence about its own state, never a
// bare success and never an import report. applied=false is part of that
// success: it is what a repeated requestId returns, the idempotency key
// working, and it is also how a caller polls for sweepPending going false.
func (h *handlers) handleFPPDefinitionRepublish(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	req, problem := decodeFPPDefinitionRepublishBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "list fpp endpoints for a definition republish", err)
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
	// request safe, and what lets a later repeat of the same id report
	// whether the sweep finished, so it is accepted verbatim. Absent, one
	// is minted and echoed on the response.
	requestID := req.RequestID
	if requestID == "" {
		requestID = uuid.NewString()
	}

	ac := authFromContext(ctx)
	auditParams := map[string]any{"requestId": requestID}
	commandID := uuid.NewString()
	if !h.writeAuditOrFail(ctx, w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPDefinitionRepublish, Target: instanceID,
		Kind: identity.AuditDispatch, CommandID: commandID, IdempotencyKey: requestID,
		Params: auditParams,
	}) {
		return
	}

	republish := h.fppDefinitionRepublisher
	if republish == nil {
		republish = republishFPPDefinitions
	}
	outcome, republishErr := republish(ctx, baseURL, requestID)
	h.auditFPPDefinitionRepublishOutcome(r, ac, commandID, instanceID, auditParams, outcome, republishErr)
	if republishErr != nil {
		writeProblem(w, h.logger, h.now(), fppDefinitionRepublishFailedProblem(instanceID, republishErr))
		return
	}

	jsonWrite(w, v1.FPPDefinitionRepublishResponse{
		ServerTime: formatTime(h.now()),
		Republish: v1.FPPDefinitionRepublishResult{
			InstanceID: instanceID, RequestID: requestID,
			Applied:                      outcome.Applied,
			DefinitionsCleared:           outcome.DefinitionsCleared,
			DefinitionsHeld:              outcome.DefinitionsHeld,
			DefinitionsRefusedTerminally: outcome.DefinitionsRefusedTerminally,
			SweepPending:                 outcome.SweepPending,
		},
	})
}

// auditFPPDefinitionRepublishOutcome records what the plugin agreed to,
// and deliberately does not record it as an import. The reason text says
// the resend is owed rather than done, because an investigator reading
// this log later must not read "republished 6 definitions" as evidence
// that six definitions were stored.
func (h *handlers) auditFPPDefinitionRepublishOutcome(r *http.Request, ac authContext, commandID, instanceID string, params map[string]any, outcome fppcommand.RepublishDefinitionsOutcome, republishErr error) {
	entry := identity.AuditEntry{
		Timestamp: h.now(), PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPDefinitionRepublish, Target: instanceID,
		Kind: identity.AuditOutcome, CommandID: commandID,
		Params: params,
	}
	switch {
	case republishErr != nil:
		entry.Outcome = outcomeWordFailed
		entry.OutcomeReason = republishErr.Error()
	case outcome.Applied:
		entry.Outcome = outcomeWordConfirmed
		entry.OutcomeReason = fmt.Sprintf(
			"the plugin agreed to resend: %d definitions cleared, %d still held, %d refused terminally and not re-sent. The sweep is owed, not done; nothing has arrived yet",
			outcome.DefinitionsCleared, outcome.DefinitionsHeld, outcome.DefinitionsRefusedTerminally)
	default:
		entry.Outcome = outcomeWordConfirmed
		entry.OutcomeReason = fmt.Sprintf(
			"this requestId was already applied; nothing cleared. %d definitions held, %d refused terminally, sweep pending %t",
			outcome.DefinitionsHeld, outcome.DefinitionsRefusedTerminally, outcome.SweepPending)
	}
	if err := h.deps.Identity.WriteAudit(r.Context(), entry); err != nil {
		h.logWarn("failed to audit a definition republish outcome", "instanceId", instanceID, "commandId", commandID, "error", err)
	}
}

// decodeFPPDefinitionRepublishBody decodes this route's body under the
// same three-way rule the FPP command endpoint enforces, and additionally
// accepts an entirely absent body: this request has no required field, so
// a caller with nothing to say has nothing to send.
func decodeFPPDefinitionRepublishBody(r *http.Request) (v1.FPPDefinitionRepublishRequest, *v1.Problem) {
	var top map[string]json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFPPDefinitionRepublishRequestBodyBytes+1))
	if err := dec.Decode(&top); err != nil {
		if errors.Is(err, io.EOF) {
			return v1.FPPDefinitionRepublishRequest{}, nil
		}
		p := invalidParameterProblem(
			"request body must be absent or a JSON object matching {\"requestId\":string?}")
		return v1.FPPDefinitionRepublishRequest{}, &p
	}

	var unknown []string
	for key := range top {
		if key != "requestId" {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		p := invalidParameterProblem(fmt.Sprintf("unrecognized field(s) %s", strings.Join(unknown, ", ")))
		return v1.FPPDefinitionRepublishRequest{}, &p
	}

	var out v1.FPPDefinitionRepublishRequest
	if raw, ok := top["requestId"]; ok {
		if err := json.Unmarshal(raw, &out.RequestID); err != nil {
			p := invalidParameterProblem("requestId must be a string")
			return v1.FPPDefinitionRepublishRequest{}, &p
		}
		if out.RequestID == "" {
			p := invalidParameterProblem("requestId must not be empty; omit it entirely to have the coordinator mint one")
			return v1.FPPDefinitionRepublishRequest{}, &p
		}
	}
	return out, nil
}

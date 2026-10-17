package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// The operator-facing brightness transition-gain write:
// POST /api/v1/fpp/brightness/transition-gain,
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

// handleFPPTransitionGain serves POST /api/v1/fpp/brightness/transition-gain
// and writes the gain to every FPP in fpp.endpoints (ADR-062 decision 7).
// Each FPP gets its own requestId; one failing does not stop the others, and
// each FPP's outcome is reported on its own.
func (h *handlers) handleFPPTransitionGain(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	req, problem := decodeFPPTransitionGainBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	targetPercent, fadeSeconds := *req.TargetPercent, *req.FadeSeconds
	if targetPercent < fppTransitionGainMinPercent || targetPercent > fppTransitionGainMaxPercent {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			"The gain %d%% is outside 0 to 100. Send a value from 0 to 100.", targetPercent)))
		return
	}
	if fadeSeconds < 0 || fadeSeconds > fppTransitionGainMaxFadeSeconds {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			"The fade of %d seconds is outside 0 to %d. Send a fade within that range.", fadeSeconds, fppTransitionGainMaxFadeSeconds)))
		return
	}

	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "list fpp endpoints for a transition gain write", err)
		return
	}
	if len(endpoints) == 0 {
		writeProblem(w, h.logger, now, v1.Problem{
			Type: ProblemTypeConflict, Title: "No FPP players", Status: http.StatusConflict,
			Detail: "No FPP players are configured. Add one in Settings, Connections.",
		})
		return
	}

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
		Action: auditActionFPPTransitionGain, Target: "display",
		Kind: identity.AuditDispatch, CommandID: commandID, IdempotencyKey: requestID,
		Params: auditParams,
	}) {
		return
	}

	write := h.fppGainWriter
	if write == nil {
		write = writeFPPTransitionGain
	}
	results := make([]v1.FPPTransitionGainResult, len(endpoints))
	errs := make([]error, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := requestID + ":" + ep.ID
			outcome, writeErr := write(ctx, ep.URL, targetPercent, fadeSeconds, id)
			results[i], errs[i] = fppTransitionGainResult(ep.ID, id, outcome, writeErr), writeErr
		}()
	}
	wg.Wait()

	var current *store.NightSessionRecord
	if h.deps.NightSessions != nil {
		if cur, ok, err := h.deps.NightSessions.GetCurrentNightSession(ctx); err == nil && ok {
			current = &cur
		}
	}
	for i, ep := range endpoints {
		h.auditFPPTransitionGainOutcome(r, ac, commandID, ep.ID, auditParams, results[i], errs[i])
		// Only a write the player accepted makes a pending lights write give way.
		if errs[i] == nil && current != nil {
			h.nightLightsYield(current.ID, ep.ID)
		}
	}

	jsonWrite(w, v1.FPPTransitionGainResponse{ServerTime: formatTime(h.now()), Players: results})
}

func fppTransitionGainResult(instanceID, requestID string, outcome fppcommand.TransitionGainOutcome, err error) v1.FPPTransitionGainResult {
	res := v1.FPPTransitionGainResult{InstanceID: instanceID, RequestID: requestID}
	if err != nil {
		res.Outcome = v1.FPPTransitionGainFailed
		res.Reason = fmt.Sprintf("The gain on the player %s was not set. Check that it is on and its ShowMesh plugin is installed.", instanceID)
		return res
	}
	res.Outcome = v1.FPPTransitionGainApplied
	if !outcome.Applied {
		res.Outcome = v1.FPPTransitionGainAlreadyApplied
	}
	gs, gt, fs, c, eo := int(outcome.GainStart), int(outcome.GainTarget), int(outcome.FadeSeconds), int(outcome.Ceiling), int(outcome.EffectiveOutput)
	res.GainStart, res.GainTarget, res.FadeSeconds, res.Ceiling, res.EffectiveOutput = &gs, &gt, &fs, &c, &eo
	return res
}

// auditFPPTransitionGainOutcome records what one FPP's write did. A repeat
// that changed nothing is recorded as such.
func (h *handlers) auditFPPTransitionGainOutcome(r *http.Request, ac authContext, commandID, instanceID string, params map[string]any, res v1.FPPTransitionGainResult, writeErr error) {
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
	case res.Outcome == v1.FPPTransitionGainApplied:
		entry.Outcome = outcomeWordConfirmed
		entry.OutcomeReason = fmt.Sprintf("gain %d to %d over %ds; ceiling %d, effective output %d",
			*res.GainStart, *res.GainTarget, *res.FadeSeconds, *res.Ceiling, *res.EffectiveOutput)
	default:
		entry.Outcome = outcomeWordConfirmed
		entry.OutcomeReason = fmt.Sprintf("this requestId was already applied; nothing changed. gain %d, ceiling %d, effective output %d",
			*res.GainTarget, *res.Ceiling, *res.EffectiveOutput)
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

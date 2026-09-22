package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// The operator write of one FPP host's brightness ceiling: POST
// /api/v1/fpp/{instanceId}/brightness/ceiling.
//
// Unlike the transition gain next door, the ceiling IS one of FPP's own
// commands, registered by the resident ShowMesh plugin, so this route
// dispatches it through the same FPP command client every other command
// goes through. The gain stays the coordinator's own fade value and is
// never written from here.

// scopeFPPBrightnessCeiling is fpp:command, the scope an operator write
// to one FPP host already needs.
var scopeFPPBrightnessCeiling = identity.ScopeFPPCommand

// auditActionFPPSetBrightnessCeiling is this route's audit action.
const auditActionFPPSetBrightnessCeiling = "fpp.set_brightness_ceiling"

// fppBrightnessCeilingAction is the wire action name this route's result
// carries. It is not a member of the FPP command primitive registry: that
// registry holds FPP's own native vocabulary, and this command exists
// only where the ShowMesh plugin is installed.
const fppBrightnessCeilingAction = "setBrightnessCeiling"

// fppSetBrightnessCeilingCommand is the command name the plugin registers
// with FPP. It travels verbatim.
const fppSetBrightnessCeilingCommand = "ShowMesh: Set Brightness Ceiling"

// fppPluginBrightnessPath is the plugin's own read-only brightness route
// on an FPP host.
const fppPluginBrightnessPath = "/api/plugin-apis/showmesh/brightness"

// maxFPPBrightnessCeilingRequestBodyBytes bounds this route's body.
const maxFPPBrightnessCeilingRequestBodyBytes = 4 << 10 // 4 KiB

// Contract bounds. Out of range is refused here rather than clamped, so a
// mistyped value stays visible.
const (
	fppBrightnessCeilingMin = 0
	fppBrightnessCeilingMax = 100
)

// fppBrightnessReadBackBudget is how long the response waits for the
// plugin to report the new ceiling. Past it the write still stands and
// the ceiling field is simply absent.
const fppBrightnessReadBackBudget = 3 * time.Second

// fppBrightnessCeilingWriter performs one ceiling write against baseURL
// and reports the ceiling the plugin read back, if any. Only a test ever
// substitutes it.
type fppBrightnessCeilingWriter func(ctx context.Context, baseURL string, ceiling int) (fppcommand.Outcome, *int, error)

func writeFPPBrightnessCeiling(ctx context.Context, baseURL string, ceiling int) (fppcommand.Outcome, *int, error) {
	client, err := fppcommand.New(baseURL, fppcommand.Options{})
	if err != nil {
		return fppcommand.Outcome{}, nil, fmt.Errorf("api: building the fpp client for the brightness ceiling write: %w", err)
	}
	outcome, err := client.Invoke(ctx, fppSetBrightnessCeilingCommand, []string{strconv.Itoa(ceiling)})
	if err != nil {
		return outcome, nil, err
	}
	return outcome, readBackFPPBrightnessCeiling(ctx, baseURL, ceiling), nil
}

// readBackFPPBrightnessCeiling polls the plugin's own brightness route
// until it reports ceiling or the budget runs out. A nil result means the
// new value was not seen, never that the command failed.
func readBackFPPBrightnessCeiling(ctx context.Context, baseURL string, want int) *int {
	deadline := time.Now().Add(fppBrightnessReadBackBudget)
	readCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	client := &http.Client{Timeout: fppBrightnessReadBackBudget}
	for {
		if got, ok := fetchFPPBrightnessCeiling(readCtx, client, baseURL); ok && got == want {
			return &got
		}
		if !time.Now().Add(250 * time.Millisecond).Before(deadline) {
			return nil
		}
		select {
		case <-readCtx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// fetchFPPBrightnessCeiling reads the plugin's current ceiling. Any
// failure reports ok false; this read never turns into an error the
// operator sees, because the write itself already succeeded.
func fetchFPPBrightnessCeiling(ctx context.Context, client *http.Client, baseURL string) (int, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+fppPluginBrightnessPath, nil)
	if err != nil {
		return 0, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, false
	}
	var body struct {
		Ceiling *int `json:"ceiling"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil || body.Ceiling == nil {
		return 0, false
	}
	return *body.Ceiling, true
}

// handleFPPBrightnessCeiling serves POST
// /api/v1/fpp/{instanceId}/brightness/ceiling.
func (h *handlers) handleFPPBrightnessCeiling(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	req, problem := decodeFPPBrightnessCeilingBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	ceiling := *req.Ceiling
	if ceiling < fppBrightnessCeilingMin || ceiling > fppBrightnessCeilingMax {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			"ceiling %d is outside [%d, %d]; an out-of-range ceiling is refused, never clamped",
			ceiling, fppBrightnessCeilingMin, fppBrightnessCeilingMax)))
		return
	}

	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "list fpp endpoints for a brightness ceiling write", err)
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

	requestID := req.RequestID
	if requestID == "" {
		requestID = uuid.NewString()
	}

	ac := authFromContext(ctx)
	params := map[string]any{"ceiling": ceiling}
	commandID := uuid.NewString()
	if !h.writeAuditOrFail(ctx, w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPSetBrightnessCeiling, Target: instanceID,
		Kind: identity.AuditDispatch, CommandID: commandID, IdempotencyKey: requestID,
		Params: params,
	}) {
		return
	}

	write := h.fppCeilingWriter
	if write == nil {
		write = writeFPPBrightnessCeiling
	}
	dispatchedAt := h.now()
	_, readBack, writeErr := write(ctx, baseURL, ceiling)
	resolvedAt := h.now()

	outcome, outcomeState, outcomeReason := fppBrightnessCeilingOutcome(ceiling, readBack, writeErr)
	h.auditFPPBrightnessCeilingOutcome(r, ac, commandID, instanceID, params, outcome, outcomeState, outcomeReason)
	if writeErr != nil {
		writeProblem(w, h.logger, h.now(), fppBrightnessCeilingWriteFailedProblem(instanceID, writeErr))
		return
	}

	dispatched, resolved := formatTime(dispatchedAt), formatTime(resolvedAt)
	jsonWrite(w, v1.FPPBrightnessCeilingResponse{
		ServerTime: formatTime(h.now()),
		Command: v1.FPPCommandResult{
			ID: commandID, IdempotencyKey: requestID,
			Action: fppBrightnessCeilingAction, InstanceID: instanceID,
			Params: params, Replay: false,
			Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
			DispatchedAt: &dispatched, ResolvedAt: &resolved,
		},
		Ceiling: readBack,
	})
}

// fppBrightnessCeilingOutcome decides what this write is allowed to
// claim. FPP answering 200 is never confirmation on its own: only the
// plugin's own read-back of the new value confirms anything.
func fppBrightnessCeilingOutcome(ceiling int, readBack *int, writeErr error) (outcome, outcomeState, outcomeReason string) {
	switch {
	case writeErr != nil:
		return outcomeWordUnconfirmed, string(observation.StateCollectionFailed), writeErr.Error()
	case readBack != nil:
		return outcomeWordConfirmed, string(observation.StateCurrent),
			fmt.Sprintf("the plugin reports a ceiling of %d", *readBack)
	default:
		return outcomeWordUnconfirmed, string(observation.StateNotCollected),
			fmt.Sprintf("FPP accepted the command; the plugin did not report a ceiling of %d in time", ceiling)
	}
}

// auditFPPBrightnessCeilingOutcome records what the write actually did,
// best effort: the command has already been sent by the time this runs.
func (h *handlers) auditFPPBrightnessCeilingOutcome(r *http.Request, ac authContext, commandID, instanceID string, params map[string]any, outcome, outcomeState, outcomeReason string) {
	entry := identity.AuditEntry{
		Timestamp: h.now(), PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPSetBrightnessCeiling, Target: instanceID,
		Kind: identity.AuditOutcome, CommandID: commandID,
		Params: params, Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
	}
	if err := h.deps.Identity.WriteAudit(r.Context(), entry); err != nil {
		h.logWarn("failed to audit a brightness ceiling outcome", "instanceId", instanceID, "commandId", commandID, "error", err)
	}
}

// decodeFPPBrightnessCeilingBody decodes this route's body under the same
// three-way rule the transition-gain route enforces: an absent field, an
// explicit null and a value are three different things, and an
// unrecognized key is a 400 naming it.
func decodeFPPBrightnessCeilingBody(r *http.Request) (v1.FPPBrightnessCeilingRequest, *v1.Problem) {
	var top map[string]json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFPPBrightnessCeilingRequestBodyBytes+1))
	if err := dec.Decode(&top); err != nil {
		p := invalidParameterProblem(`request body must be a JSON object matching {"ceiling":integer,"requestId":string?}`)
		return v1.FPPBrightnessCeilingRequest{}, &p
	}

	var unknown []string
	for key := range top {
		switch key {
		case "ceiling", "requestId":
		default:
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		p := invalidParameterProblem(fmt.Sprintf("unrecognized field(s) %s", strings.Join(unknown, ", ")))
		return v1.FPPBrightnessCeilingRequest{}, &p
	}

	raw, ok := top["ceiling"]
	if !ok {
		p := invalidParameterProblem("ceiling is required")
		return v1.FPPBrightnessCeilingRequest{}, &p
	}
	var out v1.FPPBrightnessCeilingRequest
	if err := json.Unmarshal(raw, &out.Ceiling); err != nil {
		p := invalidParameterProblem("ceiling must be an integer")
		return v1.FPPBrightnessCeilingRequest{}, &p
	}
	if out.Ceiling == nil {
		p := invalidParameterProblem("ceiling must not be null")
		return v1.FPPBrightnessCeilingRequest{}, &p
	}

	if raw, ok := top["requestId"]; ok {
		if err := json.Unmarshal(raw, &out.RequestID); err != nil {
			p := invalidParameterProblem("requestId must be a string")
			return v1.FPPBrightnessCeilingRequest{}, &p
		}
		if out.RequestID == "" {
			p := invalidParameterProblem("requestId must not be empty; omit it entirely to have the coordinator mint one")
			return v1.FPPBrightnessCeilingRequest{}, &p
		}
	}
	return out, nil
}

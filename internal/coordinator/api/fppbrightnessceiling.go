package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/displayceiling"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// The display's one brightness ceiling (ADR-062): GET /api/v1/fpp/brightness
// and PUT /api/v1/fpp/brightness/ceiling. The displayceiling reconciler does
// the writes to every FPP; these routes read and set its desired ceiling.

var (
	scopeFPPBrightnessRead    = identity.ScopeFPPRead
	scopeFPPBrightnessCeiling = identity.ScopeFPPCommand
)

// auditActionFPPSetBrightnessCeiling is the ceiling route's audit action.
const auditActionFPPSetBrightnessCeiling = "fpp.set_brightness_ceiling"

// fppPluginBrightnessPath is the plugin's own read-only brightness route
// on an FPP host.
const fppPluginBrightnessPath = "/api/plugin-apis/showmesh/brightness"

// fppBrightnessReadBackBudget is how long a write waits for the plugin to
// report the new value.
const fppBrightnessReadBackBudget = 3 * time.Second

const maxFPPBrightnessCeilingRequestBodyBytes = 4 << 10 // 4 KiB

// DisplayCeiling reads and sets the display's desired ceiling.
// *displayceiling.Reconciler satisfies it.
type DisplayCeiling interface {
	Snapshot(ctx context.Context) (displayceiling.Snapshot, error)
	Set(ctx context.Context, target, fadeSeconds int, byKind, by, byName string) (store.DisplayCeilingRecord, error)
}

func displayCeilingUnavailableProblem() v1.Problem {
	return v1.Problem{
		Type: ProblemTypeInternalError, Title: "Brightness unavailable", Status: http.StatusServiceUnavailable,
		Detail: "The display brightness is not available on this coordinator. Restart the coordinator.",
	}
}

// handleFPPBrightness serves GET /api/v1/fpp/brightness.
func (h *handlers) handleFPPBrightness(w http.ResponseWriter, r *http.Request) {
	if h.deps.DisplayCeiling == nil {
		writeProblem(w, h.logger, h.now(), displayCeilingUnavailableProblem())
		return
	}
	snap, err := h.deps.DisplayCeiling.Snapshot(r.Context())
	if err != nil {
		h.writeInternalError(w, h.now(), "read the display brightness", err)
		return
	}
	jsonWrite(w, fppBrightnessDocument(h.now(), snap))
}

func fppBrightnessDocument(now time.Time, snap displayceiling.Snapshot) v1.FPPBrightnessResponse {
	out := v1.FPPBrightnessResponse{ServerTime: formatTime(now), Players: []v1.FPPBrightnessPlayer{}}
	if d := snap.Desired; d != nil {
		c := &v1.FPPBrightnessCeiling{
			TargetPercent: d.TargetPercent, ChangedAt: formatTime(d.ChangedAt),
			ChangedBy: v1.FPPBrightnessChanger{Kind: d.ChangedByKind, ID: d.ChangedBy, Name: d.ChangedByName},
		}
		if !d.FadeEndsAt.IsZero() {
			s := formatTime(d.FadeEndsAt)
			c.FadeEndsAt = &s
		}
		out.Ceiling = c
	}
	for _, f := range snap.FPPs {
		p := v1.FPPBrightnessPlayer{
			InstanceID: f.InstanceID, State: string(f.State), Reason: f.Reason,
			Ceiling: f.Ceiling, CeilingTarget: f.CeilingTarget,
		}
		if !f.ReportedAt.IsZero() {
			s := formatTime(f.ReportedAt)
			p.ReportedAt = &s
		}
		out.Players = append(out.Players, p)
	}
	return out
}

// handleFPPSetBrightnessCeiling serves PUT /api/v1/fpp/brightness/ceiling.
func (h *handlers) handleFPPSetBrightnessCeiling(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	if h.deps.DisplayCeiling == nil {
		writeProblem(w, h.logger, now, displayCeilingUnavailableProblem())
		return
	}
	req, problem := decodeFPPBrightnessCeilingBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	target, fade := *req.TargetPercent, 0
	if req.FadeSeconds != nil {
		fade = *req.FadeSeconds
	}
	if target < 0 || target > 100 {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			"The brightness %d%% is outside 0 to 100. Send a value from 0 to 100.", target)))
		return
	}
	if fade < 0 || fade > displayceiling.MaxFadeSeconds {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			"The fade of %d seconds is outside 0 to %d. Send a fade within that range.", fade, displayceiling.MaxFadeSeconds)))
		return
	}

	ac := authFromContext(ctx)
	params := map[string]any{"targetPercent": target, "fadeSeconds": fade}
	commandID := uuid.NewString()
	audit := identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPSetBrightnessCeiling, Target: "display",
		Kind: identity.AuditDispatch, CommandID: commandID, Params: params,
	}
	if !h.writeAuditOrFail(ctx, w, now, audit) {
		return
	}
	_, setErr := h.deps.DisplayCeiling.Set(ctx, target, fade, displayceiling.ChangedByOperator, ac.result.Principal.ID, ac.result.Principal.Name)

	audit.Timestamp, audit.Kind = h.now(), identity.AuditOutcome
	if setErr != nil {
		audit.Outcome, audit.OutcomeReason = outcomeWordFailed, setErr.Error()
	} else {
		audit.Outcome, audit.OutcomeReason = outcomeWordConfirmed, fmt.Sprintf("the display ceiling is now %d%%", target)
	}
	if err := h.deps.Identity.WriteAudit(ctx, audit); err != nil {
		h.logWarn("failed to audit a brightness ceiling outcome", "commandId", commandID, "error", err)
	}
	if setErr != nil {
		h.writeInternalError(w, h.now(), "store the display brightness", setErr)
		return
	}

	snap, err := h.deps.DisplayCeiling.Snapshot(ctx)
	if err != nil {
		h.writeInternalError(w, h.now(), "read the display brightness", err)
		return
	}
	jsonWrite(w, fppBrightnessDocument(h.now(), snap))
}

// decodeFPPBrightnessCeilingBody keeps an absent field, an explicit null and
// a value apart, and names an unrecognized key.
func decodeFPPBrightnessCeilingBody(r *http.Request) (v1.FPPBrightnessCeilingRequest, *v1.Problem) {
	var top map[string]json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFPPBrightnessCeilingRequestBodyBytes+1))
	if err := dec.Decode(&top); err != nil {
		p := invalidParameterProblem("The request body is not a JSON object. Send targetPercent and, if you want a fade, fadeSeconds.")
		return v1.FPPBrightnessCeilingRequest{}, &p
	}
	var unknown []string
	for key := range top {
		if key != "targetPercent" && key != "fadeSeconds" {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		p := invalidParameterProblem(fmt.Sprintf("The field %s is not recognized. Send only targetPercent and fadeSeconds.", strings.Join(unknown, ", ")))
		return v1.FPPBrightnessCeilingRequest{}, &p
	}
	var out v1.FPPBrightnessCeilingRequest
	raw, ok := top["targetPercent"]
	if !ok {
		p := invalidParameterProblem("The brightness is missing. Send targetPercent from 0 to 100.")
		return v1.FPPBrightnessCeilingRequest{}, &p
	}
	if err := json.Unmarshal(raw, &out.TargetPercent); err != nil || out.TargetPercent == nil {
		p := invalidParameterProblem("The brightness is not a whole number. Send targetPercent from 0 to 100.")
		return v1.FPPBrightnessCeilingRequest{}, &p
	}
	if raw, ok := top["fadeSeconds"]; ok {
		if err := json.Unmarshal(raw, &out.FadeSeconds); err != nil || out.FadeSeconds == nil {
			p := invalidParameterProblem("The fade is not a whole number of seconds. Send fadeSeconds in whole seconds, or leave it out.")
			return v1.FPPBrightnessCeilingRequest{}, &p
		}
	}
	return out, nil
}

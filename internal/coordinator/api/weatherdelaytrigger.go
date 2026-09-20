package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/internal/coordinator/weathertrigger"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// ADR-053 decision 12: automatic triggers that ask first, then start a
// delay or cancel the night. A trigger may start or change to a cancel; it
// may never resume: weatherDelayResolveDecision below runs only start and
// cancel-night, never the resume path, and
// TestWeatherDelayTriggerNeverResumes holds it to that.

const (
	maxWeatherDelayTriggerRequestBodyBytes  = 2048
	maxWeatherDelayDecisionRequestBodyBytes = 512
)

// decodeWeatherDelayTriggerRequestBody reads the body of
// POST /weather-delay/triggers/{source}. Unknown keys are refused.
func decodeWeatherDelayTriggerRequestBody(r *http.Request) (v1.WeatherDelayTriggerRequest, *v1.Problem) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWeatherDelayTriggerRequestBodyBytes+1))
	if err != nil {
		p := invalidParameterProblem(fmt.Sprintf("reading request body: %v", err))
		return v1.WeatherDelayTriggerRequest{}, &p
	}
	if int64(len(body)) > maxWeatherDelayTriggerRequestBodyBytes {
		p := invalidParameterProblem("request body too large")
		return v1.WeatherDelayTriggerRequest{}, &p
	}
	var req v1.WeatherDelayTriggerRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		p := invalidParameterProblem(fmt.Sprintf("request body must match {kind, eventType?, severity?, expiresAt?, distanceKm?, suggestCancel?}: %v", err))
		return v1.WeatherDelayTriggerRequest{}, &p
	}
	return req, nil
}

// weatherDelayTriggerEventFromRequest validates req and converts it to a
// [weathertrigger.TriggerEvent].
func weatherDelayTriggerEventFromRequest(source string, req v1.WeatherDelayTriggerRequest) (weathertrigger.TriggerEvent, *v1.Problem) {
	if !weathertrigger.ValidKind(req.Kind) {
		p := invalidParameterProblem(fmt.Sprintf("kind must be %q or %q", weathertrigger.KindWarning, weathertrigger.KindLightning))
		return weathertrigger.TriggerEvent{}, &p
	}
	var expiresAt *time.Time
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			p := invalidParameterProblem(fmt.Sprintf("expiresAt must be RFC 3339: %v", err))
			return weathertrigger.TriggerEvent{}, &p
		}
		expiresAt = &t
	}
	return weathertrigger.TriggerEvent{
		Source: source, Kind: req.Kind, EventType: req.EventType, Severity: req.Severity,
		ExpiresAt: expiresAt, DistanceKm: req.DistanceKm, SuggestCancel: req.SuggestCancel,
	}, nil
}

// handleWeatherDelayTrigger serves POST /api/v1/weather-delay/triggers/{source}.
func (h *handlers) handleWeatherDelayTrigger(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := context.WithoutCancel(r.Context())

	source := r.PathValue("source")
	if err := mqttproto.ValidateNodeID(source); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("source: %v", err)))
		return
	}
	req, problem := decodeWeatherDelayTriggerRequestBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	ev, problem := weatherDelayTriggerEventFromRequest(source, req)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	ac := authFromContext(r.Context())
	clientAddr := h.clientAddr(r)

	accepted, pending, message, err := h.weatherDelayReceiveTrigger(ctx, now, ev, ac, clientAddr)
	if err != nil {
		h.writeInternalError(w, now, "receive weather delay trigger", err)
		return
	}

	resp := v1.WeatherDelayTriggerResponse{ServerTime: formatTime(now), Accepted: accepted, Message: message}
	if pending != nil {
		wire := mapWeatherDelayPendingDecision(*pending)
		resp.PendingDecision = &wire
	}
	jsonWrite(w, resp)
}

func mapWeatherDelayPendingDecision(rec store.PendingWeatherDelayDecisionRecord) v1.WeatherDelayPendingDecision {
	wire := v1.WeatherDelayPendingDecision{
		ID: rec.ID, Source: rec.Source, Reason: rec.Reason, Question: rec.Question,
		DefaultAction: rec.DefaultAction, AskedAt: formatTime(rec.AskedAt), Deadline: formatTime(rec.Deadline),
	}
	if !rec.ExpiresAt.IsZero() {
		wire.ExpiresAt = formatTime(rec.ExpiresAt)
	}
	return wire
}

// weatherDelayReceiveTrigger is ADR-053 decision 12's trigger intake,
// shared by the inbound endpoint above and the built-in NWS poller
// (weatherdelaytriggerloop.go). It never starts, ends, or changes a delay
// on its own: it only raises or leaves alone the one pending decision.
// err is non-nil only for a failure that is worth the caller retrying (a
// poller re-polls; the HTTP handler answers an internal error); every
// other outcome (suppressed, already active, already pending, invalid) is
// a normal, non-error answer.
func (h *handlers) weatherDelayReceiveTrigger(ctx context.Context, now time.Time, ev weathertrigger.TriggerEvent, ac authContext, clientAddr string) (accepted bool, pending *store.PendingWeatherDelayDecisionRecord, message string, err error) {
	audit := func(outcome string, params map[string]any) {
		if params == nil {
			params = map[string]any{}
		}
		params["outcome"] = outcome
		if ev.Kind != "" {
			params["kind"] = ev.Kind
		}
		h.writeBestEffortAuditBounded(ctx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: clientAddr,
			Action: identity.AuditActionShowWeatherDelayTrigger, Target: ev.Source,
			Kind: identity.AuditOutcome, Params: params,
		})
	}

	if verr := ev.Validate(); verr != nil {
		audit("invalid", map[string]any{"error": verr.Error()})
		return false, nil, verr.Error(), nil
	}

	warningKey := weathertrigger.WarningKey(ev)
	if supp, ok, serr := h.deps.WeatherDelayTrigger.GetWeatherDelayTriggerSuppression(ctx, ev.Source); serr != nil {
		h.logWarn("weather delay trigger: failed to read suppression state; asking anyway", "source", ev.Source, "error", serr)
	} else if ok && supp.WarningKey == warningKey && now.Before(supp.Until) {
		audit("suppressed", nil)
		return false, nil, "This warning was dismissed earlier and is suppressed for now.", nil
	}

	active, aerr := h.weatherDelayActive(ctx)
	if aerr != nil {
		h.logWarn("weather delay trigger: failed to read weather delay state; asking anyway", "error", aerr)
	}
	if active && !ev.SuggestCancel {
		audit("ignored: a delay is already active", nil)
		return true, nil, "A weather delay is already active. This trigger did not suggest a cancel, so no new question was asked.", nil
	}

	if existing, ok, perr := h.deps.WeatherDelayTrigger.GetPendingWeatherDelayDecision(ctx); perr != nil {
		h.logWarn("weather delay trigger: failed to read the pending decision; asking anyway", "error", perr)
	} else if ok {
		audit("ignored: a decision is already pending", map[string]any{"pendingDecisionId": existing.ID})
		return true, &existing, "A decision is already pending. This trigger did not raise a second one.", nil
	}

	payload, _, _, _, cerr := resolveWeatherDelayConfig(ctx, h.deps.Config)
	if cerr != nil {
		h.logWarn("weather delay trigger: failed to resolve show.weatherdelay config; using default trigger timing", "error", cerr)
		payload = config.WeatherDelayDefaultPayload
	}

	pd := weathertrigger.NewPendingDecision(uuid.NewString(), ev, now, payload.Triggers.AnswerWindowSeconds, payload.Triggers.CancelAnswerWindowSeconds)
	rec := store.PendingWeatherDelayDecisionRecord{
		ID: pd.ID, Source: pd.Source, Reason: pd.Reason, Question: pd.Question, DefaultAction: pd.DefaultAction,
		AskedAt: pd.AskedAt, Deadline: pd.Deadline, WarningKey: warningKey,
	}
	if ev.ExpiresAt != nil {
		rec.ExpiresAt = *ev.ExpiresAt
	}
	stored, serr := h.deps.WeatherDelayTrigger.SetPendingWeatherDelayDecision(ctx, rec)
	if serr != nil {
		audit("failed: could not persist the pending decision", map[string]any{"error": serr.Error()})
		return false, nil, "The pending decision could not be saved. Try again.", serr
	}
	// A decision raised between the read above and this write keeps its
	// place: one question at a time, whichever source got there first.
	if !stored {
		existing, ok, perr := h.deps.WeatherDelayTrigger.GetPendingWeatherDelayDecision(ctx)
		if perr != nil || !ok {
			audit("ignored: a decision is already pending", nil)
			return true, nil, "A decision is already pending. This trigger did not raise a second one.", nil
		}
		audit("ignored: a decision is already pending", map[string]any{"pendingDecisionId": existing.ID})
		return true, &existing, "A decision is already pending. This trigger did not raise a second one.", nil
	}
	audit("asked", map[string]any{"pendingDecisionId": rec.ID, "question": rec.Question})
	h.weatherDelayNotifyDecision(ctx, "decisionNeeded", pd, "")
	h.notifyStreamHub()
	return true, &rec, "", nil
}

// decodeWeatherDelayDecisionRequestBody reads the body of
// POST /weather-delay/decision.
func decodeWeatherDelayDecisionRequestBody(r *http.Request) (v1.WeatherDelayDecisionRequest, *v1.Problem) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWeatherDelayDecisionRequestBodyBytes+1))
	if err != nil {
		p := invalidParameterProblem(fmt.Sprintf("reading request body: %v", err))
		return v1.WeatherDelayDecisionRequest{}, &p
	}
	if int64(len(body)) > maxWeatherDelayDecisionRequestBodyBytes {
		p := invalidParameterProblem("request body too large")
		return v1.WeatherDelayDecisionRequest{}, &p
	}
	var req v1.WeatherDelayDecisionRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		p := invalidParameterProblem(fmt.Sprintf("request body must match {id, answer}: %v", err))
		return v1.WeatherDelayDecisionRequest{}, &p
	}
	if req.ID == "" {
		p := invalidParameterProblem("id is required")
		return v1.WeatherDelayDecisionRequest{}, &p
	}
	switch req.Answer {
	case weathertrigger.AnswerDelay, weathertrigger.AnswerCancelNight, weathertrigger.AnswerDismiss:
	default:
		p := invalidParameterProblem(fmt.Sprintf("answer must be %q, %q, or %q", weathertrigger.AnswerDelay, weathertrigger.AnswerCancelNight, weathertrigger.AnswerDismiss))
		return v1.WeatherDelayDecisionRequest{}, &p
	}
	return req, nil
}

// handleWeatherDelayDecision serves POST /api/v1/weather-delay/decision.
func (h *handlers) handleWeatherDelayDecision(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(weatherDelayHandlerWriteDeadline()))
	ctx := context.WithoutCancel(r.Context())

	req, problem := decodeWeatherDelayDecisionRequestBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	ac := authFromContext(r.Context())
	clientAddr := h.clientAddr(r)

	pending, ok, err := h.deps.WeatherDelayTrigger.GetPendingWeatherDelayDecision(ctx)
	if err != nil {
		h.writeInternalError(w, now, "get pending weather delay decision", err)
		return
	}
	res := weatherDelayDecisionResolution{Answer: req.Answer, Action: req.Answer, Outcome: req.Answer}
	if ok && !weathertrigger.DecisionStillActionable(now, pending.Deadline, pending.ExpiresAt) {
		res = weatherDelayDecisionResolution{Answer: req.Answer, Outcome: weatherDelayDecisionOutcomeExpired}
	}
	resp, claimed := h.weatherDelayResolveDecision(ctx, now, pending, res, ac, clientAddr)
	if !ok || pending.ID != req.ID || !claimed {
		writeProblem(w, h.logger, now, v1.Problem{
			Type: ProblemTypeConflict, Title: "No matching pending decision", Status: http.StatusConflict,
			Detail: "There is no pending weather delay decision with that id. It may already have been answered or timed out.",
		})
		return
	}
	jsonWrite(w, resp)
}

// weatherDelayDecisionResolution is how one pending decision settles:
// Answer is what is reported as answered, Action is what actually runs
// (empty runs nothing), and Outcome is reported to the webhook, the audit
// entry and the change stream. ViaDeadline marks a settlement raised by
// [handlers.weatherDelayApplyTriggerDeadline] rather than an operator's
// own POST.
type weatherDelayDecisionResolution struct {
	Answer      string
	Action      string
	Outcome     string
	ViaDeadline bool
}

// The two outcomes that settle a decision without running its action.
const (
	weatherDelayDecisionOutcomeExpired           = "expired"
	weatherDelayDecisionOutcomeDismissedByResume = "dismissed by resume"
)

// weatherDelayDecisionExpiredMessage is what an operator is told about a
// question that went stale before anyone answered it.
const weatherDelayDecisionExpiredMessage = "The weather question expired before it was answered. Nothing was started."

// weatherDelayResolveDecision claims the pending decision by deleting the
// row carrying pending.ID, and runs res.Action only if that delete removed
// it: dismiss persists a suppression, delay and cancelNight run exactly
// [handlers.weatherDelayRunStartOrChange], the same path start/cancel-night
// themselves use. An operator's answer and that decision's own deadline
// race here, as do two coordinators' timers after a restart, and claimed
// is how exactly one of them acts.
func (h *handlers) weatherDelayResolveDecision(ctx context.Context, now time.Time, pending store.PendingWeatherDelayDecisionRecord, res weatherDelayDecisionResolution, ac authContext, clientAddr string) (v1.WeatherDelayDecisionResponse, bool) {
	answer := res.Answer
	claimed, err := h.deps.WeatherDelayTrigger.ClearPendingWeatherDelayDecision(ctx, pending.ID)
	if err != nil {
		h.logWarn("weather delay decision: failed to claim the pending decision; taking no action on it", "error", err)
		return v1.WeatherDelayDecisionResponse{ServerTime: formatTime(now), Answer: answer}, false
	}
	if !claimed {
		return v1.WeatherDelayDecisionResponse{ServerTime: formatTime(now), Answer: answer}, false
	}

	resp := v1.WeatherDelayDecisionResponse{ServerTime: formatTime(now), Answer: answer}
	if res.Outcome == weatherDelayDecisionOutcomeExpired {
		resp.Message = weatherDelayDecisionExpiredMessage
	}
	auditParams := map[string]any{
		"pendingDecisionId": pending.ID, "source": pending.Source, "answer": answer,
		"outcome": res.Outcome, "deadlineDefault": res.ViaDeadline,
	}

	switch res.Action {
	case weathertrigger.AnswerDismiss:
		quietMinutes := config.WeatherDelayDefaultPayload.Triggers.DismissQuietMinutes
		if payload, _, _, _, err := resolveWeatherDelayConfig(ctx, h.deps.Config); err != nil {
			h.logWarn("weather delay decision: failed to resolve show.weatherdelay config; using the default quiet period", "error", err)
		} else {
			quietMinutes = payload.Triggers.DismissQuietMinutes
		}
		until := now.Add(time.Duration(quietMinutes) * time.Minute)
		if err := h.deps.WeatherDelayTrigger.SetWeatherDelayTriggerSuppression(ctx, store.WeatherDelayTriggerSuppressionRecord{
			Source: pending.Source, WarningKey: pending.WarningKey, Until: until,
		}); err != nil {
			h.logWarn("weather delay decision: failed to persist the dismiss suppression", "error", err)
		}
	case weathertrigger.AnswerDelay:
		result := h.weatherDelayRunStartOrChange(ctx, now, weatherdelay.KindDelay, identity.AuditActionShowWeatherDelayStart, "", ac, clientAddr, nil)
		resp.Result = &result
	case weathertrigger.AnswerCancelNight:
		result := h.weatherDelayRunStartOrChange(ctx, now, weatherdelay.KindCancelNight, identity.AuditActionShowWeatherDelayCancelNight, "", ac, clientAddr, h.weatherDelayCancelNightAfterDispatch)
		resp.Result = &result
	}

	h.writeBestEffortAuditBounded(ctx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: clientAddr,
		Action: identity.AuditActionShowWeatherDelayDecision, Target: pending.Source,
		Kind: identity.AuditOutcome, Params: auditParams,
	})
	h.weatherDelayNotifyDecision(ctx, "decisionResolved", weathertrigger.PendingDecision{
		ID: pending.ID, Source: pending.Source, Reason: pending.Reason, Question: pending.Question,
		DefaultAction: pending.DefaultAction, AskedAt: pending.AskedAt, Deadline: pending.Deadline,
	}, res.Outcome)
	if res.Action == "" {
		h.appendWeatherDelayChangedEvent(ctx, now, "question "+res.Outcome)
	}
	h.notifyStreamHub()
	return resp, true
}

// weatherDelayApplyTriggerDeadline runs pending's default action once its
// deadline has passed, under a synthetic actor named after the trigger
// source (ADR-053 decision 12: "startedBy = the trigger source name").
func (h *handlers) weatherDelayApplyTriggerDeadline(ctx context.Context, now time.Time, pending store.PendingWeatherDelayDecisionRecord) {
	ac := weatherDelayTriggerSystemAuthContext(pending.Source)
	res := weatherDelayDecisionResolution{
		Answer: pending.DefaultAction, Action: pending.DefaultAction,
		Outcome: pending.DefaultAction, ViaDeadline: true,
	}
	if !weathertrigger.DecisionStillActionable(now, pending.Deadline, pending.ExpiresAt) {
		res = weatherDelayDecisionResolution{
			Answer: pending.DefaultAction, Outcome: weatherDelayDecisionOutcomeExpired, ViaDeadline: true,
		}
	}
	_, _ = h.weatherDelayResolveDecision(ctx, now, pending, res, ac, "")
}

// weatherDelayDismissPendingDecisionOnResume settles any outstanding
// trigger question the way a resume implies: nothing starts from it, and
// the same warning stays quiet for the dismiss quiet period.
func (h *handlers) weatherDelayDismissPendingDecisionOnResume(ctx context.Context, now time.Time, ac authContext, clientAddr string) {
	pending, ok, err := h.deps.WeatherDelayTrigger.GetPendingWeatherDelayDecision(ctx)
	if err != nil {
		h.logWarn("weather delay resume: failed to read the pending decision; leaving it in place", "error", err)
		return
	}
	if !ok {
		return
	}
	_, _ = h.weatherDelayResolveDecision(ctx, now, pending, weatherDelayDecisionResolution{
		Answer: weathertrigger.AnswerDismiss, Action: weathertrigger.AnswerDismiss,
		Outcome: weatherDelayDecisionOutcomeDismissedByResume,
	}, ac, clientAddr)
}

// weatherDelayTriggerSystemAuthContext is the synthetic authContext an
// unanswered trigger's default action and the NWS poller's own reported
// triggers act under: no HTTP request authenticated it, so its principal
// is the trigger source's own name.
func weatherDelayTriggerSystemAuthContext(source string) authContext {
	return authContext{ok: true, result: identity.Authenticated{
		Principal: identity.Principal{ID: source, Name: source},
		Form:      identity.FormCLI,
	}}
}

// weatherDelayOnNWSAlert is the built-in NWS poller's onAlert callback
// (weatherdelaytriggerloop.go wires it in): it runs the same intake as the
// inbound endpoint, under the poller's own synthetic actor.
func (h *handlers) weatherDelayOnNWSAlert(ev weathertrigger.TriggerEvent) error {
	now := h.now()
	ac := weatherDelayTriggerSystemAuthContext(ev.Source)
	_, _, _, err := h.weatherDelayReceiveTrigger(context.Background(), now, ev, ac, "")
	return err
}

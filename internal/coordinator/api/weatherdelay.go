package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// GET /api/v1/weather-delay (the live state, working now) and the three
// trigger routes (start/cancel-night/resume). ADR-053 decision 1 fixes the
// vocabulary; none of the three trigger routes dispatches anything yet —
// each answers [ProblemTypeNotImplemented] behind its own scope, so a
// caller can already tell "not authorized" apart from "not built yet"
// before the night loop, cue activation refusal, enforcement loop, and
// plugin output gate land on later branches.

var (
	scopeShowWeatherDelayInvoke = identity.ScopeShowWeatherDelayInvoke
	scopeShowWeatherDelayResume = identity.ScopeShowWeatherDelayResume
)

const maxWeatherDelayActionRequestBodyBytes = 1024

// handleGetWeatherDelayState serves GET /api/v1/weather-delay: the
// persisted state, or "not active" when nothing has ever been written
// (store.GetWeatherDelayState's own default).
func (h *handlers) handleGetWeatherDelayState(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	rec, err := h.deps.WeatherDelay.GetWeatherDelayState(ctx)
	if err != nil {
		h.writeInternalError(w, now, "get weather delay state", err)
		return
	}

	resp := v1.WeatherDelayStateResponse{ServerTime: formatTime(now), Active: rec.Active, Revision: rec.Revision}
	if rec.Active {
		resp.Kind = rec.Kind
		resp.StartedAt = formatTime(rec.StartedAt)
		resp.StartedBy = rec.StartedBy
	}
	jsonWrite(w, resp)
}

// decodeWeatherDelayActionRequestBody parses and validates the shared
// {"idempotencyKey": string} body every trigger route accepts, on
// decodeEmergencyStopIdempotencyKeyBody's own shape (this route accepts no
// armToken sibling key: nothing here is a deliberate-intent hard-stop-style
// gate).
func decodeWeatherDelayActionRequestBody(r *http.Request) (idempotencyKey string, problem *v1.Problem) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWeatherDelayActionRequestBodyBytes+1))
	if err != nil {
		p := invalidParameterProblem(fmt.Sprintf("reading request body: %v", err))
		return "", &p
	}
	if int64(len(body)) > maxWeatherDelayActionRequestBodyBytes {
		p := invalidParameterProblem("request body too large")
		return "", &p
	}
	var top map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &top); err != nil {
			p := invalidParameterProblem(`request body must be a JSON object matching {"idempotencyKey":string}`)
			return "", &p
		}
	}
	var unknown []string
	for k := range top {
		if k != "idempotencyKey" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		p := invalidParameterProblem(fmt.Sprintf("request body contains unrecognized key(s): %v", unknown))
		return "", &p
	}
	if raw, ok := top["idempotencyKey"]; ok {
		_ = json.Unmarshal(raw, &idempotencyKey)
	}
	return idempotencyKey, nil
}

// handleWeatherDelayStart serves POST /api/v1/weather-delay/start: behind
// show:weatherdelay:invoke, ADR-053 decision 8's "starting is accepted
// from anywhere" scope.
func (h *handlers) handleWeatherDelayStart(w http.ResponseWriter, r *http.Request) {
	h.handleWeatherDelayNotImplemented(w, r, "starting a weather delay")
}

// handleWeatherDelayCancelNight serves POST /api/v1/weather-delay/cancel-night,
// behind the same show:weatherdelay:invoke umbrella authority as start
// (ADR-053 decision 1: "Both take one press ... available in the API").
func (h *handlers) handleWeatherDelayCancelNight(w http.ResponseWriter, r *http.Request) {
	h.handleWeatherDelayNotImplemented(w, r, "cancelling the night for weather")
}

// handleWeatherDelayResume serves POST /api/v1/weather-delay/resume,
// behind its OWN scope, show:weatherdelay:resume (ADR-053 decision 8:
// "Resume is accepted only by the authenticated coordinator API").
func (h *handlers) handleWeatherDelayResume(w http.ResponseWriter, r *http.Request) {
	h.handleWeatherDelayNotImplemented(w, r, "resuming from a weather delay")
}

func (h *handlers) handleWeatherDelayNotImplemented(w http.ResponseWriter, r *http.Request, action string) {
	now := h.now()
	if _, problem := decodeWeatherDelayActionRequestBody(r); problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	writeProblem(w, h.logger, now, notImplementedProblem(
		fmt.Sprintf("%s is not available yet on this coordinator", action)))
}

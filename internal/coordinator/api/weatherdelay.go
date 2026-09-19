package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/command"
)

// GET /api/v1/weather-delay reports the stored state. The start,
// cancel-night and resume routes check scope and body, then answer 501.

var (
	scopeShowWeatherDelayInvoke = identity.ScopeShowWeatherDelayInvoke
	scopeShowWeatherDelayResume = identity.ScopeShowWeatherDelayResume
)

const maxWeatherDelayActionRequestBodyBytes = 1024

// handleGetWeatherDelayState serves GET /api/v1/weather-delay.
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

// decodeWeatherDelayActionRequestBody reads the {"idempotencyKey": string}
// body the three action routes share.
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
		if err := json.Unmarshal(raw, &idempotencyKey); err != nil {
			p := invalidParameterProblem("idempotencyKey must be a string")
			return "", &p
		}
	}
	if err := command.ValidateIdempotencyKey(idempotencyKey); err != nil {
		p := invalidParameterProblem("idempotencyKey: " + err.Error())
		return "", &p
	}
	return idempotencyKey, nil
}

// handleWeatherDelayStart serves POST /api/v1/weather-delay/start.
func (h *handlers) handleWeatherDelayStart(w http.ResponseWriter, r *http.Request) {
	h.handleWeatherDelayNotImplemented(w, r, "starting a weather delay")
}

// handleWeatherDelayCancelNight serves POST /api/v1/weather-delay/cancel-night.
func (h *handlers) handleWeatherDelayCancelNight(w http.ResponseWriter, r *http.Request) {
	h.handleWeatherDelayNotImplemented(w, r, "cancelling the night for weather")
}

// handleWeatherDelayResume serves POST /api/v1/weather-delay/resume.
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

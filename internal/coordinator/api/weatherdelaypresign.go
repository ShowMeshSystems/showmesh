package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// weatherDelayNodeStartPath mirrors the node's own
// /showmesh/v1/weather-delay/start route (internal/agent/weatherdelayhttp.go),
// matching [handlers.weatherDelayHTTPNodeStart]'s identical literal one
// file over: this package must never import internal/agent.
const weatherDelayNodeStartPath = "/showmesh/v1/weather-delay/start"

// Pre-signed start (ADR-053 decision 8): an outside system holds this
// document and sends it directly to a node when the coordinator is down.
// Replaying it can only start a delay or a cancel night, never a resume.

const maxWeatherDelayPresignRequestBodyBytes = 1024

// weatherDelayPresignMinValidDays and weatherDelayPresignMaxValidDays
// bound validDays; the maximum matches [weatherdelay.MaxPresignValidDays],
// the node's own hard cap on notAfter.
const weatherDelayPresignMinValidDays = 1

// decodeWeatherDelayPresignRequestBody reads {"kind":string,"validDays":int}.
func decodeWeatherDelayPresignRequestBody(r *http.Request) (kind string, validDays int, problem *v1.Problem) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWeatherDelayPresignRequestBodyBytes+1))
	if err != nil {
		p := invalidParameterProblem(fmt.Sprintf("reading request body: %v", err))
		return "", 0, &p
	}
	if int64(len(body)) > maxWeatherDelayPresignRequestBodyBytes {
		p := invalidParameterProblem("request body too large")
		return "", 0, &p
	}
	var req v1.WeatherDelayPresignedStartRequest
	if err := json.Unmarshal(body, &req); err != nil {
		p := invalidParameterProblem(`request body must be a JSON object matching {"kind":string,"validDays":number}`)
		return "", 0, &p
	}
	if !weatherdelay.ValidKind(req.Kind) {
		p := invalidParameterProblem(fmt.Sprintf("kind %q must be %q or %q", req.Kind, weatherdelay.KindDelay, weatherdelay.KindCancelNight))
		return "", 0, &p
	}
	if req.ValidDays < weatherDelayPresignMinValidDays || req.ValidDays > weatherdelay.MaxPresignValidDays {
		p := invalidParameterProblem(fmt.Sprintf("validDays %d must be between %d and %d", req.ValidDays, weatherDelayPresignMinValidDays, weatherdelay.MaxPresignValidDays))
		return "", 0, &p
	}
	return req.Kind, req.ValidDays, nil
}

// handleWeatherDelayPresignedStart serves
// POST /api/v1/weather-delay/presigned-start.
func (h *handlers) handleWeatherDelayPresignedStart(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	kind, validDays, problem := decodeWeatherDelayPresignRequestBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	req := weatherdelay.StartRequest{
		Kind: kind, IssuedAt: now, Nonce: uuid.NewString(),
		NotAfter: now.AddDate(0, 0, validDays),
	}
	signed, err := weatherdelay.Sign(req, h.deps.WeatherDelaySigner)
	if err != nil {
		h.writeInternalError(w, now, "sign the presigned weather delay start", err)
		return
	}

	nodeURLs := h.weatherDelayPresignNodeURLs(ctx)

	ac := authFromContext(r.Context())
	h.writeBestEffortAuditBounded(ctx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: identity.AuditActionShowWeatherDelayPresign, Target: kind, Kind: identity.AuditOutcome,
		Params: map[string]any{"validDays": validDays, "nodeUrls": len(nodeURLs)},
	})

	jsonWrite(w, v1.WeatherDelayPresignedStartResponse{ServerTime: formatTime(now), Request: signed, NodeURLs: nodeURLs})
}

// weatherDelayPresignNodeURLs lists every plan node's own reported inbound
// listener address, as a full start URL. Missing or unreachable nodes are
// left out; the list is advisory, never a promise (see
// [v1.WeatherDelayPresignedStartResponse]'s own doc comment).
func (h *handlers) weatherDelayPresignNodeURLs(ctx context.Context) []string {
	payload, _, _, _, err := resolveWeatherDelayConfig(ctx, h.deps.Config)
	if err != nil {
		h.logWarn("weather delay presign: failed to resolve show.weatherdelay config; reporting no node urls", "error", err)
		return []string{}
	}
	nodeIDs, err := h.weatherDelayPlanNodeIDs(ctx, payload.Alert.NodeIDs)
	if err != nil {
		h.logWarn("weather delay presign: failed to resolve plan node ids; reporting no node urls", "error", err)
		return []string{}
	}
	urls := make([]string, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		if addr, ok := h.deps.WeatherDelayNodeAddrs.InboundListener(nodeID); ok && addr != "" {
			urls = append(urls, "http://"+addr+weatherDelayNodeStartPath)
		}
	}
	return urls
}

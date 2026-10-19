package api

import (
	"context"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// nightRenderFadeStart sends every declared render surface the projection
// fade matching a lights step, with the same target and end time (ADR-063).
// The sends run detached: a slow node never holds up the night loop.
func (h *handlers) nightRenderFadeStart(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload, st nightLightsStep) {
	if payload.LightsFadeOutMs == nil && payload.LightsFadeInMs == nil {
		return
	}
	params := nightRenderFadeParams(payload, st)
	surfaces, err := h.declaredRenderSurfaces(ctx)
	if err != nil {
		h.logWarn("night loop: listing render surfaces for the projection fade failed", "sessionId", rec.ID, "error", err)
		return
	}
	issuer := nightControllerIssuer(rec)
	bgCtx := context.WithoutCancel(ctx)
	for _, s := range surfaces {
		p := map[string]any{"surfaceId": s.SurfaceID}
		for k, v := range params {
			p[k] = v
		}
		in := renderDispatchInput{
			Action: "render.surface.fade", NodeID: s.NodeID, SurfaceID: s.SurfaceID,
			Params:         p,
			IdempotencyKey: "night-render-fade:" + st.key + ":" + s.NodeID + ":" + s.SurfaceID,
			IssuerID:       issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		}
		h.nightRenderFadeWG.Add(1)
		go func() {
			defer h.nightRenderFadeWG.Done()
			if _, problem, err := h.executeRenderDispatch(bgCtx, now, in); err != nil || problem != nil {
				h.logWarn("night loop: projection fade was not sent", "sessionId", rec.ID, "nodeId", in.NodeID, "surfaceId", in.SurfaceID, "error", err, "problem", problem)
			}
		}()
	}
}

// nightRenderFadeParams maps a lights step onto render.surface.fade timing.
// A fade-out ends with the lights at the boundary and starts later when its
// own time is shorter; a fade-in starts with the lights; the rest are instant.
func nightRenderFadeParams(payload config.NightSessionPayload, st nightLightsStep) map[string]any {
	params := map[string]any{"targetPercent": float64(st.target)}
	switch {
	case !st.endsAt.IsZero():
		params["endsAt"] = st.endsAt.UTC().Format(time.RFC3339Nano)
		if payload.RenderFadeOutMs != nil {
			params["startsAt"] = st.endsAt.Add(-time.Duration(*payload.RenderFadeOutMs) * time.Millisecond).UTC().Format(time.RFC3339Nano)
		}
	case st.seconds > 0:
		d := time.Duration(st.seconds) * time.Second
		if payload.RenderFadeInMs != nil {
			d = time.Duration(*payload.RenderFadeInMs) * time.Millisecond
		} else if payload.LightsFadeInMs != nil {
			d = time.Duration(*payload.LightsFadeInMs) * time.Millisecond
		}
		params["durationMs"] = float64(d.Milliseconds())
	}
	return params
}

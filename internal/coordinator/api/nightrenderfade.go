package api

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// A projection fade send that did not reach the broker is retried at this
// interval until the step is replaced, its fade ends, or the window passes.
var (
	nightRenderFadeRetryBackoff = 5 * time.Second
	nightRenderFadeRetryWindow  = 2 * time.Minute
)

// nightRenderFadeOutMs is the projection fade-out length: its own, or the
// lights fade-out. Nil means projections do not fade out.
func nightRenderFadeOutMs(p config.NightSessionPayload) *int {
	if p.RenderFadeOutMs != nil {
		return p.RenderFadeOutMs
	}
	return p.LightsFadeOutMs
}

// nightRenderFadeInMs is the projection fade-in length: its own, or the
// lights fade-in. Nil means projections do not fade in.
func nightRenderFadeInMs(p config.NightSessionPayload) *int {
	if p.RenderFadeInMs != nil {
		return p.RenderFadeInMs
	}
	return p.LightsFadeInMs
}

// nightRenderFadeOutNightMs is Fade Out Night's projection fade length.
func nightRenderFadeOutNightMs(p config.NightSessionPayload) *int {
	if p.FadeOutNightMs != nil {
		return p.FadeOutNightMs
	}
	return nightRenderFadeOutMs(p)
}

func nightRenderFadesConfigured(p config.NightSessionPayload) bool {
	return nightRenderFadeOutMs(p) != nil || nightRenderFadeInMs(p) != nil
}

func nightRenderFadeKey(rec store.NightSessionRecord, step string) string {
	return nightLightsKey(rec, "render-"+step)
}

// nightRenderFadeSent reports whether this cycle's projection step was sent.
func (h *handlers) nightRenderFadeSent(rec store.NightSessionRecord, step string) bool {
	_, ok := h.nightRenderFadesSent.Load(nightRenderFadeKey(rec, step))
	return ok
}

// nightRenderFade sends every declared render surface one projection fade
// step, once per session and cycle (ADR-063). A zero endsAt and durationMs
// is an instant change. The sends never hold up the night loop.
func (h *handlers) nightRenderFade(ctx context.Context, now time.Time, rec store.NightSessionRecord, step string, target int, startsAt, endsAt time.Time, durationMs int64) {
	key := nightRenderFadeKey(rec, step)
	if _, loaded := h.nightRenderFadesSent.LoadOrStore(key, true); loaded {
		return
	}
	surfaces, err := h.declaredRenderSurfaces(ctx)
	if err != nil {
		h.nightRenderFadesSent.Delete(key)
		h.logWarn("night loop: listing render surfaces for the projection fade failed", "sessionId", rec.ID, "error", err)
		return
	}
	issuer := nightControllerIssuer(rec)
	bgCtx := context.WithoutCancel(ctx)
	until := now.Add(nightRenderFadeRetryWindow)
	if !endsAt.IsZero() {
		until = endsAt
	}
	for _, s := range surfaces {
		p := map[string]any{"surfaceId": s.SurfaceID, "targetPercent": float64(target)}
		if !startsAt.IsZero() {
			p["startsAt"] = startsAt.UTC().Format(time.RFC3339Nano)
		}
		if !endsAt.IsZero() {
			p["endsAt"] = endsAt.UTC().Format(time.RFC3339Nano)
		}
		if durationMs > 0 {
			p["durationMs"] = float64(durationMs)
		}
		in := renderDispatchInput{
			Action: "render.surface.fade", NodeID: s.NodeID, SurfaceID: s.SurfaceID,
			Params:         p,
			IdempotencyKey: "night-render-fade:" + key + ":" + s.NodeID + ":" + s.SurfaceID,
			IssuerID:       issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		}
		lane := rec.ID + "|" + s.NodeID + "|" + s.SurfaceID
		seq := h.nightRenderFadeSeq.Add(1)
		h.nightRenderFadeLatest.Store(lane, seq)
		h.nightRenderFadeWG.Add(1)
		go func() {
			defer h.nightRenderFadeWG.Done()
			h.nightRenderFadeSend(bgCtx, now, rec, in, lane, seq, until)
		}()
	}
}

// nightRenderFadeSend sends one surface's fade step, retrying a send that
// did not reach the broker while the step is still the surface's newest.
func (h *handlers) nightRenderFadeSend(ctx context.Context, now time.Time, rec store.NightSessionRecord, in renderDispatchInput, lane string, seq uint64, until time.Time) {
	key, started := in.IdempotencyKey, time.Now()
	for attempt := 1; ; attempt++ {
		if latest, _ := h.nightRenderFadeLatest.Load(lane); latest != seq {
			return
		}
		at := now.Add(time.Since(started))
		_, problem, err := h.executeRenderDispatch(ctx, at, in)
		if err == nil && problem == nil {
			return
		}
		h.logWarn("night loop: projection fade was not sent", "sessionId", rec.ID, "nodeId", in.NodeID, "surfaceId", in.SurfaceID, "attempt", attempt, "error", err, "problem", problem)
		if problem != nil || !at.Add(nightRenderFadeRetryBackoff).Before(until) {
			return
		}
		t := time.NewTimer(nightRenderFadeRetryBackoff)
		<-t.C
		in.IdempotencyKey = fmt.Sprintf("%s:retry-%d", key, attempt)
	}
}

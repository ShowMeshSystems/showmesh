package api

import (
	"context"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// nightCueTarget is the target a night cue dispatches: the declared target,
// with a Resolume fadeLayer's duration taken from the transition.
func (h *handlers) nightCueTarget(ctx context.Context, rec store.NightSessionRecord, phase string, cue config.NightSessionCue, target config.ShowActionTarget) config.ShowActionTarget {
	target = h.nightAnnouncementDeclaredTarget(ctx, rec, cue, target)
	if target.Integration != config.ShowActionIntegrationResolume || target.Action != config.ShowActionResolumeFadeLayer {
		return target
	}
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		return target
	}
	return nightFadeLayerTarget(target, cue, phase, payload)
}

// nightFadeLayerTarget sets a fadeLayer's durationMs: the step's own fade
// time, else the lights fade for that direction, else the action's own.
func nightFadeLayerTarget(target config.ShowActionTarget, cue config.NightSessionCue, phase string, payload config.NightSessionPayload) config.ShowActionTarget {
	ms := cue.FadeDurationMs
	if ms == nil {
		switch phase {
		case nightPhaseEnterShow, nightPhaseFadeOut:
			ms = payload.LightsFadeOutMs
		case nightPhaseEnterResting:
			ms = payload.LightsFadeInMs
		}
	}
	if ms == nil {
		return target
	}
	ref := make(map[string]any, len(target.Ref)+1)
	for k, v := range target.Ref {
		ref[k] = v
	}
	ref["durationMs"] = float64(*ms)
	target.Ref = ref
	return target
}

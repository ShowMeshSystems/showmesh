package api

import (
	"context"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// Track F seam F4's own readiness checks, called from
// nightComputeReadinessChecks alongside nightasset.go's own checks.

// nightCheckFirstOutwardCueConfirmable is §7.1.1's own gate, surfaced at
// readiness time: the earliest-offset enterShow cue must resolve to a
// [nightCueConfirmable] action, because its atomic commit can never be
// reversed once it dispatches.
func (h *handlers) nightCheckFirstOutwardCueConfirmable(ctx context.Context, cues []config.NightSessionCue) nightReadinessCheck {
	name := "enterShow:first-cue-confirmable"
	sorted := sortedNightCues(cues)
	if len(sorted) == 0 {
		return nightReadinessCheck{name: name, health: nightCheckStateNotVerifiable, reason: "no enterShow cues are configured"}
	}
	first := sorted[0]
	action, _, err := nightResolveShowAction(ctx, h.deps.Config, first.Action)
	if err != nil {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: fmt.Sprintf("could not resolve cue %q's action %q: %s", first.Name, first.Action, err.Error())}
	}
	if !nightCueConfirmable(action.Target) {
		reason := fmt.Sprintf("cue %q is the earliest-offset enterShow cue, and its action's own adapter cannot confirm its effect; it may not be the first outward-facing cue", first.Name)
		if action.Target.Integration == config.ShowActionIntegrationMQTT {
			reason = fmt.Sprintf("cue %q is the earliest-offset enterShow cue, and its mqtt action has no dispatcher wired into this coordinator build; it may not be the first outward-facing cue", first.Name)
		}
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: reason}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy()}
}

// nightCheckNoUnbuiltBrightnessComposition rejects any lighting-role cue
// that declares a fade duration: the ceiling times transition-gain
// provider that could apply one without overwriting the scheduled
// ceiling is specified (RES-018) but not yet built.
func nightCheckNoUnbuiltBrightnessComposition(transition string, cues []config.NightSessionCue) nightReadinessCheck {
	name := transition + ":brightness-composition"
	for _, cue := range cues {
		if cue.Role == config.NightSessionCueRoleLighting && cue.FadeDurationMs != nil {
			return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(
				"cue %q declares a lighting fade, and the brightness provider that can apply a fade without overwriting the currently scheduled ceiling is specified but not yet built or verified", cue.Name)}
		}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy()}
}

package api

import (
	"context"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// Track F seam F4's own readiness checks, called from
// nightComputeReadinessChecks alongside nightasset.go's own checks.

// nightCheckFirstOutwardCueConfirmable is §7.1.1's own gate, surfaced at
// readiness time: the earliest-offset enterShow cue must resolve to an
// action [nightCueAllowedAsFirstOutwardCue] accepts - either its own
// adapter can confirm the effect, or the action itself declares
// idempotent true - because its atomic commit can never be reversed once
// it dispatches. The two ways to fail that are refused with distinct
// reasons: a declared-false action and an undeclared one point an
// operator at two different fixes, and only the second is fixed by adding
// a field.
func (h *handlers) nightCheckFirstOutwardCueConfirmable(ctx context.Context, cues []config.NightSessionCue) nightReadinessCheck {
	name := "enterShow:first-cue-confirmable"
	sorted := sortedNightCues(cues)
	if len(sorted) == 0 {
		return nightReadinessCheck{name: name, health: nightCheckStateNotVerifiable, reason: "No cue is set to run when the show starts."}
	}
	first := sorted[0]
	action, _, err := nightResolveShowAction(ctx, h.deps.Config, first.Action)
	if err != nil {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), cause: err,
			reason: fmt.Sprintf("The coordinator could not read the action for cue %s. Run readiness again.", first.Name)}
	}
	if !nightCueAllowedAsFirstOutwardCue(action) {
		reason := fmt.Sprintf("Cue %s runs first when the show starts, and its action does not say if it is safe to repeat. Set that on the action.", first.Name)
		if action.Idempotent != nil && !*action.Idempotent {
			reason = fmt.Sprintf("Cue %s runs first when the show starts, and its action is not safe to repeat. Put a repeatable cue first.", first.Name)
		}
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: reason}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy()}
}

// nightCheckBrightnessComposition names the lighting cue that fades the
// lights in a transition. It never warns: a fade's real-display check is a
// rig test, not a readiness condition.
func nightCheckBrightnessComposition(transition string, cues []config.NightSessionCue) nightReadinessCheck {
	name := transition + ":brightness-composition"
	for _, cue := range cues {
		if cue.Role == config.NightSessionCueRoleLighting && cue.FadeDurationMs != nil {
			return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: fmt.Sprintf("Cue %s fades the lights.", cue.Name)}
		}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy()}
}

// nightCheckEndOfNightCues states which cues run after the final show, so
// an operator can see that an omitted block still runs the resting cues.
func nightCheckEndOfNightCues(payload config.NightSessionPayload) nightReadinessCheck {
	const name = "enterEndOfNight:cues"
	switch payload.EndOfNightMode() {
	case config.NightSessionEndOfNightModeNone:
		return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: "After the last show no cues run, and only the end of night playlist starts."}
	case config.NightSessionEndOfNightModeCustom:
		return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: fmt.Sprintf("After the last show the %d end of night cues run instead of the resting cues.", len(payload.EnterEndOfNight.Cues))}
	default:
		return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: "After the last show the resting cues run, because no end of night cues are set."}
	}
}

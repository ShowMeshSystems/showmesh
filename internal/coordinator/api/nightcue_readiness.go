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
			reason: fmt.Sprintf("ShowMesh could not read the action for cue %s. Run readiness again.", first.Name)}
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

// nightCheckBrightnessCompositionUnverified warns, and no longer refuses,
// on a lighting-role cue that declares a fade duration.
//
// It used to fail, because the ceiling times transition-gain provider was
// specified and not built. The provider is now built: the plugin serves
// the transition-gain route on both FPP majors and the coordinator writes
// it as a cue effect. What is still missing is the other half of
// RESTING-MODE's own condition. Line 228 rejects "until the ShowMesh
// component passes its real-host acceptance matrix", and line 236 says
// the seam "must be implemented before this behavior can be claimed".
// Implemented is now true; the real-host acceptance matrix is the owner's
// rig work and does not exist yet.
//
// So failing would make a built feature unreachable, and reporting
// healthy would claim behaviour the spec explicitly forbids claiming
// until it is verified on a real host. Degraded is the honest third
// answer: the night starts, every consumer treats it as ready, and the
// operator sees the one thing still outstanding before showtime. The
// warning is what the acceptance matrix clears.
func nightCheckBrightnessCompositionUnverified(transition string, cues []config.NightSessionCue) nightReadinessCheck {
	name := transition + ":brightness-composition"
	for _, cue := range cues {
		if cue.Role == config.NightSessionCueRoleLighting && cue.FadeDurationMs != nil {
			return nightReadinessCheck{name: name, health: nightHealthDegraded(), reason: fmt.Sprintf(
				"Cue %s fades the lights. The fade will run, but it has not been proven on real hardware.", cue.Name)}
		}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy()}
}

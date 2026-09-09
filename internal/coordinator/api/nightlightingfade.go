package api

import (
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// The brightness transition gain a lighting cue asks for, and when it is
// written relative to that cue's own action.
//
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 2.1: effective output is
// the installation's scheduled ceiling times this gain. ShowMesh owns the
// gain and never the ceiling, so a fade here can never overwrite a limit
// FPP's own schedule set.

// nightLightingFade is one lighting cue's gain write. Nil means the cue
// asks for no fade, which is every cue that is not lighting-role and
// every lighting cue that declared no duration.
type nightLightingFade struct {
	// TargetPercent is the gain to reach, 0-100. Not a brightness: the
	// display's actual level is this times FPP's own ceiling.
	TargetPercent int

	// FadeSeconds is how long to take. 0 applies immediately.
	FadeSeconds int

	// BeforeAction is the ordering rule, and there is one principle
	// behind it rather than two cases: THE CONTENT CHANGE HAPPENS WHILE
	// THE DISPLAY IS DARK.
	//
	// Entering a show, the gain goes down first and the cue's action runs
	// after, so nobody watches the resting look being torn down. Entering
	// resting, the action runs first and the gain comes up after, so
	// nobody watches the resting look being assembled. A later transition
	// follows the same principle rather than looking up its own case.
	BeforeAction bool
}

// nightLightingFadeFor returns the gain write a cue asks for in phase, or
// nil if it asks for none.
//
// The direction is a property of the transition, not of the cue, because
// a cue carries a duration and no target level (config.NightSessionCue
// has no such field). RESTING-MODE.md's own example block is what names
// them: enterShow carries `lighting.fadeDuration` while the show takes
// over, and enterResting carries `lightingFadeIn` while the resting look
// returns.
//
// Fading up goes to 100 rather than to any remembered level, which is the
// same rule stated in two places: RESTING-MODE.md line 236, "A fade-up
// returns to the current ceiling, not a cached earlier ceiling", and
// contract section 2.3's fourth forbidden case. A gain of 100 reveals
// whatever ceiling FPP holds at that moment, so a ceiling the schedule
// changed during the show is honoured rather than overwritten.
func nightLightingFadeFor(cue config.NightSessionCue, phase string) *nightLightingFade {
	if cue.Role != config.NightSessionCueRoleLighting || cue.FadeDurationMs == nil {
		return nil
	}
	// Rounded to whole seconds because that is the unit contract section
	// 2.2 accepts. A sub-second duration becomes 0, an immediate apply,
	// rather than being refused: the authored intent is still "change it
	// now", and refusing a cue over a rounding boundary would fail a
	// night for a fade nobody would perceive.
	fadeSeconds := (*cue.FadeDurationMs + 500) / 1000

	switch phase {
	case nightPhaseEnterShow:
		return &nightLightingFade{TargetPercent: 0, FadeSeconds: fadeSeconds, BeforeAction: true}
	case nightPhaseEnterResting:
		return &nightLightingFade{TargetPercent: 100, FadeSeconds: fadeSeconds, BeforeAction: false}
	default:
		// Any other phase asks for no gain write. Returning nil rather
		// than guessing a direction: a phase this function does not know
		// has no established answer to "dark or lit afterwards", and
		// inventing one would dim an installation on a transition nobody
		// designed a fade for.
		return nil
	}
}

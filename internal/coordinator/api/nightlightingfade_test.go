package api

import (
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

func lightingCue(fadeMs *int) config.NightSessionCue {
	return config.NightSessionCue{Name: "house-lights", Role: config.NightSessionCueRoleLighting, FadeDurationMs: fadeMs}
}

func intPtr(v int) *int { return &v }

// TestNightLightingFadeDirectionComesFromTheTransition is the behaviour
// RESTING-MODE's own example block names: enterShow carries
// lighting.fadeDuration while the show takes over, enterResting carries
// lightingFadeIn while the resting look returns. A cue carries a duration
// and no target level, so the direction cannot come from the cue.
func TestNightLightingFadeDirectionComesFromTheTransition(t *testing.T) {
	cue := lightingCue(intPtr(15000))

	down := nightLightingFadeFor(cue, nightPhaseEnterShow)
	if down == nil {
		t.Fatal("enterShow lighting cue asked for no fade")
	}
	if down.TargetPercent != 0 {
		t.Errorf("enterShow target = %d, want 0: the resting look goes away as the show takes over", down.TargetPercent)
	}
	if !down.BeforeAction {
		t.Error("enterShow fade runs after the action; the content change must happen while the display is dark")
	}

	up := nightLightingFadeFor(cue, nightPhaseEnterResting)
	if up == nil {
		t.Fatal("enterResting lighting cue asked for no fade")
	}
	// 100 rather than any remembered level. RESTING-MODE line 236 and
	// contract 2.3's fourth forbidden case both say a fade-up returns to
	// the CURRENT ceiling, so the gain reveals whatever FPP holds now.
	// Storing and restoring a previous level would reinstate a ceiling
	// the schedule had since changed.
	if up.TargetPercent != 100 {
		t.Errorf("enterResting target = %d, want 100", up.TargetPercent)
	}
	if up.BeforeAction {
		t.Error("enterResting fade runs before the action; the resting look must be assembled in the dark")
	}
}

func TestNightLightingFadeIgnoresCuesThatAskForNone(t *testing.T) {
	for _, tc := range []struct {
		name string
		cue  config.NightSessionCue
	}{
		{"lighting cue with no duration", lightingCue(nil)},
		{"a non-lighting cue that declares one", config.NightSessionCue{
			Name: "bed", Role: config.NightSessionCueRoleAudio, FadeDurationMs: intPtr(3000)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nightLightingFadeFor(tc.cue, nightPhaseEnterShow); got != nil {
				t.Fatalf("asked for a gain write (%+v) where none was declared", got)
			}
		})
	}
}

// TestNightLightingFadeRefusesToGuessAnUnknownPhase: a phase with no
// designed fade direction gets no gain write at all. Guessing one would
// dim a real installation on a transition nobody authored a fade for.
func TestNightLightingFadeRefusesToGuessAnUnknownPhase(t *testing.T) {
	if got := nightLightingFadeFor(lightingCue(intPtr(15000)), nightPhaseFadeOut); got != nil {
		t.Fatalf("phase %q produced a gain write %+v; an undesigned phase must produce none", nightPhaseFadeOut, got)
	}
}

func TestNightLightingFadeRoundsToTheUnitTheContractAccepts(t *testing.T) {
	for _, tc := range []struct {
		ms   int
		want int
	}{
		{15000, 15},
		{0, 0},
		{499, 0}, // sub-second becomes an immediate apply, not a refusal
		{500, 1}, // rounds to nearest rather than truncating
		{10400, 10},
		{10600, 11},
	} {
		got := nightLightingFadeFor(lightingCue(intPtr(tc.ms)), nightPhaseEnterShow)
		if got == nil {
			t.Fatalf("%dms produced no fade", tc.ms)
		}
		if got.FadeSeconds != tc.want {
			t.Errorf("%dms -> %ds, want %ds", tc.ms, got.FadeSeconds, tc.want)
		}
	}
}

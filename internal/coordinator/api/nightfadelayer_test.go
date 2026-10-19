package api

import (
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

func TestNightFadeLayerTargetTakesTheDurationFromTheTransition(t *testing.T) {
	ms := func(v int) *int { return &v }
	fade := config.ShowActionTarget{Integration: config.ShowActionIntegrationResolume, Action: config.ShowActionResolumeFadeLayer,
		Ref: map[string]any{"layer": "Video", "targetPercent": float64(0), "durationMs": float64(500)}}
	payload := config.NightSessionPayload{LightsFadeOutMs: ms(2000), LightsFadeInMs: ms(3000)}

	cases := []struct {
		name  string
		cue   config.NightSessionCue
		phase string
		want  float64
	}{
		{"the step's own fade time wins", config.NightSessionCue{FadeDurationMs: ms(750)}, nightPhaseEnterShow, 750},
		{"enter show follows the lights fade-out", config.NightSessionCue{}, nightPhaseEnterShow, 2000},
		{"fade out night follows the lights fade-out", config.NightSessionCue{}, nightPhaseFadeOut, 2000},
		{"enter resting follows the lights fade-in", config.NightSessionCue{}, nightPhaseEnterResting, 3000},
		{"no lights fade keeps the action's own time", config.NightSessionCue{}, nightPhasePowerOffAction, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nightFadeLayerTarget(fade, tc.cue, tc.phase, payload)
			if got.Ref["durationMs"] != tc.want {
				t.Fatalf("durationMs = %v, want %v", got.Ref["durationMs"], tc.want)
			}
		})
	}
	if fade.Ref["durationMs"] != float64(500) {
		t.Fatalf("the stored action's ref was changed in place: %v", fade.Ref)
	}
}

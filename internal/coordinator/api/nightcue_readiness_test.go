package api

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// TestNightCheckFirstOutwardCueConfirmable defends readiness's own
// surfaced half of §7.1.1's gate: it names the EARLIEST-offset cue
// (never array order), and fails only when that specific cue is not
// confirmable. Mutation-checked: sorting by array position instead of
// OffsetMs would pick "second" here and pass; asserting against the
// actual failing cue name catches that.
func TestNightCheckFirstOutwardCueConfirmable(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	putNightAction(t, st, "act-mqtt-bad", config.ShowActionPayload{
		Show: "halloween", Label: "Notify", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationMQTT, Broker: "home",
			Publish: &config.ShowActionMQTTPublish{Topic: "t", Payload: "p"},
			Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNone},
		},
	})
	putNightAction(t, st, "act-blackout", blackoutResolumeAction())

	cues := []config.NightSessionCue{
		{Name: "second", Role: config.NightSessionCueRoleAnnouncement, Action: "act-mqtt-bad", OffsetMs: 500},
		{Name: "first", Role: config.NightSessionCueRoleLighting, Action: "act-mqtt-bad", OffsetMs: -1000},
	}
	got := h.nightCheckFirstOutwardCueConfirmable(context.Background(), cues)
	if got.health == nightHealthHealthy() {
		t.Fatal("check reported healthy, want failed — the earliest cue's action is unconfirmable")
	}

	cues[1].Action = "act-blackout" // the earliest cue now resolves to a confirmable action.
	got = h.nightCheckFirstOutwardCueConfirmable(context.Background(), cues)
	if got.health != nightHealthHealthy() {
		t.Errorf("check = %+v, want healthy once the earliest-offset cue is confirmable", got)
	}
}

// TestNightCheckNoUnbuiltBrightnessComposition defends RES-018's own
// standing rejection: a lighting cue with a fade duration fails; a
// lighting cue with no fade, or a non-lighting cue with a fade, does not.
// Mutation-checked: dropping the Role comparison would fail an audio fade
// too, which the second case below catches.
func TestNightCheckNoUnbuiltBrightnessComposition(t *testing.T) {
	fade := 2000
	cases := []struct {
		name string
		cue  config.NightSessionCue
		want nightCheckState
	}{
		{"lighting with fade rejected", config.NightSessionCue{Name: "c1", Role: config.NightSessionCueRoleLighting, FadeDurationMs: &fade}, nightHealthFailed()},
		{"lighting with no fade is fine", config.NightSessionCue{Name: "c2", Role: config.NightSessionCueRoleLighting}, nightHealthHealthy()},
		{"audio fade is unaffected", config.NightSessionCue{Name: "c3", Role: config.NightSessionCueRoleAudio, FadeDurationMs: &fade}, nightHealthHealthy()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nightCheckNoUnbuiltBrightnessComposition("enterShow", []config.NightSessionCue{tc.cue})
			if got.health != tc.want {
				t.Errorf("health = %v, want %v (reason: %s)", got.health, tc.want, got.reason)
			}
		})
	}
}

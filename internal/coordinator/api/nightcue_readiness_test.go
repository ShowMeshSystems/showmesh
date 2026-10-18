package api

import (
	"context"
	"strings"
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

func boolPtr(b bool) *bool { return &b }

// mqttUnconfirmableAction builds an mqtt show.action whose adapter cannot
// confirm its effect (Expect.Kind "none"), with idempotent set to
// declared.
func mqttUnconfirmableAction(idempotent *bool) config.ShowActionPayload {
	return config.ShowActionPayload{
		Show: "halloween", Label: "Notify", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationMQTT, Broker: "home",
			Publish: &config.ShowActionMQTTPublish{Topic: "t", Payload: "p"},
			Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNone},
		},
		Idempotent: idempotent,
	}
}

// TestNightCheckFirstOutwardCueConfirmableIdempotentDeclaration defends:
// a declared-idempotent action that its own adapter cannot confirm
// is still accepted as the first outward-facing cue; a declared-false or
// undeclared one is refused, and the two refusals name which of those two
// is blocking the operator, never the same sentence. Mutation-checked:
// dropping the `action.Idempotent != nil && *action.Idempotent` disjunct
// from nightCueAllowedAsFirstOutwardCue would fail the "declared true"
// case here (it would stay refused); collapsing the two refusal reasons
// to one string would fail the "distinct reasons" assertion.
func TestNightCheckFirstOutwardCueConfirmableIdempotentDeclaration(t *testing.T) {
	cueFor := func(action string) []config.NightSessionCue {
		return []config.NightSessionCue{{Name: "notify", Role: config.NightSessionCueRoleAnnouncement, Action: action}}
	}

	t.Run("declared true is healthy despite an unconfirmable adapter", func(t *testing.T) {
		h, st := nightCueTestHandlers(t)
		putNightAction(t, st, "act", mqttUnconfirmableAction(boolPtr(true)))
		got := h.nightCheckFirstOutwardCueConfirmable(context.Background(), cueFor("act"))
		if got.health != nightHealthHealthy() {
			t.Fatalf("check = %+v, want healthy: the action declares idempotent true", got)
		}
	})

	t.Run("declared false is refused, naming the declaration", func(t *testing.T) {
		h, st := nightCueTestHandlers(t)
		putNightAction(t, st, "act", mqttUnconfirmableAction(boolPtr(false)))
		got := h.nightCheckFirstOutwardCueConfirmable(context.Background(), cueFor("act"))
		if got.health == nightHealthHealthy() {
			t.Fatal("check reported healthy, want failed: the action declares idempotent false")
		}
		if !strings.Contains(got.reason, "its action is not safe to repeat") {
			t.Errorf("reason = %q, want it to name the declared-false state", got.reason)
		}
	})

	t.Run("undeclared is refused, naming the absence", func(t *testing.T) {
		h, st := nightCueTestHandlers(t)
		putNightAction(t, st, "act", mqttUnconfirmableAction(nil))
		got := h.nightCheckFirstOutwardCueConfirmable(context.Background(), cueFor("act"))
		if got.health == nightHealthHealthy() {
			t.Fatal("check reported healthy, want failed: the action never declares idempotent")
		}
		if !strings.Contains(got.reason, "does not say if it is safe to repeat") {
			t.Errorf("reason = %q, want it to name the undeclared state", got.reason)
		}
	})

	// The two refusal reasons must differ: an operator reading either one
	// needs to tell "add the field" apart from "this cannot be repeated".
	t.Run("the two refusal reasons are distinguishable", func(t *testing.T) {
		hFalse, stFalse := nightCueTestHandlers(t)
		putNightAction(t, stFalse, "act", mqttUnconfirmableAction(boolPtr(false)))
		falseResult := hFalse.nightCheckFirstOutwardCueConfirmable(context.Background(), cueFor("act"))

		hNil, stNil := nightCueTestHandlers(t)
		putNightAction(t, stNil, "act", mqttUnconfirmableAction(nil))
		nilResult := hNil.nightCheckFirstOutwardCueConfirmable(context.Background(), cueFor("act"))

		if falseResult.reason == nilResult.reason {
			t.Fatalf("declared-false and undeclared produced the SAME reason: %q", falseResult.reason)
		}
	})
}

// A lighting cue's fade is named in readiness and never makes it a warning.
func TestNightCheckBrightnessComposition(t *testing.T) {
	fade := 2000
	cases := []struct {
		name, reason string
		cue          config.NightSessionCue
	}{
		{"lighting with fade is named", "Cue c1 fades the lights.", config.NightSessionCue{Name: "c1", Role: config.NightSessionCueRoleLighting, FadeDurationMs: &fade}},
		{"lighting with no fade", "", config.NightSessionCue{Name: "c2", Role: config.NightSessionCueRoleLighting}},
		{"audio fade is not a lights fade", "", config.NightSessionCue{Name: "c3", Role: config.NightSessionCueRoleAudio, FadeDurationMs: &fade}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nightCheckBrightnessComposition("enterShow", []config.NightSessionCue{tc.cue})
			if got.health != nightHealthHealthy() || got.reason != tc.reason {
				t.Errorf("got health %v reason %q, want healthy %q", got.health, got.reason, tc.reason)
			}
		})
	}
}

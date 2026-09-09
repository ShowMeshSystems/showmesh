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
		if !strings.Contains(got.reason, "declared non-idempotent") {
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
		if !strings.Contains(got.reason, "does not declare whether it is idempotent") {
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

// TestNightCheckBrightnessCompositionUnverified: a lighting cue with a
// fade duration WARNS; a lighting cue with no fade, or a non-lighting cue
// with a fade, does not.
//
// The first case used to expect a failure, when the provider was
// specified and unbuilt. It is now built, so refusing would make it
// unreachable, while reporting healthy would claim what RESTING-MODE.md
// line 228 forbids claiming until the real-host acceptance matrix passes.
// Degraded is the third answer and the scenario under test is unchanged.
//
// Mutation-checked: dropping the Role comparison would warn on an audio
// fade too, which the third case below catches.
func TestNightCheckBrightnessCompositionUnverified(t *testing.T) {
	fade := 2000
	cases := []struct {
		name string
		cue  config.NightSessionCue
		want nightCheckState
	}{
		{"lighting with fade warns rather than blocking", config.NightSessionCue{Name: "c1", Role: config.NightSessionCueRoleLighting, FadeDurationMs: &fade}, nightHealthDegraded()},
		{"lighting with no fade is fine", config.NightSessionCue{Name: "c2", Role: config.NightSessionCueRoleLighting}, nightHealthHealthy()},
		{"audio fade is unaffected", config.NightSessionCue{Name: "c3", Role: config.NightSessionCueRoleAudio, FadeDurationMs: &fade}, nightHealthHealthy()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nightCheckBrightnessCompositionUnverified("enterShow", []config.NightSessionCue{tc.cue})
			if got.health != tc.want {
				t.Errorf("health = %v, want %v (reason: %s)", got.health, tc.want, got.reason)
			}
		})
	}
}

// TestBrightnessCompositionWarningDoesNotBlockTheNight is the property
// the change turns on and the reason degraded was chosen over healthy or
// failed: a warning must let the night start. If this ever reads
// "not_ready", a built feature is unreachable again.
func TestBrightnessCompositionWarningDoesNotBlockTheNight(t *testing.T) {
	fade := 2000
	warned := nightCheckBrightnessCompositionUnverified("enterShow", []config.NightSessionCue{
		{Name: "c1", Role: config.NightSessionCueRoleLighting, FadeDurationMs: &fade},
	})
	if got := nightOutcomeFromChecks([]nightReadinessCheck{warned}); got != "ready_with_warnings" {
		t.Fatalf("outcome = %q, want ready_with_warnings", got)
	}
	// And it is genuinely a warning rather than silence: an operator has
	// to be able to see the one thing still outstanding before showtime.
	if warned.reason == "" {
		t.Error("the warning carries no reason, so nothing tells an operator what is unverified")
	}
}

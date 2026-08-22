package api

import (
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// TestEvaluateNightInterlockRule_ClosedBehaviorMatrix pins
// RESTING-MODE.md §10.1's closed behavior matrix exactly: every one of
// its named cells (posture x condition/availability) gets its own row.
func TestEvaluateNightInterlockRule_ClosedBehaviorMatrix(t *testing.T) {
	failureText := "enclosure too cold"
	cases := []struct {
		name         string
		rule         config.NightInterlockRule
		cond         nightInterlockCondition
		wantWithhold bool
		wantHealth   nightCheckState
	}{
		{
			name:         "observe/condition-false: report failed, never withhold",
			rule:         config.NightInterlockRule{Posture: config.NightInterlockPostureObserve, FailureText: failureText},
			cond:         nightInterlockConditionFalse,
			wantWithhold: false,
			wantHealth:   nightHealthFailed(),
		},
		{
			name:         "observe/unavailable: report unknown, never withhold",
			rule:         config.NightInterlockRule{Posture: config.NightInterlockPostureObserve, FailureText: failureText},
			cond:         nightInterlockConditionUnavailable,
			wantWithhold: false,
			wantHealth:   nightHealthUnknown(),
		},
		{
			name:         "observe/condition-true: healthy, never withhold",
			rule:         config.NightInterlockRule{Posture: config.NightInterlockPostureObserve, FailureText: failureText},
			cond:         nightInterlockConditionTrue,
			wantWithhold: false,
			wantHealth:   nightHealthHealthy(),
		},
		{
			name: "block+onUnavailable=block / condition-false: withhold",
			rule: config.NightInterlockRule{
				Posture: config.NightInterlockPostureBlock, OnUnavailable: config.NightInterlockOnUnavailableBlock,
				OverridePolicy: config.NightInterlockOverridePolicyNone, FailureText: failureText,
			},
			cond:         nightInterlockConditionFalse,
			wantWithhold: true,
			wantHealth:   nightHealthFailed(),
		},
		{
			name: "block+onUnavailable=block / unavailable: report unknown AND withhold",
			rule: config.NightInterlockRule{
				Posture: config.NightInterlockPostureBlock, OnUnavailable: config.NightInterlockOnUnavailableBlock,
				OverridePolicy: config.NightInterlockOverridePolicyNone, FailureText: failureText,
			},
			cond:         nightInterlockConditionUnavailable,
			wantWithhold: true,
			wantHealth:   nightHealthUnknown(),
		},
		{
			name: "block+onUnavailable=allow / condition-false: withhold only the declared phase",
			rule: config.NightInterlockRule{
				Posture: config.NightInterlockPostureBlock, OnUnavailable: config.NightInterlockOnUnavailableAllow,
				OverridePolicy: config.NightInterlockOverridePolicyNone, FailureText: failureText,
			},
			cond:         nightInterlockConditionFalse,
			wantWithhold: true,
			wantHealth:   nightHealthFailed(),
		},
		{
			name: "block+onUnavailable=allow / unavailable: report unknown AND allow (never withhold)",
			rule: config.NightInterlockRule{
				Posture: config.NightInterlockPostureBlock, OnUnavailable: config.NightInterlockOnUnavailableAllow,
				OverridePolicy: config.NightInterlockOverridePolicyNone, FailureText: failureText,
			},
			cond:         nightInterlockConditionUnavailable,
			wantWithhold: false,
			wantHealth:   nightHealthUnknown(),
		},
		{
			name: "block / condition-true: healthy, never withhold",
			rule: config.NightInterlockRule{
				Posture: config.NightInterlockPostureBlock, OnUnavailable: config.NightInterlockOnUnavailableBlock,
				OverridePolicy: config.NightInterlockOverridePolicyNone, FailureText: failureText,
			},
			cond:         nightInterlockConditionTrue,
			wantWithhold: false,
			wantHealth:   nightHealthHealthy(),
		},
		{
			name:         "disabled: never evaluated, never withholds, regardless of condition",
			rule:         config.NightInterlockRule{Posture: config.NightInterlockPostureDisabled},
			cond:         nightInterlockConditionFalse,
			wantWithhold: false,
			wantHealth:   nightCheckStateNotVerifiable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateNightInterlockRule(tc.rule, tc.cond)
			if got.Withhold != tc.wantWithhold {
				t.Fatalf("Withhold = %v, want %v (decision: %+v)", got.Withhold, tc.wantWithhold, got)
			}
			if got.Health != tc.wantHealth {
				t.Fatalf("Health = %q, want %q (decision: %+v)", got.Health, tc.wantHealth, got)
			}
			if tc.wantHealth == nightHealthFailed() && got.Reason != failureText {
				t.Fatalf("Reason = %q, want the rule's own failureText %q on a failed condition", got.Reason, failureText)
			}
		})
	}
}

func TestClassifyMQTTActionResult(t *testing.T) {
	cases := []struct {
		name string
		res  MQTTActionResult
		want nightInterlockCondition
	}{
		{"confirmed", MQTTActionResult{Outcome: outcomeWordConfirmed}, nightInterlockConditionTrue},
		{"negative answer", MQTTActionResult{Outcome: outcomeWordFailed, OutcomeState: mqttActionStateNegativeAnswer}, nightInterlockConditionFalse},
		{"deadline exceeded", MQTTActionResult{Outcome: outcomeWordUnconfirmed, OutcomeState: mqttActionStateDeadlineExceeded}, nightInterlockConditionUnavailable},
		{"transport error", MQTTActionResult{Outcome: outcomeWordFailed, OutcomeState: mqttActionStateTransportError}, nightInterlockConditionUnavailable},
		{"unknown broker", MQTTActionResult{Outcome: outcomeWordFailed, OutcomeState: mqttActionStateUnknownBroker}, nightInterlockConditionUnavailable},
		{"malformed payload", MQTTActionResult{Outcome: outcomeWordFailed, OutcomeState: mqttActionStateMalformedPayload}, nightInterlockConditionUnavailable},
		{"unconfirmable (kind none, unreachable in practice)", MQTTActionResult{Outcome: outcomeWordUnconfirmable}, nightInterlockConditionUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyMQTTActionResult(tc.res); got != tc.want {
				t.Fatalf("classifyMQTTActionResult(%+v) = %v, want %v", tc.res, got, tc.want)
			}
		})
	}
}

func TestNightConditionFromHealthRoundTrip(t *testing.T) {
	cases := []struct {
		health string
		want   nightInterlockCondition
	}{
		{string(observation.HealthHealthy), nightInterlockConditionTrue},
		{string(observation.HealthFailed), nightInterlockConditionFalse},
		{string(observation.HealthUnknown), nightInterlockConditionUnavailable},
		{string(observation.HealthDegraded), nightInterlockConditionUnavailable},
		{"garbage", nightInterlockConditionUnavailable},
	}
	for _, tc := range cases {
		if got := nightConditionFromHealth(tc.health); got != tc.want {
			t.Fatalf("nightConditionFromHealth(%q) = %v, want %v", tc.health, got, tc.want)
		}
	}
}

// --- nightEvaluatePhaseInterlockGate: override authorization ---

func blockRule(name, phase string) config.NightInterlockRule {
	return config.NightInterlockRule{
		Name: name, Phase: phase, Posture: config.NightInterlockPostureBlock,
		Signal: "s", FailureText: "blocked", OnUnavailable: config.NightInterlockOnUnavailableBlock,
		OverridePolicy: config.NightInterlockOverridePolicyAuthorizedOperator,
	}
}

func failedCheck(rule config.NightInterlockRule) nightReadinessCheck {
	return nightReadinessCheck{name: nightInterlockCheckName(rule), health: nightHealthFailed()}
}

func TestNightEvaluatePhaseInterlockGate_WithholdsWithNoOverride(t *testing.T) {
	rule := blockRule("cooldown", "start-night")
	payload := config.NightSessionPayload{Interlocks: []config.NightInterlockRule{rule}}
	gate := nightEvaluatePhaseInterlockGate(payload, "start-night", []nightReadinessCheck{failedCheck(rule)}, nil, false)
	if len(gate.Withheld) != 1 || len(gate.Overridden) != 0 {
		t.Fatalf("gate = %+v, want exactly one withheld rule and no overrides", gate)
	}
}

func TestNightEvaluatePhaseInterlockGate_OverrideRequiresScope(t *testing.T) {
	rule := blockRule("cooldown", "start-night")
	payload := config.NightSessionPayload{Interlocks: []config.NightInterlockRule{rule}}
	overrides := []nightInterlockOverrideRequest{{Rule: "cooldown", Reason: "operator confirmed safe"}}

	// Requested but caller lacks the scope: still withheld.
	gate := nightEvaluatePhaseInterlockGate(payload, "start-night", []nightReadinessCheck{failedCheck(rule)}, overrides, false)
	if len(gate.Withheld) != 1 || len(gate.Overridden) != 0 {
		t.Fatalf("gate without override scope = %+v, want still withheld", gate)
	}

	// Requested and authorized: overridden, not withheld.
	gate = nightEvaluatePhaseInterlockGate(payload, "start-night", []nightReadinessCheck{failedCheck(rule)}, overrides, true)
	if len(gate.Withheld) != 0 || len(gate.Overridden) != 1 {
		t.Fatalf("gate with override scope = %+v, want overridden, not withheld", gate)
	}
	if gate.Overridden[0].Reason != "operator confirmed safe" {
		t.Fatalf("override reason not carried through: %+v", gate.Overridden[0])
	}
}

// TestNightEvaluatePhaseInterlockGate_OverridePolicyNoneRefusesOverride
// is RESTING-MODE.md §10.1's own rule: "An override is accepted only when
// that rule declares authorized-operator" — a rule that declares
// overridePolicy "none" must stay withheld even with a requesting,
// correctly-scoped caller.
func TestNightEvaluatePhaseInterlockGate_OverridePolicyNoneRefusesOverride(t *testing.T) {
	rule := blockRule("cooldown", "start-night")
	rule.OverridePolicy = config.NightInterlockOverridePolicyNone
	payload := config.NightSessionPayload{Interlocks: []config.NightInterlockRule{rule}}
	overrides := []nightInterlockOverrideRequest{{Rule: "cooldown", Reason: "operator confirmed safe"}}

	gate := nightEvaluatePhaseInterlockGate(payload, "start-night", []nightReadinessCheck{failedCheck(rule)}, overrides, true)
	if len(gate.Withheld) != 1 || len(gate.Overridden) != 0 {
		t.Fatalf("gate with overridePolicy=none = %+v, want still withheld despite the override request", gate)
	}
}

// TestNightEvaluatePhaseInterlockGate_OnlyGatesItsOwnPhase is
// RESTING-MODE.md §10.1's own rule: "Only rules for the phase currently
// being entered can withhold that phase."
func TestNightEvaluatePhaseInterlockGate_OnlyGatesItsOwnPhase(t *testing.T) {
	rule := blockRule("other-phase-rule", "fade-out-night")
	payload := config.NightSessionPayload{Interlocks: []config.NightInterlockRule{rule}}
	gate := nightEvaluatePhaseInterlockGate(payload, "start-night", []nightReadinessCheck{failedCheck(rule)}, nil, false)
	if len(gate.Withheld) != 0 {
		t.Fatalf("a rule declared for a DIFFERENT phase withheld start-night: %+v", gate)
	}
}

// TestNightEvaluatePhaseInterlockGate_MissingCheckIsUnavailable proves a
// rule with no matching stored check (a configuration change since
// readiness ran, or readiness never ran for it) is treated as
// evidence-unavailable rather than silently skipped.
func TestNightEvaluatePhaseInterlockGate_MissingCheckIsUnavailable(t *testing.T) {
	rule := blockRule("cooldown", "start-night")
	payload := config.NightSessionPayload{Interlocks: []config.NightInterlockRule{rule}}
	gate := nightEvaluatePhaseInterlockGate(payload, "start-night", nil, nil, false)
	if len(gate.Withheld) != 1 {
		t.Fatalf("a block rule with no stored check at all did not withhold: %+v", gate)
	}
}

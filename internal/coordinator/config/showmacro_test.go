package config

import (
	"fmt"
	"testing"
)

func alwaysResolves(string) bool { return true }
func neverResolves(string) bool  { return false }

func resolvesOnly(ids ...string) func(string) bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(id string) bool { return set[id] }
}

func validMacroStepJSON(id, action, extra string) string {
	return fmt.Sprintf(`{"id": %q, "action": %q, "localFallback": {"class": "none", "reason": "no fallback exists for this step"}%s}`, id, action, extra)
}

func validMacroJSON(steps string) string {
	return `{"show": "halloween-2026", "label": "Begin set", "steps": [` + steps + `]}`
}

func TestDecodeShowMacroPayloadValid(t *testing.T) {
	raw := validMacroJSON(validMacroStepJSON("projectors", "projectors-on", ""))
	p, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if len(p.Steps) != 1 || p.Steps[0].ID != "projectors" {
		t.Fatalf("unexpected steps: %+v", p.Steps)
	}
}

func TestEncodeShowMacroPayloadRoundTrips(t *testing.T) {
	raw := validMacroJSON(validMacroStepJSON("projectors", "projectors-on", ""))
	p, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	out, err := EncodeShowMacroPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	p2, verr := DecodeShowMacroPayload(out, alwaysResolves)
	if verr != nil {
		t.Fatalf("re-decode: %+v", verr)
	}
	if p2.Steps[0].OnFailure != ShowMacroOnFailureAbort || p2.Steps[0].OnUnconfirmed != ShowMacroOnUnconfirmedContinue {
		t.Fatalf("resolved defaults did not round trip: %+v", p2.Steps[0])
	}
}

// TestDecodeShowMacroPayloadOnFailureDefault is one of the wave2-builder-a.md
// brief's six required break-and-confirm tests.
func TestDecodeShowMacroPayloadOnFailureDefault(t *testing.T) {
	raw := validMacroJSON(validMacroStepJSON("s1", "a1", ""))
	p, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Steps[0].OnFailure != ShowMacroOnFailureAbort {
		t.Fatalf("expected onFailure to default to %q, got %q", ShowMacroOnFailureAbort, p.Steps[0].OnFailure)
	}
}

// TestDecodeShowMacroPayloadOnUnconfirmedDefault is one of the six required
// break-and-confirm tests, and it is the single most consequential
// assertion in this file: ADR-031 decision 2's whole point is that this
// default is NOT the same as onFailure's.
func TestDecodeShowMacroPayloadOnUnconfirmedDefault(t *testing.T) {
	raw := validMacroJSON(validMacroStepJSON("s1", "a1", ""))
	p, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Steps[0].OnUnconfirmed != ShowMacroOnUnconfirmedContinue {
		t.Fatalf("expected onUnconfirmed to default to %q, got %q", ShowMacroOnUnconfirmedContinue, p.Steps[0].OnUnconfirmed)
	}
	if p.Steps[0].OnUnconfirmed == p.Steps[0].OnFailure {
		// Only true when OnFailure's default is also "continue", which it
		// must never be — this assertion fails loudly if a future edit
		// makes the two axes share one default value.
		t.Fatalf("onUnconfirmed and onFailure resolved to the same default (%q) — the two policy axes must have independent defaults", p.Steps[0].OnFailure)
	}
}

func TestDecodeShowMacroPayloadOnFailureValues(t *testing.T) {
	mk := func(extra string) string {
		return validMacroJSON(validMacroStepJSON("s1", "a1", extra))
	}
	t.Run("explicit-abort", func(t *testing.T) {
		p, verr := DecodeShowMacroPayload(mk(`, "onFailure": "abort"`), alwaysResolves)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Steps[0].OnFailure != ShowMacroOnFailureAbort {
			t.Fatalf("unexpected onFailure: %q", p.Steps[0].OnFailure)
		}
	})
	t.Run("explicit-continue", func(t *testing.T) {
		p, verr := DecodeShowMacroPayload(mk(`, "onFailure": "continue"`), alwaysResolves)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Steps[0].OnFailure != ShowMacroOnFailureContinue {
			t.Fatalf("unexpected onFailure: %q", p.Steps[0].OnFailure)
		}
	})
	t.Run("invalid-value", func(t *testing.T) {
		_, verr := DecodeShowMacroPayload(mk(`, "onFailure": "retry"`), alwaysResolves)
		if verr == nil || verr.Code != ValidationCodeFieldInvalid {
			t.Fatalf("expected field-invalid error, got %+v", verr)
		}
	})
}

// TestDecodeShowMacroPayloadOnFailureAbsentVsNullVsEmpty is one of the six
// required break-and-confirm tests: absent, null, and empty must be three
// different outcomes, not two.
func TestDecodeShowMacroPayloadOnFailureAbsentVsNullVsEmpty(t *testing.T) {
	mk := func(extra string) string {
		return validMacroJSON(validMacroStepJSON("s1", "a1", extra))
	}
	t.Run("absent-gives-default", func(t *testing.T) {
		p, verr := DecodeShowMacroPayload(mk(""), alwaysResolves)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Steps[0].OnFailure != ShowMacroOnFailureAbort {
			t.Fatalf("expected default %q, got %q", ShowMacroOnFailureAbort, p.Steps[0].OnFailure)
		}
	})
	t.Run("null-errors", func(t *testing.T) {
		_, verr := DecodeShowMacroPayload(mk(`, "onFailure": null`), alwaysResolves)
		if verr == nil || verr.Code != ValidationCodeFieldNull {
			t.Fatalf("expected field-null error, got %+v", verr)
		}
	})
	t.Run("empty-string-errors", func(t *testing.T) {
		_, verr := DecodeShowMacroPayload(mk(`, "onFailure": ""`), alwaysResolves)
		if verr == nil || verr.Code != ValidationCodeFieldEmpty {
			t.Fatalf("expected field-empty error, got %+v", verr)
		}
	})
}

func TestDecodeShowMacroPayloadOnUnconfirmedAbsentVsNullVsEmpty(t *testing.T) {
	mk := func(extra string) string {
		return validMacroJSON(validMacroStepJSON("s1", "a1", extra))
	}
	t.Run("absent-gives-default", func(t *testing.T) {
		p, verr := DecodeShowMacroPayload(mk(""), alwaysResolves)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Steps[0].OnUnconfirmed != ShowMacroOnUnconfirmedContinue {
			t.Fatalf("expected default %q, got %q", ShowMacroOnUnconfirmedContinue, p.Steps[0].OnUnconfirmed)
		}
	})
	t.Run("null-errors", func(t *testing.T) {
		_, verr := DecodeShowMacroPayload(mk(`, "onUnconfirmed": null`), alwaysResolves)
		if verr == nil || verr.Code != ValidationCodeFieldNull {
			t.Fatalf("expected field-null error, got %+v", verr)
		}
	})
	t.Run("empty-string-errors", func(t *testing.T) {
		_, verr := DecodeShowMacroPayload(mk(`, "onUnconfirmed": ""`), alwaysResolves)
		if verr == nil || verr.Code != ValidationCodeFieldEmpty {
			t.Fatalf("expected field-empty error, got %+v", verr)
		}
	})
}

func TestDecodeShowMacroPayloadStepsEmpty(t *testing.T) {
	raw := validMacroJSON("")
	_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr == nil || verr.Code != ValidationCodeStepsEmpty {
		t.Fatalf("expected steps-empty error, got %+v", verr)
	}
}

func TestDecodeShowMacroPayloadStepsTooMany(t *testing.T) {
	var stepsJSON string
	for i := 0; i < ShowMacroMaxSteps+1; i++ {
		if i > 0 {
			stepsJSON += ","
		}
		stepsJSON += validMacroStepJSON(fmt.Sprintf("s%d", i), "a", "")
	}
	raw := validMacroJSON(stepsJSON)
	_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr == nil || verr.Code != ValidationCodeStepsTooMany {
		t.Fatalf("expected steps-too-many error, got %+v", verr)
	}
}

func TestDecodeShowMacroPayloadStepsAtCapAccepted(t *testing.T) {
	var stepsJSON string
	for i := 0; i < ShowMacroMaxSteps; i++ {
		if i > 0 {
			stepsJSON += ","
		}
		stepsJSON += validMacroStepJSON(fmt.Sprintf("s%d", i), "a", "")
	}
	raw := validMacroJSON(stepsJSON)
	p, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr != nil {
		t.Fatalf("unexpected error at the cap: %+v", verr)
	}
	if len(p.Steps) != ShowMacroMaxSteps {
		t.Fatalf("expected %d steps, got %d", ShowMacroMaxSteps, len(p.Steps))
	}
}

func TestDecodeShowMacroPayloadStepIDDuplicate(t *testing.T) {
	raw := validMacroJSON(validMacroStepJSON("dup", "a1", "") + "," + validMacroStepJSON("dup", "a2", ""))
	_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr == nil || verr.Code != ValidationCodeStepIDDuplicate {
		t.Fatalf("expected step-id-duplicate error, got %+v", verr)
	}
}

func TestDecodeShowMacroPayloadStepIDRequired(t *testing.T) {
	raw := validMacroJSON(`{"action": "a1", "localFallback": {"class": "none", "reason": "r"}}`)
	_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr == nil || verr.Field != "steps[0].id" {
		t.Fatalf("expected steps[0].id-required error, got %+v", verr)
	}
}

func TestDecodeShowMacroPayloadStepActionRequired(t *testing.T) {
	raw := validMacroJSON(`{"id": "s1", "localFallback": {"class": "none", "reason": "r"}}`)
	_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr == nil || verr.Field != "steps[0].action" {
		t.Fatalf("expected steps[0].action-required error, got %+v", verr)
	}
}

func TestDecodeShowMacroPayloadStepActionMustResolve(t *testing.T) {
	raw := validMacroJSON(validMacroStepJSON("s1", "nonexistent-action", ""))
	_, verr := DecodeShowMacroPayload(raw, neverResolves)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "steps[0].action" {
		t.Fatalf("expected step action unknown-reference error, got %+v", verr)
	}
}

func TestDecodeShowMacroPayloadStepActionResolvesSelectively(t *testing.T) {
	resolver := resolvesOnly("known-action")
	raw := validMacroJSON(validMacroStepJSON("s1", "known-action", ""))
	_, verr := DecodeShowMacroPayload(raw, resolver)
	if verr != nil {
		t.Fatalf("unexpected error for a known action: %+v", verr)
	}
	raw = validMacroJSON(validMacroStepJSON("s1", "unknown-action", ""))
	_, verr = DecodeShowMacroPayload(raw, resolver)
	if verr == nil {
		t.Fatal("expected an error for an action the resolver does not know")
	}
}

// TestDecodeShowMacroPayloadLocalFallbackRequired is required per STEP-9-
// SPEC.md section 5.4: "a step with no localFallback is rejected"
// (acceptance criterion 10).
func TestDecodeShowMacroPayloadLocalFallbackRequired(t *testing.T) {
	raw := validMacroJSON(`{"id": "s1", "action": "a1"}`)
	_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr == nil || verr.Field != "steps[0].localFallback" {
		t.Fatalf("expected localFallback-required error, got %+v", verr)
	}
}

func TestDecodeShowMacroPayloadLocalFallbackClasses(t *testing.T) {
	mk := func(class string) string {
		return validMacroJSON(fmt.Sprintf(`{"id": "s1", "action": "a1", "localFallback": {"class": %q, "reason": "because"}}`, class))
	}
	t.Run("none-accepted", func(t *testing.T) {
		_, verr := DecodeShowMacroPayload(mk("none"), alwaysResolves)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
	})
	t.Run("coordinator-required-accepted", func(t *testing.T) {
		_, verr := DecodeShowMacroPayload(mk("coordinator-required"), alwaysResolves)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
	})
	t.Run("silence-accepted", func(t *testing.T) {
		_, verr := DecodeShowMacroPayload(mk("silence"), alwaysResolves)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
	})
	// TestDecodeShowMacroPayloadLocalFallbackReducedRejected below is the
	// dedicated required break-and-confirm test for "reduced"; this table
	// also covers an arbitrary unrecognized class for completeness.
	t.Run("unrecognized-rejected", func(t *testing.T) {
		_, verr := DecodeShowMacroPayload(mk("teleport"), alwaysResolves)
		if verr == nil || verr.Code != ValidationCodeFieldInvalid {
			t.Fatalf("expected field-invalid error, got %+v", verr)
		}
	})
}

// TestDecodeShowMacroPayloadLocalFallbackReducedRejected is one of the six
// required break-and-confirm tests: STEP-9-SPEC.md section 5.4 and
// acceptance criterion 10 require "reduced" to be rejected with its own
// Code, distinct from an ordinary bad enum value.
func TestDecodeShowMacroPayloadLocalFallbackReducedRejected(t *testing.T) {
	raw := validMacroJSON(`{"id": "s1", "action": "a1", "localFallback": {"class": "reduced", "reason": "because"}}`)
	_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr == nil {
		t.Fatal("expected localFallback class \"reduced\" to be rejected")
	}
	if verr.Code != ValidationCodeLocalFallbackReduced {
		t.Fatalf("expected code %q, got %q (%+v)", ValidationCodeLocalFallbackReduced, verr.Code, verr)
	}
}

func TestDecodeShowMacroPayloadLocalFallbackReasonRequiredOnEveryClass(t *testing.T) {
	for _, class := range []string{"none", "coordinator-required", "silence"} {
		t.Run(class, func(t *testing.T) {
			raw := validMacroJSON(fmt.Sprintf(`{"id": "s1", "action": "a1", "localFallback": {"class": %q}}`, class))
			_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
			if verr == nil || verr.Field != "steps[0].localFallback.reason" {
				t.Fatalf("expected reason-required error for class %q, got %+v", class, verr)
			}
		})
	}
}

func TestDecodeShowMacroPayloadShowAndLabelRequired(t *testing.T) {
	t.Run("show-required", func(t *testing.T) {
		raw := `{"label": "x", "steps": [` + validMacroStepJSON("s1", "a1", "") + `]}`
		_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
		if verr == nil || verr.Field != "show" {
			t.Fatalf("expected show-required error, got %+v", verr)
		}
	})
	t.Run("label-required", func(t *testing.T) {
		raw := `{"show": "halloween-2026", "steps": [` + validMacroStepJSON("s1", "a1", "") + `]}`
		_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
		if verr == nil || verr.Field != "label" {
			t.Fatalf("expected label-required error, got %+v", verr)
		}
	})
}

func TestDecodeShowMacroPayloadStepsNullRejected(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "steps": null}`
	_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "steps" {
		t.Fatalf("expected steps-null error, got %+v", verr)
	}
}

// TestDecodeShowMacroPayloadUnknownTopLevelKeyRejected is the defect a
// second review found: before rejectUnknownTopLevelKeys existed, a typo of
// a real key — most dangerously one with a default, like "onFailur" one
// level down inside a step — was silently ignored rather than reported,
// so the coordinator stored a DIFFERENT policy than the operator typed
// with no error at all. This proves the top-level sweep specifically (a
// typo of "steps" itself, "step" singular), which decodeTopLevelObject's
// own new caller runs before any per-field decode.
func TestDecodeShowMacroPayloadUnknownTopLevelKeyRejected(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "step": [` + validMacroStepJSON("s1", "a1", "") + `]}`
	_, verr := DecodeShowMacroPayload(raw, alwaysResolves)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key error for typo'd \"step\", got %+v", verr)
	}
}

package config

import (
	"encoding/json"
	"fmt"
)

// See showaction.go's own top doc comment for this file's shared context
// (why show.macro is hand-written rather than built on a generic kind
// registry, and why every referenced id or identifier is a caller-supplied
// parameter rather than something this package fetches).

// ShowMacroMaxSteps is STEP-9-SPEC.md section 5.4's cap on steps, "so §9's
// follow mode has a finite worst case."
const ShowMacroMaxSteps = 32

// The two values of show.macro.steps[].onFailure, and its default.
// ADR-031 decision 2 / STEP-9-SPEC.md section 2.2: a failed step stops the
// run unless the step says otherwise.
const (
	ShowMacroOnFailureAbort    = "abort"
	ShowMacroOnFailureContinue = "continue"
	ShowMacroOnFailureDefault  = ShowMacroOnFailureAbort
)

var showMacroOnFailureValues = map[string]bool{
	ShowMacroOnFailureAbort:    true,
	ShowMacroOnFailureContinue: true,
}

// The two values of show.macro.steps[].onUnconfirmed, and its default.
// ADR-031 decision 2 / STEP-9-SPEC.md section 2.2: a monitoring gap must
// never stop a show, so this default is the OPPOSITE polarity from
// onFailure's — continue, not abort. This is the specific defect ADR-031
// decision 2's rewrite exists to prevent: collapsing these two into one
// field, or one default, silently reintroduces "an unconfirmed step aborts
// the run", which is the rejected alternative ADR-031 records by name.
const (
	ShowMacroOnUnconfirmedContinue = "continue"
	ShowMacroOnUnconfirmedAbort    = "abort"
	ShowMacroOnUnconfirmedDefault  = ShowMacroOnUnconfirmedContinue
)

var showMacroOnUnconfirmedValues = map[string]bool{
	ShowMacroOnUnconfirmedContinue: true,
	ShowMacroOnUnconfirmedAbort:    true,
}

// The three ACCEPTED values of show.macro.steps[].localFallback.class
// (STEP-9-SPEC.md section 5.4, ADR-016, ADR-019). There is deliberately no
// default: an unlabelled step is rejected at write time, so
// showMacroLocalFallbackClasses carries no "ShowMacroLocalFallbackDefault"
// the way the two policy axes above do.
const (
	ShowMacroLocalFallbackNone                = "none"
	ShowMacroLocalFallbackCoordinatorRequired = "coordinator-required"
	ShowMacroLocalFallbackSilence             = "silence"

	// showMacroLocalFallbackReduced is NOT a member of
	// showMacroLocalFallbackClasses. It exists only so decodeLocalFallback
	// can name it explicitly and answer with
	// ValidationCodeLocalFallbackReduced rather than the generic
	// ValidationCodeFieldInvalid every other unrecognized class value gets
	// — STEP-9-SPEC.md section 5.4: "reduced is not an accepted value and
	// must be rejected with a message saying no delivery path exists ...
	// Its own Code."
	showMacroLocalFallbackReduced = "reduced"
)

var showMacroLocalFallbackClasses = map[string]bool{
	ShowMacroLocalFallbackNone:                true,
	ShowMacroLocalFallbackCoordinatorRequired: true,
	ShowMacroLocalFallbackSilence:             true,
}

// ShowMacroPayload is config_revisions.payload_json's decoded, VALIDATED
// shape for [ShowMacroConfigKind].
type ShowMacroPayload struct {
	Show        string          `json:"show"`
	Label       string          `json:"label"`
	Description string          `json:"description,omitempty"`
	Steps       []ShowMacroStep `json:"steps"`
}

// ShowMacroStep is one entry of show.macro.steps. OnFailure and
// OnUnconfirmed are always populated with the resolved value (default or
// explicit), never left blank to stand in for "the default applies" — a
// stored revision states its own policy outright rather than requiring a
// reader to know the default rule to interpret an absent field (the same
// "absent evidence is stated, never omitted" instinct this project already
// applies to observations, applied here to configuration).
type ShowMacroStep struct {
	ID            string                 `json:"id"`
	Action        string                 `json:"action"`
	OnFailure     string                 `json:"onFailure"`
	OnUnconfirmed string                 `json:"onUnconfirmed"`
	LocalFallback ShowMacroLocalFallback `json:"localFallback"`
}

// ShowMacroLocalFallback is one step's required localFallback object.
type ShowMacroLocalFallback struct {
	Class  string `json:"class"`
	Reason string `json:"reason"`
}

// EncodeShowMacroPayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeShowMacroPayload); this function does not re-validate.
func EncodeShowMacroPayload(p ShowMacroPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.macro payload: %w", err)
	}
	return string(b), nil
}

// DecodeShowMacroPayload parses and validates raw against STEP-9-SPEC.md
// section 5.4's rules. resolveAction reports whether a step's action id
// names an existing show.action object — a caller-supplied lookup, per this
// file's own top note, never something this package fetches itself.
func DecodeShowMacroPayload(raw string, resolveAction func(actionID string) bool) (ShowMacroPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return ShowMacroPayload{}, verr
	}

	show, verr := decodeRequiredString(top, "show", "show")
	if verr != nil {
		return ShowMacroPayload{}, verr
	}
	if verr := validateShowRef(show); verr != nil {
		return ShowMacroPayload{}, verr
	}

	label, verr := decodeRequiredString(top, "label", "label")
	if verr != nil {
		return ShowMacroPayload{}, verr
	}

	description, verr := decodeOptionalString(top, "description", "description")
	if verr != nil {
		return ShowMacroPayload{}, verr
	}

	stepsRaw, present := top["steps"]
	if !present {
		return ShowMacroPayload{}, &ValidationError{Code: ValidationCodeFieldRequired, Field: "steps", Detail: "steps is required"}
	}
	if isJSONNull(stepsRaw) {
		return ShowMacroPayload{}, &ValidationError{Code: ValidationCodeFieldNull, Field: "steps", Detail: "steps must not be null"}
	}
	var rawSteps []json.RawMessage
	if err := json.Unmarshal(stepsRaw, &rawSteps); err != nil {
		return ShowMacroPayload{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "steps", Detail: "steps must be a JSON array"}
	}
	if len(rawSteps) == 0 {
		return ShowMacroPayload{}, &ValidationError{Code: ValidationCodeStepsEmpty, Field: "steps", Detail: "steps must contain at least one step"}
	}
	if len(rawSteps) > ShowMacroMaxSteps {
		return ShowMacroPayload{}, &ValidationError{
			Code: ValidationCodeStepsTooMany, Field: "steps",
			Detail: fmt.Sprintf("steps must contain no more than %d entries", ShowMacroMaxSteps),
		}
	}

	steps := make([]ShowMacroStep, 0, len(rawSteps))
	seenIDs := make(map[string]bool, len(rawSteps))
	for i, rawStep := range rawSteps {
		field := fmt.Sprintf("steps[%d]", i)
		step, verr := decodeShowMacroStep(rawStep, field, resolveAction)
		if verr != nil {
			return ShowMacroPayload{}, verr
		}
		if seenIDs[step.ID] {
			return ShowMacroPayload{}, &ValidationError{
				Code: ValidationCodeStepIDDuplicate, Field: field + ".id",
				Detail: fmt.Sprintf("step id %q is used more than once", step.ID),
			}
		}
		seenIDs[step.ID] = true
		steps = append(steps, step)
	}

	return ShowMacroPayload{Show: show, Label: label, Description: description, Steps: steps}, nil
}

func decodeShowMacroStep(raw json.RawMessage, field string, resolveAction func(string) bool) (ShowMacroStep, *ValidationError) {
	if isJSONNull(raw) {
		return ShowMacroStep{}, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ShowMacroStep{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON object", field)}
	}

	id, verr := decodeRequiredString(fields, "id", field+".id")
	if verr != nil {
		return ShowMacroStep{}, verr
	}

	action, verr := decodeRequiredString(fields, "action", field+".action")
	if verr != nil {
		return ShowMacroStep{}, verr
	}
	if !resolveAction(action) {
		return ShowMacroStep{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: field + ".action",
			Detail: fmt.Sprintf("action %q does not resolve to an existing show.action object", action),
		}
	}

	onFailure, verr := decodeDefaultedEnum(fields, "onFailure", field+".onFailure", ShowMacroOnFailureDefault, showMacroOnFailureValues)
	if verr != nil {
		return ShowMacroStep{}, verr
	}

	onUnconfirmed, verr := decodeDefaultedEnum(fields, "onUnconfirmed", field+".onUnconfirmed", ShowMacroOnUnconfirmedDefault, showMacroOnUnconfirmedValues)
	if verr != nil {
		return ShowMacroStep{}, verr
	}

	fallbackFields, verr := decodeRequiredObject(fields, "localFallback", field+".localFallback")
	if verr != nil {
		return ShowMacroStep{}, verr
	}
	fallback, verr := decodeLocalFallback(fallbackFields, field+".localFallback")
	if verr != nil {
		return ShowMacroStep{}, verr
	}

	return ShowMacroStep{ID: id, Action: action, OnFailure: onFailure, OnUnconfirmed: onUnconfirmed, LocalFallback: fallback}, nil
}

func decodeLocalFallback(fields map[string]json.RawMessage, field string) (ShowMacroLocalFallback, *ValidationError) {
	class, verr := decodeRequiredString(fields, "class", field+".class")
	if verr != nil {
		return ShowMacroLocalFallback{}, verr
	}

	if class == showMacroLocalFallbackReduced {
		return ShowMacroLocalFallback{}, &ValidationError{
			Code: ValidationCodeLocalFallbackReduced, Field: field + ".class",
			Detail: "localFallback class \"reduced\" is not accepted: no delivery path exists to a node for it yet",
		}
	}
	if !showMacroLocalFallbackClasses[class] {
		return ShowMacroLocalFallback{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: field + ".class",
			Detail: "class must be one of none, coordinator-required, or silence",
		}
	}

	// reason is required and non-empty on EVERY class, including "none" —
	// STEP-9-SPEC.md section 5.4: "reason is required and must be
	// non-empty on every class, including none."
	reason, verr := decodeRequiredString(fields, "reason", field+".reason")
	if verr != nil {
		return ShowMacroLocalFallback{}, verr
	}

	return ShowMacroLocalFallback{Class: class, Reason: reason}, nil
}

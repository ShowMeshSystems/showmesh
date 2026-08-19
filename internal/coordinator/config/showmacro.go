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
//
// DEFAULT REVERSED 2026-08-14, OWNER DECISION, superseding ADR-031
// decision 2 / STEP-9-SPEC.md section 2.2's "a failed step stops the run
// unless the step says otherwise". The owner's rule: "the run needs to
// always RUN all steps, no matter what. If something doesn't confirm, or
// we can't record it, that doesn't matter. it should still send the
// command. we cannot risk the show because a logging or audit system is
// down, that's not how show critical infrastructure works."
//
// The abort default was never an owner decision; it was written into the
// specification and inherited from there. What it bought was a run that
// stopped early so an operator could see one clean failure instead of a
// cascade. What it cost is that ONE failed step suppressed every later
// step, including the blackout at the end of the sequence, which is the
// opposite of what a show wants from a control system.
//
// "abort" survives as an EXPLICIT per-step choice, because an operator
// writing it into a macro is making that call themselves, which is a
// different thing from ShowMesh making it for them by default.
const (
	ShowMacroOnFailureAbort    = "abort"
	ShowMacroOnFailureContinue = "continue"
	ShowMacroOnFailureDefault  = ShowMacroOnFailureContinue
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

// showMacroTopLevelKeys is the complete set of keys
// DecodeShowMacroPayload recognizes at the top level of the request body
// — see rejectUnknownTopLevelKeys (showaction.go).
var showMacroTopLevelKeys = map[string]bool{
	"show": true, "label": true, "description": true, "steps": true,
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
// names an existing show.action object and, when it does, that action's
// own target.integration and its own "show" — a caller-supplied lookup,
// per this file's own top note, never something this package fetches
// itself. The integration is needed because a step whose action is a
// Resolume action must declare localFallback.class ==
// "coordinator-required": a controlled device (ADR-016) holds no fallback
// of its own. The action's show is needed because a macro may only step
// through actions in its own show namespace (ADR-027). showExists reports
// whether the macro's own "show" names an existing show config object.
func DecodeShowMacroPayload(raw string, resolveAction func(actionID string) (integration, show string, ok bool), showExists func(string) bool) (ShowMacroPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return ShowMacroPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, showMacroTopLevelKeys); verr != nil {
		return ShowMacroPayload{}, verr
	}

	show, verr := decodeRequiredString(top, "show", "show")
	if verr != nil {
		return ShowMacroPayload{}, verr
	}
	if verr := validateShowRef(show); verr != nil {
		return ShowMacroPayload{}, verr
	}
	if !showExists(show) {
		return ShowMacroPayload{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "show",
			Detail: fmt.Sprintf("show %q is not a configured show; create it first", show),
		}
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
		step, verr := decodeShowMacroStep(rawStep, field, show, resolveAction)
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

func decodeShowMacroStep(raw json.RawMessage, field string, macroShow string, resolveAction func(string) (string, string, bool)) (ShowMacroStep, *ValidationError) {
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
	integration, actionShow, ok := resolveAction(action)
	if !ok {
		return ShowMacroStep{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: field + ".action",
			Detail: fmt.Sprintf("action %q does not resolve to an existing show.action object", action),
		}
	}
	if actionShow != macroShow {
		return ShowMacroStep{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: field + ".action",
			Detail: fmt.Sprintf("action %q belongs to show %q, not this macro's show %q", action, actionShow, macroShow),
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

	// Every Resolume action is coordinator-required (ADR-016): a
	// controlled device holds no fallback, so a step naming one must
	// declare exactly that class.
	if integration == ShowActionIntegrationResolume && fallback.Class != ShowMacroLocalFallbackCoordinatorRequired {
		return ShowMacroStep{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: field + ".localFallback.class",
			Detail: fmt.Sprintf(
				"%s.localFallback.class must be %q for a step whose action is a Resolume action: a controlled device holds no fallback, so there is nothing to run locally when the coordinator is gone",
				field, ShowMacroLocalFallbackCoordinatorRequired),
		}
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

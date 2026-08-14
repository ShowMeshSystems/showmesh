package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file and showmacro.go are Step 9 wave 2 Builder A's own addition
// (STEP-9-SPEC.md section 5, ADR-027 decision 2): the config_objects.kind /
// config_revisions.payload_json shape for the two new configuration kinds,
// "show.action" and "show.macro". STEP-9-SPEC.md section 3 is explicit that
// there is no generic kind registry and that fpp.endpoints (fppendpoints.go)
// is hand-written end to end; this file follows the identical pattern
// rather than inventing an abstraction two kinds does not justify.
//
// Unlike fpp.endpoints, both kinds here need write-time validation that
// depends on state this package does not own (the configured FPP endpoint
// list, the declared MQTT integration brokers, the Step 8 FPP primitive
// registry, and — for show.macro — the set of existing show.action object
// ids). Every one of those is taken as a parameter to the Decode functions
// below rather than fetched, per the wave 2 shared contract section 3 and
// STEP-9-SPEC.md sections 5.3/5.4/5.6: this package has no store access and
// must not import internal/coordinator/api (that import direction is
// forced the other way — see FPPPrimitiveRegistry's own doc comment).

const (
	// ShowActionConfigKind is config_objects.kind and config_revisions.kind
	// for a show.action object, and the second path segment of
	// GET/PUT /api/v1/config/show.action/{id} (Builder C's route, not built
	// here). Unlike FPPEndpointsConfigObjectID, show.action is a
	// collection: each object id is the action's own identifier, chosen by
	// the caller, not a fixed singleton.
	ShowActionConfigKind = "show.action"

	// ShowMacroConfigKind is show.macro's own equivalent of
	// ShowActionConfigKind. See showmacro.go.
	ShowMacroConfigKind = "show.macro"
)

// --- The api-side dependency this package needs but must not import. ---

// FPPPrimitiveRegistry is the eight registered Step 8 primitives as this
// package needs them when validating an authored show.action, declared here
// (rather than in internal/coordinator/api) because the import direction
// between config and api is forced: internal/coordinator/macro imports
// internal/coordinator/api (wave 2 shared contract section 1), so api must
// never import macro, and api/showaction_registry.go implements this
// interface over its own unexported registry rather than this package
// importing api and creating the cycle.
type FPPPrimitiveRegistry interface {
	// DecodeActionParams validates and normalizes an authored action's
	// params against the named primitive, applying the same absent /
	// explicit-null / unknown-key rules the HTTP command endpoint applies
	// (internal/coordinator/api's decodeFPPCommandParams, plus that
	// primitive's own ValidateParams — STEP-9-SPEC.md section 5.3: "an
	// action authored with a bad playlist type fails at write time rather
	// than at 17:00"). raw is the decoded "target" object of a show.action
	// payload (a map keyed by JSON field name, e.g. "instanceId",
	// "primitive", "params"), mirroring decodeFPPCommandParams's own "top"
	// parameter shape exactly so the adapter can pass it through unchanged
	// rather than re-wrapping it.
	DecodeActionParams(wireAction string, raw map[string]json.RawMessage) (map[string]any, error)

	// Decision11Class is the primitive's own registered safety class, in
	// the show.action safetyClass vocabulary ("none" | "blackout" |
	// "stop" | "powerOff"). ok is false for an unregistered action.
	Decision11Class(wireAction string) (class string, ok bool)

	// WireActions is the registered vocabulary, for an error naming what
	// is supported.
	WireActions() []string
}

// --- Validation errors: machine-readable, per the wave 2 shared contract
// section 4 ("a client that must tell two refusals apart may never branch
// on prose"). ---

// ValidationError is the typed error every Decode function in this file and
// showmacro.go returns on a rejected write. Code is a small, closed,
// exported set (below) so Builder C can map it onto a problem type URI
// without reading Detail's prose; Field names where in the payload the
// problem is, using a dotted/indexed path ("target.publish.qos",
// "steps[2].action"); Detail is the operator-facing sentence — and, per the
// wave 2 shared contract section 4 and CLAUDE.md's own standing rule, it
// carries no repo path, no .md reference, no ADR number, and no section
// citation. The reasoning for why a rule exists lives in this file's Go
// doc comments, never in Detail.
type ValidationError struct {
	Code   string
	Field  string
	Detail string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Detail)
}

// The closed set of Codes this file and showmacro.go produce.
//
// Most structural problems (a field absent, present as an explicit JSON
// null, present as an empty string where that is not a valid choice, or of
// the wrong JSON type / not a member of a closed enum) share one of the
// four generic codes below and are told apart by Field, which always names
// the exact dotted path. A handful of business rules get their OWN code
// rather than folding into ValidationCodeFieldInvalid, because the wave 2
// shared contract and STEP-9-SPEC.md name them explicitly as needing to be
// distinguishable from an ordinary bad value:
//
//   - ValidationCodeSafetyClassMismatch: an FPP action's declared
//     safetyClass disagrees with its primitive's own registered class
//     (STEP-9-SPEC.md section 5.3 — "This is a distinct Code, not folded
//     into a generic bad-value error").
//   - ValidationCodeLocalFallbackReduced: a macro step declared
//     localFallback.class "reduced" (STEP-9-SPEC.md section 5.4 —
//     "reduced is not an accepted value ... Its own Code").
//   - ValidationCodeStepsEmpty / ValidationCodeStepsTooMany /
//     ValidationCodeStepIDDuplicate: showmacro.go's own structural rules
//     that are not "one field, one problem" (they are about the shape of
//     the whole steps array), so a shared field-level code would not let a
//     client distinguish "too many steps" from "a bad value inside one
//     step" without also parsing Field.
const (
	// ValidationCodeBodyInvalid means the payload was not a JSON object at
	// all (Field is empty).
	ValidationCodeBodyInvalid = "body-invalid"

	// ValidationCodeFieldRequired means Field was absent.
	ValidationCodeFieldRequired = "field-required"

	// ValidationCodeFieldNull means Field was present as an explicit JSON
	// null — always an error in this step's payloads: absent, null, and
	// explicitly empty are three different things (CLAUDE.md, restated for
	// every write surface this step adds), and no field in show.action or
	// show.macro treats a present null as "use the default".
	ValidationCodeFieldNull = "field-null"

	// ValidationCodeFieldEmpty means Field was present as an empty string
	// where an empty string is not a valid choice for that field (a
	// required label, id, or action reference).
	ValidationCodeFieldEmpty = "field-empty"

	// ValidationCodeFieldInvalid means Field was present and non-null but
	// failed some other check: wrong JSON type, not a member of a closed
	// enum, out of range, or a nested object's own shape rule.
	ValidationCodeFieldInvalid = "field-invalid"

	// ValidationCodeFieldUnknownReference means Field named something that
	// must resolve against caller-supplied state and did not: an
	// unconfigured FPP instanceId, an undeclared MQTT broker identifier, or
	// (show.macro) a step.action that is not an existing show.action
	// object id (STEP-9-SPEC.md section 5.6).
	ValidationCodeFieldUnknownReference = "field-unknown-reference"

	// ValidationCodeSafetyClassMismatch: see this const block's own doc
	// comment above.
	ValidationCodeSafetyClassMismatch = "safety-class-mismatch"

	// ValidationCodeLocalFallbackReduced: see this const block's own doc
	// comment above. Defined here rather than in showmacro.go because every
	// other Code in the closed set lives in this one place.
	ValidationCodeLocalFallbackReduced = "local-fallback-reduced"

	// ValidationCodeStepsEmpty, ValidationCodeStepsTooMany,
	// ValidationCodeStepIDDuplicate: see this const block's own doc comment
	// above. showmacro.go is the only user.
	ValidationCodeStepsEmpty      = "steps-empty"
	ValidationCodeStepsTooMany    = "steps-too-many"
	ValidationCodeStepIDDuplicate = "step-id-duplicate"
)

// --- show.action's own vocabulary. ---

// The four members of show.action.safetyClass, matching ADR-024 decision
// 11's own named list exactly and adding no members (STEP-9-SPEC.md section
// 5.3, ADR-031 decision 5).
const (
	ShowSafetyClassNone     = "none"
	ShowSafetyClassBlackout = "blackout"
	ShowSafetyClassStop     = "stop"
	ShowSafetyClassPowerOff = "powerOff"
)

var showSafetyClasses = map[string]bool{
	ShowSafetyClassNone:     true,
	ShowSafetyClassBlackout: true,
	ShowSafetyClassStop:     true,
	ShowSafetyClassPowerOff: true,
}

// The two members of show.action.target.integration this step supports
// (STEP-9-SPEC.md section 5.3).
const (
	ShowActionIntegrationFPP  = "fpp"
	ShowActionIntegrationMQTT = "mqtt"
)

// The five members of show.action.target.expect.kind (STEP-9-SPEC.md
// section 7.3).
const (
	MQTTExpectKindNone    = "none"
	MQTTExpectKindBoolean = "boolean"
	MQTTExpectKindNumber  = "number"
	MQTTExpectKindText    = "text"
	MQTTExpectKindMatch   = "match"
)

var mqttExpectKinds = map[string]bool{
	MQTTExpectKindNone:    true,
	MQTTExpectKindBoolean: true,
	MQTTExpectKindNumber:  true,
	MQTTExpectKindText:    true,
	MQTTExpectKindMatch:   true,
}

// mqttExpectMaxDeadlineSeconds is STEP-9-SPEC.md section 7.3's 120-second
// cap on target.expect.deadlineSeconds, and internal/coordinator/broker's
// MaxResponseDeadline (broker/response.go) is the SAME 120 seconds by
// design — the wave 2 shared contract section 3 asks this package to
// import that constant rather than duplicate the literal, "unless that
// import is unacceptable from config, in which case say so in your report
// and put a test that asserts the two numbers agree."
//
// That import is unacceptable: internal/coordinator/broker already imports
// internal/coordinator/config (broker.go's own import block, for
// config.Config), so config importing broker back would be an import
// cycle. The literal is therefore repeated here, and
// TestMQTTExpectMaxDeadlineSecondsAgreesWithBrokerMaxResponseDeadline (an
// external config_test package test, which CAN import both without a
// cycle) asserts the two stay equal.
const mqttExpectMaxDeadlineSeconds = 120

// --- show.action payload shape. ---

// ShowActionPayload is config_revisions.payload_json's decoded, VALIDATED
// shape for [ShowActionConfigKind]. Every value here has already passed
// DecodeShowActionPayload's rules; nothing downstream needs to re-check
// absent/null/empty, an unresolved reference, or a safety-class mismatch.
type ShowActionPayload struct {
	Show        string           `json:"show"`
	Label       string           `json:"label"`
	Description string           `json:"description,omitempty"`
	SafetyClass string           `json:"safetyClass"`
	Target      ShowActionTarget `json:"target"`
}

// ShowActionTarget is show.action.target, flattened exactly as STEP-9-
// SPEC.md section 5.3's wire examples show it (integration plus either the
// fpp fields or the mqtt fields directly, never nested a second level under
// an "fpp"/"mqtt" key) — this is the shape Builder C's wire types and
// api/openapi.yaml are expected to mirror.
type ShowActionTarget struct {
	Integration string `json:"integration"`

	// fpp-only. Empty/nil when Integration is "mqtt".
	InstanceID string         `json:"instanceId,omitempty"`
	Primitive  string         `json:"primitive,omitempty"`
	Params     map[string]any `json:"params,omitempty"`

	// mqtt-only. Empty/nil when Integration is "fpp".
	Broker  string                 `json:"broker,omitempty"`
	Publish *ShowActionMQTTPublish `json:"publish,omitempty"`
	Expect  *ShowActionMQTTExpect  `json:"expect,omitempty"`
}

// ShowActionMQTTPublish is show.action.target.publish.
type ShowActionMQTTPublish struct {
	Topic string `json:"topic"`
	// Payload is a real MQTT payload and an explicit empty string is a
	// valid, meaningful value (an empty publish is ordinary MQTT usage) —
	// only absent and explicit null are rejected. See
	// decodeRequiredStringAllowEmpty.
	Payload string `json:"payload"`
	QoS     int    `json:"qos"`
	// Retain defaults to false when absent; a present null is an error.
	// This is a deliberate THIRD absent-carries-meaning field beyond
	// onFailure/onUnconfirmed — see this package's showmacro.go doc comment
	// on ShowMacroOnFailureDefault for why the wave 2 shared contract's "the
	// only two keys" line is read as scoped to show.macro's policy axes,
	// not as a blanket rule over every payload this step adds, and why this
	// builder followed the wave2-builder-a.md brief's explicit,
	// field-specific instruction ("retain ... defaults false") over that
	// more general line rather than silently picking one. Flagged in this
	// builder's own report as a place the two source documents disagreed.
	Retain bool `json:"retain"`
}

// ShowActionMQTTExpect is show.action.target.expect. Value is a pointer so
// "value absent" and "value present" are distinguishable; which kinds
// accept, require, or forbid it is decodeMQTTExpect's own rule (a judgment
// call this builder made beyond what STEP-9-SPEC.md section 7.3 states
// explicitly for "boolean" and "text" — see this builder's report).
type ShowActionMQTTExpect struct {
	Kind  string  `json:"kind"`
	Topic string  `json:"topic,omitempty"`
	Value *string `json:"value,omitempty"`
	// DeadlineSeconds is 0 (omitted) only for kind "none".
	DeadlineSeconds int `json:"deadlineSeconds,omitempty"`
}

// EncodeShowActionPayload marshals p into config_revisions.payload_json's
// column shape, mirroring [EncodeFPPEndpointsPayload]'s own pattern. p is
// assumed already valid (the product of DecodeShowActionPayload); this
// function does not re-validate.
func EncodeShowActionPayload(p ShowActionPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.action payload: %w", err)
	}
	return string(b), nil
}

// DecodeShowActionPayload parses and validates raw against STEP-9-SPEC.md
// section 5.3's rules. endpoints is the caller's currently-configured FPP
// endpoint list (Config.FPPEndpoints or the store-authoritative
// equivalent); brokers is the caller's declared integration broker set
// (see integrationbrokers.go); registry resolves and validates an FPP
// primitive's own parameter vocabulary and safety class. None of the three
// is fetched by this package — see this file's own top doc comment.
func DecodeShowActionPayload(raw string, endpoints []FPPEndpoint, brokers []IntegrationBroker, registry FPPPrimitiveRegistry) (ShowActionPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	show, verr := decodeRequiredString(top, "show", "show")
	if verr != nil {
		return ShowActionPayload{}, verr
	}
	if verr := validateShowRef(show); verr != nil {
		return ShowActionPayload{}, verr
	}

	label, verr := decodeRequiredString(top, "label", "label")
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	description, verr := decodeOptionalString(top, "description", "description")
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	safetyClass, verr := decodeRequiredString(top, "safetyClass", "safetyClass")
	if verr != nil {
		return ShowActionPayload{}, verr
	}
	if !showSafetyClasses[safetyClass] {
		return ShowActionPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "safetyClass",
			Detail: "safetyClass must be one of none, blackout, stop, or powerOff",
		}
	}

	targetFields, verr := decodeRequiredObject(top, "target", "target")
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	integration, verr := decodeRequiredString(targetFields, "integration", "target.integration")
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	var target ShowActionTarget
	switch integration {
	case ShowActionIntegrationFPP:
		target, verr = decodeFPPTarget(targetFields, safetyClass, endpoints, registry)
	case ShowActionIntegrationMQTT:
		target, verr = decodeMQTTTarget(targetFields, brokers)
	default:
		verr = &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.integration",
			Detail: "integration must be \"fpp\" or \"mqtt\"",
		}
	}
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	return ShowActionPayload{
		Show:        show,
		Label:       label,
		Description: description,
		SafetyClass: safetyClass,
		Target:      target,
	}, nil
}

// decodeFPPTarget decodes and validates target.integration == "fpp".
// declaredSafetyClass is the payload's own top-level safetyClass, needed
// here to enforce STEP-9-SPEC.md section 5.3's agreement rule.
func decodeFPPTarget(targetFields map[string]json.RawMessage, declaredSafetyClass string, endpoints []FPPEndpoint, registry FPPPrimitiveRegistry) (ShowActionTarget, *ValidationError) {
	instanceID, verr := decodeRequiredString(targetFields, "instanceId", "target.instanceId")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	if !fppInstanceConfigured(instanceID, endpoints) {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "target.instanceId",
			Detail: fmt.Sprintf("instance %q is not a configured FPP endpoint", instanceID),
		}
	}

	primitive, verr := decodeRequiredString(targetFields, "primitive", "target.primitive")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	if !stringInSlice(primitive, registry.WireActions()) {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "target.primitive",
			Detail: fmt.Sprintf("primitive %q is not a supported FPP action (supported: %s)", primitive, strings.Join(registry.WireActions(), ", ")),
		}
	}

	params, err := registry.DecodeActionParams(primitive, targetFields)
	if err != nil {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.params",
			Detail: err.Error(),
		}
	}

	registeredClass, ok := registry.Decision11Class(primitive)
	if !ok {
		// Unreachable given the WireActions membership check just above,
		// but answered rather than left to panic on a nil map lookup one
		// layer down if the two ever disagree.
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.primitive",
			Detail: fmt.Sprintf("primitive %q has no registered safety class", primitive),
		}
	}
	if registeredClass != declaredSafetyClass {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeSafetyClassMismatch, Field: "safetyClass",
			Detail: fmt.Sprintf("safetyClass %q does not match primitive %q's own registered safety class %q", declaredSafetyClass, primitive, registeredClass),
		}
	}

	return ShowActionTarget{
		Integration: ShowActionIntegrationFPP,
		InstanceID:  instanceID,
		Primitive:   primitive,
		Params:      params,
	}, nil
}

// decodeMQTTTarget decodes and validates target.integration == "mqtt".
func decodeMQTTTarget(targetFields map[string]json.RawMessage, brokers []IntegrationBroker) (ShowActionTarget, *ValidationError) {
	broker, verr := decodeRequiredString(targetFields, "broker", "target.broker")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	if !integrationBrokerDeclared(broker, brokers) {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "target.broker",
			Detail: fmt.Sprintf("broker %q is not a declared integration broker", broker),
		}
	}

	publishFields, verr := decodeRequiredObject(targetFields, "publish", "target.publish")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	publish, verr := decodeMQTTPublish(publishFields)
	if verr != nil {
		return ShowActionTarget{}, verr
	}

	expectFields, verr := decodeRequiredObject(targetFields, "expect", "target.expect")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	expect, verr := decodeMQTTExpect(expectFields)
	if verr != nil {
		return ShowActionTarget{}, verr
	}

	return ShowActionTarget{
		Integration: ShowActionIntegrationMQTT,
		Broker:      broker,
		Publish:     &publish,
		Expect:      &expect,
	}, nil
}

func decodeMQTTPublish(fields map[string]json.RawMessage) (ShowActionMQTTPublish, *ValidationError) {
	topic, verr := decodeRequiredString(fields, "topic", "target.publish.topic")
	if verr != nil {
		return ShowActionMQTTPublish{}, verr
	}

	payload, verr := decodeRequiredStringAllowEmpty(fields, "payload", "target.publish.payload")
	if verr != nil {
		return ShowActionMQTTPublish{}, verr
	}

	qos, verr := decodeRequiredInt(fields, "qos", "target.publish.qos")
	if verr != nil {
		return ShowActionMQTTPublish{}, verr
	}
	if qos < 0 || qos > 2 {
		return ShowActionMQTTPublish{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.publish.qos",
			Detail: "qos must be 0, 1, or 2",
		}
	}

	// retain: THE one deliberate exception in this file beyond
	// onFailure/onUnconfirmed — see ShowActionMQTTPublish.Retain's own doc
	// comment.
	retain, verr := decodeDefaultedBool(fields, "retain", "target.publish.retain", false)
	if verr != nil {
		return ShowActionMQTTPublish{}, verr
	}

	return ShowActionMQTTPublish{Topic: topic, Payload: payload, QoS: qos, Retain: retain}, nil
}

func decodeMQTTExpect(fields map[string]json.RawMessage) (ShowActionMQTTExpect, *ValidationError) {
	kind, verr := decodeRequiredString(fields, "kind", "target.expect.kind")
	if verr != nil {
		return ShowActionMQTTExpect{}, verr
	}
	if !mqttExpectKinds[kind] {
		return ShowActionMQTTExpect{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.expect.kind",
			Detail: "kind must be one of none, boolean, number, text, or match",
		}
	}

	_, hasTopic := fields["topic"]
	_, hasValue := fields["value"]
	_, hasDeadline := fields["deadlineSeconds"]

	if kind == MQTTExpectKindNone {
		if hasTopic || hasValue || hasDeadline {
			return ShowActionMQTTExpect{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "target.expect",
				Detail: "kind \"none\" must not supply topic, value, or deadlineSeconds",
			}
		}
		return ShowActionMQTTExpect{Kind: kind}, nil
	}

	topic, verr := decodeRequiredString(fields, "topic", "target.expect.topic")
	if verr != nil {
		return ShowActionMQTTExpect{}, verr
	}

	deadline, verr := decodeRequiredInt(fields, "deadlineSeconds", "target.expect.deadlineSeconds")
	if verr != nil {
		return ShowActionMQTTExpect{}, verr
	}
	if deadline <= 0 {
		return ShowActionMQTTExpect{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.expect.deadlineSeconds",
			Detail: "deadlineSeconds must be positive",
		}
	}
	if deadline > mqttExpectMaxDeadlineSeconds {
		return ShowActionMQTTExpect{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.expect.deadlineSeconds",
			Detail: fmt.Sprintf("deadlineSeconds must not exceed %d", mqttExpectMaxDeadlineSeconds),
		}
	}

	// value's treatment per kind is this builder's own judgment call — see
	// ShowActionMQTTExpect's doc comment and this builder's report.
	// "match" requires it (the equality target); "number" accepts it as an
	// optional equality check; "boolean" and "text" have no use for it
	// (the payload IS the boolean, or IS the recorded text), so — matching
	// this endpoint's own "supplying one is an error rather than being
	// ignored" rule for kind "none" — a present value is rejected rather
	// than silently discarded.
	var value *string
	switch kind {
	case MQTTExpectKindMatch:
		v, verr := decodeRequiredStringAllowEmpty(fields, "value", "target.expect.value")
		if verr != nil {
			return ShowActionMQTTExpect{}, verr
		}
		value = &v
	case MQTTExpectKindNumber:
		if hasValue {
			raw := fields["value"]
			if isJSONNull(raw) {
				return ShowActionMQTTExpect{}, &ValidationError{
					Code: ValidationCodeFieldNull, Field: "target.expect.value",
					Detail: "value must not be null; omit it to accept receipt without an equality check",
				}
			}
			var f float64
			if err := json.Unmarshal(raw, &f); err != nil {
				return ShowActionMQTTExpect{}, &ValidationError{
					Code: ValidationCodeFieldInvalid, Field: "target.expect.value",
					Detail: "value must be a JSON number for kind \"number\"",
				}
			}
			s := strings.TrimSpace(string(raw))
			value = &s
		}
	case MQTTExpectKindBoolean, MQTTExpectKindText:
		if hasValue {
			return ShowActionMQTTExpect{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "target.expect.value",
				Detail: fmt.Sprintf("value must not be supplied for kind %q", kind),
			}
		}
	}

	return ShowActionMQTTExpect{Kind: kind, Topic: topic, Value: value, DeadlineSeconds: deadline}, nil
}

func fppInstanceConfigured(id string, endpoints []FPPEndpoint) bool {
	for _, ep := range endpoints {
		if ep.ID == id {
			return true
		}
	}
	return false
}

func integrationBrokerDeclared(id string, brokers []IntegrationBroker) bool {
	for _, b := range brokers {
		if b.ID == id {
			return true
		}
	}
	return false
}

func stringInSlice(s string, list []string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// validateShowRef format-validates (never existence-validates — the Show
// object is Track E's and does not exist yet, STEP-9-SPEC.md section 5.2)
// a "show" reference. Reuses [mqttproto.ValidateNodeID] rather than
// inventing a second identifier grammar: this builder's own judgment call,
// since neither the shared contract nor STEP-9-SPEC.md states an exact
// format — see this builder's report. It happens to accept every example
// in STEP-9-SPEC.md ("halloween-2026").
func validateShowRef(show string) *ValidationError {
	if err := mqttproto.ValidateNodeID(show); err != nil {
		return &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "show",
			Detail: "show must be 1-64 characters of lowercase letters, digits, and hyphens, and must not start or end with a hyphen",
		}
	}
	return nil
}

// --- Shared low-level JSON decode helpers, used by this file and
// showmacro.go. Every one enforces the absent/null/empty distinction this
// step's payloads require (CLAUDE.md's own standing rule, restated at every
// write surface this project has shipped) rather than letting Go's zero
// value stand in for "not decided". ---

// isJSONNull reports whether raw is the literal JSON null token. Mirrors
// internal/coordinator/api's identical, unexported isJSONNull
// (fppcommand_primitives.go) — that copy is private to its own package and
// this package cannot import it (see FPPPrimitiveRegistry's own doc
// comment), so this is a second, intentionally identical implementation of
// a two-line function, not a drifting duplicate of anything with real
// logic in it.
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// decodeTopLevelObject parses raw as a JSON object of raw fields, the entry
// point every Decode* function in this file and showmacro.go starts from.
func decodeTopLevelObject(raw string) (map[string]json.RawMessage, *ValidationError) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return nil, &ValidationError{
			Code:   ValidationCodeBodyInvalid,
			Detail: "the request body must be a JSON object",
		}
	}
	return top, nil
}

// decodeRequiredObject reads key from top as a required, non-null JSON
// object.
func decodeRequiredObject(top map[string]json.RawMessage, key, field string) (map[string]json.RawMessage, *ValidationError) {
	raw, present := top[key]
	if !present {
		return nil, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON object", field)}
	}
	return fields, nil
}

// decodeRequiredString reads key from top as a required, non-null,
// non-empty string.
func decodeRequiredString(top map[string]json.RawMessage, key, field string) (string, *ValidationError) {
	raw, present := top[key]
	if !present {
		return "", &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return "", &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a string", field)}
	}
	if s == "" {
		return "", &ValidationError{Code: ValidationCodeFieldEmpty, Field: field, Detail: fmt.Sprintf("%s must not be empty", field)}
	}
	return s, nil
}

// decodeRequiredStringAllowEmpty is [decodeRequiredString] without the
// empty-string rejection, for a field where an explicit empty string is a
// real, distinct, valid value (an MQTT payload, or a "match" target value)
// rather than a stand-in for "nothing was provided".
func decodeRequiredStringAllowEmpty(top map[string]json.RawMessage, key, field string) (string, *ValidationError) {
	raw, present := top[key]
	if !present {
		return "", &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return "", &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a string", field)}
	}
	return s, nil
}

// decodeOptionalString reads key from top: absent means "" (unset);
// present-and-null is an error; present is the value verbatim, including
// "" (an operator explicitly clearing a description is indistinguishable
// on the wire from never having set one, and nothing downstream needs the
// two told apart the way onFailure/onUnconfirmed's defaults must be).
func decodeOptionalString(top map[string]json.RawMessage, key, field string) (string, *ValidationError) {
	raw, present := top[key]
	if !present {
		return "", nil
	}
	if isJSONNull(raw) {
		return "", &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null; omit it to leave it unset", field)}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a string", field)}
	}
	return s, nil
}

// decodeRequiredInt reads key from top as a required, non-null whole
// number.
func decodeRequiredInt(top map[string]json.RawMessage, key, field string) (int, *ValidationError) {
	raw, present := top[key]
	if !present {
		return 0, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return 0, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON number", field)}
	}
	if f != float64(int(f)) {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a whole number", field)}
	}
	return int(f), nil
}

// decodeDefaultedBool reads key from top: absent takes def; present-and-null
// is always an error (never "use the default"); present is the value
// verbatim.
func decodeDefaultedBool(top map[string]json.RawMessage, key, field string, def bool) (bool, *ValidationError) {
	raw, present := top[key]
	if !present {
		return def, nil
	}
	if isJSONNull(raw) {
		return false, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null; omit it to use the default (%v)", field, def)}
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a boolean", field)}
	}
	return b, nil
}

// decodeDefaultedEnum reads key from top: absent takes def; present-and-null
// is always an error; present-and-empty-string is always an error (distinct
// from absent — CLAUDE.md's own standing rule for this step, restated
// explicitly rather than left to fall out of a zero value); present must be
// a member of allowed. This is the ONE function onFailure and onUnconfirmed
// both go through (showmacro.go), each with its own field name, its own
// default, and its own allowed set — deliberately independent calls rather
// than one shared piece of state, so the two policy axes STEP-9-SPEC.md
// section 2.2 / ADR-031 decision 2 requires stay genuinely separate and
// this function cannot become "the one field that answers both".
func decodeDefaultedEnum(top map[string]json.RawMessage, key, field, def string, allowed map[string]bool) (string, *ValidationError) {
	raw, present := top[key]
	if !present {
		return def, nil
	}
	if isJSONNull(raw) {
		return "", &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null; omit it to use the default (%q)", field, def)}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a string", field)}
	}
	if s == "" {
		return "", &ValidationError{Code: ValidationCodeFieldEmpty, Field: field, Detail: fmt.Sprintf("%s must not be an empty string; omit it to use the default (%q)", field, def)}
	}
	if !allowed[s] {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s is not a recognized value", field)}
	}
	return s, nil
}

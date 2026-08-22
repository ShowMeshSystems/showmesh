package config

import (
	"encoding/json"
	"fmt"
)

// Track F seam F6 (RESTING-MODE.md §10, ADR-016, ADR-024, ADR-029,
// ADR-038): night.session's two optional blocks, "siteControl" and
// "interlocks", which nightsession.go's own rejectNightSessionUnimplemented
// Blocks refused unconditionally until now. Both blocks are independently
// omittable; RESTING-MODE.md §10's own opening line ("None of this section
// is required to run a night session") is nightsession_test.go's own
// TestDecodeNightSessionPayloadAbsentSiteControlIsValid.
//
// Every cross-object reference this file needs (an existing show.action
// object, and — for an interlock's own signal — whether that action is a
// confirmable mqtt binding) is a caller-supplied callback, exactly like
// nightsession.go's own AssetCurrent/ActionResolver: this package has no
// store access.

// --- interlocks: RESTING-MODE.md §10.1 ---

// The nine v1 interlock phases, exactly. An unknown value is rejected
// rather than treated as observational (RESTING-MODE.md §10.1's own
// words).
const (
	NightInterlockPhasePrepareSite           = "prepare-site"
	NightInterlockPhasePresentationPowerOn   = "presentation-power-on"
	NightInterlockPhaseProjectorStrike       = "projector-strike"
	NightInterlockPhaseRunReadiness          = "run-readiness"
	NightInterlockPhaseStartPreshow          = "start-preshow"
	NightInterlockPhaseStartNight            = "start-night"
	NightInterlockPhaseEnterResting          = "enter-resting"
	NightInterlockPhaseFadeOutNight          = "fade-out-night"
	NightInterlockPhasePowerDownPresentation = "power-down-presentation"
)

var nightInterlockPhaseValues = map[string]bool{
	NightInterlockPhasePrepareSite:           true,
	NightInterlockPhasePresentationPowerOn:   true,
	NightInterlockPhaseProjectorStrike:       true,
	NightInterlockPhaseRunReadiness:          true,
	NightInterlockPhaseStartPreshow:          true,
	NightInterlockPhaseStartNight:            true,
	NightInterlockPhaseEnterResting:          true,
	NightInterlockPhaseFadeOutNight:          true,
	NightInterlockPhasePowerDownPresentation: true,
}

// The three interlock postures.
const (
	NightInterlockPostureObserve  = "observe"
	NightInterlockPostureBlock    = "block"
	NightInterlockPostureDisabled = "disabled"
)

var nightInterlockPostureValues = map[string]bool{
	NightInterlockPostureObserve:  true,
	NightInterlockPostureBlock:    true,
	NightInterlockPostureDisabled: true,
}

// The two members of a "block" rule's onUnavailable. No default:
// RESTING-MODE.md §10.1 — "required onUnavailable: block|allow with no
// default."
const (
	NightInterlockOnUnavailableBlock = "block"
	NightInterlockOnUnavailableAllow = "allow"
)

var nightInterlockOnUnavailableValues = map[string]bool{
	NightInterlockOnUnavailableBlock: true,
	NightInterlockOnUnavailableAllow: true,
}

// The two members of a "block" rule's overridePolicy. No default, for the
// identical reason onUnavailable has none.
const (
	NightInterlockOverridePolicyNone               = "none"
	NightInterlockOverridePolicyAuthorizedOperator = "authorized-operator"
)

var nightInterlockOverridePolicyValues = map[string]bool{
	NightInterlockOverridePolicyNone:               true,
	NightInterlockOverridePolicyAuthorizedOperator: true,
}

// nightInterlockMaxFreshnessSeconds bounds an enabled rule's optional
// freshnessSeconds. Ruling (this seam's own build brief): this build may
// decode and validate freshness now even where no observation-backed
// source exists to enforce it against, but must never claim it IS
// enforced. See nightinterlock.go (internal/coordinator/api) for the one
// evidence source this build actually has (a named mqtt action's own
// request/response), which carries no independent "collected at" the way
// an observation.Observation does — freshness here is validated shape,
// not yet a runtime gate.
const nightInterlockMaxFreshnessSeconds = 3600

// NightInterlockSignalInfo is what an interlock's "signal" must resolve
// to: an existing show.action, which show it belongs to (for ADR-027's
// namespace check), and enough of its target to confirm it can actually
// answer a condition. Deliberately narrower than a full ShowActionTarget:
// this package must not import the mqtt target's own decoded shape here,
// since the caller (internal/coordinator/api) is the one that already
// decoded it.
type NightInterlockSignalInfo struct {
	Show           string
	Integration    string
	MQTTExpectKind string
}

// InterlockSignalResolver reports whether actionID names an existing
// show.action object and, when it does, enough of its target for
// decodeNightInterlockRule to confirm it is a usable evidence source.
// Caller-supplied, mirroring ActionResolver one field over.
type InterlockSignalResolver func(actionID string) (NightInterlockSignalInfo, bool)

// NightInterlockRule is one night.session.interlocks[] entry.
// FreshnessSeconds is a pointer because absent and 0 are both legal and
// distinct (0 means "evidence must be from this same instant," an
// operator choice, not this decoder's own default). OnUnavailable and
// OverridePolicy are "" for posture "observe" or "disabled" — never a
// zero-value stand-in for one of the two real enum members, matching
// [NightSessionBackgroundAudio]'s own CrossfadeMs precedent one file over.
type NightInterlockRule struct {
	Name             string `json:"name"`
	Phase            string `json:"phase"`
	Posture          string `json:"posture"`
	Signal           string `json:"signal,omitempty"`
	FreshnessSeconds *int   `json:"freshnessSeconds,omitempty"`
	FailureText      string `json:"failureText,omitempty"`
	OnUnavailable    string `json:"onUnavailable,omitempty"`
	OverridePolicy   string `json:"overridePolicy,omitempty"`
}

var (
	nightInterlockEnabledKeys = map[string]bool{
		"name": true, "phase": true, "posture": true, "signal": true,
		"freshnessSeconds": true, "failureText": true, "onUnavailable": true, "overridePolicy": true,
	}
	// nightInterlockDisabledKeys is RESTING-MODE.md §10.1's own rule: "A
	// disabled entry contains only its name, phase, and posture: disabled;
	// signal, condition, freshness, timeout, unavailable, and override
	// fields are rejected."
	nightInterlockDisabledKeys = map[string]bool{"name": true, "phase": true, "posture": true}
)

// decodeNightInterlocks decodes night.session.interlocks. An absent key
// and an explicit empty array both mean "no interlocks configured" —
// RESTING-MODE.md §10's own "Omitting siteControl and interlocks is
// valid," read as applying at the granularity of "no rules" rather than
// forcing every deployment with any rule at all to also avoid the empty
// case.
func decodeNightInterlocks(top map[string]json.RawMessage, sessionShow string, resolver InterlockSignalResolver) ([]NightInterlockRule, *ValidationError) {
	raw, present := top["interlocks"]
	if !present {
		return nil, nil
	}
	if isJSONNull(raw) {
		return nil, &ValidationError{
			Code: ValidationCodeFieldNull, Field: "interlocks",
			Detail: "interlocks must not be null; omit it entirely to configure none",
		}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "interlocks", Detail: "interlocks must be a JSON array"}
	}

	rules := make([]NightInterlockRule, 0, len(items))
	seenNames := make(map[string]bool, len(items))
	for i, item := range items {
		field := fmt.Sprintf("interlocks[%d]", i)
		rule, verr := decodeNightInterlockRule(item, field, sessionShow, resolver)
		if verr != nil {
			return nil, verr
		}
		if seenNames[rule.Name] {
			return nil, &ValidationError{
				Code: ValidationCodeInterlockNameDuplicate, Field: field + ".name",
				Detail: fmt.Sprintf("interlock name %q is used more than once", rule.Name),
			}
		}
		seenNames[rule.Name] = true
		rules = append(rules, rule)
	}
	return rules, nil
}

func decodeNightInterlockRule(raw json.RawMessage, field, sessionShow string, resolver InterlockSignalResolver) (NightInterlockRule, *ValidationError) {
	if isJSONNull(raw) {
		return NightInterlockRule{}, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return NightInterlockRule{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON object", field)}
	}

	name, verr := decodeRequiredString(fields, "name", field+".name")
	if verr != nil {
		return NightInterlockRule{}, verr
	}
	phase, verr := decodeRequiredEnum(fields, "phase", field+".phase", nightInterlockPhaseValues)
	if verr != nil {
		return NightInterlockRule{}, verr
	}
	posture, verr := decodeRequiredEnum(fields, "posture", field+".posture", nightInterlockPostureValues)
	if verr != nil {
		return NightInterlockRule{}, verr
	}

	if posture == NightInterlockPostureDisabled {
		if verr := rejectUnknownKeysUnder(fields, nightInterlockDisabledKeys, field); verr != nil {
			return NightInterlockRule{}, verr
		}
		return NightInterlockRule{Name: name, Phase: phase, Posture: posture}, nil
	}

	if verr := rejectUnknownKeysUnder(fields, nightInterlockEnabledKeys, field); verr != nil {
		return NightInterlockRule{}, verr
	}

	signal, verr := decodeRequiredString(fields, "signal", field+".signal")
	if verr != nil {
		return NightInterlockRule{}, verr
	}
	if resolver == nil {
		return NightInterlockRule{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: field + ".signal",
			Detail: "no signal resolver is available to validate this reference",
		}
	}
	info, ok := resolver(signal)
	if !ok {
		return NightInterlockRule{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: field + ".signal",
			Detail: fmt.Sprintf("signal %q does not resolve to an existing show.action object", signal),
		}
	}
	if info.Show != sessionShow {
		return NightInterlockRule{}, &ValidationError{
			Code: ValidationCodeCrossShowReference, Field: field + ".signal",
			Detail: fmt.Sprintf("signal %q belongs to show %q, not this session's own show %q (ADR-027: a Show is a namespace)", signal, info.Show, sessionShow),
		}
	}
	if info.Integration != ShowActionIntegrationMQTT || info.MQTTExpectKind == "" || info.MQTTExpectKind == MQTTExpectKindNone {
		return NightInterlockRule{}, &ValidationError{
			Code: ValidationCodeInterlockSignalNotConfirmable, Field: field + ".signal",
			Detail: fmt.Sprintf("signal %q must be an mqtt action with an expected response other than \"none\"; an interlock's evidence is that action's own request/response", signal),
		}
	}

	var freshness *int
	if _, has := fields["freshnessSeconds"]; has {
		v, verr := decodeRequiredNonNegativeInt(fields, "freshnessSeconds", field+".freshnessSeconds")
		if verr != nil {
			return NightInterlockRule{}, verr
		}
		if v > nightInterlockMaxFreshnessSeconds {
			return NightInterlockRule{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: field + ".freshnessSeconds",
				Detail: fmt.Sprintf("freshnessSeconds must not exceed %d", nightInterlockMaxFreshnessSeconds),
			}
		}
		freshness = &v
	}

	failureText, verr := decodeRequiredString(fields, "failureText", field+".failureText")
	if verr != nil {
		return NightInterlockRule{}, verr
	}

	_, hasOnUnavailable := fields["onUnavailable"]
	_, hasOverridePolicy := fields["overridePolicy"]

	rule := NightInterlockRule{Name: name, Phase: phase, Posture: posture, Signal: signal, FreshnessSeconds: freshness, FailureText: failureText}

	switch posture {
	case NightInterlockPostureObserve:
		// RESTING-MODE.md §10.1: "An observe rule may not set onUnavailable
		// or overridePolicy because it never withholds."
		if hasOnUnavailable || hasOverridePolicy {
			return NightInterlockRule{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: field,
				Detail: "an \"observe\" interlock never withholds and must not set onUnavailable or overridePolicy",
			}
		}
	case NightInterlockPostureBlock:
		onUnavailable, verr := decodeRequiredEnum(fields, "onUnavailable", field+".onUnavailable", nightInterlockOnUnavailableValues)
		if verr != nil {
			return NightInterlockRule{}, verr
		}
		overridePolicy, verr := decodeRequiredEnum(fields, "overridePolicy", field+".overridePolicy", nightInterlockOverridePolicyValues)
		if verr != nil {
			return NightInterlockRule{}, verr
		}
		rule.OnUnavailable = onUnavailable
		rule.OverridePolicy = overridePolicy
	}
	return rule, nil
}

// --- siteControl: RESTING-MODE.md §10.2/§10.4 ---

// The four members of a power binding's powerDomain.
const (
	NightPowerDomainPresentation  = "presentation"
	NightPowerDomainEnvironmental = "environmental"
	NightPowerDomainMixed         = "mixed"
	NightPowerDomainUnknown       = "unknown"
)

var nightPowerDomainValues = map[string]bool{
	NightPowerDomainPresentation:  true,
	NightPowerDomainEnvironmental: true,
	NightPowerDomainMixed:         true,
	NightPowerDomainUnknown:       true,
}

// The two members of a power binding's domainProvenance. "provider" is a
// closed-enum member with no accepting decoder path today — see
// decodeNightPowerBinding's own comment — kept in the vocabulary because
// RESTING-MODE.md §10.2 names it as the shape a future authoritative
// provider would use, not because this build can produce it.
const (
	NightDomainProvenanceProvider         = "provider"
	NightDomainProvenanceOperatorDeclared = "operator-declared"
)

var nightDomainProvenanceValues = map[string]bool{
	NightDomainProvenanceProvider:         true,
	NightDomainProvenanceOperatorDeclared: true,
}

// The two members of presentationPowerOff.removalPolicy. No default:
// RESTING-MODE.md §10.2 — "no default" and "a half-filled configuration is
// invalid at write time."
const (
	NightRemovalPolicyImmediate    = "immediate"
	NightRemovalPolicyAfterActions = "after-actions"
)

var nightRemovalPolicyValues = map[string]bool{
	NightRemovalPolicyImmediate:    true,
	NightRemovalPolicyAfterActions: true,
}

// The three prerequisite kinds RESTING-MODE.md §10.2 names: "required
// shutdown actions, confirmations, delays, or evidence conditions." A
// confirmation is an action prerequisite with requireConfirmation true,
// not a fourth kind.
const (
	NightPrerequisiteKindAction   = "action"
	NightPrerequisiteKindDelay    = "delay"
	NightPrerequisiteKindEvidence = "evidence"
)

var nightPrerequisiteKindValues = map[string]bool{
	NightPrerequisiteKindAction:   true,
	NightPrerequisiteKindDelay:    true,
	NightPrerequisiteKindEvidence: true,
}

// nightPrerequisiteMaxDelayMs bounds one "delay" prerequisite: 24 hours,
// generous enough for any real cooldown while still finite and schema-
// bounded (RESTING-MODE.md §10.2: "finite, non-negative, limited by
// versioned schema bounds").
const nightPrerequisiteMaxDelayMs = 24 * 60 * 60 * 1000

// nightPrerequisiteMaxCount bounds the length of one prerequisites list.
const nightPrerequisiteMaxCount = 20

// NightPowerBinding is the shared shape of presentationPowerOn and
// presentationPowerOff's own first three fields.
type NightPowerBinding struct {
	Action           string `json:"action"`
	PowerDomain      string `json:"powerDomain"`
	DomainProvenance string `json:"domainProvenance"`
}

// NightPrerequisite is one presentationPowerOff.prerequisites[] entry.
// Action and RequireConfirmation apply only to Kind "action" and
// "evidence" (RequireConfirmation to "action" only); DelayMs applies only
// to Kind "delay". A zero value in the field that does not apply to Kind
// is never a distinct configuration from that field being absent — the
// decoder rejects the field being present at all outside its own Kind.
type NightPrerequisite struct {
	Kind                string `json:"kind"`
	Action              string `json:"action,omitempty"`
	RequireConfirmation bool   `json:"requireConfirmation,omitempty"`
	DelayMs             int    `json:"delayMs,omitempty"`
}

// NightPresentationPowerOff is night.session.siteControl.presentationPowerOff.
type NightPresentationPowerOff struct {
	NightPowerBinding
	RemovalPolicy            string              `json:"removalPolicy"`
	ImmediateSafeAttestation bool                `json:"immediateSafeAttestation,omitempty"`
	Prerequisites            []NightPrerequisite `json:"prerequisites,omitempty"`
}

// NightSiteControl is night.session.siteControl. Every field is
// independently optional; decodeNightSiteControl refuses an entirely
// empty object rather than accepting configuration nothing enforces (the
// same posture the retired rejectNightSessionUnimplementedBlocks held for
// the whole block, narrowed now to "empty" instead of "present at all").
type NightSiteControl struct {
	RequestThermalProfile string                     `json:"requestThermalProfile,omitempty"`
	PresentationPowerOn   *NightPowerBinding         `json:"presentationPowerOn,omitempty"`
	PresentationPowerOff  *NightPresentationPowerOff `json:"presentationPowerOff,omitempty"`
}

var (
	nightSiteControlKeys  = map[string]bool{"requestThermalProfile": true, "presentationPowerOn": true, "presentationPowerOff": true}
	nightPowerBindingKeys = map[string]bool{"action": true, "powerDomain": true, "domainProvenance": true}
	nightPowerOffKeys     = map[string]bool{
		"action": true, "powerDomain": true, "domainProvenance": true,
		"removalPolicy": true, "immediateSafeAttestation": true, "prerequisites": true,
	}
	nightPrerequisiteKeys = map[string]bool{"kind": true, "action": true, "requireConfirmation": true, "delayMs": true}
)

// decodeNightSiteControl decodes night.session.siteControl. actionResolver
// is [ActionResolver]: a power/thermal binding needs only existence plus
// same-show, never the mqtt-confirmable check an interlock's signal needs
// (RESTING-MODE.md §10.4's own shutdown actions are dispatched, not
// polled for a condition).
func decodeNightSiteControl(top map[string]json.RawMessage, sessionShow string, actionResolver ActionResolver) (*NightSiteControl, *ValidationError) {
	raw, present := top["siteControl"]
	if !present {
		return nil, nil
	}
	if isJSONNull(raw) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: "siteControl", Detail: "siteControl must not be null; omit it entirely to configure no site control"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "siteControl", Detail: "siteControl must be a JSON object"}
	}
	if verr := rejectUnknownKeysUnder(fields, nightSiteControlKeys, "siteControl"); verr != nil {
		return nil, verr
	}
	if len(fields) == 0 {
		return nil, &ValidationError{
			Code: ValidationCodeFieldRequired, Field: "siteControl",
			Detail: "siteControl configures none of requestThermalProfile, presentationPowerOn, or presentationPowerOff; omit siteControl entirely rather than configuring nothing",
		}
	}

	sc := &NightSiteControl{}

	if _, has := fields["requestThermalProfile"]; has {
		action, verr := decodeRequiredString(fields, "requestThermalProfile", "siteControl.requestThermalProfile")
		if verr != nil {
			return nil, verr
		}
		if verr := validateNightSiteActionRef(action, "siteControl.requestThermalProfile", sessionShow, actionResolver); verr != nil {
			return nil, verr
		}
		sc.RequestThermalProfile = action
	}

	if raw, has := fields["presentationPowerOn"]; has {
		if isJSONNull(raw) {
			return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: "siteControl.presentationPowerOn", Detail: "siteControl.presentationPowerOn must not be null; omit it to configure none"}
		}
		var pf map[string]json.RawMessage
		if err := json.Unmarshal(raw, &pf); err != nil {
			return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "siteControl.presentationPowerOn", Detail: "siteControl.presentationPowerOn must be a JSON object"}
		}
		if verr := rejectUnknownKeysUnder(pf, nightPowerBindingKeys, "siteControl.presentationPowerOn"); verr != nil {
			return nil, verr
		}
		binding, verr := decodeNightPowerBinding(pf, "siteControl.presentationPowerOn", sessionShow, actionResolver)
		if verr != nil {
			return nil, verr
		}
		if binding.PowerDomain != NightPowerDomainPresentation {
			return nil, &ValidationError{
				Code: ValidationCodePowerDomainRefused, Field: "siteControl.presentationPowerOn.powerDomain",
				Detail: fmt.Sprintf("presentationPowerOn only accepts powerDomain %q, not %q", NightPowerDomainPresentation, binding.PowerDomain),
			}
		}
		sc.PresentationPowerOn = &binding
	}

	if raw, has := fields["presentationPowerOff"]; has {
		if isJSONNull(raw) {
			return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: "siteControl.presentationPowerOff", Detail: "siteControl.presentationPowerOff must not be null; omit it to configure none"}
		}
		var pf map[string]json.RawMessage
		if err := json.Unmarshal(raw, &pf); err != nil {
			return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "siteControl.presentationPowerOff", Detail: "siteControl.presentationPowerOff must be a JSON object"}
		}
		off, verr := decodeNightPresentationPowerOff(pf, sessionShow, actionResolver)
		if verr != nil {
			return nil, verr
		}
		sc.PresentationPowerOff = &off
	}

	return sc, nil
}

func validateNightSiteActionRef(action, field, sessionShow string, actionResolver ActionResolver) *ValidationError {
	if actionResolver == nil {
		return &ValidationError{Code: ValidationCodeFieldUnknownReference, Field: field, Detail: "no action resolver is available to validate this reference"}
	}
	show, ok := actionResolver(action)
	if !ok {
		return &ValidationError{Code: ValidationCodeFieldUnknownReference, Field: field, Detail: fmt.Sprintf("action %q does not resolve to an existing show.action object", action)}
	}
	if show != sessionShow {
		return &ValidationError{
			Code: ValidationCodeCrossShowReference, Field: field,
			Detail: fmt.Sprintf("action %q belongs to show %q, not this session's own show %q (ADR-027: a Show is a namespace)", action, show, sessionShow),
		}
	}
	return nil
}

func decodeNightPowerBinding(fields map[string]json.RawMessage, path, sessionShow string, actionResolver ActionResolver) (NightPowerBinding, *ValidationError) {
	action, verr := decodeRequiredString(fields, "action", path+".action")
	if verr != nil {
		return NightPowerBinding{}, verr
	}
	if verr := validateNightSiteActionRef(action, path+".action", sessionShow, actionResolver); verr != nil {
		return NightPowerBinding{}, verr
	}
	domain, verr := decodeRequiredEnum(fields, "powerDomain", path+".powerDomain", nightPowerDomainValues)
	if verr != nil {
		return NightPowerBinding{}, verr
	}
	provenance, verr := decodeRequiredEnum(fields, "domainProvenance", path+".domainProvenance", nightDomainProvenanceValues)
	if verr != nil {
		return NightPowerBinding{}, verr
	}
	if provenance == NightDomainProvenanceProvider {
		// RESTING-MODE.md §10.2: "A provider may supply domain provenance
		// only when it can authoritatively identify every target. Generic
		// MQTT and Home Assistant service-call bindings are
		// operator-declared." ADR-016 confirms no such provider exists in
		// this codebase for any power/climate device — enforced here
		// rather than trusted, per this seam's own build brief.
		return NightPowerBinding{}, &ValidationError{
			Code: ValidationCodeDomainProvenanceRefused, Field: path + ".domainProvenance",
			Detail: "domainProvenance \"provider\" is refused: no control provider in this build can authoritatively identify a power binding's physical targets, so every binding is operator-declared",
		}
	}
	return NightPowerBinding{Action: action, PowerDomain: domain, DomainProvenance: provenance}, nil
}

func decodeNightPresentationPowerOff(fields map[string]json.RawMessage, sessionShow string, actionResolver ActionResolver) (NightPresentationPowerOff, *ValidationError) {
	if verr := rejectUnknownKeysUnder(fields, nightPowerOffKeys, "siteControl.presentationPowerOff"); verr != nil {
		return NightPresentationPowerOff{}, verr
	}
	binding, verr := decodeNightPowerBinding(fields, "siteControl.presentationPowerOff", sessionShow, actionResolver)
	if verr != nil {
		return NightPresentationPowerOff{}, verr
	}
	if binding.PowerDomain != NightPowerDomainPresentation {
		return NightPresentationPowerOff{}, &ValidationError{
			Code: ValidationCodePowerDomainRefused, Field: "siteControl.presentationPowerOff.powerDomain",
			Detail: fmt.Sprintf("power-down-presentation only accepts powerDomain %q bindings, not %q (RESTING-MODE.md §10.2)", NightPowerDomainPresentation, binding.PowerDomain),
		}
	}

	policy, verr := decodeRequiredEnum(fields, "removalPolicy", "siteControl.presentationPowerOff.removalPolicy", nightRemovalPolicyValues)
	if verr != nil {
		return NightPresentationPowerOff{}, verr
	}

	_, hasAttestation := fields["immediateSafeAttestation"]
	_, hasPrereqs := fields["prerequisites"]

	off := NightPresentationPowerOff{NightPowerBinding: binding, RemovalPolicy: policy}

	switch policy {
	case NightRemovalPolicyImmediate:
		if hasPrereqs {
			return NightPresentationPowerOff{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "siteControl.presentationPowerOff.prerequisites",
				Detail: "prerequisites must be absent when removalPolicy is \"immediate\"",
			}
		}
		attested, verr := decodeRequiredBool(fields, "immediateSafeAttestation", "siteControl.presentationPowerOff.immediateSafeAttestation")
		if verr != nil {
			return NightPresentationPowerOff{}, verr
		}
		if !attested {
			return NightPresentationPowerOff{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "siteControl.presentationPowerOff.immediateSafeAttestation",
				Detail: "immediateSafeAttestation must be true to select \"immediate\"; use \"after-actions\" for a target that is not safe for immediate removal",
			}
		}
		off.ImmediateSafeAttestation = true
	case NightRemovalPolicyAfterActions:
		if hasAttestation {
			return NightPresentationPowerOff{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "siteControl.presentationPowerOff.immediateSafeAttestation",
				Detail: "immediateSafeAttestation must be absent when removalPolicy is \"after-actions\"",
			}
		}
		prereqs, verr := decodeNightPrerequisites(fields, sessionShow, actionResolver, binding.Action)
		if verr != nil {
			return NightPresentationPowerOff{}, verr
		}
		off.Prerequisites = prereqs
	}
	return off, nil
}

// decodeNightPrerequisites decodes presentationPowerOff.prerequisites.
// selfAction is the enclosing power-off binding's own action id: an
// "action" or "evidence" prerequisite naming it back is a direct cycle
// (RESTING-MODE.md §10.2 — "may not invoke the same power-off binding
// directly or indirectly"). This build has exactly one presentation
// power-off binding per session and show.action is a leaf protocol
// binding with no action-to-action call graph of its own (ADR-029: a
// macro invokes actions, an action never invokes another action), so a
// genuinely INDIRECT cycle has no representable path here — only direct
// self-reference is checked, and that is the whole reachable cycle set.
func decodeNightPrerequisites(fields map[string]json.RawMessage, sessionShow string, actionResolver ActionResolver, selfAction string) ([]NightPrerequisite, *ValidationError) {
	raw, present := fields["prerequisites"]
	if !present {
		return nil, &ValidationError{
			Code: ValidationCodeFieldRequired, Field: "siteControl.presentationPowerOff.prerequisites",
			Detail: "prerequisites is required when removalPolicy is \"after-actions\"",
		}
	}
	if isJSONNull(raw) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: "siteControl.presentationPowerOff.prerequisites", Detail: "prerequisites must not be null"}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "siteControl.presentationPowerOff.prerequisites", Detail: "prerequisites must be a JSON array"}
	}
	if len(items) == 0 {
		return nil, &ValidationError{
			Code: ValidationCodePrerequisitesEmpty, Field: "siteControl.presentationPowerOff.prerequisites",
			Detail: "prerequisites must contain at least one entry when removalPolicy is \"after-actions\"",
		}
	}
	if len(items) > nightPrerequisiteMaxCount {
		return nil, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "siteControl.presentationPowerOff.prerequisites",
			Detail: fmt.Sprintf("prerequisites must not exceed %d entries", nightPrerequisiteMaxCount),
		}
	}

	out := make([]NightPrerequisite, 0, len(items))
	for i, item := range items {
		field := fmt.Sprintf("siteControl.presentationPowerOff.prerequisites[%d]", i)
		p, verr := decodeNightPrerequisite(item, field, sessionShow, actionResolver, selfAction)
		if verr != nil {
			return nil, verr
		}
		out = append(out, p)
	}
	return out, nil
}

func decodeNightPrerequisite(raw json.RawMessage, field, sessionShow string, actionResolver ActionResolver, selfAction string) (NightPrerequisite, *ValidationError) {
	if isJSONNull(raw) {
		return NightPrerequisite{}, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return NightPrerequisite{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON object", field)}
	}
	if verr := rejectUnknownKeysUnder(fields, nightPrerequisiteKeys, field); verr != nil {
		return NightPrerequisite{}, verr
	}

	kind, verr := decodeRequiredEnum(fields, "kind", field+".kind", nightPrerequisiteKindValues)
	if verr != nil {
		return NightPrerequisite{}, verr
	}

	_, hasAction := fields["action"]
	_, hasConfirm := fields["requireConfirmation"]
	_, hasDelay := fields["delayMs"]

	if kind == NightPrerequisiteKindDelay {
		if hasAction || hasConfirm {
			return NightPrerequisite{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: "a \"delay\" prerequisite must not set action or requireConfirmation"}
		}
		delay, verr := decodeRequiredNonNegativeInt(fields, "delayMs", field+".delayMs")
		if verr != nil {
			return NightPrerequisite{}, verr
		}
		if delay > nightPrerequisiteMaxDelayMs {
			return NightPrerequisite{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field + ".delayMs", Detail: fmt.Sprintf("delayMs must not exceed %d", nightPrerequisiteMaxDelayMs)}
		}
		return NightPrerequisite{Kind: kind, DelayMs: delay}, nil
	}

	// "action" or "evidence".
	if hasDelay {
		return NightPrerequisite{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("a %q prerequisite must not set delayMs", kind)}
	}
	action, verr := decodeRequiredString(fields, "action", field+".action")
	if verr != nil {
		return NightPrerequisite{}, verr
	}
	if verr := validateNightSiteActionRef(action, field+".action", sessionShow, actionResolver); verr != nil {
		return NightPrerequisite{}, verr
	}
	if action == selfAction {
		return NightPrerequisite{}, &ValidationError{
			Code: ValidationCodePowerOffPrerequisiteCycle, Field: field + ".action",
			Detail: fmt.Sprintf("prerequisite action %q is this same presentation power-off binding's own action; a removal policy may not invoke itself", action),
		}
	}

	if kind == NightPrerequisiteKindEvidence {
		if hasConfirm {
			return NightPrerequisite{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field + ".requireConfirmation", Detail: "requireConfirmation only applies to an \"action\" prerequisite"}
		}
		return NightPrerequisite{Kind: kind, Action: action}, nil
	}

	requireConfirmation, verr := decodeDefaultedBool(fields, "requireConfirmation", field+".requireConfirmation", false)
	if verr != nil {
		return NightPrerequisite{}, verr
	}
	return NightPrerequisite{Kind: kind, Action: action, RequireConfirmation: requireConfirmation}, nil
}

// decodeRequiredBool reads key from top as a required, non-null JSON
// boolean: showaction.go's decodeDefaultedBool has a default and this
// deliberately does not, for immediateSafeAttestation, where a half-filled
// "immediate" policy (no explicit attestation) must be invalid rather than
// silently defaulting to either true or false (RESTING-MODE.md §10.2:
// "There is no default").
func decodeRequiredBool(top map[string]json.RawMessage, key, field string) (bool, *ValidationError) {
	raw, present := top[key]
	if !present {
		return false, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return false, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON boolean", field)}
	}
	return b, nil
}

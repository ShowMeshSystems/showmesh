package config

import (
	"encoding/json"
	"fmt"
)

// This file is the per-node kind (ADR-039, IDENTIFIER-REGISTER.md's
// "audio.node" reservation): which discovered output route carries
// program, which carries LTC, and the operator-declared clock domain (no
// software call proves two outputs share a hardware clock, so this is
// declared, never inferred). A collection, mirroring show.surface and
// show.action: the object id is the node id itself rather than a
// caller-chosen name.

// AudioNodeConfigKind is config_objects.kind and config_revisions.kind for
// an audio.node object.
const AudioNodeConfigKind = "audio.node"

// ValidateAudioNodeObjectID validates an audio.node object id against the
// same syntax a node id must satisfy — reusing [ValidateShowObjectID]'s own
// reuse of [mqttproto.ValidateNodeID] rather than a second copy of the
// pattern, because an audio.node object id IS a node id, not merely
// shaped like one.
func ValidateAudioNodeObjectID(id string) *ValidationError {
	return ValidateShowObjectID("node id", id)
}

var audioNodeTopLevelKeys = map[string]bool{
	"programRoute": true, "ltcRoute": true,
	"programChannels": true, "ltcChannel": true,
	"clockDomain": true, "clockDomainProvenance": true,
}

// AudioNodePayload is config_revisions.payload_json's decoded, VALIDATED
// shape for [AudioNodeConfigKind].
type AudioNodePayload struct {
	// ProgramRoute names the discovered output route (the agent's own
	// device identity — internal/agent/audio's RouteEvidence.Device, as
	// advertised in the node's audio.output.local capability) carrying
	// program audio.
	ProgramRoute string `json:"programRoute"`

	// LTCRoute names the discovered output route carrying LTC, or is
	// empty on a program-only node that emits no LTC at all (a
	// two-output interface has no channel to spare for it, and ADR-042
	// section 5 already treats LTC as losable without costing program
	// audio). Empty exactly when LTCChannel is zero: DecodeAudioNodePayload
	// refuses one without the other. ADR-018 requires LTC be a DISCRETE
	// channel, never mixed into program; whether a given route can supply
	// that is probe evidence (the audio.output.ltc capability), not
	// something this package can check on its own — see
	// [ValidateAudioNodePlacement]. Program and LTC leave through one
	// interface in one clock domain, so DecodeAudioNodePayload refuses a
	// non-empty value that differs from ProgramRoute.
	LTCRoute string `json:"ltcRoute,omitempty"`

	// ProgramChannels is the ordered, 1-based channel indices on
	// ProgramRoute carrying program audio: [1, 2] for reference stereo,
	// [1] for mono.
	ProgramChannels []int `json:"programChannels"`

	// LTCChannel is the 1-based channel index on LTCRoute carrying LTC,
	// or zero on a program-only node. Zero exactly when LTCRoute is
	// empty. Never a member of ProgramChannels — ADR-018 requires LTC on
	// a discrete channel, never mixed into program.
	LTCChannel int `json:"ltcChannel,omitempty"`

	// ClockDomain is the operator's own name for the shared hardware
	// clock ProgramRoute and LTCRoute are declared to share. Required
	// even on a program-only node, where it names the clock the program
	// route runs on: it is what a later LTC or multi-node alignment
	// question is answered against, and asking for it once at declaration
	// time is cheaper than inferring it afterwards. Never inferred: no
	// software call on this platform proves two outputs share a clock.
	ClockDomain string `json:"clockDomain"`

	// ClockDomainProvenance is the operator's stated reason for the
	// ClockDomain declaration (e.g. "single interface, both routes on it"
	// or "manufacturer datasheet states shared word clock") — required
	// for the identical reason ClockDomain itself is required: a
	// declaration with no stated basis is indistinguishable from a guess.
	ClockDomainProvenance string `json:"clockDomainProvenance"`
}

// EncodeAudioNodePayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeAudioNodePayload); this function does not re-validate.
func EncodeAudioNodePayload(p AudioNodePayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode audio.node payload: %w", err)
	}
	return string(b), nil
}

// DecodeAudioNodePayload parses and validates raw's STRUCTURE: every
// required field non-null and non-empty. "ltcRoute" and "ltcChannel" are
// the one OPTIONAL pair, and they are optional together: both absent
// declares a program-only node that emits no LTC, and one without the
// other is refused (see [decodeAudioNodeLTC]). It deliberately does NOT check
// ProgramRoute/LTCRoute against a node's advertised capabilities — that is
// probe evidence, fetched by the API layer, checked by
// [ValidateAudioNodePlacement], exactly the same split showsurface.go uses
// for its showExists/nodeDeclared callbacks (this package has no store
// access).
func DecodeAudioNodePayload(raw string) (AudioNodePayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return AudioNodePayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, audioNodeTopLevelKeys); verr != nil {
		return AudioNodePayload{}, verr
	}

	programRoute, verr := decodeRequiredString(top, "programRoute", "programRoute")
	if verr != nil {
		return AudioNodePayload{}, verr
	}
	programChannels, verr := decodeAudioNodeProgramChannels(top)
	if verr != nil {
		return AudioNodePayload{}, verr
	}
	ltcRoute, ltcChannel, verr := decodeAudioNodeLTC(top, programChannels)
	if verr != nil {
		return AudioNodePayload{}, verr
	}
	clockDomain, verr := decodeRequiredString(top, "clockDomain", "clockDomain")
	if verr != nil {
		return AudioNodePayload{}, verr
	}
	clockDomainProvenance, verr := decodeRequiredString(top, "clockDomainProvenance", "clockDomainProvenance")
	if verr != nil {
		return AudioNodePayload{}, verr
	}

	if ltcRoute != "" && programRoute != ltcRoute {
		return AudioNodePayload{}, &ValidationError{
			Code: ValidationCodeAudioNodeRouteMismatch, Field: "ltcRoute",
			Detail: fmt.Sprintf(
				"ltcRoute %q must name the same route as programRoute %q; program and LTC leave through one interface in one clock domain",
				ltcRoute, programRoute),
		}
	}

	return AudioNodePayload{
		ProgramRoute: programRoute, LTCRoute: ltcRoute,
		ProgramChannels: programChannels, LTCChannel: ltcChannel,
		ClockDomain: clockDomain, ClockDomainProvenance: clockDomainProvenance,
	}, nil
}

// decodeAudioNodeProgramChannels decodes and validates the required
// "programChannels" field: absent, explicit null, and an explicitly empty
// array are three distinct refusals (decodeRequiredIntList's own doc
// comment), and every element must be a distinct positive 1-based index.
func decodeAudioNodeProgramChannels(top map[string]json.RawMessage) ([]int, *ValidationError) {
	channels, verr := decodeRequiredIntList(top, "programChannels", "programChannels")
	if verr != nil {
		return nil, verr
	}
	seen := make(map[int]bool, len(channels))
	for i, ch := range channels {
		field := fmt.Sprintf("programChannels[%d]", i)
		if ch < 1 {
			return nil, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: field,
				Detail: fmt.Sprintf("%s must be a positive 1-based channel index", field),
			}
		}
		if seen[ch] {
			return nil, &ValidationError{
				Code: ValidationCodeAudioNodeChannelDuplicate, Field: field,
				Detail: fmt.Sprintf("channel index %d appears more than once in programChannels", ch),
			}
		}
		seen[ch] = true
	}
	return channels, nil
}

// decodeAudioNodeLTC decodes the optional "ltcRoute" and "ltcChannel"
// pair. Both absent declares a program-only node, which emits no LTC:
// a two-output interface has no discrete channel to carry it, and
// ADR-042 section 5 already treats losing LTC as costing timecode and
// never the audience's audio. Either one alone is refused rather than
// half-honoured, because a payload naming an LTC route with no channel
// (or the reverse) is an operator mistake with two plausible readings.
// When both are present the rules are unchanged: a positive 1-based
// index not already claimed by programChannels.
func decodeAudioNodeLTC(top map[string]json.RawMessage, programChannels []int) (string, int, *ValidationError) {
	_, haveRoute := top["ltcRoute"]
	_, haveChannel := top["ltcChannel"]
	switch {
	case !haveRoute && !haveChannel:
		return "", 0, nil
	case haveRoute && !haveChannel:
		return "", 0, &ValidationError{
			Code: ValidationCodeFieldRequired, Field: "ltcChannel",
			Detail: "ltcChannel is required when ltcRoute is given; omit both to declare a program-only node that emits no LTC",
		}
	case !haveRoute && haveChannel:
		return "", 0, &ValidationError{
			Code: ValidationCodeFieldRequired, Field: "ltcRoute",
			Detail: "ltcRoute is required when ltcChannel is given; omit both to declare a program-only node that emits no LTC",
		}
	}

	ltcRoute, verr := decodeRequiredString(top, "ltcRoute", "ltcRoute")
	if verr != nil {
		return "", 0, verr
	}
	ltcChannel, verr := decodeRequiredInt(top, "ltcChannel", "ltcChannel")
	if verr != nil {
		return "", 0, verr
	}
	if ltcChannel < 1 {
		return "", 0, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "ltcChannel",
			Detail: "ltcChannel must be a positive 1-based channel index",
		}
	}
	for _, ch := range programChannels {
		if ch == ltcChannel {
			return "", 0, &ValidationError{
				Code: ValidationCodeAudioNodeChannelOverlap, Field: "ltcChannel",
				Detail: fmt.Sprintf("ltcChannel %d also appears in programChannels; LTC must be on a channel discrete from program", ltcChannel),
			}
		}
	}
	return ltcRoute, ltcChannel, nil
}

// decodeRequiredIntList reads key from top as a required, non-null,
// non-empty JSON array of whole numbers. Absent, explicit null, and an
// explicitly empty array are three distinct refusals — the same rule this
// package enforces for every other required field, extended to arrays.
func decodeRequiredIntList(top map[string]json.RawMessage, key, field string) ([]int, *ValidationError) {
	raw, present := top[key]
	if !present {
		return nil, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var floats []float64
	if err := json.Unmarshal(raw, &floats); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON array of whole numbers", field)}
	}
	if len(floats) == 0 {
		return nil, &ValidationError{Code: ValidationCodeFieldEmpty, Field: field, Detail: fmt.Sprintf("%s must not be empty", field)}
	}
	out := make([]int, len(floats))
	for i, f := range floats {
		if f != float64(int(f)) {
			return nil, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: fmt.Sprintf("%s[%d]", field, i),
				Detail: fmt.Sprintf("%s[%d] must be a whole number", field, i),
			}
		}
		out[i] = int(f)
	}
	return out, nil
}

// ErrAudioNodeNoEvidence is [ValidateAudioNodePlacement]'s error when a
// node has advertised no audio capability at all — not "this route is
// wrong", but "this coordinator holds no probe evidence for this node's
// audio output at all", which is a different, more basic refusal.
var ErrAudioNodeNoEvidence = fmt.Errorf("audio.node: this node has advertised no audio output capability; " +
	"placement is refused against the node's own probe evidence, never against the operator's claim alone")

// ValidateAudioNodePlacement refuses placement of p against advertised
// probe evidence (audio.output.local / audio.output.ltc capability
// attributes' "routes" list, gathered by the API layer from the node's own
// Hello advertisement — this package has no store access): refused
// against what the node actually proved, never against what the operator
// typed.
//
// programRoutes and ltcRoutes are both nil/empty exactly when the node has
// advertised no usable audio route at all (or has never advertised
// anything), which is told apart from "advertised something, but not this
// route" ([ErrAudioNodeNoEvidence] vs. a named-route error) because an
// operator fixing a typo needs to know which case they are in.
//
// A program-only p (empty LTCRoute) is checked against programRoutes
// alone. That is what makes a two-output interface placeable: its
// ltcRoutes list is correctly empty, because ADR-018 needs a third
// channel beyond the program pair, so no LTC declaration could ever
// pass. Every refusal below still applies unchanged to a p that DOES
// declare LTC, whatever the node advertises.
func ValidateAudioNodePlacement(p AudioNodePayload, programRoutes, ltcRoutes []string) error {
	if len(programRoutes) == 0 && len(ltcRoutes) == 0 {
		return ErrAudioNodeNoEvidence
	}
	if !containsString(programRoutes, p.ProgramRoute) {
		return fmt.Errorf("audio.node: programRoute %q is not among this node's advertised program-capable routes %v",
			p.ProgramRoute, programRoutes)
	}
	if p.LTCRoute == "" {
		// A program-only node declares no LTC route, so there is nothing
		// to check it against. This is the only path by which a node
		// whose every route is two-channel can be placed at all.
		return nil
	}
	if !containsString(ltcRoutes, p.LTCRoute) {
		return fmt.Errorf("audio.node: ltcRoute %q is not among this node's advertised discrete LTC-capable routes %v "+
			"(a route needs at least a third channel beyond the program pair to carry LTC per ADR-018)",
			p.LTCRoute, ltcRoutes)
	}
	return nil
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

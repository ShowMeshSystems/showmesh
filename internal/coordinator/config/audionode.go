package config

import (
	"encoding/json"
	"fmt"
	"time"
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

// The three members of audio.node.role (ADR-045 decision c): "program"
// plays program audio only; "program+ltc" plays program audio and is this
// installation's sole LTC emitter (ADR-018's one clock domain, enforced by
// [ValidateAudioNodeRoleUniqueness]); "zone" plays an independent local
// speaker zone, never program or LTC.
const (
	AudioNodeRoleProgram    = "program"
	AudioNodeRoleProgramLTC = "program+ltc"
	AudioNodeRoleZone       = "zone"

	// AudioNodeRoleDefault is used whenever a payload omits "role" —
	// ADR-045 is additive, so a pre-ADR-045 audio.node object (there was
	// only ever one per installation, and it always carried both program
	// and LTC) decodes to the role that describes what it already was,
	// rather than requiring every existing installation to be re-written
	// the day this ships.
	AudioNodeRoleDefault = AudioNodeRoleProgramLTC
)

var audioNodeRoles = map[string]bool{
	AudioNodeRoleProgram:    true,
	AudioNodeRoleProgramLTC: true,
	AudioNodeRoleZone:       true,
}

// The four members of outputLatency.method (RES-019 section 8).
// "unmeasured" is the default and the only member that must carry no
// other outputLatency field: see [decodeAudioNodeOutputLatency].
const (
	OutputLatencyMethodUnmeasured = "unmeasured"
	OutputLatencyMethodLoopback   = "loopback"
	OutputLatencyMethodAcoustic   = "acoustic"
	OutputLatencyMethodDeclared   = "declared"
)

var outputLatencyMethods = map[string]bool{
	OutputLatencyMethodUnmeasured: true,
	OutputLatencyMethodLoopback:   true,
	OutputLatencyMethodAcoustic:   true,
	OutputLatencyMethodDeclared:   true,
}

// outputLatencyBoundUs sanity-bounds outputLatency.valueUs against a typo:
// RES-019 section 8's own measurement moved from about 53 ms to about
// 81 ms across a 4x buffer-quantum change, so a bound of one full second
// comfortably covers any plausible output chain while still catching a
// units mistake, such as milliseconds entered where microseconds were
// asked for.
const outputLatencyBoundUs = 1_000_000

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
	"role": true, "zone": true,
	"outputLatency": true,
}

var outputLatencyTopLevelKeys = map[string]bool{
	"valueUs": true, "measuredAt": true, "method": true,
	"reference": true, "confidence": true, "configuration": true,
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

	// Role is ADR-045's audio.node role: one of [AudioNodeRoleProgram],
	// [AudioNodeRoleProgramLTC], or [AudioNodeRoleZone]. Optional on the
	// wire; absent decodes to [AudioNodeRoleDefault]
	// ("program+ltc") so every pre-ADR-045 payload keeps decoding
	// unchanged. At most one audio.node across the installation may carry
	// "program+ltc" — ADR-018's one clock domain means one LTC emitter —
	// enforced by [ValidateAudioNodeRoleUniqueness], not by this decode
	// step, because it is a cross-object rule and this package has no
	// store access. omitempty: many callers across this codebase construct
	// an AudioNodePayload literal directly (test fixtures, mostly) without
	// setting Role at all — encoding that zero value must produce the same
	// wire shape as a payload that genuinely never mentioned "role", so it
	// decodes back to [AudioNodeRoleDefault] rather than to a rejected
	// present-but-empty string.
	Role string `json:"role,omitempty"`

	// Zone is the operator's own name for the independent speaker zone
	// this node drives, meaningful only when Role is
	// [AudioNodeRoleZone]. nil for every other role — refused otherwise
	// at decode time, matching show.cue's outputs.announcement.duckGainDb
	// precedent: an ignored field would read as an applied one.
	Zone *string `json:"zone,omitempty"`

	// OutputLatency is this node's calibrated static output-chain delay
	// (RES-019 section 8). Optional on the wire: absent decodes to
	// [OutputLatencyPayload]'s zero value, method "unmeasured".
	OutputLatency OutputLatencyPayload `json:"outputLatency"`
}

// OutputLatencyPayload is audio.node.outputLatency (RES-019 section 8): a
// signed per-output offset, in microseconds, subtracted from that node's
// scheduled start instant. See [decodeAudioNodeOutputLatency] for the
// validation this shape is held to.
type OutputLatencyPayload struct {
	// ValueUs is the offset itself, in signed microseconds. Meaningless,
	// and never set on the wire, when Method is
	// [OutputLatencyMethodUnmeasured].
	ValueUs int `json:"valueUs,omitempty"`

	// Method is one of [OutputLatencyMethodUnmeasured] (the default),
	// [OutputLatencyMethodLoopback], [OutputLatencyMethodAcoustic], or
	// [OutputLatencyMethodDeclared]. See [decodeAudioNodeOutputLatency].
	Method string `json:"method,omitempty"`

	// MeasuredAt is when ValueUs was taken. RES-019 section 8.2 covers
	// re-measurement.
	MeasuredAt *time.Time `json:"measuredAt,omitempty"`

	// Reference names what ValueUs was measured against: the other
	// node/signal in a loopback or acoustic capture, or the datasheet
	// section for a declared value.
	Reference string `json:"reference,omitempty"`

	// Confidence is the operator's own free-text judgment of how much to
	// trust ValueUs. Free text, not a closed vocabulary, matching
	// ClockDomainProvenance's own precedent.
	Confidence string `json:"confidence,omitempty"`

	// Configuration records the buffer/quantum/sample-rate configuration
	// ValueUs was measured under: RES-019 section 8.4 explains why a
	// value is only valid for the configuration it was measured under.
	Configuration string `json:"configuration,omitempty"`
}

// EncodeAudioNodePayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeAudioNodePayload); this function does not re-validate. A
// zero-value p.OutputLatency (a caller that never set it) is normalized
// to method "unmeasured" so it re-decodes, matching what an absent
// "outputLatency" key already decodes to.
func EncodeAudioNodePayload(p AudioNodePayload) (string, error) {
	if p.OutputLatency.Method == "" {
		p.OutputLatency.Method = OutputLatencyMethodUnmeasured
	}
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

	role, verr := decodeDefaultedEnum(top, "role", "role", AudioNodeRoleDefault, audioNodeRoles)
	if verr != nil {
		return AudioNodePayload{}, verr
	}

	var zone *string
	if raw, present := top["zone"]; present {
		if role != AudioNodeRoleZone {
			return AudioNodePayload{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "zone",
				Detail: fmt.Sprintf("zone must be absent unless role is %q: an ignored field would read as an applied one", AudioNodeRoleZone),
			}
		}
		if isJSONNull(raw) {
			return AudioNodePayload{}, &ValidationError{Code: ValidationCodeFieldNull, Field: "zone", Detail: "zone must not be null; omit it to leave it unset"}
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return AudioNodePayload{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "zone", Detail: "zone must be a string"}
		}
		if s == "" {
			return AudioNodePayload{}, &ValidationError{Code: ValidationCodeFieldEmpty, Field: "zone", Detail: "zone must not be an empty string"}
		}
		zone = &s
	}

	outputLatency, verr := decodeAudioNodeOutputLatency(top)
	if verr != nil {
		return AudioNodePayload{}, verr
	}

	return AudioNodePayload{
		ProgramRoute: programRoute, LTCRoute: ltcRoute,
		ProgramChannels: programChannels, LTCChannel: ltcChannel,
		ClockDomain: clockDomain, ClockDomainProvenance: clockDomainProvenance,
		Role: role, Zone: zone,
		OutputLatency: outputLatency,
	}, nil
}

// decodeAudioNodeOutputLatency decodes the optional "outputLatency"
// object (RES-019 section 8). Absent decodes to the zero
// [OutputLatencyPayload], method "unmeasured".
func decodeAudioNodeOutputLatency(top map[string]json.RawMessage) (OutputLatencyPayload, *ValidationError) {
	raw, present := top["outputLatency"]
	if !present {
		return OutputLatencyPayload{Method: OutputLatencyMethodUnmeasured}, nil
	}
	fields, verr := decodeRequiredObjectFromRaw(raw, "outputLatency")
	if verr != nil {
		return OutputLatencyPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(fields, outputLatencyTopLevelKeys); verr != nil {
		return OutputLatencyPayload{}, verr
	}

	// An absent method reads as unmeasured rather than a decode error: a
	// row written before this normalization existed stored
	// "outputLatency":{} with no method, and this keeps that row
	// readable without weakening what a client is required to send.
	method := OutputLatencyMethodUnmeasured
	if _, present := fields["method"]; present {
		var verr *ValidationError
		method, verr = decodeRequiredEnum(fields, "method", "outputLatency.method", outputLatencyMethods)
		if verr != nil {
			return OutputLatencyPayload{}, verr
		}
	}

	if method == OutputLatencyMethodUnmeasured {
		for _, key := range []string{"valueUs", "measuredAt", "reference", "confidence", "configuration"} {
			if _, present := fields[key]; present {
				return OutputLatencyPayload{}, &ValidationError{
					Code: ValidationCodeFieldInvalid, Field: "outputLatency." + key,
					Detail: fmt.Sprintf("outputLatency.%s must be absent when method is %q; a value beside the default method would be a fabricated measurement", key, OutputLatencyMethodUnmeasured),
				}
			}
		}
		return OutputLatencyPayload{Method: OutputLatencyMethodUnmeasured}, nil
	}

	valueUs, verr := decodeRequiredInt(fields, "valueUs", "outputLatency.valueUs")
	if verr != nil {
		return OutputLatencyPayload{}, verr
	}
	if valueUs < -outputLatencyBoundUs || valueUs > outputLatencyBoundUs {
		return OutputLatencyPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "outputLatency.valueUs",
			Detail: fmt.Sprintf("outputLatency.valueUs must be within +/-%d microseconds of a plausible output delay", outputLatencyBoundUs),
		}
	}
	if valueUs == 0 {
		return OutputLatencyPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "outputLatency.valueUs",
			Detail: "outputLatency.valueUs must not be zero for a measured method; no real output chain has zero delay",
		}
	}
	measuredAt, verr := decodeRequiredTime(fields, "measuredAt", "outputLatency.measuredAt")
	if verr != nil {
		return OutputLatencyPayload{}, verr
	}
	reference, verr := decodeRequiredString(fields, "reference", "outputLatency.reference")
	if verr != nil {
		return OutputLatencyPayload{}, verr
	}
	confidence, verr := decodeRequiredString(fields, "confidence", "outputLatency.confidence")
	if verr != nil {
		return OutputLatencyPayload{}, verr
	}
	configuration, verr := decodeRequiredString(fields, "configuration", "outputLatency.configuration")
	if verr != nil {
		return OutputLatencyPayload{}, verr
	}
	return OutputLatencyPayload{
		ValueUs: valueUs, Method: method, MeasuredAt: &measuredAt,
		Reference: reference, Confidence: confidence, Configuration: configuration,
	}, nil
}

// decodeRequiredTime reads key from top as a required, non-null,
// non-empty RFC 3339 timestamp: the one timestamp field this package
// decodes, so it gets its own helper rather than a generic one.
func decodeRequiredTime(top map[string]json.RawMessage, key, field string) (time.Time, *ValidationError) {
	s, verr := decodeRequiredString(top, key, field)
	if verr != nil {
		return time.Time{}, verr
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be an RFC 3339 timestamp", field)}
	}
	return t, nil
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

// ValidateAudioNodeRoleUniqueness refuses id's write when p.Role is
// "program+ltc" and a DIFFERENT existing audio.node object already carries
// that role (ADR-045 decision b/ADR-018: exactly one LTC emitter per
// installation). existingRoles is every OTHER currently-configured
// audio.node id mapped to its own stored role — caller-supplied (this
// package has no store access), and MUST already exclude id itself so a
// no-op re-write of the sole program+ltc node does not refuse against its
// own prior revision. The error names BOTH node ids so the operator knows
// which one to change.
func ValidateAudioNodeRoleUniqueness(id string, p AudioNodePayload, existingRoles map[string]string) error {
	if p.Role != AudioNodeRoleProgramLTC {
		return nil
	}
	for otherID, otherRole := range existingRoles {
		if otherID == id {
			continue
		}
		if otherRole == AudioNodeRoleProgramLTC {
			return fmt.Errorf(
				"audio.node: %q and %q would both carry role %q; exactly one audio.node may be the installation's program+ltc node (ADR-018's one clock domain, ADR-045)",
				id, otherID, AudioNodeRoleProgramLTC)
		}
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

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

	// LTCRoute names the discovered output route carrying LTC. ADR-018
	// requires it be a DISCRETE channel, never mixed into program;
	// whether a given route can supply that is probe evidence (the
	// audio.output.ltc capability), not something this package can check
	// on its own — see [ValidateAudioNodePlacement].
	LTCRoute string `json:"ltcRoute"`

	// ClockDomain is the operator's own name for the shared hardware
	// clock ProgramRoute and LTCRoute are declared to share. Never
	// inferred: no software call on this platform proves two outputs
	// share a clock.
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

// DecodeAudioNodePayload parses and validates raw's STRUCTURE: every field
// required, non-null, non-empty. It deliberately does NOT check
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
	ltcRoute, verr := decodeRequiredString(top, "ltcRoute", "ltcRoute")
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

	return AudioNodePayload{
		ProgramRoute: programRoute, LTCRoute: ltcRoute,
		ClockDomain: clockDomain, ClockDomainProvenance: clockDomainProvenance,
	}, nil
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
func ValidateAudioNodePlacement(p AudioNodePayload, programRoutes, ltcRoutes []string) error {
	if len(programRoutes) == 0 && len(ltcRoutes) == 0 {
		return ErrAudioNodeNoEvidence
	}
	if !containsString(programRoutes, p.ProgramRoute) {
		return fmt.Errorf("audio.node: programRoute %q is not among this node's advertised program-capable routes %v",
			p.ProgramRoute, programRoutes)
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

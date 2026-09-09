package config

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// ShowConfigKind is config_objects.kind and config_revisions.kind for a
// show object (ADR-027 decision 2: a Show is a namespace, not a
// container — this payload carries no list of surfaces, actions, or
// macros). See showaction.go's own top doc comment for why this kind is
// hand-written rather than built on a generic kind registry.
const ShowConfigKind = "show"

// maxShowNameRunes and maxShowNotesRunes bound the two fields of
// [ShowPayload]. Chosen as generous, round sanity bounds — not a limit
// this project has verified against anything downstream.
const (
	maxShowNameRunes  = 200
	maxShowNotesRunes = 4000
)

// showTopLevelKeys is the complete set of keys DecodeShowPayload
// recognizes — see rejectUnknownTopLevelKeys (showaction.go).
var showTopLevelKeys = map[string]bool{
	"name": true, "notes": true, "fppInstances": true, "resolumeInstances": true,
}

// ShowPayload is config_revisions.payload_json's decoded, VALIDATED shape
// for [ShowConfigKind]. A PUT of this payload is a full replacement: an
// absent "notes" means notes is empty, never "leave the previous value",
// because this store keeps no per-field carry-forward across revisions.
//
// FPPInstances and ResolumeInstances are the deliberate EXCEPTION to the
// sentence above, and the exception is the whole point of the fields:
// participation is selected by hand, never detected, so absent has to mean
// "nobody has chosen yet" rather than "empty". Each is a pointer to a
// slice because that is the only representation that keeps three states
// apart on the wire and in Go: nil is absent, a non-nil pointer to a
// zero-length slice is the operator's explicit "no instance of this
// integration takes part", and a populated slice is a selection. A plain
// []string would collapse the first two, every show that predates this
// field would read as "nothing takes part", and a consumer checking
// participation would go quietly green on an unconfigured show. Ask
// [ShowPayload.ParticipationUnconfigured] rather than testing the pointers
// at each call site.
type ShowPayload struct {
	Name              string    `json:"name"`
	Notes             string    `json:"notes"`
	FPPInstances      *[]string `json:"fppInstances,omitempty"`
	ResolumeInstances *[]string `json:"resolumeInstances,omitempty"`
}

// ParticipationUnconfigured reports that NEITHER integration has a
// participation selection recorded for this show: nobody has yet said
// which FPP hosts and which Resolume instances take part. Every show
// written before participation existed answers true here, and a consumer
// must be able to say that out loud instead of reading it as "nothing
// takes part", which would pass every check it was meant to fail.
func (p ShowPayload) ParticipationUnconfigured() bool {
	return p.FPPInstances == nil && p.ResolumeInstances == nil
}

// FPPParticipation returns the FPP instance ids selected for this show and
// whether a selection was ever recorded. selected false means absent, never
// "selected nothing"; selected true with no ids is the operator's explicit
// choice that no FPP host takes part.
func (p ShowPayload) FPPParticipation() (ids []string, selected bool) {
	if p.FPPInstances == nil {
		return nil, false
	}
	return *p.FPPInstances, true
}

// ResolumeParticipation is [ShowPayload.FPPParticipation] for Resolume. An
// explicitly empty selection here is an ordinary night with no projection,
// not a misconfiguration.
func (p ShowPayload) ResolumeParticipation() (ids []string, selected bool) {
	if p.ResolumeInstances == nil {
		return nil, false
	}
	return *p.ResolumeInstances, true
}

// ValidateShowObjectID checks the config_objects id a show or surface is
// being written under. It is the same rule a "show" REFERENCE is held to,
// so a write cannot mint an object whose id no other object could ever
// name: without it, a show created as "Halloween 2026" stores fine and
// then rejects every surface that tries to point at it.
func ValidateShowObjectID(field, id string) *ValidationError {
	if err := mqttproto.ValidateNodeID(id); err != nil {
		return &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: field,
			Detail: fmt.Sprintf("%s must be 1-64 characters of lowercase letters, digits, and hyphens, and must not start or end with a hyphen", field),
		}
	}
	return nil
}

// EncodeShowPayload marshals p into config_revisions.payload_json's column
// shape. p is assumed already valid (the product of DecodeShowPayload);
// this function does not re-validate.
func EncodeShowPayload(p ShowPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show payload: %w", err)
	}
	return string(b), nil
}

// DecodeShowPayload parses and validates raw. name is required, non-empty,
// and at most [maxShowNameRunes] runes. notes is optional: absent or an
// explicit empty string both decode to "", and null is rejected like every
// other optional string field in this package (see decodeOptionalString).
// fppInstances and resolumeInstances are optional and, unlike notes, keep
// absent and explicitly empty apart - see [ShowPayload]'s own doc comment.
func DecodeShowPayload(raw string) (ShowPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return ShowPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, showTopLevelKeys); verr != nil {
		return ShowPayload{}, verr
	}

	name, verr := decodeRequiredString(top, "name", "name")
	if verr != nil {
		return ShowPayload{}, verr
	}
	if utf8.RuneCountInString(name) > maxShowNameRunes {
		return ShowPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "name",
			Detail: fmt.Sprintf("name must be %d characters or fewer", maxShowNameRunes),
		}
	}

	notes, verr := decodeOptionalString(top, "notes", "notes")
	if verr != nil {
		return ShowPayload{}, verr
	}
	if utf8.RuneCountInString(notes) > maxShowNotesRunes {
		return ShowPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "notes",
			Detail: fmt.Sprintf("notes must be %d characters or fewer", maxShowNotesRunes),
		}
	}

	fppInstances, verr := decodeShowInstanceSelection(top, "fppInstances")
	if verr != nil {
		return ShowPayload{}, verr
	}
	resolumeInstances, verr := decodeShowInstanceSelection(top, "resolumeInstances")
	if verr != nil {
		return ShowPayload{}, verr
	}

	return ShowPayload{
		Name: name, Notes: notes,
		FPPInstances: fppInstances, ResolumeInstances: resolumeInstances,
	}, nil
}

// decodeShowInstanceSelection reads one participation list: absent stays
// absent (nil), a present array is validated entry by entry against the
// same instance-id syntax fpp.endpoints and resolume.instances hold their
// own ids to, and a repeated id is REFUSED rather than deduplicated. A
// silent deduplication would accept a selection the operator did not type
// and hide the typo that produced it.
func decodeShowInstanceSelection(top map[string]json.RawMessage, key string) (*[]string, *ValidationError) {
	ids, verr := decodeOptionalStringList(top, key, key)
	if verr != nil || ids == nil {
		return nil, verr
	}
	seen := make(map[string]bool, len(*ids))
	for i, id := range *ids {
		field := fmt.Sprintf("%s[%d]", key, i)
		if err := mqttproto.ValidateNodeID(id); err != nil {
			return nil, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: field,
				Detail: fmt.Sprintf("%s must be 1-64 characters of lowercase letters, digits, and hyphens, and must not start or end with a hyphen", field),
			}
		}
		if seen[id] {
			return nil, &ValidationError{
				Code: ValidationCodeInstanceIDDuplicate, Field: field,
				Detail: fmt.Sprintf("%s repeats instance id %q; list each instance once", field, id),
			}
		}
		seen[id] = true
	}
	return ids, nil
}

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
var showTopLevelKeys = map[string]bool{"name": true, "notes": true}

// ShowPayload is config_revisions.payload_json's decoded, VALIDATED shape
// for [ShowConfigKind]. A PUT of this payload is a full replacement: an
// absent "notes" means notes is empty, never "leave the previous value",
// because this store keeps no per-field carry-forward across revisions.
type ShowPayload struct {
	Name  string `json:"name"`
	Notes string `json:"notes"`
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

	return ShowPayload{Name: name, Notes: notes}, nil
}

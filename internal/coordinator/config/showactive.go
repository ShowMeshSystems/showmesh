package config

import (
	"encoding/json"
	"fmt"
)

// ShowActiveConfigKind is config_objects.kind and config_revisions.kind for
// the show.active singleton.
const ShowActiveConfigKind = "show.active"

// ShowActiveObjectID is the config_objects id the active-show pointer is
// ALWAYS stored under — a fixed constant, never derived from any
// configuration value. Mirrors
// internal/coordinator/api/resolumecomposition.go's
// resolumeCompositionObjectIDConst: deriving a singleton's id from a
// value an operator can rename (e.g. the active show's own id) would
// orphan every stored revision the moment that value changed, which is
// the same "manufacturing absence from a rename" defect this project has
// caught before.
const ShowActiveObjectID = "active"

// showActiveTopLevelKeys is the complete set of keys
// DecodeShowActivePayload recognizes at the top level of the request body.
var showActiveTopLevelKeys = map[string]bool{"show": true}

// ShowActivePayload is config_revisions.payload_json's decoded, VALIDATED
// shape for [ShowActiveConfigKind]. The active show is configuration,
// revisioned and audited like any other kind (ADR-027 decision 3), so that
// programming Christmas cannot accidentally break Halloween.
type ShowActivePayload struct {
	Show string `json:"show"`
}

// EncodeShowActivePayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeShowActivePayload); this function does not re-validate.
func EncodeShowActivePayload(p ShowActivePayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.active payload: %w", err)
	}
	return string(b), nil
}

// DecodeShowActivePayload parses and validates raw. showExists reports
// whether "show" names an existing show config object — caller-supplied,
// like every cross-object reference check in this package (see
// showsurface.go).
func DecodeShowActivePayload(raw string, showExists func(string) bool) (ShowActivePayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return ShowActivePayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, showActiveTopLevelKeys); verr != nil {
		return ShowActivePayload{}, verr
	}

	show, verr := decodeRequiredString(top, "show", "show")
	if verr != nil {
		return ShowActivePayload{}, verr
	}
	if verr := validateShowRef(show); verr != nil {
		return ShowActivePayload{}, verr
	}
	if !showExists(show) {
		return ShowActivePayload{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "show",
			Detail: fmt.Sprintf("show %q is not a configured show", show),
		}
	}

	return ShowActivePayload{Show: show}, nil
}

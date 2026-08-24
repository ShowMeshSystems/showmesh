package config

import (
	"encoding/json"
	"fmt"
)

// This file is ADR-033's installation-wide operating mode as a
// configuration kind. One value for the whole installation, held with the
// revision and audit semantics every other kind already has: not per-node,
// not per-device, and never a per-subsystem flag.
//
// Singleton, on rendersettings.go's shape: one config object id, a
// well-defined default so GET never 404s, PUT is a full replacement. No
// environment variable ever backed this value, so there is no ADR-039
// decision 3 migration to write.

const (
	// ShowModeConfigKind is config_objects.kind and config_revisions.kind
	// for this object, and the second path segment of GET/PUT
	// /api/v1/config/show.mode.
	ShowModeConfigKind = "show.mode"

	// ShowModeConfigObjectID is the single config_objects.id this kind ever
	// uses. A fixed literal, never derived from any configuration value,
	// for showactive.go's ShowActiveObjectID reason.
	ShowModeConfigObjectID = "default"

	// ShowModeSourceAPI is this kind's only config_revisions.source value:
	// no environment variable ever backed the mode (ADR-039 decision 2 - an
	// operating mode an operator changes during a show is the opposite of a
	// start-time value), so there is nothing to migrate.
	ShowModeSourceAPI = "api"
)

// The two members of ADR-033's closed mode vocabulary. The wire value and
// the operator-facing label are the same word, deliberately (ADR-033
// decision 1): two names for one state is a drift the API contract cannot
// enforce.
const (
	ShowModeProgram = "program"
	ShowModeShow    = "show"
)

// showModes is the closed enum as a map, mirroring renderIdleOutputs.
// Additional members require an amendment to ADR-033.
var showModes = map[string]bool{
	ShowModeProgram: true,
	ShowModeShow:    true,
}

// ShowModeDefault is the mode reported when nothing has ever been written.
//
// Program, by owner ruling: a fresh install is by definition being set up,
// and black in front of someone programming is worse than useless.
//
// This is NOT ADR-033 decision 5's "unknown", and the two must never be
// collapsed. "Never been set" is this constant, a coordinator answering
// with a value it knows. "Cannot be read" is "unknown", which behaves as
// show and is a node-side held-value state (internal/agent/showmode.go)
// that no operator can ever write.
const ShowModeDefault = ShowModeProgram

// ShowModePayload is config_revisions.payload_json's decoded, VALIDATED
// shape for [ShowModeConfigKind].
type ShowModePayload struct {
	Mode string `json:"mode"`
}

// ShowModeDefaultPayload is the value reported when nothing has ever been
// written for this kind.
var ShowModeDefaultPayload = ShowModePayload{Mode: ShowModeDefault}

// showModeTopLevelKeys is the complete set of keys DecodeShowModePayload
// recognizes at the top level of the request body.
var showModeTopLevelKeys = map[string]bool{"mode": true}

// ValidShowMode reports whether mode is a member of ADR-033's closed
// vocabulary. Exported because the mode travels beyond this package, to
// nodes over the control plane, and every receiver has to be able to
// refuse a value that is not one of the two.
func ValidShowMode(mode string) bool { return showModes[mode] }

// EncodeShowModePayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeShowModePayload); this function does not re-validate.
func EncodeShowModePayload(p ShowModePayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.mode payload: %w", err)
	}
	return string(b), nil
}

// DecodeShowModePayload parses and validates raw. PUT is a full
// replacement: "mode" is required on every write, so an absent key is
// refused by name rather than silently defaulted - the same rule
// DecodeRenderSettingsPayload's doc comment argues at length. A reader of
// the STORED value on an object nothing has ever configured gets
// [ShowModeDefaultPayload] instead, through a distinct code path.
func DecodeShowModePayload(raw string) (ShowModePayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return ShowModePayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, showModeTopLevelKeys); verr != nil {
		return ShowModePayload{}, verr
	}

	mode, verr := decodeRequiredString(top, "mode", "mode")
	if verr != nil {
		return ShowModePayload{}, verr
	}
	if !ValidShowMode(mode) {
		return ShowModePayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "mode",
			Detail: "mode must be one of program or show",
		}
	}

	return ShowModePayload{Mode: mode}, nil
}

package config

import (
	"encoding/json"
	"fmt"
)

// The installation-wide emergency-stop follow-up configuration.
// Three trigger levels exist (POST-only, built in internal/coordinator/api;
// nothing here decides what a level DOES) and every level stops playout
// immediately; this kind holds only each level's OPTIONAL, best-effort
// follow-up actions — an ordered list of existing show.action ids to
// invoke after the stop, never a new place to author an action's own
// target. The shared show.action pool (already namespace-scoped and
// validated) with per-level selection here, rather than an independently
// authored list per level, is the deliberate choice: the same "worklights
// on" action is reusable across levels instead of being redefined three
// times with three chances to drift.
//
// Singleton, on showmode.go's own shape: one config object id, a
// well-defined default (every level empty) so GET never 404s, PUT is a
// full replacement. No environment variable ever backed this value.

const (
	// ShowEmergencyStopConfigKind is config_objects.kind and
	// config_revisions.kind for this object, and the second path segment
	// of GET/PUT /api/v1/config/show.emergencystop.
	ShowEmergencyStopConfigKind = "show.emergencystop"

	// ShowEmergencyStopConfigObjectID is the single config_objects.id this
	// kind ever uses, on ShowModeConfigObjectID's own precedent.
	ShowEmergencyStopConfigObjectID = "default"

	// ShowEmergencyStopSourceAPI is this kind's only config_revisions.source
	// value: nothing ever backed it as a start-time environment variable.
	ShowEmergencyStopSourceAPI = "api"
)

// emergencyStopActionsMaxCount bounds one level's own actions list, on
// nightPrerequisiteMaxCount's identical rationale: each configured action
// is dispatched serially and live, so an unbounded list makes one
// emergency-stop request's own dispatch time unbounded in practice.
const emergencyStopActionsMaxCount = 32

// EmergencyStopLevelConfig is one level's own optional, ordered follow-up
// action list — show.action ids, invoked best-effort, in this order,
// after that level's own immediate stop.
type EmergencyStopLevelConfig struct {
	Actions []string `json:"actions"`
}

// EmergencyStopPayload is config_revisions.payload_json's decoded,
// VALIDATED shape for [ShowEmergencyStopConfigKind]. Field names deliberately
// avoid this kind's own internal level names (see the three trigger routes'
// own doc comments in internal/coordinator/api): "stop", "stopPowerDown"
// and "hardStop" name the SAME three levels, spelled to match this build's
// audit-action and URL-path spellings exactly, so a reader correlating a
// PUT body against an audit entry or a route never has to translate
// between three different vocabularies for one level.
type EmergencyStopPayload struct {
	Stop          EmergencyStopLevelConfig `json:"stop"`
	StopPowerDown EmergencyStopLevelConfig `json:"stopPowerDown"`
	HardStop      EmergencyStopLevelConfig `json:"hardStop"`
}

// EmergencyStopDefaultPayload is the value reported when nothing has ever
// been written for this kind: every level configured with no follow-up
// actions, which — per this kind's own opening rule — must work exactly as
// well as a fully configured one.
var EmergencyStopDefaultPayload = EmergencyStopPayload{}

// emergencyStopTopLevelKeys is the complete set of keys
// DecodeEmergencyStopPayload recognizes at the top level of the request
// body.
var emergencyStopTopLevelKeys = map[string]bool{"stop": true, "stopPowerDown": true, "hardStop": true}

// emergencyStopLevelKeys is the complete set of keys one level object
// recognizes.
var emergencyStopLevelKeys = map[string]bool{"actions": true}

// EmergencyStopActionResolver reports whether actionID names an existing
// show.action object, of any show: an emergency stop is installation-wide,
// never scoped to whichever show happens to be active, so — unlike
// [ActionResolver] — this checks existence only, not show membership. The
// caller supplies it (this package has no store access, mirroring
// [ActionResolver] and [InterlockSignalResolver] one field over).
type EmergencyStopActionResolver func(actionID string) bool

// EncodeEmergencyStopPayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeEmergencyStopPayload); this function does not re-validate.
func EncodeEmergencyStopPayload(p EmergencyStopPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.emergencystop payload: %w", err)
	}
	return string(b), nil
}

// DecodeEmergencyStopPayload parses and validates raw. PUT is a full
// replacement: "stop", "stopPowerDown" and "hardStop" are all required on
// every write, each with its own required (possibly empty) "actions" list —
// the identical "absent key is refused by name, empty is a real configured
// value" rule ConfigFPPEndpointsPayload.endpoints and ShowModePayload.mode
// both already state for their own kind.
func DecodeEmergencyStopPayload(raw string, actionResolver EmergencyStopActionResolver) (EmergencyStopPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return EmergencyStopPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, emergencyStopTopLevelKeys); verr != nil {
		return EmergencyStopPayload{}, verr
	}

	stop, verr := decodeEmergencyStopLevel(top, "stop", actionResolver)
	if verr != nil {
		return EmergencyStopPayload{}, verr
	}
	stopPowerDown, verr := decodeEmergencyStopLevel(top, "stopPowerDown", actionResolver)
	if verr != nil {
		return EmergencyStopPayload{}, verr
	}
	hardStop, verr := decodeEmergencyStopLevel(top, "hardStop", actionResolver)
	if verr != nil {
		return EmergencyStopPayload{}, verr
	}

	return EmergencyStopPayload{Stop: stop, StopPowerDown: stopPowerDown, HardStop: hardStop}, nil
}

func decodeEmergencyStopLevel(top map[string]json.RawMessage, field string, actionResolver EmergencyStopActionResolver) (EmergencyStopLevelConfig, *ValidationError) {
	raw, present := top[field]
	if !present {
		return EmergencyStopLevelConfig{}, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: field + " is required"}
	}
	if isJSONNull(raw) {
		return EmergencyStopLevelConfig{}, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: field + " must not be null"}
	}
	var levelTop map[string]json.RawMessage
	if err := json.Unmarshal(raw, &levelTop); err != nil {
		return EmergencyStopLevelConfig{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: field + " must be a JSON object"}
	}
	if verr := rejectUnknownTopLevelKeys(levelTop, emergencyStopLevelKeys); verr != nil {
		return EmergencyStopLevelConfig{}, &ValidationError{Code: verr.Code, Field: field, Detail: field + ": " + verr.Detail}
	}

	actionsField := field + ".actions"
	actionsRaw, present := levelTop["actions"]
	if !present {
		return EmergencyStopLevelConfig{}, &ValidationError{Code: ValidationCodeFieldRequired, Field: actionsField, Detail: actionsField + " is required"}
	}
	if isJSONNull(actionsRaw) {
		return EmergencyStopLevelConfig{}, &ValidationError{Code: ValidationCodeFieldNull, Field: actionsField, Detail: actionsField + " must not be null; an empty list is how \"no follow-up actions\" is configured"}
	}
	var actions []string
	if err := json.Unmarshal(actionsRaw, &actions); err != nil {
		return EmergencyStopLevelConfig{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: actionsField, Detail: actionsField + " must be a JSON array of show.action ids"}
	}
	if len(actions) > emergencyStopActionsMaxCount {
		return EmergencyStopLevelConfig{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: actionsField,
			Detail: fmt.Sprintf("%s must not exceed %d entries", actionsField, emergencyStopActionsMaxCount),
		}
	}

	seen := make(map[string]bool, len(actions))
	for i, id := range actions {
		itemField := fmt.Sprintf("%s[%d]", actionsField, i)
		if id == "" {
			return EmergencyStopLevelConfig{}, &ValidationError{Code: ValidationCodeFieldEmpty, Field: itemField, Detail: itemField + " must not be empty"}
		}
		if seen[id] {
			return EmergencyStopLevelConfig{}, &ValidationError{
				Code: ValidationCodeEmergencyStopActionDuplicate, Field: itemField,
				Detail: fmt.Sprintf("action %q is already configured earlier in %s", id, actionsField),
			}
		}
		seen[id] = true
		if !actionResolver(id) {
			return EmergencyStopLevelConfig{}, &ValidationError{
				Code: ValidationCodeFieldUnknownReference, Field: itemField,
				Detail: fmt.Sprintf("action %q is not a configured show.action; create it first", id),
			}
		}
	}

	return EmergencyStopLevelConfig{Actions: actions}, nil
}

// ValidationCodeEmergencyStopActionDuplicate: the same show.action id
// appears more than once in one level's own actions list. Its own code
// rather than the generic ValidationCodeFieldInvalid, on
// ValidationCodeStepIDDuplicate/ValidationCodeItemIDDuplicate's own
// precedent — a caller telling two refusals apart must never branch on
// prose.
const ValidationCodeEmergencyStopActionDuplicate = "emergency-stop-action-duplicate"

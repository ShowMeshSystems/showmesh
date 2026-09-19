package config

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is ADR-053's "show.weatherdelay" configuration kind: how the
// optional alert, power groups, and automatic triggers are set up. It
// authors configuration only; nothing here evaluates a trigger, dispatches
// an alert, or reads the live weather-delay state (store/weatherdelay.go).
//
// Singleton, on emergencystop.go's own shape: one config object id, PUT is
// a full replacement. UNLIKE emergencystop.go, every field here is
// OPTIONAL with a working default (ADR-053 decision 13: "The feature is
// optional... Every part is independently optional"), so an empty PUT body
// `{}` is a valid, fully-defaulted configuration, not a refusal.

const (
	// ShowWeatherDelayConfigKind is config_objects.kind and
	// config_revisions.kind for this object, and the second path segment of
	// GET/PUT /api/v1/config/show.weatherdelay.
	ShowWeatherDelayConfigKind = "show.weatherdelay"

	// ShowWeatherDelayConfigObjectID is the single config_objects.id this
	// kind ever uses, on ShowEmergencyStopConfigObjectID's own precedent.
	ShowWeatherDelayConfigObjectID = "default"

	// ShowWeatherDelaySourceAPI is this kind's only config_revisions.source
	// value: nothing ever backed it as a start-time environment variable.
	ShowWeatherDelaySourceAPI = "api"
)

// weatherDelayDefaultRepeatCount, weatherDelayMinRepeatCount, and
// weatherDelayMaxRepeatCount are ADR-053 decision 7's own numbers: "The
// alert plays a configured number of times, ten by default."
const (
	weatherDelayDefaultRepeatCount = 10
	weatherDelayMinRepeatCount     = 1
	weatherDelayMaxRepeatCount     = 50
)

// weatherDelayDefaultAnswerWindowSeconds, weatherDelayDefaultCancelAnswer
// WindowSeconds, and weatherDelayDefaultRestartMinutes are ADR-053 decision
// 12's own defaults for an automatic trigger: "a delay... a longer window"
// for the delay-or-cancel question, and a rough guess at how long an
// interrupted show still has time to restart within.
const (
	weatherDelayDefaultAnswerWindowSeconds       = 30
	weatherDelayDefaultCancelAnswerWindowSeconds = 180
	weatherDelayDefaultRestartMinutes            = 15
)

// weatherDelayDefaultHeartbeatIntervalSeconds is a SHOWMESH GUESS, NOT
// SPECIFIED BY ADR-053: decision 10 requires the heartbeat interval be
// operator-set ("on timeouts the installation sets") but names no default
// cadence. 30s is chosen the way [Config.ResolumeRecoverySettle]'s own
// default is: workable, not measured against a real power controller.
const weatherDelayDefaultHeartbeatIntervalSeconds = 30

const (
	weatherDelayMinAnswerWindowSeconds = 1
	weatherDelayMaxWindowSeconds       = 3600
	weatherDelayMinRestartMinutes      = 0
	weatherDelayMaxRestartMinutes      = 1440
	weatherDelayMinHeartbeatSeconds    = 1
	weatherDelayMaxHeartbeatSeconds    = 3600
)

// WeatherDelayAlertPayload is the optional alert an active delay or
// cancel-night plays (ADR-053 decision 7). DelayAssetID/CancelNightAssetID
// empty means no alert asset is configured for that kind; NodeIDs empty
// means every declared audio node (the task's own "empty = every audio
// node" rule), never "no nodes".
type WeatherDelayAlertPayload struct {
	DelayAssetID       string   `json:"delayAssetId"`
	CancelNightAssetID string   `json:"cancelNightAssetId"`
	RepeatCount        int      `json:"repeatCount"`
	NodeIDs            []string `json:"nodeIds"`
}

// WeatherDelayHeartbeatPayload is one power group's own optional dark
// heartbeat publication (ADR-053 decision 10).
type WeatherDelayHeartbeatPayload struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"intervalSeconds"`
}

// WeatherDelayPowerGroupPayload is one operator-defined power group
// (ADR-053 decision 10): the devices whose dark confirmation this group's
// own heartbeat reports on. An installation with no cutoff configures no
// groups at all.
type WeatherDelayPowerGroupPayload struct {
	ID                  string                       `json:"id"`
	Label               string                       `json:"label"`
	FPPInstanceIDs      []string                     `json:"fppInstanceIds"`
	ResolumeInstanceIDs []string                     `json:"resolumeInstanceIds"`
	RenderNodeIDs       []string                     `json:"renderNodeIds"`
	Heartbeat           WeatherDelayHeartbeatPayload `json:"heartbeat"`
}

// WeatherDelayTriggersPayload is ADR-053 decision 12's automatic-trigger
// timing: how long the operator has to answer before a delay starts on its
// own, and, when too little of the night would remain, the longer
// delay-or-cancel window.
type WeatherDelayTriggersPayload struct {
	AnswerWindowSeconds       int `json:"answerWindowSeconds"`
	CancelAnswerWindowSeconds int `json:"cancelAnswerWindowSeconds"`
	RestartMinutes            int `json:"restartMinutes"`
}

// WeatherDelayPayload is config_revisions.payload_json's decoded,
// VALIDATED shape for [ShowWeatherDelayConfigKind]. Every member is
// optional on the wire; see this file's header comment.
type WeatherDelayPayload struct {
	Alert       WeatherDelayAlertPayload        `json:"alert"`
	PowerGroups []WeatherDelayPowerGroupPayload `json:"powerGroups"`
	Triggers    WeatherDelayTriggersPayload     `json:"triggers"`
}

// WeatherDelayDefaultPayload is the value reported when nothing has ever
// been written for this kind: no alert asset configured, no power groups,
// and this file's own default trigger windows.
var WeatherDelayDefaultPayload = WeatherDelayPayload{
	Alert:       WeatherDelayAlertPayload{RepeatCount: weatherDelayDefaultRepeatCount, NodeIDs: []string{}},
	PowerGroups: []WeatherDelayPowerGroupPayload{},
	Triggers: WeatherDelayTriggersPayload{
		AnswerWindowSeconds:       weatherDelayDefaultAnswerWindowSeconds,
		CancelAnswerWindowSeconds: weatherDelayDefaultCancelAnswerWindowSeconds,
		RestartMinutes:            weatherDelayDefaultRestartMinutes,
	},
}

var (
	weatherDelayTopLevelKeys   = map[string]bool{"alert": true, "powerGroups": true, "triggers": true}
	weatherDelayAlertKeys      = map[string]bool{"delayAssetId": true, "cancelNightAssetId": true, "repeatCount": true, "nodeIds": true}
	weatherDelayPowerGroupKeys = map[string]bool{"id": true, "label": true, "fppInstanceIds": true, "resolumeInstanceIds": true, "renderNodeIds": true, "heartbeat": true}
	weatherDelayHeartbeatKeys  = map[string]bool{"enabled": true, "intervalSeconds": true}
	weatherDelayTriggersKeys   = map[string]bool{"answerWindowSeconds": true, "cancelAnswerWindowSeconds": true, "restartMinutes": true}
)

// EncodeWeatherDelayPayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeWeatherDelayPayload); this function does not re-validate.
func EncodeWeatherDelayPayload(p WeatherDelayPayload) (string, error) {
	if p.PowerGroups == nil {
		p.PowerGroups = []WeatherDelayPowerGroupPayload{}
	}
	if p.Alert.NodeIDs == nil {
		p.Alert.NodeIDs = []string{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.weatherdelay payload: %w", err)
	}
	return string(b), nil
}

// DecodeWeatherDelayPayload parses and validates raw. Every top-level key
// is optional: an empty object `{}` decodes to [WeatherDelayDefaultPayload].
// Unknown keys, at every level, are refused by name rather than ignored.
func DecodeWeatherDelayPayload(raw string) (WeatherDelayPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return WeatherDelayPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, weatherDelayTopLevelKeys); verr != nil {
		return WeatherDelayPayload{}, verr
	}

	alert, verr := decodeWeatherDelayAlert(top)
	if verr != nil {
		return WeatherDelayPayload{}, verr
	}
	powerGroups, verr := decodeWeatherDelayPowerGroups(top)
	if verr != nil {
		return WeatherDelayPayload{}, verr
	}
	triggers, verr := decodeWeatherDelayTriggers(top)
	if verr != nil {
		return WeatherDelayPayload{}, verr
	}

	return WeatherDelayPayload{Alert: alert, PowerGroups: powerGroups, Triggers: triggers}, nil
}

func decodeWeatherDelayAlert(top map[string]json.RawMessage) (WeatherDelayAlertPayload, *ValidationError) {
	raw, present := top["alert"]
	if !present {
		return WeatherDelayDefaultPayload.Alert, nil
	}
	if isJSONNull(raw) {
		return WeatherDelayAlertPayload{}, &ValidationError{Code: ValidationCodeFieldNull, Field: "alert", Detail: "alert must not be null; omit it to use the default"}
	}
	fields, verr := decodeRequiredObjectFromRaw(raw, "alert")
	if verr != nil {
		return WeatherDelayAlertPayload{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, weatherDelayAlertKeys, "alert"); verr != nil {
		return WeatherDelayAlertPayload{}, verr
	}

	delayAssetID, verr := decodeOptionalString(fields, "delayAssetId", "alert.delayAssetId")
	if verr != nil {
		return WeatherDelayAlertPayload{}, verr
	}
	cancelNightAssetID, verr := decodeOptionalString(fields, "cancelNightAssetId", "alert.cancelNightAssetId")
	if verr != nil {
		return WeatherDelayAlertPayload{}, verr
	}
	repeatCount, verr := decodeDefaultedIntRange(fields, "repeatCount", "alert.repeatCount", weatherDelayDefaultRepeatCount, weatherDelayMinRepeatCount, weatherDelayMaxRepeatCount)
	if verr != nil {
		return WeatherDelayAlertPayload{}, verr
	}
	nodeIDs, verr := decodeWeatherDelayIDList(fields, "nodeIds", "alert.nodeIds")
	if verr != nil {
		return WeatherDelayAlertPayload{}, verr
	}

	return WeatherDelayAlertPayload{DelayAssetID: delayAssetID, CancelNightAssetID: cancelNightAssetID, RepeatCount: repeatCount, NodeIDs: nodeIDs}, nil
}

func decodeWeatherDelayPowerGroups(top map[string]json.RawMessage) ([]WeatherDelayPowerGroupPayload, *ValidationError) {
	raw, present := top["powerGroups"]
	if !present {
		return []WeatherDelayPowerGroupPayload{}, nil
	}
	if isJSONNull(raw) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: "powerGroups", Detail: "powerGroups must not be null; omit it or use [] for no power groups"}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "powerGroups", Detail: "powerGroups must be an array"}
	}

	out := make([]WeatherDelayPowerGroupPayload, 0, len(items))
	seen := make(map[string]bool, len(items))
	for i, item := range items {
		field := fmt.Sprintf("powerGroups[%d]", i)
		group, verr := decodeWeatherDelayPowerGroup(item, field)
		if verr != nil {
			return nil, verr
		}
		if seen[group.ID] {
			return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field + ".id", Detail: fmt.Sprintf("power group id %q is already configured earlier in powerGroups", group.ID)}
		}
		seen[group.ID] = true
		out = append(out, group)
	}
	return out, nil
}

func decodeWeatherDelayPowerGroup(raw json.RawMessage, field string) (WeatherDelayPowerGroupPayload, *ValidationError) {
	fields, verr := decodeRequiredObjectFromRaw(raw, field)
	if verr != nil {
		return WeatherDelayPowerGroupPayload{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, weatherDelayPowerGroupKeys, field); verr != nil {
		return WeatherDelayPowerGroupPayload{}, verr
	}

	id, verr := decodeRequiredString(fields, "id", field+".id")
	if verr != nil {
		return WeatherDelayPowerGroupPayload{}, verr
	}
	if err := mqttproto.ValidateNodeID(id); err != nil {
		return WeatherDelayPowerGroupPayload{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field + ".id", Detail: fmt.Sprintf("%s.id: %v", field, err)}
	}
	label, verr := decodeOptionalString(fields, "label", field+".label")
	if verr != nil {
		return WeatherDelayPowerGroupPayload{}, verr
	}
	fppInstanceIDs, verr := decodeWeatherDelayIDList(fields, "fppInstanceIds", field+".fppInstanceIds")
	if verr != nil {
		return WeatherDelayPowerGroupPayload{}, verr
	}
	resolumeInstanceIDs, verr := decodeWeatherDelayIDList(fields, "resolumeInstanceIds", field+".resolumeInstanceIds")
	if verr != nil {
		return WeatherDelayPowerGroupPayload{}, verr
	}
	renderNodeIDs, verr := decodeWeatherDelayIDList(fields, "renderNodeIds", field+".renderNodeIds")
	if verr != nil {
		return WeatherDelayPowerGroupPayload{}, verr
	}
	heartbeat, verr := decodeWeatherDelayHeartbeat(fields, field+".heartbeat")
	if verr != nil {
		return WeatherDelayPowerGroupPayload{}, verr
	}

	return WeatherDelayPowerGroupPayload{
		ID: id, Label: label, FPPInstanceIDs: fppInstanceIDs, ResolumeInstanceIDs: resolumeInstanceIDs,
		RenderNodeIDs: renderNodeIDs, Heartbeat: heartbeat,
	}, nil
}

func decodeWeatherDelayHeartbeat(fields map[string]json.RawMessage, field string) (WeatherDelayHeartbeatPayload, *ValidationError) {
	def := WeatherDelayHeartbeatPayload{Enabled: false, IntervalSeconds: weatherDelayDefaultHeartbeatIntervalSeconds}
	raw, present := fields["heartbeat"]
	if !present {
		return def, nil
	}
	if isJSONNull(raw) {
		return WeatherDelayHeartbeatPayload{}, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: field + " must not be null; omit it to use the default"}
	}
	hbFields, verr := decodeRequiredObjectFromRaw(raw, field)
	if verr != nil {
		return WeatherDelayHeartbeatPayload{}, verr
	}
	if verr := rejectUnknownKeysUnder(hbFields, weatherDelayHeartbeatKeys, field); verr != nil {
		return WeatherDelayHeartbeatPayload{}, verr
	}

	enabled, verr := decodeDefaultedBool(hbFields, "enabled", field+".enabled", def.Enabled)
	if verr != nil {
		return WeatherDelayHeartbeatPayload{}, verr
	}
	intervalSeconds, verr := decodeDefaultedIntRange(hbFields, "intervalSeconds", field+".intervalSeconds", def.IntervalSeconds, weatherDelayMinHeartbeatSeconds, weatherDelayMaxHeartbeatSeconds)
	if verr != nil {
		return WeatherDelayHeartbeatPayload{}, verr
	}
	return WeatherDelayHeartbeatPayload{Enabled: enabled, IntervalSeconds: intervalSeconds}, nil
}

func decodeWeatherDelayTriggers(top map[string]json.RawMessage) (WeatherDelayTriggersPayload, *ValidationError) {
	raw, present := top["triggers"]
	if !present {
		return WeatherDelayDefaultPayload.Triggers, nil
	}
	if isJSONNull(raw) {
		return WeatherDelayTriggersPayload{}, &ValidationError{Code: ValidationCodeFieldNull, Field: "triggers", Detail: "triggers must not be null; omit it to use the default"}
	}
	fields, verr := decodeRequiredObjectFromRaw(raw, "triggers")
	if verr != nil {
		return WeatherDelayTriggersPayload{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, weatherDelayTriggersKeys, "triggers"); verr != nil {
		return WeatherDelayTriggersPayload{}, verr
	}

	answerWindowSeconds, verr := decodeDefaultedIntRange(fields, "answerWindowSeconds", "triggers.answerWindowSeconds", weatherDelayDefaultAnswerWindowSeconds, weatherDelayMinAnswerWindowSeconds, weatherDelayMaxWindowSeconds)
	if verr != nil {
		return WeatherDelayTriggersPayload{}, verr
	}
	cancelAnswerWindowSeconds, verr := decodeDefaultedIntRange(fields, "cancelAnswerWindowSeconds", "triggers.cancelAnswerWindowSeconds", weatherDelayDefaultCancelAnswerWindowSeconds, weatherDelayMinAnswerWindowSeconds, weatherDelayMaxWindowSeconds)
	if verr != nil {
		return WeatherDelayTriggersPayload{}, verr
	}
	restartMinutes, verr := decodeDefaultedIntRange(fields, "restartMinutes", "triggers.restartMinutes", weatherDelayDefaultRestartMinutes, weatherDelayMinRestartMinutes, weatherDelayMaxRestartMinutes)
	if verr != nil {
		return WeatherDelayTriggersPayload{}, verr
	}

	return WeatherDelayTriggersPayload{AnswerWindowSeconds: answerWindowSeconds, CancelAnswerWindowSeconds: cancelAnswerWindowSeconds, RestartMinutes: restartMinutes}, nil
}

// decodeWeatherDelayIDList reads key as an optional array of node-id-syntax
// strings: absent or `[]` both mean "none" (for nodeIds, this file's own
// "empty = every audio node" rule; for the three power-group id lists,
// "none of this kind"). Every entry is checked with
// [mqttproto.ValidateNodeID] and against duplicates within the same list.
func decodeWeatherDelayIDList(top map[string]json.RawMessage, key, field string) ([]string, *ValidationError) {
	ptr, verr := decodeOptionalStringList(top, key, field)
	if verr != nil {
		return nil, verr
	}
	if ptr == nil {
		return []string{}, nil
	}
	seen := make(map[string]bool, len(*ptr))
	for i, id := range *ptr {
		itemField := fmt.Sprintf("%s[%d]", field, i)
		if err := mqttproto.ValidateNodeID(id); err != nil {
			return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: itemField, Detail: fmt.Sprintf("%s: %v", itemField, err)}
		}
		if seen[id] {
			return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: itemField, Detail: fmt.Sprintf("%s: duplicate id %q", itemField, id)}
		}
		seen[id] = true
	}
	return *ptr, nil
}

// decodeDefaultedIntRange reads key from top: absent takes def; present
// (including present-and-null) is refused-then-validated like
// [decodeRequiredInt], then bounds-checked against [min, max].
func decodeDefaultedIntRange(top map[string]json.RawMessage, key, field string, def, min, max int) (int, *ValidationError) {
	raw, present := top[key]
	if !present {
		return def, nil
	}
	if isJSONNull(raw) {
		return 0, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null; omit it to use the default (%d)", field, def)}
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON number", field)}
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a whole number", field)}
	}
	if v < int64(min) || v > int64(max) {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be between %d and %d", field, min, max)}
	}
	return int(v), nil
}

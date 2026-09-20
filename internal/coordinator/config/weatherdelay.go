package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// The "show.weatherdelay" configuration kind: the optional alert, power
// groups and trigger timing. A singleton, fully replaced on PUT. Every field
// is optional (ADR-053 decision 13), so `{}` is a valid configuration.

const (
	ShowWeatherDelayConfigKind     = "show.weatherdelay"
	ShowWeatherDelayConfigObjectID = "default"
	// ShowWeatherDelaySourceAPI is the only source: no environment variable
	// sets this kind.
	ShowWeatherDelaySourceAPI = "api"
)

// The alert plays ten times by default (ADR-053 decision 7).
const (
	weatherDelayDefaultRepeatCount = 10
	weatherDelayMinRepeatCount     = 1
	weatherDelayMaxRepeatCount     = 50
)

// Trigger defaults (ADR-053 decision 12). The ADR sets only that the cancel
// window is longer; these numbers are unmeasured choices.
const (
	weatherDelayDefaultAnswerWindowSeconds       = 30
	weatherDelayDefaultCancelAnswerWindowSeconds = 180
	weatherDelayDefaultRestartMinutes            = 15
	// weatherDelayDefaultDismissQuietMinutes is the task's own default
	// (30 minutes); an unmeasured choice like the other trigger windows.
	weatherDelayDefaultDismissQuietMinutes = 30
)

// weatherDelayDefaultHeartbeatIntervalSeconds is an unmeasured choice; the
// ADR leaves the cadence to the installation.
const weatherDelayDefaultHeartbeatIntervalSeconds = 30

const (
	weatherDelayMinAnswerWindowSeconds = 1
	weatherDelayMaxWindowSeconds       = 3600
	weatherDelayMinRestartMinutes      = 0
	weatherDelayMaxRestartMinutes      = 1440
	weatherDelayMinHeartbeatSeconds    = 1
	weatherDelayMaxHeartbeatSeconds    = 3600
)

// The NWS poller defaults and bounds. pollSeconds' 60s default and 30s floor
// and the two default event types are unmeasured choices the task left to
// this build; the API's own alert id dedupe is what keeps a short interval
// from asking twice about the same alert.
const (
	weatherDelayDefaultNWSPollSeconds = 60
	weatherDelayMinNWSPollSeconds     = 30
	weatherDelayMaxNWSPollSeconds     = 3600
	weatherDelayNWSContactMaxLength   = 256
)

// weatherDelayDefaultNWSEventTypes is copied fresh by
// [decodeWeatherDelayNWS] and [WeatherDelayDefaultPayload]'s initializer so
// no caller can mutate the shared default slice.
func weatherDelayDefaultNWSEventTypes() []string {
	return []string{"Tornado Warning", "Severe Thunderstorm Warning"}
}

// WeatherDelayAlertPayload is the optional alert (ADR-053 decision 7). An
// empty asset id means no alert for that kind; empty NodeIDs means every
// declared audio node.
type WeatherDelayAlertPayload struct {
	DelayAssetID       string   `json:"delayAssetId"`
	CancelNightAssetID string   `json:"cancelNightAssetId"`
	RepeatCount        int      `json:"repeatCount"`
	NodeIDs            []string `json:"nodeIds"`
}

// WeatherDelayHeartbeatPayload is one power group's optional dark heartbeat.
type WeatherDelayHeartbeatPayload struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"intervalSeconds"`
}

// WeatherDelayPowerGroupPayload is one operator-defined power group: the
// devices its dark heartbeat reports on (ADR-053 decision 10).
type WeatherDelayPowerGroupPayload struct {
	ID                  string                       `json:"id"`
	Label               string                       `json:"label"`
	FPPInstanceIDs      []string                     `json:"fppInstanceIds"`
	ResolumeInstanceIDs []string                     `json:"resolumeInstanceIds"`
	RenderNodeIDs       []string                     `json:"renderNodeIds"`
	Heartbeat           WeatherDelayHeartbeatPayload `json:"heartbeat"`
}

// WeatherDelayTriggersPayload is the automatic trigger timing (ADR-053
// decision 12) plus the optional built-in NWS poller.
type WeatherDelayTriggersPayload struct {
	AnswerWindowSeconds       int `json:"answerWindowSeconds"`
	CancelAnswerWindowSeconds int `json:"cancelAnswerWindowSeconds"`
	RestartMinutes            int `json:"restartMinutes"`
	// DismissQuietMinutes is how long a dismiss answer suppresses a new
	// question about the same source and same warning.
	DismissQuietMinutes int                           `json:"dismissQuietMinutes"`
	NWS                 WeatherDelayNWSTriggerPayload `json:"nws"`
}

// WeatherDelayNWSTriggerPayload configures the optional built-in poller of
// the United States National Weather Service alerts API. Disabled by
// default; enabling it without Contact is refused, since that API's own
// User-Agent policy requires one.
type WeatherDelayNWSTriggerPayload struct {
	Enabled     bool     `json:"enabled"`
	Latitude    float64  `json:"latitude"`
	Longitude   float64  `json:"longitude"`
	Contact     string   `json:"contact"`
	PollSeconds int      `json:"pollSeconds"`
	EventTypes  []string `json:"eventTypes"`
}

// WeatherDelayNotifyPayload is the optional webhook (ADR-053 decision 13's
// "the coordinator... calls a configured webhook"). An empty WebhookURL
// means no webhook is configured.
type WeatherDelayNotifyPayload struct {
	WebhookURL string `json:"webhookUrl"`
}

// weatherDelayWebhookURLMaxLength bounds a webhook URL: generous for any
// real endpoint, short enough that a misconfiguration cannot smuggle a
// large blob into stored configuration.
const weatherDelayWebhookURLMaxLength = 2048

// WeatherDelayPayload is the decoded, validated show.weatherdelay payload.
type WeatherDelayPayload struct {
	Alert       WeatherDelayAlertPayload        `json:"alert"`
	PowerGroups []WeatherDelayPowerGroupPayload `json:"powerGroups"`
	Triggers    WeatherDelayTriggersPayload     `json:"triggers"`
	Notify      WeatherDelayNotifyPayload       `json:"notify"`
}

// WeatherDelayDefaultPayload is reported when nothing has been written.
var WeatherDelayDefaultPayload = WeatherDelayPayload{
	Alert:       WeatherDelayAlertPayload{RepeatCount: weatherDelayDefaultRepeatCount, NodeIDs: []string{}},
	PowerGroups: []WeatherDelayPowerGroupPayload{},
	Triggers: WeatherDelayTriggersPayload{
		AnswerWindowSeconds:       weatherDelayDefaultAnswerWindowSeconds,
		CancelAnswerWindowSeconds: weatherDelayDefaultCancelAnswerWindowSeconds,
		RestartMinutes:            weatherDelayDefaultRestartMinutes,
		DismissQuietMinutes:       weatherDelayDefaultDismissQuietMinutes,
		NWS: WeatherDelayNWSTriggerPayload{
			PollSeconds: weatherDelayDefaultNWSPollSeconds,
			EventTypes:  weatherDelayDefaultNWSEventTypes(),
		},
	},
}

var (
	weatherDelayTopLevelKeys   = map[string]bool{"alert": true, "powerGroups": true, "triggers": true, "notify": true}
	weatherDelayAlertKeys      = map[string]bool{"delayAssetId": true, "cancelNightAssetId": true, "repeatCount": true, "nodeIds": true}
	weatherDelayPowerGroupKeys = map[string]bool{"id": true, "label": true, "fppInstanceIds": true, "resolumeInstanceIds": true, "renderNodeIds": true, "heartbeat": true}
	weatherDelayHeartbeatKeys  = map[string]bool{"enabled": true, "intervalSeconds": true}
	weatherDelayTriggersKeys   = map[string]bool{"answerWindowSeconds": true, "cancelAnswerWindowSeconds": true, "restartMinutes": true, "dismissQuietMinutes": true, "nws": true}
	weatherDelayNWSKeys        = map[string]bool{"enabled": true, "latitude": true, "longitude": true, "contact": true, "pollSeconds": true, "eventTypes": true}
	weatherDelayNotifyKeys     = map[string]bool{"webhookUrl": true}
)

// EncodeWeatherDelayPayload marshals an already validated p for storage.
func EncodeWeatherDelayPayload(p WeatherDelayPayload) (string, error) {
	if p.PowerGroups == nil {
		p.PowerGroups = []WeatherDelayPowerGroupPayload{}
	}
	if p.Alert.NodeIDs == nil {
		p.Alert.NodeIDs = []string{}
	}
	if p.Triggers.NWS.EventTypes == nil {
		p.Triggers.NWS.EventTypes = []string{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.weatherdelay payload: %w", err)
	}
	return string(b), nil
}

// DecodeWeatherDelayPayload parses and validates raw. `{}` decodes to
// [WeatherDelayDefaultPayload]; unknown keys at any level are refused by name.
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
	notify, verr := decodeWeatherDelayNotify(top)
	if verr != nil {
		return WeatherDelayPayload{}, verr
	}

	return WeatherDelayPayload{Alert: alert, PowerGroups: powerGroups, Triggers: triggers, Notify: notify}, nil
}

// decodeWeatherDelayNotify reads the optional notify object. Absent or
// {} decodes to no webhook configured.
func decodeWeatherDelayNotify(top map[string]json.RawMessage) (WeatherDelayNotifyPayload, *ValidationError) {
	raw, present := top["notify"]
	if !present {
		return WeatherDelayNotifyPayload{}, nil
	}
	if isJSONNull(raw) {
		return WeatherDelayNotifyPayload{}, &ValidationError{Code: ValidationCodeFieldNull, Field: "notify", Detail: "notify must not be null; omit it for no webhook"}
	}
	fields, verr := decodeRequiredObjectFromRaw(raw, "notify")
	if verr != nil {
		return WeatherDelayNotifyPayload{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, weatherDelayNotifyKeys, "notify"); verr != nil {
		return WeatherDelayNotifyPayload{}, verr
	}
	webhookURL, verr := decodeOptionalString(fields, "webhookUrl", "notify.webhookUrl")
	if verr != nil {
		return WeatherDelayNotifyPayload{}, verr
	}
	if webhookURL != "" {
		if verr := validateWeatherDelayWebhookURL(webhookURL); verr != nil {
			return WeatherDelayNotifyPayload{}, verr
		}
	}
	return WeatherDelayNotifyPayload{WebhookURL: webhookURL}, nil
}

// validateWeatherDelayWebhookURL accepts an absolute http(s) URL with a
// host and no embedded credentials, within the length cap.
func validateWeatherDelayWebhookURL(raw string) *ValidationError {
	field := "notify.webhookUrl"
	if len(raw) > weatherDelayWebhookURLMaxLength {
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be at most %d characters", field, weatherDelayWebhookURLMaxLength)}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s: %v", field, err)}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: field + " must be an http or https URL"}
	}
	if u.Host == "" {
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: field + " must include a host"}
	}
	if u.User != nil {
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: field + " must not include a username or password"}
	}
	return nil
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
	dismissQuietMinutes, verr := decodeDefaultedIntRange(fields, "dismissQuietMinutes", "triggers.dismissQuietMinutes", weatherDelayDefaultDismissQuietMinutes, weatherDelayMinRestartMinutes, weatherDelayMaxRestartMinutes)
	if verr != nil {
		return WeatherDelayTriggersPayload{}, verr
	}
	nws, verr := decodeWeatherDelayNWS(fields)
	if verr != nil {
		return WeatherDelayTriggersPayload{}, verr
	}

	return WeatherDelayTriggersPayload{
		AnswerWindowSeconds: answerWindowSeconds, CancelAnswerWindowSeconds: cancelAnswerWindowSeconds,
		RestartMinutes: restartMinutes, DismissQuietMinutes: dismissQuietMinutes, NWS: nws,
	}, nil
}

// decodeWeatherDelayNWS reads the optional built-in NWS poller. Absent or
// {} decodes to disabled with the default poll interval and event types.
// Enabled without a contact is refused: that API's User-Agent policy
// requires one.
func decodeWeatherDelayNWS(top map[string]json.RawMessage) (WeatherDelayNWSTriggerPayload, *ValidationError) {
	def := WeatherDelayNWSTriggerPayload{PollSeconds: weatherDelayDefaultNWSPollSeconds, EventTypes: weatherDelayDefaultNWSEventTypes()}
	raw, present := top["nws"]
	if !present {
		return def, nil
	}
	if isJSONNull(raw) {
		return WeatherDelayNWSTriggerPayload{}, &ValidationError{Code: ValidationCodeFieldNull, Field: "triggers.nws", Detail: "triggers.nws must not be null; omit it to use the default"}
	}
	fields, verr := decodeRequiredObjectFromRaw(raw, "triggers.nws")
	if verr != nil {
		return WeatherDelayNWSTriggerPayload{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, weatherDelayNWSKeys, "triggers.nws"); verr != nil {
		return WeatherDelayNWSTriggerPayload{}, verr
	}

	enabled, verr := decodeDefaultedBool(fields, "enabled", "triggers.nws.enabled", def.Enabled)
	if verr != nil {
		return WeatherDelayNWSTriggerPayload{}, verr
	}
	latitude, verr := decodeDefaultedFloatRange(fields, "latitude", "triggers.nws.latitude", 0, -90, 90)
	if verr != nil {
		return WeatherDelayNWSTriggerPayload{}, verr
	}
	longitude, verr := decodeDefaultedFloatRange(fields, "longitude", "triggers.nws.longitude", 0, -180, 180)
	if verr != nil {
		return WeatherDelayNWSTriggerPayload{}, verr
	}
	contact, verr := decodeOptionalString(fields, "contact", "triggers.nws.contact")
	if verr != nil {
		return WeatherDelayNWSTriggerPayload{}, verr
	}
	if len(contact) > weatherDelayNWSContactMaxLength {
		return WeatherDelayNWSTriggerPayload{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "triggers.nws.contact", Detail: fmt.Sprintf("triggers.nws.contact must be at most %d characters", weatherDelayNWSContactMaxLength)}
	}
	pollSeconds, verr := decodeDefaultedIntRange(fields, "pollSeconds", "triggers.nws.pollSeconds", weatherDelayDefaultNWSPollSeconds, weatherDelayMinNWSPollSeconds, weatherDelayMaxNWSPollSeconds)
	if verr != nil {
		return WeatherDelayNWSTriggerPayload{}, verr
	}
	eventTypesPtr, verr := decodeOptionalStringList(fields, "eventTypes", "triggers.nws.eventTypes")
	if verr != nil {
		return WeatherDelayNWSTriggerPayload{}, verr
	}
	eventTypes := def.EventTypes
	if eventTypesPtr != nil {
		eventTypes = *eventTypesPtr
	}
	if enabled && contact == "" {
		return WeatherDelayNWSTriggerPayload{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "triggers.nws.contact", Detail: "triggers.nws.contact is required when triggers.nws.enabled is true; the National Weather Service alerts API requires a contact in the User-Agent header"}
	}
	if enabled && len(eventTypes) == 0 {
		return WeatherDelayNWSTriggerPayload{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "triggers.nws.eventTypes", Detail: "triggers.nws.eventTypes must not be empty when triggers.nws.enabled is true"}
	}

	return WeatherDelayNWSTriggerPayload{
		Enabled: enabled, Latitude: latitude, Longitude: longitude, Contact: contact,
		PollSeconds: pollSeconds, EventTypes: eventTypes,
	}, nil
}

// decodeDefaultedFloatRange reads an optional number in [min, max]. Absent
// takes def; null is refused.
func decodeDefaultedFloatRange(top map[string]json.RawMessage, key, field string, def, min, max float64) (float64, *ValidationError) {
	raw, present := top[key]
	if !present {
		return def, nil
	}
	if isJSONNull(raw) {
		return 0, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null; omit it to use the default (%v)", field, def)}
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON number", field)}
	}
	if v < min || v > max {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be between %v and %v", field, min, max)}
	}
	return v, nil
}

// decodeWeatherDelayIDList reads an optional list of unique ids in node id
// syntax. Absent reads as empty.
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

// decodeDefaultedIntRange reads an optional whole number in [min, max].
// Absent takes def; null is refused.
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

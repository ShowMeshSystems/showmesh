package v1

// This file is ADR-053's weather-delay wire contract: the read-only
// current state, the "show.weatherdelay" configuration kind, and the
// three trigger routes' request shape. The three trigger routes
// (start/cancel-night/resume) currently always answer 501 — see
// api/openapi.yaml's own description of each - so this file carries no
// response type for them beyond the shared [Problem] document.

// EventKindWeatherDelayChanged is the change-stream event kind a future
// build publishes when the weather-delay state changes. Reserved here, not
// yet wired into the stream: stream.go's pendingFrame switch gains a case
// for it in a later branch.
const EventKindWeatherDelayChanged = "weatherDelay.changed"

// WeatherDelayStateResponse is the body of GET /api/v1/weather-delay:
// [pkg/weatherdelay.State]'s wire projection. Kind/StartedAt/StartedBy are
// empty/omitted while Active is false, mirroring [ShowModeConfigResponse]'s
// own "never a partial or stale value while inactive" posture.
type WeatherDelayStateResponse struct {
	ServerTime string `json:"serverTime"`
	Active     bool   `json:"active"`
	Kind       string `json:"kind,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	StartedBy  string `json:"startedBy,omitempty"`
	Revision   int64  `json:"revision"`
}

// WeatherDelayActionRequest is the body of POST .../weather-delay/start,
// .../cancel-night, and .../resume: an idempotencyKey, on
// [EmergencyStopRequest]'s own identical shape.
type WeatherDelayActionRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

// ConfigWeatherDelayAlertPayload is [config.WeatherDelayAlertPayload]'s
// wire projection.
type ConfigWeatherDelayAlertPayload struct {
	DelayAssetID       string   `json:"delayAssetId"`
	CancelNightAssetID string   `json:"cancelNightAssetId"`
	RepeatCount        int      `json:"repeatCount"`
	NodeIDs            []string `json:"nodeIds"`
}

// ConfigWeatherDelayHeartbeatPayload is
// [config.WeatherDelayHeartbeatPayload]'s wire projection.
type ConfigWeatherDelayHeartbeatPayload struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"intervalSeconds"`
}

// ConfigWeatherDelayPowerGroupPayload is
// [config.WeatherDelayPowerGroupPayload]'s wire projection.
type ConfigWeatherDelayPowerGroupPayload struct {
	ID                  string                             `json:"id"`
	Label               string                             `json:"label"`
	FPPInstanceIDs      []string                           `json:"fppInstanceIds"`
	ResolumeInstanceIDs []string                           `json:"resolumeInstanceIds"`
	RenderNodeIDs       []string                           `json:"renderNodeIds"`
	Heartbeat           ConfigWeatherDelayHeartbeatPayload `json:"heartbeat"`
}

// ConfigWeatherDelayTriggersPayload is
// [config.WeatherDelayTriggersPayload]'s wire projection.
type ConfigWeatherDelayTriggersPayload struct {
	AnswerWindowSeconds       int `json:"answerWindowSeconds"`
	CancelAnswerWindowSeconds int `json:"cancelAnswerWindowSeconds"`
	RestartMinutes            int `json:"restartMinutes"`
}

// ConfigWeatherDelayPayload is the "show.weatherdelay" configuration
// kind's decoded payload: the body PUT /config/show.weatherdelay accepts
// (a full replacement, every member optional with a working default -
// UNLIKE [ConfigEmergencyStopPayload]), and the "payload" member of
// GET /config/show.weatherdelay's response.
type ConfigWeatherDelayPayload struct {
	Alert       ConfigWeatherDelayAlertPayload        `json:"alert"`
	PowerGroups []ConfigWeatherDelayPowerGroupPayload `json:"powerGroups"`
	Triggers    ConfigWeatherDelayTriggersPayload     `json:"triggers"`
}

// WeatherDelayConfigResponse is the body of GET and PUT
// /config/show.weatherdelay. revision 0 / source "default" means nothing
// has ever been written and payload carries the built-in default.
type WeatherDelayConfigResponse struct {
	ServerTime             string                    `json:"serverTime"`
	Kind                   string                    `json:"kind"`
	Revision               int64                     `json:"revision"`
	Payload                ConfigWeatherDelayPayload `json:"payload"`
	UpdatedAt              string                    `json:"updatedAt"`
	CreatedByPrincipalID   *string                   `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                   `json:"createdByPrincipalName"`
	Source                 string                    `json:"source"`
}

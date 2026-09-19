package v1

// EventKindWeatherDelayChanged is the change-stream event kind for a
// weather delay state change. Reserved; nothing publishes it yet.
const EventKindWeatherDelayChanged = "weatherDelay.changed"

// WeatherDelayStateResponse is the body of GET /api/v1/weather-delay. Kind,
// StartedAt and StartedBy are omitted while Active is false.
type WeatherDelayStateResponse struct {
	ServerTime string `json:"serverTime"`
	Active     bool   `json:"active"`
	Kind       string `json:"kind,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	StartedBy  string `json:"startedBy,omitempty"`
	Revision   int64  `json:"revision"`
}

// WeatherDelayActionRequest is the body of the start, cancel-night and
// resume routes.
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

// ConfigWeatherDelayPayload is the show.weatherdelay payload: the PUT body
// and the GET "payload" member. Every member is optional.
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

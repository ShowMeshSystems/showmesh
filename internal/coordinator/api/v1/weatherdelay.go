package v1

// EventKindWeatherDelayChanged is the change-stream event kind for a
// weather delay state change, both the recorded event (see
// appendWeatherDelayChangedEvent) and the stream frame (see
// [WeatherDelayChangedEvent]).
const EventKindWeatherDelayChanged = "weatherDelay.changed"

// WeatherDelayChangedEvent is the "weatherDelay.changed" stream frame, one
// kind for the delay's own state change (start, resume, cancel) and a
// power group's own confirmedDark flip, mirroring
// [NightSessionChangedEvent]'s identical "full state, not delta" posture:
// this stream is not resumable, so a reconnecting client re-fetches
// GET /api/v1/weather-delay on any gap rather than reconstructing state
// from a transition history.
type WeatherDelayChangedEvent struct {
	Seq         uint64                         `json:"seq"`
	ServerTime  string                         `json:"serverTime"`
	Active      bool                           `json:"active"`
	Kind        string                         `json:"kind,omitempty"`
	StartedAt   string                         `json:"startedAt,omitempty"`
	StartedBy   string                         `json:"startedBy,omitempty"`
	Revision    int64                          `json:"revision"`
	PowerGroups []WeatherDelayPowerGroupStatus `json:"powerGroups"`
}

// WeatherDelayStateResponse is the body of GET /api/v1/weather-delay. Kind,
// StartedAt and StartedBy are omitted while Active is false. Assets
// reports, per plan node, whether each configured alert asset is present
// and hash-verified there, so an operator can see on
// a calm day that the alert is ready.
type WeatherDelayStateResponse struct {
	ServerTime  string                         `json:"serverTime"`
	Active      bool                           `json:"active"`
	Kind        string                         `json:"kind,omitempty"`
	StartedAt   string                         `json:"startedAt,omitempty"`
	StartedBy   string                         `json:"startedBy,omitempty"`
	Revision    int64                          `json:"revision"`
	Assets      []WeatherDelayNodeAssets       `json:"assets"`
	PowerGroups []WeatherDelayPowerGroupStatus `json:"powerGroups"`
}

// WeatherDelayPowerGroupStatus is one configured power group's own dark
// confirmation (ADR-053 decision 10). Since is empty while ConfirmedDark
// is false.
type WeatherDelayPowerGroupStatus struct {
	ID            string                         `json:"id"`
	Label         string                         `json:"label"`
	ConfirmedDark bool                           `json:"confirmedDark"`
	Since         string                         `json:"since,omitempty"`
	Members       []WeatherDelayPowerGroupMember `json:"members"`
}

// WeatherDelayPowerGroupMember is one device tracked by a power group.
// Kind is "fpp", "resolume" or "render". Reason is an operator sentence,
// set whenever Dark is false.
type WeatherDelayPowerGroupMember struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Dark   bool   `json:"dark"`
	Reason string `json:"reason,omitempty"`
}

// WeatherDelayNodeAssets is one plan node's own alert asset readiness.
type WeatherDelayNodeAssets struct {
	NodeID      string                   `json:"nodeId"`
	DelayAsset  *WeatherDelayAssetStatus `json:"delayAsset,omitempty"`
	CancelAsset *WeatherDelayAssetStatus `json:"cancelNightAsset,omitempty"`
}

// WeatherDelayAssetStatus is one alert asset's own presence on one node.
type WeatherDelayAssetStatus struct {
	AssetID  string `json:"assetId"`
	Present  bool   `json:"present"`
	Filename string `json:"filename,omitempty"`
}

// WeatherDelayActionRequest is the body of the start, cancel-night and
// resume routes.
type WeatherDelayActionRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

// WeatherDelayTargetOutcome is one target's own weather delay start/resume
// dispatch outcome, mirroring [EmergencyStopInstanceOutcome]'s shape.
// TargetKind is one of [EmergencyStopTargetKindFPP],
// [EmergencyStopTargetKindNode], [EmergencyStopTargetKindResolume],
// [EmergencyStopTargetKindRender], or "node-command" for the
// weatherdelay.start/resume node command itself. DeliveredVia is set only
// for "node-command": "mqtt", "http", "both", or "none", ADR-053 decision
// 8's "a node reached by either path counts as reached."
type WeatherDelayTargetOutcome struct {
	InstanceID    string  `json:"instanceId"`
	TargetKind    string  `json:"targetKind"`
	Outcome       string  `json:"outcome"`
	OutcomeReason string  `json:"outcomeReason"`
	DeliveredVia  string  `json:"deliveredVia,omitempty"`
	DispatchedAt  *string `json:"dispatchedAt"`
}

// WeatherDelayTargetKindNodeCommand is the weatherdelay.start/resume node
// command's own target kind: distinct from [EmergencyStopTargetKindNode]
// (audio.node.silence), since weather delay never sends that action to a
// plan node, where it would race the alert.
const WeatherDelayTargetKindNodeCommand = "node-command"

// WeatherDelayTargetKindGate is the weather gate close/open dispatch's own
// target kind, one entry per configured FPP instance.
const WeatherDelayTargetKindGate = "weather-gate"

// WeatherDelayActionResult is the shared result shape start and resume
// both answer with.
type WeatherDelayActionResult struct {
	Kind           string                      `json:"kind"`
	IdempotencyKey string                      `json:"idempotencyKey"`
	Active         bool                        `json:"active"`
	StartedAt      string                      `json:"startedAt,omitempty"`
	StartedBy      string                      `json:"startedBy,omitempty"`
	Revision       int64                       `json:"revision"`
	Targets        []WeatherDelayTargetOutcome `json:"targets"`
	// NotSaved is true when start could not store the active state; this
	// coordinator process still holds it. NotSavedMessage says so.
	NotSaved        bool   `json:"notSaved,omitempty"`
	NotSavedMessage string `json:"notSavedMessage,omitempty"`
}

// WeatherDelayActionResponse is the body of POST .../weather-delay/start
// and POST .../weather-delay/resume.
type WeatherDelayActionResponse struct {
	ServerTime string                   `json:"serverTime"`
	Result     WeatherDelayActionResult `json:"result"`
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

package v1

import "github.com/showmeshsystems/showmesh/pkg/weatherdelay"

// EventKindWeatherDelayChanged is the change-stream event kind for a
// weather delay state change, both the recorded event (see
// appendWeatherDelayChangedEvent) and the stream frame (see
// [WeatherDelayChangedEvent]).
const EventKindWeatherDelayChanged = "weatherDelay.changed"

// WeatherDelayChangedEvent is the "weatherDelay.changed" stream frame for a
// state change or a power group flip. It is full state, not a delta; a
// reconnecting client re-reads GET /api/v1/weather-delay.
type WeatherDelayChangedEvent struct {
	Seq           uint64                         `json:"seq"`
	ServerTime    string                         `json:"serverTime"`
	Active        bool                           `json:"active"`
	Kind          string                         `json:"kind,omitempty"`
	StartedAt     string                         `json:"startedAt,omitempty"`
	StartedBy     string                         `json:"startedBy,omitempty"`
	StartedByName string                         `json:"startedByName,omitempty"`
	Revision      int64                          `json:"revision"`
	PowerGroups   []WeatherDelayPowerGroupStatus `json:"powerGroups"`
	HeldPlayers   []WeatherDelayHeldPlayer       `json:"heldPlayers"`
}

// WeatherDelayStateResponse is the body of GET /api/v1/weather-delay. Kind,
// StartedAt and StartedBy are omitted while Active is false. Assets
// reports, per plan node, whether each configured alert asset is present
// and hash-verified there, so an operator can see on
// a calm day that the alert is ready.
type WeatherDelayStateResponse struct {
	ServerTime      string                         `json:"serverTime"`
	Active          bool                           `json:"active"`
	Kind            string                         `json:"kind,omitempty"`
	StartedAt       string                         `json:"startedAt,omitempty"`
	StartedBy       string                         `json:"startedBy,omitempty"`
	StartedByName   string                         `json:"startedByName,omitempty"`
	Revision        int64                          `json:"revision"`
	Assets          []WeatherDelayNodeAssets       `json:"assets"`
	PowerGroups     []WeatherDelayPowerGroupStatus `json:"powerGroups"`
	HeldPlayers     []WeatherDelayHeldPlayer       `json:"heldPlayers"`
	LastNotifyError string                         `json:"lastNotifyError,omitempty"`
	// PendingDecision is ADR-053 decision 12's one outstanding automatic
	// trigger question, nil when none is pending.
	PendingDecision *WeatherDelayPendingDecision `json:"pendingDecision,omitempty"`
	// Sources reports every configured trigger source's own health.
	Sources []WeatherDelaySourceHealth `json:"sources"`
	// SourcesMessage is set when a warning feed is the only enabled
	// source: it does not cover ordinary lightning on its own.
	SourcesMessage string `json:"sourcesMessage,omitempty"`
}

// WeatherDelayPendingDecision is the wire form of
// weathertrigger.PendingDecision.
type WeatherDelayPendingDecision struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	Reason        string `json:"reason"`
	Question      string `json:"question"`
	DefaultAction string `json:"defaultAction"`
	AskedAt       string `json:"askedAt"`
	Deadline      string `json:"deadline"`
	// ExpiresAt is the warning's own expiry as its source reported it,
	// omitted when the source reported none.
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// WeatherDelaySourceHealth is one configured trigger source's own health.
// LastError, LastErrorAt and LastSuccessAt are empty for a source with no
// health history yet (an inbound source with no poll loop of its own, or
// the NWS poller before its first poll).
type WeatherDelaySourceHealth struct {
	Source        string `json:"source"`
	Enabled       bool   `json:"enabled"`
	LastError     string `json:"lastError,omitempty"`
	LastErrorAt   string `json:"lastErrorAt,omitempty"`
	LastSuccessAt string `json:"lastSuccessAt,omitempty"`
}

// WeatherDelayTriggerRequest is the body of POST
// /weather-delay/triggers/{source}.
type WeatherDelayTriggerRequest struct {
	Kind          string   `json:"kind"`
	EventType     string   `json:"eventType,omitempty"`
	Severity      string   `json:"severity,omitempty"`
	ExpiresAt     string   `json:"expiresAt,omitempty"`
	DistanceKm    *float64 `json:"distanceKm,omitempty"`
	SuggestCancel bool     `json:"suggestCancel,omitempty"`
}

// WeatherDelayTriggerResponse is the body of POST
// /weather-delay/triggers/{source}.
type WeatherDelayTriggerResponse struct {
	ServerTime      string                       `json:"serverTime"`
	Accepted        bool                         `json:"accepted"`
	Message         string                       `json:"message"`
	PendingDecision *WeatherDelayPendingDecision `json:"pendingDecision,omitempty"`
}

// WeatherDelayDecisionRequest is the body of POST /weather-delay/decision.
type WeatherDelayDecisionRequest struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
}

// WeatherDelayDecisionResponse is the body of POST /weather-delay/decision.
// Result is set when answer is "delay" or "cancelNight"; it is nil for
// "dismiss".
type WeatherDelayDecisionResponse struct {
	ServerTime string                    `json:"serverTime"`
	Answer     string                    `json:"answer"`
	Result     *WeatherDelayActionResult `json:"result,omitempty"`
}

// WeatherDelayHeldPlayer is a player whose gate reads closed while no delay
// is active. Only a resume opens it; Message is the operator sentence.
type WeatherDelayHeldPlayer struct {
	InstanceID string `json:"instanceId"`
	Message    string `json:"message"`
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
	StartedByName  string                      `json:"startedByName,omitempty"`
	Revision       int64                       `json:"revision"`
	Targets        []WeatherDelayTargetOutcome `json:"targets"`
	// NotSaved is true when start could not store the active state; this
	// coordinator process still holds it. NotSavedMessage says so.
	NotSaved        bool   `json:"notSaved,omitempty"`
	NotSavedMessage string `json:"notSavedMessage,omitempty"`
	// Message is set when a start found the night already cancelled.
	Message string `json:"message,omitempty"`
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
	AnswerWindowSeconds       int                                 `json:"answerWindowSeconds"`
	CancelAnswerWindowSeconds int                                 `json:"cancelAnswerWindowSeconds"`
	RestartMinutes            int                                 `json:"restartMinutes"`
	DismissQuietMinutes       int                                 `json:"dismissQuietMinutes"`
	NWS                       ConfigWeatherDelayNWSTriggerPayload `json:"nws"`
}

// ConfigWeatherDelayNWSTriggerPayload is
// [config.WeatherDelayNWSTriggerPayload]'s wire projection.
type ConfigWeatherDelayNWSTriggerPayload struct {
	Enabled     bool     `json:"enabled"`
	Latitude    float64  `json:"latitude"`
	Longitude   float64  `json:"longitude"`
	Contact     string   `json:"contact"`
	PollSeconds int      `json:"pollSeconds"`
	EventTypes  []string `json:"eventTypes"`
}

// ConfigWeatherDelayNotifyPayload is [config.WeatherDelayNotifyPayload]'s
// wire projection. An empty WebhookURL means no webhook is configured.
type ConfigWeatherDelayNotifyPayload struct {
	WebhookURL string `json:"webhookUrl"`
}

// ConfigWeatherDelayPayload is the show.weatherdelay payload: the PUT body
// and the GET "payload" member. Every member is optional.
type ConfigWeatherDelayPayload struct {
	Alert       ConfigWeatherDelayAlertPayload        `json:"alert"`
	PowerGroups []ConfigWeatherDelayPowerGroupPayload `json:"powerGroups"`
	Triggers    ConfigWeatherDelayTriggersPayload     `json:"triggers"`
	Notify      ConfigWeatherDelayNotifyPayload       `json:"notify"`
}

// WeatherDelayPresignedStartRequest is the body of POST
// /api/v1/weather-delay/presigned-start.
type WeatherDelayPresignedStartRequest struct {
	Kind      string `json:"kind"`
	ValidDays int    `json:"validDays"`
}

// WeatherDelayPresignedStartResponse is the signed document to hold and the
// node URLs it can be POSTed to today. Plan nodes change, so the list is advisory.
type WeatherDelayPresignedStartResponse struct {
	ServerTime string                          `json:"serverTime"`
	Request    weatherdelay.SignedStartRequest `json:"request"`
	NodeURLs   []string                        `json:"nodeUrls"`
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

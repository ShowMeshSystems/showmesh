package v1

// This file is the wire contract (ADR-039): the
// "audio.settings" singleton and the "audio.node" collection.
// ConfigObjectSummary/ConfigObjectsListResponse (showmacros.go) and
// ConfigRevisionMeta/ConfigRevisionsResponse (types.go) are reused
// verbatim rather than declared a second time — both are already
// kind-agnostic.

// ConfigAudioSettingsPayload is the "audio.settings" configuration kind's
// decoded payload: the body PUT /config/audio.settings accepts (a full
// replacement — every field required), and the "payload" member of GET
// /config/audio.settings' response.
type ConfigAudioSettingsPayload struct {
	DriftIgnoreThresholdMs int    `json:"driftIgnoreThresholdMs"`
	DefaultFadeCurve       string `json:"defaultFadeCurve"`
	DefaultFadeDurationMs  int    `json:"defaultFadeDurationMs"`
	// Both gains are DECIBELS on this surface: 0 dB is
	// unity. The coordinator converts them to the engine's linear
	// multiplier once, at its own boundary.
	DefaultMaxBackgroundGainDb float64 `json:"defaultMaxBackgroundGainDb"`
	DuckTargetGainDb           float64 `json:"duckTargetGainDb"`
	LTCFrameRate               string  `json:"ltcFrameRate"`
	LTCDefaultStartOffset      string  `json:"ltcDefaultStartOffset"`
}

// AudioSettingsConfigResponse is the body of GET and PUT
// /config/audio.settings. revision 0 / source "default" means nothing has
// ever been written and payload carries the built-in default — mirrors
// RenderSettingsConfigResponse's identical "no 404, a stated default"
// posture.
type AudioSettingsConfigResponse struct {
	ServerTime             string                     `json:"serverTime"`
	Kind                   string                     `json:"kind"`
	Revision               int64                      `json:"revision"`
	Payload                ConfigAudioSettingsPayload `json:"payload"`
	UpdatedAt              string                     `json:"updatedAt"`
	CreatedByPrincipalID   *string                    `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                    `json:"createdByPrincipalName"`
	Source                 string                     `json:"source"`
}

// ConfigAudioNode is the "audio.node" configuration kind's decoded
// payload: the body PUT /config/audio.node/{nodeId} accepts (a full
// replacement — every field required), and the "payload" member of GET
// /config/audio.node/{nodeId}'s response.
type ConfigAudioNode struct {
	ProgramRoute          string `json:"programRoute"`
	LTCRoute              string `json:"ltcRoute"`
	ProgramChannels       []int  `json:"programChannels"`
	LTCChannel            int    `json:"ltcChannel"`
	ClockDomain           string `json:"clockDomain"`
	ClockDomainProvenance string `json:"clockDomainProvenance"`

	// Role is ADR-045's audio.node role: "program", "program+ltc", or
	// "zone". Optional on the wire; absent decodes to "program+ltc" so a
	// pre-ADR-045 payload keeps working unchanged.
	Role string `json:"role,omitempty"`

	// Zone is the operator's own name for the independent speaker zone
	// this node drives, present only when Role is "zone".
	Zone *string `json:"zone,omitempty"`
}

// AudioNodeConfigResponse is the body of GET and PUT
// /config/audio.node/{nodeId}.
type AudioNodeConfigResponse struct {
	ServerTime             string          `json:"serverTime"`
	Kind                   string          `json:"kind"`
	ID                     string          `json:"id"`
	Revision               int64           `json:"revision"`
	Payload                ConfigAudioNode `json:"payload"`
	UpdatedAt              string          `json:"updatedAt"`
	CreatedByPrincipalID   *string         `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string         `json:"createdByPrincipalName"`
	Source                 string          `json:"source"`
}

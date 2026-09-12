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
	// DuckFadeDurationMs/DuckRestoreFadeDurationMs are how long a session
	// takes to fade down into a duck and back up out of one — see
	// config.AudioSettingsPayload's own doc comment for why they differ.
	DuckFadeDurationMs        int    `json:"duckFadeDurationMs"`
	DuckRestoreFadeDurationMs int    `json:"duckRestoreFadeDurationMs"`
	LTCFrameRate              string `json:"ltcFrameRate"`
	LTCDefaultStartOffset     string `json:"ltcDefaultStartOffset"`
	// The only two fields here the COORDINATOR reads rather than a node:
	// the two terms it adds to a scheduled start's T0. Both are guesses,
	// not measurements; see config.AudioSettingsPayload.
	ScheduledStartDeliveryBoundMs int `json:"scheduledStartDeliveryBoundMs"`
	ScheduledStartMarginMs        int `json:"scheduledStartMarginMs"`
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
// replacement), and the "payload" member of GET
// /config/audio.node/{nodeId}'s response. LTCRoute and LTCChannel are
// the one optional pair: both absent declares a program-only node that
// emits no LTC, which is how a two-output interface is declared at all.
type ConfigAudioNode struct {
	ProgramRoute          string `json:"programRoute"`
	LTCRoute              string `json:"ltcRoute,omitempty"`
	ProgramChannels       []int  `json:"programChannels"`
	LTCChannel            int    `json:"ltcChannel,omitempty"`
	ClockDomain           string `json:"clockDomain"`
	ClockDomainProvenance string `json:"clockDomainProvenance"`

	// Role is ADR-045's audio.node role: "program", "program+ltc", or
	// "zone". Optional on the wire; absent decodes to "program+ltc" so a
	// pre-ADR-045 payload keeps working unchanged.
	Role string `json:"role,omitempty"`

	// Zone is the operator's own name for the independent speaker zone
	// this node drives, present only when Role is "zone".
	Zone *string `json:"zone,omitempty"`

	// SinkBackend is the GStreamer output backend this node's agent
	// builds against: "alsasink" or "pipewiresink" (RES-019 section 7.2
	// candidate A, ADR-046). Optional on the wire; absent decodes to
	// "alsasink".
	SinkBackend string `json:"sinkBackend,omitempty"`

	// PipewireTargetNode is the PipeWire node name this node's
	// pipewiresink builds its "target-object" property from, so program
	// audio goes to a specific PipeWire node rather than whatever
	// PipeWire's own default sink happens to be. Present only when
	// SinkBackend is "pipewiresink". Optional even then: omitted,
	// pipewiresink is built with no target-object property at all
	// (PipeWire's own default sink, unchanged from before this field
	// existed).
	PipewireTargetNode *string `json:"pipewireTargetNode,omitempty"`
}

// AudioNodeSummary is one element of [AudioNodeListResponse]: enough to
// enumerate every audio.node object's routing without fetching each one's
// full payload. ProgramChannels and LTCChannel reuse [ConfigAudioNode]'s
// own already-pinned names for the same two facts, rather than a second
// pair of names for the same meaning. A dedicated type rather than
// [ConfigObjectSummary]: that shape is shared across kinds that carry a
// Show reference, which audio.node does not, and bolting kind-specific
// channel fields onto a kind-agnostic shape would make every other
// consumer of ConfigObjectSummary carry fields that mean nothing for them.
type AudioNodeSummary struct {
	ID              string `json:"id"`
	Label           string `json:"label"`
	ProgramChannels []int  `json:"programChannels"`
	LTCChannel      int    `json:"ltcChannel,omitempty"`
	CurrentRevision int64  `json:"currentRevision"`
	UpdatedAt       string `json:"updatedAt"`
}

// AudioNodeListResponse is the body of GET /config/audio.node.
type AudioNodeListResponse struct {
	ServerTime string             `json:"serverTime"`
	Kind       string             `json:"kind"`
	Objects    []AudioNodeSummary `json:"objects"`
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

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

	// OutputLatency is this node's calibrated static output-chain delay
	// (RES-019 section 8), always present so its provenance (or the
	// "unmeasured" default) is never omitted from a read.
	OutputLatency ConfigAudioOutputLatency `json:"outputLatency"`
}

// ConfigAudioOutputLatency is "audio.node.outputLatency" (RES-019
// section 8): a signed per-output offset, in microseconds, subtracted
// from that node's scheduled start instant so playback reaches the air
// at the intended instant instead of one output-chain delay late.
// Method "unmeasured" is the default, applies zero, and carries every
// other field empty/absent: a value beside it would be a fabricated
// measurement. Every other method requires MeasuredAt, Reference,
// Confidence, and Configuration together with ValueUs: Configuration in
// particular records the buffer/quantum/sample-rate configuration the
// value was measured under, because RES-019 section 8 found the offset
// moves with PipeWire's graph quantum and a value is only valid for the
// configuration it was measured under.
type ConfigAudioOutputLatency struct {
	// ValueUs is a pointer so a stored measured value of exactly 0
	// microseconds still reaches the wire; a plain int with omitempty
	// would drop it and break a carry-forward write of that value.
	ValueUs       *int    `json:"valueUs,omitempty"`
	Method        string  `json:"method"`
	MeasuredAt    *string `json:"measuredAt,omitempty"`
	Reference     string  `json:"reference,omitempty"`
	Confidence    string  `json:"confidence,omitempty"`
	Configuration string  `json:"configuration,omitempty"`
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

package main

import "time"

// Independent transcription of internal/coordinator/api/v1/nightlifecycle.go
// into this program's own wire-decoding layer (types_night.go's own
// standing rule).

type nightReadinessCheckWire struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type nightReadinessWire struct {
	State       string                    `json:"state"`
	Reason      string                    `json:"reason"`
	Outcome     string                    `json:"outcome,omitempty"`
	EpochID     string                    `json:"epochId,omitempty"`
	CompletedAt string                    `json:"completedAt,omitempty"`
	SameEpoch   bool                      `json:"sameEpoch"`
	Fresh       bool                      `json:"fresh"`
	Checks      []nightReadinessCheckWire `json:"checks"`
}

type nightPhaseEvidenceWire struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type nightCueWire struct {
	Name           string  `json:"name"`
	Phase          string  `json:"phase"`
	Role           string  `json:"role"`
	Action         string  `json:"action"`
	ActionRevision *int64  `json:"actionRevision"`
	State          string  `json:"state"`
	Outcome        string  `json:"outcome,omitempty"`
	Reason         string  `json:"reason,omitempty"`
	DispatchedAt   *string `json:"dispatchedAt"`
	ResolvedAt     *string `json:"resolvedAt"`
}

type nightCuesWire struct {
	State  string         `json:"state"`
	Reason string         `json:"reason"`
	Cues   []nightCueWire `json:"cues"`
}

type nightBackgroundAudioStepWire struct {
	Sequence       string  `json:"sequence"`
	Phase          string  `json:"phase"`
	CueName        string  `json:"cueName"`
	NodeID         string  `json:"nodeId"`
	Kind           string  `json:"kind"`
	ActionRevision int64   `json:"actionRevision"`
	State          string  `json:"state"`
	Outcome        string  `json:"outcome,omitempty"`
	Reason         string  `json:"reason,omitempty"`
	DispatchedAt   *string `json:"dispatchedAt"`
	ResolvedAt     *string `json:"resolvedAt"`
}

type nightBackgroundAudioWire struct {
	State           string                         `json:"state"`
	Reason          string                         `json:"reason"`
	Steps           []nightBackgroundAudioStepWire `json:"steps"`
	PinnedMaxGainDb *float64                       `json:"pinnedMaxGainDb,omitempty"`
}

type nightSessionStateWire struct {
	ID             string `json:"id"`
	ConfigObjectID string `json:"configObjectId"`
	ConfigRevision int64  `json:"configRevision"`

	State          string `json:"state"`
	StateEnteredAt string `json:"stateEnteredAt"`
	Cycle          int64  `json:"cycle"`

	FinalShowRequested   bool    `json:"finalShowRequested"`
	FinalShowRequestedAt *string `json:"finalShowRequestedAt"`
	AdmissionClosed      bool    `json:"admissionClosed"`
	AdmissionClosedAt    *string `json:"admissionClosedAt"`
	ShutdownIntent       string  `json:"shutdownIntent"`

	ArmedShowID   string `json:"armedShowId"`
	ShowCommitted bool   `json:"showCommitted"`

	Readiness nightReadinessWire `json:"readiness"`

	PowerPhase nightPhaseEvidenceWire `json:"powerPhase"`
	Transition nightPhaseEvidenceWire `json:"transition"`

	Cues nightCuesWire `json:"cues"`

	BackgroundAudio nightBackgroundAudioWire `json:"backgroundAudio"`

	Degraded            bool   `json:"degraded"`
	DegradedReason      string `json:"degradedReason,omitempty"`
	AttributionDegraded bool   `json:"attributionDegraded"`

	UpdatedAt string `json:"updatedAt"`
}

type nightSessionLifecycleResponse struct {
	ServerTime time.Time             `json:"serverTime"`
	Session    nightSessionStateWire `json:"session"`
}

type nightCommandResultWire struct {
	Command             string `json:"command"`
	Outcome             string `json:"outcome"`
	Reason              string `json:"reason,omitempty"`
	AttributionDegraded bool   `json:"attributionDegraded"`
}

// nightCommandOverrideWire is one entry of the POST
// /night/commands/{command} request body's optional
// "interlockOverrides" array (Track F seam F6, RESTING-MODE.md §10.1).
type nightCommandOverrideWire struct {
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
}

type nightCommandResponseWire struct {
	ServerTime time.Time              `json:"serverTime"`
	Command    nightCommandResultWire `json:"command"`
	Session    nightSessionStateWire  `json:"session"`
}

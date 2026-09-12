package v1

// This file is the wire contract for a long-run program-to-LTC drift
// recording. See api/openapi.yaml for the full route contract.

// AudioAlignmentRun is one run.
type AudioAlignmentRun struct {
	ID                   string  `json:"id"`
	NodeID               string  `json:"nodeId"`
	StartedAt            string  `json:"startedAt"`
	StoppedAt            *string `json:"stoppedAt"`
	StartedBy            string  `json:"startedBy"`
	StartedByPrincipalID string  `json:"startedByPrincipalId"`
	StoppedBy            *string `json:"stoppedBy"`
	StoppedByPrincipalID *string `json:"stoppedByPrincipalId"`
	StopReason           *string `json:"stopReason"`
}

// AudioAlignmentSample is one recorded sample.
type AudioAlignmentSample struct {
	SampledAt string  `json:"sampledAt"`
	OffsetMs  float64 `json:"offsetMs"`
	SessionID string  `json:"sessionId"`
}

// AudioAlignmentRunSummary is computed from a run's samples, never
// stored. DriftRateMsPerHour is nil, with DriftRateUnavailableReason
// set, when fewer than two samples exist.
type AudioAlignmentRunSummary struct {
	SampleCount           int      `json:"sampleCount"`
	FirstSampleAt         *string  `json:"firstSampleAt"`
	LastSampleAt          *string  `json:"lastSampleAt"`
	MaxExcursionOffsetMs  *float64 `json:"maxExcursionOffsetMs"`
	MaxExcursionSampledAt *string  `json:"maxExcursionSampledAt"`
	DriftRateMsPerHour    *float64 `json:"driftRateMsPerHour"`

	// DriftRateUnavailableReason is set only when DriftRateMsPerHour is nil.
	DriftRateUnavailableReason string `json:"driftRateUnavailableReason,omitempty"`
}

// AudioAlignmentRunStopRequest is the optional body of the stop route.
type AudioAlignmentRunStopRequest struct {
	Reason string `json:"reason,omitempty"`
}

// AudioAlignmentRunResponse is the start and stop routes' response.
type AudioAlignmentRunResponse struct {
	ServerTime string            `json:"serverTime"`
	Run        AudioAlignmentRun `json:"run"`
}

// AudioAlignmentRunListResponse is the list route's response.
type AudioAlignmentRunListResponse struct {
	ServerTime string              `json:"serverTime"`
	Runs       []AudioAlignmentRun `json:"runs"`
}

// AudioAlignmentRunDetailResponse is the get-one-run route's response:
// the run, up to `limit` of its samples in ascending sampledAt order,
// whether that page dropped later samples (Truncated), and the summary
// (always computed over the run's full series, regardless of Truncated).
type AudioAlignmentRunDetailResponse struct {
	ServerTime string                   `json:"serverTime"`
	Run        AudioAlignmentRun        `json:"run"`
	Samples    []AudioAlignmentSample   `json:"samples"`
	Truncated  bool                     `json:"truncated"`
	Summary    AudioAlignmentRunSummary `json:"summary"`
}

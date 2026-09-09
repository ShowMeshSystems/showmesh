package v1

// This file is the wire contract for the nine audio.session.* dispatch
// endpoints. Mirrors RenderCommandResult's shape (evidence-based
// outcome, never a bare 200-means-success), narrowed to what
// pkg/audio.OutcomeResult's own vocabulary reports.

// AudioSessionCommandRequest is the body every audio.session.* dispatch
// endpoint accepts. Revision and Params go through
// pkg/audio.RevisionState on the node; Params carries whatever fields
// that specific operation needs (apply's sourceRole/media/playlist/
// outputs, seek's positionMs) — see internal/agent's
// parseApplyRequest/seekSession for the exact shape each operation reads.
type AudioSessionCommandRequest struct {
	Revision       uint64         `json:"revision"`
	IdempotencyKey string         `json:"idempotencyKey,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
}

// AudioSessionCommandResult is the outcome of dispatching one
// audio.session.* operation to a node — Outcome is only ever "started",
// "position", "stopped", or "completed" when evidence dated at or after
// DispatchedAt corroborates it; every other case (refused, failed,
// unconfirmable) reports Reason.
type AudioSessionCommandResult struct {
	CommandID      string `json:"commandId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Action         string `json:"action"`
	NodeID         string `json:"nodeId"`
	SessionID      string `json:"sessionId"`
	Replay         bool   `json:"replay"`

	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`

	DispatchedAt string  `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt"`

	// AttributionDegraded is true when this command's dispatch could not
	// record its ADR-024 audit entry atomically with the command and
	// proceeded anyway under the stop/clear/mute safety-class exemption
	// (ADR-024 decision 11) — mirrors FPPCommandResult/
	// ResolumeActionResult.AttributionDegraded exactly.
	AttributionDegraded bool `json:"attributionDegraded"`
}

// AudioSessionCommandResponse wraps AudioSessionCommandResult with the
// standard serverTime envelope (contract section 6.2).
type AudioSessionCommandResponse struct {
	ServerTime string                    `json:"serverTime"`
	Command    AudioSessionCommandResult `json:"command"`
}

// AlignedAudioStartRequest is the body of
// POST /audio/sessions/{sessionId}/aligned-start: start one session on
// several nodes at ONE instant on the shared media clock.
type AlignedAudioStartRequest struct {
	Revision       uint64   `json:"revision"`
	IdempotencyKey string   `json:"idempotencyKey"`
	NodeIDs        []string `json:"nodeIds"`
}

// AlignedAudioStartSelection is the chosen start instant and the evidence
// behind it, absent when no instant could be chosen.
//
// ScheduledAtNs is a JSON int64 around 1.79e18, past IEEE-754 double's
// exact integer range: a client that parses this body with a stock JSON
// parser rounds it.
type AlignedAudioStartSelection struct {
	ScheduledAtNs int64  `json:"scheduledAtNs"`
	ClockNodeID   string `json:"clockNodeId"`
	LeadNs        int64  `json:"leadNs"`

	PrerollNs         int64 `json:"prerollNs"`
	PrerollReportedBy int   `json:"prerollReportedBy"`
	DeliveryBoundNs   int64 `json:"deliveryBoundNs"`
	MarginNs          int64 `json:"marginNs"`

	// ClockErrorBoundKnown false means the clock's error bound was
	// UNKNOWN and no allowance for it is included, NOT that it was zero.
	ClockErrorBoundKnown bool  `json:"clockErrorBoundKnown"`
	ClockErrorBoundNs    int64 `json:"clockErrorBoundNs"`
}

// AlignedAudioStartResponse reports the prepare and start results per
// node alongside the selection.
//
// Aligned is false when no start instant could be chosen. Every node was
// then started UNSCHEDULED, on arrival, exactly as before this endpoint
// existed, and UnalignedReason says why. A caller must not read
// aligned=false as a failure, nor as an aligned start.
type AlignedAudioStartResponse struct {
	ServerTime string `json:"serverTime"`
	SessionID  string `json:"sessionId"`

	Aligned         bool                        `json:"aligned"`
	UnalignedReason string                      `json:"unalignedReason"`
	Selection       *AlignedAudioStartSelection `json:"selection"`

	Prepares []AudioSessionCommandResult `json:"prepares"`
	Starts   []AudioSessionCommandResult `json:"starts"`
}

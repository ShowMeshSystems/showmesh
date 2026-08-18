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
}

// AudioSessionCommandResponse wraps AudioSessionCommandResult with the
// standard serverTime envelope (contract section 6.2).
type AudioSessionCommandResponse struct {
	ServerTime string                    `json:"serverTime"`
	Command    AudioSessionCommandResult `json:"command"`
}

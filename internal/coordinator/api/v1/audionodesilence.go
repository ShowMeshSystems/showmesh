package v1

// This file is the wire contract for POST /nodes/{nodeId}/audio/silence,
// the one node-scoped audio.* dispatch endpoint: no sessionId, no
// revision, no per-command single outcome. It reports every session the
// node's agent silenced (internal/agent/audio.Manager.SilenceAll) rather
// than one AudioSessionCommandResult's own single sessionId/outcome pair.

// AudioNodeSilenceRequest is the body POST /nodes/{nodeId}/audio/silence
// accepts. audio.node.silence takes no params of its own
// (internal/agent/audionodesilenceops.go), so the only field is an
// optional idempotencyKey.
type AudioNodeSilenceRequest struct {
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// AudioNodeSilenceSessionResult is one session's own outcome, as the
// agent's silenceNode reported it (internal/agent/audionodesilenceops.go)
// mirroring internal/agent/audio.SessionSilenceOutcome field for field.
type AudioNodeSilenceSessionResult struct {
	SessionID string `json:"sessionId"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason"`
}

// AudioNodeSilenceResult is the outcome of dispatching audio.node.silence
// to a node. Outcome/Reason are the wire-level result the agent itself
// reported (confirmed/unconfirmed/refused/failed, mirroring
// mqttproto.ResultPayload.Outcome exactly) rather than a per-session
// outcome word: a node whose agent predates this operation reports
// "refused" here with its own refusal reason, never flattened into a
// generic failure.
type AudioNodeSilenceResult struct {
	CommandID      string `json:"commandId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Action         string `json:"action"`
	NodeID         string `json:"nodeId"`
	Replay         bool   `json:"replay"`

	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`

	SessionsFound int                             `json:"sessionsFound"`
	Sessions      []AudioNodeSilenceSessionResult `json:"sessions"`

	DispatchedAt string  `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt"`

	// AttributionDegraded mirrors AudioSessionCommandResult.
	// AttributionDegraded exactly: true when this command's dispatch
	// proceeded under ADR-024 decision 11's safety-class exemption
	// (audioSafetyExemptActions) after an audit-write failure.
	AttributionDegraded bool `json:"attributionDegraded"`
}

// AudioNodeSilenceResponse wraps AudioNodeSilenceResult with the standard
// serverTime envelope (contract section 6.2).
type AudioNodeSilenceResponse struct {
	ServerTime string                 `json:"serverTime"`
	Command    AudioNodeSilenceResult `json:"command"`
}

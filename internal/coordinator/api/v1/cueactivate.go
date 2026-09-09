package v1

// CueActivateResponse is the body of POST /api/v1/cues/{id}/activate:
// an operator hand-firing one Cue directly from Live Control,
// outside the automatic FPP-observation-driven activation loop. 202,
// never 200 - the request is accepted and each node's own outcome below
// is this coordinator's own evidence, gathered synchronously before this
// response is written, never a bare "it was published" claim.
type CueActivateResponse struct {
	ServerTime string `json:"serverTime"`
	CueID      string `json:"cueId"`
	// Nodes is one outcome per node participating in CueID, never a
	// single collapsed verdict: a Cue's outputs may resolve on several
	// nodes, and one node's refusal is never evidence about another's.
	Nodes []CueActivationNodeOutcome `json:"nodes"`
}

// CueActivationNodeOutcome is one node's own cue.activate dispatch
// outcome, in the shared "confirmed" | "unconfirmed" | "refused" |
// "failed" vocabulary (ADR-020) every other command route on this API
// already reports outcomes in.
type CueActivationNodeOutcome struct {
	NodeID     string `json:"nodeId"`
	Dispatched bool   `json:"dispatched"`
	Confirmed  bool   `json:"confirmed"`
	Outcome    string `json:"outcome"`
	// OutcomeReason is always non-empty except when Outcome is
	// "confirmed", the same rule [ActionInvocationResult.OutcomeReason]
	// already follows.
	OutcomeReason string `json:"outcomeReason,omitempty"`
}

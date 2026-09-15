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

	// Aligned is ADR-049 decision 3's own verdict: true when this Cue
	// reached at most one audio-bearing node (nothing to align, per that
	// ADR's "a Cue reaching one node behaves exactly as today"), or when
	// it reached more than one and the coordinator chose one shared start
	// instant for all of them. False only when more than one audio-
	// bearing node was reached and no usable media-clock reading could be
	// obtained: every one of those nodes still started, on arrival,
	// never reported as a synchronized success it did not reach.
	Aligned bool `json:"aligned"`
	// UnalignedReason is the concrete reason, non-empty only when Aligned
	// is false.
	UnalignedReason string `json:"unalignedReason,omitempty"`
	// ScheduledAtNs is the shared start instant every audio-bearing node
	// was started at, present only when Aligned is true AND scheduling
	// was actually attempted (more than one audio-bearing node); a
	// single-audio-node or render-only Cue never sets it. Around 1.79e18
	// nanoseconds, past IEEE-754 double's exact integer range (9.007e15):
	// a client parsing this body with a stock JSON parser rounds it.
	ScheduledAtNs *int64 `json:"scheduledAtNs,omitempty"`
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

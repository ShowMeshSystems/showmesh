package v1

// ActionInvocationRequest is the body of POST /actions/{id}/invocations.
// IdempotencyKey is required. RequestedRevision optionally pins the
// exact show.action revision to execute: a durable/queued caller (e.g. a
// Track F cue) should always set it, so activating a newer revision
// after the cue was queued never changes what runs. An interactive
// caller may omit it to mean "whichever revision is active right now" —
// the response's Revision field always states which revision actually
// ran, so an interactive caller still learns it.
type ActionInvocationRequest struct {
	IdempotencyKey    string `json:"idempotencyKey"`
	RequestedRevision *int64 `json:"requestedRevision,omitempty"`
}

// ActionInvocationResult reports one invocation's lifecycle and, once
// resolved, its outcome (ADR-020 decision 5: absent evidence is stated
// with a state and a reason, never omitted).
//
// State is always "pending" or "resolved". Outcome is null while State
// is "pending" and one of the five terminal words (confirmed,
// unconfirmed, unconfirmable, refused, failed) once resolved — a pending
// result never carries a blank outcome pretending to be one.
// OutcomeReason is always non-empty, in both states.
//
// DispatchAttribution and OutcomeAttribution independently name whether
// the dispatch-time and outcome-time audit records are known-complete or
// degraded (each "pending", "complete", or "degraded", with its own
// reason), replacing a single aggregate boolean that could not
// distinguish the two. AttributionDegraded is derived — true iff either
// attribution above is "degraded" — and kept only for a caller that has
// not moved to the named states.
type ActionInvocationResult struct {
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotencyKey"`
	ActionID       string `json:"actionId"`
	// Revision is the show.action revision that actually executed —
	// either the caller's own RequestedRevision, or whichever revision
	// was active at dispatch time when the request named none.
	Revision int64  `json:"revision"`
	Label    string `json:"label,omitempty"`
	Replay   bool   `json:"replay"`

	State         string  `json:"state"`
	Outcome       *string `json:"outcome"`
	OutcomeReason string  `json:"outcomeReason"`

	DispatchAttribution       string `json:"dispatchAttribution"`
	DispatchAttributionReason string `json:"dispatchAttributionReason"`
	OutcomeAttribution        string `json:"outcomeAttribution"`
	OutcomeAttributionReason  string `json:"outcomeAttributionReason"`
	AttributionDegraded       bool   `json:"attributionDegraded"`

	DispatchedAt *string `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt"`
}

// ActionInvocationResponse is the body of a successful
// POST /actions/{id}/invocations.
type ActionInvocationResponse struct {
	ServerTime string                 `json:"serverTime"`
	Result     ActionInvocationResult `json:"result"`
}

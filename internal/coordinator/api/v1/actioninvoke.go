package v1

// ActionInvocationRequest is the body of POST /actions/{id}/invocations.
// IdempotencyKey is the only field: the stored action's own target
// supplies every parameter, so a caller cannot pass a protocol address,
// topic, or path here.
type ActionInvocationRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

// ActionInvocationResult reports one invocation's outcome against this
// API's shared five-word vocabulary (confirmed, unconfirmed,
// unconfirmable, refused, failed). OutcomeReason is always non-empty.
// Replay is true when IdempotencyKey named an already-dispatched
// invocation.
type ActionInvocationResult struct {
	ID                  string  `json:"id"`
	IdempotencyKey      string  `json:"idempotencyKey"`
	ActionID            string  `json:"actionId"`
	Label               string  `json:"label,omitempty"`
	Replay              bool    `json:"replay"`
	Outcome             string  `json:"outcome"`
	OutcomeReason       string  `json:"outcomeReason"`
	AttributionDegraded bool    `json:"attributionDegraded"`
	DispatchedAt        *string `json:"dispatchedAt"`
	ResolvedAt          *string `json:"resolvedAt"`
}

// ActionInvocationResponse is the body of a successful
// POST /actions/{id}/invocations.
type ActionInvocationResponse struct {
	ServerTime string                 `json:"serverTime"`
	Result     ActionInvocationResult `json:"result"`
}

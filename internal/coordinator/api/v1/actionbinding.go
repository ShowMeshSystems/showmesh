package v1

// The three members of ActionBinding.state. "unknown" is not a soft
// "ok": it means the check could not be performed at all.
const (
	ActionBindingStateOK      = "ok"
	ActionBindingStateBroken  = "broken"
	ActionBindingStateUnknown = "unknown"
)

// ActionBinding is one show.action's binding-check result. Reason is
// always non-empty, including for "ok" — every state carries a reason,
// never a bare word.
type ActionBinding struct {
	ActionID string `json:"actionId"`
	Label    string `json:"label"`
	Show     string `json:"show"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
}

// ActionBindingResponse is the body of GET /actions/{id}/binding.
type ActionBindingResponse struct {
	ServerTime string        `json:"serverTime"`
	Binding    ActionBinding `json:"binding"`
}

// ActionBindingsResponse is the body of GET /actions/bindings.
type ActionBindingsResponse struct {
	ServerTime string          `json:"serverTime"`
	Bindings   []ActionBinding `json:"bindings"`
}

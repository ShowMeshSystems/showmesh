package v1

// FPPTransitionGainRequest is the body of
// POST /api/v1/fpp/brightness/transition-gain: the operator write of the
// brightness transition gain to every FPP,
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 2.2.
//
// TargetPercent and FadeSeconds are pointers so the handler can tell an
// absent field from an explicit null from a real zero: 0 is a legitimate
// value for both (blackout, and an immediate apply), so a plain int would
// make a forgotten field indistinguishable from a deliberate blackout.
//
// Out of range is refused, never clamped (contract section 2.2), so a
// mistyped value stays visible instead of silently becoming a value the
// operator did not ask for.
type FPPTransitionGainRequest struct {
	// TargetPercent is the gain, 0-100 inclusive. It is a multiplier over
	// FPP's own scheduled ceiling, never the ceiling itself.
	TargetPercent *int `json:"targetPercent"`

	// FadeSeconds is the fade duration, 0-86400 inclusive; 0 applies
	// immediately.
	FadeSeconds *int `json:"fadeSeconds"`

	// RequestID is the caller-minted idempotency key. Optional: a caller
	// that supplies one can retry an unanswered request safely, because a
	// repeat of an id already applied changes nothing and reports the
	// state as it stands. Omitted, the coordinator mints a fresh one, and
	// the request is then a new fade rather than a retry of any earlier
	// one.
	RequestID string `json:"requestId,omitempty"`
}

// FPPTransitionGainResponse is the 200 body of
// POST /api/v1/fpp/brightness/transition-gain: one result per FPP.
type FPPTransitionGainResponse struct {
	ServerTime string                    `json:"serverTime"`
	Players    []FPPTransitionGainResult `json:"players"`
}

// One FPP's transition gain outcome.
const (
	FPPTransitionGainApplied        = "applied"
	FPPTransitionGainAlreadyApplied = "alreadyApplied"
	FPPTransitionGainFailed         = "failed"
)

// FPPTransitionGainResult is one FPP's outcome. The numbers are the plugin's
// applied state (contract section 2.2), present unless the write failed.
// alreadyApplied is a success: that requestId already took on this FPP.
type FPPTransitionGainResult struct {
	InstanceID string `json:"instanceId"`
	RequestID  string `json:"requestId"`
	Outcome    string `json:"outcome"`
	// Reason is an operator sentence, present when the write failed.
	Reason string `json:"reason,omitempty"`

	GainStart   *int `json:"gainStart,omitempty"`
	GainTarget  *int `json:"gainTarget,omitempty"`
	FadeSeconds *int `json:"fadeSeconds,omitempty"`
	// Ceiling is the FPP's ceiling and EffectiveOutput what reaches its channels.
	Ceiling         *int `json:"ceiling,omitempty"`
	EffectiveOutput *int `json:"effectiveOutput,omitempty"`
}

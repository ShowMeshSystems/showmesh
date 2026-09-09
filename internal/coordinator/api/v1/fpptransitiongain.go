package v1

// FPPTransitionGainRequest is the body of
// POST /api/v1/fpp/{instanceId}/brightness/transition-gain: the operator
// write of one FPP host's brightness transition gain,
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

// FPPTransitionGainResponse is the body of a successful (200) response
// from POST /api/v1/fpp/{instanceId}/brightness/transition-gain. See
// [NodeResponse]'s doc comment for why ServerTime is present with no
// exception.
type FPPTransitionGainResponse struct {
	ServerTime     string                  `json:"serverTime"`
	TransitionGain FPPTransitionGainResult `json:"transitionGain"`
}

// FPPTransitionGainResult is the applied state contract section 2.2
// requires the plugin to return, carried through to the caller unchanged.
// It exists so a client has evidence of what the FPP host is now doing
// rather than only "the request did not error".
type FPPTransitionGainResult struct {
	InstanceID string `json:"instanceId"`

	// RequestID is the idempotency key this write actually carried,
	// whether the caller supplied it or the coordinator minted it. Echoed
	// so a caller that let the coordinator mint one can still retry with
	// the same key.
	RequestID string `json:"requestId"`

	// Applied is false for an idempotent repeat of a requestId already
	// applied. That is a SUCCESS, not a failure: nothing was changed and
	// the fields below report the gain as it stands. A client that treats
	// false as an error will retry a write that already took.
	Applied bool `json:"applied"`

	// GainStart and GainTarget bound the fade the plugin is now running.
	GainStart   int `json:"gainStart"`
	GainTarget  int `json:"gainTarget"`
	FadeSeconds int `json:"fadeSeconds"`

	// Ceiling is FPP's own scheduled ceiling as the plugin sees it, and
	// EffectiveOutput is round(ceiling * gain / 100) - the value actually
	// reaching the channels. Both are carried because the gain alone does
	// not say what the audience sees.
	Ceiling         int `json:"ceiling"`
	EffectiveOutput int `json:"effectiveOutput"`
}

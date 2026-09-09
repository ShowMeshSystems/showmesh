package v1

// FPPDefinitionRepublishRequest is the body of
// POST /api/v1/fpp/{instanceId}/playlist-definitions/republish: the
// operator ask that one FPP host's plugin resend its playlist definitions,
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 3.9.
//
// It carries nothing but an optional idempotency key, because the request
// has no parameters: a republish is all of that host's definitions or
// none. The plugin's own body adds a schemaVersion, which the coordinator
// supplies rather than the caller.
type FPPDefinitionRepublishRequest struct {
	// RequestID is the caller-minted idempotency key. Optional: supply one
	// and a retry of an unanswered request is safe, because a repeat of an
	// id the plugin already applied clears nothing and reports the state
	// as it stands. Reusing it later is also how a caller learns the
	// resend finished. Omitted, the coordinator mints one and echoes it.
	RequestID string `json:"requestId,omitempty"`
}

// FPPDefinitionRepublishResponse is the body of a successful (200)
// response from
// POST /api/v1/fpp/{instanceId}/playlist-definitions/republish. See
// [NodeResponse]'s doc comment for why ServerTime is present with no
// exception.
type FPPDefinitionRepublishResponse struct {
	ServerTime string                       `json:"serverTime"`
	Republish  FPPDefinitionRepublishResult `json:"republish"`
}

// FPPDefinitionRepublishResult is the plugin's own evidence, carried
// through unchanged.
//
// Read what it says and not more. It reports that the plugin agreed to
// resend, what it dropped from its published-set, what it still holds,
// what it deliberately kept back, and that a sweep is OWED. It carries no
// count of definitions the coordinator accepted, and cannot: when the
// plugin answers, not one post of that sweep has been attempted. Whether
// the definitions arrived is read from the coordinator's own stored
// definitions, GET /api/v1/integrations/fpp/playlist-definitions, which is
// authoritative because the coordinator computed those hashes itself.
type FPPDefinitionRepublishResult struct {
	InstanceID string `json:"instanceId"`

	// RequestID is the idempotency key this request actually carried,
	// whether the caller supplied it or the coordinator minted it. Echoed
	// so a caller can retry with it, and so it can be sent again later to
	// read SweepPending as it stands.
	RequestID string `json:"requestId"`

	// Applied is false for an idempotent repeat of a requestId the plugin
	// already applied. That is a SUCCESS, not a failure: nothing was
	// cleared a second time and the fields below report the state as it
	// stands.
	Applied bool `json:"applied"`

	// DefinitionsCleared is how many stored definitions the plugin dropped
	// from its published-set, so it will send them again even though their
	// content, and therefore their hash, has not changed. 0 on a repeat.
	DefinitionsCleared int `json:"definitionsCleared"`

	// DefinitionsHeld is how many the plugin still records as published as
	// it answered: 0 immediately after an applied clear, and on a repeat
	// how many the sweep has already re-sent and had accepted.
	DefinitionsHeld int `json:"definitionsHeld"`

	// DefinitionsRefusedTerminally is how many the plugin holds as
	// terminally refused and deliberately did not clear, because re-sending
	// those bytes gets the identical refusal until the plugin restarts.
	// This route will not re-send them, which is what an operator needs
	// when the playlist they were chasing is still missing afterwards.
	DefinitionsRefusedTerminally int `json:"definitionsRefusedTerminally"`

	// SweepPending is whether the resend is still owed. True on an applied
	// answer, always: the plugin performs the sweep on its own worker, and
	// no post of it has been attempted when this answers. It goes false
	// only on a later repeat of the same requestId, which is how a caller
	// learns the sweep finished. It says the sending finished, never that
	// the coordinator accepted what was sent.
	SweepPending bool `json:"sweepPending"`
}

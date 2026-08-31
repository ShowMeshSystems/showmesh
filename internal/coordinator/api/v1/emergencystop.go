package v1

// The installation-wide emergency-stop surface. Three trigger
// levels — stop, stop-power-down, hard-stop — each stop playout
// immediately and each carries its own optional, best-effort follow-up
// actions. hard-stop additionally requires the arm/fire deliberate-intent
// pair below; stop and stop-power-down dispatch directly.
//
// EmergencyStopResult.StopOutcomes and .FollowUps are reported as two
// SEPARATE arrays, with no single aggregate success flag folding them
// together: a follow-up action's own failure must never read, on the
// wire, as "the stop did not happen" (this build's own degrade-safely
// rule). A caller's exit code is driven by StopOutcomes alone.

// ConfigEmergencyStopLevelPayload is one level's own optional, ordered
// follow-up action list — see [ConfigEmergencyStopPayload].
type ConfigEmergencyStopLevelPayload struct {
	Actions []string `json:"actions"`
}

// ConfigEmergencyStopPayload is the "show.emergencystop" configuration
// kind's decoded payload: the body PUT /config/show.emergencystop accepts
// (a full replacement — all three level keys required, each with its own
// required, possibly empty actions list), and the "payload" member of
// GET /config/show.emergencystop's response.
type ConfigEmergencyStopPayload struct {
	Stop          ConfigEmergencyStopLevelPayload `json:"stop"`
	StopPowerDown ConfigEmergencyStopLevelPayload `json:"stopPowerDown"`
	HardStop      ConfigEmergencyStopLevelPayload `json:"hardStop"`
}

// EmergencyStopConfigResponse is the body of GET and PUT
// /config/show.emergencystop. revision 0 / source "default" means nothing
// has ever been written and payload carries the built-in default (every
// level configured with no follow-up actions).
type EmergencyStopConfigResponse struct {
	ServerTime             string                     `json:"serverTime"`
	Kind                   string                     `json:"kind"`
	Revision               int64                      `json:"revision"`
	Payload                ConfigEmergencyStopPayload `json:"payload"`
	UpdatedAt              string                     `json:"updatedAt"`
	CreatedByPrincipalID   *string                    `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                    `json:"createdByPrincipalName"`
	Source                 string                     `json:"source"`
}

// EmergencyStopRequest is the body of POST .../emergency-stop/stop and
// POST .../emergency-stop/stop-power-down.
type EmergencyStopRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

// EmergencyStopInstanceOutcome is one configured FPP instance's own stop
// dispatch outcome, in [command]'s shared five-word vocabulary.
type EmergencyStopInstanceOutcome struct {
	InstanceID    string  `json:"instanceId"`
	Outcome       string  `json:"outcome"`
	OutcomeReason string  `json:"outcomeReason"`
	DispatchedAt  *string `json:"dispatchedAt"`
	Replay        bool    `json:"replay"`
}

// EmergencyStopFollowUpResult is one configured follow-up show.action's
// own best-effort invocation outcome. Outcome is empty (never a
// zero-value word) when the action id itself could not even be resolved —
// OutcomeReason always explains why.
type EmergencyStopFollowUpResult struct {
	ActionID      string `json:"actionId"`
	Label         string `json:"label,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	OutcomeReason string `json:"outcomeReason"`
}

// EmergencyStopNightSessionOutcome reports what, if anything, happened to
// the active night session as level stop-power-down's own "standard
// graceful shutdown" component. Present is false when no night session
// was active — a real, valid, non-degraded outcome, not an error.
type EmergencyStopNightSessionOutcome struct {
	Present   bool   `json:"present"`
	SessionID string `json:"sessionId,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
}

// EmergencyStopResult is the shared result shape every trigger route
// (stop, stop-power-down, and hard-stop's own fire) answers with.
type EmergencyStopResult struct {
	Level          string                            `json:"level"`
	IdempotencyKey string                            `json:"idempotencyKey"`
	StopOutcomes   []EmergencyStopInstanceOutcome    `json:"stopOutcomes"`
	NightSession   *EmergencyStopNightSessionOutcome `json:"nightSession,omitempty"`
	FollowUps      []EmergencyStopFollowUpResult     `json:"followUps"`
}

// EmergencyStopResponse is the body of POST .../emergency-stop/stop,
// POST .../emergency-stop/stop-power-down, and
// POST .../emergency-stop/hard-stop/fire.
type EmergencyStopResponse struct {
	ServerTime string              `json:"serverTime"`
	Result     EmergencyStopResult `json:"result"`
}

// EmergencyStopArmRequest is the body of
// POST .../emergency-stop/hard-stop/arm. Arming has no side effect on the
// show itself; idempotencyKey exists only for this endpoint's own
// request-shape consistency with every other write in this contract, not
// because arming needs replay protection the way fire does.
type EmergencyStopArmRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

// EmergencyStopArmResponse carries the single-use token
// POST .../emergency-stop/hard-stop/fire must present within expiresAt.
// Arming again before that deadline invalidates THIS token immediately —
// at most one live token per principal, so a caller cannot accumulate a
// pocketful of valid tokens and fire on an act that is no longer recent.
type EmergencyStopArmResponse struct {
	ServerTime string `json:"serverTime"`
	ArmToken   string `json:"armToken"`
	ExpiresAt  string `json:"expiresAt"`
}

// EmergencyStopFireRequest is the body of
// POST .../emergency-stop/hard-stop/fire.
type EmergencyStopFireRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	ArmToken       string `json:"armToken"`
}

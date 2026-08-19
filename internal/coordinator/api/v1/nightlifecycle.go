package v1

// Wire types for Track F seam F2's night-session lifecycle controller
// (RESTING-MODE.md §3/§4/§11, ADR-038). Distinct from nightsession.go's
// config-kind types, which are the authored definition a session pins,
// not the running session itself.

// NightEvidenceState follows ADR-020: a missing field states a state and
// a reason rather than being omitted. "not_available" marks a field this
// seam never fills (Track F seam F3's job), distinct from "unknown"
// (nothing has happened yet to produce a value).
type NightEvidenceState string

const (
	NightEvidenceRecorded      NightEvidenceState = "recorded"
	NightEvidenceUnknown       NightEvidenceState = "unknown"
	NightEvidenceNotConfigured NightEvidenceState = "not_configured"
	NightEvidenceNotAvailable  NightEvidenceState = "not_available"
)

// NightReadinessCheck is one named signal run-readiness evaluated — see
// the OpenAPI schema for the current, full list of checks. A healthy
// result on one check is never evidence any other check passed.
type NightReadinessCheck struct {
	Name string `json:"name"`
	// State is observation.Health's vocabulary plus "not_verifiable" for
	// a check that can never be anything else — excluded from the
	// aggregate outcome but always listed.
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// NightReadiness is the current session's most recent run-readiness
// result, or the explicit absence of one. No precomputed age field
// (ADR-020 decision 6): CompletedAt plus the envelope's own serverTime is
// what a client compares.
type NightReadiness struct {
	State       NightEvidenceState    `json:"state"`
	Reason      string                `json:"reason"`
	Outcome     string                `json:"outcome,omitempty"`
	EpochID     string                `json:"epochId,omitempty"`
	CompletedAt string                `json:"completedAt,omitempty"`
	SameEpoch   bool                  `json:"sameEpoch"`
	Fresh       bool                  `json:"fresh"`
	Checks      []NightReadinessCheck `json:"checks"`
}

// NightPhaseEvidence names one optional or not-yet-implemented section.
type NightPhaseEvidence struct {
	State  NightEvidenceState `json:"state"`
	Reason string             `json:"reason"`
}

// NightCue is one configured cue's outbox detail for the session's
// current cycle. State "not_dispatched" means no outbox row exists yet.
type NightCue struct {
	Name           string  `json:"name"`
	Phase          string  `json:"phase"` // "enterShow" | "enterResting" | "fadeOut"
	Role           string  `json:"role"`
	Action         string  `json:"action"`
	ActionRevision *int64  `json:"actionRevision"`
	State          string  `json:"state"` // "not_dispatched" | "pending" | "dispatched" | "resolved" | "ambiguous"
	Outcome        string  `json:"outcome,omitempty"`
	Reason         string  `json:"reason,omitempty"`
	DispatchedAt   *string `json:"dispatchedAt"`
	ResolvedAt     *string `json:"resolvedAt"`
}

// NightCues is the current cycle's per-cue detail, or the stated reason it
// could not be read — never a silently empty list standing in for either
// "no cues configured" or "read failed" (ADR-020).
type NightCues struct {
	State  NightEvidenceState `json:"state"`
	Reason string             `json:"reason"`
	Cues   []NightCue         `json:"cues"`
}

// NightSessionState is the full lifecycle resource.
type NightSessionState struct {
	ID             string `json:"id"`
	ConfigObjectID string `json:"configObjectId"`
	ConfigRevision int64  `json:"configRevision"`

	State          string `json:"state"`
	StateEnteredAt string `json:"stateEnteredAt"`
	Cycle          int64  `json:"cycle"`

	FinalShowRequested   bool    `json:"finalShowRequested"`
	FinalShowRequestedAt *string `json:"finalShowRequestedAt"`
	AdmissionClosed      bool    `json:"admissionClosed"`
	AdmissionClosedAt    *string `json:"admissionClosedAt"`
	ShutdownIntent       string  `json:"shutdownIntent"` // "" | "fade-out" | "power-down"

	ArmedShowID   string `json:"armedShowId"`
	ShowCommitted bool   `json:"showCommitted"`

	Readiness NightReadiness `json:"readiness"`

	PowerPhase NightPhaseEvidence `json:"powerPhase"`
	Transition NightPhaseEvidence `json:"transition"`

	Cues NightCues `json:"cues"`

	Degraded       bool   `json:"degraded"`
	DegradedReason string `json:"degradedReason,omitempty"`

	// AttributionDegraded is true when this session's most recent command
	// applied despite its audit entry failing to write (ADR-024 decision
	// 11's exemption), or when an autonomous dispatch ran with no
	// authorizing principal recorded. Never cleared once true.
	AttributionDegraded bool `json:"attributionDegraded"`

	// Authorization is who authorized this session, for provenance. The
	// night controller dispatches as its own system actor, never as this
	// principal, so this is a record of authority and not a live
	// credential.
	Authorization NightAuthorization `json:"authorization"`

	UpdatedAt string `json:"updatedAt"`
}

// NightAuthorization states the authorizing principal or the explicit
// absence of one (ADR-020: absent evidence carries a state and a reason).
type NightAuthorization struct {
	State         NightEvidenceState `json:"state"`
	Reason        string             `json:"reason,omitempty"`
	PrincipalID   string             `json:"principalId,omitempty"`
	PrincipalName string             `json:"principalName,omitempty"`
	Command       string             `json:"command,omitempty"`
	RecordedAt    *string            `json:"recordedAt"`
}

// NightSessionResponse is the body of GET /api/v1/night/session and
// GET /api/v1/night/sessions/{id}.
type NightSessionResponse struct {
	ServerTime string            `json:"serverTime"`
	Session    NightSessionState `json:"session"`
}

// NightCommandResult is one night/commands/{command} response's "command"
// member. Outcome's idempotent_no_op is a real, distinct outcome.
type NightCommandResult struct {
	Command             string `json:"command"`
	Outcome             string `json:"outcome"` // "applied" | "idempotent_no_op"
	Reason              string `json:"reason,omitempty"`
	AttributionDegraded bool   `json:"attributionDegraded"`
}

// NightCommandResponse is POST /api/v1/night/commands/{command}'s 202
// body: acceptance plus the resulting session state, never a held
// connection waiting on a downstream outcome.
type NightCommandResponse struct {
	ServerTime string             `json:"serverTime"`
	Command    NightCommandResult `json:"command"`
	Session    NightSessionState  `json:"session"`
}

// NightSessionChangedEvent is the "nightSession.changed" stream frame —
// one kind, not one per transition. The stream is not resumable, so this
// carries the full NightSessionState a GET returns, never a delta.
type NightSessionChangedEvent struct {
	Seq        uint64            `json:"seq"`
	ServerTime string            `json:"serverTime"`
	Session    NightSessionState `json:"session"`
}

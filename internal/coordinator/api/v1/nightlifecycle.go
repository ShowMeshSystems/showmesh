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

// NightReadinessCheck is one named signal run-readiness evaluated. In
// this build that is ONLY fpp.reachable for the session's own referenced
// FPP instances — see the OpenAPI schema for the full scope statement.
type NightReadinessCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"` // observation.Health vocabulary
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

	Degraded       bool   `json:"degraded"`
	DegradedReason string `json:"degradedReason,omitempty"`

	// AttributionDegraded is true when this session's most recent command
	// applied despite its audit entry failing to write (ADR-024 decision
	// 11's exemption). Never cleared once true.
	AttributionDegraded bool `json:"attributionDegraded"`

	UpdatedAt string `json:"updatedAt"`
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

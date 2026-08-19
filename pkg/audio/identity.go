package audio

import "sync"

// SessionID is a playback session's stable identity, minted by the caller.
type SessionID string

// Revision is a session's monotonic desired-state revision. A command
// whose Revision is not strictly greater than the session's current
// desired revision is refused — see [RevisionState.Apply].
type Revision uint64

// InvocationID is the caller's stable invocation identity, equal across
// every retry of one logical intent. It maps onto the ADR-008 command
// envelope's idempotency key (pkg/command.Envelope.IdempotencyKey).
type InvocationID string

// Reasons a [RevisionState] refuses an apply.
const (
	ReasonStaleRevision              = "stale_revision"
	ReasonInvalidInvocation          = "invalid_invocation"
	ReasonInvocationRevisionMismatch = "invocation_revision_mismatch"
)

// RevisionDecision is what [RevisionState.Apply] resolves to and, for a
// valid InvocationID, remembers.
type RevisionDecision struct {
	// Requested is the revision this decision was asked to apply.
	Requested Revision

	// Accepted is true when Requested became the session's current
	// desired revision.
	Accepted bool

	// Revision is the session's desired revision after this decision:
	// Requested when Accepted, otherwise the unchanged prior current
	// revision.
	Revision Revision

	// Result is nil when Accepted, and a refused [OutcomeResult]
	// otherwise. Acceptance is not itself a confirmed Outcome — a
	// caller confirms the operation's own outcome separately once
	// dispatched.
	Result *OutcomeResult
}

// RevisionState enforces this package's anti-rewind and idempotent-replay
// rules for one session.
type RevisionState struct {
	mu          sync.Mutex
	session     SessionID
	current     Revision
	invocations map[InvocationID]RevisionDecision
}

// NewRevisionState returns a RevisionState for session with no
// invocations seen and a current desired revision of 0.
func NewRevisionState(session SessionID) *RevisionState {
	return &RevisionState{session: session, invocations: make(map[InvocationID]RevisionDecision)}
}

// RestoreRevisionState rebuilds a RevisionState from persisted state:
// current is the session's last known desired revision and prior is
// every invocation decision recorded before the restart. prior is copied
// defensively and never aliased by the returned state.
func RestoreRevisionState(session SessionID, current Revision, prior map[InvocationID]RevisionDecision) *RevisionState {
	invocations := make(map[InvocationID]RevisionDecision, len(prior))
	for id, d := range prior {
		invocations[id] = d
	}
	return &RevisionState{session: session, current: current, invocations: invocations}
}

// Session returns the SessionID this state was constructed for.
func (s *RevisionState) Session() SessionID {
	return s.session
}

// Decisions returns a defensive copy of every invocation decision
// recorded so far, for a caller that persists it (e.g. before a
// restart, to seed a later [RestoreRevisionState]).
func (s *RevisionState) Decisions() map[InvocationID]RevisionDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[InvocationID]RevisionDecision, len(s.invocations))
	for id, d := range s.invocations {
		cp[id] = d
	}
	return cp
}

func refusal(requested, current Revision, reason string) RevisionDecision {
	return RevisionDecision{
		Requested: requested,
		Accepted:  false,
		Revision:  current,
		Result:    &OutcomeResult{Outcome: OutcomeRefused, Reason: reason},
	}
}

// Apply resolves one apply attempt. An empty invocation is refused with
// [ReasonInvalidInvocation] and nothing is recorded. A previously seen
// invocation replayed with the same Requested returns its original
// decision unchanged; replayed with a different Requested it is refused
// with [ReasonInvocationRevisionMismatch]. A new invocation whose
// requested revision is not strictly greater than current is refused
// with [ReasonStaleRevision]; otherwise current advances and the
// decision is Accepted.
func (s *RevisionState) Apply(invocation InvocationID, requested Revision) RevisionDecision {
	s.mu.Lock()
	defer s.mu.Unlock()

	if invocation == "" {
		return refusal(requested, s.current, ReasonInvalidInvocation)
	}

	if prior, seen := s.invocations[invocation]; seen {
		if prior.Requested != requested {
			return refusal(requested, s.current, ReasonInvocationRevisionMismatch)
		}
		return prior
	}

	var d RevisionDecision
	if requested > s.current {
		s.current = requested
		d = RevisionDecision{Requested: requested, Accepted: true, Revision: requested}
	} else {
		d = refusal(requested, s.current, ReasonStaleRevision)
	}
	s.invocations[invocation] = d
	return d
}

// Current returns the session's current desired revision.
func (s *RevisionState) Current() Revision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

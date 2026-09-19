package audio

import (
	"context"
	"sync"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// SessionSilenceOutcome is one session's result from [Manager.SilenceAll].
type SessionSilenceOutcome struct {
	ID      pkgaudio.SessionID
	Outcome pkgaudio.OutcomeResult
}

// SilenceAll stops every session this Manager currently holds, bypassing
// [Session.dispatch]'s revState ledger entirely and reporting one
// outcome per session. Stops rather than clears, and is idempotent, an
// already-stopped session's outcome is Stopped again, never an error.
//
// Nothing is ever resumed or faded up here: every session in this call
// is being silenced in the same pass, so restoring a duck or an
// interrupt hold with a real Engine.Resume/Start would make the
// emergency stop start or brighten audio it exists to kill. Duck and
// interrupt membership is dropped directly, with no engine call.
//
// Each session's engine calls run under [boundedEngineCallContext], not
// the raw ctx given here: this loop is serial, so one wedged call must
// not stall the sessions waiting behind it or the result this operation
// reports.
func (m *Manager) SilenceAll(ctx context.Context) []SessionSilenceOutcome {
	return m.silenceSessions(ctx, m.liveSessionsExcept(""))
}

// SilenceAllExcept is [Manager.SilenceAll] for every session other than
// excludeID — ADR-053 decision 7's own stop step, run after the alert
// session (excludeID) has already been started, and never confused with
// it: a weather delay alert must keep playing while every other session
// this node holds is silenced the identical way SilenceAll would silence
// it too.
func (m *Manager) SilenceAllExcept(ctx context.Context, excludeID pkgaudio.SessionID) []SessionSilenceOutcome {
	return m.silenceSessions(ctx, m.liveSessionsExcept(excludeID))
}

// liveSessionsExcept snapshots this Manager's live sessions, omitting
// excludeID (an empty id matches nothing, so [SilenceAll] excludes
// none).
func (m *Manager) liveSessionsExcept(excludeID pkgaudio.SessionID) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	sessions := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		if excludeID != "" && id == excludeID {
			continue
		}
		sessions = append(sessions, s)
	}
	return sessions
}

// silenceSessions is [SilenceAll]/[SilenceAllExcept]'s shared body.
func (m *Manager) silenceSessions(ctx context.Context, sessions []*Session) []SessionSilenceOutcome {
	results := make([]SessionSilenceOutcome, 0, len(sessions))
	for _, s := range sessions {
		s.mu.Lock()
		outcome := m.stopExecLocked(ctx, s, boundedEngineCallContext)
		outcome = s.persistOrFailLocked(outcome)
		s.mu.Unlock()

		m.dropHoldMembershipEverywhere(s.id)

		results = append(results, SessionSilenceOutcome{ID: s.id, Outcome: outcome})
	}
	return results
}

// ZeroGainExcept drives every session other than excludeID straight to
// gain zero (ADR-053 decision 7's own first step): a direct engine
// SetGain call on whatever handle is already loaded, bypassing both the
// revision ledger [Session.dispatch] enforces and the configured/
// effective-gain bookkeeping [Session.applyEffectiveGainLocked] composes
// — no pipeline teardown, no fade, and no change to a session's own
// desired gain.
//
// Each session's own engine call runs on its own goroutine, bounded by
// [boundedEngineCallContext], so one wedged or failing session's call
// never delays or fails another's — decision 7's "a failure in (a) for
// one session must not prevent (b)". This method still waits for every
// goroutine to finish (or time out) before returning: "must not wait on
// anything" is decision 7's rule against an UNBOUNDED wait, the same
// bound every other engine call in this package already carries, not a
// rule against waiting at all — unlike [Manager.SilenceAllExcept], which
// the alert this call precedes truly never waits on.
func (m *Manager) ZeroGainExcept(ctx context.Context, excludeID pkgaudio.SessionID) {
	var wg sync.WaitGroup
	for _, s := range m.liveSessionsExcept(excludeID) {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.handleLoaded {
				return
			}
			gainCtx, cancel := boundedEngineCallContext(ctx)
			defer cancel()
			_, _ = m.engine.SetGain(gainCtx, s.handle, mutedGain)
		}(s)
	}
	wg.Wait()
}

// dropHoldMembershipEverywhere removes sessionID from every other live
// session's duck and interrupt sets without an engine call: the
// SilenceAll-only counterpart to [Manager.restoreDucked]/[Manager.
// restoreInterrupted], which must not run here (see [Manager.
// SilenceAll]'s own doc comment).
func (m *Manager) dropHoldMembershipEverywhere(sessionID pkgaudio.SessionID) {
	for _, t := range m.otherSessions(sessionID) {
		t.mu.Lock()
		m.dropDuckerMembershipLocked(t, sessionID)
		m.dropInterrupterMembershipLocked(t, sessionID)
		t.mu.Unlock()
	}
}

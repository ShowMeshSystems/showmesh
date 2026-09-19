package audio

import (
	"context"
	"sync"
	"time"

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
// excludeID, ADR-053 decision 7's own stop step, run after the alert
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

// ZeroGainExcept sets every session other than excludeID to gain zero on
// its loaded engine handle, with no fade and no change to desired gain.
// It returns within engineCallTimeout even when a session's lock is held.
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
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	bound := time.NewTimer(engineCallTimeout)
	defer bound.Stop()
	select {
	case <-done:
	case <-bound.C:
	}
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

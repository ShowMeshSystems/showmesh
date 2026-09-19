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
// excludeID, so a weather delay alert keeps playing.
func (m *Manager) SilenceAllExcept(ctx context.Context, excludeID pkgaudio.SessionID) []SessionSilenceOutcome {
	return m.silenceSessions(ctx, m.liveSessionsExcept(excludeID))
}

// SilenceSession stops one session the way [Manager.SilenceAll] does,
// whatever its state and revision, and reports false when it does not exist.
func (m *Manager) SilenceSession(ctx context.Context, id pkgaudio.SessionID) (SessionSilenceOutcome, bool) {
	s, ok := m.get(id)
	if !ok {
		return SessionSilenceOutcome{}, false
	}
	return m.silenceSessions(ctx, []*Session{s})[0], true
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

// ZeroGainExcept sets every session other than excludeID to gain zero on its
// loaded and staged engine handles, all sessions at once, and returns within
// bound with the ids of the sessions it could not mute by then.
func (m *Manager) ZeroGainExcept(ctx context.Context, excludeID pkgaudio.SessionID, bound time.Duration) []pkgaudio.SessionID {
	sessions := m.liveSessionsExcept(excludeID)
	var mu sync.Mutex
	muted := make(map[pkgaudio.SessionID]bool, len(sessions))
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			if m.zeroGainSession(ctx, s) {
				mu.Lock()
				muted[s.id] = true
				mu.Unlock()
			}
		}(s)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}

	mu.Lock()
	defer mu.Unlock()
	var unmuted []pkgaudio.SessionID
	for _, s := range sessions {
		if !muted[s.id] {
			unmuted = append(unmuted, s.id)
		}
	}
	return unmuted
}

// zeroGainSession mutes s's loaded and staged handles, reporting whether
// every engine call it made succeeded.
func (m *Manager) zeroGainSession(ctx context.Context, s *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	handles := make([]EngineHandle, 0, 2)
	if s.handleLoaded {
		handles = append(handles, s.handle)
	}
	if s.stage != nil && s.stage.ready {
		handles = append(handles, s.stage.handle)
	}
	ok := true
	for _, h := range handles {
		gainCtx, cancel := boundedEngineCallContext(ctx)
		if _, err := m.engine.SetGain(gainCtx, h, mutedGain); err != nil {
			ok = false
		}
		cancel()
	}
	return ok
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

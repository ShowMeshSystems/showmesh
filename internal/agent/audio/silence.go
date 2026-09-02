package audio

import (
	"context"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// SessionSilenceOutcome is one session's result from [Manager.SilenceAll].
type SessionSilenceOutcome struct {
	ID      pkgaudio.SessionID
	Outcome pkgaudio.OutcomeResult
}

// SilenceAll stops every session this Manager currently holds, whatever
// the coordinator has told this node about any of them, and reports one
// outcome per session. It is a safety command in the same class as
// [Manager.invalidateActiveSessions]: it bypasses [Session.dispatch] and
// its revState ledger entirely, applying unconditionally rather than
// being refused as stale against a session no coordinator-issued
// invocation ever addressed. Skipping the ledger also means it is never
// advanced or corrupted by this call, so a later legitimate
// audio.session.* command against a silenced session still sees exactly
// the revision history it would have without this call.
//
// This stops sessions; it does not clear them. State stays inspectable
// afterward, matching audio.session.stop rather than
// audio.session.clear. It is idempotent: an already-stopped session's
// outcome is Stopped again, never an error.
func (m *Manager) SilenceAll(ctx context.Context) []SessionSilenceOutcome {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	results := make([]SessionSilenceOutcome, 0, len(sessions))
	for _, s := range sessions {
		s.mu.Lock()
		outcome := m.silenceSessionLocked(ctx, s)
		s.mu.Unlock()

		// Released on the attempt, matching Stop's own rule: a stuck
		// duck or interrupt-hold must not survive a session this call
		// just silenced.
		m.restoreDucked(ctx, s.id)
		m.restoreInterrupted(ctx, s.id)

		results = append(results, SessionSilenceOutcome{ID: s.id, Outcome: outcome})
	}
	return results
}

// silenceSessionLocked applies the same stop body [Manager.Stop] runs
// through [Session.dispatch], called here directly with s.mu already
// held and no invocation or revision involved. Kept in sync with Stop's
// exec closure by hand, matching this package's own convention
// elsewhere of independently reproducing a body across an
// unconditional-safety boundary rather than sharing it with a gated
// path.
func (m *Manager) silenceSessionLocked(ctx context.Context, s *Session) pkgaudio.OutcomeResult {
	if !s.handleLoaded {
		s.resolveFadePendingStrandedLocked("session silenced before its pending fade resolved")
		s.state = pkgaudio.StateStopped
		s.bookmark = nil
		s.setGapUnknownLocked("session is stopped")
		m.stopLTCLocked(ctx, s)
		outcome := m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStopped})
		s.persistBestEffortLocked("node silence")
		return outcome
	}

	s.state = pkgaudio.StateStopping
	m.stopLTCLocked(ctx, s)
	_, stopErr := s.mgr.engine.Stop(ctx, s.handle)
	var releaseErr error
	if stopErr == nil {
		releaseErr = s.mgr.engine.Release(ctx, s.handle)
	}
	err := stopErr
	if err == nil {
		err = releaseErr
	}
	if err != nil {
		s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
		if releaseErr != nil {
			s.resolveFadePendingStrandedLocked("session silenced before its pending fade resolved")
			s.handleLoaded = false
			s.loadedIdentity = ""
			s.state = pkgaudio.StateStopped
			s.bookmark = nil
			s.setGapUnknownLocked("session is stopped")
		}
		outcome := m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: err.Error()})
		s.persistBestEffortLocked("node silence")
		return outcome
	}

	s.resolveFadePendingStrandedLocked("session silenced before its pending fade resolved")
	s.handleLoaded = false
	s.loadedIdentity = ""
	s.state = pkgaudio.StateStopped
	s.bookmark = nil
	s.setGapUnknownLocked("session is stopped")
	outcome := m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStopped})
	s.persistBestEffortLocked("node silence")
	return outcome
}

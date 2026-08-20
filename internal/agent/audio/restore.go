package audio

import (
	"context"
	"fmt"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// RestoreAll rebuilds every persisted session — an in-memory-only
// guarantee is no guarantee, because the restart is exactly the case
// this exists for — called once at agent startup before any command is
// handled. A single session's restore failure is logged and does not
// stop the rest — see [Manager.restoreOne].
func (m *Manager) RestoreAll(ctx context.Context) error {
	ids, err := m.store.List()
	if err != nil {
		return fmt.Errorf("audio: list persisted sessions: %w", err)
	}
	for _, id := range ids {
		if err := m.restoreOne(ctx, id); err != nil {
			m.logf("audio session %s: restore failed: %v", id, err)
		}
	}

	corrupt, err := m.store.ListCorrupt()
	if err != nil {
		m.logf("audio: list corrupt persisted sessions: %v", err)
	}
	for _, c := range corrupt {
		m.logf("audio: persisted session file %q could not be recovered: %s", c.Filename, c.Reason)
	}
	m.mu.Lock()
	m.corruptSessions = corrupt
	m.mu.Unlock()

	return nil
}

// restoreOne rebuilds one session from its persisted record and, for a
// session that was Playing or mid-advance (Preparing) at last persist,
// re-loads and re-starts its current item — never the item that had
// already been advanced away from, and never skipping the item a crash
// landed on between the persisted advance and the engine call that would
// have started it (see [Session.advanceLocked]'s doc comment for the
// write ordering this recovery depends on). Position honors
// [pkgaudio.ResumePolicy]: Resume restarts from the last bookmark,
// Restart always begins at 0. Either way this is a discontinuity —
// timingKnown starts false until a fresh observation resolves it.
func (m *Manager) restoreOne(ctx context.Context, id pkgaudio.SessionID) error {
	rec, ok, err := m.store.Load(id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	s := newSession(id, m)
	s.desired = rec.Desired
	s.revState = pkgaudio.RestoreRevisionState(id, rec.Revision, rec.Decisions)
	if rec.ExecutedResults != nil {
		s.executedResults = rec.ExecutedResults
		// True insertion order does not survive a JSON map round trip;
		// this rebuilds SOME order so the maxRetainedInvocations bound
		// keeps holding from here on. Which entry a post-restart eviction
		// picks first is best-effort, not exact recency — see
		// [Session.rememberExecutedResultLocked]'s doc comment on why
		// that is an acceptable, documented trade rather than a defect.
		s.executedOrder = make([]pkgaudio.InvocationID, 0, len(rec.ExecutedResults))
		for invocation := range rec.ExecutedResults {
			s.executedOrder = append(s.executedOrder, invocation)
		}
	}
	s.state = rec.SessionState
	s.currentIndex = rec.CurrentIndex
	s.currentItemID = rec.CurrentItemID
	s.bookmark = rec.Bookmark
	s.muted = rec.Muted
	s.preMuteGain = rec.PreMuteGain
	if len(rec.DuckedByAll) > 0 {
		s.duckedByAll = make(map[pkgaudio.SessionID]struct{}, len(rec.DuckedByAll))
		for _, id := range rec.DuckedByAll {
			s.duckedByAll[id] = struct{}{}
		}
	}
	s.preDuckGain = rec.PreDuckGain
	s.fault = rec.Fault
	if s.fault == "" {
		s.fault = pkgaudio.FaultNone
	}
	s.faultReason = rec.FaultReason
	s.faultAt = rec.FaultAt
	s.lastProbe = rec.LastProbe
	s.fadePending = rec.FadePending
	s.fadeInvocation = rec.FadeInvocation
	// The handle reloaded below has never been given this fade.
	s.fadeHandleNeverFaded = rec.FadePending
	s.fadeState = rec.FadeState
	if s.fadeState == "" {
		s.fadeState = FadeStateNone
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.state {
	case pkgaudio.StatePlaying, pkgaudio.StatePreparing:
		item, ok := s.currentItemLocked()
		if !ok {
			return nil
		}
		position := time.Duration(0)
		if s.bookmark != nil && s.desired.Playlist != nil && s.desired.Playlist.Resume == pkgaudio.ResumePolicyResume {
			resolved, err := s.resolveBookmarkPositionLocked(item)
			if err != nil {
				// A stale bookmark surviving a restart must not silently
				// use a garbage position, and must not abort recovery of
				// an otherwise-healthy session either: it is logged and
				// cleared, and the item still starts, from 0.
				// prepareLocked's own success below clears any fault this
				// branch might set, so a fault is not the right evidence
				// channel here — the log line is.
				m.logf("audio session %s: restore bookmark could not be resolved, starting from 0: %v", id, err)
				s.bookmark = nil
			} else {
				position = resolved
			}
		}
		if _, err := s.prepareLocked(ctx, item); err != nil {
			s.state = pkgaudio.StateFailed
			s.persistBestEffortLocked("state change")
			return err
		}
		if _, err := m.engine.Start(ctx, s.handle, position); err != nil {
			s.state = pkgaudio.StateFailed
			s.persistBestEffortLocked("state change")
			return err
		}
		s.state = pkgaudio.StatePlaying
		s.timingKnown = false
		s.persistBestEffortLocked("state change")
	case pkgaudio.StatePaused:
		// prepareLocked only reaches a freshly-Loaded engine handle (Ready),
		// never Paused: an engine's own Resume requires a handle it itself
		// last drove into Paused, so restoring here reproduces that by
		// starting from the bookmark and immediately pausing again, rather
		// than leaving the handle at Ready and trusting the session-level
		// state alone.
		item, ok := s.currentItemLocked()
		if !ok {
			return nil
		}
		if _, err := s.prepareLocked(ctx, item); err != nil {
			s.state = pkgaudio.StateFailed
			s.persistBestEffortLocked("state change")
			return err
		}
		position := time.Duration(0)
		if s.bookmark != nil && s.desired.Playlist != nil && s.desired.Playlist.Resume == pkgaudio.ResumePolicyResume {
			resolved, err := s.resolveBookmarkPositionLocked(item)
			if err != nil {
				// Same reasoning as the Playing/Preparing branch above: log
				// and clear rather than fault, since the Start+Pause below
				// (on success) has no equivalent clear to race against, but
				// consistency with the sibling branch matters more than a
				// fault that would only ever be reported once.
				m.logf("audio session %s: restore bookmark could not be resolved, resuming from 0: %v", id, err)
				s.bookmark = nil
			} else {
				position = resolved
			}
		}
		if _, err := m.engine.Start(ctx, s.handle, position); err != nil {
			s.state = pkgaudio.StateFailed
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			s.persistBestEffortLocked("state change")
			return err
		}
		if _, err := m.engine.Pause(ctx, s.handle); err != nil {
			s.state = pkgaudio.StateFailed
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			s.persistBestEffortLocked("state change")
			return err
		}
		s.state = pkgaudio.StatePaused
		s.timingKnown = false
		s.persistBestEffortLocked("state change")
	}

	// A session left ducked when the coordinator crashed is restored
	// right here if the session that was ducking it did not itself
	// survive to Playing/Preparing — a crash after the ducker's own stop
	// persisted but before this restore ran. If the ducker IS still
	// active, s stays ducked, correctly: it will be restored normally
	// when the ducker later stops. Either way this is the same
	// exactly-once check [Manager.removeDuckerLocked] runs on the live
	// path, so a session can never be restored twice regardless of which
	// side of the crash the restore boundary fell on.
	// Copy the ids first: removeDuckerLocked mutates s.duckedByAll, and
	// ranging over a map while deleting other-in-progress entries from a
	// second, independent pass is easier to reason about than relying on
	// range-during-delete semantics for a set this method itself owns.
	duckers := make([]pkgaudio.SessionID, 0, len(s.duckedByAll))
	for id := range s.duckedByAll {
		duckers = append(duckers, id)
	}
	for _, duckerID := range duckers {
		if !m.duckerStillActiveOnDisk(duckerID) {
			m.removeDuckerLocked(ctx, s, duckerID)
		}
	}
	return nil
}

// duckerStillActiveOnDisk reports whether id's persisted record shows a
// session that is still (or will again be, once restored) Playing —
// i.e. one that legitimately still owns an active duck. Reads the
// store directly rather than the in-memory session map because restore
// order across sessions is unspecified.
func (m *Manager) duckerStillActiveOnDisk(id pkgaudio.SessionID) bool {
	rec, ok, err := m.store.Load(id)
	if err != nil || !ok {
		return false
	}
	return rec.SessionState == pkgaudio.StatePlaying || rec.SessionState == pkgaudio.StatePreparing
}

// RunWatcher polls, once per tick, every Playing session for natural
// completion and advances it exactly once through [Session.advanceLocked]
// — the same path [Manager.Advance] uses, per that method's doc comment
// — and every session with a fade in progress for its completion, per
// [Session.checkFadeCompletionLocked]. This is what lets a playlist keep
// advancing, and a fade resolve, on their own while the coordinator or
// broker is unreachable: nothing here depends on either.
func (m *Manager) RunWatcher(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			m.watchTick(ctx)
		}
	}
}

func (m *Manager) watchTick(ctx context.Context) {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	var completed []pkgaudio.SessionID
	for _, s := range sessions {
		s.mu.Lock()
		s.checkFadeCompletionLocked(ctx)
		s.checkStopCompletionLocked(ctx)
		if s.state == pkgaudio.StatePlaying && s.handleLoaded {
			obsCtx, cancel := boundedObserveContext(ctx)
			obs, err := m.engine.Observe(obsCtx, s.handle)
			cancel()
			if err != nil {
				// A background poll failing to read a loaded, playing
				// handle is itself evidence: this is where a real
				// engine's crash or freeze is most likely to surface
				// first, ahead of any operator-dispatched command.
				s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			} else {
				s.lastObservedAt = obs.ObservedAt
				if obs.State == pkgaudio.StateCompleted {
					s.advanceLocked(ctx, false)
				}
			}
		}
		if s.state == pkgaudio.StateCompleted {
			completed = append(completed, s.id)
		}
		s.mu.Unlock()
	}

	// restoreDucked locks OTHER sessions, so it must run after every
	// session's own mu from the loop above is released — see
	// [Manager.duckLowerPriority]'s doc comment.
	for _, id := range completed {
		m.restoreDucked(ctx, id)
	}
}

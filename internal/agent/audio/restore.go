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
	}
	s.state = rec.SessionState
	s.currentIndex = rec.CurrentIndex
	s.currentItemID = rec.CurrentItemID
	s.bookmark = rec.Bookmark
	s.muted = rec.Muted
	s.preMuteGain = rec.PreMuteGain
	s.duckedBy = rec.DuckedBy
	s.preDuckGain = rec.PreDuckGain
	s.fault = rec.Fault
	if s.fault == "" {
		s.fault = pkgaudio.FaultNone
	}
	s.faultReason = rec.FaultReason
	s.faultAt = rec.FaultAt
	s.lastProbe = rec.LastProbe

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
			position = s.bookmark.Position
		}
		if _, err := s.prepareLocked(ctx, item); err != nil {
			s.state = pkgaudio.StateFailed
			s.persistLocked()
			return err
		}
		if _, err := m.engine.Start(ctx, s.handle, position); err != nil {
			s.state = pkgaudio.StateFailed
			s.persistLocked()
			return err
		}
		s.state = pkgaudio.StatePlaying
		s.timingKnown = false
		s.persistLocked()
	case pkgaudio.StatePaused:
		item, ok := s.currentItemLocked()
		if !ok {
			return nil
		}
		if _, err := s.prepareLocked(ctx, item); err != nil {
			s.state = pkgaudio.StateFailed
			s.persistLocked()
			return err
		}
		s.timingKnown = false
	}

	// A session left ducked when the coordinator crashed is restored
	// right here if the session that was ducking it did not itself
	// survive to Playing/Preparing — a crash after the ducker's own stop
	// persisted but before this restore ran. If the ducker IS still
	// active, s stays ducked, correctly: it will be restored normally
	// when the ducker later stops. Either way this is the same
	// exactly-once check [Manager.restoreOneDuckLocked] runs on the live
	// path, so a session can never be restored twice regardless of which
	// side of the crash the restore boundary fell on.
	if s.duckedBy != "" && !m.duckerStillActiveOnDisk(s.duckedBy) {
		m.restoreOneDuckLocked(ctx, s)
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
		if s.state == pkgaudio.StatePlaying && s.handleLoaded {
			obs, err := m.engine.Observe(ctx, s.handle)
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

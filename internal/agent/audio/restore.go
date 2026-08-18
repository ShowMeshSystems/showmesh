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
	return nil
}

// RunWatcher polls, once per tick, every Playing session for natural
// completion and advances it exactly once through [Session.advanceLocked]
// — the same path [Manager.Advance] uses, per that method's doc comment.
// This is what lets a playlist keep advancing on its own while the
// coordinator or broker is unreachable: nothing here depends on either.
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

	for _, s := range sessions {
		s.mu.Lock()
		if s.state == pkgaudio.StatePlaying && s.handleLoaded {
			obs, err := m.engine.Observe(ctx, s.handle)
			if err == nil && obs.State == pkgaudio.StateCompleted {
				s.advanceLocked(ctx, false)
			}
		}
		s.mu.Unlock()
	}
}

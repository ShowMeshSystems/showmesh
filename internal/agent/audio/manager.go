package audio

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// Manager owns every session on this node, against one [Engine]. Every
// public method here is one of the nine audio.session.* operations —
// create/revise, prepare, start, pause, resume, seek, advance, stop,
// clear — and each returns a [pkgaudio.OutcomeResult] gated by
// [Manager.gateAvailability] — see that method's doc comment for
// why every outcome this type can produce is forced to Unconfirmable
// while the wired Engine reports itself unavailable, which is true of
// every Engine this repository ships (see [FakeEngine]).
type Manager struct {
	engine   Engine
	store    SessionStore
	assetDir string
	decoder  Decoder
	now      func() time.Time
	logger   *slog.Logger

	mu       sync.Mutex
	sessions map[pkgaudio.SessionID]*Session

	// corruptSessions is [Manager.RestoreAll]'s record of every persisted
	// file it could not decode into a real session (finding 17) — never
	// addressable by a command, but reported by [Manager.Snapshot] so it
	// is retained fault evidence rather than a silent disappearance.
	corruptSessions []CorruptSessionRecord
}

// NewManager builds a Manager. decoder is [RealDecoder]{} in production
// and a fake in tests, matching internal/agent/audiomediaprobe.go's own
// convention for the same interface.
func NewManager(engine Engine, store SessionStore, assetDir string, decoder Decoder, now func() time.Time, logger *slog.Logger) *Manager {
	return &Manager{
		engine: engine, store: store, assetDir: assetDir, decoder: decoder, now: now, logger: logger,
		sessions: make(map[pkgaudio.SessionID]*Session),
	}
}

func (m *Manager) logf(format string, args ...any) {
	if m.logger != nil {
		m.logger.Warn(fmt.Sprintf(format, args...))
	}
}

func (m *Manager) getOrCreate(id pkgaudio.SessionID) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		return s
	}
	s := newSession(id, m)
	m.sessions[id] = s
	return s
}

func (m *Manager) get(id pkgaudio.SessionID) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Snapshot returns fresh, read-only telemetry for every session this
// Manager currently holds — the retained observation surface. It never
// mutates any command-facing session state and never consults
// [Manager.gateAvailability]: what it reports is the session state
// machine's own internal bookkeeping, real even while every command's
// own outward-facing outcome is being rewritten to Unconfirmable. See
// [Session.snapshotLocked].
func (m *Manager) Snapshot(ctx context.Context) []SessionSnapshot {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	corrupt := m.corruptSessions
	m.mu.Unlock()

	out := make([]SessionSnapshot, 0, len(sessions)+len(corrupt))
	for _, s := range sessions {
		s.mu.Lock()
		out = append(out, s.snapshotLocked(ctx))
		s.mu.Unlock()
	}
	for _, c := range corrupt {
		out = append(out, corruptSessionSnapshot(c))
	}
	return out
}

// corruptSessionSnapshot builds a synthetic, non-addressable
// [SessionSnapshot] for one [CorruptSessionRecord]: State Failed, Fault
// Other, and an id derived from the filename so an operator can find and
// remove the bad file — never a real session id, and never something a
// command can target, since nothing was actually recovered to act on.
func corruptSessionSnapshot(c CorruptSessionRecord) SessionSnapshot {
	return SessionSnapshot{
		ID:          pkgaudio.SessionID("corrupt-session-file:" + c.Filename),
		State:       pkgaudio.StateFailed,
		Fault:       pkgaudio.FaultOther,
		FaultReason: fmt.Sprintf("persisted session file %q could not be recovered: %s", c.Filename, c.Reason),
	}
}

// gateAvailability is the single choke point that keeps this seam honest
// about what it is: a Refused or Failed outcome (a structural decision —
// bad invocation, stale revision, invalid params, no media to act on)
// stands as-is, because it reflects the session layer's own logic, proven
// against [FakeEngine] on purpose. Every other outcome — anything that
// would otherwise claim audible playback actually happened — is replaced
// with Unconfirmable and m.engine's own unavailability reason. No caller
// of Manager can ever receive Started, Position, Stopped, or Completed
// while the wired Engine is unavailable.
func (m *Manager) gateAvailability(outcome pkgaudio.OutcomeResult) pkgaudio.OutcomeResult {
	if ok, reason := m.engine.Available(); !ok {
		switch outcome.Outcome {
		case pkgaudio.OutcomeRefused, pkgaudio.OutcomeFailed:
			return outcome
		default:
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: reason}
		}
	}
	return outcome
}

// Apply merges req onto id's desired state (creating id if it does not
// exist) — "create" and "revise" are the same operation applied to an
// empty or non-empty session. Reported as OutcomePosition: acceptance is
// evidenced
// by the resulting desired state, not by an engine transition — apply
// never touches the engine.
func (m *Manager) Apply(_ context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision, req pkgaudio.ApplyRequest) pkgaudio.OutcomeResult {
	s := m.getOrCreate(id)
	s.mu.Lock()
	defer s.mu.Unlock()

	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		merged, _, err := req.Merge(s.desired)
		if err != nil {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: err.Error()}
		}
		// interrupt is declared and refused, never silently downgraded to
		// duck: whether announcements ever interrupt show audio is an
		// open owner decision.
		if merged.MixPolicy != nil && *merged.MixPolicy == pkgaudio.MixPolicyInterrupt {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: `mix policy "interrupt" is not supported; use "mix" or "duck"`}
		}
		s.desired = merged
		if s.currentIndex < 0 {
			s.currentIndex = 0
		}
		if item, ok := s.currentItemLocked(); ok {
			s.currentItemID = item.ItemID
		}
		if s.state == pkgaudio.StateUnknown {
			s.state = pkgaudio.StateReady
		}
		return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomePosition})
	})
	return res.outcome
}

// Prepare gates readiness — a missing, changed, or undecodable asset
// fails here rather than at Start — and loads the current item without
// starting it.
func (m *Manager) Prepare(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		dispatchedAt := m.now()
		item, ok := s.currentItemLocked()
		if !ok {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session has no media or playlist to prepare"}
		}
		s.releaseEngineLocked(ctx)
		obs, err := s.prepareLocked(ctx, item)
		if err != nil {
			s.state = pkgaudio.StateFailed
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
		}
		s.state = pkgaudio.StateReady
		s.timingKnown = true
		return m.gateAvailability(confirmLocked(pkgaudio.StateReady, pkgaudio.OutcomePosition, obs, dispatchedAt))
	})
	return res.outcome
}

// Start prepares (if not already prepared) and starts the current item
// from its bookmark position, or 0 with no bookmark.
func (m *Manager) Start(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()

	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		item, ok := s.currentItemLocked()
		if !ok {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session has no media or playlist to start"}
		}
		// A handle loaded for a now-superseded item identity (a media or
		// playlist revision landed via Apply between Prepare and Start) is
		// as stale as no handle at all: starting it would play the OLD
		// content while every other surface reports the new one.
		if !s.handleLoaded || s.loadedIdentity != itemIdentity(item) {
			s.releaseEngineLocked(ctx)
			if _, err := s.prepareLocked(ctx, item); err != nil {
				s.state = pkgaudio.StateFailed
				return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
			}
		}
		position, err := s.resolveBookmarkPositionLocked(item)
		if err != nil {
			// Visible and self-healing (finding 16): the operator sees
			// exactly why this Start was refused, and the stale bookmark
			// is cleared so a subsequent Start is not refused forever by
			// the same dead reference.
			s.bookmark = nil
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "bookmark could not be resolved and was cleared: " + err.Error()}
		}
		dispatchedAt := m.now()
		obs, err := s.mgr.engine.Start(ctx, s.handle, position)
		if err != nil {
			s.state = pkgaudio.StateFailed
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
		}
		s.state = pkgaudio.StatePlaying
		s.timingKnown = true
		s.bookmark = nil
		s.lastObservedAt = obs.ObservedAt
		return m.gateAvailability(confirmLocked(pkgaudio.StatePlaying, pkgaudio.OutcomeStarted, obs, dispatchedAt))
	})

	// duck resolution needs to lock OTHER sessions, so it must run after
	// s.mu is released — see [Manager.duckLowerPriority]'s doc comment on
	// why this can never hold two sessions' locks at once.
	duck := res.executed && s.state == pkgaudio.StatePlaying && s.desired.MixPolicy != nil && *s.desired.MixPolicy == pkgaudio.MixPolicyDuck
	var role pkgaudio.SourceRole
	if s.desired.SourceRole != nil {
		role = *s.desired.SourceRole
	}
	s.mu.Unlock()

	if duck {
		m.duckLowerPriority(ctx, id, role)
	}
	return res.outcome
}

// Pause suspends the current item, marking timing unresolved until the
// next fresh observation.
func (m *Manager) Pause(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		if !s.handleLoaded {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "no active playback to pause"}
		}
		s.timingKnown = false
		dispatchedAt := m.now()
		obs, err := s.mgr.engine.Pause(ctx, s.handle)
		if err != nil {
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
		}
		s.state = pkgaudio.StatePaused
		s.bookmark = &pkgaudio.Bookmark{ItemID: s.currentItemID, Index: s.currentIndex, Position: obs.Position}
		if s.desired.Playlist != nil {
			s.bookmark.PlaylistRevision = s.desired.Playlist.OwnerRevision
		}
		s.timingKnown = true
		s.lastObservedAt = obs.ObservedAt
		return m.gateAvailability(confirmLocked(pkgaudio.StatePaused, pkgaudio.OutcomePosition, obs, dispatchedAt))
	})
	return res.outcome
}

// Resume continues from Pause's bookmark position.
func (m *Manager) Resume(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		if !s.handleLoaded || s.state != pkgaudio.StatePaused {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session is not paused"}
		}
		s.timingKnown = false
		dispatchedAt := m.now()
		obs, err := s.mgr.engine.Resume(ctx, s.handle)
		if err != nil {
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
		}
		s.state = pkgaudio.StatePlaying
		s.bookmark = nil
		s.timingKnown = true
		s.lastObservedAt = obs.ObservedAt
		return m.gateAvailability(confirmLocked(pkgaudio.StatePlaying, pkgaudio.OutcomeStarted, obs, dispatchedAt))
	})
	return res.outcome
}

// Seek is a discontinuity: position is re-anchored, never a continuation
// of pre-seek timing.
func (m *Manager) Seek(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision, position time.Duration) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		if !s.handleLoaded {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "no active playback to seek"}
		}
		s.timingKnown = false
		dispatchedAt := m.now()
		obs, err := s.mgr.engine.Seek(ctx, s.handle, position)
		if err != nil {
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
		}
		s.timingKnown = true
		s.lastObservedAt = obs.ObservedAt
		if s.state == pkgaudio.StatePaused {
			s.bookmark = &pkgaudio.Bookmark{ItemID: s.currentItemID, Index: s.currentIndex, Position: obs.Position}
			if s.desired.Playlist != nil {
				s.bookmark.PlaylistRevision = s.desired.Playlist.OwnerRevision
			}
		}
		return m.gateAvailability(confirmLocked(obs.State, pkgaudio.OutcomePosition, obs, dispatchedAt))
	})
	return res.outcome
}

// Advance is the operator-invoked, forced skip to the next playlist item
// — the same underlying transition natural completion drives (see
// [Manager.watchTick]), so an item is advanced exactly once regardless
// of what triggered it: there is exactly one code path, never two that
// could disagree.
func (m *Manager) Advance(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		return m.gateAvailability(s.advanceLocked(ctx, true))
	})
	return res.outcome
}

// Stop is a commanded stop, permanently distinguishable in evidence from
// natural completion — a session that stopped on its own must never be
// reported as if it had been commanded to. Never REFUSED for want of
// engine evidence (ADR-024 decision 7): an idle or unloaded session
// still reports Stopped, and a loaded one always attempts the engine
// call. But a failed Engine.Stop or Release is not silently treated as
// success either (finding 15): the handle stays loaded — never released
// — so a retried Stop can still address it, and the outcome is
// Unconfirmable with the failure's reason, the same "declared, not
// refused, not fabricated" shape ADR-024 decision 7's other exempt
// safety actions use.
func (m *Manager) Stop(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()

	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		if !s.handleLoaded {
			s.state = pkgaudio.StateStopped
			s.bookmark = nil
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStopped})
		}
		s.state = pkgaudio.StateStopping
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
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: err.Error()})
		}
		s.handleLoaded = false
		s.loadedIdentity = ""
		s.state = pkgaudio.StateStopped
		s.bookmark = nil
		return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStopped})
	})
	reachedStopped := res.executed && s.state == pkgaudio.StateStopped
	s.mu.Unlock()

	if reachedStopped {
		m.restoreDucked(ctx, id)
	}
	return res.outcome
}

// Clear releases the session entirely: engine resources, desired state,
// and its persisted record. Like Stop, never refused for want of
// evidence.
func (m *Manager) Clear(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStopped}
	}
	s.mu.Lock()
	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		s.releaseEngineLocked(ctx)
		s.desired = pkgaudio.SessionDesiredState{}
		s.state = pkgaudio.StateStopped
		s.currentIndex = -1
		s.currentItemID = ""
		s.bookmark = nil
		return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStopped})
	})
	s.mu.Unlock()

	if res.executed {
		m.restoreDucked(ctx, id)
		if err := m.store.Delete(id); err != nil {
			m.logf("audio session %s: failed to delete persisted state on clear: %v", id, err)
		}
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}
	return res.outcome
}

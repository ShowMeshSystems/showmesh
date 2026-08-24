package audio

import (
	"context"
	"errors"
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
	// A prior RestoreAll that failed partway through can leave ownership
	// pointing at a session this run is about to rebuild from scratch;
	// nothing else ever clears it, so a re-run must start from "free".
	m.ltc.resetForRestore()

	ids, err := m.store.List()
	if err != nil {
		return fmt.Errorf("audio: list persisted sessions: %w", err)
	}
	for _, id := range ids {
		if err := m.restoreOne(ctx, id, false); err != nil {
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
//
// retry is true only when [Manager.retryDeferredRestores] is the
// caller — a session already deferred once, now being retried right
// after RebindEngine set a real engine. It changes exactly one thing:
// a Start/Pause failure that is NOT [ErrNoEngineBinding] re-queues the
// session for the next binding (see queueForRetryLocked) instead of
// persisting Failed. This is deliberately conservative: rebindMu
// already rules out the confirmed engine-mismatch race, but a session
// resuming in the same call that just bound a brand new engine is not
// a place to make a single failed attempt permanent — the next binding
// gets another try before this session is reported Failed.
func (m *Manager) restoreOne(ctx context.Context, id pkgaudio.SessionID, retry bool) error {
	rec, ok, err := m.store.Load(id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	// A redundant restore (a second startup after a crash mid-restore, or
	// a hot reload) must not leak the previous in-memory session's engine
	// handle, or risk two Session objects both driving the same handle
	// identity — release it before this call's own Session, and its own
	// Load, replaces it.
	// A redundant restore (a second startup after a crash mid-restore, or
	// a hot reload) must not leak the previous in-memory session's engine
	// handle, or risk two Session objects both driving the same handle
	// identity — release it before this call's own Session, and its own
	// Load, replaces it.
	m.mu.Lock()
	previous, hadPrevious := m.sessions[id]
	m.mu.Unlock()
	if hadPrevious {
		previous.mu.Lock()
		previous.releaseEngineLocked(ctx)
		previous.mu.Unlock()
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
	if len(rec.InterruptedByAll) > 0 {
		s.interruptedByAll = make(map[pkgaudio.SessionID]struct{}, len(rec.InterruptedByAll))
		for _, id := range rec.InterruptedByAll {
			s.interruptedByAll[id] = struct{}{}
		}
	}
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
	s.gapKnown = rec.GapKnown
	s.gap = rec.Gap
	s.gapReason = rec.GapReason
	s.gapObservedAt = rec.GapObservedAt
	if !s.gapKnown && s.gapReason == "" {
		s.gapReason = gapReasonNeverAdvanced
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
			if errors.Is(err, ErrNoEngineBinding) {
				m.deferRestoreLocked(ctx, s, id)
				return nil
			}
			s.state = pkgaudio.StateFailed
			m.stopLTCLocked(ctx, s)
			s.persistBestEffortLocked("state change")
			return err
		}
		// This can still fail for a real engine reason (ClassifyFault
		// below covers those), but never with ErrNoEngineBinding
		// specifically: RebindEngine's rebindMu holds this whole retry
		// call, and every attempt outside a retry runs from RestoreAll,
		// strictly before MQTT (and so any RebindEngine call) starts —
		// see agent.go's construction order. Either way, by the time
		// this line runs, prepareLocked's own Load has already
		// succeeded against SOME bound engine, and nothing can unbind
		// or swap it out from under this call.
		if _, err := m.engine.Start(ctx, s.handle, position); err != nil {
			if retry && !errors.Is(err, ErrNoEngineBinding) {
				m.queueForRetryLocked(ctx, s, id, fmt.Sprintf("Start failed while retrying a deferred restore (%v); re-queued for the next audio.node binding rather than persisted as failed", err))
				return nil
			}
			s.state = pkgaudio.StateFailed
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			m.stopLTCLocked(ctx, s)
			s.persistBestEffortLocked("state change")
			return err
		}
		s.state = pkgaudio.StatePlaying
		s.timingKnown = false
		m.startLTCLocked(ctx, s, position)
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
			if errors.Is(err, ErrNoEngineBinding) {
				m.deferRestoreLocked(ctx, s, id)
				return nil
			}
			s.state = pkgaudio.StateFailed
			m.stopLTCLocked(ctx, s)
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
		// Same reasoning as the Playing/Preparing branch's own Start
		// call above: neither call here can hit ErrNoEngineBinding
		// specifically, only a real engine error.
		if _, err := m.engine.Start(ctx, s.handle, position); err != nil {
			if retry && !errors.Is(err, ErrNoEngineBinding) {
				m.queueForRetryLocked(ctx, s, id, fmt.Sprintf("Start failed while retrying a deferred restore (%v); re-queued for the next audio.node binding rather than persisted as failed", err))
				return nil
			}
			s.state = pkgaudio.StateFailed
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			m.stopLTCLocked(ctx, s)
			s.persistBestEffortLocked("state change")
			return err
		}
		if _, err := m.engine.Pause(ctx, s.handle); err != nil {
			if retry && !errors.Is(err, ErrNoEngineBinding) {
				m.queueForRetryLocked(ctx, s, id, fmt.Sprintf("Pause failed while retrying a deferred restore (%v); re-queued for the next audio.node binding rather than persisted as failed", err))
				return nil
			}
			s.state = pkgaudio.StateFailed
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			m.stopLTCLocked(ctx, s)
			s.persistBestEffortLocked("state change")
			return err
		}
		// The handle ends Paused, never Playing, so no LTC run belongs
		// to this branch.
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
		if !m.sourceStillActiveOnDisk(duckerID) {
			m.removeDuckerLocked(ctx, s, duckerID)
		}
	}

	// Same restore-boundary reasoning, and the same
	// [Manager.removeInterrupterLocked] exactly-once membership check,
	// for a session left suspended by an interrupting announcement that
	// did not itself survive to Playing/Preparing.
	interrupters := make([]pkgaudio.SessionID, 0, len(s.interruptedByAll))
	for id := range s.interruptedByAll {
		interrupters = append(interrupters, id)
	}
	for _, interrupterID := range interrupters {
		if !m.sourceStillActiveOnDisk(interrupterID) {
			m.removeInterrupterLocked(ctx, s, interrupterID)
		}
	}
	return nil
}

// deferRestoreLocked handles the "cannot restore yet" case restoreOne's
// prepareLocked call hits whenever no audio.node binding has arrived:
// m.engine is a [SwitchableEngine] and prepareLocked's own engine.Load
// call fails with [ErrNoEngineBinding] until its first Set — the
// earliest engine call on every restore path, so it is the only place
// this check is ever reachable. That is not a restore failure — it is a
// not-yet, and it must never overwrite the on-disk persisted record with
// StateFailed. Caller holds s.mu.
func (m *Manager) deferRestoreLocked(ctx context.Context, s *Session, id pkgaudio.SessionID) {
	m.queueForRetryLocked(ctx, s, id, "no audio engine bound yet; restore deferred until an audio.node binding arrives")
}

// queueForRetryLocked is deferRestoreLocked's and restoreOne's own
// shared retry-queueing: it must never overwrite the on-disk persisted
// record with StateFailed, because that overwrite is exactly the
// permanent damage this exists to prevent. s.state is left as whatever
// restoreOne already loaded from disk (Playing/Preparing/Paused), and
// nothing is persisted here — so a reboot before the next successful
// retry still finds the original desired state on disk, not Failed.
//
// s is left with no engine handle (any partial Load this attempt
// produced is released), so [Manager.invalidateActiveSessions] must
// never treat this session as having a live handle to invalidate — see
// that method's own handleLoaded check. id is recorded in
// m.pendingEngineRestore so [Manager.retryDeferredRestores] re-runs
// restoreOne for it the moment RebindEngine next sets an engine — see
// that method's doc comment for why a session already resolved here
// can never be retried twice.
//
// faultReason is set deliberately, not left to whatever prepareLocked's
// own setFaultLocked call happened to leave behind on its Load failure:
// a future change to that internal call must not silently turn a
// deferred session into one reported with no explanation for why it is
// not actually driving audio. This does not change what STATE
// [Session.snapshotLocked] reports — still whatever was persisted
// (Playing/Preparing/Paused) — only the fault attached to it; see that
// function's own doc comment for the open question of whether State
// itself should say something different here.
func (m *Manager) queueForRetryLocked(ctx context.Context, s *Session, id pkgaudio.SessionID, faultReason string) {
	// A no-binding failure never loads a handle, so these two lines are
	// a no-op on that path; kept defensively for the Start/Pause retry
	// failure path, where prepareLocked's own Load DID succeed and left
	// one behind.
	s.releaseEngineLocked(ctx)
	s.handle = ""
	s.setFaultLocked(pkgaudio.FaultOther, faultReason)
	m.mu.Lock()
	if m.pendingEngineRestore == nil {
		m.pendingEngineRestore = make(map[pkgaudio.SessionID]struct{})
	}
	m.pendingEngineRestore[id] = struct{}{}
	m.mu.Unlock()
	m.logf("audio session %s: %s", id, faultReason)
}

// sourceStillActiveOnDisk reports whether id's persisted record shows a
// session that is still (or will again be, once restored) Playing —
// i.e. one that legitimately still owns an active duck or interrupt.
// Reads the store directly rather than the in-memory session map because
// restore order across sessions is unspecified.
func (m *Manager) sourceStillActiveOnDisk(id pkgaudio.SessionID) bool {
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
				// first, ahead of any operator-dispatched command. The
				// state must be downgraded here, not just the fault
				// field: a session the operator sees as Playing while
				// its own engine cannot even be observed is exactly the
				// fail-silent gap this exists to close.
				s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
				s.state = pkgaudio.StateFailed
			} else {
				s.lastObservedAt = obs.ObservedAt
				if obs.State == pkgaudio.StateCompleted {
					s.advanceLocked(ctx, false, obs.ObservedAt)
				}
			}
		}
		if s.state == pkgaudio.StateCompleted {
			completed = append(completed, s.id)
		}
		s.mu.Unlock()
	}

	// restoreDucked/restoreInterrupted lock OTHER sessions, so they must
	// run after every session's own mu from the loop above is released —
	// see [Manager.duckLowerPriority]'s doc comment.
	for _, id := range completed {
		m.restoreDucked(ctx, id)
		m.restoreInterrupted(ctx, id)
	}
}

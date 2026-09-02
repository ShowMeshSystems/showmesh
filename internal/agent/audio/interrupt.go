package audio

import (
	"context"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// interruptLowerPriority runs after a session with role interrupterRole and
// mix policy Interrupt reaches Playing: it suspends every OTHER currently
// Playing session whose role priority is strictly lower, using the same
// role-priority rule and no-two-locks-held-at-once discipline as
// [Manager.duckLowerPriority] — see that method's doc comment.
func (m *Manager) interruptLowerPriority(ctx context.Context, interrupterID pkgaudio.SessionID, interrupterRole pkgaudio.SourceRole) {
	for _, t := range m.otherSessions(interrupterID) {
		t.mu.Lock()
		if t.state == pkgaudio.StatePlaying {
			var role pkgaudio.SourceRole
			if t.desired.SourceRole != nil {
				role = *t.desired.SourceRole
			}
			if pkgaudio.OutranksForMixing(interrupterRole, role) {
				m.interruptOneLocked(ctx, t, interrupterID)
			}
		}
		t.mu.Unlock()
	}
}

// interruptOneLocked adds interrupterID to t's set of active interrupters,
// suspending t's own playback — captured as a bookmark exactly as a
// commanded [Manager.Pause] captures one — only on the transition from
// zero interrupters to one, the same "first one suspends, last one
// releases" shape [Manager.duckOneLocked] uses for gain. A target already
// interrupted by someone else just gains a second member. A failed
// Engine.Pause is fault evidence and leaves t neither suspended nor
// recorded, so a later release never mistakes a session that never
// actually stopped for one that needs resuming. Caller holds t.mu.
func (m *Manager) interruptOneLocked(ctx context.Context, t *Session, interrupterID pkgaudio.SessionID) {
	if _, already := t.interruptedByAll[interrupterID]; already {
		return
	}
	if len(t.interruptedByAll) == 0 {
		if !t.handleLoaded {
			return
		}
		pauseCtx, cancel := boundedEngineCallContext(ctx)
		obs, err := m.engine.Pause(pauseCtx, t.handle)
		cancel()
		if err != nil {
			t.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			return
		}
		t.state = pkgaudio.StatePaused
		t.bookmark = &pkgaudio.Bookmark{ItemID: t.currentItemID, Identity: t.loadedIdentity, Index: t.currentIndex, Position: obs.Position}
		if t.desired.Playlist != nil {
			t.bookmark.PlaylistRevision = t.desired.Playlist.OwnerRevision
		}
		t.timingKnown = true
		t.lastObservedAt = obs.ObservedAt
		m.stopLTCLocked(ctx, t)
	}
	if t.interruptedByAll == nil {
		t.interruptedByAll = make(map[pkgaudio.SessionID]struct{}, 1)
	}
	t.interruptedByAll[interrupterID] = struct{}{}
	t.persistBestEffortLocked("state change")
}

// restoreInterrupted runs after an interrupting session leaves Playing
// (stop, clear, or natural completion): it removes interrupterID from
// every other session's interrupter set, resuming a session that has no
// interrupters left. Same no-lock-held-on-entry discipline as
// [Manager.restoreDucked].
func (m *Manager) restoreInterrupted(ctx context.Context, interrupterID pkgaudio.SessionID) {
	for _, t := range m.otherSessions(interrupterID) {
		t.mu.Lock()
		m.removeInterrupterLocked(ctx, t, interrupterID)
		t.mu.Unlock()
	}
}

// removeInterrupterLocked removes interrupterID from t's interrupter set,
// or does nothing when interrupterID is not a member — the same
// membership-check exactly-once guarantee [Manager.removeDuckerLocked]
// uses, run identically from the live release path and from
// [Manager.restoreOne]'s crash-boundary check. t is only actually resumed
// once its interrupter set is empty AND its state is still Paused: an
// operator who stopped or cleared t while it was interrupted has already
// moved it on, and there is nothing here to resume. Every path re-anchors
// through a genuine [Engine.Resume] or [Engine.Start] call, never by
// inferring a position from pre-interrupt timing — the same rule
// [Engine.Seek]'s doc comment states at the engine boundary. Caller holds
// t.mu.
//
// Deliberately ignores t's own configured [pkgaudio.ResumePolicy]: that
// field still means "restart the current item from 0" for a genuine
// restart request, for [Manager.restoreOne]'s crash recovery, and for the
// coordinator's own end-of-resting-mode decision — but an announcement
// ending is none of those. It is a transient suspension the session never
// asked for, so its release always tries to continue from the bookmark,
// landing on 0 only when that bookmark genuinely cannot be resolved.
func (m *Manager) removeInterrupterLocked(ctx context.Context, t *Session, interrupterID pkgaudio.SessionID) {
	if _, ok := t.interruptedByAll[interrupterID]; !ok {
		return
	}
	delete(t.interruptedByAll, interrupterID)
	if len(t.interruptedByAll) > 0 {
		t.persistBestEffortLocked("state change")
		return
	}
	if t.state != pkgaudio.StatePaused {
		t.persistBestEffortLocked("state change")
		return
	}
	item, ok := t.currentItemLocked()
	if !ok {
		t.bookmark = nil
		t.persistBestEffortLocked("state change")
		return
	}

	handleStillValid := t.handleLoaded && t.loadedIdentity == itemIdentity(item)

	// The common case, the handle untouched since the pause, uses the
	// exact same [Engine.Resume] call an operator-issued [Manager.Resume]
	// makes: continue the still-paused handle from exactly the position
	// [Engine.Pause] froze it at (Resume issues its own internal seek to
	// that position; this call site does not pass one of its own). This
	// is deliberately NOT [Engine.Start] with a resolved position: a
	// handle that is merely paused (never released) has no reason for
	// this call site to seek it a second time when Resume already
	// re-anchors it.
	if handleStillValid {
		resumeCtx, cancel := boundedEngineCallContext(ctx)
		obs, err := m.engine.Resume(resumeCtx, t.handle)
		cancel()
		if err != nil {
			// Unlike [Manager.Resume]'s own failure handling (which also
			// drops the handle so its own next call re-prepares), this
			// internal reconciliation call leaves t's handle untouched:
			// nothing here retries by re-invoking itself, so there is no
			// stale-handle retry to guard against, and t stays Paused so
			// an operator Resume (or the next interrupter release) can
			// still recover it instead of dead-ending here.
			t.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			t.persistBestEffortLocked("state change")
			return
		}
		t.state = pkgaudio.StatePlaying
		t.bookmark = nil
		t.timingKnown = true
		t.lastObservedAt = obs.ObservedAt
		m.startLTCLocked(ctx, t, obs.Position)
		t.persistBestEffortLocked("state change")
		return
	}

	// The handle went stale while suspended (desired state changed
	// underneath it): re-established through the same
	// release+prepare+Start(bookmark position) sequence
	// [Manager.restoreOne] uses for crash recovery, against a fresh
	// handle. Still tries to land on the bookmarked position, not 0 — see
	// this function's own doc comment — landing on 0 only when the
	// bookmark itself cannot be resolved against the fresh handle. This
	// is [Manager.restoreOne]'s own IDENTICAL fallback (restore.go), and
	// deliberately matches its choice not to fault: this case is only
	// reachable through a deliberate operator Apply while suspended, so
	// faulting it would report the operator's own intent as a fault on a
	// session that is, immediately after, playing the new item
	// correctly. Logged, same as there.
	position := time.Duration(0)
	resolved, err := t.resolveBookmarkPositionLocked(item)
	if err != nil {
		m.logf("audio session %s: interrupt resume bookmark could not be resolved, starting from 0: %v", t.id, err)
	} else {
		position = resolved
	}
	t.bookmark = nil

	t.releaseEngineLocked(ctx)
	if _, err := t.prepareLocked(ctx, item); err != nil {
		t.state = pkgaudio.StateFailed
		m.stopLTCLocked(ctx, t)
		t.persistBestEffortLocked("state change")
		return
	}

	startCtx, startCancel := boundedEngineCallContext(ctx)
	obs, err := m.engine.Start(startCtx, t.handle, position)
	startCancel()
	if err != nil {
		t.state = pkgaudio.StateFailed
		t.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
		m.stopLTCLocked(ctx, t)
		t.persistBestEffortLocked("state change")
		return
	}
	t.state = pkgaudio.StatePlaying
	t.timingKnown = true
	t.lastObservedAt = obs.ObservedAt
	m.startLTCLocked(ctx, t, position)
	t.persistBestEffortLocked("state change")
}

// dropInterrupterMembershipLocked removes interrupterID from t's
// interrupter set with no Engine.Resume/Start and no gain change, unlike
// [Manager.removeInterrupterLocked]. Used only by [Manager.SilenceAll]:
// every session in that call, t included, is being silenced too, so
// resuming t here would start audio the emergency stop exists to kill,
// if only for the round trip until the loop reaches t itself. Caller
// holds t.mu.
func (m *Manager) dropInterrupterMembershipLocked(t *Session, interrupterID pkgaudio.SessionID) {
	delete(t.interruptedByAll, interrupterID)
}

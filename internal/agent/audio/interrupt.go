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
	myPriority := sourceRolePriority[interrupterRole]
	for _, t := range m.otherSessions(interrupterID) {
		t.mu.Lock()
		if t.state == pkgaudio.StatePlaying {
			var role pkgaudio.SourceRole
			if t.desired.SourceRole != nil {
				role = *t.desired.SourceRole
			}
			if sourceRolePriority[role] < myPriority {
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
		obs, err := m.engine.Pause(ctx, t.handle)
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

// interruptResumePolicyLocked is the [pkgaudio.ResumePolicy] an interrupted
// session resumes under: its own playlist's Resume when it has one — the
// same field [Manager.restoreOne] already honors for crash recovery — or
// Resume by default for a single-media session, which carries no such
// field of its own. Caller holds t.mu.
func interruptResumePolicyLocked(t *Session) pkgaudio.ResumePolicy {
	if t.desired.Playlist != nil {
		return t.desired.Playlist.Resume
	}
	return pkgaudio.ResumePolicyResume
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

	policy := interruptResumePolicyLocked(t)
	handleStillValid := t.handleLoaded && t.loadedIdentity == itemIdentity(item)

	// The common case — Resume, handle untouched since the pause — uses
	// the exact same [Engine.Resume] call an operator-issued
	// [Manager.Resume] makes: continue the still-paused handle from
	// exactly the position [Engine.Pause] froze it at. This is
	// deliberately NOT [Engine.Start] with a resolved position: seeking a
	// handle that is merely paused (never released) has no reason to
	// exist when Resume already does the same thing without a seek.
	if policy == pkgaudio.ResumePolicyResume && handleStillValid {
		obs, err := m.engine.Resume(ctx, t.handle)
		if err != nil {
			t.state = pkgaudio.StateFailed
			t.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			m.stopLTCLocked(ctx, t)
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

	// Restart, or a resume whose handle went stale in the meantime
	// (desired state changed while suspended): re-established through
	// the same release+prepare+Start(0-or-bookmark) sequence
	// [Manager.restoreOne] uses for crash recovery, always against a
	// FRESH handle — including when handleStillValid, for Restart: the
	// paused handle sits frozen at its pre-interrupt position, and this
	// package's own [Engine.Start] never seeks a zero position, so a
	// restart reusing that handle would resume it, not restart it.
	position := time.Duration(0)
	if policy == pkgaudio.ResumePolicyResume {
		resolved, err := t.resolveBookmarkPositionLocked(item)
		if err != nil {
			m.logf("audio session %s: interrupt resume bookmark could not be resolved, restarting from 0: %v", t.id, err)
		} else {
			position = resolved
		}
	}
	t.bookmark = nil

	t.releaseEngineLocked(ctx)
	if _, err := t.prepareLocked(ctx, item); err != nil {
		t.state = pkgaudio.StateFailed
		m.stopLTCLocked(ctx, t)
		t.persistBestEffortLocked("state change")
		return
	}

	obs, err := m.engine.Start(ctx, t.handle, position)
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

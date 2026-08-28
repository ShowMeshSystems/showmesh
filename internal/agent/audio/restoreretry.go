package audio

import (
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// PendingRestoreCount reports how many sessions currently have a
// deferred or re-queued restore waiting on an audio.node binding or a
// build refusal to clear (m.pendingEngineRestore — see restore.go
// queueForRetryLocked). internal/agent's own automatic restore-retry
// driver polls this to decide whether re-probing the device is worth
// attempting at all.
func (m *Manager) PendingRestoreCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pendingEngineRestore)
}

// sessionPendingRestore reports whether id currently has a restore
// queued (m.pendingEngineRestore) — [Session.snapshotLocked]'s own gate
// for whether to attach the automatic retry status below. Deliberately
// independent of [pkgaudio.StateRestorePending] (introduced by a sibling
// branch, not this one): both checks agree once both branches are
// merged, but this one alone must not depend on the other branch's own
// commit.
func (m *Manager) sessionPendingRestore(id pkgaudio.SessionID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, pending := m.pendingEngineRestore[id]
	return pending
}

// SetRestoreRetryStatus records internal/agent's own automatic
// restore-retry driver's latest attempt: attempts is how many automatic
// attempts it has made since every deferred restore last resolved,
// nextAttemptAt is when it will try again (the zero value once the
// driver's own bounded schedule is exhausted), and lastReason is why the
// most recent attempt did not build an engine. Read back by every
// RestorePending session's own snapshot as
// audio_session.restore.attempts/.next_attempt_ms/.last_reason
// (docs/build/IDENTIFIER-REGISTER.md) — see [Session.snapshotLocked].
func (m *Manager) SetRestoreRetryStatus(attempts int, nextAttemptAt time.Time, lastReason string) {
	m.mu.Lock()
	m.restoreRetryAttempts = attempts
	m.restoreRetryNextAttemptAt = nextAttemptAt
	m.restoreRetryLastReason = lastReason
	m.mu.Unlock()
}

// ClearRestoreRetryStatus resets the automatic retry status once every
// deferred restore has resolved (PendingRestoreCount reaches 0), so the
// next, unrelated deferral starts its own attempt count at 1 rather than
// wherever a previous one left off.
func (m *Manager) ClearRestoreRetryStatus() {
	m.SetRestoreRetryStatus(0, time.Time{}, "")
}

// RestoreRetryStatus is [Session.snapshotLocked]'s own read of the
// current automatic retry status for a session reporting
// [pkgaudio.StateRestorePending], translating restoreRetryNextAttemptAt
// into a countdown from now (never negative — the zero value once no
// further automatic attempt is scheduled) — the shape
// audio_session.restore.next_attempt_ms actually reports on the wire.
// Caller holds s.mu, not m.mu; this takes m.mu itself, which is safe
// under the s.mu-then-m.mu order queueForRetryLocked already uses in
// this package (restore.go).
func (m *Manager) RestoreRetryStatus(now time.Time) (attempts int, nextAttempt time.Duration, lastReason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	attempts = m.restoreRetryAttempts
	lastReason = m.restoreRetryLastReason
	if !m.restoreRetryNextAttemptAt.IsZero() {
		if d := m.restoreRetryNextAttemptAt.Sub(now); d > 0 {
			nextAttempt = d
		}
	}
	return attempts, nextAttempt, lastReason
}

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

// restoreRetryStatus is one session's own automatic restore-retry
// status. Tracked per session, not node-wide: internal/agent's driver
// retries every currently-pending session together on each cycle, so
// they share one outcome per cycle by construction, but the count is
// still keyed to the exact set of sessions that cycle actually applied
// to — a session deferred later, by an unrelated incident, starts its
// own count at zero instead of inheriting whatever an earlier, unrelated
// session's retries left behind.
type restoreRetryStatus struct {
	attempts      int
	nextAttemptAt time.Time
	lastReason    string
}

// SetRestoreRetryStatus records internal/agent's own automatic
// restore-retry driver's latest attempt, applied identically to every
// session CURRENTLY pending (m.pendingEngineRestore): attempts is how
// many automatic attempts have been made since this exact set of pending
// sessions was last fully resolved; nextAttemptAt is when the driver
// will try again (the zero value once its bounded schedule is exhausted
// or no further attempt is scheduled); lastReason is why the most recent
// attempt did not build an engine. Every call replaces the tracked set
// outright (rather than merging), so a session that resolved between two
// calls is pruned automatically instead of carrying a stale entry
// forward. Read back by every pending session's own snapshot as
// audio_session.restore.attempts/.next_attempt_ms/.last_reason
// (docs/build/IDENTIFIER-REGISTER.md) — see [Session.snapshotLocked].
func (m *Manager) SetRestoreRetryStatus(attempts int, nextAttemptAt time.Time, lastReason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := restoreRetryStatus{attempts: attempts, nextAttemptAt: nextAttemptAt, lastReason: lastReason}
	fresh := make(map[pkgaudio.SessionID]restoreRetryStatus, len(m.pendingEngineRestore))
	for id := range m.pendingEngineRestore {
		fresh[id] = status
	}
	m.restoreRetryStatusBySession = fresh
}

// ClearRestoreRetryStatus resets every session's automatic retry status
// once every deferred restore has resolved (PendingRestoreCount reaches
// 0), so the next, unrelated deferral starts its own attempt count at 1
// rather than wherever a previous one left off.
func (m *Manager) ClearRestoreRetryStatus() {
	m.mu.Lock()
	m.restoreRetryStatusBySession = nil
	m.mu.Unlock()
}

// RestoreRetryStatus is [Session.snapshotLocked]'s own read of id's
// current automatic retry status, translating the tracked
// nextAttemptAt into a countdown from now (never negative — the zero
// value once no further automatic attempt is scheduled for id) — the
// shape audio_session.restore.next_attempt_ms actually reports on the
// wire. id with no tracked status (never retried, or resolved and
// pruned) reports all zero, which is exactly right for a session
// [Session.snapshotLocked] never even calls this for (see its own gate)
// and harmless for one it does. Caller holds s.mu, not m.mu; this takes
// m.mu itself, which is safe under the s.mu-then-m.mu order
// queueForRetryLocked already uses in this package (restore.go).
func (m *Manager) RestoreRetryStatus(id pkgaudio.SessionID, now time.Time) (attempts int, nextAttempt time.Duration, lastReason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	status, ok := m.restoreRetryStatusBySession[id]
	if !ok {
		return 0, 0, ""
	}
	attempts = status.attempts
	lastReason = status.lastReason
	if !status.nextAttemptAt.IsZero() {
		if d := status.nextAttemptAt.Sub(now); d > 0 {
			nextAttempt = d
		}
	}
	return attempts, nextAttempt, lastReason
}

// EngineRestoreState is internal/agent's own automatic restore-retry
// driver's status for THIS NODE (node.audio.engine.restore.state,
// docs/build/IDENTIFIER-REGISTER.md), independent of whether any session
// on it currently has a restore queued: a delivered audio.node binding
// with no persisted session has zero entries in m.pendingEngineRestore
// and so nothing in restoreRetryStatusBySession above, which is exactly
// the gap this type exists to close. Three values, not a countdown and
// not a boolean -- see this package's restoreRetryStatus doc comment
// above ("0 both before the driver's first attempt and once its bounded
// schedule is exhausted"): a countdown alone cannot tell those two apart,
// and neither can a boolean tell "never started" from "gave up".
type EngineRestoreState string

const (
	// EngineRestoreIdle is the zero value and every Manager's starting
	// state: no automatic restore attempt has ever been made on this
	// node. Also what an unset/omitted engineRestoreState reads as on
	// the wire (mqttproto.AudioPayload.Validate treats it as optional
	// for exactly this reason).
	EngineRestoreIdle EngineRestoreState = "idle"
	// EngineRestoreScheduled is set on every attempt that did not fully
	// resolve while the bounded schedule still has delays left: another
	// automatic attempt WILL run.
	EngineRestoreScheduled EngineRestoreState = "scheduled"
	// EngineRestoreExhausted is set once the bounded schedule's last
	// delay has been used and still did not resolve: no further
	// automatic attempt will run on its own; a genuine audio.node.configure
	// delivery is the only remaining path back to a working engine.
	EngineRestoreExhausted EngineRestoreState = "exhausted"
)

// nodeRestoreRetryStatus is [Manager.nodeRestoreRetry]'s stored value --
// [restoreRetryStatus]'s node-level counterpart, plus the state that
// disambiguates "scheduled" from "exhausted" at node level, where
// nothing forces every node to have a pending session to key off.
type nodeRestoreRetryStatus struct {
	state         EngineRestoreState
	attempts      int
	nextAttemptAt time.Time
	lastReason    string
}

// SetNodeRestoreRetryStatus records internal/agent's own automatic
// restore-retry driver's latest attempt at NODE level -- the counterpart
// to [Manager.SetRestoreRetryStatus] that survives a node with no
// pending session at all. state, attempts, nextAttemptAt, and lastReason
// mean exactly what they mean on [Manager.SetRestoreRetryStatus]; state
// additionally distinguishes "scheduled" (nextAttemptAt in the future)
// from "exhausted" (schedule used up, nextAttemptAt the zero value) --
// the distinction the per-session field cannot make on its own. Read
// back by [Manager.NodeRestoreRetryStatus] as
// node.audio.engine.restore.state/.attempts/.next_attempt_ms/.last_reason
// (docs/build/IDENTIFIER-REGISTER.md).
func (m *Manager) SetNodeRestoreRetryStatus(state EngineRestoreState, attempts int, nextAttemptAt time.Time, lastReason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodeRestoreRetry = nodeRestoreRetryStatus{state: state, attempts: attempts, nextAttemptAt: nextAttemptAt, lastReason: lastReason}
}

// ClearNodeRestoreRetryStatus resets this node's automatic retry status
// to [EngineRestoreIdle] once every deferred restore has resolved and the
// engine reports available -- [Manager.ClearRestoreRetryStatus]'s
// node-level counterpart, called alongside it at every call site.
func (m *Manager) ClearNodeRestoreRetryStatus() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodeRestoreRetry = nodeRestoreRetryStatus{}
}

// NodeRestoreRetryStatus is this node's own audio report's read of its
// current automatic retry status -- [Manager.RestoreRetryStatus]'s
// node-level counterpart, same nextAttemptAt-to-countdown translation.
// A Manager that has never had SetNodeRestoreRetryStatus called on it
// (never had an engine problem) reports [EngineRestoreIdle], never an
// empty string and never [EngineRestoreExhausted].
func (m *Manager) NodeRestoreRetryStatus(now time.Time) (state EngineRestoreState, attempts int, nextAttempt time.Duration, lastReason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.nodeRestoreRetry
	state = status.state
	if state == "" {
		state = EngineRestoreIdle
	}
	attempts = status.attempts
	lastReason = status.lastReason
	if !status.nextAttemptAt.IsZero() {
		if d := status.nextAttemptAt.Sub(now); d > 0 {
			nextAttempt = d
		}
	}
	return state, attempts, nextAttempt, lastReason
}

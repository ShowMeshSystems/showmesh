package audio

import (
	"context"
	"time"

	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// restartClockWaitBound is how long a deferred restore of a scheduled
// session waits for this node's clock to lock before starting on arrival.
// The clock configuration and the audio binding arrive in no fixed order
// after a restart, and a provider this agent starts itself needs seconds.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: 8 seconds is the owner's bound.
var restartClockWaitBound = 8 * time.Second // var, not const: shrunk by tests

// restartClockWaitPollInterval is how often the wait re-reads the clock.
var restartClockWaitPollInterval = 500 * time.Millisecond

// holdRestoreForClock reports whether id's deferred restore stays queued
// because its record carries a schedule and this node's clock cannot yet
// say whether it is the one that schedule was read on. A held session
// keeps reporting a pending restore; the wait holds no lock, and it is
// granted once per session: a zero deadline marks one already spent.
// Caller holds rebindMu.
func (m *Manager) holdRestoreForClock(ctx context.Context, id pkgaudio.SessionID) bool {
	m.mu.Lock()
	deadline, waiting := m.clockWaitDeadline[id]
	m.mu.Unlock()
	if waiting && deadline.IsZero() {
		return false
	}
	rec, ok, err := m.store.Load(id)
	if err != nil || !ok || !restoreWantsClock(rec) {
		return false
	}
	decidable := m.restartClockDecidable(ctx)

	m.mu.Lock()
	if m.clockWaitDeadline == nil {
		m.clockWaitDeadline = make(map[pkgaudio.SessionID]time.Time)
	}
	if !waiting {
		deadline = time.Now().Add(restartClockWaitBound)
	}
	if decidable || !time.Now().Before(deadline) {
		m.clockWaitDeadline[id] = time.Time{}
		m.mu.Unlock()
		return false
	}
	m.clockWaitDeadline[id] = deadline
	if m.pendingEngineRestore == nil {
		m.pendingEngineRestore = make(map[pkgaudio.SessionID]struct{})
	}
	m.pendingEngineRestore[id] = struct{}{}
	start := !m.clockWaitRunning
	m.clockWaitRunning = true
	m.mu.Unlock()

	if !waiting {
		m.logf("audio session %s: restore is waiting up to %s for this node's clock before it starts", id, restartClockWaitBound)
	}
	if start {
		go m.runRestoreClockWait(ctx)
	}
	return true
}

// restoreWantsClock reports whether rec is a playing session whose stored
// schedule could be rejoined, which is the only restore worth holding.
func restoreWantsClock(rec PersistedSession) bool {
	playing := rec.SessionState == pkgaudio.StatePlaying || rec.SessionState == pkgaudio.StatePreparing
	return playing && rec.ScheduleActive && rec.ScheduleClock != nil && rec.ScheduleItemIndex == rec.CurrentIndex
}

// restartClockDecidable reports whether this node's clock is locked and
// readable right now, which is all a restore needs to tell the same clock
// from a different one. A different clock is decidable and is not waited on.
func (m *Manager) restartClockDecidable(ctx context.Context) bool {
	source := m.clockSourceSnapshot()
	if source == nil {
		return false
	}
	if source.Poll(ctx).State != agentclock.StateLocked {
		return false
	}
	return source.Now(ctx).Valid
}

// runRestoreClockWait retries the held restores once the clock is
// decidable or a session's wait has run out, and exits when none is held.
// The retry itself runs under rebindMu, exactly as a binding's own does.
func (m *Manager) runRestoreClockWait(ctx context.Context) {
	ticker := time.NewTicker(restartClockWaitPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.clockWaitRunning = false
			m.mu.Unlock()
			return
		case <-ticker.C:
		}

		m.mu.Lock()
		held, expired := 0, false
		now := time.Now()
		for id := range m.pendingEngineRestore {
			if deadline, ok := m.clockWaitDeadline[id]; ok && !deadline.IsZero() {
				held++
				expired = expired || !now.Before(deadline)
			}
		}
		if held == 0 {
			m.clockWaitRunning = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		if expired || m.restartClockDecidable(ctx) {
			m.rebindMu.Lock()
			m.retryDeferredRestores(ctx)
			m.rebindMu.Unlock()
		}
	}
}

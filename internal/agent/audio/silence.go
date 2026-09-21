package audio

import (
	"context"
	"sort"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// SessionSilenceOutcome is one session's result from [Manager.SilenceAll].
type SessionSilenceOutcome struct {
	ID      pkgaudio.SessionID
	Outcome pkgaudio.OutcomeResult
}

// SilenceAll stops every session this Manager currently holds, bypassing
// [Session.dispatch]'s revState ledger entirely and reporting one
// outcome per session. Stops rather than clears, and is idempotent, an
// already-stopped session's outcome is Stopped again, never an error.
//
// Nothing is ever resumed or faded up here: every session in this call
// is being silenced in the same pass, so restoring a duck or an
// interrupt hold with a real Engine.Resume/Start would make the
// emergency stop start or brighten audio it exists to kill. Duck and
// interrupt membership is dropped directly, with no engine call.
//
// Each session's engine calls run under [boundedEngineCallContext], not
// the raw ctx given here: this loop is serial, so one wedged call must
// not stall the sessions waiting behind it or the result this operation
// reports.
//
// The second return is how many additional engine branches the final,
// unconditional [Engine.ReleaseAll] sweep released that no session's own
// stop above accounted for: a branch a failed per-session stop left
// behind, or one no session ever owned. This runs even when a per-session
// stop above failed or blocked, so an emergency stop never leaves a
// branch playing merely because one session lost track of it.
//
// The third return is false when that final sweep itself could not run
// to completion (for example, an engine rebind window with no engine
// currently bound): the count above is then a floor, not a clean sweep,
// and the caller must not report this operation as confirmed on the
// strength of the per-session outcomes alone.
func (m *Manager) SilenceAll(ctx context.Context) ([]SessionSilenceOutcome, int, bool) {
	outcomes := m.silenceSessions(ctx, m.liveSessionsExcept())
	released, sweepConfirmed := m.releaseEveryEngineBranchExcept(ctx)
	return outcomes, released, sweepConfirmed
}

// SilenceAllExcept is [Manager.SilenceAll] for every session other than
// excludeIDs, so a weather delay alert keeps playing. More than one id is
// excluded when a caller must leave every alert session alone, not just
// the one it started. Its own final engine-wide sweep excepts the handles
// (loaded and staged) those excluded sessions currently own, so their
// audio survives it exactly as their own per-session stop above was
// never run against them.
func (m *Manager) SilenceAllExcept(ctx context.Context, excludeIDs ...pkgaudio.SessionID) ([]SessionSilenceOutcome, int, bool) {
	outcomes := m.silenceSessions(ctx, m.liveSessionsExcept(excludeIDs...))
	released, sweepConfirmed := m.releaseEveryEngineBranchExceptSessions(ctx, excludeIDs...)
	return outcomes, released, sweepConfirmed
}

// releaseEveryEngineBranchExceptSessions is [Manager.
// releaseEveryEngineBranchExcept] except by session id: it locks every
// named, still-live session in ids and keeps every one of those locks
// held across both reading its loaded/staged handles AND running the
// engine sweep itself, so none of them can load or stage a new handle in
// the window between the two -- the race a plain snapshot-then-sweep
// (read the handles, unlock, sweep) leaves open, since a session loading
// right in that window would own a handle this sweep never learns to
// except. ids are locked in a fixed order (sorted), not the order the
// caller gave them, so two overlapping calls naming the same sessions in
// different orders cannot deadlock each other.
func (m *Manager) releaseEveryEngineBranchExceptSessions(ctx context.Context, ids ...pkgaudio.SessionID) (int, bool) {
	seen := make(map[pkgaudio.SessionID]bool, len(ids))
	sorted := make([]pkgaudio.SessionID, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	sessions := make([]*Session, 0, len(sorted))
	for _, id := range sorted {
		if s, ok := m.get(id); ok {
			sessions = append(sessions, s)
		}
	}
	for _, s := range sessions {
		s.mu.Lock()
	}
	defer func() {
		for _, s := range sessions {
			s.mu.Unlock()
		}
	}()

	var except []EngineHandle
	for _, s := range sessions {
		if s.handleLoaded {
			except = append(except, s.handle)
		}
		if s.stage != nil && s.stage.ready {
			except = append(except, s.stage.handle)
		}
	}
	return m.releaseEveryEngineBranchExcept(ctx, except...)
}

// releaseEveryEngineBranchExcept runs after every per-session stop
// [SilenceAll]/[SilenceAllExcept] dispatches has already been attempted:
// it releases whatever the engine still holds live except the named
// handles, and logs rather than fails on an engine error, since this is
// already the last-resort sweep behind the per-session outcomes those
// callers report. Bounded like every other engine call SilenceAll makes.
// The second return is false when the sweep itself did not run to
// completion (for example [SwitchableEngine.ReleaseAll] with no engine
// currently bound, mid-rebind): the released count is then a floor, not
// proof nothing else was left playing.
//
// When it releases at least one branch, it also logs one WARN naming the
// count and the released handles' own names, taken from a [Engine.
// LiveHandles] read before the sweep minus one after it: an orphaned
// branch no session accounted for must be findable in the node's own
// log, even though [pkg/audio]'s wire evidence today only carries the
// count.
func (m *Manager) releaseEveryEngineBranchExcept(ctx context.Context, except ...EngineHandle) (int, bool) {
	liveCtx, liveCancel := boundedEngineCallContext(ctx)
	before, beforeErr := m.engine.LiveHandles(liveCtx)
	liveCancel()

	relCtx, relCancel := boundedEngineCallContext(ctx)
	released, err := m.engine.ReleaseAll(relCtx, except...)
	relCancel()
	if err != nil {
		m.logf("audio: an emergency stop's final engine sweep did not complete; some branches may still be playing: %v", err)
	}

	if released > 0 && beforeErr == nil {
		afterCtx, afterCancel := boundedEngineCallContext(ctx)
		after, afterErr := m.engine.LiveHandles(afterCtx)
		afterCancel()
		if afterErr == nil {
			m.logf("audio: an emergency stop released %d engine branch(es) no session had already accounted for: %v", released, releasedHandleNames(before, after))
		}
	}

	return released, err == nil
}

// releasedHandleNames returns every handle present in before but not in
// after, so [Manager.releaseEveryEngineBranchExcept] can name what an
// unaccounted-for release actually tore down.
func releasedHandleNames(before, after []EngineHandle) []EngineHandle {
	still := make(map[EngineHandle]struct{}, len(after))
	for _, h := range after {
		still[h] = struct{}{}
	}
	var out []EngineHandle
	for _, h := range before {
		if _, ok := still[h]; !ok {
			out = append(out, h)
		}
	}
	return out
}

// SilenceSession stops one session the way [Manager.SilenceAll] does,
// whatever its state and revision, and reports false when it does not exist.
func (m *Manager) SilenceSession(ctx context.Context, id pkgaudio.SessionID) (SessionSilenceOutcome, bool) {
	s, ok := m.get(id)
	if !ok {
		return SessionSilenceOutcome{}, false
	}
	return m.silenceSessions(ctx, []*Session{s})[0], true
}

// liveSessionsExcept snapshots this Manager's live sessions, omitting
// every id in excludeIDs (an empty id matches nothing, so [SilenceAll]
// excludes none).
func (m *Manager) liveSessionsExcept(excludeIDs ...pkgaudio.SessionID) []*Session {
	excluded := make(map[pkgaudio.SessionID]struct{}, len(excludeIDs))
	for _, id := range excludeIDs {
		if id != "" {
			excluded[id] = struct{}{}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sessions := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		if _, skip := excluded[id]; skip {
			continue
		}
		sessions = append(sessions, s)
	}
	return sessions
}

// silenceSessions is [SilenceAll]/[SilenceAllExcept]'s shared body.
func (m *Manager) silenceSessions(ctx context.Context, sessions []*Session) []SessionSilenceOutcome {
	results := make([]SessionSilenceOutcome, 0, len(sessions))
	for _, s := range sessions {
		s.mu.Lock()
		outcome := m.stopExecLocked(ctx, s, boundedEngineCallContext)
		outcome = s.persistOrFailLocked(outcome)
		s.mu.Unlock()

		m.dropHoldMembershipEverywhere(s.id)

		results = append(results, SessionSilenceOutcome{ID: s.id, Outcome: outcome})
	}
	return results
}

// ZeroGainExcept sets every session other than excludeID to gain zero on its
// loaded and staged engine handles, all sessions at once, and returns within
// bound with the ids of the sessions it could not mute by then.
func (m *Manager) ZeroGainExcept(ctx context.Context, excludeID pkgaudio.SessionID, bound time.Duration) []pkgaudio.SessionID {
	sessions := m.liveSessionsExcept(excludeID)
	var mu sync.Mutex
	muted := make(map[pkgaudio.SessionID]bool, len(sessions))
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			if m.zeroGainSession(ctx, s) {
				mu.Lock()
				muted[s.id] = true
				mu.Unlock()
			}
		}(s)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}

	mu.Lock()
	defer mu.Unlock()
	var unmuted []pkgaudio.SessionID
	for _, s := range sessions {
		if !muted[s.id] {
			unmuted = append(unmuted, s.id)
		}
	}
	return unmuted
}

// zeroGainSession mutes s's loaded and staged handles, reporting whether
// every engine call it made succeeded.
func (m *Manager) zeroGainSession(ctx context.Context, s *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	handles := make([]EngineHandle, 0, 2)
	if s.handleLoaded {
		handles = append(handles, s.handle)
	}
	if s.stage != nil && s.stage.ready {
		handles = append(handles, s.stage.handle)
	}
	ok := true
	for _, h := range handles {
		gainCtx, cancel := boundedEngineCallContext(ctx)
		if _, err := m.engine.SetGain(gainCtx, h, mutedGain); err != nil {
			ok = false
		}
		cancel()
	}
	return ok
}

// dropHoldMembershipEverywhere removes sessionID from every other live
// session's duck and interrupt sets without an engine call: the
// SilenceAll-only counterpart to [Manager.restoreDucked]/[Manager.
// restoreInterrupted], which must not run here (see [Manager.
// SilenceAll]'s own doc comment).
func (m *Manager) dropHoldMembershipEverywhere(sessionID pkgaudio.SessionID) {
	for _, t := range m.otherSessions(sessionID) {
		t.mu.Lock()
		m.dropDuckerMembershipLocked(t, sessionID)
		m.dropInterrupterMembershipLocked(t, sessionID)
		t.mu.Unlock()
	}
}

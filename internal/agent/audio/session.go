package audio

import (
	"context"
	"fmt"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// PersistedSession is one session's durable record: everything
// [Manager.RestoreAll] needs to rebuild a [Session], including its
// [pkgaudio.RevisionState] and every invocation's already-produced
// outcome, so a redelivered command arriving after a restart is answered
// from this record rather than re-executed.
type PersistedSession struct {
	ID              pkgaudio.SessionID
	Desired         pkgaudio.SessionDesiredState
	Revision        pkgaudio.Revision
	Decisions       map[pkgaudio.InvocationID]pkgaudio.RevisionDecision
	ExecutedResults map[pkgaudio.InvocationID]pkgaudio.OutcomeResult

	// SessionState is this session's last known state at the point of
	// the last persist. CurrentIndex/CurrentItemID/Bookmark locate the
	// current playlist item; Bookmark is nil unless a discontinuity
	// paused it at a specific position.
	SessionState  pkgaudio.State
	CurrentIndex  int
	CurrentItemID string
	Bookmark      *pkgaudio.Bookmark

	// Muted and PreMuteGain record an operator-issued audio.output.mute
	// still in effect: PreMuteGain is the gain audio.output.unmute
	// restores. Nil PreMuteGain with Muted true means mute happened
	// before any gain was ever set — Unmute restores unity.
	Muted       bool
	PreMuteGain *pkgaudio.Gain

	// DuckedBy is the id of the session whose duck mix policy is
	// currently suppressing this session's gain, empty when this session
	// is not ducked. PreDuckGain is the gain to restore. This pair is the
	// restore-exactly-once guard across a restart: a restore clears and
	// persists both, so a repeated or racing restore attempt finds
	// DuckedBy already empty and does nothing — see
	// [Manager.restoreOneDuckLocked].
	DuckedBy    pkgaudio.SessionID
	PreDuckGain *pkgaudio.Gain
}

// SessionStore is the session layer's durability boundary — enough of
// every session to rebuild it after a restart. An in-memory-only
// guarantee is no guarantee: the restart is exactly the case this
// exists for.
type SessionStore interface {
	Save(id pkgaudio.SessionID, rec PersistedSession) error
	Load(id pkgaudio.SessionID) (PersistedSession, bool, error)
	Delete(id pkgaudio.SessionID) error
	List() ([]pkgaudio.SessionID, error)
}

// Session is one authoritative playback session: desired state, its
// [pkgaudio.RevisionState], and the engine handle currently backing it,
// if any. All mutation goes through [Manager]'s dispatch methods, which
// hold session.mu for the duration of one command.
type Session struct {
	id       pkgaudio.SessionID
	mgr      *Manager
	mu       sync.Mutex
	revState *pkgaudio.RevisionState

	desired         pkgaudio.SessionDesiredState
	executedResults map[pkgaudio.InvocationID]pkgaudio.OutcomeResult

	state         pkgaudio.State
	handle        EngineHandle
	handleLoaded  bool
	currentIndex  int
	currentItemID string
	bookmark      *pkgaudio.Bookmark

	// timingKnown is false immediately after any discontinuity (pause,
	// seek, restart, media change) until a fresh post-dispatch
	// observation resolves it. It is reporting-only state, never used to
	// gate a transition.
	timingKnown bool

	// fadePending is true from the moment audio.gain.fade dispatches a
	// fade until [Manager.watchTick] observes it complete. Reporting-only,
	// like timingKnown: it never gates a transition. fadeInvocation is the
	// invocation that dispatched it, so the eventual completion (or
	// unconfirmable non-completion) can be written back onto that exact
	// invocation's cached [pkgaudio.OutcomeResult] — see
	// [Session.checkFadeCompletionLocked].
	fadePending    bool
	fadeInvocation pkgaudio.InvocationID

	muted       bool
	preMuteGain *pkgaudio.Gain

	duckedBy    pkgaudio.SessionID
	preDuckGain *pkgaudio.Gain
}

func newSession(id pkgaudio.SessionID, mgr *Manager) *Session {
	return &Session{
		id:              id,
		mgr:             mgr,
		revState:        pkgaudio.NewRevisionState(id),
		executedResults: make(map[pkgaudio.InvocationID]pkgaudio.OutcomeResult),
		state:           pkgaudio.StateUnknown,
		currentIndex:    -1,
	}
}

// persistedLocked snapshots s for [SessionStore.Save]. Caller holds s.mu.
func (s *Session) persistedLocked() PersistedSession {
	return PersistedSession{
		ID:              s.id,
		Desired:         s.desired,
		Revision:        s.revState.Current(),
		Decisions:       s.revState.Decisions(),
		ExecutedResults: s.executedResults,
		SessionState:    s.state,
		CurrentIndex:    s.currentIndex,
		CurrentItemID:   s.currentItemID,
		Bookmark:        s.bookmark,
		Muted:           s.muted,
		PreMuteGain:     s.preMuteGain,
		DuckedBy:        s.duckedBy,
		PreDuckGain:     s.preDuckGain,
	}
}

func (s *Session) persistLocked() {
	if err := s.mgr.store.Save(s.id, s.persistedLocked()); err != nil {
		s.mgr.logf("audio session %s: failed to persist state: %v", s.id, err)
	}
}

// currentItemLocked returns the item at s.currentIndex, from either a
// pinned playlist or a single-media session (index forced to 0).
func (s *Session) currentItemLocked() (pkgaudio.PlaylistItem, bool) {
	if s.desired.Playlist != nil {
		if s.currentIndex < 0 || s.currentIndex >= len(s.desired.Playlist.Items) {
			return pkgaudio.PlaylistItem{}, false
		}
		return s.desired.Playlist.Items[s.currentIndex], true
	}
	if s.desired.Media != nil {
		return pkgaudio.PlaylistItem{ItemID: "media", Index: 0, Media: *s.desired.Media}, true
	}
	return pkgaudio.PlaylistItem{}, false
}

func (s *Session) engineHandleFor(itemID string) EngineHandle {
	return EngineHandle(fmt.Sprintf("%s/%s", s.id, itemID))
}

// releaseEngineLocked discards s's current engine handle, if any. Best
// effort: a release failure is logged, never returned, matching
// [Engine.Release]'s own idempotent contract.
func (s *Session) releaseEngineLocked(ctx context.Context) {
	if !s.handleLoaded {
		return
	}
	if err := s.mgr.engine.Release(ctx, s.handle); err != nil {
		s.mgr.logf("audio session %s: engine release failed: %v", s.id, err)
	}
	s.handleLoaded = false
}

// dispatchedResult is what every dispatch-through-revision method
// produces: outcome plus whether execution actually ran (false for a
// refusal or a cache hit — see [Session.dispatch]).
type dispatchedResult struct {
	outcome  pkgaudio.OutcomeResult
	executed bool
}

// dispatch is the one path through which every command-shaped session
// operation runs: invocation validity, replay/staleness via
// s.revState.Apply, and — only when newly accepted — exec, with the
// result cached under invocation and persisted before returning. exec
// runs with the session already locked and must not itself lock it.
func (s *Session) dispatch(invocation pkgaudio.InvocationID, revision pkgaudio.Revision, exec func() pkgaudio.OutcomeResult) dispatchedResult {
	if invocation == "" {
		return dispatchedResult{outcome: pkgaudio.OutcomeResult{
			Outcome: pkgaudio.OutcomeRefused, Reason: "invocation id is required",
		}}
	}

	// s.revState.Apply is consulted BEFORE the executedResults cache, not
	// after: it is what tells apart a legitimate replay (same invocation,
	// same revision — Accepted, unchanged) from a REUSED invocation id
	// carrying a different revision (ReasonInvocationRevisionMismatch).
	// Checking executedResults first would let the mismatch case slip
	// through as a plain cache hit.
	decision := s.revState.Apply(invocation, revision)
	if !decision.Accepted {
		return dispatchedResult{outcome: *decision.Result}
	}
	if cached, ok := s.executedResults[invocation]; ok {
		return dispatchedResult{outcome: cached}
	}

	result := exec()
	s.executedResults[invocation] = result
	s.persistLocked()
	return dispatchedResult{outcome: result, executed: true}
}

// prepareLocked probes item's readiness — a missing, changed, or
// undecodable asset fails here rather than at Start — and, only when
// ready, loads it on a fresh engine handle. A prior handle must already
// have been released by the caller.
func (s *Session) prepareLocked(ctx context.Context, item pkgaudio.PlaylistItem) (EngineObservation, error) {
	probe := ProbeAsset(ctx, s.mgr.assetDir, item.Media, s.mgr.decoder)
	if probe.State != MediaReady {
		return EngineObservation{}, fmt.Errorf("media not ready: %s", probe.Reason)
	}
	handle := s.engineHandleFor(item.ItemID)
	obs, err := s.mgr.engine.Load(ctx, handle, item.Media, probe.Duration)
	if err != nil {
		return EngineObservation{}, err
	}
	s.handle = handle
	s.handleLoaded = true
	s.currentItemID = item.ItemID
	return obs, nil
}

// advanceLocked is the one path that moves s to its next playlist item,
// used identically by a forced [Manager.Advance] and by the natural-
// completion watcher (forced distinguishes the two only for what happens
// past the last item with [pkgaudio.RepeatNone] — see below). It persists
// the new current item BEFORE touching the engine: a crash between those
// two steps recovers, on [Manager.RestoreAll], to the newly-persisted
// item rather than replaying the one that just completed
// or losing track of the boundary. Single-item (Media, not Playlist)
// sessions have no next item and refuse.
func (s *Session) advanceLocked(ctx context.Context, forced bool) pkgaudio.OutcomeResult {
	if s.desired.Playlist == nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session has no playlist to advance"}
	}
	items := s.desired.Playlist.Items
	next := s.currentIndex
	if s.desired.Playlist.Repeat != pkgaudio.RepeatItem {
		next = s.currentIndex + 1
	}
	if next >= len(items) || next < 0 {
		switch {
		case s.desired.Playlist.Repeat == pkgaudio.RepeatPlaylist:
			next = 0
		case forced:
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "no next playlist item"}
		default:
			s.releaseEngineLocked(ctx)
			s.state = pkgaudio.StateCompleted
			s.bookmark = nil
			s.persistLocked()
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeCompleted}
		}
	}

	item := items[next]
	s.currentIndex = next
	s.currentItemID = item.ItemID
	s.state = pkgaudio.StatePreparing
	s.bookmark = nil
	s.persistLocked() // the persisted advance boundary — see doc comment above.

	s.releaseEngineLocked(ctx)
	dispatchedAt := s.mgr.now()
	if _, err := s.prepareLocked(ctx, item); err != nil {
		s.state = pkgaudio.StateFailed
		s.persistLocked()
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()}
	}
	obs, err := s.mgr.engine.Start(ctx, s.handle, 0)
	if err != nil {
		s.state = pkgaudio.StateFailed
		s.persistLocked()
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()}
	}
	s.state = pkgaudio.StatePlaying
	s.timingKnown = true
	s.persistLocked()
	return confirmLocked(pkgaudio.StatePlaying, pkgaudio.OutcomeStarted, obs, dispatchedAt)
}

// confirmLocked builds an [pkgaudio.OutcomeResult] from an engine
// observation collected no earlier than dispatchedAt — never a
// pre-dispatch reading, which is how a past defect reported a command
// "confirmed" microseconds after its own dispatch off a stale value.
// want is
// the state a successful transition should have reached; obs.State
// mismatching want (including a mismatch produced by a same-tick natural
// completion) is reported as Unconfirmable, never silently treated as
// success.
func confirmLocked(want pkgaudio.State, outcomeIfConfirmed pkgaudio.Outcome, obs EngineObservation, dispatchedAt time.Time) pkgaudio.OutcomeResult {
	if obs.ObservedAt.Before(dispatchedAt) {
		return pkgaudio.OutcomeResult{
			Outcome: pkgaudio.OutcomeUnconfirmable,
			Reason:  "engine evidence predates this dispatch",
		}
	}
	if obs.State != want {
		return pkgaudio.OutcomeResult{
			Outcome: pkgaudio.OutcomeUnconfirmable,
			Reason:  fmt.Sprintf("engine reports state %q, expected %q", obs.State, want),
		}
	}
	return pkgaudio.OutcomeResult{Outcome: outcomeIfConfirmed}
}

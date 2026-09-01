package audio

import (
	"context"
	"errors"
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

	// settingsMu and settings back [Manager.SetSettings]/
	// [Manager.SettingsSnapshot] — see settings.go. Its own mutex, not
	// m.mu: a settings read/write must never contend with session
	// dispatch.
	settingsMu     sync.RWMutex
	settings       Settings
	settingsIssues []string

	// ltc tracks which session, if any, currently owns this node's one
	// LTC run — see ltclifecycle.go.
	ltc ltcOwner

	// corruptSessions is [Manager.RestoreAll]'s record of every persisted
	// file it could not decode into a real session — never
	// addressable by a command, but reported by [Manager.Snapshot] so it
	// is retained fault evidence rather than a silent disappearance.
	corruptSessions []CorruptSessionRecord

	// pendingEngineRestore holds every session id whose restore hit "no
	// engine bound yet" rather than a genuine failure — see
	// restore.go's deferRestoreLocked. RebindEngine drains this set via
	// retryDeferredRestores once an engine is actually set, so a
	// startup that races ahead of the first audio.node binding still
	// resumes once that binding arrives, instead of staying stuck with
	// no engine handle and no persisted Failed record to explain why.
	pendingEngineRestore map[pkgaudio.SessionID]struct{}

	// restoreRetryStatusBySession is internal/agent's own automatic
	// restore-retry driver's status, per session, never written by this
	// package itself — see restoreretry.go's
	// SetRestoreRetryStatus/ClearRestoreRetryStatus and
	// RestoreRetryStatus. A session in pendingEngineRestore reads its own
	// entry back on its own snapshot; nil or a missing entry reports all
	// zero.
	restoreRetryStatusBySession map[pkgaudio.SessionID]restoreRetryStatus

	// rebindMu makes invalidate-Set-retry one atomic unit across
	// concurrent RebindEngine calls. Two audio.node.configure commands
	// delivered back to back produce genuinely concurrent calls (MQTT
	// dispatches each inbound command on its own goroutine — see
	// internal/agent/mqtt.go's HandleMessage and
	// internal/agent/audionodeops.go's applyNode, which releases its own
	// lock before invoking the rebuild callback). Without this, one
	// call's invalidateActiveSessions can run against a pre-swap
	// snapshot while a second call's retryDeferredRestores starts a
	// session against the WRONG engine, and that session's own later
	// Start/Pause failure then persists Failed — the exact permanent
	// damage this package exists to prevent, reached through a new
	// door. Never acquired anywhere but RebindEngine.
	rebindMu sync.Mutex
}

// NewManager builds a Manager. decoder is [RealDecoder]{} in production
// and a fake in tests, matching internal/agent/audiomediaprobe.go's own
// convention for the same interface.
func NewManager(engine Engine, store SessionStore, assetDir string, decoder Decoder, now func() time.Time, logger *slog.Logger) *Manager {
	return &Manager{
		engine: engine, store: store, assetDir: assetDir, decoder: decoder, now: now, logger: logger,
		sessions: make(map[pkgaudio.SessionID]*Session),
		settings: DefaultSettings,
	}
}

// RebindReasonEngineRebind is the [pkgaudio.FaultRouteChanged] reason
// recorded on every session [Manager.RebindEngine] invalidates.
const RebindReasonEngineRebind = "audio output configuration changed; this session's engine handle is no longer valid"

// RebindEngine invalidates every session with an active engine handle
// (never a silent drop: each is set to StateFailed with
// [pkgaudio.FaultRouteChanged] and persisted, see
// [Manager.invalidateActiveSessionsLocked]), swaps m.engine's backing
// implementation via engine.Set, then retries every session
// [Manager.deferRestoreLocked] deferred — in that order, and all under
// rebindMu, so a second RebindEngine call (a second audio.node binding
// delivered concurrently — see rebindMu's own doc comment) can never
// interleave with any part of this one. engine must be the same
// [*SwitchableEngine] this Manager was constructed with; a caller that
// passes any other Engine here defeats this method's whole reason to
// exist, since m.engine itself never changes identity. ctx bounds the
// retry's own prepare/Load/Start calls — pass a context tied to agent
// shutdown, not context.Background(), so a binding delivered while the
// agent is exiting does not run those uncancellably.
func (m *Manager) RebindEngine(ctx context.Context, engine *SwitchableEngine, next Engine, reason string) Engine {
	m.rebindMu.Lock()
	defer m.rebindMu.Unlock()
	m.invalidateActiveSessions(reason)
	prev := engine.Set(next)
	// A nil next detaches the node (rebuild closes the outgoing engine
	// before it probes the device); there is nothing to retry a deferred
	// restore against until the replacement is bound.
	if next != nil {
		m.retryDeferredRestores(ctx)
	}
	return prev
}

// retryDeferredRestores re-runs restoreOne for every session
// deferRestoreLocked deferred, now that engine.Set has just given
// m.engine a real backing implementation. Draining the set before
// looping, rather than while iterating it, makes a second RebindEngine
// firing concurrently with this one race onto a snapshot rather than a
// live map, and it guarantees a session already retried here is removed
// from pendingEngineRestore win or lose (restoreOne itself resolves it
// to either Playing/Paused or a genuine Failed), so a later RebindEngine
// call — the next audio.node binding, or any rebind after that — never
// re-triggers it and never double-starts it.
func (m *Manager) retryDeferredRestores(ctx context.Context) {
	m.mu.Lock()
	ids := make([]pkgaudio.SessionID, 0, len(m.pendingEngineRestore))
	for id := range m.pendingEngineRestore {
		ids = append(ids, id)
	}
	m.pendingEngineRestore = nil
	m.mu.Unlock()

	for _, id := range ids {
		if err := m.restoreOne(ctx, id, true); err != nil {
			m.logf("audio session %s: deferred restore failed: %v", id, err)
		}
	}
}

// invalidateActiveSessions fails every session currently in a state that
// implies a live engine handle. Called before [SwitchableEngine.Set]
// swaps the backing engine out from under every existing handle. A
// session deferRestoreLocked left in Playing/Preparing/Paused with no
// engine handle actually loaded (handleLoaded false) is deliberately
// excluded: it never held a handle on the outgoing engine, so there is
// nothing here to invalidate, and failing it here would be exactly the
// defect this deferral exists to prevent.
func (m *Manager) invalidateActiveSessions(reason string) {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	for _, s := range sessions {
		s.mu.Lock()
		if sessionStateImpliesHandleLocked(s.state) && s.handleLoaded {
			s.state = pkgaudio.StateFailed
			s.setFaultLocked(pkgaudio.FaultRouteChanged, reason)
			s.handle = ""
			s.handleLoaded = false
			s.timingKnown = false
			// The outgoing engine is discarded whole, so only the new
			// engine's state matters: it starts with no owner.
			m.ltc.release(s.id)
			s.persistBestEffortLocked("engine rebind")
		}
		s.mu.Unlock()
	}
}

// sessionStateImpliesHandleLocked reports whether state is one this
// package only ever reaches with a loaded (or loading) engine handle
// behind it — the set [Manager.invalidateActiveSessions] must fail
// rather than leave silently pointing at a handle the new engine has
// never heard of.
func sessionStateImpliesHandleLocked(state pkgaudio.State) bool {
	switch state {
	case pkgaudio.StatePreparing, pkgaudio.StateReady, pkgaudio.StatePlaying, pkgaudio.StatePaused:
		return true
	default:
		return false
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
// [Manager.gateAvailability]. Each session goes through [Session.
// snapshotWithBudget], not a direct Lock/Unlock, so one session wedged
// inside a bounded engine call cannot delay every other session's
// telemetry in the same call — see that method's own doc comment.
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
		out = append(out, s.snapshotWithBudget(ctx, snapshotLockBudget))
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
func (m *Manager) Apply(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision, req pkgaudio.ApplyRequest) pkgaudio.OutcomeResult {
	s := m.getOrCreate(id)
	s.mu.Lock()
	defer s.mu.Unlock()

	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		merged, _, err := req.Merge(s.desired)
		if err != nil {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: err.Error()}
		}
		// "unsupported" only ever appears in an adapter's own capability
		// report (AUDIO-ENGINE section 9): a session may never desire it.
		if merged.MixPolicy != nil && *merged.MixPolicy == pkgaudio.MixPolicyUnsupported {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: `mix policy "unsupported" cannot be requested; it only appears in adapter capability reports`}
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
		// A session that just stopped being a show session (or never was
		// one) must not keep reporting a stale LTC claim it no longer
		// earns — the same clear-on-exit stopLTCLocked applies to a
		// commanded stop, whether or not this session ever actually held
		// the run.
		if !isShowSessionLocked(s) && s.ltcClaimState != LTCClaimNone && s.ltcClaimState != "" {
			m.stopLTCLocked(ctx, s)
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
		// Prepare never leaves a session Playing (it ends Ready or
		// Failed), so any LTC run it owned from before must stop
		// regardless of which of those two this call reaches.
		m.stopLTCLocked(ctx, s)
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
		// A paused session whose bookmark still names the item currently
		// loaded is a Resume, not a Start: under the known pause-fidelity
		// limitation the engine's own position may have moved on since the
		// pause, so replaying the bookmark here would seek it backwards
		// rather than continue it. A bookmark for a different item (an
		// Apply landed while paused) is not this case and falls through to
		// the ordinary stale-handle handling below.
		if s.state == pkgaudio.StatePaused && s.bookmark != nil && s.bookmark.Identity == itemIdentity(item) {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session is paused; use Resume to continue this item, not Start"}
		}
		// A handle loaded for a now-superseded item identity (a media or
		// playlist revision landed via Apply between Prepare and Start) is
		// as stale as no handle at all: starting it would play the OLD
		// content while every other surface reports the new one.
		if !s.handleLoaded || s.loadedIdentity != itemIdentity(item) {
			s.releaseEngineLocked(ctx)
			if _, err := s.prepareLocked(ctx, item); err != nil {
				// No audio.node binding has arrived yet (the same boot
				// window restoreOne's deferral exists for): a session
				// with no other issue must not be permanently converted
				// to Failed on disk just because a command reached it
				// before the binding did. Refuse without touching
				// s.state, so dispatch's own persist re-writes the same
				// state that was already there, not a new one.
				if errors.Is(err, ErrNoEngineBinding) {
					return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "no audio engine bound yet; refused, not failed — retry once an audio.node binding has arrived"}
				}
				s.state = pkgaudio.StateFailed
				m.stopLTCLocked(ctx, s)
				return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
			}
		}
		position, err := s.resolveBookmarkPositionLocked(item)
		if err != nil {
			// Visible and self-healing: the operator sees
			// exactly why this Start was refused, and the stale bookmark
			// is cleared so a subsequent Start is not refused forever by
			// the same dead reference.
			s.bookmark = nil
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "bookmark could not be resolved and was cleared: " + err.Error()}
		}
		dispatchedAt := m.now()
		// This call can still fail for a real engine reason
		// (ClassifyFault below covers those), but never with
		// ErrNoEngineBinding specifically: the branch above already
		// refused and returned if prepareLocked's own Load hit that —
		// the earliest engine call this method makes — so by the time
		// this line runs, m.engine has resolved to SOME bound
		// implementation.
		obs, err := s.mgr.engine.Start(ctx, s.handle, position)
		if err != nil {
			s.state = pkgaudio.StateFailed
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			// Drop the handle so the ordinary operator retry re-prepares
			// instead of calling into this same stale handle and failing
			// identically.
			s.releaseEngineLocked(ctx)
			m.stopLTCLocked(ctx, s)
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
		}
		s.state = pkgaudio.StatePlaying
		s.timingKnown = true
		s.bookmark = nil
		s.lastObservedAt = obs.ObservedAt
		m.startLTCLocked(ctx, s, position)
		return m.gateAvailability(confirmLocked(pkgaudio.StatePlaying, pkgaudio.OutcomeStarted, obs, dispatchedAt))
	})

	// duck/interrupt resolution needs to lock OTHER sessions, so it must
	// run after s.mu is released — see [Manager.duckLowerPriority]'s doc
	// comment on why this can never hold two sessions' locks at once.
	started := res.executed && s.state == pkgaudio.StatePlaying
	duck := res.executed && s.state == pkgaudio.StatePlaying && s.desired.MixPolicy != nil && *s.desired.MixPolicy == pkgaudio.MixPolicyDuck
	interrupt := res.executed && s.state == pkgaudio.StatePlaying && s.desired.MixPolicy != nil && *s.desired.MixPolicy == pkgaudio.MixPolicyInterrupt
	var role pkgaudio.SourceRole
	if s.desired.SourceRole != nil {
		role = *s.desired.SourceRole
	}
	s.mu.Unlock()

	if duck {
		m.duckLowerPriority(ctx, id, role)
	}
	if interrupt {
		m.interruptLowerPriority(ctx, id, role)
	}
	// And the other direction: this session may itself have started
	// underneath a higher-priority session already playing with a duck or
	// interrupt policy. Runs regardless of whether this one ducks others
	// - a show session can duck background and be ducked by an
	// announcement at the same time.
	if started {
		m.submitToActivePolicies(ctx, id, role)
	}
	return res.outcome
}

// Promote moves the already-loaded engine handle from fromID onto toID
// and starts it there, skipping the load Start would otherwise do. It
// exists for exactly one case: a session prepared ahead of time under a
// temporary staging id, once the cue it was staged for is confirmed to
// be the one actually activating, needs to become the well-known show
// session (cueActivationAudioSessionID) without repeating the media
// load that staging already paid for.
//
// toID must already exist — the caller's own Apply against toID (which
// creates it if needed, see getOrCreate's own doc comment) must run
// before Promote, exactly as it already must before Prepare or Start.
// Promote itself calls m.get, never m.getOrCreate, for both fromID and
// toID: it moves a handle between two sessions that already exist, and
// creates neither. getOrCreate's single caller remains Apply.
//
// fromID must be Ready with a loaded handle whose identity matches
// toID's own current desired item (itemIdentity, the same comparison
// Start already makes against its own prior handle). A mismatch —
// wrong guess, or a fresher Apply landed on toID after fromID was
// staged — refuses without touching either session's engine state; the
// caller falls back to Prepare+Start on toID exactly as it would if
// nothing had been staged, and Clear on fromID to discard the stale
// stage (see internal/agent/cueactivationops.go's own bookkeeping).
//
// Never acquires fromID's and toID's session locks at the same time.
// Every other cross-session operation in this file (duckLowerPriority,
// interruptLowerPriority, submitToActivePolicies) holds that same
// invariant, releasing s.mu before locking another session — see
// Manager.duckLowerPriority's own doc comment. Promote's two sessions
// are both fixed, well-known ids, never chosen at runtime, so a
// consistent lock order between them is trivial to state and hold: to
// first (read-only), then from, then to again (the mutating dispatch).
// This is documented here because it is the one place in this file
// that locks two sessions in the same call, and it must never be
// extended to lock them concurrently without re-deriving this
// argument.
//
// Promote reads and writes neither session's owner field. That field
// is coordinator-stamped desired state, not agent mechanics, and an
// absent owner is what lets a session retire on its own — clearing
// fromID's own owner here would make the emptied staging session
// permanently unretirable instead.
func (m *Manager) Promote(ctx context.Context, fromID, toID pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	to, ok := m.get(toID)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	from, ok := m.get(fromID)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "no session staged for promotion"}
	}

	to.mu.Lock()
	item, ok := to.currentItemLocked()
	if !ok {
		to.mu.Unlock()
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session has no media to promote onto"}
	}
	wantIdentity := itemIdentity(item)
	to.mu.Unlock()

	from.mu.Lock()
	switch {
	case from.state != pkgaudio.StateReady:
		from.mu.Unlock()
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "staged session is not ready"}
	case !from.handleLoaded:
		from.mu.Unlock()
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "staged session holds no loaded handle"}
	case from.loadedIdentity != wantIdentity:
		from.mu.Unlock()
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "staged session's content no longer matches what toID now desires"}
	}
	handle := from.handle
	capturedIdentity := from.loadedIdentity
	from.handle = ""
	from.handleLoaded = false
	from.loadedIdentity = ""
	from.state = pkgaudio.StateStopped
	from.timingKnown = false
	from.persistBestEffortLocked("promoted to show session")
	from.mu.Unlock()

	to.mu.Lock()
	res := to.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		item, ok := to.currentItemLocked()
		if !ok || itemIdentity(item) != wantIdentity {
			relCtx, relCancel := boundedObserveContext(ctx)
			if err := m.engine.Release(relCtx, handle); err != nil {
				m.logf("audio session %s: engine release of an orphaned promoted handle failed: %v", toID, err)
			}
			relCancel()
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "toID's desired content changed while promoting; the staged session was already released — retry with an ordinary Prepare and Start"}
		}
		to.handle = handle
		to.handleLoaded = true
		to.loadedIdentity = capturedIdentity
		position, err := to.resolveBookmarkPositionLocked(item)
		if err != nil {
			to.bookmark = nil
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "bookmark could not be resolved and was cleared: " + err.Error()}
		}
		dispatchedAt := m.now()
		obs, err := to.mgr.engine.Start(ctx, to.handle, position)
		if err != nil {
			to.state = pkgaudio.StateFailed
			to.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			to.releaseEngineLocked(ctx)
			m.stopLTCLocked(ctx, to)
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
		}
		to.state = pkgaudio.StatePlaying
		to.timingKnown = true
		to.bookmark = nil
		to.lastObservedAt = obs.ObservedAt
		m.startLTCLocked(ctx, to, position)
		return m.gateAvailability(confirmLocked(pkgaudio.StatePlaying, pkgaudio.OutcomeStarted, obs, dispatchedAt))
	})

	// duck/interrupt resolution needs to lock OTHER sessions, so it must
	// run after to.mu is released — see [Manager.duckLowerPriority]'s doc
	// comment on why this can never hold two sessions' locks at once. The
	// same shape Start's own tail uses: read to's own state/desired here,
	// while to.mu is still held, never after Unlock.
	started := res.executed && to.state == pkgaudio.StatePlaying
	duck := res.executed && to.state == pkgaudio.StatePlaying && to.desired.MixPolicy != nil && *to.desired.MixPolicy == pkgaudio.MixPolicyDuck
	interrupt := res.executed && to.state == pkgaudio.StatePlaying && to.desired.MixPolicy != nil && *to.desired.MixPolicy == pkgaudio.MixPolicyInterrupt
	var role pkgaudio.SourceRole
	if to.desired.SourceRole != nil {
		role = *to.desired.SourceRole
	}
	to.mu.Unlock()

	if !res.executed {
		relCtx, relCancel := boundedObserveContext(ctx)
		if err := m.engine.Release(relCtx, handle); err != nil {
			m.logf("audio session %s: engine release of an unpromoted staged handle failed: %v", toID, err)
		}
		relCancel()
	}

	if duck {
		m.duckLowerPriority(ctx, toID, role)
	}
	if interrupt {
		m.interruptLowerPriority(ctx, toID, role)
	}
	if started {
		m.submitToActivePolicies(ctx, toID, role)
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
		s.bookmark = &pkgaudio.Bookmark{ItemID: s.currentItemID, Identity: s.loadedIdentity, Index: s.currentIndex, Position: obs.Position}
		if s.desired.Playlist != nil {
			s.bookmark.PlaylistRevision = s.desired.Playlist.OwnerRevision
		}
		s.timingKnown = true
		s.lastObservedAt = obs.ObservedAt
		m.stopLTCLocked(ctx, s)
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
		if s.state != pkgaudio.StatePaused {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session is not paused"}
		}
		if len(s.interruptedByAll) > 0 {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session is suspended by an interrupting announcement; it resumes on its own once that ends"}
		}
		item, ok := s.currentItemLocked()
		if !ok {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session has no media or playlist to resume"}
		}
		// A failed Resume below drops the handle but keeps the session
		// Paused with its bookmark intact, so a plain retry (an operator's
		// or the night loop's own per-tick one) lands here with no handle
		// loaded. Mirrors Manager.Start's stale-handle re-prepare branch,
		// but finishes with Engine.Start at the bookmark's position rather
		// than Engine.Resume: a freshly loaded handle was never paused,
		// so there is nothing for Engine.Resume to continue.
		if !s.handleLoaded || s.loadedIdentity != itemIdentity(item) {
			s.releaseEngineLocked(ctx)
			if _, err := s.prepareLocked(ctx, item); err != nil {
				if errors.Is(err, ErrNoEngineBinding) {
					return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "no audio engine bound yet; refused, not failed — retry once an audio.node binding has arrived"}
				}
				s.state = pkgaudio.StateFailed
				m.stopLTCLocked(ctx, s)
				return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
			}
			position, err := s.resolveBookmarkPositionLocked(item)
			if err != nil {
				s.bookmark = nil
				return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "bookmark could not be resolved and was cleared: " + err.Error()}
			}
			dispatchedAt := m.now()
			obs, err := s.mgr.engine.Start(ctx, s.handle, position)
			if err != nil {
				s.state = pkgaudio.StateFailed
				s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
				s.releaseEngineLocked(ctx)
				m.stopLTCLocked(ctx, s)
				return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
			}
			s.state = pkgaudio.StatePlaying
			s.timingKnown = true
			s.bookmark = nil
			s.lastObservedAt = obs.ObservedAt
			m.startLTCLocked(ctx, s, position)
			return m.gateAvailability(confirmLocked(pkgaudio.StatePlaying, pkgaudio.OutcomeStarted, obs, dispatchedAt))
		}
		s.timingKnown = false
		dispatchedAt := m.now()
		obs, err := s.mgr.engine.Resume(ctx, s.handle)
		if err != nil {
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			// Drop the handle but keep the session Paused with its
			// bookmark intact: the next Resume re-prepares through the
			// branch above instead of refusing forever against a handle
			// that no longer exists.
			s.releaseEngineLocked(ctx)
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()})
		}
		s.state = pkgaudio.StatePlaying
		s.bookmark = nil
		s.timingKnown = true
		s.lastObservedAt = obs.ObservedAt
		m.startLTCLocked(ctx, s, obs.Position)
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
			s.bookmark = &pkgaudio.Bookmark{ItemID: s.currentItemID, Identity: s.loadedIdentity, Index: s.currentIndex, Position: obs.Position}
			if s.desired.Playlist != nil {
				s.bookmark.PlaylistRevision = s.desired.Playlist.OwnerRevision
			}
		}
		// A seek is the realignment case: while playing, LTC must jump to
		// the new position's timecode too, never keep emitting the
		// pre-seek one. A seek while paused has no running LTC to
		// realign.
		if s.state == pkgaudio.StatePlaying {
			m.startLTCLocked(ctx, s, obs.Position)
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
		return m.gateAvailability(s.advanceLocked(ctx, true, time.Time{}))
	})
	return res.outcome
}

// Stop is a commanded stop, permanently distinguishable in evidence from
// natural completion — a session that stopped on its own must never be
// reported as if it had been commanded to. Never REFUSED for want of
// engine evidence (ADR-024 decision 7): an idle or unloaded session
// still reports Stopped, and a loaded one always attempts the engine
// call. But a failed Engine.Stop or Release is not silently treated as
// success either: the outcome is Unconfirmable with the failure's
// reason, the same "declared, not refused, not fabricated" shape
// ADR-024 decision 7's other exempt safety actions use. A duck this
// session imposed on others is released once the stop is attempted,
// engine confirmation or not. A failed Engine.Stop leaves the handle
// loaded — never released — so a retried Stop can still address it,
// and a session left in StateStopping that way is re-resolved by
// [Session.checkStopCompletionLocked] once engine evidence confirms it.
// A failed Engine.Release is different: Release always discards the
// handle at the engine regardless of its own outcome, so that evidence
// can never arrive, and this resolves the session immediately instead
// of stranding it in StateStopping behind a poll that can never
// succeed.
func (m *Manager) Stop(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()

	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		if !s.handleLoaded {
			// A fade can be left pending here too: invalidateActiveSessions
			// (an engine rebind after a route change) clears handleLoaded
			// without resolving one. Same hazard as the loaded branch
			// below, reached a different way.
			s.resolveFadePendingStrandedLocked("session stopped before its pending fade resolved")
			s.state = pkgaudio.StateStopped
			s.bookmark = nil
			s.setGapUnknownLocked("session is stopped")
			m.stopLTCLocked(ctx, s)
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStopped})
		}
		s.state = pkgaudio.StateStopping
		// Stopped on the attempt, not on confirmation — the same
		// "declared, not refused, not fabricated" rule this method's own
		// doc comment states for the ducks it releases.
		m.stopLTCLocked(ctx, s)
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
			if releaseErr != nil {
				// Release already discarded the handle at the engine
				// before attempting teardown, success or not (see
				// [gstengine.Engine.Release]), so no later Observe will
				// ever find it again: resolving here is the only way
				// out, since leaving state at StateStopping would strand
				// the session behind [Session.checkStopCompletionLocked],
				// which requires handleLoaded and can never re-confirm a
				// handle the engine has already forgotten.
				s.resolveFadePendingStrandedLocked("session stopped before its pending fade resolved")
				s.handleLoaded = false
				s.loadedIdentity = ""
				s.state = pkgaudio.StateStopped
				s.bookmark = nil
				s.setGapUnknownLocked("session is stopped")
			}
			return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: err.Error()})
		}
		// A fade this Stop interrupted has no engine handle left to
		// report its own completion against (checkFadeCompletionLocked
		// requires handleLoaded), so it must resolve here or it never
		// resolves at all: fadePending would stay true and the
		// invocation that dispatched it would never receive an outcome.
		s.resolveFadePendingStrandedLocked("session stopped before its pending fade resolved")
		s.handleLoaded = false
		s.loadedIdentity = ""
		s.state = pkgaudio.StateStopped
		s.bookmark = nil
		s.setGapUnknownLocked("session is stopped")
		return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStopped})
	})
	// Released on the attempt, not on confirmation: a stuck duck is
	// audible all night, and the outcome carries the engine evidence.
	attempted := res.executed
	s.mu.Unlock()

	if attempted {
		m.restoreDucked(ctx, id)
		m.restoreInterrupted(ctx, id)
	}
	return res.outcome
}

// Clear releases the session entirely: engine resources, desired state,
// and its persisted record. Like Stop, never refused for want of
// evidence, and never refused as stale either: see
// [Session.dispatchExemptFromStaleRevision]'s doc comment for why a
// teardown must always land regardless of the requested revision.
func (m *Manager) Clear(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStopped}
	}
	s.mu.Lock()
	res := s.dispatchExemptFromStaleRevision(invocation, revision, func() pkgaudio.OutcomeResult {
		m.stopLTCLocked(ctx, s)
		s.releaseEngineLocked(ctx)
		// Same hazard Stop resolves: a fade this Clear interrupted has no
		// engine handle left to report against, so it strands forever
		// unless resolved here.
		s.resolveFadePendingStrandedLocked("session cleared before its pending fade resolved")
		s.desired = pkgaudio.SessionDesiredState{}
		s.state = pkgaudio.StateStopped
		s.currentIndex = -1
		s.currentItemID = ""
		s.bookmark = nil
		s.setGapUnknownLocked("session was cleared")
		return m.gateAvailability(pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStopped})
	})
	s.mu.Unlock()

	if res.executed {
		m.restoreDucked(ctx, id)
		m.restoreInterrupted(ctx, id)
		if err := m.store.Delete(id); err != nil {
			m.logf("audio session %s: failed to delete persisted state on clear: %v", id, err)
		}
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}
	return res.outcome
}

package audio

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// FadeState is a session's retained fade-progress state
// (SessionSnapshot.FadeState), reporting-only like timingKnown and
// fadePending: three values because a boolean cannot distinguish "just
// completed" from "never started".
type FadeState string

const (
	FadeStateNone       FadeState = "none"
	FadeStateInProgress FadeState = "in_progress"
	FadeStateComplete   FadeState = "complete"
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

	// Muted records an operator-issued audio.output.mute still in
	// effect. There is no separate restore-target field: the gain
	// audio.output.unmute applies is derived fresh from Desired.Gain
	// (the configured gain) and DuckedByAll at unmute time, never a
	// value captured once and carried forward.
	Muted bool

	// DuckedByAll is every session whose duck mix policy is currently
	// suppressing this session's gain — a set, not a single id, because
	// two overlapping announcements ducking the same background session
	// must both release it before gain is restored. Empty means this
	// session is not ducked. This is the restore-exactly-once guard
	// across a restart: a restore removes and persists, so a repeated or
	// racing restore attempt finds the id already absent and does
	// nothing (see [Manager.removeDuckerLocked]). As with Muted, the
	// restored gain is derived fresh, not carried in a second field.
	DuckedByAll []pkgaudio.SessionID

	// InterruptedByAll is every session whose interrupt mix policy is
	// currently suspending this session — the same set/restore shape as
	// DuckedByAll, but for a full suspend rather than a gain reduction.
	// The position to resume from is this record's own Bookmark, captured
	// exactly as a commanded Pause captures it; no separate field is
	// needed to remember it.
	InterruptedByAll []pkgaudio.SessionID

	// Fault, FaultReason, and FaultAt are the last engine fault reported
	// against this session (AUDIO-ENGINE section 11.4), FaultNone when
	// none is in effect. Persisted so a fault survives a restart instead
	// of silently clearing itself.
	Fault       pkgaudio.SessionFault
	FaultReason string
	FaultAt     time.Time

	// LastProbe is the most recent [ProbeAsset] result for this session's
	// current item — the asset probe evidence the retained observation
	// surface reports.
	LastProbe MediaItemResult

	// FadePending, FadeInvocation, and FadeState mirror the identically
	// named Session fields: a crash mid-fade must not lose track of the
	// pending invocation or leave [Manager.watchTick] unable to ever
	// resolve it. Without these, a restart drops back to
	// fadePending=false regardless of desired.Fade still describing an
	// in-flight ramp, so the dispatching invocation's cached "not yet
	// complete" outcome would never be corrected to a terminal one.
	FadePending    bool
	FadeInvocation pkgaudio.InvocationID
	FadeState      FadeState

	// GapKnown, Gap, GapReason, and GapObservedAt mirror the identically
	// named Session fields — the last measured (or explicitly unmeasured)
	// inter-item gap, so a restart does not silently forget genuine
	// evidence collected before it.
	GapKnown      bool
	Gap           time.Duration
	GapReason     string
	GapObservedAt time.Time
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

	// ListCorrupt reports every persisted record List could not decode
	// well enough to recover a session id from — a malformed or
	// truncated file — so [Manager.RestoreAll] can raise retained fault
	// evidence for it instead of List silently omitting it
	// from the ids it returns, which is indistinguishable from "this
	// session was never persisted at all."
	ListCorrupt() ([]CorruptSessionRecord, error)
}

// CorruptSessionRecord names one persisted record [SessionStore.List]
// could not decode. Filename, not a session id, is the identifying
// evidence here: a malformed file may not even contain a readable id.
type CorruptSessionRecord struct {
	Filename string
	Reason   string
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
	// executedOrder is executedResults' insertion order, oldest first —
	// what [Session.rememberExecutedResultLocked] evicts against. See
	// [maxRetainedInvocations].
	executedOrder []pkgaudio.InvocationID

	state         pkgaudio.State
	handle        EngineHandle
	handleLoaded  bool
	currentIndex  int
	currentItemID string
	bookmark      *pkgaudio.Bookmark

	// loadedIdentity is the item identity (see [itemIdentity]) the engine
	// handle currently in s.handle was actually [Session.prepareLocked]
	// against. It is compared against the CURRENT item's identity before
	// any path (chiefly [Manager.Start]) would otherwise skip repreparing
	// just because handleLoaded is true: desired state can change between
	// a Prepare and a Start (a new Apply landing a different media or
	// playlist item at the same index/id), and handleLoaded alone cannot
	// tell that the loaded content is now stale.
	loadedIdentity string

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

	// fadeDispatchedTarget is the gain [Session.startFadeLocked] actually
	// told the engine to ramp toward, which is the CURRENT effective
	// gain at dispatch time, not necessarily the fade's own configured
	// target: a fade dispatched while suppressed ramps toward
	// duckTargetGain instead. [Session.checkFadeCompletionLocked] judges
	// completion against this, not against a value recomputed later,
	// because a mute or a duck landing mid-fade drives the engine to a
	// NEW gain via SetGain (see [Session.applyEffectiveGainLocked]),
	// which cancels the ramp partway. Judging the cancelled fade against
	// whatever effectiveGainLocked returns after that would compare the
	// engine's evidence to the wrong question and report a cancelled
	// fade as having completed. Not persisted: a restart takes the
	// fadeHandleNeverFaded path instead, which never reads this field.
	fadeDispatchedTarget pkgaudio.Gain

	// fadeHandleNeverFaded marks a fadePending inherited from disk onto a
	// handle that was never given that fade. Such a handle reports
	// FadeActive false from the moment it loads, which is otherwise
	// indistinguishable from a fade that just completed.
	fadeHandleNeverFaded bool

	// fadeState is fadePending's reporting-only companion: it additionally
	// distinguishes a fade that just completed from one that never
	// started, which fadePending's own true-to-false transition cannot —
	// by the time a caller reads fadePending as false, "just completed"
	// and "never started" already look identical. See [FadeState].
	fadeState FadeState

	muted bool

	duckedByAll map[pkgaudio.SessionID]struct{}

	// interruptedByAll is every session whose interrupt mix policy is
	// currently suspending this session — see [PersistedSession.
	// InterruptedByAll].
	interruptedByAll map[pkgaudio.SessionID]struct{}

	fault       pkgaudio.SessionFault
	faultReason string
	faultAt     time.Time
	lastProbe   MediaItemResult

	// lastObservedAt is when [Engine.Observe] (or an equivalent
	// state-changing call) last returned a genuine reading, engine-clock
	// time — never the coordinator's or this process's own wall-clock
	// "now". Zero means no engine evidence has ever been collected for
	// this session. The retained observation surface reports position
	// against this, never against the time it happens to be read.
	lastObservedAt time.Time

	// gapKnown, gap, gapReason, and gapObservedAt are the measured
	// interval between the previous playlist item's natural completion
	// and this item's confirmed start — never derived from a requested
	// transition or an item duration. gapReason is set whenever gapKnown
	// is false, stating why (first item, no advance yet, a forced
	// advance, or a session that is stopped), and is otherwise the one
	// path this package never lets read as a fabricated zero. See
	// [Session.setGapKnownLocked] and [Session.setGapUnknownLocked].
	gapKnown      bool
	gap           time.Duration
	gapReason     string
	gapObservedAt time.Time
}

// sortedSessionIDsLocked returns set's members as a deterministically
// ordered slice, for [PersistedSession.DuckedByAll] and
// [PersistedSession.InterruptedByAll]. Caller holds s.mu.
func sortedSessionIDsLocked(set map[pkgaudio.SessionID]struct{}) []pkgaudio.SessionID {
	if len(set) == 0 {
		return nil
	}
	out := make([]pkgaudio.SessionID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// primaryDuckedByLocked picks one member of set for
// [SessionSnapshot.DuckedBy]'s single-value report — the wire format
// (pkg/mqttproto) still reports one ducking session, so this is advisory
// ("ducked by at least this one"), never the full membership; Ducked is
// what tells a caller ducking is in effect at all. The choice is
// deterministic (lowest id) so it does not flap between report calls
// while the same set is unchanged. Caller holds s.mu.
func primaryDuckedByLocked(set map[pkgaudio.SessionID]struct{}) pkgaudio.SessionID {
	var best pkgaudio.SessionID
	first := true
	for id := range set {
		if first || id < best {
			best, first = id, false
		}
	}
	return best
}

// gapReasonNeverAdvanced is the default gap reason for a session that has
// never run [Session.advanceLocked] to a successor item — a first item and
// a session that never advanced report this identically, since neither
// has a predecessor completion to measure against.
const gapReasonNeverAdvanced = "no playlist advance has occurred yet"

func newSession(id pkgaudio.SessionID, mgr *Manager) *Session {
	return &Session{
		id:              id,
		mgr:             mgr,
		revState:        pkgaudio.NewRevisionState(id),
		executedResults: make(map[pkgaudio.InvocationID]pkgaudio.OutcomeResult),
		state:           pkgaudio.StateUnknown,
		currentIndex:    -1,
		fault:           pkgaudio.FaultNone,
		fadeState:       FadeStateNone,
		gapReason:       gapReasonNeverAdvanced,
	}
}

// setGapKnownLocked records a genuine inter-item gap measurement: gap is
// the interval between the predecessor's completion evidence and the
// successor's confirmed start evidence, at is the successor's own
// engine-clock evidence time. Caller holds s.mu.
func (s *Session) setGapKnownLocked(gap time.Duration, at time.Time) {
	s.gapKnown = true
	s.gap = gap
	s.gapReason = ""
	s.gapObservedAt = at
}

// setGapUnknownLocked records that no gap measurement is available for
// this session's current playlist position, with reason stated rather
// than defaulting the value to zero. Caller holds s.mu.
func (s *Session) setGapUnknownLocked(reason string) {
	s.gapKnown = false
	s.gap = 0
	s.gapReason = reason
	s.gapObservedAt = time.Time{}
}

// maxRetainedInvocations bounds how many invocation decisions/results one
// session retains and persists: unbounded growth here means
// unbounded disk growth and an unbounded reload cost on every restart,
// for history nothing legitimate consults past a client's own retry
// window. [Session.rememberExecutedResultLocked] evicts the oldest entry
// once this is exceeded. This is safe even for a genuinely late replay
// of an evicted invocation: [pkgaudio.RevisionState.Apply] refuses ANY
// invocation, seen or not, whose requested revision is not the session's
// current one, so an evicted invocation's replay is refused as stale
// rather than silently re-executed — eviction can only drop the CACHED
// ANSWER, never the safety property that answer existed to shortcut.
const maxRetainedInvocations = 200

// rememberExecutedResultLocked records result for invocation, evicting
// the oldest tracked invocation once s.executedResults would exceed
// [maxRetainedInvocations]. Overwriting an ALREADY-tracked invocation
// (checkFadeCompletionLocked resolving a fade's terminal outcome onto
// the same invocation GainFade already recorded) is not growth and never
// evicts anything. Caller holds s.mu.
func (s *Session) rememberExecutedResultLocked(invocation pkgaudio.InvocationID, result pkgaudio.OutcomeResult) {
	if _, existed := s.executedResults[invocation]; existed {
		s.executedResults[invocation] = result
		return
	}
	s.executedResults[invocation] = result
	s.executedOrder = append(s.executedOrder, invocation)
	for len(s.executedOrder) > maxRetainedInvocations {
		oldest := s.executedOrder[0]
		s.executedOrder = s.executedOrder[1:]
		delete(s.executedResults, oldest)
	}
}

// retainedDecisionsLocked returns s.revState's own decisions filtered
// down to exactly the invocations [Session.rememberExecutedResultLocked]'s
// eviction still keeps in s.executedResults, so the persisted record's
// two invocation-keyed maps never disagree about which invocations
// survived. [pkgaudio.RevisionState] itself has no bound of its own
// (out of this package's scope to change); this is the boundary this
// package DOES own, applied at the one place a session's record is ever
// written to disk.
func (s *Session) retainedDecisionsLocked() map[pkgaudio.InvocationID]pkgaudio.RevisionDecision {
	all := s.revState.Decisions()
	out := make(map[pkgaudio.InvocationID]pkgaudio.RevisionDecision, len(s.executedResults))
	for id := range s.executedResults {
		if d, ok := all[id]; ok {
			out[id] = d
		}
	}
	return out
}

// persistedLocked snapshots s for [SessionStore.Save]. Caller holds s.mu.
func (s *Session) persistedLocked() PersistedSession {
	return PersistedSession{
		ID:               s.id,
		Desired:          s.desired,
		Revision:         s.revState.Current(),
		Decisions:        s.retainedDecisionsLocked(),
		ExecutedResults:  s.executedResults,
		SessionState:     s.state,
		CurrentIndex:     s.currentIndex,
		CurrentItemID:    s.currentItemID,
		Bookmark:         s.bookmark,
		Muted:            s.muted,
		DuckedByAll:      sortedSessionIDsLocked(s.duckedByAll),
		InterruptedByAll: sortedSessionIDsLocked(s.interruptedByAll),
		Fault:            s.fault,
		FaultReason:      s.faultReason,
		FaultAt:          s.faultAt,
		LastProbe:        s.lastProbe,
		FadePending:      s.fadePending,
		FadeInvocation:   s.fadeInvocation,
		FadeState:        s.fadeState,
		GapKnown:         s.gapKnown,
		Gap:              s.gap,
		GapReason:        s.gapReason,
		GapObservedAt:    s.gapObservedAt,
	}
}

// persistLocked saves s's current state and reports whether the save
// succeeded. Most callers (advanceLocked, checkFadeCompletionLocked, the
// mix.go duck helpers, ...) intentionally discard the error: those
// persists are best-effort bookkeeping on an already-decided transition.
// [Session.dispatch] is the one caller that must not — see its own doc
// comment.
// persistBestEffortLocked persists and logs a failure rather than
// returning it, for transitions whose outcome is already decided. The
// caller-facing contract belongs to dispatch, which propagates instead.
func (s *Session) persistBestEffortLocked(what string) {
	if err := s.persistLocked(); err != nil && s.mgr != nil && s.mgr.logger != nil {
		s.mgr.logger.Warn("audio: persisting session state failed", "session", string(s.id), "after", what, "error", err)
	}
}

func (s *Session) persistLocked() error {
	if err := s.mgr.store.Save(s.id, s.persistedLocked()); err != nil {
		s.mgr.logf("audio session %s: failed to persist state: %v", s.id, err)
		return err
	}
	return nil
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
	relCtx, relCancel := boundedObserveContext(ctx)
	err := s.mgr.engine.Release(relCtx, s.handle)
	relCancel()
	if err != nil {
		s.mgr.logf("audio session %s: engine release failed: %v", s.id, err)
	}
	s.handleLoaded = false
	s.loadedIdentity = ""
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
	s.rememberExecutedResultLocked(invocation, result)
	// A command that ran but could not be durably recorded must not be
	// reported as if it had: on the next crash, this session recovers
	// from whatever the LAST successful persist held, which is not this
	// outcome, so telling the caller it succeeded would be a claim this
	// process cannot back up. The command may well have
	// taken effect (e.g. the engine actually started) — that evidence is
	// not erased — but the OUTCOME reported, and cached for a replay of
	// this same invocation, is the persistence failure, not a success
	// this store cannot survive.
	if err := s.persistLocked(); err != nil {
		result = pkgaudio.OutcomeResult{
			Outcome: pkgaudio.OutcomeFailed,
			Reason:  "operation executed but could not be durably persisted: " + err.Error(),
		}
		s.rememberExecutedResultLocked(invocation, result)
	}
	return dispatchedResult{outcome: result, executed: true}
}

// prepareLocked probes item's readiness — a missing, changed, or
// undecodable asset fails here rather than at Start — and, only when
// ready, loads it on a fresh engine handle. A prior handle must already
// have been released by the caller.
//
// This is the one revalidation choke point: every path that reaches a
// successful Load — Prepare, Start, and the advance/restore paths that
// call this directly — clears any standing fault right here, and a
// probe or Load failure classifies and records one. A reappearing
// device or a re-probed asset therefore stays faulted until an actual
// successful prepare, never resumes silently.
func (s *Session) prepareLocked(ctx context.Context, item pkgaudio.PlaylistItem) (EngineObservation, error) {
	probe := ProbeAsset(ctx, s.mgr.assetDir, item.Media, s.mgr.decoder)
	s.lastProbe = probe
	if probe.State != MediaReady {
		err := fmt.Errorf("media not ready: %s", probe.Reason)
		s.setFaultLocked(mediaFaultToSessionFault(probe.Fault), err.Error())
		return EngineObservation{}, err
	}
	handle := s.engineHandleFor(item.ItemID)
	obs, err := s.mgr.engine.Load(ctx, handle, item.Media, probe.Duration)
	if err != nil {
		s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
		return EngineObservation{}, err
	}
	s.handle = handle
	s.handleLoaded = true
	s.currentItemID = item.ItemID
	s.loadedIdentity = itemIdentity(item)
	s.lastObservedAt = obs.ObservedAt
	s.clearFaultLocked()
	// A fresh handle from Load starts at the engine's own default gain,
	// not this session's configured gain or its active mute/duck state.
	// Every caller that reaches a successful Load through this function,
	// Prepare, Start, playlist advance, and restore, must drive it to
	// the correct effective gain here, once, rather than each repeating
	// that logic or leaving a newly (re)prepared session briefly, or
	// permanently on a failed restart, above its configured maximum.
	s.mgr.applyEffectiveGainBestEffortLocked(ctx, s)
	return obs, nil
}

// itemIdentity is what makes a loaded engine handle valid or stale: the
// item slot (ItemID) plus the exact asset content pinned to it (ADR-028
// identity), so a playlist revision or a media.Apply that replaces the
// asset behind an unchanged ItemID is detected as a change, not missed.
func itemIdentity(item pkgaudio.PlaylistItem) string {
	return item.ItemID + "|" + item.Media.AssetID + "|" + item.Media.ContentHash
}

// mediaFaultToSessionFault maps a pre-flight [MediaFault] (C2's ProbeAsset
// vocabulary) onto the closest runtime [pkgaudio.SessionFault] class, for
// the cases prepareLocked can itself detect: the asset is gone, the asset
// is present but its content no longer matches the pinned reference, or
// it will not decode. Missing and mismatched stay distinct outcomes —
// collapsing them previously sent an operator looking for an absent file
// when the file was present with the wrong content.
// [MediaFaultDurationUnknown] never reaches here — that probe state is
// [MediaReady] with an advisory Reason, not a prepareLocked failure.
func mediaFaultToSessionFault(f MediaFault) pkgaudio.SessionFault {
	switch f {
	case MediaFaultMissing:
		return pkgaudio.FaultMediaDisappeared
	case MediaFaultHashMismatch:
		return pkgaudio.FaultMediaMismatch
	case MediaFaultUndecodable, MediaFaultUnsupportedFormat:
		return pkgaudio.FaultDecodeFailure
	default:
		return pkgaudio.FaultOther
	}
}

// setFaultLocked records fault as this session's current fault, unless
// fault is [pkgaudio.FaultNone] (use [Session.clearFaultLocked] for that
// so every caller states its intent, rather than one function meaning
// two things depending on its argument).
func (s *Session) setFaultLocked(fault pkgaudio.SessionFault, reason string) {
	s.fault = fault
	s.faultReason = reason
	s.faultAt = s.mgr.now()
}

// clearFaultLocked resets this session to no fault. Only called from a
// genuine revalidation ([Session.prepareLocked]'s success path) — never
// from a state transition alone, so a session cannot silently resume
// out of a fault it was never actually revalidated against.
func (s *Session) clearFaultLocked() {
	s.fault = pkgaudio.FaultNone
	s.faultReason = ""
}

// resolveBookmarkPositionLocked validates s.bookmark, if any, against
// item before it may ever reach [Engine.Start]: a negative position, or
// (for a playlist session) a bookmark whose playlist revision or item no
// longer matches via [pkgaudio.Bookmark.Resolve], is reported as an
// error rather than silently substituted with 0 or handed to the engine
// unexamined. A nil bookmark is not an error — it means
// "start from the top" — and returns (0, nil). For a media (non-
// playlist) session, ItemID alone is always the constant "media" and
// cannot distinguish one Apply'd asset from another, so the bookmark's
// own Identity (set from [Session.loadedIdentity] at the moment it was
// taken) must also match item's current [itemIdentity] — the same
// ItemID|AssetID|ContentHash comparison [Manager.Start] already uses to
// decide whether a loaded engine handle is stale. Caller holds s.mu.
func (s *Session) resolveBookmarkPositionLocked(item pkgaudio.PlaylistItem) (time.Duration, error) {
	if s.bookmark == nil {
		return 0, nil
	}
	if s.bookmark.Position < 0 {
		return 0, fmt.Errorf("%w: bookmark position %s is negative", pkgaudio.ErrBookmarkStale, s.bookmark.Position)
	}
	if s.desired.Playlist != nil {
		resolved, err := s.bookmark.Resolve(*s.desired.Playlist)
		if err != nil {
			return 0, err
		}
		if resolved.ItemID != item.ItemID {
			return 0, fmt.Errorf("%w: bookmark resolves to item %q, current item is %q", pkgaudio.ErrBookmarkStale, resolved.ItemID, item.ItemID)
		}
		return s.bookmark.Position, nil
	}
	if s.bookmark.ItemID != "" && s.bookmark.ItemID != item.ItemID {
		return 0, fmt.Errorf("%w: bookmark item %q does not match the current item %q", pkgaudio.ErrBookmarkStale, s.bookmark.ItemID, item.ItemID)
	}
	if s.bookmark.Identity != "" && s.bookmark.Identity != itemIdentity(item) {
		return 0, fmt.Errorf("%w: bookmark asset identity %q does not match the current item's %q", pkgaudio.ErrBookmarkStale, s.bookmark.Identity, itemIdentity(item))
	}
	return s.bookmark.Position, nil
}

// advanceLocked is the one path that moves s past its current item, used
// identically by a forced [Manager.Advance] and by the natural-completion
// watcher (forced distinguishes the two only for what happens past the
// last item with [pkgaudio.RepeatNone] — see below). It persists the new
// current item BEFORE touching the engine: a crash between those two
// steps recovers, on [Manager.RestoreAll], to the newly-persisted item
// rather than replaying the one that just completed or losing track of
// the boundary. A single-item (Media, not Playlist) session has no next
// item to forced-advance to, but it still has an end: the natural-
// completion watcher calling this unforced is exactly how it learns
// playback reached it, and that must still move the session to
// Completed — leaving it refused here left every single-Media session
// reporting Playing forever once the engine was actually done.
//
// completedAt is the predecessor's own completion evidence time — the
// [EngineObservation.ObservedAt] of the Observe call that reported it
// Completed — and is the zero [time.Time] for a forced advance, which has
// no natural completion to measure from. It is the sole input the
// inter-item gap measurement (docs/build/IDENTIFIER-REGISTER.md) is
// computed from; it is never derived from the requested transition or an
// item's known duration.
func (s *Session) advanceLocked(ctx context.Context, forced bool, completedAt time.Time) pkgaudio.OutcomeResult {
	if s.desired.Playlist == nil {
		if s.desired.Media == nil {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session has no media or playlist to advance"}
		}
		if forced {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "no next item to advance to"}
		}
		s.releaseEngineLocked(ctx)
		s.state = pkgaudio.StateCompleted
		s.bookmark = nil
		s.setGapUnknownLocked("session has no playlist to measure a gap within")
		s.mgr.stopLTCLocked(ctx, s)
		s.resolveFadePendingStrandedLocked("session completed before its pending fade resolved")
		s.persistBestEffortLocked("state change")
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeCompleted}
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
			s.setGapUnknownLocked("playlist ended with no successor item")
			s.mgr.stopLTCLocked(ctx, s)
			s.resolveFadePendingStrandedLocked("session completed before its pending fade resolved")
			s.persistBestEffortLocked("state change")
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeCompleted}
		}
	}

	// The gap this advance could produce is decided here, before the
	// successor is even attempted, so every failure path below (prepare,
	// Start) already leaves a stated reason rather than a stale or
	// fabricated value — the success path at the bottom is the only place
	// that ever overwrites this with a genuine measurement.
	switch {
	case forced:
		s.setGapUnknownLocked("advance was operator-forced, not driven by natural completion")
	case completedAt.IsZero():
		s.setGapUnknownLocked("no completion evidence was available for the predecessor item")
	default:
		s.setGapUnknownLocked("successor item did not reach a confirmed start")
	}

	item := items[next]
	s.currentIndex = next
	s.currentItemID = item.ItemID
	s.state = pkgaudio.StatePreparing
	s.bookmark = nil
	// The persisted advance boundary: a failure here voids the
	// crash-recovery guarantee, so it is reported rather than dropped.
	s.persistBestEffortLocked("playlist advance boundary")

	s.releaseEngineLocked(ctx)
	dispatchedAt := s.mgr.now()
	if _, err := s.prepareLocked(ctx, item); err != nil {
		s.state = pkgaudio.StateFailed
		s.mgr.stopLTCLocked(ctx, s)
		s.persistBestEffortLocked("state change")
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()}
	}
	obs, err := s.mgr.engine.Start(ctx, s.handle, 0)
	if err != nil {
		s.state = pkgaudio.StateFailed
		s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
		s.mgr.stopLTCLocked(ctx, s)
		s.persistBestEffortLocked("state change")
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()}
	}
	s.state = pkgaudio.StatePlaying
	s.timingKnown = true
	s.lastObservedAt = obs.ObservedAt
	// The only place a genuine measurement is ever recorded: both sides
	// are engine evidence (the predecessor's own completion, this item's
	// own confirmed start), never a requested transition or a duration.
	// A negative interval is discarded rather than reported, since it can
	// only mean the two observations are not actually comparable evidence.
	if !forced && !completedAt.IsZero() {
		if gap := obs.ObservedAt.Sub(completedAt); gap >= 0 {
			s.setGapKnownLocked(gap, obs.ObservedAt)
		} else {
			s.setGapUnknownLocked("measured gap was negative; discarded as invalid evidence")
		}
	}
	s.mgr.startLTCLocked(ctx, s, 0)
	s.persistBestEffortLocked("state change")
	return confirmLocked(pkgaudio.StatePlaying, pkgaudio.OutcomeStarted, obs, dispatchedAt)
}

// checkStopCompletionLocked re-resolves a session a failed Engine.Stop or
// Release left in StateStopping, once engine evidence shows it actually
// stopped. Caller holds s.mu.
func (s *Session) checkStopCompletionLocked(ctx context.Context) {
	if s.state != pkgaudio.StateStopping || !s.handleLoaded {
		return
	}
	obsCtx, cancel := boundedObserveContext(ctx)
	obs, err := s.mgr.engine.Observe(obsCtx, s.handle)
	cancel()
	if err != nil {
		s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
		return
	}
	if obs.State != pkgaudio.StateStopped {
		return
	}
	relCtx, relCancel := boundedObserveContext(ctx)
	err = s.mgr.engine.Release(relCtx, s.handle)
	relCancel()
	if err != nil {
		s.mgr.logf("audio session %s: engine release failed while resolving stop: %v", s.id, err)
		return
	}
	// Same hazard [Manager.Stop] resolves on its own success path: this
	// is the other half of the same operation completing, and a fade
	// still pending here has no engine handle left to report against.
	s.resolveFadePendingStrandedLocked("session stopped before its pending fade resolved")
	s.handleLoaded = false
	s.loadedIdentity = ""
	s.state = pkgaudio.StateStopped
	s.bookmark = nil
	// This session may never have gone through [Manager.Stop]'s own LTC
	// release (an engine-spontaneous stop reaches here some other way),
	// so this is not a redundant call — a no-op when it already ran.
	s.mgr.stopLTCLocked(ctx, s)
	s.persistBestEffortLocked("state change")
}

// SessionSnapshot is one session's retained telemetry, fresh as of the
// moment [Manager.Snapshot] built it (AUDIO-ENGINE section 15): the
// source this node's audio report (audioreport.go) turns into wire
// evidence and the coordinator's nodeaudio collector turns into
// audio_session.* observations. Every Has*/*Known field distinguishes
// "not set" from the zero value of its paired field — a field left
// false is never rendered by reading its paired value.
type SessionSnapshot struct {
	ID pkgaudio.SessionID

	HasSourceRole bool
	SourceRole    pkgaudio.SourceRole

	HasPlaylist      bool
	PlaylistRevision pkgaudio.Revision

	HasItem   bool
	ItemID    string
	ItemIndex int

	// PositionKnown is false immediately after any discontinuity
	// (mirrors s.timingKnown) or when no handle is loaded. ObservedAt is
	// only meaningful when PositionKnown is true, and is the engine's
	// own evidence time from the fresh [Engine.Observe] this snapshot
	// issued — never this call's own wall-clock time.
	PositionKnown bool
	Position      time.Duration
	ObservedAt    time.Time

	State           pkgaudio.State
	DesiredRevision pkgaudio.Revision

	HasGain    bool
	Gain       pkgaudio.Gain
	HasCeiling bool
	Ceiling    pkgaudio.Ceiling

	FadeState FadeState

	Ducked   bool
	DuckedBy pkgaudio.SessionID

	HasAssetProbe    bool
	AssetProbeState  MediaReadiness
	AssetProbeReason string

	Fault       pkgaudio.SessionFault
	FaultReason string

	// GapKnown, Gap, GapReason, and GapObservedAt are the measured
	// interval between the previous playlist item's natural completion
	// and this item's confirmed start (docs/build/IDENTIFIER-REGISTER.md
	// audio_session.item_gap_ms). GapReason is set whenever GapKnown is
	// false; GapObservedAt is only meaningful when GapKnown is true, and
	// is the successor's own engine-clock evidence time, never this
	// call's own wall-clock time.
	GapKnown      bool
	Gap           time.Duration
	GapReason     string
	GapObservedAt time.Time
}

// snapshotLocked builds s's [SessionSnapshot]. Caller holds s.mu. When a
// handle is loaded this issues one fresh [Engine.Observe] — never the
// cached position a past command call left behind — so Position and
// ObservedAt reflect genuine evidence collected at this call, not
// extrapolated from elapsed wall time by this method itself. An Observe
// failure is itself fault evidence (see [Manager.watchTick]'s identical
// treatment) and leaves PositionKnown false, never a stale reading
// presented as current.
//
// Open question, not yet decided: a session restore.go's
// queueForRetryLocked deferred (no engine bound yet, or a retry-path
// engine failure) reports State exactly as persisted — Playing,
// Preparing, or Paused — with PositionKnown left false and Fault/
// FaultReason naming why. That state value is part of this package's
// public [pkgaudio.State] vocabulary, so changing what gets reported
// for this specific case is a caller-visible contract question, not an
// internal implementation detail; nothing here decides it unilaterally.
func (s *Session) snapshotLocked(ctx context.Context) SessionSnapshot {
	snap := SessionSnapshot{
		ID: s.id, State: s.state, DesiredRevision: s.revState.Current(),
		FadeState: s.fadeState, Fault: s.fault, FaultReason: s.faultReason,
		GapKnown: s.gapKnown, Gap: s.gap, GapReason: s.gapReason, GapObservedAt: s.gapObservedAt,
	}

	if s.desired.SourceRole != nil {
		snap.HasSourceRole, snap.SourceRole = true, *s.desired.SourceRole
	}
	if s.desired.Playlist != nil {
		snap.HasPlaylist, snap.PlaylistRevision = true, s.desired.Playlist.OwnerRevision
	}
	if item, ok := s.currentItemLocked(); ok {
		snap.HasItem, snap.ItemID, snap.ItemIndex = true, item.ItemID, s.currentIndex
	}
	if s.desired.Gain != nil || s.muted || len(s.duckedByAll) > 0 {
		// Reported as the EFFECTIVE gain, not the raw configured value:
		// this is the wire-observable "what is this session actually
		// outputting right now" (pkg/mqttproto, the coordinator's
		// nodeaudio collector), which a mute or a duck must still be
		// able to answer honestly, and each is itself enough evidence to
		// report a gain even before any audio.gain.set has ever landed:
		// effectiveGainLocked's own default (unity, reduced by whichever
		// suppression is active) is well defined regardless.
		snap.HasGain, snap.Gain = true, s.effectiveGainLocked()
	}
	if s.desired.Ceiling != nil {
		snap.HasCeiling, snap.Ceiling = true, *s.desired.Ceiling
	}
	snap.Ducked = len(s.duckedByAll) > 0
	snap.DuckedBy = primaryDuckedByLocked(s.duckedByAll)

	if s.lastProbe.State != "" {
		snap.HasAssetProbe = true
		snap.AssetProbeState = s.lastProbe.State
		snap.AssetProbeReason = s.lastProbe.Reason
	}

	if s.handleLoaded && s.timingKnown {
		obsCtx, cancel := boundedObserveContext(ctx)
		obs, err := s.mgr.engine.Observe(obsCtx, s.handle)
		cancel()
		if err != nil {
			// Mirrors watchTick's identical Observe-failure handling
			// (restore.go): a session this snapshot cannot even observe
			// must not still be reported as Playing.
			s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
			s.state = pkgaudio.StateFailed
			snap.State = s.state
			snap.Fault = s.fault
			snap.FaultReason = s.faultReason
		} else {
			s.lastObservedAt = obs.ObservedAt
			snap.PositionKnown = true
			snap.Position = obs.Position
			snap.ObservedAt = obs.ObservedAt
		}
	}

	return snap
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

package api

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Track F seam F3: the event-driven driver that advances a night session
// out of the states F2's own command handlers never leave on their own
// (transition-to-show, live, transition-to-resting, resting-intershow).
// F4's cue outbox (night_cue_outbox) is untouched here - this seam has no
// lighting/projection/audio cues to dispatch through it. The one
// outward-facing action here is launching the show playlist itself, via
// the existing startPlaylist primitive.

// NightLoop is this seam's own background driver: a periodic, idempotent
// tick, never a continuous reissue loop (RESTING-MODE.md §11).
type NightLoop struct {
	h        *handlers
	interval time.Duration
	logger   *slog.Logger
	inFlight chan struct{} // 1-buffered: acts as a non-blocking mutex.
}

// NewNightLoop builds a [NightLoop] against deps/opts, mirroring
// [NewFPPCommandDispatcher]'s own "build a private *handlers, never route
// through HTTP" construction: this loop needs [handlers.dispatchFPPCommand]
// and this package's other unexported helpers, with no HTTP request of
// its own.
func NewNightLoop(deps Dependencies, opts Options) *NightLoop {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	return &NightLoop{
		h: &handlers{
			deps:                      deps,
			clock:                     opts.Clock,
			logger:                    opts.Logger,
			fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline,
			fppCommandPollInterval:    opts.FPPCommandPollInterval,
			nightReadinessMaxAge:      opts.NightReadinessMaxAge,
		},
		interval: opts.NightLoopInterval,
		logger:   opts.Logger,
		inFlight: make(chan struct{}, 1),
	}
}

// Run ticks until ctx is done, then waits for any in-flight tick to finish
// before returning - the caller (coordinator.go) treats Run's return as
// "this loop no longer touches the store," and a detached goroutine still
// writing after that would violate it.
func (l *NightLoop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			l.inFlight <- struct{}{} // wait for any in-flight tick to release.
			return
		case <-ticker.C:
			select {
			case l.inFlight <- struct{}{}:
				go func() {
					defer func() { <-l.inFlight }()
					l.h.nightTick(ctx, l.h.now())
				}()
			default:
				// Previous tick still running; skip this one.
			}
		}
	}
}

// resolveFPPEndpoint looks up instanceID's currently configured endpoint
// URL - nightasset.go's playlist-definition reads are the only other
// caller.
func (h *handlers) resolveFPPEndpoint(ctx context.Context, instanceID string) (endpoint string, ok bool, err error) {
	if instanceID == "" {
		return "", false, nil
	}
	views, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		return "", false, err
	}
	for _, v := range views {
		if v.InstanceID == instanceID {
			return v.Endpoint, true, nil
		}
	}
	return "", false, nil
}

func (h *handlers) nightTick(ctx context.Context, now time.Time) {
	rec, ok, err := h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil {
		h.logWarn("night loop: failed to read current night session", "error", err)
		return
	}
	if !ok {
		return
	}
	// A degraded session advances nothing, with one exception: fading out
	// is how the operator stops the show through this session, and the
	// three shutdown commands are accepted while degraded precisely so
	// they work. Parking here without ever issuing the stop would take
	// away the one thing a degraded session must still be able to do.
	if rec.Degraded && rec.State != nightStateFadingOut {
		return
	}
	switch rec.State {
	case nightStatePreshow:
		h.nightAdvancePreshow(ctx, now, rec)
	case nightStateRestingIntershow:
		h.nightAdvanceRestingIntershow(ctx, now, rec)
	case nightStateTransitionToShow:
		h.nightAdvanceTransitionToShow(ctx, now, rec)
	case nightStateLive:
		h.nightAdvanceLive(ctx, now, rec)
	case nightStateTransitionToResting:
		h.nightAdvanceTransitionToResting(ctx, now, rec)
	case nightStateFadingOut:
		h.nightAdvanceFadingOut(ctx, now, rec)
	}

	// Track F seam F5: resting.backgroundAudio runs for the WHOLE resting
	// presentation, not on one state's own tick alone - preshow and
	// end-of-night-resting have no autonomous FPP action of their own
	// (this switch's own comment below for end-of-night-resting), but
	// background audio still advances in both, per RESTING-MODE.md §4.3/
	// §5: the bed plays through pre-show, inter-show resting, and
	// post-show alike, never just the two inter-show states.
	//
	// resting-intershow and transition-to-show do NOT unconditionally keep
	// it running: RESTING-MODE.md section 7.1 stages the audio fade
	// beginning at E minus the audio lead, so a fade meant to finish AT E
	// must START before it, not at Live. resting.backgroundAudio.fadeOutMs
	// (config.NightSessionBackgroundAudio) is exactly that field -
	// [handlers.nightBackgroundAudioFadeDownDue] reports true once now has
	// reached E minus fadeOutMs, and only then does either state fall
	// through to the same suspend path every OTHER state uses. Before that
	// lead point (or with no fadeOutMs configured at all, which keeps
	// today's unchanged instant-cut-at-Live behavior), resting-intershow
	// keeps advancing the bed forward and transition-to-show does nothing,
	// letting it keep playing through the transition (found by review: an
	// earlier version hard-stopped at transition-to-show entry, about 20
	// seconds early, which also made the enterShow duck/restore path dead
	// code since the announcement always found an already-resolved stop).
	// Every OTHER state stops a still-playing background session
	// idempotently; a session already stopped or never started costs
	// nothing to check.
	switch rec.State {
	case nightStatePreshow, nightStateEndOfNightResting:
		h.nightAdvanceBackgroundAudio(ctx, now, rec)
	case nightStateRestingIntershow, nightStateTransitionToShow:
		if h.nightBackgroundAudioFadeDownDue(ctx, now, rec) {
			h.nightStopBackgroundAudioIfRunning(ctx, now, rec)
		} else if rec.State == nightStateRestingIntershow {
			h.nightAdvanceBackgroundAudio(ctx, now, rec)
		}
		// transition-to-show, lead not yet reached: intentionally no
		// action, per this switch's own comment above.
	case nightStateStopped:
		// end-session's own clear (nightClearBackgroundAudioAtEndSession,
		// called from handleNightCommand the moment the session record
		// commits to stopped) is warn-and-proceed, not guaranteed: when
		// the node is unreachable, refused, or unacknowledged, that one
		// synchronous attempt never lands. Falling into the default
		// branch below (nightStopBackgroundAudioIfRunning) would be wrong
		// two ways at once - a stop/pause leaves the node's own persisted
		// session record in place for its RestoreAll to resurrect the
		// bed, which is exactly what a clear (and ADR-038's promise of no
		// resume) exists to prevent; and doing nothing at all, as this
		// case once did, leaves the bed audibly playing over a session
		// the operator was told is stopped, for as long as this record
		// stays current - the same defect background audio's own clear
		// was built to fix, one layer down. So this retries the CLEAR
		// itself, through nightRetryEndSessionClear: every tick mints a
		// fresh idempotency key (nightEndSessionClearIdempotencyKey) so a
		// retry can never be answered by the first attempt's own cached,
		// failed outcome, and the retry stops the moment a clear
		// genuinely confirms - a session that already cleared costs only
		// a JSON decode to recheck, never another dispatch.
		h.nightRetryEndSessionClear(ctx, now, rec)
	default:
		h.nightStopBackgroundAudioIfRunning(ctx, now, rec)
	}
	// preparing, end-of-night-resting, stopped, inactive: no autonomous
	// action here. end-of-night-resting's repeating resting playlist was
	// already started on the tick that entered it; its only exit is
	// fade-out-night.
}

// nightBackgroundAudioFadeDownDue is nightTick's own second switch's gate
// for resting-intershow and transition-to-show: true once now has reached
// E minus resting.backgroundAudio.fadeOutMs, the bed's own pre-boundary
// lead (RESTING-MODE.md §7.1's "E - audio lead   begin audio fade",
// applied to the bed the same way [nightEnterShowLeadMs] applies it to
// enterShow cues). False whenever the boundary is not yet armed with a
// known E (nothing to lead against yet) or resting.backgroundAudio is
// absent or carries no fadeOutMs - the unconfigured case is unchanged from
// before this existed: the bed keeps playing at full gain until Live, then
// cuts instantly (nightStopBackgroundAudioIfRunningForNode's own
// FadeOutMs==nil branch).
func (h *handlers) nightBackgroundAudioFadeDownDue(ctx context.Context, now time.Time, rec store.NightSessionRecord) bool {
	boundary, ok := decodeNightBoundary(rec.BoundaryJSON)
	if !ok || boundary.State != nightBoundaryStateArmed || boundary.ExpectedAt == nil {
		return false
	}
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: background audio: failed to read pinned night.session payload for the fade-down lead", "sessionId", rec.ID, "error", err)
		return false
	}
	ba := payload.Resting.BackgroundAudio
	if ba == nil || ba.FadeOutMs == nil {
		return false
	}
	lead := time.Duration(*ba.FadeOutMs) * time.Millisecond
	return !now.Before(boundary.ExpectedAt.Add(-lead))
}

// getPinnedNightSessionPayload reads outside any HTTP request's own
// transaction, unlike nightsessioncontrol.go's tx-bound equivalent.
func (h *handlers) getPinnedNightSessionPayload(ctx context.Context, rec store.NightSessionRecord) (config.NightSessionPayload, error) {
	return nightPinnedNightSessionPayload(ctx, h.deps, rec)
}

// nightPinnedNightSessionPayload is [handlers.getPinnedNightSessionPayload]
// as a package function taking deps directly, for the (non-method) wire
// mapping functions in nightsessioncontrol.go that only have deps, not a
// *handlers.
func nightPinnedNightSessionPayload(ctx context.Context, deps Dependencies, rec store.NightSessionRecord) (config.NightSessionPayload, error) {
	rev, err := deps.Config.GetConfigRevision(ctx, config.NightSessionConfigKind, rec.ConfigObjectID, rec.ConfigRevision)
	if err != nil {
		return config.NightSessionPayload{}, fmt.Errorf("api: get pinned night.session revision %s/%d: %w", rec.ConfigObjectID, rec.ConfigRevision, err)
	}
	var payload config.NightSessionPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return config.NightSessionPayload{}, fmt.Errorf("api: decode pinned night.session payload: %w", err)
	}
	return payload, nil
}

// nightCommit re-reads the current session inside one transaction and
// applies mutate only if it still matches (sessionID, expectState): a
// session an HTTP command has since moved out from under this tick is
// left alone.
func (h *handlers) nightCommit(ctx context.Context, now time.Time, sessionID, expectState string, mutate func(store.NightSessionRecord) store.NightSessionRecord) {
	err := h.deps.NightSessions.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		cur, ok, err := tx.GetCurrentNightSession(ctx)
		if err != nil {
			return err
		}
		if !ok || cur.ID != sessionID || cur.State != expectState {
			return nil
		}
		return tx.UpdateNightSession(ctx, mutate(cur), now)
	})
	if err != nil {
		h.logWarn("night loop: failed to persist night session state", "sessionId", sessionID, "error", err)
	}
}

func (h *handlers) nightCommitAnchor(ctx context.Context, now time.Time, rec store.NightSessionRecord, anchor nightContentAnchor, boundary nightBoundary) {
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.ContentAnchorJSON = encodeNightContentAnchor(anchor)
		cur.BoundaryJSON = encodeNightBoundary(boundary)
		return cur
	})
}

func (h *handlers) nightCommitBoundary(ctx context.Context, now time.Time, rec store.NightSessionRecord, boundary nightBoundary) {
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.BoundaryJSON = encodeNightBoundary(boundary)
		return cur
	})
}

// nightAdvancePreshow runs the resting playlist in repeat mode: preshow's
// own end is the start-night command, never content-driven, so unlike
// resting-intershow (whose FSEQ end IS the show-transition boundary) it
// has nothing for a one-shot item to hand off to.
func (h *handlers) nightAdvancePreshow(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: failed to read pinned night.session payload", "sessionId", rec.ID, "error", err)
		return
	}
	anchor, ready, changed := h.nightEnsureAnchor(ctx, now, rec, nightAnchorPurposeRestingRepeat, payload.Resting.FPPInstanceID, payload.Resting.Playlist, true, 0, fppIfBusyRefuse)
	if !changed {
		return
	}
	if ready {
		h.nightCommitAnchor(ctx, now, rec, anchor, nightBoundary{State: nightBoundaryStateUnknown, Reason: "pre-show resting has no show-transition deadline; start-night ends it"})
		return
	}
	h.nightCommitAnchor(ctx, now, rec, anchor, nightBoundary{State: nightBoundaryStateUnknown, Reason: anchor.Source})
}

// nightDispatchRetryBackoff paces retries of a dispatch refused before it
// reached the wire; nightDispatchRetryWindow bounds how long that may
// continue before the session degrades rather than retrying silently
// forever.
const (
	nightDispatchRetryBackoff = 10 * time.Second
	nightDispatchRetryWindow  = 5 * time.Minute
)

// nightRefusalIsTerminal separates a refusal a later tick may clear (the
// host is busy, or its evidence is not current yet) from one it cannot: a
// missing instance, invalid parameters, or an authorization failure.
func nightRefusalIsTerminal(problem *v1.Problem) bool {
	switch problem.Type {
	case ProblemTypeFPPStartPlaylistBusy, ProblemTypeFPPStartPlaylistEvidenceNotCurrent:
		return false
	}
	return true
}

// nightClockBackstepTolerance: a wall clock that reads earlier than an
// anchor's own ObservedAt by more than this is a clock correction, not
// measurement noise (poll/dispatch latency is sub-second per F0) - the
// persisted absolute ExpectedAt survives a restart, but a backward step
// after it was armed must invalidate rather than silently sit unfired or
// misfire once the clock catches back up.
const nightClockBackstepTolerance = 5 * time.Second

// nightDerivationInvalidRetryLimit bounds how many consecutive ticks a
// derivation-kind invalid boundary (nightBoundaryKindDerivation) may be
// retried from fresh observation before this coordinator gives up and
// degrades for real. A count, not a time window: nightContentAnchor
// already tracks an unrelated retry count this same way (Attempts, for
// shutdown-stop dispatch), and each retry here is already paced one full
// night-loop tick apart, so a count already implies a rough wall-clock
// bound without a second timing concept on the struct. Three attempts
// gives ordinary collector noise a few real chances to clear on its own;
// a duration that is genuinely wrong must not spin past that before
// telling the operator. A var so a test can drive it down.
var nightDerivationInvalidRetryLimit = 3

// nightAdvanceRestingIntershow is rule 3's own load-bearing invalidation:
// an anchor already flagged invalid (BoundaryJSON's own persisted state)
// is never recomputed from - it was invalidated by
// nightInvalidateAnchor, which cleared the observed half, so
// nightEnsureAnchor's own pending-observation path re-derives a fresh
// anchor from new evidence before this function will arm anything again.
func (h *handlers) nightAdvanceRestingIntershow(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: failed to read pinned night.session payload", "sessionId", rec.ID, "error", err)
		return
	}

	anchor, has := decodeNightContentAnchor(rec.ContentAnchorJSON)
	if !has || anchor.Purpose != nightAnchorPurposeRestingOneShot || anchor.ObservedAt.IsZero() {
		var durationMS int64
		if has && anchor.Purpose == nightAnchorPurposeRestingOneShot && anchor.DurationMS > 0 {
			durationMS = anchor.DurationMS
		} else {
			res := nightResolveFSEQDuration(ctx, h.deps, h.deps.Assets, payload.Show, payload.Resting.TimelineAsset)
			if res.Reason != "" {
				// Neither a contradiction nor a derivation - the asset this
				// derivation would need is itself unresolvable, so nothing has
				// been committed yet for this cycle to retry from. Stamped for
				// the record's own self-description; nightBoundaryRetryEligible
				// already reads this as not-eligible like every other
				// non-derivation kind, and in practice this branch is retried
				// unconditionally every tick anyway (no anchor is committed
				// here for the ObservedAt.IsZero() check above to ever find
				// non-zero), so the classification never actually reaches the
				// retry gate below.
				h.nightCommitBoundary(ctx, now, rec, nightBoundary{State: nightBoundaryStateInvalid, Reason: res.Reason, Kind: nightBoundaryKindUnresolvedAsset})
				return
			}
			durationMS = res.DurationMS
		}
		newAnchor, ready, changed := h.nightEnsureAnchor(ctx, now, rec, nightAnchorPurposeRestingOneShot, payload.Resting.FPPInstanceID, payload.Resting.Playlist, false, durationMS, fppIfBusyRefuse)
		if !changed {
			return
		}
		boundary := nightBoundary{State: nightBoundaryStateUnknown, Reason: newAnchor.Source}
		if ready {
			boundary = deriveNightBoundary(newAnchor)
			if boundary.State == nightBoundaryStateInvalid {
				// The only site that can produce this exact pairing (an
				// anchor whose ObservedAt is set, alongside an invalid
				// boundary): nightInvalidateAnchor always clears ObservedAt in
				// the same commit as an invalid boundary, so a contradiction
				// can never leave this pairing behind. newAnchor.DerivationInvalidAttempts
				// already carries forward whatever a prior retry left it at
				// (nightFillAnchorFromObservation only ever touches the
				// observed-evidence fields), so it is deliberately left
				// untouched here - the persisted-invalid check below is what
				// increments it, once, per retry.
				boundary.Kind = nightBoundaryKindDerivation
			} else {
				// A fresh armed derivation ends any retry streak this anchor
				// was on; a LATER, unrelated invalid derivation must not
				// inherit a resolved problem's own attempt count.
				newAnchor.DerivationInvalidAttempts = 0
			}
		}
		h.nightCommitAnchor(ctx, now, rec, newAnchor, boundary)
		return
	}

	// The anchor carries observed evidence, but a PRIOR tick may already
	// have invalidated the boundary derived from it. nightBoundaryRetryEligible
	// draws the line the ruling requires: a CONTRADICTION (rule 3's own
	// load-bearing invalidation - fresh evidence disagreed with playback
	// that was already armed) is never recomputed past, on purpose, and an
	// unclassified boundary (Kind empty - every boundary persisted before
	// this field existed, or one this coordinator declined to classify)
	// reads exactly the same way, conservatively. Only a DERIVATION
	// invalidation (an arithmetic check on this coordinator's own gathered
	// evidence came back invalid; nothing contradicted anything) is
	// eligible for a bounded number of retries from fresh observation
	// before this gives up and degrades for real.
	//
	// This is safe by construction, not just by convention: every
	// contradiction site below (and nightAdvanceTransitionToShow's own
	// early-idle case) calls nightInvalidateAnchor in the SAME commit as
	// the invalid boundary, which clears ObservedAt - so a contradiction-kind
	// boundary can never reach this branch at all; the ObservedAt.IsZero()
	// check above routes it back to the re-derive branch instead. Only a
	// derivation-kind pairing (ObservedAt set, boundary invalid) can ever
	// be read here.
	if persisted, hasBoundary := decodeNightBoundary(rec.BoundaryJSON); hasBoundary && persisted.State == nightBoundaryStateInvalid {
		if nightBoundaryRetryEligible(persisted) && anchor.DerivationInvalidAttempts < nightDerivationInvalidRetryLimit {
			anchor.DerivationInvalidAttempts++
			reason := fmt.Sprintf("retrying an automatically-derived boundary that came back invalid (%s); attempt %d of %d", persisted.Reason, anchor.DerivationInvalidAttempts, nightDerivationInvalidRetryLimit)
			h.nightCommitAnchor(ctx, now, rec, nightInvalidateAnchor(anchor, reason), nightBoundary{State: nightBoundaryStateUnknown, Reason: reason})
			return
		}
		reason := "resting-intershow's content boundary was invalidated (" + persisted.Reason + ") and is never recomputed; run end-session, then prepare-site, to recover"
		if nightBoundaryRetryEligible(persisted) {
			reason = fmt.Sprintf("resting-intershow's content boundary stayed invalid (%s) after %d automatic re-derive attempts; run end-session, then prepare-site, to recover", persisted.Reason, nightDerivationInvalidRetryLimit)
		}
		h.nightDegradeSession(ctx, now, rec, reason)
		return
	}

	obsNow := nightObservePlayback(ctx, h.deps.Observations, anchor.FPPInstanceID, time.Time{}, now)
	if bad, reason := nightBoundaryContradicted(anchor, obsNow, now); bad {
		h.nightCommitAnchor(ctx, now, rec, nightInvalidateAnchor(anchor, reason), nightBoundary{State: nightBoundaryStateInvalid, Reason: reason, Kind: nightBoundaryKindContradiction})
		// Every other contradiction can resolve itself, because something
		// is still playing to re-observe. A stop cannot: re-observation
		// waits for playback that is gone, so this would otherwise hold
		// silently for the rest of the night.
		if obsNow.Status == fppStatusValueIdle {
			h.nightDegradeSession(ctx, now, rec, "resting playback stopped before its expected end and nothing is playing to re-derive a boundary from ("+reason+"); run end-session, then prepare-site, to recover")
		}
		return
	}

	boundary := deriveNightBoundary(anchor)
	if boundary.State != nightBoundaryStateArmed || boundary.ExpectedAt == nil {
		return
	}
	if now.Before(anchor.ObservedAt.Add(-nightClockBackstepTolerance)) {
		reason := "the local clock now reads earlier than this boundary's own anchoring observation; treating as a clock correction"
		h.nightCommitAnchor(ctx, now, rec, nightInvalidateAnchor(anchor, reason), nightBoundary{State: nightBoundaryStateInvalid, Reason: reason, Kind: nightBoundaryKindContradiction})
		return
	}
	// §7.1: the transition begins at E minus the largest enterShow lead,
	// not at E itself, or a fade meant to finish before the resting FSEQ
	// ends only begins after it. See [nightEnterShowLeadMs].
	lead := time.Duration(nightEnterShowLeadMs(payload.EnterShow.Cues)) * time.Millisecond
	if now.Before(boundary.ExpectedAt.Add(-lead)) {
		return
	}
	boundaryE := *boundary.ExpectedAt
	lastTick := now
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.State = nightStateTransitionToShow
		cur.StateEnteredAt = now
		cur.ArmedShowID = uuid.NewString()
		cur.ShowCommitted = false
		cur.Cycle = cur.Cycle + 1
		// The resting anchor is KEPT, not cleared: it is still playing
		// through the lead window and must stay supervised (finding 7)
		// until the first outward-facing cue commits.
		cur.ContentAnchorJSON = encodeNightContentAnchor(anchor)
		cur.BoundaryJSON = encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &boundaryE, LastTickAt: &lastTick, Reason: "content boundary E; enterShow cues and the show launch are relative to it"})
		return cur
	})
}

// nightEnterShowLeadMs is RESTING-MODE.md §7.1's own pre-boundary lead: the
// largest amount of time any enterShow cue asks to run BEFORE E. Only a
// negative offsetMs counts as a lead; a cue at or after E contributes
// nothing here and simply becomes due once its own offset elapses inside
// transition-to-show ([nightAdvanceCueList]'s own due-ness check, now run
// against E rather than this state's entry time).
func nightEnterShowLeadMs(cues []config.NightSessionCue) int64 {
	var lead int64
	for _, cue := range cues {
		if cue.OffsetMs < 0 && int64(-cue.OffsetMs) > lead {
			lead = int64(-cue.OffsetMs)
		}
	}
	return lead
}

// nightClockForwardJumpTolerance mirrors [nightClockBackstepTolerance] the
// other direction: a jump past ordinary tick cadence is a discontinuity,
// not progress, and must not satisfy the hold and barrier deadline at once.
const nightClockForwardJumpTolerance = 30 * time.Second

// nightMarkAttributionDegraded records that an autonomous dispatch ran
// with no authorizing principal on the session. It never blocks the
// dispatch; it makes the gap visible to the operator.
func (h *handlers) nightMarkAttributionDegraded(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	if rec.AttributionDegraded {
		return
	}
	h.logWarn("night loop: dispatching with no authorizing principal recorded on the session", "sessionId", rec.ID)
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.AttributionDegraded = true
		return cur
	})
}

// nightDegradeSession marks rec degraded with reason and stops advancing
// it (nightTick's own top-level guard skips a degraded session) - used
// where an assumption this state depends on (its own boundary, or the
// clock) is no longer trustworthy, rather than silently substituting one.
// One-shot like [handlers.nightMarkAttributionDegraded]: a caller invoked
// more than once for an already-degraded session (directly, rather than
// through nightTick's own guard) must not re-warn or re-log every time.
func (h *handlers) nightDegradeSession(ctx context.Context, now time.Time, rec store.NightSessionRecord, reason string) {
	if rec.Degraded {
		return
	}
	h.logWarn("night loop: session degraded", "sessionId", rec.ID, "reason", reason)
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.Degraded = true
		cur.DegradedReason = reason
		return cur
	})
}

func (h *handlers) nightAdvanceTransitionToShow(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: failed to read pinned night.session payload", "sessionId", rec.ID, "error", err)
		return
	}

	// The resting playback stays supervised through the lead window, up to
	// the moment the show commits. Only a real resting-oneshot anchor is
	// checked; start-night's own boundary has none - the first show has
	// no playback to supervise.
	if !rec.ShowCommitted {
		if anchor, has := decodeNightContentAnchor(rec.ContentAnchorJSON); has && anchor.Purpose == nightAnchorPurposeRestingOneShot {
			obsNow := nightObservePlayback(ctx, h.deps.Observations, anchor.FPPInstanceID, time.Time{}, now)
			if bad, reason := nightBoundaryContradicted(anchor, obsNow, now); bad {
				h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
					cur.State = nightStateRestingIntershow
					cur.StateEnteredAt = now
					cur.ArmedShowID = ""
					cur.ContentAnchorJSON = encodeNightContentAnchor(nightInvalidateAnchor(anchor, reason))
					cur.BoundaryJSON = encodeNightBoundary(nightBoundary{State: nightBoundaryStateInvalid, Reason: reason, Kind: nightBoundaryKindContradiction})
					if obsNow.Status == fppStatusValueIdle {
						cur.Degraded = true
						cur.DegradedReason = "resting playback stopped during the transition into a show and nothing is playing to re-derive a boundary from (" + reason + "); run end-session, then prepare-site, to recover"
					}
					return cur
				})
				return
			}
		}
	}

	// E is persisted, never substituted: an absent or malformed boundary is
	// a stated degraded condition, not a fallback to this state's own
	// entry time (that reproduced the collapse-to-zero defect).
	b, ok := decodeNightBoundary(rec.BoundaryJSON)
	if !ok || b.ExpectedAt == nil {
		h.nightDegradeSession(ctx, now, rec, "transition-to-show has no persisted content boundary; end-session and prepare-site again to recover")
		return
	}
	boundaryE := *b.ExpectedAt

	// A gap outside ordinary tick cadence, either direction, is a clock
	// discontinuity: resync the checkpoint and wait for a sane gap before
	// evaluating hold or the deadline again (also covers a restart
	// benignly - one skipped tick, not a false "jumped" degrade).
	if b.LastTickAt != nil && (now.Before(b.LastTickAt.Add(-nightClockBackstepTolerance)) || now.After(b.LastTickAt.Add(nightClockForwardJumpTolerance))) {
		lastTick := now
		h.nightCommitBoundary(ctx, now, rec, nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &boundaryE, LastTickAt: &lastTick, Reason: "resynchronized after a clock discontinuity"})
		return
	}
	lastTick := now

	// Cues run every tick regardless of hold: an offset cue may legitimately
	// fire before the hold elapses. The launch itself (below) waits on both
	// hold AND every barrier cue's own resolved outcome.
	barrierOK, blockedReason := h.nightAdvanceCueList(ctx, now, rec, boundaryE, nightPhaseEnterShow, payload.EnterShow.Cues, payload)

	hold := time.Duration(payload.EnterShow.BlackoutHoldMs) * time.Millisecond
	if now.Before(boundaryE.Add(hold)) {
		return
	}
	if !barrierOK {
		h.nightCommitBoundary(ctx, now, rec, nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &boundaryE, LastTickAt: &lastTick, Reason: blockedReason})
		return
	}
	// ifBusy is decided ONCE here, from a snapshot read; the dispatch is a
	// separate moment and is not re-checked a second time before it - see
	// [handlers.nightShowLaunchIfBusy]'s own doc comment for why that is
	// safe only because replace is granted solely on positive identity.
	ifBusy := h.nightShowLaunchIfBusy(ctx, now, payload)
	anchor, ready, changed := h.nightEnsureAnchor(ctx, now, rec, nightAnchorPurposeShow, payload.ShowPlaylist.FPPInstanceID, payload.ShowPlaylist.Playlist, false, 0, ifBusy)
	if !changed {
		return
	}
	if !ready {
		// anchor.Source carries the primitive's own refusal detail. The
		// session stays in transition-to-show; live is never entered.
		h.nightCommitAnchor(ctx, now, rec, anchor, nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &boundaryE, LastTickAt: &lastTick, Reason: anchor.Source})
		return
	}
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.State = nightStateLive
		cur.StateEnteredAt = now
		cur.ShowCommitted = true
		cur.ContentAnchorJSON = encodeNightContentAnchor(anchor)
		cur.BoundaryJSON = ""
		return cur
	})
}

// nightShowLaunchEvidenceMaxAge is deliberately far tighter than
// fpp.DefaultValidFor's 45s: stale identity evidence here can license
// silently replacing whatever FPP's own scheduler started in the gap.
const nightShowLaunchEvidenceMaxAge = 5 * time.Second

// nightShowLaunchIfBusy returns replace only when ALL hold, checked at
// this call site rather than trusted from a distant invariant: resting
// and show share one FPP instance; payload.Resting.Playlist is non-empty;
// fpp.status is current, not idle, not "unknown"; fpp.playlist.name is
// current, equals payload.Resting.Playlist, and is no older than
// nightShowLaunchEvidenceMaxAge. Every other case returns refuse.
//
// SAFETY: once this returns replace, nothing else checks again - replace
// bypasses startPlaylist's own PreDispatchCheck by FPP's own design, so
// this function is the ONLY guard against replacing an unrelated running
// playlist. Refuse, not replace, gets a backstop (PreDispatchCheck
// re-evaluates fresh evidence at dispatch time).
func (h *handlers) nightShowLaunchIfBusy(ctx context.Context, now time.Time, payload config.NightSessionPayload) string {
	if payload.Resting.Playlist == "" || payload.Resting.FPPInstanceID == "" ||
		payload.Resting.FPPInstanceID != payload.ShowPlaylist.FPPInstanceID {
		return fppIfBusyRefuse
	}
	instanceID := payload.ShowPlaylist.FPPInstanceID

	statusVal, _, statusCurrent, _, _ := resolveConfirmationEvidence(ctx, h.deps.Observations, instanceID, fppStatusSignal, time.Time{}, now)
	statusStr, _ := statusVal.(string)
	if !statusCurrent || statusStr == fppStatusValueIdle || statusStr == fppStatusValueUnknown {
		return fppIfBusyRefuse
	}

	nameVal, _, nameCurrent, _, _ := resolveConfirmationEvidence(ctx, h.deps.Observations, instanceID, fppPlaylistNameSignal, time.Time{}, now)
	if !nameCurrent {
		return fppIfBusyRefuse
	}
	nameStr, ok := nameVal.(string)
	if !ok || nameStr != payload.Resting.Playlist {
		return fppIfBusyRefuse
	}
	collectedAt, ok := nightResolveCollectedAt(ctx, h.deps.Observations, instanceID, fppPlaylistNameSignal, time.Time{}, now)
	if !ok || now.Sub(collectedAt) > nightShowLaunchEvidenceMaxAge {
		return fppIfBusyRefuse
	}
	return fppIfBusyReplace
}

// nightAdvanceLiveDeadline bounds how long nightAdvanceLive may wait, from
// the moment the session entered nightStateLive, for the three completion
// conditions below to all be met. A SHOWMESH HYPOTHESIS, not a measured
// value: long enough that no real show's own end-of-show evidence is
// mistaken for a stall, short enough that a genuinely missing FPP observer
// does not leave the night silently live until morning. Exceeding it never
// forces the transition (that would fabricate end evidence rule 4
// specifically forbids); it only ends the SILENCE, via
// [handlers.nightDegradeSession]. A var so a test can drive it down.
var nightAdvanceLiveDeadline = 90 * time.Minute

// nightAdvanceLive is rule 4: completion evidence, never graceful-stop
// acceptance. F0's own captured shape is the exact condition checked -
// status_name is "idle" AND current_playlist has genuinely cleared -
// which requires the playlist-name evidence to be CURRENT: an absent,
// stale, or unsupported reading also decodes to "", indistinguishable
// from genuine idle unless currency is checked separately.
//
// Absent or stale end-of-show evidence must not leave the night live
// forever with no operator-visible sign of why: past
// [nightAdvanceLiveDeadline], this WARNs with whichever of the three
// conditions is unmet - they are three different faults - and degrades the
// session, without ever moving it out of live on a timeout alone.
func (h *handlers) nightAdvanceLive(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	anchor, has := decodeNightContentAnchor(rec.ContentAnchorJSON)
	if !has || anchor.Purpose != nightAnchorPurposeShow {
		return
	}
	obs := nightObservePlayback(ctx, h.deps.Observations, anchor.FPPInstanceID, anchor.ObservedAt, now)
	var unmet string
	switch {
	case !obs.Current:
		unmet = fmt.Sprintf("playback status/position evidence for FPP instance %q is not current", anchor.FPPInstanceID)
	case obs.Status != fppStatusValueIdle:
		unmet = fmt.Sprintf("playback status is %q, not idle", obs.Status)
	case !obs.PlaylistCurrent:
		unmet = fmt.Sprintf("current-playlist evidence for FPP instance %q is not current", anchor.FPPInstanceID)
	case obs.Playlist != "":
		unmet = fmt.Sprintf("current playlist is still named %q, not cleared", obs.Playlist)
	}
	if unmet != "" {
		if now.Sub(rec.StateEnteredAt) >= nightAdvanceLiveDeadline {
			h.nightDegradeSession(ctx, now, rec, fmt.Sprintf(
				"live has produced no end-of-show completion evidence for %s: %s; run end-session, then prepare-site, to recover",
				nightAdvanceLiveDeadline, unmet))
		}
		return
	}
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.State = nightStateTransitionToResting
		cur.StateEnteredAt = now
		cur.ContentAnchorJSON = ""
		cur.BoundaryJSON = ""
		return cur
	})
}

func (h *handlers) nightAdvanceTransitionToResting(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: failed to read pinned night.session payload", "sessionId", rec.ID, "error", err)
		return
	}
	// A deferred shutdown outranks entry into resting: the show it was
	// waiting on has finished, so nothing else starts and the resting
	// fade-up cues never run.
	if rec.ShutdownIntent != "" {
		h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
			cur.State = nightStateFadingOut
			cur.StateEnteredAt = now
			cur.ArmedShowID = ""
			cur.ShowCommitted = false
			cur.ContentAnchorJSON = ""
			cur.BoundaryJSON = encodeNightBoundary(nightBoundary{State: nightBoundaryStateInvalid, Reason: "a shutdown was requested during the final show; end-of-night resting is not started"})
			return cur
		})
		return
	}

	// §7.2's fade-up cues run independently of the resting-playlist restart
	// below: unlike enter-show, no atomic commit boundary or barrier gates
	// entry into resting on their outcome (RESTING-MODE.md §7.2's ordering
	// note: "show completion remains the authoritative anchor").
	h.nightAdvanceCueList(ctx, now, rec, rec.StateEnteredAt, nightPhaseEnterResting, payload.EnterResting.Cues, payload)

	hold := time.Duration(payload.EnterResting.BlackoutAfterShowMs) * time.Millisecond
	if now.Sub(rec.StateEnteredAt) < hold {
		return
	}

	if rec.FinalShowRequested {
		anchor, ready, changed := h.nightEnsureAnchor(ctx, now, rec, nightAnchorPurposeRestingRepeat, payload.Resting.FPPInstanceID, payload.Resting.EndOfNightPlaylist, payload.Resting.EndOfNightRepeat, 0, fppIfBusyRefuse)
		if !changed {
			return
		}
		if !ready {
			h.nightCommitAnchor(ctx, now, rec, anchor, nightBoundary{State: nightBoundaryStateUnknown, Reason: anchor.Source})
			return
		}
		h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
			cur.State = nightStateEndOfNightResting
			cur.StateEnteredAt = now
			cur.ContentAnchorJSON = encodeNightContentAnchor(anchor)
			cur.BoundaryJSON = encodeNightBoundary(nightBoundary{State: nightBoundaryStateUnknown, Reason: "end-of-night resting has no show-transition deadline"})
			return cur
		})
		return
	}

	res := nightResolveFSEQDuration(ctx, h.deps, h.deps.Assets, payload.Show, payload.Resting.TimelineAsset)
	if res.Reason != "" {
		h.nightCommitBoundary(ctx, now, rec, nightBoundary{State: nightBoundaryStateInvalid, Reason: res.Reason})
		return
	}
	anchor, ready, changed := h.nightEnsureAnchor(ctx, now, rec, nightAnchorPurposeRestingOneShot, payload.Resting.FPPInstanceID, payload.Resting.Playlist, false, res.DurationMS, fppIfBusyRefuse)
	if !changed {
		return
	}
	if !ready {
		h.nightCommitAnchor(ctx, now, rec, anchor, nightBoundary{State: nightBoundaryStateUnknown, Reason: anchor.Source})
		return
	}
	boundary := deriveNightBoundary(anchor)
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.State = nightStateRestingIntershow
		cur.StateEnteredAt = now
		cur.ContentAnchorJSON = encodeNightContentAnchor(anchor)
		cur.BoundaryJSON = encodeNightBoundary(boundary)
		return cur
	})
}

// nightEnsureAnchor is this file's one dispatch/observe primitive. Given
// rec's current ContentAnchorJSON, it either (a) finds a matching,
// already-complete anchor and returns it unchanged; (b) finds a matching,
// dispatched-but-not-yet-observed anchor (DispatchedAt set, ObservedAt
// zero - including one nightInvalidateAnchor just reset) and polls
// EXISTING evidence for it to complete, without dispatching again; or (c)
// dispatches ONE startPlaylist call and folds in whatever evidence is
// available once dispatchFPPCommand's own bounded confirmation returns.
//
// changed reports whether the caller must persist a new anchor value;
// ready reports whether that anchor now carries post-dispatch observed
// evidence and is safe to derive a boundary from.
func (h *handlers) nightEnsureAnchor(ctx context.Context, now time.Time, rec store.NightSessionRecord, purpose, instanceID, playlist string, repeat bool, durationMS int64, ifBusy string) (anchor nightContentAnchor, ready, changed bool) {
	cur, has := decodeNightContentAnchor(rec.ContentAnchorJSON)
	matches := has && cur.Purpose == purpose && cur.Playlist == playlist

	if matches && !cur.DispatchedAt.IsZero() {
		if !cur.ObservedAt.IsZero() {
			return cur, true, false
		}
		obs := nightObservePlayback(ctx, h.deps.Observations, instanceID, cur.DispatchedAt, now)
		if obs.Current && obs.Status == fppStatusValuePlaying && obs.Playlist == playlist {
			if nightFillAnchorFromObservation(ctx, h.deps.Observations, instanceID, obs, cur.DispatchedAt, now, &cur) {
				return cur, true, true
			}
		}
		return cur, false, false
	}

	// A prior attempt was refused before reaching the wire. A terminal
	// refusal is not retried; a transient one waits out its backoff and
	// degrades the session once it has been failing for the whole window.
	if matches && !cur.AttemptedAt.IsZero() {
		switch {
		case cur.RefusalTerminal:
			return cur, false, false
		case now.Sub(cur.FirstAttemptAt) >= nightDispatchRetryWindow:
			h.nightDegradeSession(ctx, now, rec, fmt.Sprintf(
				"playlist %q could not be started on FPP instance %q for %s: %s; run end-session, then prepare-site, to recover",
				playlist, instanceID, nightDispatchRetryWindow, cur.Source))
			return cur, false, false
		case now.Sub(cur.AttemptedAt) < nightDispatchRetryBackoff:
			return cur, false, false
		}
	}

	issuer := nightControllerIssuer(rec)
	if nightAttributionMissing(rec) {
		// The dispatch still happens: refusing it would leave the show in
		// whatever state a restart found. The gap is recorded instead.
		h.nightMarkAttributionDegraded(ctx, now, rec)
	}

	// idemKey mints a fresh key per tick that reaches this branch (which
	// only happens once per purpose/cycle in the ordinary path, since a
	// successful or refused dispatch persists DispatchedAt and the branch
	// above then owns it): it protects against this ONE call being
	// retried internally, not against a second tick redispatching - a
	// failed dispatch (err != nil, nothing persisted) legitimately mints
	// a new key and retries on a later tick.
	idemKey := fmt.Sprintf("night:%s:%d:%s:%d", rec.ID, rec.Cycle, purpose, now.UnixNano())
	outcome, problem, err := h.dispatchFPPCommand(ctx, now, FPPCommandInput{
		InstanceID:                  instanceID,
		Action:                      "startPlaylist",
		Params:                      map[string]any{"playlist": playlist, "repeat": repeat, "ifBusy": ifBusy},
		IdempotencyKey:              idemKey,
		Issuer:                      issuer,
		NeverWithholdOnAuditFailure: true,
	})
	if err != nil {
		h.logWarn("night loop: dispatch startPlaylist failed", "sessionId", rec.ID, "instanceId", instanceID, "error", err)
		return nightContentAnchor{}, false, false
	}

	// Nothing reached FPP, either because a guard refused it or because
	// the host did not accept the request. DispatchedAt stays zero so the
	// branch above keeps retrying under this same purpose rather than
	// treating it as a dispatch whose evidence merely has not arrived.
	if problem != nil || outcome.DispatchFailed {
		reason := "the request did not reach FPP: " + outcome.OutcomeReason
		terminal := false
		if problem != nil {
			reason = "refused: " + problem.Detail
			terminal = nightRefusalIsTerminal(problem)
		}
		next := nightContentAnchor{
			Purpose: purpose, FPPInstanceID: instanceID, Playlist: playlist,
			DurationMS: durationMS, RepeatMode: repeat,
			FirstAttemptAt: now, AttemptedAt: now,
			RefusalTerminal: terminal,
			Source:          reason,
		}
		if matches && !cur.FirstAttemptAt.IsZero() {
			next.FirstAttemptAt = cur.FirstAttemptAt
		}
		return next, false, true
	}

	dispatchedAt := now
	if outcome.DispatchedAt != nil {
		dispatchedAt = *outcome.DispatchedAt
	}
	next := nightContentAnchor{Purpose: purpose, FPPInstanceID: instanceID, Playlist: playlist, DurationMS: durationMS, RepeatMode: repeat, DispatchedAt: dispatchedAt}
	if outcome.Outcome == "confirmed" {
		obs := nightObservePlayback(ctx, h.deps.Observations, instanceID, dispatchedAt, now)
		if obs.Current {
			if nightFillAnchorFromObservation(ctx, h.deps.Observations, instanceID, obs, dispatchedAt, now, &next) {
				return next, true, true
			}
		}
	}
	next.Source = outcome.OutcomeReason
	return next, false, true
}

// nightFillAnchorFromObservation completes anchor's observed half from
// obs, using the POSITION signal's own CollectedAt as ObservedAt rather
// than the status signal's - the two are read independently and can
// disagree (finding: a 40s-old position paired with a fresh status
// reading armed a boundary 40s late while reporting "armed"). Refuses
// (returns false) when no position evidence is current at all, or when
// the status and position observations' own CollectedAt differ by more
// than nightAnchorEvidenceTolerance - either way the caller keeps polling
// rather than anchor from mismatched evidence.
func nightFillAnchorFromObservation(ctx context.Context, lister ObservationLister, instanceID string, obs nightPlaybackObservation, notBefore, now time.Time, anchor *nightContentAnchor) bool {
	if !obs.PositionMSCurrent && !obs.PositionCurrent {
		return false
	}
	statusCollectedAt, sOK := nightResolveCollectedAt(ctx, lister, instanceID, fppStatusSignal, notBefore, now)
	positionCollectedAt, pOK := nightResolvePositionCollectedAt(ctx, lister, instanceID, notBefore, now)
	if !sOK || !pOK {
		return false
	}
	diff := statusCollectedAt.Sub(positionCollectedAt)
	if diff < 0 {
		diff = -diff
	}
	if diff > nightAnchorEvidenceTolerance {
		return false
	}
	anchor.ObservedAt = positionCollectedAt
	anchor.Item = obs.Item
	anchor.PositionSeconds = obs.PositionSeconds
	anchor.PositionMS, anchor.PositionMSKnown = obs.PositionMS, obs.PositionMSCurrent
	anchor.RepeatMode = obs.RepeatMode
	return true
}

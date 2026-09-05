package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// This file is Track H seam H4's own activation trigger: the periodic,
// idempotent tick that turns an accepted FPP playlist-entry observation
// into a dispatched cue.activate (or an H0.2 mismatch effect), for every
// instance this coordinator has heard from. It is deliberately NOT
// DISPATCHED FROM fppobservations.go's own POST handler — that file's own
// doc comment states its rule explicitly ("Ingestion grants no execution
// authority: this method calls nothing in this package's macro, command,
// or cue-dispatch paths"), and this loop is what acts on that evidence
// instead, mirroring nightloop.go's own event-driven-tick shape one seam
// over (NightLoop) rather than reusing it — Track H's own H4 brief
// forbids touching the night-session cue machinery, names and concepts
// both.
//
// fppobservations.go's POST handler DOES call [CueActivationLoop.Nudge]
// after accepting a genuinely new (non-replay) observation, mirroring
// [assetsync.Service.Nudge]'s identical "an upload nudges the sync
// service" pattern one seam over (see [CueActivationNudger]'s own doc
// comment for why this is not a rule violation): Nudge only makes the
// loop's own next reconcile-and-decide pass run promptly instead of
// waiting out interval; it does not decide, authorize, or dispatch
// anything itself, and the loop still independently runs the identical
// Reconcile/Decide/Authorize path it would on its own scheduled tick.
// Without it, a fresh entry change was invisible to a wall for up to
// interval (1 second) — long enough to be operator-visible on a real
// show; the periodic tick remains as the fallback for every case Nudge
// does not cover (a coordinator restart, a dropped nudge under
// backpressure, evidence that changes for a reason other than a fresh
// POST).
//
// This is NOT the frame-rate timing path: the tick period is bounded well
// above FPP's own MultiSync frame rate, and [cueactivate.Decide]'s own
// idempotent ActivationID means a tick over an UNCHANGED entry replays the
// same command row (store.DuplicateCommandError) rather than publishing
// again — an activation is dispatched once per ENTRY change, never once
// per tick and never once per frame.

// CueActivationLoop is this seam's background driver.
type CueActivationLoop struct {
	h        *handlers
	interval time.Duration
	logger   *slog.Logger
	inFlight chan struct{} // 1-buffered: acts as a non-blocking mutex, mirroring NightLoop's own.

	// nudge is buffered 1: a pending, not-yet-consumed nudge coalesces
	// with a second one rather than queuing — matching
	// [assetsync.Service]'s own identically-shaped field one seam over
	// (internal/coordinator/assetsync/sync.go): a tick is about to run
	// anyway, and a second "run now" before the first has even started
	// adds nothing.
	nudge chan struct{}

	// minNudgeInterval and nudgeMu/lastNudgeTick are defect 6's own fix:
	// [Options.CueActivationNudgeMinInterval]'s own doc comment for why a
	// floor between NUDGE-DRIVEN ticks exists at all (the periodic
	// ticker's own cadence is untouched by this — it always ticks every
	// l.interval regardless of any nudge).
	minNudgeInterval time.Duration
	nudgeMu          sync.Mutex
	lastNudgeTick    time.Time

	// pinMu/pin hold ADR-033 show mode's own frozen authorization identity
	// pin is nil whenever program mode applies (Decide/Authorize
	// then resolve live, exactly as before this field existed), and holds
	// a live *cueactivate.ShowPin whenever show mode applies, for as long
	// as the active Show/Generation stays the one the pin was minted for,
	// see [CueActivationLoop.resolvePin]'s own doc comment for exactly
	// when it is (re)started or dropped. This is loop-lifetime state, not
	// per-tick state, which is the entire point: a mid-show show.cue edit
	// must never reach a fresh resolution before the show itself restarts.
	pinMu    sync.Mutex
	pin      *cueactivate.ShowPin
	pinnedAt time.Time
}

// PinStatus implements [CueActivationPinStatus] for GET
// /api/v1/config/show.mode: the operator-visible surface for
// ADR-033 show mode's own frozen authorization identity, whenever one is
// held.
func (l *CueActivationLoop) PinStatus() (pinned bool, show string, generation int64, pinnedAt time.Time) {
	l.pinMu.Lock()
	defer l.pinMu.Unlock()
	if l.pin == nil {
		return false, "", 0, time.Time{}
	}
	return true, l.pin.Active.ShowID, l.pin.Active.Generation, l.pinnedAt
}

// NewCueActivationLoop builds a [CueActivationLoop] against deps/opts,
// mirroring [NewNightLoop]'s identical "build a private *handlers, never
// route through HTTP" construction.
func NewCueActivationLoop(deps Dependencies, opts Options) *CueActivationLoop {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	return &CueActivationLoop{
		h:                &handlers{deps: deps, clock: opts.Clock, logger: opts.Logger},
		interval:         opts.CueActivationLoopInterval,
		logger:           opts.Logger,
		inFlight:         make(chan struct{}, 1),
		nudge:            make(chan struct{}, 1),
		minNudgeInterval: opts.CueActivationNudgeMinInterval,
	}
}

// Nudge requests an out-of-band reconcile-and-decide pass as soon as
// [CueActivationLoop.Run]'s current (or next) tick returns, instead of
// waiting out its own interval — [assetsync.Service.Nudge]'s identical
// contract one seam over. Safe to call concurrently, at any time,
// including before Run starts (a no-op either way, since Run always ticks
// immediately on its own first iteration regardless). It requests
// promptness only: Run still independently reconciles, decides, and
// authorizes exactly as it would on its own scheduled tick — see this
// file's own doc comment for why that is not the same thing as ingestion
// granting execution authority.
func (l *CueActivationLoop) Nudge() {
	select {
	case l.nudge <- struct{}{}:
	default:
	}
}

// Run ticks on its own l.interval cadence, OR on a [CueActivationLoop.
// Nudge], until ctx is done, then waits for any in-flight tick to finish
// before returning — identical contract to the original ticker-only Run,
// with Nudge added as a second, immediate wake source (mirroring
// [assetsync.Service.Run]'s own interval-or-nudge select one seam over),
// so a fresh entry change reaches a tick without waiting out l.interval.
func (l *CueActivationLoop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			l.inFlight <- struct{}{}
			// The tick's own goroutine (runTick, above) is now done, so it
			// can no longer launch a new dispatchAssetMissingFailToBlack
			// goroutine (cueActivationTickOne's own go statement, below) —
			// l.h.cueActivationFailToBlackWG's own Add calls are all
			// already made, and Wait can only shrink from here. Waiting on
			// this SAME *handlers' WaitGroup (l.h is the one instance both
			// runTick and cueActivationTickOne share) is what gives that
			// goroutine a real owner: without it, Run could return, and the
			// store it still writes to could close, while it was still
			// dispatching a fail-to-black and appending its audit entry.
			l.h.cueActivationFailToBlackWG.Wait()
			return
		case <-ticker.C:
			l.runTick(ctx)
		case <-l.nudge:
			l.runNudgedTick(ctx)
		}
	}
}

// runNudgedTick is [CueActivationLoop.Run]'s nudge-driven case: it enforces
// l.minNudgeInterval between two nudge-driven ticks (defect 6 — see
// [Options.CueActivationNudgeMinInterval]'s own doc comment for why), and
// runs [CueActivationLoop.runTick] once that floor is satisfied. A nudge
// arriving before the floor is never dropped, only DEFERRED: it schedules
// a fresh [CueActivationLoop.Nudge] for the moment the floor is satisfied,
// so the evidence a fast-posting plugin's nudge represents is still
// reconciled, only at the floor's own bounded rate rather than instantly
// every time. This never delays the periodic ticker's own cadence
// (l.interval, Run's other select case) — only nudge-driven promptness is
// rate-limited here.
func (l *CueActivationLoop) runNudgedTick(ctx context.Context) {
	now := l.h.now()

	l.nudgeMu.Lock()
	elapsed := now.Sub(l.lastNudgeTick)
	if l.lastNudgeTick.IsZero() || elapsed >= l.minNudgeInterval {
		l.lastNudgeTick = now
		l.nudgeMu.Unlock()
		l.runTick(ctx)
		return
	}
	wait := l.minNudgeInterval - elapsed
	l.nudgeMu.Unlock()

	// Deferred, not dropped: re-request a nudge once the floor is
	// satisfied. A duplicate AfterFunc from several rapid nudges arriving
	// inside the SAME wait window costs nothing extra: l.nudge's own
	// 1-buffered coalescing (this struct's own doc comment) already
	// collapses any number of pending re-nudges into at most one.
	time.AfterFunc(wait, l.Nudge)
}

// runTick starts one tick as a non-blocking, skip-if-already-running
// attempt — the exact body Run's own select cases used to inline, factored
// out so the immediate first tick, the periodic tick, and a nudge-driven
// tick (via runNudgedTick, once its own floor is satisfied) all share it
// rather than three copies that could drift.
func (l *CueActivationLoop) runTick(ctx context.Context) {
	select {
	case l.inFlight <- struct{}{}:
		go func() {
			defer func() { <-l.inFlight }()
			pin, err := l.resolvePin(ctx)
			if err != nil {
				l.logger.Warn("cue activation loop: resolve show-mode pin failed; falling back to live resolution this tick", "error", err)
				pin = nil
			}
			l.h.cueActivationTick(ctx, l.h.now(), pin)
		}()
	default:
		// Previous tick still running; skip this one — the next tick
		// (periodic or nudged) will pick up whatever changed.
	}
}

// resolvePin is ADR-033 show mode's own pin lifecycle. It reads
// the CURRENT mode and active show fresh, every tick, and answers what
// this tick's Decide/Authorize calls must use.
//
//   - Program mode: any existing pin is dropped and nil is returned, so
//     the tick resolves live, unchanged from before pinning existed, and
//     "close to today's behaviour" per Eric's ruling.
//   - Show mode, no pin yet, or the active Show/Generation has changed
//     since the pin held was minted: a fresh [cueactivate.NewShowPin] is
//     started against the CURRENTLY active show, right now. This is "the
//     show starts" and "the show stops and restarts" both, in this
//     repository's own terms: ADR-027 already treats the active show as
//     configuration, and Generation is show.active's own config revision
//     number (assetsync.ActiveShow's own doc comment), the exact
//     mechanism this codebase uses to express "a new run of a show began".
//     A show.cue edit never touches show.active, so it can never look like
//     a restart here; only actually changing which show (or which
//     generation of it) is active does.
//   - Show mode, existing pin still matches the live Show/Generation: that
//     SAME pin is returned again, so a show.cue edit saved mid-show never
//     reaches this tick's Decide/Authorize calls at all.
func (l *CueActivationLoop) resolvePin(ctx context.Context) (*cueactivate.ShowPin, error) {
	if l.h.deps.Config == nil || l.h.deps.AssetManifests == nil {
		return nil, nil
	}
	mode, _, _, _, err := resolveShowMode(ctx, l.h.deps.Config)
	if err != nil {
		return nil, fmt.Errorf("resolve show mode: %w", err)
	}
	if mode.Mode != config.ShowModeShow {
		l.pinMu.Lock()
		l.pin = nil
		l.pinMu.Unlock()
		return nil, nil
	}

	active, err := assetsync.ResolveActiveShow(ctx, l.h.deps.AssetManifests)
	if err != nil {
		return nil, fmt.Errorf("resolve active show: %w", err)
	}

	l.pinMu.Lock()
	defer l.pinMu.Unlock()
	if l.pin == nil || l.pin.Active.ShowID != active.ShowID || l.pin.Active.Generation != active.Generation {
		l.pin = cueactivate.NewShowPin(active)
		l.pinnedAt = l.h.now()
	}
	return l.pin, nil
}

// cueActivationTick resolves and dispatches an activation for every FPP
// instance this coordinator has an accepted observation from.
func (h *handlers) cueActivationTick(ctx context.Context, now time.Time, pin *cueactivate.ShowPin) {
	if h.deps.FPPReconciliation == nil || h.deps.FPPObservations == nil {
		return
	}
	obsList, err := h.deps.FPPObservations.ListFPPPlaylistEntryObservations(ctx)
	if err != nil {
		h.logWarn("cue activation loop: failed to list fpp playlist entry observations", "error", err)
		return
	}
	for _, obs := range obsList {
		h.cueActivationTickOne(ctx, now, obs, pin)
	}
}

func (h *handlers) cueActivationTickOne(ctx context.Context, now time.Time, obs store.FPPPlaylistEntryObservationRecord, pin *cueactivate.ShowPin) {
	result, err := h.deps.FPPReconciliation.ReconcileFPPPlaylistEntryObservation(ctx, obs)
	if err != nil {
		h.logWarn("cue activation loop: reconcile failed", "instanceUuid", obs.InstanceUUID, "error", err)
		return
	}
	if h.deps.AssetManifests == nil {
		return
	}
	dec, err := cueactivate.Decide(ctx, h.deps.AssetManifests, result, obs, obs.InstanceUUID, pin)
	if err != nil {
		h.logWarn("cue activation loop: decide failed", "instanceUuid", obs.InstanceUUID, "error", err)
		return
	}

	issuer := cueActivationIssuer{PrincipalID: cueActivationSystemPrincipalID(obs.InstanceUUID)}

	switch dec.State {
	case cueactivate.StateActivated, cueactivate.StateMismatched:
		var outcomes []cueActivationDispatchOutcome
		if len(dec.Activations) > 0 {
			outcomes = h.dispatchCueActivations(ctx, now, dec.Activations, issuer, pin)
			for _, outcome := range outcomes {
				switch {
				case outcome.Err != nil:
					h.logWarn("cue activation loop: dispatch failed", "instanceUuid", obs.InstanceUUID, "nodeId", outcome.NodeID, "error", outcome.Err)
				case outcome.AuthorizeOutcome != "":
					h.logWarn("cue activation loop: this coordinator's own authorization refused before dispatch",
						"instanceUuid", obs.InstanceUUID, "nodeId", outcome.NodeID, "outcome", outcome.AuthorizeOutcome, "reason", outcome.AuthorizeReason)
				case outcome.Dispatched && !outcome.Confirmed:
					// The node itself refused it, or no confirmed result
					// ever arrived — recorded durably either way (see
					// writeCueActivationOutcomeAudit); this log line is
					// operator-visible evidence at the moment it happens,
					// not the only record of it.
					h.logWarn("cue activation loop: node did not confirm this activation",
						"instanceUuid", obs.InstanceUUID, "nodeId", outcome.NodeID, "nodeOutcome", outcome.NodeOutcome)
				}
			}
		}
		if len(dec.ClearNodes) > 0 {
			h.dispatchBlackAndSilence(ctx, now, dec.ClearNodes, issuer, blackAndSilenceEpisode(obs))
		}
		// A genuinely missing asset must never leave a node showing WRONG
		// content while an operator is not looking at it — but it must
		// also never black anything the refused Cue does not itself
		// declare (Eric's ruling: "it should not fail for the entire
		// show, just that single bad cue"). assetMissingFailToBlackTargets
		// collects BOTH kinds of asset-missing refusal this seam can see
		// (this coordinator's own pre-dispatch refusal, and the node's
		// own post-dispatch refusal — see that function's own doc
		// comment for why both must reach this path), each carrying the
		// refused Cue's own resolved outputs so the dispatch below can
		// scope to exactly those.
		//
		// Dispatched in its own goroutine, detached from this tick's own
		// return: [dispatchCueScopedBlackAndSilence] awaits real node
		// confirmation per surface/session (renderCommandConfirmDeadline/
		// audioCommandConfirmDeadline, 15s each), and this method is
		// called once per FPP instance from cueActivationTick's own
		// sequential loop over EVERY instance — a node that never
		// confirms (the exact case an asset-missing refusal often is)
		// would otherwise stall every OTHER instance's tick behind a
		// repeated multi-second wait, paid again at every new entry-start
		// on the bad node. ctx here is [CueActivationLoop.Run]'s own
		// long-lived context (this tick's caller), not a per-request one,
		// so it outlives this method's own return exactly the way the
		// outer per-tick goroutine (runTick) already does one level up.
		//
		// h.cueActivationFailToBlackWG is this goroutine's real owner:
		// Add happens here, on the CALLER's own goroutine, strictly
		// before the go statement starts the new one, so
		// [CueActivationLoop.Run]'s own ctx.Done() Wait can never
		// observe a zero counter while a dispatch is still about to
		// start. Without an owner, this goroutine could still be
		// dispatching, and appending its audit entry, after Run had
		// already returned and the coordinator's shutdown path had
		// closed the store out from under it.
		if targets := assetMissingFailToBlackTargets(outcomes); len(targets) > 0 {
			episode := blackAndSilenceEpisode(obs)
			h.cueActivationFailToBlackWG.Add(1)
			go func() {
				defer h.cueActivationFailToBlackWG.Done()
				h.dispatchAssetMissingFailToBlack(ctx, now, obs.InstanceUUID, targets, issuer, episode)
			}()
		}
	case cueactivate.StateEvidenceBroken:
		// Owner ruling 2026-09-02 (cue-deactivate-on-jump): a persisted
		// sequence-regression marker outranks whatever else this tick would
		// otherwise have decided (StateEvidenceBroken's own doc comment,
		// cueactivate/decide.go) — stop exactly what dec.EvidenceBroken
		// names, the same per-cue-scoped effect the asset-missing
		// fail-to-black path already uses, and touch nothing else: never
		// the background bed, never the prepare-staging session.
		if len(dec.EvidenceBroken) == 0 {
			return
		}
		targets, err := h.evidenceBrokenFailToBlackTargets(ctx, dec.EvidenceBroken)
		if err != nil {
			h.logWarn("cue activation loop: resolve evidence-broken fail-to-black targets failed", "instanceUuid", obs.InstanceUUID, "error", err)
			return
		}
		if len(targets) == 0 {
			return
		}
		// Detached, in its own goroutine, for the identical reason
		// dispatchAssetMissingFailToBlack is (that function's own doc
		// comment, immediately above): dispatchCueScopedBlackAndSilence
		// awaits real per-surface/session node confirmation, and this
		// method runs once per FPP instance from cueActivationTick's own
		// sequential loop over every instance. h.cueActivationFailToBlackWG
		// is reused rather than a second WaitGroup: it already exists to
		// own exactly this shape of detached scoped-clear dispatch, and
		// [CueActivationLoop.Run]'s own shutdown path already waits on it.
		episode := evidenceBrokenEpisode(obs)
		h.cueActivationFailToBlackWG.Add(1)
		go func() {
			defer h.cueActivationFailToBlackWG.Done()
			h.dispatchCueScopedBlackAndSilence(ctx, now, targets, issuer, episode)
		}()
	case cueactivate.StateUnbound, cueactivate.StateIdentityUnavailable:
		// Nothing to dispatch or hold — see [cueactivate.State]'s own doc
		// comment.
	}
}

// evidenceBrokenFailToBlackTargets resolves evidenceBroken's own per-node
// Activations (cueactivate.Decision.EvidenceBroken) into
// cueScopedFailToBlackTarget{NodeID, Outputs} pairs, mirroring
// dispatchPrepareAheadAudio's own reasoning for reusing act.Show/
// act.Generation directly rather than re-resolving the active show live a
// second time: cueactivate.Decide already resolved them, live, at decide
// time. A node whose Cue no longer resolves in that catalog — the active
// show changed, or the Cue itself was deleted, between when the now-broken
// evidence was last resolved and this tick — is skipped: there is nothing
// left to scope a stop to for that node.
func (h *handlers) evidenceBrokenFailToBlackTargets(ctx context.Context, evidenceBroken map[string]cueactivation.Activation) ([]cueScopedFailToBlackTarget, error) {
	if h.deps.AssetManifests == nil || len(evidenceBroken) == 0 {
		return nil, nil
	}
	var out []cueScopedFailToBlackTarget
	for nodeID, act := range evidenceBroken {
		active := assetsync.ActiveShow{Configured: true, ShowID: act.Show, Generation: act.Generation}
		catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
		if err != nil {
			return nil, fmt.Errorf("resolve cue catalog for node %q: %w", nodeID, err)
		}
		entry, participates := prepareAheadCatalogEntry(catalog, act.CueID)
		if !participates {
			continue
		}
		out = append(out, cueScopedFailToBlackTarget{NodeID: nodeID, Outputs: entry.Outputs})
	}
	return out, nil
}

// evidenceBrokenEpisode is StateEvidenceBroken's own idempotency-key
// dimension, mirroring blackAndSilenceEpisode's identical role for the H0.2
// mismatch effect: obs.InstanceUUID and obs.EvidenceBrokenAt, never
// obs.EntryOccurrenceSequence, which is frozen at whatever it was before
// evidence broke and does not advance again until a fresh accepted
// observation clears the marker (schemaV29's own doc comment). Stable
// across repeat ticks of the SAME unresolved break, so a repeat tick
// replays idempotently rather than re-dispatching; changes the moment the
// marker clears and later sets again, so a later, distinct break gets a
// fresh episode.
func evidenceBrokenEpisode(obs store.FPPPlaylistEntryObservationRecord) string {
	brokenAt := ""
	if obs.EvidenceBrokenAt != nil {
		brokenAt = obs.EvidenceBrokenAt.UTC().Format(time.RFC3339Nano)
	}
	return obs.InstanceUUID + "-" + brokenAt
}

// cueScopedFailToBlackTarget is one node this tick must fail to black,
// scoped to exactly the outputs the refused Cue itself declared for it —
// never the node's every surface and every audio session, which is
// [dispatchBlackAndSilence]'s own broader H0.2 mismatch effect (still
// correct for THAT case: an entire Playlist binding mismatch is not "one
// bad cue").
type cueScopedFailToBlackTarget struct {
	NodeID  string
	Outputs cuecatalog.Outputs
}

// assetMissingFailToBlackTargets collects the fail-to-black target set
// from outcomes: every node refused asset-missing, from EITHER of the two
// places that refusal can come from —
//
//  1. AuthorizeOutcome == [cueauth.OutcomeAssetMissing]: THIS
//     coordinator's own pre-dispatch [cueactivate.Authorize] refused
//     before anything ever reached the wire.
//  2. NodeOutcome == string([cueauth.OutcomeAssetMissing]): the ACTIVATION
//     was dispatched (this coordinator's own Authorize passed), but the
//     node's own post-dispatch [cueauth.CheckLazy] refused it against
//     its own, independently-observed asset inventory. Before this fix,
//     this case reached no fail-to-black path at all: a node-side
//     asset-missing refusal only produced a log line
//     (outcome.Dispatched && !outcome.Confirmed, cueActivationTickOne's
//     own switch above), identical to a bare unconfirmed dispatch. This
//     matters even now that [cueactivate.cueAssetsPresent] itself refuses
//     a never-uploaded sequence (so the ordinary case reaches path 1,
//     pre-dispatch): this coordinator's own view of a node's inventory can
//     still be stale or wrong relative to what the node just observed on
//     disk, and path 2 is what catches THAT residual disagreement rather
//     than depending on the coordinator having noticed first.
//
// Every other outcome (successfully confirmed, refused for an unrelated
// reason, or no result at all) is excluded — fail-to-black has no bearing
// on any of them.
func assetMissingFailToBlackTargets(outcomes []cueActivationDispatchOutcome) []cueScopedFailToBlackTarget {
	var out []cueScopedFailToBlackTarget
	for _, o := range outcomes {
		if o.AuthorizeOutcome == cueauth.OutcomeAssetMissing || o.NodeOutcome == string(cueauth.OutcomeAssetMissing) {
			out = append(out, cueScopedFailToBlackTarget{NodeID: o.NodeID, Outputs: o.RefusedCueOutputs})
		}
	}
	return out
}

// assetMissingFailToBlack reports whether the fail-to-black effect should
// fire for targets under mode: true only in show mode
// (config.ShowModeShow) and only when there is at least one target to
// black. In setup/program mode the refusal stays loud — the existing
// per-node log line and audit entry — rather than disappearing to black,
// per the owner's own ruling: errors can be caught in programming mode
// since it is designed to be used while the operator is looking at the
// show, so a refusal there must stay visible rather than fail silently to
// black.
func assetMissingFailToBlack(mode string, targets []cueScopedFailToBlackTarget) bool {
	return mode == config.ShowModeShow && len(targets) > 0
}

// dispatchAssetMissingFailToBlack resolves show mode and, only in show
// mode, dispatches [dispatchCueScopedBlackAndSilence] for targets — split
// out from cueActivationTickOne so it can run in its own goroutine (see
// that method's own doc comment for why: this is the call that awaits
// real per-surface/per-session node confirmation and must not stall the
// sequential tick loop over every OTHER FPP instance).
func (h *handlers) dispatchAssetMissingFailToBlack(ctx context.Context, now time.Time, instanceUUID string, targets []cueScopedFailToBlackTarget, issuer cueActivationIssuer, episode string) {
	if h.deps.Config == nil {
		return
	}
	mode, _, _, _, merr := resolveShowMode(ctx, h.deps.Config)
	if merr != nil {
		h.logWarn("cue activation loop: resolve show mode for asset-missing fail-to-black failed", "instanceUuid", instanceUUID, "error", merr)
		return
	}
	if !assetMissingFailToBlack(mode.Mode, targets) {
		return
	}
	h.dispatchCueScopedBlackAndSilence(ctx, now, targets, issuer, episode)
}

// cueActivationSystemPrincipalID attributes an autonomous tick's own
// dispatch to a stable, clearly-labeled identity rather than an operator
// principal that never made this request — mirrors nightMarkAttribution
// Degraded's own posture of making an autonomous dispatch's attribution
// gap VISIBLE rather than hiding it behind a normal-looking principal id.
func cueActivationSystemPrincipalID(instanceUUID string) string {
	return "system:cue-activation-loop:" + instanceUUID
}

// blackAndSilenceAudioSessionID is the audio session H0.2's blackAndSilence
// policy stops. It comes from pkg/cueactivation, which both this package and
// the node already import: the node creates the session under that id and
// this dispatches audio.session.stop against it, so a second declared copy
// would compile, drift, and leave the policy stopping a session that does
// not exist.
const blackAndSilenceAudioSessionID = cueactivation.AudioSessionID

// blackAndSilenceAudioSessionIDs is every audio session H0.2's
// blackAndSilence policy stops (TRACK-H-cues-and-playlists.md section H5
// build item 4's own fix): H5 created two more well-known sessions after
// H0.2 was written — [cueactivation.BackgroundSessionID] (a ShowMesh bed)
// and [cueactivation.AnnouncementSessionID] (an in-flight announcement) —
// and blackAndSilence used to stop only [blackAndSilenceAudioSessionID],
// leaving both of H5's own sessions completely outside every silence path:
// "the renderer blacks its surfaces and ShowMesh-owned audio silences" (H0.2)
// was never true for either one. H5 created these sessions, so extending
// this policy to reach them is H5's own debt, even though the show-switch
// transition this policy also participates in is broader than H5.
var blackAndSilenceAudioSessionIDs = []string{
	blackAndSilenceAudioSessionID,
	cueactivation.BackgroundSessionID,
	cueactivation.AnnouncementSessionID,
}

// blackAndSilenceEpisode is defect 3's own episode dimension for the
// blackAndSilence clear/silence idempotency keys: obs.InstanceUUID and its
// EntryOccurrenceSequence (schemaV18's own entry-start identity, computed
// at ingestion — see fppobservations.go and [cueactivate.activationID]'s
// identical use one seam over). idempotency_key is globally unique on the
// commands table (store.InsertCommand), and both
// [handlers.resolveRenderCommandReplay]/[resolveAudioSessionReplay] answer
// a reused key with the FIRST command's already-resolved outcome forever,
// never dispatching again — so a key scoped only to node/surface (the
// render clear) or only to node (the audio stop) means the SECOND
// mismatch episode on the same node ever, no matter how much later or how
// unrelated its trigger, silently replays the first episode's outcome
// instead of dispatching. Stable across repeat ticks of one CONTINUING
// mismatch (the coordinator's own store keeps only the latest accepted
// observation per instance, so an unchanged mismatch re-reads the
// identical obs every tick) and changes on any genuinely new FPP
// entry-start evidence — including the mismatch clearing and later
// recurring, which always advances EntryOccurrenceSequence again first.
func blackAndSilenceEpisode(obs store.FPPPlaylistEntryObservationRecord) string {
	return obs.InstanceUUID + "-" + strconv.FormatInt(obs.EntryOccurrenceSequence, 10)
}

// dispatchBlackAndSilence dispatches render.surface.clear to every surface
// belonging to a node in nodeIDs, and audio.session.stop against
// [blackAndSilenceAudioSessionID] on every one of those nodes that has
// declared an audio.node object — H0.2's full blackAndSilence effect
// ("the renderer blacks its surfaces and ShowMesh-owned audio silences"),
// not only its render half. A node with no audio.node object has no
// program-audio route to silence at all (ADR-018), so it is skipped
// rather than dispatched-and-refused every tick. episode is
// [blackAndSilenceEpisode]'s own value for the observation that produced
// this mismatch — see that function's own doc comment for why the
// idempotency keys below carry it.
func (h *handlers) dispatchBlackAndSilence(ctx context.Context, now time.Time, nodeIDs []string, issuer cueActivationIssuer, episode string) {
	if h.deps.Config == nil {
		return
	}
	for _, nodeID := range nodeIDs {
		h.dispatchBlackAndSilenceClearSurfaces(ctx, now, nodeID, issuer, episode)
		h.dispatchBlackAndSilenceAudioStop(ctx, now, nodeID, issuer, episode)
	}
}

// dispatchBlackAndSilenceClearSurfaces dispatches render.surface.clear to
// every show.surface belonging to nodeID (H0.2's render half of the
// blackAndSilence effect) — split out of [dispatchBlackAndSilence] so
// [dispatchCueScopedBlackAndSilence] can reuse the identical clear logic
// (idempotency key shape, log lines) for a scoped target that must clear
// render surfaces WITHOUT also stopping every audio session on the node,
// which dispatchBlackAndSilence's own node-wide effect always does.
func (h *handlers) dispatchBlackAndSilenceClearSurfaces(ctx context.Context, now time.Time, nodeID string, issuer cueActivationIssuer, episode string) {
	surfaceIDs, err := surfaceIDsForNodeAnyShow(ctx, h.deps.Config, nodeID)
	if err != nil {
		h.logWarn("cue activation loop: resolve surfaces for blackAndSilence failed", "nodeId", nodeID, "error", err)
		return
	}
	for _, surfaceID := range surfaceIDs {
		in := renderDispatchInput{
			Action: "render.surface.clear", NodeID: nodeID, SurfaceID: surfaceID,
			IdempotencyKey: "cueact-clear-" + nodeID + "-" + surfaceID + "-" + episode,
			DesiredState:   "stopped",
			IssuerID:       issuer.PrincipalID, IssuerName: issuer.PrincipalName,
			Form: issuer.Form, CredentialID: issuer.CredentialID,
		}
		result, problem, err := h.executeRenderDispatch(ctx, now, in)
		switch {
		case err != nil:
			h.logWarn("cue activation loop: blackAndSilence clear dispatch failed", "nodeId", nodeID, "surfaceId", surfaceID, "episode", episode, "error", err)
		case problem != nil:
			h.logWarn("cue activation loop: blackAndSilence clear dispatch refused", "nodeId", nodeID, "surfaceId", surfaceID, "episode", episode, "detail", problem.Detail)
		case result.Replay:
			// Not a failure: this episode's clear was already
			// dispatched on an earlier tick and this is a repeat
			// tick of the SAME episode — logged so a suppressed
			// dispatch is visible evidence, never a silent no-op
			// (TRACK-H-H3-SPEC.md section 6).
			h.logDebug("cue activation loop: blackAndSilence clear suppressed as a replay of an unchanged episode", "nodeId", nodeID, "surfaceId", surfaceID, "episode", episode)
		}
	}
}

// dispatchCueScopedBlackAndSilence fails targets to black, each scoped to
// exactly the outputs its own refused Cue declared for it (Eric's ruling:
// "it should not fail for the entire show, just that single bad cue") —
// never [dispatchBlackAndSilence]'s node-wide clear-every-surface-and-
// stop-every-session effect, which is correct for an H0.2 Playlist-binding
// mismatch (the WHOLE binding is wrong) but was wrong here: an audio-only
// Cue's refusal must never clear a render surface that Cue never touches,
// and a render-only Cue's refusal must never stop the background bed or
// an in-flight announcement, neither of which that Cue's own outputs
// declare.
//
//   - target.Outputs.Render != nil: clear every one of the node's own
//     show.surface objects (render is scoped by surface ASSIGNMENT, not
//     further per-Cue — [assetsync.ResolveCueCatalog]'s own doc comment —
//     so "the surfaces this Cue declares" is exactly the node's own
//     assigned surfaces).
//   - target.Outputs.Audio != nil: stop ONLY [cueactivation.AudioSessionID],
//     the one session an ordinary Cue's audio output ever runs in — never
//     [cueactivation.BackgroundSessionID] (the showmesh-audio runner's own
//     background Playlist, which no Cue's own outputs ever name) and
//     never [cueactivation.AnnouncementSessionID] unless this SAME Cue
//     also declares its own announcement output (handled by the very next
//     case, not this one).
//   - target.Outputs.Announcement != nil: stop ONLY
//     [cueactivation.AnnouncementSessionID].
//   - target.Outputs.LTC declares no asset and no session of its own — it
//     rides on the audio output's own session ([cuecatalog.LTCOutput]'s
//     own doc comment) — so the Audio case above already covers it; LTC
//     alone with no Audio output cannot occur (config.ShowCuePayload's own
//     authoring-time "outputs.ltc requires outputs.audio" rule).
func (h *handlers) dispatchCueScopedBlackAndSilence(ctx context.Context, now time.Time, targets []cueScopedFailToBlackTarget, issuer cueActivationIssuer, episode string) {
	if h.deps.Config == nil {
		return
	}
	for _, target := range targets {
		if target.Outputs.Render != nil {
			h.dispatchBlackAndSilenceClearSurfaces(ctx, now, target.NodeID, issuer, episode)
		}
		if target.Outputs.Audio != nil {
			h.dispatchBlackAndSilenceAudioStopSession(ctx, now, target.NodeID, cueactivation.AudioSessionID, issuer, episode)
		}
		if target.Outputs.Announcement != nil {
			h.dispatchBlackAndSilenceAudioStopSession(ctx, now, target.NodeID, cueactivation.AnnouncementSessionID, issuer, episode)
		}
	}
}

// dispatchBlackAndSilenceAudioStop stops every session in
// [blackAndSilenceAudioSessionIDs] on nodeID — this seam's own audio half
// of H0.2's blackAndSilence policy — if and only if nodeID has declared an
// audio.node object at all. A dispatch failure or refusal is logged, never
// silently swallowed (TRACK-H-H3-SPEC.md section 6's "a refusal is a state
// with evidence, never a silent no-op" applied here to a policy effect
// rather than an authorization check): an operator who chose
// blackAndSilence specifically to avoid the wrong content reaching an
// audience must be able to see that the silence attempt itself failed, not
// just infer it from continued audio on the wall. episode is
// [blackAndSilenceEpisode]'s own value; see that function's own doc
// comment for why the idempotency key carries it.
func (h *handlers) dispatchBlackAndSilenceAudioStop(ctx context.Context, now time.Time, nodeID string, issuer cueActivationIssuer, episode string) {
	hasAudioNode, err := nodeHasAudioNodeObject(ctx, h.deps.Config, nodeID)
	if err != nil {
		h.logWarn("cue activation loop: resolve audio.node for blackAndSilence failed", "nodeId", nodeID, "error", err)
		return
	}
	if !hasAudioNode {
		return
	}
	for _, sessionID := range blackAndSilenceAudioSessionIDs {
		h.dispatchBlackAndSilenceAudioStopSession(ctx, now, nodeID, sessionID, issuer, episode)
	}
}

// dispatchBlackAndSilenceAudioStopSession is
// [dispatchBlackAndSilenceAudioStop] narrowed to one sessionID — split out
// so the same stop logic (idempotency key, revision derivation, dispatch,
// and every log line) applies identically to all three of
// [blackAndSilenceAudioSessionIDs] rather than being written once for the
// show session and copied by hand for the other two.
func (h *handlers) dispatchBlackAndSilenceAudioStopSession(ctx context.Context, now time.Time, nodeID, sessionID string, issuer cueActivationIssuer, episode string) {
	idempotencyKey := "cueact-silence-" + nodeID + "-" + sessionID + "-" + episode
	// stopAt is the LATER of this coordinator's own now and the EvidenceAt
	// of the last cue.activate this coordinator itself dispatched to
	// nodeID — never a bare now, because now is THIS coordinator's clock
	// while the node's own activateAudio steps derive their revisions from
	// act.EvidenceAt, a reading taken on the FPP player (see
	// cueactivation.AudioSessionRevision's own doc comment). A Raspberry Pi
	// FPP player with no RTC and no internet can boot with a clock badly
	// ahead of this coordinator's; if this stop used now alone in that
	// case, its revision would be smaller than the session's own current
	// revision and pkg/audio's RevisionState.Apply would refuse it as
	// stale, leaving that node's audio session unstoppable for the rest of
	// its life. Reading the last-dispatched activation back out of the
	// commands table (rather than trusting anything the node itself
	// reports) keeps this derivation entirely on evidence this coordinator
	// already recorded.
	stopAt := now
	if h.deps.Commands != nil {
		last, err := h.deps.Commands.GetLatestCommandByTargetAction(ctx, "node", nodeID, "cue.activate")
		switch {
		case err == nil:
			var act cueactivation.Activation
			if uerr := json.Unmarshal([]byte(last.ParamsJSON), &act); uerr != nil {
				h.logWarn("cue activation loop: decode last dispatched activation for blackAndSilence stop failed", "nodeId", nodeID, "error", uerr)
			} else if act.EvidenceAt.After(stopAt) {
				stopAt = act.EvidenceAt
			}
		case errors.Is(err, store.ErrCommandNotFound):
			// No cue.activate was ever dispatched to this node: nothing to
			// compare against, so now is the correct (and only available)
			// reading.
		default:
			h.logWarn("cue activation loop: read last dispatched activation for blackAndSilence stop failed", "nodeId", nodeID, "error", err)
		}
	}
	// AudioSessionRevision, not a bare stopAt.UnixNano(): the node's own
	// activateAudio steps derive their revisions through the identical
	// function (internal/agent/cueactivationaudio.go's activationRevision),
	// and AudioSessionStepStop is defined to sort after every one of them
	// — see cueactivation.AudioSessionRevision's own doc comment for why a
	// second, independently written revision rule left this stop refused
	// as stale for the life of the session.
	revision := cueactivation.AudioSessionRevision(stopAt, cueactivation.AudioSessionStepStop)
	in := AudioDispatchInput{
		Action: "audio.session.stop", NodeID: nodeID, SessionID: sessionID,
		Params: map[string]any{
			"sessionId": sessionID, "invocationId": idempotencyKey, "revision": revision,
		},
		Revision: revision, IdempotencyKey: idempotencyKey,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
	}
	result, problem, err := h.executeAudioSessionDispatch(ctx, now, in)
	switch {
	case err != nil:
		h.logWarn("cue activation loop: blackAndSilence audio stop dispatch failed", "nodeId", nodeID, "sessionId", sessionID, "episode", episode, "error", err)
	case problem != nil:
		h.logWarn("cue activation loop: blackAndSilence audio stop dispatch refused", "nodeId", nodeID, "sessionId", sessionID, "episode", episode, "detail", problem.Detail)
	case result.Replay:
		// Not a failure: see the identical case in dispatchBlackAndSilence
		// above — a suppressed dispatch is logged, never silent.
		h.logDebug("cue activation loop: blackAndSilence audio stop suppressed as a replay of an unchanged episode", "nodeId", nodeID, "sessionId", sessionID, "episode", episode)
	}
}

// nextPlaylistEntryCueID looks up the entry immediately after entryID in
// playlistID's own entries, at playlistRevision — the ordered knowledge a
// node's own flat, unordered catalog cannot derive itself (see
// [cueactivation.PrepareStagingSessionID]'s own doc comment for why the
// coordinator, not the node, is what schedules a prepare-ahead). ok is
// false, with no error, whenever there is genuinely nothing to prepare
// ahead: playlistID or entryID is empty (a directly-activated
// announcement, or a safeCue mismatch fallback — neither advances through
// an ordered Playlist), the named revision no longer exists, entryID is
// not one of its entries, or entryID is that revision's own last entry.
//
// The last-entry case is a KNOWN, deliberate gap, not an oversight: an FPP
// playlist that loops (its last entry restarting its first, rather than
// ending) is still a real cue start with the same video-leads-audio lead
// this whole mechanism exists to close, but this coordinator has nothing
// to prepare ahead with at that transition, because it cannot currently
// tell whether a given FPP-runner show.playlist loops at all.
// config.ShowPlaylistPayload's own Repeat field (ShowPlaylistShowmeshAudio)
// is gated to Runner=="showmesh-audio" and does not exist for an
// FPP-runner binding; FPP's own raw playlist definition JSON DOES carry a
// "repeat" key (confirmed against real FPP fixtures), but
// fppidentity.ParseDefinitionEntries — the coordinator's one reader of
// that JSON — does not extract it, and nothing else in this codebase
// surfaces it. Wiring that through would be new plumbing in a different
// package, not a reversible detail of this function; guessing wrong here
// (wrapping to the first entry when the playlist does not actually loop)
// would stage a Cue that never activates, so skipping is the correct,
// evidence-bounded choice until that data is actually available here.
//
// Reads the persisted revision directly, mirroring fppreconcile's own
// decodeStoredShowPlaylistPayload one seam over (internal/coordinator/
// fppreconcile/reconcile.go): a stored revision is already valid, and
// re-running config.DecodeShowPlaylistPayload's own authoring-time
// cross-reference validation (show exists, cue resolves) here would need
// callbacks this read-only lookup has no use for.
func nextPlaylistEntryCueID(ctx context.Context, cfg ConfigStore, playlistID string, playlistRevision int64, entryID string) (cueID string, ok bool, err error) {
	if playlistID == "" || entryID == "" {
		return "", false, nil
	}
	rev, err := cfg.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, playlistID, playlistRevision)
	if err != nil {
		if errors.Is(err, store.ErrConfigRevisionNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get show.playlist %q revision %d: %w", playlistID, playlistRevision, err)
	}
	var payload config.ShowPlaylistPayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return "", false, fmt.Errorf("decode show.playlist %q revision %d: %w", playlistID, playlistRevision, err)
	}
	for i, entry := range payload.Entries {
		if entry.ID != entryID {
			continue
		}
		if i+1 >= len(payload.Entries) {
			return "", false, nil
		}
		return payload.Entries[i+1].Cue, true, nil
	}
	return "", false, nil
}

// prepareAheadCatalogEntry mirrors cueactivate's own unexported
// catalogEntry exactly — independently reproduced rather than imported,
// matching this file's own established convention (see
// activeShowFPPBinding's doc comment one package over, cueactivate/
// decide.go) for a small, self-contained lookup neither side's own
// internals need to share.
func prepareAheadCatalogEntry(catalog assetsync.Catalog, cueID string) (cuecatalog.Entry, bool) {
	for _, e := range catalog.Entries {
		if e.CueID == cueID {
			return e, true
		}
	}
	return cuecatalog.Entry{}, false
}

// dispatchPrepareAheadAudio best-effort stages cue N+1's audio content
// under [cueactivation.PrepareStagingSessionID] while cue N (act) is still
// activating on nodeID — the coordinator's own half of closing the
// video-leads-audio gap [audio.Manager.Promote] exists for (see that
// method's own doc comment, internal/agent/audio/manager.go, for the
// node-side mechanism this feeds).
//
// Deliberately NOT an authorization decision: it never calls
// [cueauth.Check] or [cueactivate.Authorize] against the guessed next Cue.
// [audio.Manager.Promote]'s own identity check, run at cue N+1's REAL
// activation, is what actually gates whether this staged content is ever
// used — a wrong or stale guess here costs nothing beyond one wasted
// prepare; activateAudio's ordinary Apply+Prepare+Start fallback still
// runs exactly as it would have if nothing had ever been staged. Every
// failure here is logged and swallowed for the identical reason: this must
// never fail, delay, or retry cue N's own activation, which has already
// been dispatched (or refused) by the time this runs.
//
// Runs synchronously, not in its own goroutine, unlike
// dispatchAssetMissingFailToBlack: that goroutine exists because an
// asset-missing refusal recurs on EVERY tick for as long as the bad node/
// cue stays active, which would otherwise stall every other FPP
// instance's tick repeatedly. This dispatch's idempotency key is
// act.ActivationID-derived, the identical key scope cue N's own
// dispatchOneCueActivation already uses — so a repeat tick over an
// unchanged act (the ordinary case between one entry-start and the next)
// answers from the replay path with no publish and no await, exactly as
// dispatchOneCueActivation's own repeat-tick cost already does today,
// unguarded by a goroutine.
func (h *handlers) dispatchPrepareAheadAudio(ctx context.Context, now time.Time, nodeID string, act cueactivation.Activation, issuer cueActivationIssuer) {
	if h.deps.Config == nil || h.deps.AssetManifests == nil {
		return
	}
	nextCueID, ok, err := nextPlaylistEntryCueID(ctx, h.deps.Config, act.Playlist, act.PlaylistRevision, act.EntryID)
	if err != nil {
		h.logWarn("cue activation loop: resolve next playlist entry for prepare-ahead failed", "nodeId", nodeID, "playlistId", act.Playlist, "error", err)
		return
	}
	if !ok {
		return
	}

	// act.Show/act.Generation are already this activation's own pinned or
	// freshly-resolved identity (cueactivate.Decide's own job) — reused
	// directly rather than re-resolving assetsync.ResolveActiveShow (or
	// threading the tick's own *cueactivate.ShowPin through another layer)
	// a second time for the identical answer.
	active := assetsync.ActiveShow{Configured: true, ShowID: act.Show, Generation: act.Generation}
	catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
	if err != nil {
		h.logWarn("cue activation loop: resolve cue catalog for prepare-ahead failed", "nodeId", nodeID, "cueId", nextCueID, "error", err)
		return
	}
	entry, participates := prepareAheadCatalogEntry(catalog, nextCueID)
	if !participates || entry.Outputs.Audio == nil || entry.Outputs.Audio.Filename == "" {
		// Nothing to stage: the next Cue has no audio output on this node,
		// or — cueAssetsPresent's own "never uploaded" rule (cueactivate/
		// decide.go), mirrored here — no asset has ever been uploaded for
		// it. Not an error: the ordinary Apply+Prepare+Start path at real
		// activation time covers this exactly as it always has.
		return
	}
	contentHash := ""
	if len(entry.Outputs.Audio.AssetHashes) > 0 {
		contentHash = entry.Outputs.Audio.AssetHashes[0]
	}

	// t is act.EvidenceAt, not this coordinator's own now: the apply and
	// prepare idempotency keys below are stable for act's whole lifetime,
	// so their params must be byte-identical on every repeat tick too, and
	// only a value fixed to act itself (never a fresh wall-clock reading)
	// gives that. [PrepareStagingSessionStepApply]/[PrepareStagingSessionStepPrepare]
	// both sort past every AudioSessionStep* constant, so the resulting
	// revisions still land above activationRevision(act, activationStepStart)
	// — the revision the node's own activateAudio already consumed against
	// THIS SAME staging session (Promote or Clear) before this runs.
	t := act.EvidenceAt

	staging := cueactivation.PrepareStagingSessionID
	applyInvocation := act.ActivationID + ":prepare-ahead-apply"
	prepareInvocation := act.ActivationID + ":prepare-ahead-prepare"
	applyRevision := cueactivation.PrepareStagingSessionRevision(t, cueactivation.PrepareStagingSessionStepApply)
	prepareRevision := cueactivation.PrepareStagingSessionRevision(t, cueactivation.PrepareStagingSessionStepPrepare)

	applyResult, applyProblem, err := h.executeAudioSessionDispatch(ctx, now, AudioDispatchInput{
		Action: "audio.session.apply", NodeID: nodeID, SessionID: staging,
		Params: map[string]any{
			"sessionId": staging, "invocationId": applyInvocation, "revision": applyRevision,
			"sourceRole": string(pkgaudio.SourceRoleShow),
			"media": map[string]any{
				"assetId": entry.Outputs.Audio.Asset, "contentHash": contentHash, "filename": entry.Outputs.Audio.Filename,
			},
		},
		Revision: applyRevision, IdempotencyKey: applyInvocation,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
	})
	switch {
	case err != nil:
		h.logWarn("cue activation loop: prepare-ahead audio apply dispatch failed", "nodeId", nodeID, "cueId", nextCueID, "error", err)
		return
	case applyProblem != nil:
		h.logWarn("cue activation loop: prepare-ahead audio apply dispatch refused", "nodeId", nodeID, "cueId", nextCueID, "detail", applyProblem.Detail)
		return
	case applyResult.Outcome == "refused" || applyResult.Outcome == "failed":
		h.logWarn("cue activation loop: prepare-ahead audio apply outcome", "nodeId", nodeID, "cueId", nextCueID, "outcome", applyResult.Outcome, "reason", applyResult.Reason)
		return
	}

	_, prepareProblem, err := h.executeAudioSessionDispatch(ctx, now, AudioDispatchInput{
		Action: "audio.session.prepare", NodeID: nodeID, SessionID: staging,
		Params:   map[string]any{"sessionId": staging, "invocationId": prepareInvocation, "revision": prepareRevision},
		Revision: prepareRevision, IdempotencyKey: prepareInvocation,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
	})
	switch {
	case err != nil:
		h.logWarn("cue activation loop: prepare-ahead audio prepare dispatch failed", "nodeId", nodeID, "cueId", nextCueID, "error", err)
	case prepareProblem != nil:
		h.logWarn("cue activation loop: prepare-ahead audio prepare dispatch refused", "nodeId", nodeID, "cueId", nextCueID, "detail", prepareProblem.Detail)
	}
}

// safeDispatchPrepareAheadAudio wraps [handlers.dispatchPrepareAheadAudio]
// with a recover, so best-effort means genuinely total: this runs on
// runTick's own detached goroutine (this file's own Run), with no caller
// left to recover a panic before it reaches the Go runtime and takes down
// the entire process — cue N's own activation, dispatched by this same
// caller (cueactivationdispatch.go's dispatchCueActivations) immediately
// before this call, must never be put at that kind of risk by a wrong or
// unanticipated guess about cue N+1.
func (h *handlers) safeDispatchPrepareAheadAudio(ctx context.Context, now time.Time, nodeID string, act cueactivation.Activation, issuer cueActivationIssuer) {
	defer func() {
		if r := recover(); r != nil {
			h.logWarn("cue activation loop: prepare-ahead audio dispatch panicked; recovered", "nodeId", nodeID, "cueId", act.CueID, "panic", fmt.Sprintf("%v", r))
		}
	}()
	h.dispatchPrepareAheadAudio(ctx, now, nodeID, act, issuer)
}

// nodeHasAudioNodeObject reports whether nodeID has a current audio.node
// object — mirrors internal/coordinator/assetsync/cuecatalog.go's own
// hasAudioNode exactly (that function is unexported in a different
// package, so this is independently reproduced, not shared, matching this
// file's own established convention one const above).
func nodeHasAudioNodeObject(ctx context.Context, cfg ConfigStore, nodeID string) (bool, error) {
	if cfg == nil {
		return false, nil
	}
	_, err := cfg.GetConfigObject(ctx, config.AudioNodeConfigKind, nodeID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// surfaceIDsForNodeAnyShow lists every show.surface object id assigned to
// nodeID, across every Show — deliberately not scoped to only the active
// Show: a blackAndSilence effect is clearing THIS node's own output,
// whatever Show most recently assigned a surface to it, not re-deriving
// participation through the active Show's own catalog (which is exactly
// what H0.2 says may be wrong right now).
func surfaceIDsForNodeAnyShow(ctx context.Context, cfg ConfigStore, nodeID string) ([]string, error) {
	objs, err := cfg.ListConfigObjects(ctx, config.ShowSurfaceConfigKind)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := cfg.GetConfigRevision(ctx, config.ShowSurfaceConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			continue
		}
		var p struct {
			Node string `json:"node"`
		}
		if err := json.Unmarshal([]byte(rev.PayloadJSON), &p); err != nil {
			continue
		}
		if p.Node == nodeID {
			out = append(out, obj.ID)
		}
	}
	return out, nil
}

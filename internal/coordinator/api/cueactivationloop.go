package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
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
			l.h.cueActivationTick(ctx, l.h.now())
		}()
	default:
		// Previous tick still running; skip this one — the next tick
		// (periodic or nudged) will pick up whatever changed.
	}
}

// cueActivationTick resolves and dispatches an activation for every FPP
// instance this coordinator has an accepted observation from.
func (h *handlers) cueActivationTick(ctx context.Context, now time.Time) {
	if h.deps.FPPReconciliation == nil || h.deps.FPPObservations == nil {
		return
	}
	obsList, err := h.deps.FPPObservations.ListFPPPlaylistEntryObservations(ctx)
	if err != nil {
		h.logWarn("cue activation loop: failed to list fpp playlist entry observations", "error", err)
		return
	}
	for _, obs := range obsList {
		h.cueActivationTickOne(ctx, now, obs)
	}
}

func (h *handlers) cueActivationTickOne(ctx context.Context, now time.Time, obs store.FPPPlaylistEntryObservationRecord) {
	result, err := h.deps.FPPReconciliation.ReconcileFPPPlaylistEntryObservation(ctx, obs)
	if err != nil {
		h.logWarn("cue activation loop: reconcile failed", "instanceUuid", obs.InstanceUUID, "error", err)
		return
	}
	if h.deps.AssetManifests == nil {
		return
	}
	dec, err := cueactivate.Decide(ctx, h.deps.AssetManifests, result, obs, obs.InstanceUUID)
	if err != nil {
		h.logWarn("cue activation loop: decide failed", "instanceUuid", obs.InstanceUUID, "error", err)
		return
	}

	issuer := cueActivationIssuer{PrincipalID: cueActivationSystemPrincipalID(obs.InstanceUUID)}

	switch dec.State {
	case cueactivate.StateActivated, cueactivate.StateMismatched:
		var outcomes []cueActivationDispatchOutcome
		if len(dec.Activations) > 0 {
			outcomes = h.dispatchCueActivations(ctx, now, dec.Activations, issuer)
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
		// content while an operator is not looking at it. In show mode
		// only — never in setup/program mode, where an operator IS looking
		// and the refusal must stay loud rather than disappear to black —
		// fail that one node to black the identical way an H0.2 mismatch
		// does, reusing the SAME effect.
		assetMissingNodes := assetMissingNodeIDs(outcomes)
		if len(assetMissingNodes) > 0 && h.deps.Config != nil {
			mode, _, _, _, merr := resolveShowMode(ctx, h.deps.Config)
			if merr != nil {
				h.logWarn("cue activation loop: resolve show mode for asset-missing fail-to-black failed", "instanceUuid", obs.InstanceUUID, "error", merr)
			} else if assetMissingFailToBlack(mode.Mode, assetMissingNodes) {
				h.dispatchBlackAndSilence(ctx, now, assetMissingNodes, issuer, blackAndSilenceEpisode(obs))
			}
		}
	case cueactivate.StateUnbound, cueactivate.StateIdentityUnavailable:
		// Nothing to dispatch or hold — see [cueactivate.State]'s own doc
		// comment.
	}
}

// assetMissingNodeIDs returns the node ids among outcomes whose own
// [cueActivationDispatchOutcome.AuthorizeOutcome] is [cueauth.
// OutcomeAssetMissing] — the fail-to-black target set: every node THIS
// coordinator's own pre-dispatch [cueactivate.Authorize] refused because
// the activated Cue's own asset is genuinely missing, never a node
// refused for an unrelated reason (cross-show, stale generation/catalog/
// cue) that fail-to-black has no bearing on.
func assetMissingNodeIDs(outcomes []cueActivationDispatchOutcome) []string {
	var out []string
	for _, o := range outcomes {
		if o.AuthorizeOutcome == cueauth.OutcomeAssetMissing {
			out = append(out, o.NodeID)
		}
	}
	return out
}

// assetMissingFailToBlack reports whether the fail-to-black effect should
// fire for assetMissingNodes under mode: true only in show mode
// (config.ShowModeShow) and only when there is at least one node to
// black. In setup/program mode the refusal stays loud — the existing
// per-node log line and audit entry — rather than disappearing to black,
// per the owner's own ruling: errors can be caught in programming mode
// since it is designed to be used while the operator is looking at the
// show, so a refusal there must stay visible rather than fail silently to
// black.
func assetMissingFailToBlack(mode string, assetMissingNodes []string) bool {
	return mode == config.ShowModeShow && len(assetMissingNodes) > 0
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
		surfaceIDs, err := surfaceIDsForNodeAnyShow(ctx, h.deps.Config, nodeID)
		if err != nil {
			h.logWarn("cue activation loop: resolve surfaces for blackAndSilence failed", "nodeId", nodeID, "error", err)
		} else {
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

		h.dispatchBlackAndSilenceAudioStop(ctx, now, nodeID, issuer, episode)
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
	in := audioDispatchInput{
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

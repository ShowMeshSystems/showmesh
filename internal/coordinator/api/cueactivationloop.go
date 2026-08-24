package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
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
}

// NewCueActivationLoop builds a [CueActivationLoop] against deps/opts,
// mirroring [NewNightLoop]'s identical "build a private *handlers, never
// route through HTTP" construction.
func NewCueActivationLoop(deps Dependencies, opts Options) *CueActivationLoop {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	return &CueActivationLoop{
		h:        &handlers{deps: deps, clock: opts.Clock, logger: opts.Logger},
		interval: opts.CueActivationLoopInterval,
		logger:   opts.Logger,
		inFlight: make(chan struct{}, 1),
		nudge:    make(chan struct{}, 1),
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
			l.runTick(ctx)
		}
	}
}

// runTick starts one tick as a non-blocking, skip-if-already-running
// attempt — the exact body Run's own select cases used to inline, factored
// out so the immediate first tick, the periodic tick, and a nudge-driven
// tick all share it rather than three copies that could drift.
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
		if len(dec.Activations) > 0 {
			for _, outcome := range h.dispatchCueActivations(ctx, now, dec.Activations, issuer) {
				switch {
				case outcome.Err != nil:
					h.logWarn("cue activation loop: dispatch failed", "instanceUuid", obs.InstanceUUID, "nodeId", outcome.NodeID, "error", outcome.Err)
				case outcome.AuthorizeOutcome != "":
					h.logWarn("cue activation loop: this coordinator's own authorization refused before dispatch",
						"instanceUuid", obs.InstanceUUID, "nodeId", outcome.NodeID, "outcome", outcome.AuthorizeOutcome)
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
			h.dispatchBlackAndSilence(ctx, now, dec.ClearNodes, issuer)
		}
	case cueactivate.StateUnbound, cueactivate.StateIdentityUnavailable:
		// Nothing to dispatch or hold — see [cueactivate.State]'s own doc
		// comment.
	}
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

// dispatchBlackAndSilence dispatches render.surface.clear to every surface
// belonging to a node in nodeIDs, and audio.session.stop against
// [blackAndSilenceAudioSessionID] on every one of those nodes that has
// declared an audio.node object — H0.2's full blackAndSilence effect
// ("the renderer blacks its surfaces and ShowMesh-owned audio silences"),
// not only its render half. A node with no audio.node object has no
// program-audio route to silence at all (ADR-018), so it is skipped
// rather than dispatched-and-refused every tick.
func (h *handlers) dispatchBlackAndSilence(ctx context.Context, now time.Time, nodeIDs []string, issuer cueActivationIssuer) {
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
					IdempotencyKey: "cueact-clear-" + nodeID + "-" + surfaceID,
					DesiredState:   "stopped",
					IssuerID:       issuer.PrincipalID, IssuerName: issuer.PrincipalName,
					Form: issuer.Form, CredentialID: issuer.CredentialID,
				}
				if _, problem, err := h.executeRenderDispatch(ctx, now, in); err != nil {
					h.logWarn("cue activation loop: blackAndSilence clear dispatch failed", "nodeId", nodeID, "surfaceId", surfaceID, "error", err)
				} else if problem != nil {
					h.logWarn("cue activation loop: blackAndSilence clear dispatch refused", "nodeId", nodeID, "surfaceId", surfaceID, "detail", problem.Detail)
				}
			}
		}

		h.dispatchBlackAndSilenceAudioStop(ctx, now, nodeID, issuer)
	}
}

// dispatchBlackAndSilenceAudioStop stops [blackAndSilenceAudioSessionID]
// on nodeID — this seam's own audio half of H0.2's blackAndSilence policy
// — if and only if nodeID has declared an audio.node object at all. A
// dispatch failure or refusal is logged, never silently swallowed
// (TRACK-H-H3-SPEC.md section 6's "a refusal is a state with evidence,
// never a silent no-op" applied here to a policy effect rather than an
// authorization check): an operator who chose blackAndSilence specifically
// to avoid the wrong content reaching an audience must be able to see that
// the silence attempt itself failed, not just infer it from continued
// audio on the wall.
func (h *handlers) dispatchBlackAndSilenceAudioStop(ctx context.Context, now time.Time, nodeID string, issuer cueActivationIssuer) {
	hasAudioNode, err := nodeHasAudioNodeObject(ctx, h.deps.Config, nodeID)
	if err != nil {
		h.logWarn("cue activation loop: resolve audio.node for blackAndSilence failed", "nodeId", nodeID, "error", err)
		return
	}
	if !hasAudioNode {
		return
	}

	idempotencyKey := "cueact-silence-" + nodeID
	revision := uint64(now.UnixNano())
	in := audioDispatchInput{
		Action: "audio.session.stop", NodeID: nodeID, SessionID: blackAndSilenceAudioSessionID,
		Params: map[string]any{
			"sessionId": blackAndSilenceAudioSessionID, "invocationId": idempotencyKey, "revision": revision,
		},
		Revision: revision, IdempotencyKey: idempotencyKey,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
	}
	if _, problem, err := h.executeAudioSessionDispatch(ctx, now, in); err != nil {
		h.logWarn("cue activation loop: blackAndSilence audio stop dispatch failed", "nodeId", nodeID, "error", err)
	} else if problem != nil {
		h.logWarn("cue activation loop: blackAndSilence audio stop dispatch refused", "nodeId", nodeID, "detail", problem.Detail)
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

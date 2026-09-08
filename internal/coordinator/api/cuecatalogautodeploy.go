package api

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/currentrun"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Part 2's own automatic-deploy trigger: when a cue or
// playlist edit changes the catalog a node must hold, and nothing is
// currently running from that node's held catalog, the new catalog is
// deployed on its own rather than waiting for an operator to notice and
// run cuecatalog.deploy by hand. It is triggered by
// internal/coordinator/fallbackreconcile's own periodic detection of a
// missing or stale cue-catalog acknowledgement (the same evidence that
// package already computes to refuse a fallback-program compile), wired
// in from coordinator.go through the small fallbackreconcile.CatalogDeployer
// interface, so this package needs no dependency on fallbackreconcile for
// that to work, since AutoDeployCueCatalog structurally satisfies it.

// cueCatalogAutoDeploySystemPrincipalID/Name attribute every automatic
// deploy this trigger dispatches, mirroring cueActivationSystemPrincipalID's
// identical reasoning (cueactivationloop.go): an unattended background
// dispatch must never be audited under whichever operator most recently
// authenticated.
const (
	cueCatalogAutoDeploySystemPrincipalID   = "system-catalog-autodeploy"
	cueCatalogAutoDeploySystemPrincipalName = "ShowMesh automatic cue-catalog deploy"
)

// AutoDeployCueCatalog implements fallbackreconcile.CatalogDeployer. It is
// called once per node whose cue-catalog acknowledgement is not current
// (fallbackreconcile resolves that candidate set itself, reusing
// [fppreconcile.NodeCatalogAckStatus], the one place that resolution is
// made). A deploy that would land under a running Cue is held rather than
// dispatched (see [handlers.cueCatalogAutoDeployHold]), and this method
// is best-effort from its caller's point of view (it returns nothing and
// never blocks the reconciler's own tick), matching this coordinator's
// existing posture for every other background best-effort action taken
// after a deploy succeeds (h.applyShowmeshAudioPlaylistIfAny,
// h.establishRenderAssignments, both in cuecatalogdeploy.go).
func (h *handlers) AutoDeployCueCatalog(ctx context.Context, now time.Time, nodeID string) {
	hold := h.cueCatalogAutoDeployHold(ctx, now, nodeID)
	if hold.Hold {
		h.logDebug("cue catalog auto-deploy: held", "node", nodeID, "evidenceUncertain", hold.EvidenceUncertain, "reason", hold.Reason)
		return
	}
	res := h.dispatchCueCatalogDeploy(ctx, now, nodeID, uuid.NewString(), cueCatalogDeployIssuer{
		PrincipalID: cueCatalogAutoDeploySystemPrincipalID, PrincipalName: cueCatalogAutoDeploySystemPrincipalName,
	})
	switch {
	case res.Err != nil:
		h.logWarn("cue catalog auto-deploy: dispatch failed", "node", nodeID, "error", res.Err)
	case res.Problem != nil:
		h.logWarn("cue catalog auto-deploy: refused", "node", nodeID, "detail", res.Problem.Detail)
	default:
		h.logDebug("cue catalog auto-deploy: dispatched", "node", nodeID, "outcome", res.Result.Outcome)
	}
}

// cueCatalogAutoDeployHoldResult is [handlers.cueCatalogAutoDeployHold]'s
// verdict on whether nodeID currently has something running that a new
// catalog deploy must not interrupt.
type cueCatalogAutoDeployHoldResult struct {
	Hold bool
	// Reason is free text explaining Hold, always set when Hold is true.
	Reason string
	// EvidenceUncertain distinguishes WHY Hold is true. false means
	// positive, current evidence that a Cue is genuinely running (H3
	// section 6's stale-catalog refusal exists precisely because swapping
	// catalog authority mid-execution is unsafe). true means this
	// coordinator could not confirm the node is idle at all: stale,
	// missing, or unreadable playback evidence, holding out of
	// caution rather than from a confirmed activation. Both hold the
	// deploy identically, but only the uncertain case is something an
	// operator needs to go look at; readiness (nightcatalogreadiness.go)
	// names which one it is.
	EvidenceUncertain bool
}

// cueCatalogAutoDeployHold decides Part 2's own safety gate, and is the
// ONE place that decision is made: both AutoDeployCueCatalog (above) and
// the night session's own readiness check (nightcatalogreadiness.go, Part
// 3's "readiness WARNS while a deploy is pending") read this same verdict
// rather than deriving it a second way.
//
// It reads [Dependencies.CurrentRuns], the SAME server-computed current-
// runs projection GET /api/v1/current-runs already serves (currentruns.go),
// reused rather than re-deriving FPP and audio playback state a second
// time. A currently PLAYING fpp run holds every node: an FPP-backed Cue's
// authority is synchronized off FPP's own timeline, not scoped per node
// any cheaper or safer than treating the show's whole FPP activity as one
// signal. A currently playing or paused showmesh-audio run holds only the
// node(s) it targets.
//
// The owner's own ruling on the asymmetry this deliberately leans on: a
// wrongly-held deploy self-corrects at the very next reconcile tick once
// whatever was running finishes, while a wrongly-released one interrupts a
// live show. So every branch below that cannot positively confirm "idle"
// holds (EvidenceUncertain=true), and only a genuinely fresh, readable
// "nothing is playing" answer clears it. That last case must be reached
// on its own terms, though, never folded into "uncertain": the common
// between-shows case is exactly "nothing running, and no evidence of
// anything running because nothing is happening," and treating silence as
// uncertain would mean automatic deploy never fires for the one case this
// whole feature exists to fix.
func (h *handlers) cueCatalogAutoDeployHold(ctx context.Context, now time.Time, nodeID string) cueCatalogAutoDeployHoldResult {
	snap, err := h.deps.CurrentRuns.Snapshot(ctx, now)
	if err != nil {
		return cueCatalogAutoDeployHoldResult{Hold: true, EvidenceUncertain: true,
			Reason: fmt.Sprintf("current-runs evidence could not be read: %s", err.Error())}
	}
	for _, run := range snap.Runs {
		switch run.Runner {
		case currentrun.RunnerFPP:
			switch run.Playback.State {
			case "playing":
				return cueCatalogAutoDeployHoldResult{Hold: true,
					Reason: fmt.Sprintf("fpp run %q is currently playing", run.ID)}
			case "stopped", "idle":
				// Confirmed idle; keep scanning the rest of the snapshot.
			default:
				return cueCatalogAutoDeployHoldResult{Hold: true, EvidenceUncertain: true,
					Reason: fmt.Sprintf("fpp run %q's playback state is %q rather than confirmed idle", run.ID, run.Playback.State)}
			}
		case currentrun.RunnerShowmeshAudio:
			targetsNode := false
			for _, t := range run.Targets {
				if t.Kind == string(observation.ResourceNode) && t.ID == nodeID {
					targetsNode = true
					break
				}
			}
			if !targetsNode {
				continue
			}
			switch run.Playback.State {
			case "stopped", "idle", "ready":
				// Confirmed idle; keep scanning.
			case "playing", "paused":
				return cueCatalogAutoDeployHoldResult{Hold: true,
					Reason: fmt.Sprintf("audio session %q on this node is %s", run.ID, run.Playback.State)}
			default:
				return cueCatalogAutoDeployHoldResult{Hold: true, EvidenceUncertain: true,
					Reason: fmt.Sprintf("audio session %q's playback state is %q rather than confirmed idle", run.ID, run.Playback.State)}
			}
		}
	}
	return cueCatalogAutoDeployHoldResult{}
}

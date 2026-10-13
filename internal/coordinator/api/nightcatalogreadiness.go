package api

import (
	"context"
	"fmt"
	"sort"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
)

// This file is Part 1's own per-participating-node catalog-currency
// check, added to the night session's run-readiness
// (nightComputeReadinessChecks, nightsessioncontrol.go) alongside seam
// F3/F4/F5's existing checks. Track F's run-readiness never checked
// catalog currency before this seam: it checked FPP reachability, the
// resting asset's duration, and the resting playlist's idle-read shape,
// none of which says anything about whether a participating node has
// actually acknowledged the catalog revision the coordinator currently
// requires, which is why an operator starting a night after editing a
// cue got no warning before the first activation refused mid-show.
//
// It reuses [fppreconcile.NodeCatalogAckStatus] rather than resolving a
// node's acknowledgement a second way (that function's own doc comment
// states it is the ONE place this is resolved), and names its checks with
// [fppreconcile.ReadinessNodeCatalogStale], the already-declared condition
// string H6's own per-Playlist readiness check uses for the identical
// fact, rather than minting a second identifier for it.
//
// Part 3's own WARN-not-FAIL distinction lives here too, on the SAME
// per-node check: a stale node whose deploy is currently held safely
// pending (see [handlers.cueCatalogAutoDeployHold]) reports degraded with a
// reason explaining the hold, rather than failing readiness outright. This
// file's own [nightCheckNodeCatalogCurrent] is the one place that FAIL/WARN
// split is made, and the free-text reason still distinguishes a genuinely
// running cue from stale/uncertain playback evidence, exactly as
// [fppreconcile.Report.Warning] does for its own, separate readiness
// surface.

// nightCatalogCurrentCheckPrefix names every check this file produces,
// built from the already-declared [fppreconcile.ReadinessNodeCatalogStale]
// condition string.
const nightCatalogCurrentCheckPrefix = "catalog:" + string(fppreconcile.ReadinessNodeCatalogStale)

// nightCheckCatalogCurrent is this file's own entry point, called from
// nightComputeReadinessChecks. It resolves show's participating nodes,
// every node [assetsync.ResolveCueCatalog] resolves at least one non-empty
// output for ([assetsync.Catalog.HasAnyOutput]), and returns one check per
// participating node. A node with no resolved output has no catalog
// obligation at all and produces no check, matching
// [fppreconcile.NodeCatalogAckStatus]'s own currentRevision == "" rule.
func (h *handlers) nightCheckCatalogCurrent(ctx context.Context, now time.Time, show string) []nightReadinessCheck {
	if h.deps.AssetManifests == nil {
		return []nightReadinessCheck{{name: nightCatalogCurrentCheckPrefix, health: nightCheckStateNotConfigured, reason: nightNoFileRecordReason}}
	}
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		return []nightReadinessCheck{{name: nightCatalogCurrentCheckPrefix, health: nightHealthUnknown(), cause: err, reason: nightActiveShowUnreadReason}}
	}
	if !active.Configured || active.ShowID != show {
		// A node's cue-catalog acknowledgement is only ever compared
		// against the revision the coordinator resolves for the ACTUALLY
		// active show (TRACK-H-H3-SPEC.md section 4). Fabricating one for
		// a show that is not active would compare a node's real
		// acknowledgement against a revision this coordinator never
		// actually asked it to hold: a false "stale" report, worse than
		// not checking at all. So this check is skipped, neither failed
		// nor warned, exactly as fppreconcile's own identical condition
		// (nodeCatalogReadiness's doc comment, internal/coordinator/
		// fppreconcile/readiness.go) does for the identical reason.
		// not_configured, not unknown: this is the ordinary state before
		// an operator has activated any show at all, not evidence this
		// coordinator failed to determine something it should know.
		return []nightReadinessCheck{{name: nightCatalogCurrentCheckPrefix, health: nightCheckStateNotConfigured,
			reason: fmt.Sprintf("Show %s is not the active show, so its cue list was not checked. Activate the show.", show)}}
	}

	nodes, err := h.deps.AssetManifests.ListNodeDeclarations(ctx)
	if err != nil {
		return []nightReadinessCheck{{name: nightCatalogCurrentCheckPrefix, health: nightHealthUnknown(), cause: err,
			reason: "The coordinator could not read the list of nodes. Run readiness again."}}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })

	var checks []nightReadinessCheck
	for _, n := range nodes {
		if check, participates := h.nightCheckNodeCatalogCurrent(ctx, now, active, n.NodeID); participates {
			checks = append(checks, check)
		}
	}
	return checks
}

// nightCheckNodeCatalogCurrent is [handlers.nightCheckCatalogCurrent]'s
// own per-node check. participates is false only when nodeID resolves no
// output at all for active's show (no catalog obligation to check), in
// which case check is the zero value and must not be added to the result.
func (h *handlers) nightCheckNodeCatalogCurrent(ctx context.Context, now time.Time, active assetsync.ActiveShow, nodeID string) (check nightReadinessCheck, participates bool) {
	name := nightCatalogCurrentCheckPrefix + ":" + nodeID
	unreadable := func(err error) nightReadinessCheck {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), cause: err,
			reason: fmt.Sprintf("The coordinator could not read the cue list for node %s. Run readiness again.", nodeID)}
	}

	catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
	if err != nil {
		return unreadable(err), true
	}
	if !catalog.HasAnyOutput() {
		return nightReadinessCheck{}, false
	}

	status, _, _, err := fppreconcile.NodeCatalogAckStatus(ctx, h.deps.AssetManifests, nodeID, catalog.Revision)
	if err != nil {
		return unreadable(err), true
	}
	if status == v1.CueCatalogStatusCurrent {
		// A node can only hold a conflicted revision via an operator's own
		// override=true deploy. fppreconcile.CueCatalogOverrideStatus is
		// the one place this coverage decision is made, reused so this and
		// exclusiveClaimReadiness never disagree.
		if len(catalog.Conflicts) > 0 {
			covered, overrideRec, err := fppreconcile.CueCatalogOverrideStatus(ctx, h.deps.AssetManifests, nodeID, catalog)
			if err != nil {
				return unreadable(err), true
			}
			if covered {
				return nightReadinessCheck{name: name, health: nightHealthDegraded(),
					reason: fppreconcile.CueCatalogOverrideWarning(nodeID, catalog, overrideRec)}, true
			}
			return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(
				"Node %s has cues sharing an output that nobody accepted. Fix the cues, or deploy and accept the conflict.", nodeID)}, true
		}
		return nightReadinessCheck{name: name, health: nightHealthHealthy()}, true
	}

	// Stale or never-acknowledged: Part 3's own WARN carve-out applies
	// only when a deploy is currently held safely pending for this node.
	// See [handlers.cueCatalogAutoDeployHold]'s own doc comment for why
	// EvidenceUncertain is named separately rather than folded into one
	// generic hold reason: an operator needs to know whether a Cue is
	// genuinely running here, or whether this coordinator simply cannot
	// currently confirm the node is idle.
	hold := h.cueCatalogAutoDeployHold(ctx, now, nodeID)
	if hold.Hold {
		reason := fmt.Sprintf(
			"Node %s does not have the active show's cue list yet. It is sent when the node finishes its cue.", nodeID)
		if hold.EvidenceUncertain {
			reason = fmt.Sprintf(
				"Node %s does not have the active show's cue list yet. It is sent once the node is seen idle.", nodeID)
		}
		return nightReadinessCheck{name: name, health: nightHealthDegraded(), reason: reason}, true
	}

	return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(
		"Node %s does not have the active show's cue list. Deploy the cue list to the node.", nodeID)}, true
}

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
// pending (see [handlers.cueCatalogAutoDeployHold]) reports healthy with a
// reason explaining the hold, rather than failing readiness outright.
// This file's own [nightCheckNodeCatalogCurrent] is the one place that
// FAIL/WARN split is made, so it never needs a new [nightCheckState]
// value: the distinction is carried entirely in free-text reason, exactly
// as [fppreconcile.Report.Warning] does for its own, separate readiness
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
		return []nightReadinessCheck{{name: nightCatalogCurrentCheckPrefix, health: nightCheckStateNotConfigured,
			reason: "no asset manifest store is configured on this coordinator"}}
	}
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		return []nightReadinessCheck{{name: nightCatalogCurrentCheckPrefix, health: nightHealthUnknown(),
			reason: "could not resolve the active show: " + err.Error()}}
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
			reason: fmt.Sprintf("show.active does not currently name this session's own show %q; catalog currency cannot be evaluated until it does", show)}}
	}

	nodes, err := h.deps.AssetManifests.ListNodeDeclarations(ctx)
	if err != nil {
		return []nightReadinessCheck{{name: nightCatalogCurrentCheckPrefix, health: nightHealthUnknown(),
			reason: "could not list node declarations: " + err.Error()}}
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

	catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
	if err != nil {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: "could not resolve cue catalog: " + err.Error()}, true
	}
	if !catalog.HasAnyOutput() {
		return nightReadinessCheck{}, false
	}

	status, ackRevision, _, err := fppreconcile.NodeCatalogAckStatus(ctx, h.deps.AssetManifests, nodeID, catalog.Revision)
	if err != nil {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: "could not resolve node catalog acknowledgement status: " + err.Error()}, true
	}
	if status == v1.CueCatalogStatusCurrent {
		return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: fmt.Sprintf(
			"node %q has acknowledged the active show's required catalog revision %q", nodeID, catalog.Revision)}, true
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
			"node %q has not acknowledged the active show's required catalog revision %q (acknowledged revision: %q); a deploy is held because %s, and will apply automatically once it clears",
			nodeID, catalog.Revision, ackRevision, hold.Reason)
		if hold.EvidenceUncertain {
			reason = fmt.Sprintf(
				"node %q has not acknowledged the active show's required catalog revision %q (acknowledged revision: %q); a deploy is held because this node's own playback evidence is stale or unreadable rather than confirmed idle (%s), and will apply automatically once fresh evidence shows nothing running",
				nodeID, catalog.Revision, ackRevision, hold.Reason)
		}
		return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: reason}, true
	}

	return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(
		"node %q has not acknowledged the active show's required catalog revision %q (status: %s, acknowledged revision: %q); no deploy is currently held for it",
		nodeID, catalog.Revision, status, ackRevision)}, true
}

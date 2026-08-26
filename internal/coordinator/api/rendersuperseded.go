package api

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is this project's own seam for closing that gap: ADR-043's
// H0.7 requires a render a node holds across a Show switch to be
// "reported as superseded with the Show and generation that authorized
// it... never reported as current or healthy."
// internal/coordinator/collector/noderender states only what a
// node itself holds (its own PipelineState, CatalogRevision, CueID, Show,
// generation); it has no notion of which Show is currently active and must
// never be given one — H0.7's own ruling: "a node that decided this for
// itself would need to know the active show, which is exactly the coupling
// Track H avoids." This file is the ONE place that comparison happens: it
// overlays [noderender.Store.NodeRenderObservations]' surface.pipeline.state
// value with [mqttproto.RenderPipelineStateSuperseded] whenever a surface's
// own reported catalog revision or Cue no longer matches
// [assetsync.ResolveCueCatalog]'s current answer for this node.
//
// This is a READ-TIME overlay only, never a second write: the persisted
// surface.pipeline.state row [Store] and internal/coordinator/collector/
// noderender's own Poll produce stays exactly "running", because
// renderdispatch.go's confirmRenderCommand reads that SAME persisted row
// (via ObservationFilter, not through this file) to confirm a dispatched
// render.surface.apply/restart, and it must keep seeing the node's raw,
// honest state to do that. Every mapNode call site (handlers.go's GET
// /nodes, GET /nodes/{id}, the snapshot, and stream.go's Hub.render) must
// go through [nodeRenderView] instead of calling
// [NodeRenderLister.NodeRenderObservations] directly, so the verdict
// renders identically everywhere — mapNode's own doc comment already
// requires byte-identical output regardless of which of those four call
// sites produced it.
//
// Signal names are redeclared here as plain strings rather than imported
// from internal/coordinator/collector/noderender: this package's own
// TestPackageNeverImportsACollector (resolumeinstances_test.go) forbids
// importing any internal/coordinator/collector/... package at all, the
// same reason renderdispatch.go's renderSignalPipelineState is a local
// constant rather than a cross-package reference.
const (
	signalSurfaceContentCatalogRevisionForSupersede = "surface.content.catalog_revision"
	signalSurfaceContentCueIDForSupersede           = "surface.content.cue_id"
	signalSurfaceContentShowForSupersede            = "surface.content.show"
	signalSurfaceContentGenerationForSupersede      = "surface.content.generation"
)

// nodeRenderView returns nodeID's current render observations with this
// file's own superseded verdict applied — see the doc comment above for why
// every mapNode call site must go through this instead of calling
// render.NodeRenderObservations directly. assetManifests may be nil (an API
// wired with no cue-catalog data source, [Dependencies.AssetManifests]'s
// own documented nil-safe posture elsewhere in this package): the verdict
// is simply never applied, and obs renders exactly as the node reported it.
func nodeRenderView(ctx context.Context, render NodeRenderLister, assetManifests *store.Store, nodeID string, now time.Time) []observation.Observation {
	obs := render.NodeRenderObservations(nodeID)
	return applySupersededVerdict(ctx, assetManifests, nodeID, obs, now)
}

// applySupersededVerdict compares each surface's reported catalog revision
// and Cue (SignalSurfaceContentCatalogRevision/CueID) against
// [assetsync.ResolveCueCatalog]'s current answer for nodeID, and — only for
// a surface currently reporting [mqttproto.RenderPipelineStateRunning] —
// swaps that value for [mqttproto.RenderPipelineStateSuperseded] when
// either no longer matches. A revision mismatch alone already implies a
// Show, generation, Cue set, or asset change (the revision hashes all of
// them — pkg/cuecatalog.RevisionInput's own doc comment); the Cue check is
// a second, independent path for a legacy assignment that carries a Cue but
// no catalog revision (one persisted before TRACK-H-H3-SPEC.md section 5
// existed).
//
// Every other field on the overridden observation — ObservedAt, ValidFor,
// CollectedAt, Source — is left exactly as the node reported it: this
// overlay changes what the coordinator says the value MEANS, never when or
// how it was observed, so an operator's freshness read (current/stale/
// unknown_age) keeps tracking the node's own report, not this comparison.
//
// A surface with no active-show resolution to compare against (no show
// configured, or a transient read error) is left untouched: with no
// authority to compare against, there is no basis to call anything
// superseded, and a transient store error here must not turn an otherwise
// healthy read into an incorrectly derived verdict.
func applySupersededVerdict(ctx context.Context, st *store.Store, nodeID string, obs []observation.Observation, now time.Time) []observation.Observation {
	if st == nil || len(obs) == 0 {
		return obs
	}

	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !active.Configured {
		return obs
	}

	catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, nodeID)
	if err != nil {
		return obs
	}
	catalogCueIDs := make(map[string]bool, len(catalog.Entries))
	for _, e := range catalog.Entries {
		catalogCueIDs[e.CueID] = true
	}

	out := make([]observation.Observation, len(obs))
	copy(out, obs)

	type surfaceContent struct {
		pipelineIdx             int
		revision, cueID         string
		haveRevision, haveCueID bool
		show                    string
		generation              int64
	}
	bySurface := map[string]*surfaceContent{}
	surfaceFor := func(id string) *surfaceContent {
		sc, ok := bySurface[id]
		if !ok {
			sc = &surfaceContent{pipelineIdx: -1}
			bySurface[id] = sc
		}
		return sc
	}

	for i, o := range out {
		if o.Resource.Kind != observation.ResourceSurface {
			continue
		}
		sc := surfaceFor(o.Resource.ID)
		switch string(o.Signal) {
		case renderSignalPipelineState:
			sc.pipelineIdx = i
		case signalSurfaceContentCatalogRevisionForSupersede:
			if v, ok := o.Value.(string); ok {
				sc.revision, sc.haveRevision = v, true
			}
		case signalSurfaceContentCueIDForSupersede:
			if v, ok := o.Value.(string); ok {
				sc.cueID, sc.haveCueID = v, true
			}
		case signalSurfaceContentShowForSupersede:
			if v, ok := o.Value.(string); ok {
				sc.show = v
			}
		case signalSurfaceContentGenerationForSupersede:
			if v, ok := o.Value.(int64); ok {
				sc.generation = v
			}
		}
	}

	for _, sc := range bySurface {
		if sc.pipelineIdx == -1 {
			continue
		}
		stateVal, ok := out[sc.pipelineIdx].Value.(string)
		if !ok || stateVal != mqttproto.RenderPipelineStateRunning {
			continue
		}

		supersededByRevision := sc.haveRevision && sc.revision != catalog.Revision
		supersededByCue := sc.haveCueID && !catalogCueIDs[sc.cueID]
		if !supersededByRevision && !supersededByCue {
			continue
		}

		// No internal citation in this operator-facing string (this
		// package's own copy-guard test) — the policy this implements is
		// documented on [mqttproto.RenderPipelineStateSuperseded] instead.
		reason := fmt.Sprintf(
			"this surface is holding a render authorized by show %q generation %d; the coordinator's currently active show is %q generation %d, and this render's authorization no longer matches its resolved cue catalog",
			sc.show, sc.generation, active.ShowID, active.Generation,
		)
		superseded := out[sc.pipelineIdx]
		superseded.Value = mqttproto.RenderPipelineStateSuperseded
		superseded.Quality = observation.QualityDerived
		superseded.Reason = reason
		out[sc.pipelineIdx] = superseded
	}

	return out
}

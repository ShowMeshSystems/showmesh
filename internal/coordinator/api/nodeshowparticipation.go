package api

import (
	"context"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// resolveActiveShowForParticipation resolves the active show once per
// render pass (mapNode's four call sites each call this exactly once,
// before their node loop), matching nightcatalogreadiness.go's own
// "resolved once, passed into every per-node check" shape: Generation does
// not vary per node, so nothing is gained by re-resolving it inside the
// loop. assetManifests may be nil (an API wired with no cue-catalog data
// source), in which case active and err are both zero-valued and every
// node's participation resolves to "not_configured".
func resolveActiveShowForParticipation(ctx context.Context, assetManifests *store.Store) (active assetsync.ActiveShow, err error) {
	if assetManifests == nil {
		return assetsync.ActiveShow{}, nil
	}
	return assetsync.ResolveActiveShow(ctx, assetManifests)
}

// nodeShowParticipation resolves nodeID's [v1.NodeShowParticipation]
// against active (already resolved once for this render pass by
// [resolveActiveShowForParticipation]), applying the identical
// assetsync.ResolveCueCatalog/Catalog.HasAnyOutput test
// nightcatalogreadiness.go's nightCheckNodeCatalogCurrent and
// rendersuperseded.go's applySupersededVerdict already apply for their own
// purposes, reused here rather than defined a fifth way.
//
// This value refreshes through the existing per-tick node.changed diff
// (Hub.render recomputes every node's full mapNode output on every tick);
// nothing here signals an active-show change directly, and nothing should.
func nodeShowParticipation(ctx context.Context, assetManifests *store.Store, active assetsync.ActiveShow, activeErr error, nodeID string) v1.NodeShowParticipation {
	if assetManifests == nil {
		return v1.NodeShowParticipation{State: "not_configured",
			Reason: strPtr("no asset manifest store is configured on this coordinator")}
	}
	if activeErr != nil {
		return v1.NodeShowParticipation{State: "unknown",
			Reason: strPtr("could not resolve the active show: " + activeErr.Error())}
	}
	if !active.Configured {
		return v1.NodeShowParticipation{State: "not_configured",
			Reason: strPtr("no show is currently active")}
	}

	catalog, err := assetsync.ResolveCueCatalog(ctx, assetManifests, active, nodeID)
	if err != nil {
		return v1.NodeShowParticipation{State: "unknown", Show: active.ShowID,
			Reason: strPtr("could not resolve cue catalog: " + err.Error())}
	}
	if !catalog.HasAnyOutput() {
		return v1.NodeShowParticipation{State: "not_participating", Show: active.ShowID}
	}
	return v1.NodeShowParticipation{State: "participating", Show: active.ShowID}
}

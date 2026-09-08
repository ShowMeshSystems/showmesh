package fppreconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// assetsMissingReadiness implements condition 11 ([ReadinessAssetsMissing]):
// a node that must render or play one of this Show's Cues does not
// currently hold an asset that has actually been uploaded and resolved to
// it. It reuses [assetsync.BuildNodeManifest] — the SAME per-node
// computation the asset-manifest API route and the sync service already
// use, itself scoped to the sequences nodeID's own participating Cues
// resolve to by [assetsync.NodeCueSequenceIDs] (a plain node id, so this
// covers audio and LTC targets exactly like render, with no separate
// resolution to write).
//
// Skipped entirely — never a failure — when p.Show is not the currently
// active show, mirroring [nodeCatalogReadiness]'s identical reasoning: a
// node only ever holds asset-inventory evidence for the active show, so
// there is nothing correct to compare against for any other Show.
//
// Only [assetsync.NodeManifest.Missing] (an asset that DOES exist in the
// store but this node's own inventory does not currently hold) fails this
// condition. [assetsync.NodeManifest.Gaps] — a sequence with NO current
// asset ever registered for this node at all — is deliberately left out:
// the coordinator and a node disagreeing about a sequence that was never
// uploaded in the first place is a separate, separately tracked defect,
// and folding it in here would silently expand this seam's scope into
// that one.
//
// An [assetsync.ManifestUnknown] node (never reported, a stale or
// incomplete report, or no active show at all) is never treated as a
// failure here, matching [assetsync.ComputeNodeManifest]'s own posture
// everywhere else in this codebase: "I could not check" must not read as
// "this node is missing something."
//
// Every declared node is checked, in id order, and the first node with a
// non-empty Missing list is reported — deterministic regardless of store
// iteration order, matching this file's other fleet-wide conditions (see
// [nodeCatalogReadiness]). Within that node's own Missing list, entries are
// sorted by sequence id before the first is named, so which asset is named
// does not depend on [assetsync.ExpectedAssetsForNode]'s own row order.
func assetsMissingReadiness(ctx context.Context, st *store.Store, p config.ShowPlaylistPayload) (ReadinessCondition, string, error) {
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		return "", "", fmt.Errorf("fppreconcile: resolve active show: %w", err)
	}
	if !active.Configured || active.ShowID != p.Show {
		return "", "", nil
	}

	inventoryInterval, err := resolveAssetInventoryInterval(ctx, st)
	if err != nil {
		return "", "", err
	}

	nodes, err := st.ListNodeDeclarations(ctx)
	if err != nil {
		return "", "", fmt.Errorf("fppreconcile: list node declarations: %w", err)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })

	now := time.Now()
	for _, n := range nodes {
		manifest, err := assetsync.BuildNodeManifest(ctx, st, now, inventoryInterval, n.NodeID)
		if err != nil {
			return "", "", fmt.Errorf("fppreconcile: build node manifest for %q: %w", n.NodeID, err)
		}
		if manifest.State != assetsync.ManifestNotReady || len(manifest.Missing) == 0 {
			continue
		}
		missing := append([]assetsync.MissingAsset(nil), manifest.Missing...)
		sort.Slice(missing, func(i, j int) bool { return missing[i].SequenceID < missing[j].SequenceID })
		m := missing[0]
		return ReadinessAssetsMissing, fmt.Sprintf(
			"node %q is missing asset %q for sequence %q (content hash %s), which this show's cues resolve to it for",
			n.NodeID, m.Filename, m.SequenceID, m.ContentHash), nil
	}
	return "", "", nil
}

// resolveAssetInventoryInterval reads the "assets.settings" singleton
// directly, the same store row [assetsync.Service] and the asset-manifest
// API route ultimately read — this package carries no handler-level
// AssetSettingsSource dependency to reuse — falling back to
// [config.DefaultAssetSettings] exactly as everywhere else in this
// codebase does for a coordinator that has never had this surface written.
func resolveAssetInventoryInterval(ctx context.Context, st *store.Store) (time.Duration, error) {
	obj, err := st.GetConfigObject(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.DefaultAssetSettings().InventoryInterval, nil
	case err != nil:
		return 0, fmt.Errorf("fppreconcile: get assets.settings: %w", err)
	case obj.CurrentRevision == 0:
		return config.DefaultAssetSettings().InventoryInterval, nil
	}
	rev, err := st.GetConfigRevision(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return 0, fmt.Errorf("fppreconcile: get assets.settings revision %d: %w", obj.CurrentRevision, err)
	}
	settings, verr := config.DecodeAssetSettingsPayload(rev.PayloadJSON)
	if verr != nil {
		return 0, fmt.Errorf("fppreconcile: decode assets.settings: %w", verr)
	}
	return settings.InventoryInterval, nil
}

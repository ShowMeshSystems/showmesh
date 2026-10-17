package api

import (
	"context"
	"fmt"
	"sort"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

const nightRestingRenderCheckPrefix = "resting:render:"

// nightCheckRestingRender lists, per render node, whether it draws the
// resting sequence (ADR-061 decision 7). A node that does not is
// informational, never a warning or failure.
func (h *handlers) nightCheckRestingRender(ctx context.Context, payload config.NightSessionPayload) []nightReadinessCheck {
	sequence := payload.Resting.TimelineAsset.Sequence
	if h.deps.AssetManifests == nil || sequence == "" {
		return nil
	}
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil || !active.Configured || active.ShowID != payload.Show {
		return nil
	}
	nodes, err := h.deps.AssetManifests.ListNodeDeclarations(ctx)
	if err != nil {
		return nil
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })

	var checks []nightReadinessCheck
	anyDraws := false
	for _, n := range nodes {
		name := nightRestingRenderCheckPrefix + n.NodeID
		catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, n.NodeID)
		if err != nil {
			checks = append(checks, nightReadinessCheck{name: name, health: nightHealthUnknown(), cause: err,
				reason: fmt.Sprintf("The coordinator could not read the cue list for node %s. Run readiness again.", n.NodeID)})
			continue
		}
		if !catalog.RenderNode {
			continue
		}
		switch {
		case catalog.Resting != nil:
			anyDraws = true
			checks = append(checks, nightReadinessCheck{name: name, health: nightHealthHealthy(),
				reason: fmt.Sprintf("Node %s draws sequence %s during resting.", n.NodeID, sequence)})
		case catalog.RestingCoveredByCue != "":
			anyDraws = true
			checks = append(checks, nightReadinessCheck{name: name, health: nightHealthHealthy(),
				reason: fmt.Sprintf("Node %s draws sequence %s during resting through Cue %s.", n.NodeID, sequence, catalog.RestingCoveredByCue)})
		default:
			checks = append(checks, nightReadinessCheck{name: name, health: nightCheckStateNotConfigured,
				reason: fmt.Sprintf("Node %s does not have sequence %s, so it stays dark during resting. Upload the sequence for this node to draw it.", n.NodeID, sequence)})
		}
	}
	if anyDraws {
		if c, ok := h.nightCheckEndOfNightPlaysResting(ctx, payload); ok {
			checks = append(checks, c)
		}
	}
	return checks
}

// nightCheckEndOfNightPlaysResting reports an end of night playlist that
// never plays the resting sequence, because render nodes draw only that
// sequence (ADR-061 decision 4).
func (h *handlers) nightCheckEndOfNightPlaysResting(ctx context.Context, payload config.NightSessionPayload) (nightReadinessCheck, bool) {
	playlist := payload.Resting.EndOfNightPlaylist
	if playlist == "" || playlist == payload.Resting.Playlist {
		return nightReadinessCheck{}, false
	}
	endpoint, ok, err := h.resolveFPPEndpoint(ctx, payload.Resting.FPPInstanceID)
	if err != nil || !ok {
		return nightReadinessCheck{}, false
	}
	res := nightResolveFSEQDuration(ctx, h.deps, h.deps.Assets, payload.Show, payload.Resting.TimelineAsset)
	if res.Filename == "" {
		return nightReadinessCheck{}, false
	}
	def, err := nightReadPlaylistDefinition(ctx, endpoint, playlist)
	if err != nil {
		return nightReadinessCheck{}, false
	}
	for _, item := range def.MainPlaylist {
		if item.SequenceName == res.Filename {
			return nightReadinessCheck{}, false
		}
	}
	return nightReadinessCheck{name: nightCheckPrefixEndOfNight + ":render:" + playlist, health: nightCheckStateNotConfigured,
		reason: fmt.Sprintf("Playlist %s does not play sequence %s, so render nodes stay dark at the end of the night. Add the sequence to the playlist to draw it.", playlist, payload.Resting.TimelineAsset.Sequence)}, true
}

package api

import (
	"context"
	"fmt"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func unknownNightAudioPlan(reason string) v1.NightBackgroundAudioPlan {
	return v1.NightBackgroundAudioPlan{State: v1.NightEvidenceUnknown, Reason: reason, Nodes: []string{}, Items: []v1.NightBackgroundAudioPlanItem{}}
}

// mapNightBackgroundAudioPlan reads the bed from rec's own pinned
// night.session revision, so it never reports another revision's items.
func mapNightBackgroundAudioPlan(ctx context.Context, deps Dependencies, rec store.NightSessionRecord) v1.NightBackgroundAudioPlan {
	if rec.ID == "" || rec.ConfigObjectID == "" {
		return unknownNightAudioPlan("no session")
	}
	payload, err := nightPinnedNightSessionPayload(ctx, deps, rec)
	if err != nil {
		return unknownNightAudioPlan("failed to read the pinned night.session revision: " + err.Error())
	}
	ba := payload.Resting.BackgroundAudio
	plan := v1.NightBackgroundAudioPlan{
		State: v1.NightEvidenceRecorded, Nodes: []string{}, Items: []v1.NightBackgroundAudioPlanItem{},
		Reason: "background audio is not configured on this night session",
	}
	if ba == nil {
		return plan
	}
	plan.Reason = ""
	plan.Configured = true
	plan.MediaPlaylist = ba.MediaPlaylist
	resolved := ba
	if ba.MediaPlaylist != "" {
		mp, _, ok := nightResolveMediaPlaylist(ctx, deps, ba.MediaPlaylist)
		if !ok {
			unknown := unknownNightAudioPlan(fmt.Sprintf("media playlist %q is missing or has been deleted. Restore it or choose another in the night definition.", ba.MediaPlaylist))
			unknown.Configured, unknown.MediaPlaylist = true, ba.MediaPlaylist
			return unknown
		}
		resolved = nightMediaPlaylistBackgroundAudio(ba.MediaPlaylist, mp, ba.Targets, ba.ExcludeNodes)
	}
	plan.Repeat, plan.Resume, plan.ItemTransition, plan.CrossfadeMs = resolved.Repeat, resolved.Resume, resolved.ItemTransition, resolved.CrossfadeMs
	nodes, _ := resolved.ResolvedPlaybackNodeIDs((&handlers{deps: deps}).showAudioNodes(ctx)(payload.Show))
	plan.Nodes = append(plan.Nodes, nodes...)
	for i, item := range resolved.Items {
		plan.Items = append(plan.Items, v1.NightBackgroundAudioPlanItem{
			Position: i + 1, ItemID: item.ItemID, Show: item.Asset.Show, Sequence: item.Asset.Sequence, Target: item.Asset.Target,
		})
	}
	return plan
}

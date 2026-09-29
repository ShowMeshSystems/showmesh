package api

import (
	"context"
	"reflect"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func TestMapNightBackgroundAudioPlan_ConfiguredInlineListsItemsBeforeAnyStep(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStatePreparing)

	got := mapNightBackgroundAudio(context.Background(), h.deps, rec, testNow, true)
	if len(got.Steps) != 0 {
		t.Fatalf("steps = %d, want none before the bed starts", len(got.Steps))
	}
	state := mapNightSessionState(context.Background(), h.deps, rec, testNow, 0, true)
	plan := state.BackgroundAudio.Plan
	if !plan.Configured || plan.MediaPlaylist != "" || plan.Reason != "" {
		t.Fatalf("plan = %+v, want configured inline with no reason", plan)
	}
	if plan.Repeat != "playlist" || plan.Resume != "resume" || plan.ItemTransition != "sequential" {
		t.Fatalf("plan settings = %q/%q/%q", plan.Repeat, plan.Resume, plan.ItemTransition)
	}
	if !reflect.DeepEqual(plan.Nodes, []string{"node-a"}) {
		t.Fatalf("nodes = %v, want [node-a]", plan.Nodes)
	}
	if len(plan.Items) != 2 || plan.Items[0].Position != 1 || plan.Items[0].Sequence != "bg-1" || plan.Items[0].Target != "node-a" ||
		plan.Items[1].Position != 2 || plan.Items[1].Sequence != "bg-2" || plan.Items[1].ItemID != "track-2" {
		t.Fatalf("items = %+v, want bg-1 then bg-2 on node-a", plan.Items)
	}
}

func TestMapNightBackgroundAudioPlan_NotConfiguredIsDistinct(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", nil, nightStatePreparing)

	plan := mapNightBackgroundAudioPlan(context.Background(), h.deps, rec)
	if plan.State != "recorded" || plan.Configured || len(plan.Items) != 0 || plan.Reason == "" {
		t.Fatalf("plan = %+v, want recorded, not configured, no items, with a reason", plan)
	}
	if plan.Items == nil || plan.Nodes == nil {
		t.Fatalf("items and nodes must serialize as empty lists, got %+v", plan)
	}
}

func TestMapNightBackgroundAudioPlan_ReadsPinnedRevisionNotNewer(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	newer := twoItemBackgroundAudioConfig("node-b", config.NightSessionBackgroundRepeatNone, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	newer.Items = newer.Items[:1]
	payload := config.NightSessionPayload{
		Show: "halloween", Label: "Halloween main loop",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "fpp-main", Playlist: "halloween-show"},
		Resting: config.NightSessionResting{
			FPPInstanceID: "fpp-main", Playlist: "halloween-resting",
			TimelineAsset:   config.NightSessionAssetRef{Show: "halloween", Sequence: "resting-loop", Target: "fpp-main"},
			BackgroundAudio: newer,
		},
		AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDefault,
	}
	raw, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 2, PayloadJSON: raw,
		CreatedByPrincipalID: "test", CreatedByPrincipalName: "test", Source: "api",
	}); err != nil {
		t.Fatalf("create revision 2: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.NightSessionConfigKind, "halloween-main", 2); err != nil {
		t.Fatalf("activate revision 2: %v", err)
	}

	plan := mapNightBackgroundAudioPlan(ctx, h.deps, rec)
	if len(plan.Items) != 2 || plan.Items[0].Target != "node-a" || plan.Repeat != "playlist" {
		t.Fatalf("plan = %+v, want the pinned revision 1 bed, not revision 2", plan)
	}
}

func TestMapNightBackgroundAudioPlan_MediaPlaylistIsNamed(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	mustCreateMediaPlaylist(t, st, "porch-bed", twoItemMediaPlaylistPayload("halloween", "node-a", "item", "resume", "sequential"))
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", &config.NightSessionBackgroundAudio{MediaPlaylist: "porch-bed"}, nightStatePreparing)

	plan := mapNightBackgroundAudioPlan(context.Background(), h.deps, rec)
	if !plan.Configured || plan.MediaPlaylist != "porch-bed" || plan.Repeat != "item" || len(plan.Items) != 2 {
		t.Fatalf("plan = %+v, want the porch-bed playlist's two items and settings", plan)
	}
}

func TestMapNightBackgroundAudioPlan_MissingMediaPlaylistStatesWhy(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", &config.NightSessionBackgroundAudio{MediaPlaylist: "gone"}, nightStatePreparing)

	plan := mapNightBackgroundAudioPlan(context.Background(), h.deps, rec)
	if plan.State != "unknown" || !plan.Configured || plan.MediaPlaylist != "gone" || plan.Reason == "" {
		t.Fatalf("plan = %+v, want unknown, configured, naming the playlist, with a reason", plan)
	}
}

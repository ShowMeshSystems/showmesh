package api

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// containsAllSubstrings reports whether s contains every one of substrs,
// for asserting a readiness check's reason names multiple expected IDs
// without depending on showobjects_test.go's own two-argument
// containsAll.
func containsAllSubstrings(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// nodeViewWithGenericCapabilities builds an [inventory.NodeView] advertising
// exactly the given capability.IDs at Version 1 — the SM-201 granular
// audio.playback/audio.mix/audio.transition IDs this file's own tests
// exercise, distinct from audionode_test.go's own
// nodeViewWithAudioCapabilities (which carries only the two route-bearing
// audio.output.* IDs placement validation reads).
func nodeViewWithGenericCapabilities(nodeID string, ids ...capability.ID) inventory.NodeView {
	caps := make(capability.Set, 0, len(ids))
	for _, id := range ids {
		caps = append(caps, capability.Capability{ID: id, Version: 1})
	}
	return inventory.NodeView{NodeID: nodeID, Hello: &store.HelloRecord{Capabilities: caps}}
}

// TestNightCheckBackgroundAudioAssets_MissingAssetFails proves a
// configured item with no current asset reports failed, not a silent
// pass.
func TestNightCheckBackgroundAudioAssets_MissingAssetFails(t *testing.T) {
	h, _, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)

	check := h.nightCheckBackgroundAudioAssets(context.Background(), "halloween", ba)

	if check.health == nightHealthHealthy() {
		t.Fatalf("check = %+v, want failed (no assets were ever registered)", check)
	}
}

// TestNightCheckBackgroundAudioAssets_PresentAssetsPass proves the
// positive case is reachable, not merely always-failed.
func TestNightCheckBackgroundAudioAssets_PresentAssetsPass(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)

	check := h.nightCheckBackgroundAudioAssets(context.Background(), "halloween", ba)

	if check.health != nightHealthHealthy() {
		t.Fatalf("check = %+v, want healthy", check)
	}
}

// TestNightCheckBackgroundAudioItemTransition_SequentialAlwaysPasses
// proves sequential needs no output evidence at all: no node is ever
// registered in inventory, and the check still reports healthy.
func TestNightCheckBackgroundAudioItemTransition_SequentialAlwaysPasses(t *testing.T) {
	h, _, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := &config.NightSessionBackgroundAudio{ItemTransition: config.NightSessionItemTransitionSequential}

	check := h.nightCheckBackgroundAudioItemTransition(context.Background(), testNow, ba)

	if check.health != nightHealthHealthy() {
		t.Fatalf("check = %+v, want healthy", check)
	}
}

// TestNightCheckBackgroundAudioItemTransition_GaplessCrossfade is SM-201's
// own defended rule: gapless/crossfade fail when the configured output
// node has never declared the matching audio.transition.* capability, and
// pass once it has - proving the check reads real evidence in both
// directions, not a hardcoded refusal.
func TestNightCheckBackgroundAudioItemTransition_GaplessCrossfade(t *testing.T) {
	cases := []struct {
		transition   string
		confirmingID capability.ID
	}{
		{config.NightSessionItemTransitionGapless, "audio.transition.gapless"},
		{config.NightSessionItemTransitionCrossfade, "audio.transition.crossfade"},
	}
	for _, tc := range cases {
		t.Run(tc.transition, func(t *testing.T) {
			h, _, _, _ := nightBackgroundAudioTestHandlers(t)
			ba := &config.NightSessionBackgroundAudio{
				Items:          []config.NightSessionBackgroundAudioItem{{Asset: config.NightSessionAssetRef{Target: "node-a"}}},
				ItemTransition: tc.transition,
			}

			check := h.nightCheckBackgroundAudioItemTransition(context.Background(), testNow, ba)
			if check.health != nightHealthFailed() {
				t.Fatalf("undeclared output: check = %+v, want failed", check)
			}

			h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
				nodeViewWithGenericCapabilities("node-a", tc.confirmingID),
			})
			check = h.nightCheckBackgroundAudioItemTransition(context.Background(), testNow, ba)
			if check.health != nightHealthHealthy() {
				t.Fatalf("output declaring %q: check = %+v, want healthy", tc.confirmingID, check)
			}
		})
	}
}

// TestNightCheckAudioOutputCapabilities defends SM-201's real check:
// failed naming the missing IDs when the output node declares none of
// them, healthy once it declares every one this configured session
// requires (background/playlist/gain always, loop only because Repeat is
// configured here).
func TestNightCheckAudioOutputCapabilities(t *testing.T) {
	h, _, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := &config.NightSessionBackgroundAudio{
		Items:  []config.NightSessionBackgroundAudioItem{{Asset: config.NightSessionAssetRef{Target: "node-a"}}},
		Repeat: config.NightSessionBackgroundRepeatPlaylist,
	}

	check := h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
	if check.health != nightHealthFailed() {
		t.Fatalf("no capability advertisement: check = %+v, want failed", check)
	}
	if !containsAllSubstrings(check.reason, "audio.playback.background", "audio.playback.playlist", "audio.playback.gain", "audio.playback.loop") {
		t.Fatalf("reason does not name every missing capability; reason: %s", check.reason)
	}

	h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithGenericCapabilities("node-a", "audio.playback.background", "audio.playback.playlist", "audio.playback.gain"),
	})
	check = h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
	if check.health != nightHealthFailed() {
		t.Fatalf("missing only loop: check = %+v, want failed", check)
	}
	if !containsAllSubstrings(check.reason, "audio.playback.loop") {
		t.Fatalf("reason does not name the still-missing loop capability; reason: %s", check.reason)
	}

	h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithGenericCapabilities("node-a", "audio.playback.background", "audio.playback.playlist", "audio.playback.gain", "audio.playback.loop"),
	})
	check = h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
	if check.health != nightHealthHealthy() {
		t.Fatalf("every required capability declared: check = %+v, want healthy", check)
	}
}

// TestNightCheckAudioOutputCapabilities_NoRepeatDoesNotRequireLoop proves
// audio.playback.loop is only demanded when the configured repeat mode
// actually asks for one, not unconditionally.
func TestNightCheckAudioOutputCapabilities_NoRepeatDoesNotRequireLoop(t *testing.T) {
	h, _, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := &config.NightSessionBackgroundAudio{
		Items:  []config.NightSessionBackgroundAudioItem{{Asset: config.NightSessionAssetRef{Target: "node-a"}}},
		Repeat: config.NightSessionBackgroundRepeatNone,
	}
	h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithGenericCapabilities("node-a", "audio.playback.background", "audio.playback.playlist", "audio.playback.gain"),
	})

	check := h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
	if check.health != nightHealthHealthy() {
		t.Fatalf("check = %+v, want healthy (repeat=none never requires loop)", check)
	}
}

// TestNightCheckAnnouncementAssets_NeverInventsAPass mirrors the same
// rule for announcement content: not_verifiable when one IS configured
// (this coordinator holds no evidence for it), not_configured when none
// is (LOW 14: absent OPTIONAL configuration is a different fact from a
// structurally unverifiable check).
func TestNightCheckAnnouncementAssets_NeverInventsAPass(t *testing.T) {
	withAnnouncement := []config.NightSessionCue{{Name: "thanks", Role: config.NightSessionCueRoleAnnouncement}}
	check := nightCheckAnnouncementAssets(withAnnouncement)
	if check.health != nightCheckStateNotVerifiable {
		t.Fatalf("with an announcement cue: health = %v, want not_verifiable", check.health)
	}

	withoutAnnouncement := []config.NightSessionCue{{Name: "lights", Role: config.NightSessionCueRoleLighting}}
	check = nightCheckAnnouncementAssets(withoutAnnouncement)
	if check.health != nightCheckStateNotConfigured {
		t.Fatalf("with no announcement cue: health = %v, want not_configured", check.health)
	}
}

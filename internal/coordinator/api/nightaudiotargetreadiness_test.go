package api

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// The show lists old-node as its audio node after the interface moved to
// new-node, whose audio settings are the only ones left. Every node
// reported healthy and the show played silent.
const (
	movedAudioShow    = "halloween"
	movedAudioOldNode = "old-node"
	movedAudioNewNode = "new-node"
)

func seedMovedAudioNode(t *testing.T, st *store.Store) {
	t.Helper()
	putConfigForTest(t, st, config.ShowConfigKind, movedAudioShow, `{"name":"halloween","notes":"","audioNodes":["`+movedAudioOldNode+`"]}`)
	putAudioNodeForTest(t, st, movedAudioNewNode)
}

func findNightCheck(checks []nightReadinessCheck, name string) (nightReadinessCheck, bool) {
	for _, c := range checks {
		if c.name == name {
			return c, true
		}
	}
	return nightReadinessCheck{}, false
}

func TestNightBackgroundAudioReadiness_ShowAudioNodeWithoutSettingsFails(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	seedMovedAudioNode(t, st)
	putBackgroundAudioAsset(t, st, movedAudioShow, "bg-1", movedAudioNewNode, "asset-1")
	putBackgroundAudioAsset(t, st, movedAudioShow, "bg-2", movedAudioNewNode, "asset-2")
	ba := twoItemBackgroundAudioConfig(movedAudioNewNode, config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)

	checks := h.nightCheckBackgroundAudioReadiness(context.Background(), testNow, movedAudioShow, ba)

	setup, ok := findNightCheck(checks, nightBackgroundAudioNodeSetupPrefix+movedAudioOldNode)
	if !ok || setup.health != nightHealthFailed() {
		t.Fatalf("node setup check = %+v (found %v), want failed; checks = %+v", setup, ok, checks)
	}
	if !containsAllSubstrings(setup.reason, movedAudioOldNode, "no audio settings", "show's audio nodes") {
		t.Fatalf("reason = %q, want it to name the node and point at the show's audio nodes", setup.reason)
	}
	items, ok := findNightCheck(checks, nightBackgroundAudioItemTargetsName)
	if !ok || items.health != nightHealthDegraded() {
		t.Fatalf("item targets check = %+v (found %v), want degraded", items, ok)
	}
	if !containsAllSubstrings(strings.Join(items.reasons, " "), movedAudioNewNode, movedAudioOldNode) {
		t.Fatalf("reasons = %q, want both the named node and the node it plays on", items.reasons)
	}
	if got := nightOutcomeFromChecks(checks); got != "not_ready" {
		t.Fatalf("outcome = %q, want not_ready", got)
	}
}

func TestNightBackgroundAudioReadiness_ConfiguredNodePasses(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putAudioNodeForTest(t, st, "node-a")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)

	checks := h.nightCheckBackgroundAudioReadiness(context.Background(), testNow, "halloween", ba)

	setup, ok := findNightCheck(checks, nightBackgroundAudioNodeSetupPrefix+"node-a")
	if !ok || setup.health != nightHealthHealthy() {
		t.Fatalf("node setup check = %+v (found %v), want healthy", setup, ok)
	}
	if _, ok := findNightCheck(checks, nightBackgroundAudioItemTargetsName); ok {
		t.Fatalf("checks = %+v, want no item target check when the show names no audio nodes", checks)
	}
}

func TestNightBackgroundAudioNodeSetup_LostProgramInterfaceFails(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putAudioNodeForTest(t, st, "node-a")
	h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{{
		NodeID:   "node-a",
		Liveness: inventory.LivenessOnline,
		Hello: &store.HelloRecord{Capabilities: capability.Set{{
			ID: "audio.output.local", Version: 1,
			Attributes: map[string]any{"missingRoutes": []any{map[string]any{"route": "usb-interface"}}},
		}}},
	}})

	check := h.nightCheckBackgroundAudioNodeSetup(context.Background(), testNow, "node-a")

	if check.health != nightHealthFailed() || !containsAllSubstrings(check.reason, "node-a", "usb-interface") {
		t.Fatalf("check = %+v, want failed naming the node and its missing interface", check)
	}
}

func seedMovedAudioShowCue(t *testing.T, st *store.Store) {
	t.Helper()
	seedMovedAudioNode(t, st)
	putConfigForTest(t, st, config.ShowCueConfigKind, "time-warp",
		`{"show":"`+movedAudioShow+`","name":"Time Warp","outputs":{"audio":{"asset":"time-warp-audio","startOffsetMillis":0},"ltc":{"startOffsetMillis":0}}}`)
	putConfigForTest(t, st, config.ShowPlaylistConfigKind, "main",
		`{"show":"`+movedAudioShow+`","name":"main","runner":"fpp","mismatchPolicy":"hold","fpp":{"instanceUuid":"11111111-1111-1111-1111-111111111111","playlistName":"main","playlistHash":"`+playlistHash64+`"},"entries":[{"id":"e1","cue":"time-warp","fpp":{"section":"mainPlaylist","position":0}}]}`)
}

func TestNightShowCueAudioTargets_ShowAudioNodeWithoutSettingsFails(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	seedMovedAudioShowCue(t, st)

	checks := h.nightCheckShowCueAudioTargets(context.Background(), movedAudioShow)

	check, ok := findNightCheck(checks, nightShowCueAudioTargetsPrefix+"main")
	if !ok || check.health != nightHealthFailed() {
		t.Fatalf("check = %+v (found %v), want failed; checks = %+v", check, ok, checks)
	}
	if !containsAllSubstrings(check.reason, "time-warp", movedAudioOldNode, "show's audio nodes") {
		t.Fatalf("reason = %q, want the cue, the node, and the show's audio nodes", check.reason)
	}
}

func TestNightCatalogCurrent_TimecodeWithoutAudioFails(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	seedMovedAudioShowCue(t, st)
	putConfigForTest(t, st, config.ShowActiveConfigKind, "active", `{"show":"`+movedAudioShow+`"}`)
	mustDeclareNode(t, st, movedAudioOldNode)
	mustDeclareNode(t, st, movedAudioNewNode)

	checks := h.nightCheckCatalogCurrent(context.Background(), testNow, movedAudioShow)

	check, ok := findNightCheck(checks, nightCatalogCurrentCheckPrefix+":"+movedAudioNewNode)
	if !ok || check.health != nightHealthFailed() {
		t.Fatalf("check = %+v (found %v), want failed; checks = %+v", check, ok, checks)
	}
	if !containsAllSubstrings(check.reason, movedAudioNewNode, "timecode but no audio", "time-warp") {
		t.Fatalf("reason = %q, want the node, the cue, and the refusal", check.reason)
	}
}

func TestNightCatalogLTCWithoutAudio(t *testing.T) {
	entries := []cuecatalog.Entry{
		{CueID: "both", Outputs: cuecatalog.Outputs{Audio: &cuecatalog.AudioOutput{}, LTC: &cuecatalog.LTCOutput{}}},
		{CueID: "ltc-only", Outputs: cuecatalog.Outputs{LTC: &cuecatalog.LTCOutput{}}},
		{CueID: "render-only", Outputs: cuecatalog.Outputs{Render: &cuecatalog.RenderOutput{}}},
	}
	got := nightCatalogLTCWithoutAudio(entries)
	if len(got) != 1 || got[0] != "ltc-only" {
		t.Fatalf("nightCatalogLTCWithoutAudio = %v, want [ltc-only]", got)
	}
}

func TestNightShowCueStartTriggers_WarnsUntilFPPPlaylistNamesTheSequence(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putAudioNodeForTest(t, st, "node-a")
	mustDeclareNode(t, st, "node-a")
	putConfigForTest(t, st, config.ShowCueConfigKind, "time-warp",
		`{"show":"`+movedAudioShow+`","name":"Time Warp","outputs":{"audio":{"asset":"time-warp-audio","startOffsetMillis":0}}}`)
	putConfigForTest(t, st, config.ShowPlaylistConfigKind, "main",
		`{"show":"`+movedAudioShow+`","name":"main","runner":"fpp","mismatchPolicy":"hold","fpp":{"instanceUuid":"11111111-1111-1111-1111-111111111111","playlistName":"main","playlistHash":"`+playlistHash64+`"},"entries":[{"id":"e1","cue":"time-warp","fpp":{"section":"mainPlaylist","position":0}}]}`)
	putConfigForTest(t, st, config.ShowActiveConfigKind, "active", `{"show":"`+movedAudioShow+`"}`)

	checks := h.nightCheckShowCueStartTriggers(context.Background(), movedAudioShow)
	check, ok := findNightCheck(checks, nightShowCueStartTriggersPrefix+"main")
	if !ok || check.health != nightHealthDegraded() || !containsAllSubstrings(strings.Join(check.reasons, " "), "time-warp", "no FPP sequence") {
		t.Fatalf("check = %+v (found %v), want a warning naming time-warp; checks = %+v", check, ok, checks)
	}

	if _, err := st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: "11111111-1111-1111-1111-111111111111", PlaylistHash: playlistHash64, PlaylistName: "main",
		DefinitionJSON: `{"name":"main","mainPlaylist":[{"type":"sequence","sequenceName":"Time Warp 2026.fseq"}]}`,
		CapturedAt:     testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("PutFPPPlaylistDefinition: %v", err)
	}

	checks = h.nightCheckShowCueStartTriggers(context.Background(), movedAudioShow)
	check, ok = findNightCheck(checks, nightShowCueStartTriggersPrefix+"main")
	if !ok || check.health != nightHealthHealthy() {
		t.Fatalf("check = %+v (found %v), want healthy once FPP's playlist names the sequence", check, ok)
	}
}

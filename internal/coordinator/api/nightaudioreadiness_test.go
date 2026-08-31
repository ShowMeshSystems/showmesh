package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/nodeaudio"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
	"github.com/showmeshsystems/showmesh/pkg/observation"
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
// exactly the given capability.IDs at Version 1, live (Liveness online),
// the granular audio.playback/audio.mix/audio.transition IDs this file's
// own tests exercise, distinct from audionode_test.go's own
// nodeViewWithAudioCapabilities (which carries only the two route-bearing
// audio.output.* IDs placement validation reads). Use
// nodeViewWithGenericCapabilitiesOffline for a node whose retained hello
// must NOT be trusted as current.
func nodeViewWithGenericCapabilities(nodeID string, ids ...capability.ID) inventory.NodeView {
	caps := make(capability.Set, 0, len(ids))
	for _, id := range ids {
		caps = append(caps, capability.Capability{ID: id, Version: 1})
	}
	return inventory.NodeView{NodeID: nodeID, Hello: &store.HelloRecord{Capabilities: caps}, Liveness: inventory.LivenessOnline}
}

// nodeViewWithGenericCapabilitiesOffline is
// [nodeViewWithGenericCapabilities] with Liveness LivenessOffline
// instead: a node that DID once advertise the given capabilities, but
// whose retained hello this coordinator can no longer confirm is
// current, per [audioNodeCapabilityEvidence]'s own doc comment.
func nodeViewWithGenericCapabilitiesOffline(nodeID string, ids ...capability.ID) inventory.NodeView {
	nv := nodeViewWithGenericCapabilities(nodeID, ids...)
	nv.Liveness = inventory.LivenessOffline
	nv.LivenessReason = "last-will evidence reports offline"
	return nv
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

// TestNightCheckBackgroundAudioItemTransition_GaplessCrossfade defends
// the rule across every reachable state, proving the check reads real
// evidence rather than a hardcoded refusal: not_verifiable when this
// coordinator has no evidence for the output at all (it has never
// appeared in inventory - this is a NO CLAIM state, distinct from a live
// node's own negative advertisement, and stays excluded from the
// aggregate outcome exactly as it did before this build ever had a
// capability signal to check), failed when a LIVE node's own
// advertisement genuinely omits the matching audio.transition.* ID, and
// healthy once it declares one. Also proves a node whose retained hello
// DOES name the ability but whose liveness is not currently online
// reports unknown, never healthy: stale evidence is not current
// evidence, and not_verifiable is reserved for "never published,"
// distinct from "published but not trustworthy right now."
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
			if check.health != nightCheckStateNotVerifiable {
				t.Fatalf("output never seen in inventory: check = %+v, want not_verifiable", check)
			}

			h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
				nodeViewWithGenericCapabilities("node-a"),
			})
			check = h.nightCheckBackgroundAudioItemTransition(context.Background(), testNow, ba)
			if check.health != nightHealthFailed() {
				t.Fatalf("live output declaring nothing: check = %+v, want failed", check)
			}

			h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
				nodeViewWithGenericCapabilitiesOffline("node-a", tc.confirmingID),
			})
			check = h.nightCheckBackgroundAudioItemTransition(context.Background(), testNow, ba)
			if check.health != nightHealthUnknown() {
				t.Fatalf("offline output whose retained hello DOES declare %q: check = %+v, want unknown (stale evidence must not read as healthy or failed)", tc.confirmingID, check)
			}

			h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
				nodeViewWithGenericCapabilities("node-a", tc.confirmingID),
			})
			check = h.nightCheckBackgroundAudioItemTransition(context.Background(), testNow, ba)
			if check.health != nightHealthHealthy() {
				t.Fatalf("live output declaring %q: check = %+v, want healthy", tc.confirmingID, check)
			}
		})
	}
}

// TestNightCheckAudioOutputCapabilities defends this check's real
// behavior: not_verifiable when this coordinator has never seen the
// output node at all (a no-claim state, excluded from the aggregate
// outcome, never "failed" - missing evidence is never a negative
// claim), failed naming the missing IDs once a LIVE node's own
// advertisement is evaluated and genuinely omits some of them, and
// healthy once it declares every one this configured session requires
// (background/playlist/gain always, loop only because Repeat is
// configured here). Also proves a node that WOULD satisfy every
// requirement but is not currently online still reports unknown, not
// healthy.
func TestNightCheckAudioOutputCapabilities(t *testing.T) {
	h, _, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := &config.NightSessionBackgroundAudio{
		Items:  []config.NightSessionBackgroundAudioItem{{Asset: config.NightSessionAssetRef{Target: "node-a"}}},
		Repeat: config.NightSessionBackgroundRepeatPlaylist,
	}

	check := h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
	if check.health != nightCheckStateNotVerifiable {
		t.Fatalf("node never seen in inventory: check = %+v, want not_verifiable", check)
	}

	h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithGenericCapabilities("node-a"),
	})
	// Every required ID is missing here, so this needs
	// node.audio.engine.state to POSITIVELY confirm the engine
	// unavailable to legitimately reach failed rather than unknown (a
	// "declares nothing yet" node is only distinguishable from "still
	// probing" by that independent evidence) - see
	// TestNightCheckAudioOutputCapabilities_StillProbingReadsUnknown for
	// that distinction proven directly.
	h.deps.Audio.(*fakeNodeAudioLister).setObservations("node-a", []observation.Observation{
		nodeAudioEngineStateObservation("node-a", nodeaudio.StateUnavailable, testNow),
	})
	check = h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
	if check.health != nightHealthFailed() {
		t.Fatalf("live node declaring nothing, engine confirmed unavailable: check = %+v, want failed", check)
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
		nodeViewWithGenericCapabilitiesOffline("node-a", "audio.playback.background", "audio.playback.playlist", "audio.playback.gain", "audio.playback.loop"),
	})
	check = h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
	if check.health != nightHealthUnknown() {
		t.Fatalf("offline node whose retained hello declares every required capability: check = %+v, want unknown", check)
	}

	h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithGenericCapabilities("node-a", "audio.playback.background", "audio.playback.playlist", "audio.playback.gain", "audio.playback.loop"),
	})
	check = h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
	if check.health != nightHealthHealthy() {
		t.Fatalf("every required capability declared by a live node: check = %+v, want healthy", check)
	}
}

// TestNightCheckAudioOutputCapabilities_OldAgentNeverPublishedIsNotVerifiable
// is the mixed-fleet distinction directly: a node this coordinator KNOWS
// about (it is in inventory, e.g. from an MQTT hello envelope predating
// this capability signal, or from LWT/health evidence alone) but that
// has never published ANY Hello.Capabilities is an old agent making NO
// CLAIM about these abilities, not a live agent that claims to declare
// nothing. Collapsing the two into failed infers a claim the old node
// never made, the identical dishonesty this whole change exists to
// remove, aimed the other way; it must stay not_verifiable, excluded
// from the aggregate outcome, exactly like a node never seen at all.
func TestNightCheckAudioOutputCapabilities_OldAgentNeverPublishedIsNotVerifiable(t *testing.T) {
	h, _, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := &config.NightSessionBackgroundAudio{
		Items:  []config.NightSessionBackgroundAudioItem{{Asset: config.NightSessionAssetRef{Target: "node-a"}}},
		Repeat: config.NightSessionBackgroundRepeatPlaylist,
	}
	// In inventory (known to this coordinator, e.g. via LWT/health), but
	// Hello is nil: this node has never published a capability
	// advertisement at all, distinct from a Hello that exists and
	// declares an empty set.
	h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		{NodeID: "node-a", Liveness: inventory.LivenessOnline},
	})

	check := h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
	if check.health != nightCheckStateNotVerifiable {
		t.Fatalf("node in inventory with Hello == nil: check = %+v, want not_verifiable", check)
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

// nodeAudioEngineStateObservation builds one node.audio.engine.state
// observation.Observation reading value, current as of observedAt (no
// ValidFor set, matching a caller that wants StateAt(now) == StateCurrent
// for any now at or after observedAt).
func nodeAudioEngineStateObservation(nodeID, value string, observedAt time.Time) observation.Observation {
	return observation.Observation{
		Resource:   observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID},
		Signal:     nodeaudio.SignalEngineState,
		Value:      value,
		ObservedAt: &observedAt,
	}
}

// TestNightCheckAudioOutputCapabilities_StillProbingReadsUnknown proves
// finding C/4's fix: a live node whose Hello capability set is entirely
// empty reads unknown, not failed, in the two cases this coordinator
// cannot rule out "still probing": independent evidence
// (node.audio.engine.state, collected on the agent's own audioreport
// cycle, entirely separate from the Hello capability cycle) confirms the
// engine is usable right now, OR that independent evidence does not
// exist yet at all (a real node's first audioreport can land 60-90s
// after connect; evidence that cannot exist yet is not evidence of
// absence). Only when that independent evidence POSITIVELY AND
// CURRENTLY confirms the engine unavailable does the identical empty
// Hello capability set read failed: an empty set is only ever excused as
// "still probing" or read as a genuine failure when there is real
// corroborating evidence for that specific story, never merely because
// the set happens to be empty.
func TestNightCheckAudioOutputCapabilities_StillProbingReadsUnknown(t *testing.T) {
	ba := &config.NightSessionBackgroundAudio{
		Items:  []config.NightSessionBackgroundAudioItem{{Asset: config.NightSessionAssetRef{Target: "node-a"}}},
		Repeat: config.NightSessionBackgroundRepeatPlaylist,
	}

	t.Run("engine confirmed usable, hello empty: unknown", func(t *testing.T) {
		h, _, _, _ := nightBackgroundAudioTestHandlers(t)
		h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
			nodeViewWithGenericCapabilities("node-a"),
		})
		h.deps.Audio.(*fakeNodeAudioLister).setObservations("node-a", []observation.Observation{
			nodeAudioEngineStateObservation("node-a", nodeaudio.StateUsable, testNow),
		})

		check := h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
		if check.health != nightHealthUnknown() {
			t.Fatalf("engine confirmed usable, hello capabilities empty: check = %+v, want unknown", check)
		}
	})

	t.Run("engine confirmed unavailable, hello empty: failed", func(t *testing.T) {
		h, _, _, _ := nightBackgroundAudioTestHandlers(t)
		h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
			nodeViewWithGenericCapabilities("node-a"),
		})
		h.deps.Audio.(*fakeNodeAudioLister).setObservations("node-a", []observation.Observation{
			nodeAudioEngineStateObservation("node-a", nodeaudio.StateUnavailable, testNow),
		})

		check := h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
		if check.health != nightHealthFailed() {
			t.Fatalf("engine confirmed unavailable, hello capabilities empty: check = %+v, want failed (no probing evidence excuses this)", check)
		}
	})

	t.Run("no node.audio.engine.state evidence at all, hello empty: unknown", func(t *testing.T) {
		h, _, _, _ := nightBackgroundAudioTestHandlers(t)
		h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
			nodeViewWithGenericCapabilities("node-a"),
		})
		// fakeNodeAudioLister returns nil for a node with no observations
		// set at all - matches a coordinator that has never received an
		// audioreport for this node (a real node's first can land 60-90s
		// after connect). Evidence that cannot exist yet is not evidence
		// of absence, so this must read unknown, not failed.

		check := h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
		if check.health != nightHealthUnknown() {
			t.Fatalf("no independent engine-state evidence at all: check = %+v, want unknown", check)
		}
	})

	t.Run("stale node.audio.engine.state evidence, hello empty: unknown", func(t *testing.T) {
		h, _, _, _ := nightBackgroundAudioTestHandlers(t)
		h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
			nodeViewWithGenericCapabilities("node-a"),
		})
		stale := nodeAudioEngineStateObservation("node-a", nodeaudio.StateUnavailable, testNow.Add(-time.Hour))
		stale.ValidFor = 45 * time.Second // nodeaudio.DefaultValidFor
		h.deps.Audio.(*fakeNodeAudioLister).setObservations("node-a", []observation.Observation{stale})

		check := h.nightCheckAudioOutputCapabilities(context.Background(), testNow, ba)
		if check.health != nightHealthUnknown() {
			t.Fatalf("stale (aged past ValidFor) engine-state evidence, even reading unavailable: check = %+v, want unknown, not failed", check)
		}
	})
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

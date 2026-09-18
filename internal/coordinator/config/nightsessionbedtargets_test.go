package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// This file is Track F seam F1's own test suite for ADR-049 decision 7's
// resting.backgroundAudio.targets: the night bed's own list of audio.node
// ids, applying identically to the inline and reference forms. It follows
// showcue_test.go's own targets test pattern one kind over.

// twoTargetInlineBackgroundAudioJSON declares two items on two different
// registered nodes (audio-node-1, audio-node-2) and a bed-level targets
// list naming audio-node-1 and a THIRD node, audio-node-3, that neither
// item is registered for, exactly the shape PlaybackItemsFor's own test
// needs: a listed node with no item of its own still plays every item
// (the registered-copy rule, ADR-049 decision 7), and audio-node-2 (an
// item's own target, but not listed) plays nothing.
const twoTargetInlineBackgroundAudioJSON = `{
  "show": "halloween-2026",
  "label": "Halloween main loop",
  "showPlaylist": {"fppInstanceId": "fpp-main", "playlist": "halloween-show"},
  "resting": {
    "fppInstanceId": "fpp-main",
    "playlist": "halloween-resting",
    "timelineAsset": {"show": "halloween-2026", "sequence": "resting-loop", "target": "fpp-main"},
    "endOfNightRepeat": true,
    "backgroundAudio": {
      "items": [
        {"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"},
        {"itemId": "track-2", "show": "halloween-2026", "sequence": "bg-track-2", "target": "audio-node-2"}
      ],
      "repeat": "playlist",
      "resume": "resume",
      "itemTransition": "sequential",
      "maxGainDb": -10,
      "targets": ["audio-node-1", "audio-node-3"]
    }
  },
  "enterShow": {"cues": [], "blackoutHoldMs": 0},
  "enterResting": {"cues": [], "blackoutAfterShowMs": 0}
}`

func decodeTwoTargetInlineBackgroundAudio(t *testing.T) NightSessionPayload {
	t.Helper()
	p, verr := DecodeNightSessionPayload(twoTargetInlineBackgroundAudioJSON, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr != nil {
		t.Fatalf("unexpected error decoding a valid two-target inline bed: %+v", verr)
	}
	return p
}

// --- Round trip: inline and reference forms. ---

func TestDecodeNightSessionPayloadBedTargetsInline(t *testing.T) {
	p := decodeTwoTargetInlineBackgroundAudio(t)
	ba := p.Resting.BackgroundAudio
	if ba == nil {
		t.Fatal("expected a decoded backgroundAudio")
	}
	if !reflect.DeepEqual(ba.Targets, []string{"audio-node-1", "audio-node-3"}) {
		t.Fatalf("targets = %+v, want [audio-node-1 audio-node-3]", ba.Targets)
	}
}

func TestEncodeNightSessionPayloadRoundTripsBedTargetsInline(t *testing.T) {
	p := decodeTwoTargetInlineBackgroundAudio(t)
	raw, err := EncodeNightSessionPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(raw, `"targets":["audio-node-1","audio-node-3"]`) {
		t.Fatalf("expected targets on the wire, got: %s", raw)
	}
	back, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr != nil {
		t.Fatalf("re-decode of the API's own encoded output must not fail: %+v (encoded: %s)", verr, raw)
	}
	if !reflect.DeepEqual(back.Resting.BackgroundAudio.Targets, []string{"audio-node-1", "audio-node-3"}) {
		t.Fatalf("targets did not round trip: %+v", back.Resting.BackgroundAudio.Targets)
	}
}

func nightSessionJSONWithMediaPlaylistRefAndTargets(id string) string {
	return strings.Replace(nightSessionJSONWithMediaPlaylistRef(id), `"mediaPlaylist": "`+id+`"`,
		`"mediaPlaylist": "`+id+`", "targets": ["audio-node-1", "audio-node-3"]`, 1)
}

func TestEncodeNightSessionPayloadRoundTripsBedTargetsReference(t *testing.T) {
	raw := nightSessionJSONWithMediaPlaylistRefAndTargets("planetary-bed")
	p, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr != nil {
		t.Fatalf("unexpected error decoding a valid two-target reference bed: %+v", verr)
	}
	if !reflect.DeepEqual(p.Resting.BackgroundAudio.Targets, []string{"audio-node-1", "audio-node-3"}) {
		t.Fatalf("targets = %+v, want [audio-node-1 audio-node-3]", p.Resting.BackgroundAudio.Targets)
	}

	encoded, err := EncodeNightSessionPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(encoded, `"targets":["audio-node-1","audio-node-3"]`) {
		t.Fatalf("expected targets on the wire alongside mediaPlaylist, got: %s", encoded)
	}
	back, verr := DecodeNightSessionPayload(encoded, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr != nil {
		t.Fatalf("re-decode of the API's own encoded output must not fail: %+v (encoded: %s)", verr, encoded)
	}
	if !reflect.DeepEqual(back.Resting.BackgroundAudio.Targets, []string{"audio-node-1", "audio-node-3"}) {
		t.Fatalf("targets did not round trip: %+v", back.Resting.BackgroundAudio.Targets)
	}
}

// --- Absent / empty targets: unchanged, existing behavior. ---

func TestDecodeNightSessionPayloadBedTargetsAbsentMatchesLegacyAccessors(t *testing.T) {
	p := decodeValidNightSession(t)
	ba := p.Resting.BackgroundAudio
	if ba.HasDeclaredTargets() {
		t.Fatalf("expected HasDeclaredTargets() false on an absent targets list, got true (Targets=%+v)", ba.Targets)
	}
	if !reflect.DeepEqual(ba.PlaybackNodeIDs(), ba.OutputNodeIDs()) {
		t.Fatalf("PlaybackNodeIDs() = %+v, want OutputNodeIDs() = %+v", ba.PlaybackNodeIDs(), ba.OutputNodeIDs())
	}
	for _, nodeID := range ba.OutputNodeIDs() {
		if !reflect.DeepEqual(ba.PlaybackItemsFor(nodeID), ba.ItemsForTarget(nodeID)) {
			t.Fatalf("PlaybackItemsFor(%q) = %+v, want ItemsForTarget(%q) = %+v", nodeID, ba.PlaybackItemsFor(nodeID), nodeID, ba.ItemsForTarget(nodeID))
		}
	}

	raw, err := EncodeNightSessionPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(raw, `"targets"`) {
		t.Fatalf("expected no targets key on the wire for an absent list, got: %s", raw)
	}
}

func TestDecodeNightSessionPayloadBedTargetsEmptyArrayMatchesAbsent(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"maxGainDb": -10`, `"maxGainDb": -10, "targets": []`, 1)
	p, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr != nil {
		t.Fatalf("unexpected error on an explicit empty targets array: %+v", verr)
	}
	ba := p.Resting.BackgroundAudio
	if ba.HasDeclaredTargets() {
		t.Fatalf("expected HasDeclaredTargets() false on an empty targets list, got true (Targets=%+v)", ba.Targets)
	}
	if !reflect.DeepEqual(ba.PlaybackNodeIDs(), ba.OutputNodeIDs()) {
		t.Fatalf("PlaybackNodeIDs() = %+v, want OutputNodeIDs() = %+v", ba.PlaybackNodeIDs(), ba.OutputNodeIDs())
	}

	encoded, err := EncodeNightSessionPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(encoded, `"targets"`) {
		t.Fatalf("expected an explicit [] to marshal without the targets key, got: %s", encoded)
	}
}

// --- PlaybackItemsFor / PlaybackNodeIDs with declared targets. ---

func TestPlaybackNodeIDsReturnsDeclaredTargets(t *testing.T) {
	p := decodeTwoTargetInlineBackgroundAudio(t)
	ba := p.Resting.BackgroundAudio
	if !ba.HasDeclaredTargets() {
		t.Fatal("expected HasDeclaredTargets() true")
	}
	if !reflect.DeepEqual(ba.PlaybackNodeIDs(), []string{"audio-node-1", "audio-node-3"}) {
		t.Fatalf("PlaybackNodeIDs() = %+v, want [audio-node-1 audio-node-3]", ba.PlaybackNodeIDs())
	}
}

func TestPlaybackItemsForListedNodeReturnsEveryItem(t *testing.T) {
	p := decodeTwoTargetInlineBackgroundAudio(t)
	ba := p.Resting.BackgroundAudio
	for _, nodeID := range []string{"audio-node-1", "audio-node-3"} {
		got := ba.PlaybackItemsFor(nodeID)
		if len(got) != 2 || got[0].ItemID != "track-1" || got[1].ItemID != "track-2" {
			t.Fatalf("PlaybackItemsFor(%q) = %+v, want both items in order", nodeID, got)
		}
	}
}

// TestPlaybackItemsForUnlistedNodeReturnsNothing proves an item's own
// Asset.Target no longer decides where it plays once targets is declared
// (ADR-049 decision 7): audio-node-2 is track-2's own registered node, but
// it is not in the bed's own targets list, so it gets nothing.
func TestPlaybackItemsForUnlistedNodeReturnsNothing(t *testing.T) {
	p := decodeTwoTargetInlineBackgroundAudio(t)
	ba := p.Resting.BackgroundAudio
	if got := ba.PlaybackItemsFor("audio-node-2"); got != nil {
		t.Fatalf("PlaybackItemsFor(%q) = %+v, want nothing for an unlisted node", "audio-node-2", got)
	}
}

// --- Write-time validation (mirrors decodeShowCueTargets, showcue_test.go). ---

func TestDecodeNightSessionPayloadBedTargetsUnknownNodeRefused(t *testing.T) {
	audioNodeExists := func(id string) bool { return id != "no-such-node" }
	raw := strings.Replace(twoTargetInlineBackgroundAudioJSON, `"audio-node-1", "audio-node-3"`, `"audio-node-1", "no-such-node"`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, audioNodeExists, nil)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "resting.backgroundAudio.targets[1]" {
		t.Fatalf("expected field-unknown-reference on resting.backgroundAudio.targets[1], got %+v", verr)
	}
	if !strings.Contains(verr.Detail, "no-such-node") {
		t.Fatalf("detail = %q, want it to name the offending id", verr.Detail)
	}
}

func TestDecodeNightSessionPayloadBedTargetsRepeatedIDRefused(t *testing.T) {
	raw := strings.Replace(twoTargetInlineBackgroundAudioJSON, `"audio-node-1", "audio-node-3"`, `"audio-node-1", "audio-node-1"`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr == nil || verr.Code != ValidationCodeNightBackgroundAudioTargetDuplicate || verr.Field != "resting.backgroundAudio.targets[1]" {
		t.Fatalf("expected night-background-audio-target-duplicate on resting.backgroundAudio.targets[1], got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadBedTargetsNullRefused(t *testing.T) {
	raw := strings.Replace(twoTargetInlineBackgroundAudioJSON, `"targets": ["audio-node-1", "audio-node-3"]`, `"targets": null`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "resting.backgroundAudio.targets" {
		t.Fatalf("expected field-null on resting.backgroundAudio.targets, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadBedTargetsNonArrayRefused(t *testing.T) {
	raw := strings.Replace(twoTargetInlineBackgroundAudioJSON, `"targets": ["audio-node-1", "audio-node-3"]`, `"targets": "audio-node-1"`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "resting.backgroundAudio.targets" {
		t.Fatalf("expected field-invalid on a non-array resting.backgroundAudio.targets, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadBedTargetsNonStringEntryRefused(t *testing.T) {
	raw := strings.Replace(twoTargetInlineBackgroundAudioJSON, `"audio-node-1", "audio-node-3"`, `"audio-node-1", 3`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "resting.backgroundAudio.targets" {
		t.Fatalf("expected field-invalid on a non-string entry in resting.backgroundAudio.targets, got %+v", verr)
	}
}

// --- Stored-config compatibility (mirrors decodeStoredShowCueTargets). ---

// TestNightSessionBackgroundAudioStoredDecodeSurvivesRemovedNode proves a
// stored revision naming an audio.node that has since been deleted still
// decodes: the plain, non-validating json.Unmarshal read-back path (the
// api package's jsonUnmarshalStrict) never calls audioNodeExists.
func TestNightSessionBackgroundAudioStoredDecodeSurvivesRemovedNode(t *testing.T) {
	raw := `{"resting":{"backgroundAudio":{
		"items":[{"itemId":"track-1","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],
		"repeat":"playlist","resume":"resume","itemTransition":"sequential","maxGainDb":-10,
		"targets":["long-gone-node"]
	}}}`
	var p NightSessionPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("stored-row decode must not fail: %v", err)
	}
	if p.Resting.BackgroundAudio == nil || !reflect.DeepEqual(p.Resting.BackgroundAudio.Targets, []string{"long-gone-node"}) {
		t.Fatalf("expected targets [long-gone-node] to decode as-is, got %+v", p.Resting.BackgroundAudio)
	}
}

// TestNightSessionBackgroundAudioStoredDecodeWithoutTargetsUnchanged proves
// an older stored revision (authored before this field existed) decodes
// unchanged: Targets is nil, HasDeclaredTargets() is false, and the legacy
// per-node accessors behave exactly as they always have.
func TestNightSessionBackgroundAudioStoredDecodeWithoutTargetsUnchanged(t *testing.T) {
	raw := `{"resting":{"backgroundAudio":{
		"items":[{"itemId":"track-1","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],
		"repeat":"playlist","resume":"resume","itemTransition":"sequential","maxGainDb":-10
	}}}`
	var p NightSessionPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("stored-row decode must not fail: %v", err)
	}
	ba := p.Resting.BackgroundAudio
	if ba == nil {
		t.Fatal("expected a decoded backgroundAudio")
	}
	if ba.Targets != nil {
		t.Fatalf("expected nil targets on an older stored revision, got %+v", ba.Targets)
	}
	if ba.HasDeclaredTargets() {
		t.Fatal("expected HasDeclaredTargets() false on an older stored revision")
	}
	if !reflect.DeepEqual(ba.OutputNodeIDs(), []string{"audio-node-1"}) {
		t.Fatalf("OutputNodeIDs() = %+v, want [audio-node-1]", ba.OutputNodeIDs())
	}
}

// --- ADR-049 decision 10: resting.backgroundAudio.excludeNodes. ---

func nightSessionJSONWithExcludeNodes(excludeNodes string) string {
	return strings.Replace(validNightSessionJSON, `"maxGainDb": -10`,
		`"maxGainDb": -10, "excludeNodes": `+excludeNodes, 1)
}

func TestDecodeNightSessionPayloadBedExcludeNodesValid(t *testing.T) {
	p, verr := DecodeNightSessionPayload(nightSessionJSONWithExcludeNodes(`["audio-node-3"]`), nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists,
		showAudioNodesFixture("audio-node-1", "audio-node-2", "audio-node-3"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if !reflect.DeepEqual(p.Resting.BackgroundAudio.ExcludeNodes, []string{"audio-node-3"}) {
		t.Fatalf("unexpected excludeNodes: %+v", p.Resting.BackgroundAudio.ExcludeNodes)
	}
	resolved, from := p.Resting.BackgroundAudio.ResolvedPlaybackNodeIDs([]string{"audio-node-1", "audio-node-2", "audio-node-3"})
	if from != AudioNodeResolutionShow || !reflect.DeepEqual(resolved, []string{"audio-node-1", "audio-node-2"}) {
		t.Fatalf("resolved = %+v from %q, want [audio-node-1 audio-node-2] from show", resolved, from)
	}
}

func TestDecodeNightSessionPayloadBedExcludeNodesWithTargetsRejected(t *testing.T) {
	raw := nightSessionJSONWithExcludeNodes(`["audio-node-3"]`)
	raw = strings.Replace(raw, `"maxGainDb": -10, "excludeNodes": ["audio-node-3"]`,
		`"maxGainDb": -10, "targets": ["audio-node-1"], "excludeNodes": ["audio-node-3"]`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists,
		showAudioNodesFixture("audio-node-1", "audio-node-3"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "resting.backgroundAudio.excludeNodes" {
		t.Fatalf("expected field-invalid on resting.backgroundAudio.excludeNodes, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadBedExcludeNodesNotInShowListRejected(t *testing.T) {
	_, verr := DecodeNightSessionPayload(nightSessionJSONWithExcludeNodes(`["ghost"]`), nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists,
		showAudioNodesFixture("audio-node-1", "audio-node-2"))
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "resting.backgroundAudio.excludeNodes[0]" {
		t.Fatalf("expected field-unknown-reference on resting.backgroundAudio.excludeNodes[0], got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadBedExcludeNodesAllRejected(t *testing.T) {
	_, verr := DecodeNightSessionPayload(nightSessionJSONWithExcludeNodes(`["audio-node-1", "audio-node-2"]`), nightSessionTestEndpoints,
		alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists,
		showAudioNodesFixture("audio-node-1", "audio-node-2"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "resting.backgroundAudio.excludeNodes" {
		t.Fatalf("expected field-invalid on resting.backgroundAudio.excludeNodes for excluding every node, got %+v", verr)
	}
}

// TestResolvedPlaybackNodeIDsFallsBackToOutputNodeIDsWithoutShowAudioNodes
// proves decision 10 step 3 for a bed: with no show.audioNodes, a bed with
// no Targets keeps today's per-item routing exactly as before.
func TestResolvedPlaybackNodeIDsFallsBackToOutputNodeIDsWithoutShowAudioNodes(t *testing.T) {
	p := decodeValidNightSession(t)
	ba := p.Resting.BackgroundAudio
	resolved, from := ba.ResolvedPlaybackNodeIDs(nil)
	if from != AudioNodeResolutionDefault || !reflect.DeepEqual(resolved, ba.OutputNodeIDs()) {
		t.Fatalf("resolved = %+v from %q, want OutputNodeIDs() from default", resolved, from)
	}
}

// TestResolvedPlaybackNodeIDsPrefersDeclaredTargetsOverShowAudioNodes
// proves decision 10 step 1: a bed's own Targets wins even when the show
// has audioNodes.
func TestResolvedPlaybackNodeIDsPrefersDeclaredTargetsOverShowAudioNodes(t *testing.T) {
	p := decodeTwoTargetInlineBackgroundAudio(t)
	ba := p.Resting.BackgroundAudio
	resolved, from := ba.ResolvedPlaybackNodeIDs([]string{"audio-node-9"})
	if from != AudioNodeResolutionExplicit || !reflect.DeepEqual(resolved, ba.Targets) {
		t.Fatalf("resolved = %+v from %q, want Targets from explicit", resolved, from)
	}
}

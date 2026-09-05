package config

import (
	"encoding/json"
	"testing"
)

func validMediaPlaylistJSON() string {
	return `{
		"show": "halloween-2026",
		"label": "Resting bed",
		"items": [
			{"kind": "asset", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"}
		],
		"repeat": "playlist",
		"resume": "resume",
		"itemTransition": "sequential",
		"maxGainDb": -10
	}`
}

func TestDecodeMediaPlaylistPayloadValid(t *testing.T) {
	p, verr := DecodeMediaPlaylistPayload(validMediaPlaylistJSON(), alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Show != "halloween-2026" || p.Label != "Resting bed" {
		t.Fatalf("unexpected payload: %+v", p)
	}
	if p.Repeat != NightSessionBackgroundRepeatPlaylist || p.Resume != NightSessionBackgroundResumeResume || p.ItemTransition != NightSessionItemTransitionSequential {
		t.Fatalf("unexpected payload: %+v", p)
	}
	if p.MaxGainDb != -10 {
		t.Fatalf("unexpected maxGainDb: %v", p.MaxGainDb)
	}
	if len(p.Items) != 1 || p.Items[0].Kind != MediaPlaylistItemKindAsset {
		t.Fatalf("unexpected items: %+v", p.Items)
	}
	if p.Items[0].Asset != (NightSessionAssetRef{Show: "halloween-2026", Sequence: "bg-track-1", Target: "audio-node-1"}) {
		t.Fatalf("unexpected item asset: %+v", p.Items[0].Asset)
	}
}

func TestEncodeMediaPlaylistPayloadRoundTrips(t *testing.T) {
	p, verr := DecodeMediaPlaylistPayload(validMediaPlaylistJSON(), alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	raw, err := EncodeMediaPlaylistPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if wire["label"] != "Resting bed" {
		t.Fatalf("unexpected encoded label: %+v", wire["label"])
	}
	items, ok := wire["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected encoded items: %+v", wire["items"])
	}
	item := items[0].(map[string]any)
	if item["kind"] != "asset" || item["show"] != "halloween-2026" || item["sequence"] != "bg-track-1" || item["target"] != "audio-node-1" {
		t.Fatalf("unexpected encoded item: %+v", item)
	}

	var back MediaPlaylistItem
	itemRaw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal item back to raw: %v", err)
	}
	if err := json.Unmarshal(itemRaw, &back); err != nil {
		t.Fatalf("unmarshal item: %v", err)
	}
	if back != p.Items[0] {
		t.Fatalf("item round trip = %+v, want %+v", back, p.Items[0])
	}
}

func TestDecodeMediaPlaylistPayloadUnknownShowRejected(t *testing.T) {
	_, verr := DecodeMediaPlaylistPayload(validMediaPlaylistJSON(), alwaysFalse, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "show" {
		t.Fatalf("expected field-unknown-reference on show, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadLabelRequired(t *testing.T) {
	raw := `{"show":"halloween-2026","items":[{"kind":"asset","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"sequential","maxGainDb":-10}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "label" {
		t.Fatalf("expected field-required on label, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadItemsEmptyRejected(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[],"resume":"resume","itemTransition":"sequential","maxGainDb":-10}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeBackgroundAudioItemsEmpty || verr.Field != "items" {
		t.Fatalf("expected background-audio-items-empty on items, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadItemKindCueNotImplemented(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"cue","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"sequential","maxGainDb":-10}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeNotImplemented || verr.Field != "items[0].kind" {
		t.Fatalf("expected not-implemented on items[0].kind for kind cue, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadItemKindUnknownRejected(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"video","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"sequential","maxGainDb":-10}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "items[0].kind" {
		t.Fatalf("expected field-invalid on items[0].kind for an unrecognized kind, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadItemDanglingAssetRejected(t *testing.T) {
	_, verr := DecodeMediaPlaylistPayload(validMediaPlaylistJSON(), alwaysTrueShowExists, alwaysFalseAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "items[0]" {
		t.Fatalf("expected field-unknown-reference on items[0], got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadItemCrossShowRejected(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"asset","show":"christmas-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"sequential","maxGainDb":-10}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeCrossShowReference || verr.Field != "items[0].show" {
		t.Fatalf("expected cross-show-reference on items[0].show, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadResumeMustBeEnum(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"asset","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"continue","itemTransition":"sequential","maxGainDb":-10}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "resume" {
		t.Fatalf("expected field-invalid on resume, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadCrossfadeMsRequiredWithCrossfadeTransition(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"asset","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"crossfade","maxGainDb":-10}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "crossfadeMs" {
		t.Fatalf("expected field-required on crossfadeMs when itemTransition is crossfade, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadCrossfadeMsRejectedWithoutCrossfadeTransition(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"asset","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"sequential","crossfadeMs":500,"maxGainDb":-10}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "crossfadeMs" {
		t.Fatalf("expected field-invalid on crossfadeMs present without crossfade transition, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadMaxGainDbMustNotBePositive(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"asset","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"sequential","maxGainDb":1}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "maxGainDb" {
		t.Fatalf("expected field-invalid on positive maxGainDb, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadFadeOutMsRequiresFadeInMs(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"asset","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"sequential","maxGainDb":-10,"fadeOutMs":200}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "fadeOutMs" {
		t.Fatalf("expected field-required when fadeOutMs is configured without fadeInMs, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadFadeOutMsMustBePositive(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"asset","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"sequential","maxGainDb":-10,"fadeOutMs":0,"fadeInMs":800}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "fadeOutMs" {
		t.Fatalf("expected field-invalid on fadeOutMs 0, got %+v", verr)
	}
}

func TestDecodeMediaPlaylistPayloadRepeatDefaultsToNone(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"asset","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"sequential","maxGainDb":-10}`
	p, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Repeat != NightSessionBackgroundRepeatNone {
		t.Fatalf("expected repeat to default to none, got %q", p.Repeat)
	}
}

func TestDecodeMediaPlaylistPayloadUnknownTopLevelKeyRejected(t *testing.T) {
	raw := `{"show":"halloween-2026","label":"Bed","items":[{"kind":"asset","show":"halloween-2026","sequence":"bg-track-1","target":"audio-node-1"}],"resume":"resume","itemTransition":"sequential","maxGainDb":-10,"runner":"fpp"}`
	_, verr := DecodeMediaPlaylistPayload(raw, alwaysTrueShowExists, alwaysTrueAssetCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key for an unrecognized top-level field, got %+v", verr)
	}
}

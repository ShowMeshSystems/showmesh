package config

import (
	"encoding/json"
	"fmt"
)

// This file promotes night.session.resting.backgroundAudio
// (nightsession.go) to its own per-object configuration kind: a media bed
// an audio node plays, authorable and deletable independent of any
// particular night.session. The audio runtime this bed drives is
// unchanged; this seam is decode/validate/store only. Unlike
// show.playlist (a list of cues a runner steps through), media.playlist is
// a list of things the audio engine plays as a bed, and several may exist
// per show - there is no one-per-show rule here.
//
// Decode reuses the SAME validators night.session's inline background-audio
// block uses for repeat, resume, itemTransition, gain, and fade bounds
// (decodeBackgroundAudioItemTransition, decodeBackgroundAudioMaxGainDb,
// decodeBackgroundAudioFadePair, nightsession.go), so a value invalid
// inline in a night session is invalid here with the same code and the
// same message text apart from the field path.

// MediaPlaylistConfigKind is config_objects.kind and config_revisions.kind
// for a media.playlist object. Like show.playlist, this is a collection:
// each object id is the bed's own identifier, chosen by the caller.
const MediaPlaylistConfigKind = "media.playlist"

// The one member of media.playlist.items[].kind this seam accepts.
const (
	MediaPlaylistItemKindAsset = "asset"

	// mediaPlaylistItemKindCue is NOT a member of any allowed-value set. It
	// exists only so decodeMediaPlaylistItem can name it explicitly and
	// answer with ValidationCodeNotImplemented rather than the generic
	// ValidationCodeFieldInvalid every other unrecognized kind gets - a
	// runner-stepped cue reference inside a media bed is reserved for a
	// later seam and must not be authorable until one exists.
	mediaPlaylistItemKindCue = "cue"
)

// mediaPlaylistTopLevelKeys is the complete set of keys
// DecodeMediaPlaylistPayload recognizes at the top level of the request
// body.
var mediaPlaylistTopLevelKeys = map[string]bool{
	"label": true, "show": true, "items": true,
	"repeat": true, "resume": true, "itemTransition": true, "crossfadeMs": true,
	"maxGainDb": true, "fadeOutMs": true, "fadeInMs": true,
}

// mediaPlaylistItemKeys is the recognized key set for one media.playlist
// items[] entry.
var mediaPlaylistItemKeys = map[string]bool{"kind": true, "show": true, "sequence": true, "target": true}

// MediaPlaylistPayload is config_revisions.payload_json's decoded,
// VALIDATED shape for [MediaPlaylistConfigKind].
type MediaPlaylistPayload struct {
	Label          string              `json:"label"`
	Show           string              `json:"show"`
	Items          []MediaPlaylistItem `json:"items"`
	Repeat         string              `json:"repeat"`
	Resume         string              `json:"resume"`
	ItemTransition string              `json:"itemTransition"`
	CrossfadeMs    *int                `json:"crossfadeMs,omitempty"`
	MaxGainDb      float64             `json:"maxGainDb"`
	FadeOutMs      *int                `json:"fadeOutMs,omitempty"`
	FadeInMs       *int                `json:"fadeInMs,omitempty"`
}

// MediaPlaylistItem is one entry of media.playlist.items. Kind is always
// [MediaPlaylistItemKindAsset] today; Asset is that item's (show, sequence,
// target) tuple, the same identity night.session's background-audio items
// resolve against.
type MediaPlaylistItem struct {
	Kind  string
	Asset NightSessionAssetRef
}

// MarshalJSON flattens Asset's three fields alongside Kind, matching the
// wire shape { "kind", "show", "sequence", "target" } rather than a nested
// "asset" object, mirroring NightSessionBackgroundAudioItem's own flattened
// shape one file over.
func (i MediaPlaylistItem) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind     string `json:"kind"`
		Show     string `json:"show"`
		Sequence string `json:"sequence"`
		Target   string `json:"target"`
	}{Kind: i.Kind, Show: i.Asset.Show, Sequence: i.Asset.Sequence, Target: i.Asset.Target})
}

// UnmarshalJSON is MarshalJSON's inverse, for a read-back of an
// already-validated stored payload. DecodeMediaPlaylistPayload never calls
// this; it decodes field-by-field with its own validation.
func (i *MediaPlaylistItem) UnmarshalJSON(b []byte) error {
	var wire struct {
		Kind     string `json:"kind"`
		Show     string `json:"show"`
		Sequence string `json:"sequence"`
		Target   string `json:"target"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	i.Kind = wire.Kind
	i.Asset = NightSessionAssetRef{Show: wire.Show, Sequence: wire.Sequence, Target: wire.Target}
	return nil
}

// EncodeMediaPlaylistPayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeMediaPlaylistPayload); this function does not re-validate.
func EncodeMediaPlaylistPayload(p MediaPlaylistPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode media.playlist payload: %w", err)
	}
	return string(b), nil
}

// DecodeMediaPlaylistPayload parses and validates raw. showExists reports
// whether "show" names an existing show config object; assetCurrent
// reports whether an item's (show, sequence, target) tuple names a current
// asset, exactly as night.session's own background-audio items resolve
// (this package has no store access - both are caller-supplied, like every
// cross-object reference check in this package).
func DecodeMediaPlaylistPayload(raw string, showExists func(string) bool, assetCurrent AssetCurrent) (MediaPlaylistPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return MediaPlaylistPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, mediaPlaylistTopLevelKeys); verr != nil {
		return MediaPlaylistPayload{}, verr
	}

	show, verr := decodeRequiredString(top, "show", "show")
	if verr != nil {
		return MediaPlaylistPayload{}, verr
	}
	if verr := validateShowRef(show); verr != nil {
		return MediaPlaylistPayload{}, verr
	}
	if !showExists(show) {
		return MediaPlaylistPayload{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "show",
			Detail: fmt.Sprintf("show %q is not a configured show", show),
		}
	}

	label, verr := decodeRequiredString(top, "label", "label")
	if verr != nil {
		return MediaPlaylistPayload{}, verr
	}

	items, verr := decodeMediaPlaylistItems(top, show, assetCurrent)
	if verr != nil {
		return MediaPlaylistPayload{}, verr
	}

	repeat, verr := decodeDefaultedEnum(top, "repeat", "repeat", NightSessionBackgroundRepeatNone, nightSessionBackgroundRepeatValues)
	if verr != nil {
		return MediaPlaylistPayload{}, verr
	}

	resume, verr := decodeRequiredEnum(top, "resume", "resume", nightSessionBackgroundResumeValues)
	if verr != nil {
		return MediaPlaylistPayload{}, verr
	}

	itemTransition, crossfadeMs, verr := decodeBackgroundAudioItemTransition(top, "itemTransition", "crossfadeMs")
	if verr != nil {
		return MediaPlaylistPayload{}, verr
	}

	maxGainDb, verr := decodeBackgroundAudioMaxGainDb(top, "maxGainDb")
	if verr != nil {
		return MediaPlaylistPayload{}, verr
	}

	fadeOutMs, fadeInMs, verr := decodeBackgroundAudioFadePair(top, "fadeOutMs", "fadeInMs")
	if verr != nil {
		return MediaPlaylistPayload{}, verr
	}

	return MediaPlaylistPayload{
		Label: label, Show: show, Items: items,
		Repeat: repeat, Resume: resume, ItemTransition: itemTransition, CrossfadeMs: crossfadeMs,
		MaxGainDb: maxGainDb, FadeOutMs: fadeOutMs, FadeInMs: fadeInMs,
	}, nil
}

// decodeMediaPlaylistItems decodes the required, non-empty, ordered
// "items" array.
func decodeMediaPlaylistItems(top map[string]json.RawMessage, show string, assetCurrent AssetCurrent) ([]MediaPlaylistItem, *ValidationError) {
	itemsRaw, present := top["items"]
	if !present {
		return nil, &ValidationError{Code: ValidationCodeFieldRequired, Field: "items", Detail: "items is required"}
	}
	if isJSONNull(itemsRaw) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: "items", Detail: "items must not be null"}
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(itemsRaw, &rawItems); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "items", Detail: "items must be a JSON array"}
	}
	if len(rawItems) == 0 {
		return nil, &ValidationError{Code: ValidationCodeBackgroundAudioItemsEmpty, Field: "items", Detail: "items must contain at least one item"}
	}

	items := make([]MediaPlaylistItem, 0, len(rawItems))
	for i, rawItem := range rawItems {
		field := fmt.Sprintf("items[%d]", i)
		item, verr := decodeMediaPlaylistItem(rawItem, field, show, assetCurrent)
		if verr != nil {
			return nil, verr
		}
		items = append(items, item)
	}
	return items, nil
}

// decodeMediaPlaylistItem decodes one items[] entry. kind "cue" is
// refused with [ValidationCodeNotImplemented] rather than the generic
// field-invalid code (this file's own top doc comment); any other
// unrecognized kind gets the ordinary refusal.
func decodeMediaPlaylistItem(raw json.RawMessage, field, show string, assetCurrent AssetCurrent) (MediaPlaylistItem, *ValidationError) {
	if isJSONNull(raw) {
		return MediaPlaylistItem{}, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return MediaPlaylistItem{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON object", field)}
	}
	if verr := rejectUnknownKeysUnder(fields, mediaPlaylistItemKeys, field); verr != nil {
		return MediaPlaylistItem{}, verr
	}

	kind, verr := decodeRequiredString(fields, "kind", field+".kind")
	if verr != nil {
		return MediaPlaylistItem{}, verr
	}
	if kind == mediaPlaylistItemKindCue {
		return MediaPlaylistItem{}, &ValidationError{
			Code: ValidationCodeNotImplemented, Field: field + ".kind",
			Detail: "kind \"cue\" is reserved but not yet supported",
		}
	}
	if kind != MediaPlaylistItemKindAsset {
		return MediaPlaylistItem{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: field + ".kind",
			Detail: "kind must be \"asset\"",
		}
	}

	asset, verr := decodeNightSessionAssetRef(fields, field, show, assetCurrent)
	if verr != nil {
		return MediaPlaylistItem{}, verr
	}

	return MediaPlaylistItem{Kind: kind, Asset: asset}, nil
}

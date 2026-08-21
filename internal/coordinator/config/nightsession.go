package config

import (
	"encoding/json"
	"fmt"
	"sort"
)

// This file is Track F seam F1's own addition (docs/private/seam-specs/
// TRACK-F-F1-night-session-config.md, ADR-038, ADR-039, RESTING-MODE.md
// §6-8/§12/§13): the config_objects.kind / config_revisions.payload_json
// shape for "night.session" and its singleton pointer
// "night.session.active" (nightsessionactive.go, one file over). Follows
// show.action/show.macro's hand-written pattern (showaction.go's own top
// doc comment) rather than a generic kind registry two files did not
// justify and six does not either.
//
// Every cross-object reference this kind needs — a configured FPP
// instance, an existing show.action object, a current asset for a
// (show, sequence, target) tuple — is a caller-supplied callback to
// DecodeNightSessionPayload, exactly like show.macro's resolveAction and
// show.active's showExists: this package has no store access.
//
// The background-audio vocabulary (itemId, resume, repeat, itemTransition)
// is pinned against pkg/audio's PlaylistItem/PlaylistRef shape on Track
// C's branch (track-c/audio-node, commit 12ce0d8, not yet on main): this
// package does not and must not import pkg/audio (it is not merged), but
// the wire spellings here are chosen to agree with it now rather than
// reconciling two vocabularies at F5.

// NightSessionConfigKind is config_objects.kind and config_revisions.kind
// for a night.session object. Like show.action, this is a collection: each
// object id is the session's own identifier, chosen by the caller.
const NightSessionConfigKind = "night.session"

// The three members of night.session.resting.backgroundAudio.repeat.
const (
	NightSessionBackgroundRepeatNone     = "none"
	NightSessionBackgroundRepeatItem     = "item"
	NightSessionBackgroundRepeatPlaylist = "playlist"
)

var nightSessionBackgroundRepeatValues = map[string]bool{
	NightSessionBackgroundRepeatNone:     true,
	NightSessionBackgroundRepeatItem:     true,
	NightSessionBackgroundRepeatPlaylist: true,
}

// The two members of night.session.resting.backgroundAudio.resume —
// pinned against pkg/audio's vocabulary on track-c/audio-node (see this
// file's own top doc comment). NOT a bool: orchestrator correction,
// 2026-08-18.
const (
	NightSessionBackgroundResumeResume  = "resume"
	NightSessionBackgroundResumeRestart = "restart"
)

var nightSessionBackgroundResumeValues = map[string]bool{
	NightSessionBackgroundResumeResume:  true,
	NightSessionBackgroundResumeRestart: true,
}

// The three members of night.session.resting.backgroundAudio.itemTransition.
const (
	NightSessionItemTransitionSequential = "sequential"
	NightSessionItemTransitionGapless    = "gapless"
	NightSessionItemTransitionCrossfade  = "crossfade"
)

var nightSessionItemTransitionValues = map[string]bool{
	NightSessionItemTransitionSequential: true,
	NightSessionItemTransitionGapless:    true,
	NightSessionItemTransitionCrossfade:  true,
}

// The two members of a transition cue's role.
const (
	NightSessionCueRoleLighting     = "lighting"
	NightSessionCueRoleProjection   = "projection"
	NightSessionCueRoleAudio        = "audio"
	NightSessionCueRoleAnnouncement = "announcement"
	NightSessionCueRoleOther        = "other"
)

var nightSessionCueRoleValues = map[string]bool{
	NightSessionCueRoleLighting:     true,
	NightSessionCueRoleProjection:   true,
	NightSessionCueRoleAudio:        true,
	NightSessionCueRoleAnnouncement: true,
	NightSessionCueRoleOther:        true,
}

// The two members of a transition cue's onFailure, and its default —
// ADR-035: a run always runs every step, so "abort" is an explicit,
// operator-chosen exception rather than the default polarity.
const (
	NightSessionCueOnFailureContinue = "continue"
	NightSessionCueOnFailureAbort    = "abort"
	NightSessionCueOnFailureDefault  = NightSessionCueOnFailureContinue
)

var nightSessionCueOnFailureValues = map[string]bool{
	NightSessionCueOnFailureContinue: true,
	NightSessionCueOnFailureAbort:    true,
}

// nightSessionCalendarKeys is ADR-038 decision 1's closed forbidden-key
// list: a key by any of these names, anywhere in a night.session payload,
// names a wall-clock or dated value, and FPP alone is the calendar
// authority.
var nightSessionCalendarKeys = map[string]bool{
	"at": true, "cron": true, "schedule": true, "time": true,
	"date": true, "weekday": true, "timezone": true,
}

// nightSessionDurationKeys is RESTING-MODE.md §6.1's forbidden-key list: a
// hand-entered restatement of the resting FSEQ's own length, which the
// FSEQ alone is the authority for.
var nightSessionDurationKeys = map[string]bool{
	"restDuration": true, "restSeconds": true, "restDurationMs": true,
	"restDurationSeconds": true, "restLengthMs": true, "restLengthSeconds": true,
}

// nightSessionTopLevelKeys is the complete set of keys
// DecodeNightSessionPayload recognizes at the top level of the request
// body. "siteControl" and "interlocks" are deliberately NOT members: see
// rejectNightSessionUnimplementedBlocks.
var nightSessionTopLevelKeys = map[string]bool{
	"show": true, "label": true, "showPlaylist": true, "resting": true,
	"enterShow": true, "enterResting": true,
}

var (
	nightSessionShowPlaylistKeys   = map[string]bool{"fppInstanceId": true, "playlist": true}
	nightSessionRestingKeys        = map[string]bool{"fppInstanceId": true, "playlist": true, "endOfNightPlaylist": true, "timelineAsset": true, "endOfNightRepeat": true, "backgroundAudio": true}
	nightSessionAssetRefKeys       = map[string]bool{"show": true, "sequence": true, "target": true}
	nightSessionBackgroundKeys     = map[string]bool{"items": true, "repeat": true, "resume": true, "itemTransition": true, "crossfadeMs": true, "maxGainDb": true}
	nightSessionBackgroundItemKeys = map[string]bool{"itemId": true, "show": true, "sequence": true, "target": true}
	nightSessionTransitionKeys     = map[string]bool{"cues": true, "blackoutHoldMs": true}
	nightSessionRestingTransKeys   = map[string]bool{"cues": true, "blackoutAfterShowMs": true}
	nightSessionCueKeys            = map[string]bool{"name": true, "role": true, "action": true, "offsetMs": true, "fadeDurationMs": true, "barrier": true, "onFailure": true}
)

// NightSessionPayload is config_revisions.payload_json's decoded,
// VALIDATED shape for [NightSessionConfigKind].
type NightSessionPayload struct {
	Show         string                   `json:"show"`
	Label        string                   `json:"label"`
	ShowPlaylist NightSessionFPPPlaylist  `json:"showPlaylist"`
	Resting      NightSessionResting      `json:"resting"`
	EnterShow    NightSessionEnterShow    `json:"enterShow"`
	EnterResting NightSessionEnterResting `json:"enterResting"`
}

// NightSessionFPPPlaylist names an FPP-owned playlist: referenced, never
// created (RESTING-MODE.md §6.1).
type NightSessionFPPPlaylist struct {
	FPPInstanceID string `json:"fppInstanceId"`
	Playlist      string `json:"playlist"`
}

// NightSessionAssetRef is ADR-028's asset identity as a night.session
// needs to name one: show, sequence, and target. TargetKind is always
// "node" here — a resting timeline or background-audio asset is always
// resolved per rendering/playout target, never per show.
type NightSessionAssetRef struct {
	Show     string `json:"show"`
	Sequence string `json:"sequence"`
	Target   string `json:"target"`
}

// NightSessionResting is night.session.resting.
type NightSessionResting struct {
	FPPInstanceID      string                       `json:"fppInstanceId"`
	Playlist           string                       `json:"playlist"`
	EndOfNightPlaylist string                       `json:"endOfNightPlaylist"`
	TimelineAsset      NightSessionAssetRef         `json:"timelineAsset"`
	EndOfNightRepeat   bool                         `json:"endOfNightRepeat"`
	BackgroundAudio    *NightSessionBackgroundAudio `json:"backgroundAudio,omitempty"`
}

// NightSessionBackgroundAudioItem is one entry of
// resting.backgroundAudio.items — pkg/audio.PlaylistItem's own
// ItemID/Media pairing, spelled out here as ItemID plus an
// [NightSessionAssetRef] (this file's own top doc comment).
type NightSessionBackgroundAudioItem struct {
	ItemID string               `json:"itemId"`
	Asset  NightSessionAssetRef `json:"-"`
}

// MarshalJSON flattens Asset's three fields alongside ItemID, matching the
// wire shape { "itemId", "show", "sequence", "target" } rather than a
// nested "asset" object — the payload contract in the seam spec.
func (i NightSessionBackgroundAudioItem) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ItemID   string `json:"itemId"`
		Show     string `json:"show"`
		Sequence string `json:"sequence"`
		Target   string `json:"target"`
	}{ItemID: i.ItemID, Show: i.Asset.Show, Sequence: i.Asset.Sequence, Target: i.Asset.Target})
}

// UnmarshalJSON is MarshalJSON's inverse, for jsonUnmarshalStrict's
// read-back of an already-validated stored payload (api's
// decodeShowActionPayloadForRead precedent). DecodeNightSessionPayload
// never calls this; it decodes field-by-field with its own validation.
func (i *NightSessionBackgroundAudioItem) UnmarshalJSON(b []byte) error {
	var wire struct {
		ItemID   string `json:"itemId"`
		Show     string `json:"show"`
		Sequence string `json:"sequence"`
		Target   string `json:"target"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	i.ItemID = wire.ItemID
	i.Asset = NightSessionAssetRef{Show: wire.Show, Sequence: wire.Sequence, Target: wire.Target}
	return nil
}

// NightSessionBackgroundAudio is night.session.resting.backgroundAudio,
// present only when the deployment configures background audio at all.
// CrossfadeMs is a pointer, not a plain int with "omitempty": the
// validator REQUIRES the key present (0 is a legal value — an immediate
// cut) whenever ItemTransition is "crossfade" and requires it ABSENT
// otherwise. A plain int with "omitempty" collapses "present as 0" and
// "absent" into the same wire form, so a stored crossfadeMs:0 silently
// vanished from GET's own response and re-PUTting that exact response
// was rejected for a field the operator never removed — found in review.
// nil means "not applicable"; non-nil (including *CrossfadeMs == 0) means
// "present on the wire", matching FadeDurationMs's existing pointer
// pattern one field over.
type NightSessionBackgroundAudio struct {
	Items          []NightSessionBackgroundAudioItem `json:"items"`
	Repeat         string                            `json:"repeat"`
	Resume         string                            `json:"resume"`
	ItemTransition string                            `json:"itemTransition"`
	CrossfadeMs    *int                              `json:"crossfadeMs,omitempty"`
	MaxGainDb      float64                           `json:"maxGainDb"`
}

// NightSessionCue is one entry of enterShow.cues or enterResting.cues.
type NightSessionCue struct {
	Name           string `json:"name"`
	Role           string `json:"role"`
	Action         string `json:"action"`
	OffsetMs       int    `json:"offsetMs"`
	FadeDurationMs *int   `json:"fadeDurationMs,omitempty"`
	Barrier        bool   `json:"barrier"`
	OnFailure      string `json:"onFailure"`
}

// NightSessionEnterShow is night.session.enterShow.
type NightSessionEnterShow struct {
	Cues           []NightSessionCue `json:"cues"`
	BlackoutHoldMs int               `json:"blackoutHoldMs"`
}

// NightSessionEnterResting is night.session.enterResting.
type NightSessionEnterResting struct {
	Cues                []NightSessionCue `json:"cues"`
	BlackoutAfterShowMs int               `json:"blackoutAfterShowMs"`
}

// EncodeNightSessionPayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeNightSessionPayload); this function does not re-validate.
func EncodeNightSessionPayload(p NightSessionPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode night.session payload: %w", err)
	}
	return string(b), nil
}

// AssetCurrent reports whether a current (non-superseded) asset exists for
// the given (show, sequence, target) tuple, with an implied TargetKind of
// "node" — the shape every night.session asset reference needs.
// Caller-supplied, like every cross-object reference check in this
// package (this file's own top doc comment).
type AssetCurrent func(show, sequence, target string) bool

// ActionResolver reports whether actionID names an existing show.action
// object with an active revision and, when it does, the show it belongs
// to. Caller-supplied, mirroring show.macro's resolveAction narrowed to
// existence plus show rather than existence plus target.integration:
// unlike a macro step, a night.session cue carries no localFallback class
// that would need the integration, but ADR-027's namespace rule DOES need
// the action's own show compared against this session's — found in
// review: neither this type's predecessor (ActionExists) nor its caller
// ever read or compared the action's show, so a christmas-2026 action
// bound into a halloween-2026 session was silently accepted.
type ActionResolver func(actionID string) (show string, ok bool)

// DecodeNightSessionPayload parses and validates raw against the seam F1
// spec. endpoints is the coordinator's currently configured FPP instances
// (show.action's own currentFPPEndpoints precedent); assetCurrent and
// actionResolver are the two cross-object reference checks this kind
// needs. Every cross-object reference this payload carries — cue actions,
// the resting timeline asset, and every backgroundAudio item — must name
// an object belonging to THIS session's own "show" (ADR-027: a Show is a
// namespace precisely so that programming Christmas cannot break
// Halloween); a reference into a different show is rejected with
// [ValidationCodeCrossShowReference], not silently accepted.
func DecodeNightSessionPayload(raw string, endpoints []FPPEndpoint, assetCurrent AssetCurrent, actionResolver ActionResolver) (NightSessionPayload, *ValidationError) {
	if verr := rejectNightSessionUnimplementedBlocks(raw); verr != nil {
		return NightSessionPayload{}, verr
	}
	if verr := scanNightSessionForbiddenKeys(raw); verr != nil {
		return NightSessionPayload{}, verr
	}

	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return NightSessionPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, nightSessionTopLevelKeys); verr != nil {
		return NightSessionPayload{}, verr
	}

	show, verr := decodeRequiredString(top, "show", "show")
	if verr != nil {
		return NightSessionPayload{}, verr
	}
	if verr := validateShowRef(show); verr != nil {
		return NightSessionPayload{}, verr
	}

	label, verr := decodeRequiredString(top, "label", "label")
	if verr != nil {
		return NightSessionPayload{}, verr
	}

	showPlaylistFields, verr := decodeRequiredObject(top, "showPlaylist", "showPlaylist")
	if verr != nil {
		return NightSessionPayload{}, verr
	}
	if verr := rejectUnknownKeysUnder(showPlaylistFields, nightSessionShowPlaylistKeys, "showPlaylist"); verr != nil {
		return NightSessionPayload{}, verr
	}
	showPlaylist, verr := decodeNightSessionFPPPlaylist(showPlaylistFields, "showPlaylist", endpoints)
	if verr != nil {
		return NightSessionPayload{}, verr
	}

	restingFields, verr := decodeRequiredObject(top, "resting", "resting")
	if verr != nil {
		return NightSessionPayload{}, verr
	}
	if verr := rejectUnknownKeysUnder(restingFields, nightSessionRestingKeys, "resting"); verr != nil {
		return NightSessionPayload{}, verr
	}
	resting, verr := decodeNightSessionResting(restingFields, show, endpoints, assetCurrent)
	if verr != nil {
		return NightSessionPayload{}, verr
	}

	enterShowFields, verr := decodeRequiredObject(top, "enterShow", "enterShow")
	if verr != nil {
		return NightSessionPayload{}, verr
	}
	if verr := rejectUnknownKeysUnder(enterShowFields, nightSessionTransitionKeys, "enterShow"); verr != nil {
		return NightSessionPayload{}, verr
	}
	enterShow, verr := decodeNightSessionEnterShow(enterShowFields, show, actionResolver)
	if verr != nil {
		return NightSessionPayload{}, verr
	}

	enterRestingFields, verr := decodeRequiredObject(top, "enterResting", "enterResting")
	if verr != nil {
		return NightSessionPayload{}, verr
	}
	if verr := rejectUnknownKeysUnder(enterRestingFields, nightSessionRestingTransKeys, "enterResting"); verr != nil {
		return NightSessionPayload{}, verr
	}
	enterResting, verr := decodeNightSessionEnterResting(enterRestingFields, show, actionResolver)
	if verr != nil {
		return NightSessionPayload{}, verr
	}

	return NightSessionPayload{
		Show: show, Label: label, ShowPlaylist: showPlaylist, Resting: resting,
		EnterShow: enterShow, EnterResting: enterResting,
	}, nil
}

// nightSessionUnimplementedBlockKeys is
// rejectNightSessionUnimplementedBlocks' forbidden-key set.
var nightSessionUnimplementedBlockKeys = map[string]bool{"siteControl": true, "interlocks": true}

// rejectNightSessionUnimplementedBlocks refuses a payload that names
// "siteControl" or "interlocks" ANYWHERE in the object tree — not only at
// the top level — with its own message naming exactly why (RESTING-MODE.md
// §10 specifies both; Track F F6 has not shipped enforcement for either).
// Run before both the generic unknown-key rule and the calendar/duration
// scans so the operator sees this specific reason rather than a generic
// "field-unknown-key" — found in review: a NESTED "resting.siteControl"
// was still refused, but by the generic rule, silently losing the
// "specified, not implemented" reason the top-level check gave.
func rejectNightSessionUnimplementedBlocks(raw string) *ValidationError {
	var generic any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		return nil // malformed JSON is reported by decodeTopLevelObject
	}
	return scanForbiddenKeysRec(generic, "", nightSessionUnimplementedBlockKeys, ValidationCodeNotImplemented,
		"%s is specified (RESTING-MODE.md §10) and not implemented (Track F F6); omit it entirely rather than configuring something nothing enforces")
}

// scanNightSessionForbiddenKeys walks raw's full JSON object tree —
// every level, not only the top — for a calendar key (ADR-038 decision 1)
// or a hand-entered rest-duration key (RESTING-MODE.md §6.1). Run before
// structural decode so a forbidden key nested inside an otherwise-valid
// object is caught with its own dotted path rather than silently accepted
// by whichever nested decoder does not happen to reject unknown keys.
func scanNightSessionForbiddenKeys(raw string) *ValidationError {
	var generic any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		return nil // malformed JSON is reported by decodeTopLevelObject
	}
	if verr := scanForbiddenKeysRec(generic, "", nightSessionCalendarKeys, ValidationCodeCalendarFieldRejected,
		"%s is a calendar field; FPP alone authorizes and schedules a night session (ADR-038 decision 1)"); verr != nil {
		return verr
	}
	return scanForbiddenKeysRec(generic, "", nightSessionDurationKeys, ValidationCodeDuplicateRestDuration,
		"%s restates the resting FSEQ's own duration; the FSEQ is the only duration authority (RESTING-MODE.md §6.1)")
}

// scanForbiddenKeysRec is scanNightSessionForbiddenKeys' recursive walker.
// Keys are visited in sorted order so a payload with more than one
// violation always reports the same one, deterministically.
func scanForbiddenKeysRec(v any, path string, forbidden map[string]bool, code, msgFmt string) *ValidationError {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			field := k
			if path != "" {
				field = path + "." + k
			}
			if forbidden[k] {
				return &ValidationError{Code: code, Field: field, Detail: fmt.Sprintf(msgFmt, field)}
			}
			if verr := scanForbiddenKeysRec(t[k], field, forbidden, code, msgFmt); verr != nil {
				return verr
			}
		}
	case []any:
		for i, item := range t {
			field := fmt.Sprintf("%s[%d]", path, i)
			if verr := scanForbiddenKeysRec(item, field, forbidden, code, msgFmt); verr != nil {
				return verr
			}
		}
	}
	return nil
}

func decodeNightSessionFPPPlaylist(fields map[string]json.RawMessage, path string, endpoints []FPPEndpoint) (NightSessionFPPPlaylist, *ValidationError) {
	instanceID, verr := decodeRequiredString(fields, "fppInstanceId", path+".fppInstanceId")
	if verr != nil {
		return NightSessionFPPPlaylist{}, verr
	}
	if !fppInstanceConfigured(instanceID, endpoints) {
		return NightSessionFPPPlaylist{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: path + ".fppInstanceId",
			Detail: fmt.Sprintf("%q is not a configured FPP instance", instanceID),
		}
	}
	playlist, verr := decodeRequiredString(fields, "playlist", path+".playlist")
	if verr != nil {
		return NightSessionFPPPlaylist{}, verr
	}
	return NightSessionFPPPlaylist{FPPInstanceID: instanceID, Playlist: playlist}, nil
}

// decodeNightSessionAssetRef decodes {show, sequence, target} and checks
// it resolves to a current asset via assetCurrent — the write-time
// dangling-reference check the seam spec requires. Whether "playlist" (the
// FPP resting/show playlist reference) or "target" (which node) names the
// SAME entity FPP itself would resolve at showtime is unverifiable without
// a live FPP and is explicitly F2/F3 readiness work, not this seam's.
//
// fields' unknown-key rejection is the CALLER's job, not this function's:
// a background-audio item's fields also carry "itemId" alongside the
// asset-ref keys, so a single fixed key set here would reject a valid
// item. See decodeNightSessionResting (timelineAsset) and
// decodeNightSessionBackgroundAudio (each item) for the two call sites'
// own key sets.
func decodeNightSessionAssetRef(fields map[string]json.RawMessage, path, sessionShow string, assetCurrent AssetCurrent) (NightSessionAssetRef, *ValidationError) {
	show, verr := decodeRequiredString(fields, "show", path+".show")
	if verr != nil {
		return NightSessionAssetRef{}, verr
	}
	if show != sessionShow {
		return NightSessionAssetRef{}, &ValidationError{
			Code: ValidationCodeCrossShowReference, Field: path + ".show",
			Detail: fmt.Sprintf("%s names show %q, not this session's own show %q (ADR-027: a Show is a namespace)", path, show, sessionShow),
		}
	}
	sequence, verr := decodeRequiredString(fields, "sequence", path+".sequence")
	if verr != nil {
		return NightSessionAssetRef{}, verr
	}
	target, verr := decodeRequiredString(fields, "target", path+".target")
	if verr != nil {
		return NightSessionAssetRef{}, verr
	}
	if !assetCurrent(show, sequence, target) {
		return NightSessionAssetRef{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: path,
			Detail: fmt.Sprintf("no current asset for show %q sequence %q target %q", show, sequence, target),
		}
	}
	return NightSessionAssetRef{Show: show, Sequence: sequence, Target: target}, nil
}

func decodeNightSessionResting(fields map[string]json.RawMessage, sessionShow string, endpoints []FPPEndpoint, assetCurrent AssetCurrent) (NightSessionResting, *ValidationError) {
	instanceID, verr := decodeRequiredString(fields, "fppInstanceId", "resting.fppInstanceId")
	if verr != nil {
		return NightSessionResting{}, verr
	}
	if !fppInstanceConfigured(instanceID, endpoints) {
		return NightSessionResting{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "resting.fppInstanceId",
			Detail: fmt.Sprintf("%q is not a configured FPP instance", instanceID),
		}
	}

	playlist, verr := decodeRequiredString(fields, "playlist", "resting.playlist")
	if verr != nil {
		return NightSessionResting{}, verr
	}

	endOfNightPlaylist, verr := decodeOptionalNonEmptyString(fields, "endOfNightPlaylist", "resting.endOfNightPlaylist")
	if verr != nil {
		return NightSessionResting{}, verr
	}
	if endOfNightPlaylist == "" {
		endOfNightPlaylist = playlist
	}

	timelineAssetFields, verr := decodeRequiredObject(fields, "timelineAsset", "resting.timelineAsset")
	if verr != nil {
		return NightSessionResting{}, verr
	}
	if verr := rejectUnknownKeysUnder(timelineAssetFields, nightSessionAssetRefKeys, "resting.timelineAsset"); verr != nil {
		return NightSessionResting{}, verr
	}
	timelineAsset, verr := decodeNightSessionAssetRef(timelineAssetFields, "resting.timelineAsset", sessionShow, assetCurrent)
	if verr != nil {
		return NightSessionResting{}, verr
	}

	endOfNightRepeat, verr := decodeDefaultedBool(fields, "endOfNightRepeat", "resting.endOfNightRepeat", false)
	if verr != nil {
		return NightSessionResting{}, verr
	}

	var background *NightSessionBackgroundAudio
	if raw, present := fields["backgroundAudio"]; present {
		if isJSONNull(raw) {
			return NightSessionResting{}, &ValidationError{
				Code: ValidationCodeFieldNull, Field: "resting.backgroundAudio",
				Detail: "resting.backgroundAudio must not be null; omit it entirely to configure no background audio",
			}
		}
		backgroundFields, verr := decodeRequiredObject(fields, "backgroundAudio", "resting.backgroundAudio")
		if verr != nil {
			return NightSessionResting{}, verr
		}
		b, verr := decodeNightSessionBackgroundAudio(backgroundFields, sessionShow, assetCurrent)
		if verr != nil {
			return NightSessionResting{}, verr
		}
		background = &b
	}

	return NightSessionResting{
		FPPInstanceID: instanceID, Playlist: playlist, EndOfNightPlaylist: endOfNightPlaylist,
		TimelineAsset: timelineAsset, EndOfNightRepeat: endOfNightRepeat, BackgroundAudio: background,
	}, nil
}

func decodeNightSessionBackgroundAudio(fields map[string]json.RawMessage, sessionShow string, assetCurrent AssetCurrent) (NightSessionBackgroundAudio, *ValidationError) {
	if verr := rejectUnknownKeysUnder(fields, nightSessionBackgroundKeys, "resting.backgroundAudio"); verr != nil {
		return NightSessionBackgroundAudio{}, verr
	}

	itemsRaw, present := fields["items"]
	if !present {
		return NightSessionBackgroundAudio{}, &ValidationError{
			Code: ValidationCodeFieldRequired, Field: "resting.backgroundAudio.items",
			Detail: "resting.backgroundAudio.items is required",
		}
	}
	if isJSONNull(itemsRaw) {
		return NightSessionBackgroundAudio{}, &ValidationError{
			Code: ValidationCodeFieldNull, Field: "resting.backgroundAudio.items",
			Detail: "resting.backgroundAudio.items must not be null",
		}
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(itemsRaw, &rawItems); err != nil {
		return NightSessionBackgroundAudio{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "resting.backgroundAudio.items",
			Detail: "resting.backgroundAudio.items must be a JSON array",
		}
	}
	if len(rawItems) == 0 {
		return NightSessionBackgroundAudio{}, &ValidationError{
			Code: ValidationCodeBackgroundAudioItemsEmpty, Field: "resting.backgroundAudio.items",
			Detail: "resting.backgroundAudio.items must contain at least one item",
		}
	}

	items := make([]NightSessionBackgroundAudioItem, 0, len(rawItems))
	seenIDs := make(map[string]bool, len(rawItems))
	for i, rawItem := range rawItems {
		field := fmt.Sprintf("resting.backgroundAudio.items[%d]", i)
		if isJSONNull(rawItem) {
			return NightSessionBackgroundAudio{}, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
		}
		var itemFields map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &itemFields); err != nil {
			return NightSessionBackgroundAudio{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON object", field)}
		}
		if verr := rejectUnknownKeysUnder(itemFields, nightSessionBackgroundItemKeys, field); verr != nil {
			return NightSessionBackgroundAudio{}, verr
		}
		itemID, verr := decodeRequiredString(itemFields, "itemId", field+".itemId")
		if verr != nil {
			return NightSessionBackgroundAudio{}, verr
		}
		if seenIDs[itemID] {
			return NightSessionBackgroundAudio{}, &ValidationError{
				Code: ValidationCodeItemIDDuplicate, Field: field + ".itemId",
				Detail: fmt.Sprintf("itemId %q is used more than once", itemID),
			}
		}
		seenIDs[itemID] = true

		asset, verr := decodeNightSessionAssetRef(itemFields, field, sessionShow, assetCurrent)
		if verr != nil {
			return NightSessionBackgroundAudio{}, verr
		}
		items = append(items, NightSessionBackgroundAudioItem{ItemID: itemID, Asset: asset})
	}

	repeat, verr := decodeDefaultedEnum(fields, "repeat", "resting.backgroundAudio.repeat", NightSessionBackgroundRepeatNone, nightSessionBackgroundRepeatValues)
	if verr != nil {
		return NightSessionBackgroundAudio{}, verr
	}

	resume, verr := decodeRequiredEnum(fields, "resume", "resting.backgroundAudio.resume", nightSessionBackgroundResumeValues)
	if verr != nil {
		return NightSessionBackgroundAudio{}, verr
	}

	itemTransition, verr := decodeRequiredEnum(fields, "itemTransition", "resting.backgroundAudio.itemTransition", nightSessionItemTransitionValues)
	if verr != nil {
		return NightSessionBackgroundAudio{}, verr
	}

	_, crossfadePresent := fields["crossfadeMs"]
	var crossfadeMs *int
	if itemTransition == NightSessionItemTransitionCrossfade {
		if !crossfadePresent {
			return NightSessionBackgroundAudio{}, &ValidationError{
				Code: ValidationCodeFieldRequired, Field: "resting.backgroundAudio.crossfadeMs",
				Detail: "resting.backgroundAudio.crossfadeMs is required when itemTransition is \"crossfade\"",
			}
		}
		v, verr := decodeRequiredNonNegativeInt(fields, "crossfadeMs", "resting.backgroundAudio.crossfadeMs")
		if verr != nil {
			return NightSessionBackgroundAudio{}, verr
		}
		crossfadeMs = &v
	} else if crossfadePresent {
		return NightSessionBackgroundAudio{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "resting.backgroundAudio.crossfadeMs",
			Detail: "resting.backgroundAudio.crossfadeMs must be absent unless itemTransition is \"crossfade\"",
		}
	}

	maxGainDb, verr := decodeRequiredNumber(fields, "maxGainDb", "resting.backgroundAudio.maxGainDb")
	if verr != nil {
		return NightSessionBackgroundAudio{}, verr
	}
	if maxGainDb > 0 {
		return NightSessionBackgroundAudio{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "resting.backgroundAudio.maxGainDb",
			Detail: "resting.backgroundAudio.maxGainDb must be <= 0",
		}
	}

	return NightSessionBackgroundAudio{
		Items: items, Repeat: repeat, Resume: resume, ItemTransition: itemTransition,
		CrossfadeMs: crossfadeMs, MaxGainDb: maxGainDb,
	}, nil
}

func decodeNightSessionEnterShow(fields map[string]json.RawMessage, show string, actionResolver ActionResolver) (NightSessionEnterShow, *ValidationError) {
	cues, verr := decodeNightSessionCues(fields, "enterShow.cues", show, actionResolver)
	if verr != nil {
		return NightSessionEnterShow{}, verr
	}
	blackoutHoldMs, verr := decodeRequiredNonNegativeInt(fields, "blackoutHoldMs", "enterShow.blackoutHoldMs")
	if verr != nil {
		return NightSessionEnterShow{}, verr
	}
	return NightSessionEnterShow{Cues: cues, BlackoutHoldMs: blackoutHoldMs}, nil
}

func decodeNightSessionEnterResting(fields map[string]json.RawMessage, show string, actionResolver ActionResolver) (NightSessionEnterResting, *ValidationError) {
	cues, verr := decodeNightSessionCues(fields, "enterResting.cues", show, actionResolver)
	if verr != nil {
		return NightSessionEnterResting{}, verr
	}
	blackoutAfterShowMs, verr := decodeRequiredNonNegativeInt(fields, "blackoutAfterShowMs", "enterResting.blackoutAfterShowMs")
	if verr != nil {
		return NightSessionEnterResting{}, verr
	}
	return NightSessionEnterResting{Cues: cues, BlackoutAfterShowMs: blackoutAfterShowMs}, nil
}

func decodeNightSessionCues(top map[string]json.RawMessage, field, show string, actionResolver ActionResolver) ([]NightSessionCue, *ValidationError) {
	rawCues, present := top["cues"]
	if !present {
		return nil, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(rawCues) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawCues, &items); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON array", field)}
	}

	cues := make([]NightSessionCue, 0, len(items))
	seenNames := make(map[string]bool, len(items))
	for i, raw := range items {
		cueField := fmt.Sprintf("%s[%d]", field, i)
		cue, verr := decodeNightSessionCue(raw, cueField, show, actionResolver)
		if verr != nil {
			return nil, verr
		}
		if seenNames[cue.Name] {
			return nil, &ValidationError{
				Code: ValidationCodeCueNameDuplicate, Field: cueField + ".name",
				Detail: fmt.Sprintf("cue name %q is used more than once in %s", cue.Name, field),
			}
		}
		seenNames[cue.Name] = true
		cues = append(cues, cue)
	}
	return cues, nil
}

func decodeNightSessionCue(raw json.RawMessage, field, sessionShow string, actionResolver ActionResolver) (NightSessionCue, *ValidationError) {
	if isJSONNull(raw) {
		return NightSessionCue{}, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return NightSessionCue{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON object", field)}
	}
	if verr := rejectUnknownKeysUnder(fields, nightSessionCueKeys, field); verr != nil {
		return NightSessionCue{}, verr
	}

	name, verr := decodeRequiredString(fields, "name", field+".name")
	if verr != nil {
		return NightSessionCue{}, verr
	}

	role, verr := decodeRequiredEnum(fields, "role", field+".role", nightSessionCueRoleValues)
	if verr != nil {
		return NightSessionCue{}, verr
	}

	action, verr := decodeRequiredString(fields, "action", field+".action")
	if verr != nil {
		return NightSessionCue{}, verr
	}
	actionShow, ok := actionResolver(action)
	if !ok {
		return NightSessionCue{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: field + ".action",
			Detail: fmt.Sprintf("action %q does not resolve to an existing show.action object", action),
		}
	}
	if actionShow != sessionShow {
		return NightSessionCue{}, &ValidationError{
			Code: ValidationCodeCrossShowReference, Field: field + ".action",
			Detail: fmt.Sprintf("action %q belongs to show %q, not this session's own show %q (ADR-027: a Show is a namespace)", action, actionShow, sessionShow),
		}
	}

	offsetMs, verr := decodeRequiredInt(fields, "offsetMs", field+".offsetMs")
	if verr != nil {
		return NightSessionCue{}, verr
	}

	var fadeDurationMs *int
	if raw, present := fields["fadeDurationMs"]; present {
		if isJSONNull(raw) {
			return NightSessionCue{}, &ValidationError{
				Code: ValidationCodeFieldNull, Field: field + ".fadeDurationMs",
				Detail: fmt.Sprintf("%s.fadeDurationMs must not be null; omit it to leave it unset", field),
			}
		}
		v, verr := decodeRequiredNonNegativeInt(fields, "fadeDurationMs", field+".fadeDurationMs")
		if verr != nil {
			return NightSessionCue{}, verr
		}
		fadeDurationMs = &v
	}

	barrier, verr := decodeDefaultedBool(fields, "barrier", field+".barrier", false)
	if verr != nil {
		return NightSessionCue{}, verr
	}

	onFailure, verr := decodeDefaultedEnum(fields, "onFailure", field+".onFailure", NightSessionCueOnFailureDefault, nightSessionCueOnFailureValues)
	if verr != nil {
		return NightSessionCue{}, verr
	}

	return NightSessionCue{
		Name: name, Role: role, Action: action, OffsetMs: offsetMs,
		FadeDurationMs: fadeDurationMs, Barrier: barrier, OnFailure: onFailure,
	}, nil
}

// --- night.session-local decode helpers not already in showaction.go. ---

// decodeRequiredEnum is [decodeDefaultedEnum] with no default: the key
// must be present. Used where a field has no sensible implicit value
// (resume, itemTransition, a cue's role) — silently defaulting one of
// these would mean guessing a playback policy the operator never stated.
func decodeRequiredEnum(top map[string]json.RawMessage, key, field string, allowed map[string]bool) (string, *ValidationError) {
	if _, present := top[key]; !present {
		return "", &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	s, verr := decodeRequiredString(top, key, field)
	if verr != nil {
		return "", verr
	}
	if !allowed[s] {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s is not a recognized value", field)}
	}
	return s, nil
}

// decodeRequiredNonNegativeInt is [decodeRequiredInt] plus a >= 0 check,
// for a duration or delay field where a negative value is meaningless
// (blackoutHoldMs, blackoutAfterShowMs, crossfadeMs, fadeDurationMs) —
// distinct from offsetMs, which IS signed (RESTING-MODE.md §7.1: a cue may
// begin before its anchor).
func decodeRequiredNonNegativeInt(top map[string]json.RawMessage, key, field string) (int, *ValidationError) {
	v, verr := decodeRequiredInt(top, key, field)
	if verr != nil {
		return 0, verr
	}
	if v < 0 {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must not be negative", field)}
	}
	return v, nil
}

// decodeRequiredNumber reads key from top as a required, non-null JSON
// number (any sign), for maxGainDb — unlike decodeRequiredInt, a
// fractional gain in dB is a normal value, not a typo.
func decodeRequiredNumber(top map[string]json.RawMessage, key, field string) (float64, *ValidationError) {
	raw, present := top[key]
	if !present {
		return 0, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return 0, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON number", field)}
	}
	return f, nil
}

// decodeOptionalNonEmptyString reads key from top: absent means "" (the
// caller applies its own default); present-and-null is always an error;
// present-and-empty-string is ALSO an error, rather than being collapsed
// into "absent" the way decodeOptionalString's "" return value would
// otherwise read — found in review, for resting.endOfNightPlaylist:
// absent, explicit null, and explicit "" are three distinct author
// intents (CLAUDE.md's own standing rule), and only the first of the
// three means "use the default (the resting playlist)". An operator who
// writes "" almost certainly meant to clear a previous override, which
// this field has no way to represent — omitting the key already means
// "no override" — so "" is treated as a mistake, not a synonym for
// omission.
func decodeOptionalNonEmptyString(top map[string]json.RawMessage, key, field string) (string, *ValidationError) {
	raw, present := top[key]
	if !present {
		return "", nil
	}
	if isJSONNull(raw) {
		return "", &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null; omit it to use the default", field)}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a string", field)}
	}
	if s == "" {
		return "", &ValidationError{Code: ValidationCodeFieldEmpty, Field: field, Detail: fmt.Sprintf("%s must not be an empty string; omit the key entirely to use the default", field)}
	}
	return s, nil
}

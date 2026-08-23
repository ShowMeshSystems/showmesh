package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

// ShowCueConfigKind is config_objects.kind and config_revisions.kind for a
// show.cue object (TRACK-H-H1-SPEC.md section 2). Like show.action and
// show.surface, this is a collection: each object id is the Cue's own
// identifier, chosen by the caller.
const ShowCueConfigKind = "show.cue"

// maxCueNameRunes bounds show.cue.name, matching maxShowNameRunes and
// maxSurfaceNameRunes.
const maxCueNameRunes = 200

// The three members of show.cue.outputs.announcement.policy
// (TRACK-H-H1-SPEC.md section 2).
const (
	ShowCueAnnouncementPolicyDuck      = "duck"
	ShowCueAnnouncementPolicyMix       = "mix"
	ShowCueAnnouncementPolicyInterrupt = "interrupt"
)

var showCueAnnouncementPolicies = map[string]bool{
	ShowCueAnnouncementPolicyDuck:      true,
	ShowCueAnnouncementPolicyMix:       true,
	ShowCueAnnouncementPolicyInterrupt: true,
}

// maxLTCStartOffsetMillis bounds outputs.ltc.startOffsetMillis at 24 hours
// (TRACK-H-H1-SPEC.md section 2).
const maxLTCStartOffsetMillis = 24 * 60 * 60 * 1000

// minDuckGainDb and maxDuckGainDb bound
// outputs.announcement.duckGainDb: negative, bounded at -60 dB. maxDuckGainDb
// is exclusive (a gain of exactly 0 dB is not a duck).
const (
	minDuckGainDb = -60.0
	maxDuckGainDb = 0.0
)

// maxAnnouncementFadeMillis bounds outputs.announcement.fadeMillis
// (TRACK-H-H1-SPEC.md section 2).
const maxAnnouncementFadeMillis = 60000

// showCueTopLevelKeys is the complete set of keys DecodeShowCuePayload
// recognizes at the top level of the request body.
var showCueTopLevelKeys = map[string]bool{
	"show": true, "name": true, "outputs": true,
}

// The recognized key sets for each nested object. A typo inside a nested
// object is refused for the same reason a typo at the top level is: an
// ignored key reads as an applied one.
var (
	showCueOutputsKeys      = map[string]bool{"render": true, "audio": true, "ltc": true, "announcement": true}
	showCueRenderKeys       = map[string]bool{"sequence": true}
	showCueAudioKeys        = map[string]bool{"asset": true, "startOffsetMillis": true}
	showCueLTCKeys          = map[string]bool{"startOffsetMillis": true}
	showCueAnnouncementKeys = map[string]bool{"policy": true, "duckGainDb": true, "fadeMillis": true}
)

// ShowCuePayload is config_revisions.payload_json's decoded, VALIDATED
// shape for [ShowCueConfigKind]. It carries no resource-claim state of its
// own — see [DeriveShowCueClaims] — and no entry key: TRACK-H-H1-SPEC.md
// section 3.1 explains why a derived value is never stored alongside its
// own inputs.
type ShowCuePayload struct {
	Show    string         `json:"show"`
	Name    string         `json:"name"`
	Outputs ShowCueOutputs `json:"outputs"`
}

// ShowCueOutputs is show.cue.outputs. At least one member is non-nil; a
// Cue declaring nothing is an authoring mistake (TRACK-H-H1-SPEC.md
// section 2), not an empty-but-valid Cue.
type ShowCueOutputs struct {
	Render       *ShowCueRenderOutput       `json:"render,omitempty"`
	Audio        *ShowCueAudioOutput        `json:"audio,omitempty"`
	LTC          *ShowCueLTCOutput          `json:"ltc,omitempty"`
	Announcement *ShowCueAnnouncementOutput `json:"announcement,omitempty"`
}

// ShowCueRenderOutput is show.cue.outputs.render. Sequence is the LOGICAL
// sequence name, never an FSEQ filename or an asset id: nodes resolve the
// target-specific render asset from it (ADR-043, TRACK-H-H1-SPEC.md
// section 2), which is the whole reason runner and target detail stay off
// the Cue.
type ShowCueRenderOutput struct {
	Sequence string `json:"sequence"`
}

// ShowCueAudioOutput is show.cue.outputs.audio. Asset names a same-show
// audio asset; this seam does not validate its existence (not in
// TRACK-H-H1-SPEC.md section 4's refused list). StartOffsetMillis is where
// inside that asset the Cue begins, default 0, must be >= 0, and is
// bounded at 24 hours like outputs.ltc.startOffsetMillis.
type ShowCueAudioOutput struct {
	Asset             string `json:"asset"`
	StartOffsetMillis int    `json:"startOffsetMillis"`
}

// ShowCueLTCOutput is show.cue.outputs.ltc. StartOffsetMillis is H0.3's
// single LTC offset; its runtime meaning ("Cue LTC start offset + current
// Cue position") is H4's arithmetic, not this seam's.
type ShowCueLTCOutput struct {
	StartOffsetMillis int `json:"startOffsetMillis"`
}

// ShowCueAnnouncementOutput is show.cue.outputs.announcement. DuckGainDb is
// non-nil only when Policy is "duck" — refused on "mix" and "interrupt" at
// decode time, since an ignored field reads as an applied one.
type ShowCueAnnouncementOutput struct {
	Policy     string   `json:"policy"`
	DuckGainDb *float64 `json:"duckGainDb,omitempty"`
	FadeMillis int      `json:"fadeMillis"`
}

// EncodeShowCuePayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeShowCuePayload); this function does not re-validate.
func EncodeShowCuePayload(p ShowCuePayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.cue payload: %w", err)
	}
	return string(b), nil
}

// DecodeShowCuePayload parses and validates raw against
// TRACK-H-H1-SPEC.md section 2. showExists reports whether a "show"
// reference names an existing show config object — caller-supplied,
// matching showsurface.go's own showExists parameter, because this
// package has no store access.
func DecodeShowCuePayload(raw string, showExists func(string) bool) (ShowCuePayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return ShowCuePayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, showCueTopLevelKeys); verr != nil {
		return ShowCuePayload{}, verr
	}

	show, verr := decodeRequiredString(top, "show", "show")
	if verr != nil {
		return ShowCuePayload{}, verr
	}
	if verr := validateShowRef(show); verr != nil {
		return ShowCuePayload{}, verr
	}
	if !showExists(show) {
		return ShowCuePayload{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "show",
			Detail: fmt.Sprintf("show %q is not a configured show", show),
		}
	}

	name, verr := decodeRequiredString(top, "name", "name")
	if verr != nil {
		return ShowCuePayload{}, verr
	}
	if utf8.RuneCountInString(name) > maxCueNameRunes {
		return ShowCuePayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "name",
			Detail: fmt.Sprintf("name must be %d characters or fewer", maxCueNameRunes),
		}
	}

	outputs, verr := decodeShowCueOutputs(top)
	if verr != nil {
		return ShowCuePayload{}, verr
	}

	return ShowCuePayload{Show: show, Name: name, Outputs: outputs}, nil
}

// decodeShowCueOutputs decodes and validates the required "outputs" field.
// Absent, explicit null, and an explicitly empty object ({}) are three
// distinct refusals — see decodeRequiredObject for the first two and this
// function's own "at least one output" check for the third.
func decodeShowCueOutputs(top map[string]json.RawMessage) (ShowCueOutputs, *ValidationError) {
	fields, verr := decodeRequiredObject(top, "outputs", "outputs")
	if verr != nil {
		return ShowCueOutputs{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, showCueOutputsKeys, "outputs"); verr != nil {
		return ShowCueOutputs{}, verr
	}

	var outputs ShowCueOutputs

	if raw, present := fields["render"]; present {
		render, verr := decodeShowCueRenderOutput(raw)
		if verr != nil {
			return ShowCueOutputs{}, verr
		}
		outputs.Render = &render
	}

	if raw, present := fields["audio"]; present {
		audio, verr := decodeShowCueAudioOutput(raw)
		if verr != nil {
			return ShowCueOutputs{}, verr
		}
		outputs.Audio = &audio
	}

	if raw, present := fields["ltc"]; present {
		ltc, verr := decodeShowCueLTCOutput(raw)
		if verr != nil {
			return ShowCueOutputs{}, verr
		}
		outputs.LTC = &ltc
	}

	if raw, present := fields["announcement"]; present {
		announcement, verr := decodeShowCueAnnouncementOutput(raw)
		if verr != nil {
			return ShowCueOutputs{}, verr
		}
		outputs.Announcement = &announcement
	}

	if outputs.Render == nil && outputs.Audio == nil && outputs.LTC == nil && outputs.Announcement == nil {
		return ShowCueOutputs{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "outputs",
			Detail: "outputs must declare at least one of render, audio, ltc, or announcement",
		}
	}

	// A Cue declaring ltc or announcement must also declare audio: H0.3
	// (ADR-018's one clock domain) and H0.4 (an announcement with no audio
	// to play is a policy with no subject).
	if outputs.LTC != nil && outputs.Audio == nil {
		return ShowCueOutputs{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "outputs.ltc",
			Detail: "outputs.ltc requires outputs.audio: LTC and program audio are one clock domain",
		}
	}
	if outputs.Announcement != nil && outputs.Audio == nil {
		return ShowCueOutputs{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "outputs.announcement",
			Detail: "outputs.announcement requires outputs.audio: an announcement with no audio to play is a policy with no subject",
		}
	}

	return outputs, nil
}

func decodeShowCueRenderOutput(raw json.RawMessage) (ShowCueRenderOutput, *ValidationError) {
	fields, verr := decodeRequiredObjectFromRaw(raw, "outputs.render")
	if verr != nil {
		return ShowCueRenderOutput{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, showCueRenderKeys, "outputs.render"); verr != nil {
		return ShowCueRenderOutput{}, verr
	}
	sequence, verr := decodeRequiredString(fields, "sequence", "outputs.render.sequence")
	if verr != nil {
		return ShowCueRenderOutput{}, verr
	}
	return ShowCueRenderOutput{Sequence: sequence}, nil
}

func decodeShowCueAudioOutput(raw json.RawMessage) (ShowCueAudioOutput, *ValidationError) {
	fields, verr := decodeRequiredObjectFromRaw(raw, "outputs.audio")
	if verr != nil {
		return ShowCueAudioOutput{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, showCueAudioKeys, "outputs.audio"); verr != nil {
		return ShowCueAudioOutput{}, verr
	}
	asset, verr := decodeRequiredString(fields, "asset", "outputs.audio.asset")
	if verr != nil {
		return ShowCueAudioOutput{}, verr
	}
	// startOffsetMillis defaults to 0 (TRACK-H-H1-SPEC.md section 2),
	// matching mismatchPolicy and showmeshAudio.repeat's own
	// absent-takes-default treatment.
	startOffsetMillis, verr := decodeDefaultedNonNegativeInt(fields, "startOffsetMillis", "outputs.audio.startOffsetMillis", 0)
	if verr != nil {
		return ShowCueAudioOutput{}, verr
	}
	if startOffsetMillis > maxLTCStartOffsetMillis {
		return ShowCueAudioOutput{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "outputs.audio.startOffsetMillis",
			Detail: fmt.Sprintf("startOffsetMillis must be at most %d (24 hours)", maxLTCStartOffsetMillis),
		}
	}
	return ShowCueAudioOutput{Asset: asset, StartOffsetMillis: startOffsetMillis}, nil
}

// decodeDefaultedNonNegativeInt is [decodeRequiredNonNegativeInt] with a
// default: absent takes def; present (including present-and-null) goes
// through the same required-field validation, so an explicit null is
// still refused rather than silently treated as "use the default".
func decodeDefaultedNonNegativeInt(top map[string]json.RawMessage, key, field string, def int) (int, *ValidationError) {
	if _, present := top[key]; !present {
		return def, nil
	}
	return decodeRequiredNonNegativeInt(top, key, field)
}

func decodeShowCueLTCOutput(raw json.RawMessage) (ShowCueLTCOutput, *ValidationError) {
	fields, verr := decodeRequiredObjectFromRaw(raw, "outputs.ltc")
	if verr != nil {
		return ShowCueLTCOutput{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, showCueLTCKeys, "outputs.ltc"); verr != nil {
		return ShowCueLTCOutput{}, verr
	}
	startOffsetMillis, verr := decodeRequiredNonNegativeInt(fields, "startOffsetMillis", "outputs.ltc.startOffsetMillis")
	if verr != nil {
		return ShowCueLTCOutput{}, verr
	}
	if startOffsetMillis > maxLTCStartOffsetMillis {
		return ShowCueLTCOutput{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "outputs.ltc.startOffsetMillis",
			Detail: fmt.Sprintf("startOffsetMillis must be at most %d (24 hours)", maxLTCStartOffsetMillis),
		}
	}
	return ShowCueLTCOutput{StartOffsetMillis: startOffsetMillis}, nil
}

func decodeShowCueAnnouncementOutput(raw json.RawMessage) (ShowCueAnnouncementOutput, *ValidationError) {
	fields, verr := decodeRequiredObjectFromRaw(raw, "outputs.announcement")
	if verr != nil {
		return ShowCueAnnouncementOutput{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, showCueAnnouncementKeys, "outputs.announcement"); verr != nil {
		return ShowCueAnnouncementOutput{}, verr
	}

	policy, verr := decodeRequiredEnum(fields, "policy", "outputs.announcement.policy", showCueAnnouncementPolicies)
	if verr != nil {
		return ShowCueAnnouncementOutput{}, verr
	}

	_, duckGainPresent := fields["duckGainDb"]

	var duckGainDb *float64
	if policy == ShowCueAnnouncementPolicyDuck {
		if !duckGainPresent {
			return ShowCueAnnouncementOutput{}, &ValidationError{
				Code: ValidationCodeFieldRequired, Field: "outputs.announcement.duckGainDb",
				Detail: "outputs.announcement.duckGainDb is required when policy is \"duck\"",
			}
		}
		v, verr := decodeRequiredNumber(fields, "duckGainDb", "outputs.announcement.duckGainDb")
		if verr != nil {
			return ShowCueAnnouncementOutput{}, verr
		}
		if v >= maxDuckGainDb || v < minDuckGainDb {
			return ShowCueAnnouncementOutput{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "outputs.announcement.duckGainDb",
				Detail: fmt.Sprintf("duckGainDb must be negative and at least %g dB", minDuckGainDb),
			}
		}
		duckGainDb = &v
	} else if duckGainPresent {
		return ShowCueAnnouncementOutput{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "outputs.announcement.duckGainDb",
			Detail: "outputs.announcement.duckGainDb must be absent unless policy is \"duck\": an ignored field would read as an applied one",
		}
	}

	fadeMillis, verr := decodeRequiredNonNegativeInt(fields, "fadeMillis", "outputs.announcement.fadeMillis")
	if verr != nil {
		return ShowCueAnnouncementOutput{}, verr
	}
	if fadeMillis > maxAnnouncementFadeMillis {
		return ShowCueAnnouncementOutput{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "outputs.announcement.fadeMillis",
			Detail: fmt.Sprintf("fadeMillis must be at most %d", maxAnnouncementFadeMillis),
		}
	}

	return ShowCueAnnouncementOutput{Policy: policy, DuckGainDb: duckGainDb, FadeMillis: fadeMillis}, nil
}

// decodeRequiredObjectFromRaw is decodeRequiredObject for a
// json.RawMessage already known present (an outputs sub-object), rather
// than a key inside a parent map — outputs.render/audio/ltc/announcement
// are read via a presence check first (decodeShowCueOutputs needs to know
// whether the key exists before deciding whether to decode it at all), so
// this function starts from the value, not from a lookup.
func decodeRequiredObjectFromRaw(raw json.RawMessage, field string) (map[string]json.RawMessage, *ValidationError) {
	if isJSONNull(raw) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON object", field)}
	}
	return fields, nil
}

// --- H0.5 resource claim derivation (TRACK-H-H1-SPEC.md section 4: "H1
// does ship the claim derivation itself ... so readiness, activation, and
// dispatch all answer the question the same way instead of each deriving
// it again"). ---

// ShowCueClaimContext supplies the node and route identifiers a Cue's
// claims need but its payload does not carry (render surfaces are a
// Show-level fact expanded from Sequence, and audio/LTC/announcement
// routing is a deployment fact — neither belongs on the Cue itself). A
// field is read only when the matching output is declared and that output
// would actually produce a claim; [DeriveShowCueClaims] refuses to derive
// a claim from an unpopulated field rather than silently emitting one with
// an empty component, because two unrelated Cues would then collide on the
// identical claim.
type ShowCueClaimContext struct {
	// ProgramAudioNode and ProgramAudioRoute back "audio".
	ProgramAudioNode  string
	ProgramAudioRoute string
	// LTCNode and LTCRoute back "ltc".
	LTCNode  string
	LTCRoute string
	// AnnouncementNode backs "announcement".
	AnnouncementNode string
	// RenderSurfaceIDs backs "render", already expanded through the
	// Show's surfaces by the caller (TRACK-H-H0.5: "render-surface:
	// <surfaceId> ... expanded through the Show's surfaces").
	RenderSurfaceIDs []string
}

// ShowCueClaimKind is the exclusive resource kind a [ShowCueClaim] names
// (TRACK-H-H0.5's four claim rows).
type ShowCueClaimKind string

// The four members of ShowCueClaimKind.
const (
	ShowCueClaimKindProgramAudioRoute   ShowCueClaimKind = "program-audio-route"
	ShowCueClaimKindRenderSurface       ShowCueClaimKind = "render-surface"
	ShowCueClaimKindLTCOutput           ShowCueClaimKind = "ltc-output"
	ShowCueClaimKindAnnouncementSession ShowCueClaimKind = "announcement-session"
)

// ShowCueClaim is one H0.5 exclusive resource claim, comparable by value
// (==) rather than by its display string: three later seams (readiness,
// activation, dispatch) compare claims across Cues, and a colon-joined
// string is ambiguous about which component is which. Node and Resource
// are populated per Kind — see [DeriveShowCueClaims] — and either may be
// empty for a Kind that has no second component (render-surface has no
// node; announcement-session has no resource).
type ShowCueClaim struct {
	Kind     ShowCueClaimKind
	Node     string
	Resource string
}

// String renders c for display and logging only. It is NOT c's comparison
// key — callers compare [ShowCueClaim] values with ==, never by comparing
// String() output, because a display string can collide across distinct
// (Kind, Node, Resource) tuples in ways the struct itself cannot.
func (c ShowCueClaim) String() string {
	switch c.Kind {
	case ShowCueClaimKindRenderSurface:
		return fmt.Sprintf("%s:%s", c.Kind, c.Resource)
	case ShowCueClaimKindAnnouncementSession:
		return fmt.Sprintf("%s:%s", c.Kind, c.Node)
	default:
		return fmt.Sprintf("%s:%s:%s", c.Kind, c.Node, c.Resource)
	}
}

// DeriveShowCueClaims returns p's H0.5 resource claim set as a sorted,
// deterministic slice of typed claims, one per exclusive resource p's
// declared outputs would occupy if activated. It is pure: given the same p
// and ctx it always returns the same claims, and it never consults a store
// or any other concurrently-active Cue — comparing claims across Cues and
// Playlists is a readiness question (TRACK-H-H1-SPEC.md section 4), not
// this function's. It DOES validate ctx against p's declared outputs: a
// field ctx must supply for a claim p's outputs actually produce, left
// empty, is refused rather than silently producing a claim with an empty
// component.
func DeriveShowCueClaims(p ShowCuePayload, ctx ShowCueClaimContext) ([]ShowCueClaim, error) {
	var claims []ShowCueClaim

	// An announcement Cue claims only announcement-session, never
	// program-audio-route: its declared duck/mix/interrupt policy is a
	// relationship with whoever holds that route, not a seizure of it
	// (H0.5).
	if p.Outputs.Audio != nil && p.Outputs.Announcement == nil {
		if ctx.ProgramAudioNode == "" || ctx.ProgramAudioRoute == "" {
			return nil, fmt.Errorf("config: derive show.cue claims: outputs.audio is declared but ShowCueClaimContext.ProgramAudioNode/ProgramAudioRoute is empty")
		}
		claims = append(claims, ShowCueClaim{Kind: ShowCueClaimKindProgramAudioRoute, Node: ctx.ProgramAudioNode, Resource: ctx.ProgramAudioRoute})
	}
	if p.Outputs.Render != nil {
		if len(ctx.RenderSurfaceIDs) == 0 {
			return nil, fmt.Errorf("config: derive show.cue claims: outputs.render is declared but ShowCueClaimContext.RenderSurfaceIDs is empty")
		}
		seen := make(map[string]bool, len(ctx.RenderSurfaceIDs))
		for _, surfaceID := range ctx.RenderSurfaceIDs {
			if surfaceID == "" {
				return nil, fmt.Errorf("config: derive show.cue claims: outputs.render is declared but ShowCueClaimContext.RenderSurfaceIDs contains an empty id")
			}
			if seen[surfaceID] {
				continue
			}
			seen[surfaceID] = true
			claims = append(claims, ShowCueClaim{Kind: ShowCueClaimKindRenderSurface, Resource: surfaceID})
		}
	}
	if p.Outputs.LTC != nil {
		if ctx.LTCNode == "" || ctx.LTCRoute == "" {
			return nil, fmt.Errorf("config: derive show.cue claims: outputs.ltc is declared but ShowCueClaimContext.LTCNode/LTCRoute is empty")
		}
		claims = append(claims, ShowCueClaim{Kind: ShowCueClaimKindLTCOutput, Node: ctx.LTCNode, Resource: ctx.LTCRoute})
	}
	if p.Outputs.Announcement != nil {
		if ctx.AnnouncementNode == "" {
			return nil, fmt.Errorf("config: derive show.cue claims: outputs.announcement is declared but ShowCueClaimContext.AnnouncementNode is empty")
		}
		claims = append(claims, ShowCueClaim{Kind: ShowCueClaimKindAnnouncementSession, Node: ctx.AnnouncementNode})
	}

	sort.Slice(claims, func(i, j int) bool {
		if claims[i].Kind != claims[j].Kind {
			return claims[i].Kind < claims[j].Kind
		}
		if claims[i].Node != claims[j].Node {
			return claims[i].Node < claims[j].Node
		}
		return claims[i].Resource < claims[j].Resource
	})
	return claims, nil
}

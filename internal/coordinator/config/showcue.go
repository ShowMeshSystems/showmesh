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
	showCueAudioKeys        = map[string]bool{"asset": true, "startOffsetMillis": true, "target": true, "targets": true, "excludeNodes": true}
	showCueLTCKeys          = map[string]bool{"startOffsetMillis": true, "target": true}
	showCueAnnouncementKeys = map[string]bool{"policy": true, "duckGainDb": true, "fadeMillis": true, "target": true, "targets": true, "excludeNodes": true}
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
// bounded at 24 hours like outputs.ltc.startOffsetMillis. Targets is
// ADR-049's list of target audio.node ids (superseding ADR-045's single
// "target"): an empty list means resolve later to the installation's
// single program+ltc audio.node, exactly the one-node behavior this Cue
// had before ADR-045. Each present id must name an existing audio.node
// object (DecodeShowCuePayload's audioNodeExists callback), the same
// "refused against what actually exists" posture show.surface.node's
// nodeDeclared check uses. The wire form accepts the deprecated singular
// "target" as a one-element Targets and always re-encodes as "targets":
// see this type's own UnmarshalJSON for the stored-row read-back path
// DecodeShowCuePayload's validating decode never touches.
type ShowCueAudioOutput struct {
	Asset             string   `json:"asset"`
	StartOffsetMillis int      `json:"startOffsetMillis"`
	Targets           []string `json:"targets,omitempty"`
	// ExcludeNodes is ADR-049 decision 10's per-output exclude list: valid
	// only when Targets is empty, and only removes nodes from the show's
	// own audioNodes list. See [ResolveAudioNodes].
	ExcludeNodes []string `json:"excludeNodes,omitempty"`
}

// UnmarshalJSON accepts a legacy "target" string or a "targets" array for
// outputs.audio, so a row stored before ADR-049 still reads through the
// non-validating paths (api/showcue.go's jsonUnmarshalStrict GET,
// fallbackcompile, fppreconcile) that call plain json.Unmarshal against
// [ShowCuePayload] directly rather than DecodeShowCuePayload. See
// [decodeStoredShowCueTargets] for exactly which of
// [decodeShowCueTargets]'s refusals this path shares, and which it
// deliberately leaves to the validating decoder.
func (o *ShowCueAudioOutput) UnmarshalJSON(b []byte) error {
	var wire struct {
		Asset             string `json:"asset"`
		StartOffsetMillis int    `json:"startOffsetMillis"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	targets, err := decodeStoredShowCueTargets(fields, "outputs.audio")
	if err != nil {
		return err
	}
	excludeNodes, err := decodeStoredExcludeNodes(fields, "outputs.audio")
	if err != nil {
		return err
	}
	o.Asset = wire.Asset
	o.StartOffsetMillis = wire.StartOffsetMillis
	o.Targets = targets
	o.ExcludeNodes = excludeNodes
	return nil
}

// decodeStoredExcludeNodes is [decodeExcludeNodes]'s own non-validating
// stored-row read-back twin, mirroring [decodeStoredShowCueTargets]: it
// parses "excludeNodes" and refuses a repeated or empty-string entry, but
// never checks membership in the show's audioNodes list, because that
// check needs a live show payload this read-back path has no access to.
func decodeStoredExcludeNodes(fields map[string]json.RawMessage, path string) ([]string, error) {
	raw, present := fields["excludeNodes"]
	if !present || isJSONNull(raw) {
		return nil, nil
	}
	var excludeNodes []string
	if err := json.Unmarshal(raw, &excludeNodes); err != nil {
		return nil, fmt.Errorf("%s.excludeNodes must be a JSON array of strings: %w", path, err)
	}
	seen := make(map[string]bool, len(excludeNodes))
	for _, id := range excludeNodes {
		if id == "" {
			return nil, fmt.Errorf("%s.excludeNodes must not contain an empty string", path)
		}
		if seen[id] {
			return nil, fmt.Errorf("%s.excludeNodes must not repeat %q", path, id)
		}
		seen[id] = true
	}
	if len(excludeNodes) == 0 {
		return nil, nil
	}
	return excludeNodes, nil
}

// decodeStoredShowCueTargets is [decodeShowCueTargets]'s own compatibility
// twin for UnmarshalJSON's non-validating, stored-row read-back path
// (ADR-049). It shares three of that function's refusals: both keys
// present (including a present, null "targets", alongside "target"), a
// repeated id in "targets", and an empty string inside "targets". It
// leaves two refusals to the validating decoder alone: a present, null,
// or empty "target" reads as absent here (impossible from this package's
// own encoder, which omits an empty Target, but not impossible in older
// or hand-edited rows), resolving to the installation's default node
// exactly as the pre-ADR-049 plain string field's own empty value did,
// rather than becoming a one-element list matching no node; and a
// present, null "targets" alone reads as absent the same way, rather than
// the validating decoder's own refusal of that shape. It also never
// checks that an id names a configured audio.node, because that check
// needs a live audioNodeExists callback this read-back path has no
// access to.
func decodeStoredShowCueTargets(fields map[string]json.RawMessage, path string) ([]string, error) {
	targetRaw, targetPresent := fields["target"]
	_, targetsPresent := fields["targets"]
	if targetPresent && targetsPresent {
		return nil, fmt.Errorf("%s must not declare both %q and %q", path, "target", "targets")
	}

	if targetPresent {
		if isJSONNull(targetRaw) {
			return nil, nil
		}
		var target string
		if err := json.Unmarshal(targetRaw, &target); err != nil {
			return nil, fmt.Errorf("%s.target must be a string: %w", path, err)
		}
		if target == "" {
			return nil, nil
		}
		return []string{target}, nil
	}

	targetsRaw, present := fields["targets"]
	if !present || isJSONNull(targetsRaw) {
		return nil, nil
	}
	var targets []string
	if err := json.Unmarshal(targetsRaw, &targets); err != nil {
		return nil, fmt.Errorf("%s.targets must be a JSON array of strings: %w", path, err)
	}
	seen := make(map[string]bool, len(targets))
	for _, id := range targets {
		if id == "" {
			return nil, fmt.Errorf("%s.targets must not contain an empty string", path)
		}
		if seen[id] {
			return nil, fmt.Errorf("%s.targets must not repeat %q", path, id)
		}
		seen[id] = true
	}
	return targets, nil
}

// ShowCueLTCOutput is show.cue.outputs.ltc. StartOffsetMillis is H0.3's
// single LTC offset; its runtime meaning ("Cue LTC start offset + current
// Cue position") is H4's arithmetic, not this seam's. Target is ADR-045's
// optional target node; ADR-049 widened outputs.audio/announcement to a
// Targets list but deliberately kept outputs.ltc singular, see
// [decodeShowCueTarget]'s doc comment.
type ShowCueLTCOutput struct {
	StartOffsetMillis int    `json:"startOffsetMillis"`
	Target            string `json:"target,omitempty"`
}

// ShowCueAnnouncementOutput is show.cue.outputs.announcement. DuckGainDb is
// non-nil only when Policy is "duck" — refused on "mix" and "interrupt" at
// decode time, since an ignored field reads as an applied one. Targets is
// ADR-049's list of target audio.node ids, see
// [ShowCueAudioOutput.Targets]'s doc comment; the same rules apply here.
type ShowCueAnnouncementOutput struct {
	Policy       string   `json:"policy"`
	DuckGainDb   *float64 `json:"duckGainDb,omitempty"`
	FadeMillis   int      `json:"fadeMillis"`
	Targets      []string `json:"targets,omitempty"`
	ExcludeNodes []string `json:"excludeNodes,omitempty"`
}

// UnmarshalJSON is [ShowCueAudioOutput.UnmarshalJSON]'s sibling for
// outputs.announcement, see that method's doc comment.
func (o *ShowCueAnnouncementOutput) UnmarshalJSON(b []byte) error {
	var wire struct {
		Policy     string   `json:"policy"`
		DuckGainDb *float64 `json:"duckGainDb"`
		FadeMillis int      `json:"fadeMillis"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	targets, err := decodeStoredShowCueTargets(fields, "outputs.announcement")
	if err != nil {
		return err
	}
	excludeNodes, err := decodeStoredExcludeNodes(fields, "outputs.announcement")
	if err != nil {
		return err
	}
	o.Policy = wire.Policy
	o.DuckGainDb = wire.DuckGainDb
	o.FadeMillis = wire.FadeMillis
	o.Targets = targets
	o.ExcludeNodes = excludeNodes
	return nil
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
// package has no store access. audioNodeExists reports whether an
// outputs.audio/ltc/announcement "target" names an existing audio.node
// object (ADR-045) — caller-supplied for the identical reason, mirroring
// showsurface.go's nodeDeclared parameter. showAudioNodes, when non-nil,
// returns the named show's own audioNodes list and switches on
// ADR-049 decision 10's excludeNodes validation (membership in that list,
// and refusing an exclude list that empties it); nil is the non-validating,
// stored-row read-back posture [decodeStoredExcludeNodes] documents.
func DecodeShowCuePayload(raw string, showExists func(string) bool, audioNodeExists func(string) bool, showAudioNodes func(string) []string) (ShowCuePayload, *ValidationError) {
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

	var showAudioNodesList []string
	validateExcludeNodes := showAudioNodes != nil
	if validateExcludeNodes {
		showAudioNodesList = showAudioNodes(show)
	}

	outputs, verr := decodeShowCueOutputs(top, audioNodeExists, showAudioNodesList, validateExcludeNodes)
	if verr != nil {
		return ShowCuePayload{}, verr
	}

	return ShowCuePayload{Show: show, Name: name, Outputs: outputs}, nil
}

// decodeShowCueOutputs decodes and validates the required "outputs" field.
// Absent, explicit null, and an explicitly empty object ({}) are three
// distinct refusals — see decodeRequiredObject for the first two and this
// function's own "at least one output" check for the third. showAudioNodes
// and validateExcludeNodes are [DecodeShowCuePayload]'s own parameters,
// threaded down to each output's excludeNodes decode.
func decodeShowCueOutputs(top map[string]json.RawMessage, audioNodeExists func(string) bool, showAudioNodes []string, validateExcludeNodes bool) (ShowCueOutputs, *ValidationError) {
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
		audio, verr := decodeShowCueAudioOutput(raw, audioNodeExists, showAudioNodes, validateExcludeNodes)
		if verr != nil {
			return ShowCueOutputs{}, verr
		}
		outputs.Audio = &audio
	}

	if raw, present := fields["ltc"]; present {
		ltc, verr := decodeShowCueLTCOutput(raw, audioNodeExists)
		if verr != nil {
			return ShowCueOutputs{}, verr
		}
		outputs.LTC = &ltc
	}

	if raw, present := fields["announcement"]; present {
		announcement, verr := decodeShowCueAnnouncementOutput(raw, audioNodeExists, showAudioNodes, validateExcludeNodes)
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

	// A Cue must not declare both ltc and announcement. A node has exactly
	// one LTC generator, tied to the program-audio clock domain
	// (ADR-018) — the show session's own Start/Seek path
	// (internal/agent/cueactivationaudio.go's activateAudio). An
	// announcement Cue runs in a SEPARATE session
	// ([cueactivation.AnnouncementSessionID], never the show session), so
	// there is no program-audio clock for its own ltc declaration to run
	// from. Refusing the combination here, at authoring, is TRACK-H-cues-and-playlists.md
	// section H5 build item 5's own ruling: "refuse the combination
	// visibly rather than implementing a second LTC owner" — a Cue that
	// declares both would otherwise reach a node whose activateAudio
	// silently never starts LTC for it (startLTCLocked gates on the show
	// role), which is exactly the silent drop that ruling forbids.
	if outputs.LTC != nil && outputs.Announcement != nil {
		return ShowCueOutputs{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "outputs.ltc",
			Detail: "outputs.ltc must not be combined with outputs.announcement. A node has one LTC generator, and it belongs to the program audio, not the announcement session.",
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

// decodeShowCueTarget decodes and validates outputs.ltc's optional
// "target" field (ADR-045; ADR-049 keeps outputs.ltc singular). Absent
// means "", resolve later to the installation's single program+ltc
// audio.node; explicit null and an explicit empty string are refused by
// [decodeOptionalNonEmptyString], and a present, non-empty value must name
// an existing audio.node object.
func decodeShowCueTarget(fields map[string]json.RawMessage, path string, audioNodeExists func(string) bool) (string, *ValidationError) {
	target, verr := decodeOptionalNonEmptyString(fields, "target", path+".target")
	if verr != nil {
		return "", verr
	}
	if target == "" {
		return "", nil
	}
	if !audioNodeExists(target) {
		return "", &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: path + ".target",
			Detail: fmt.Sprintf("target %q is not a configured audio.node", target),
		}
	}
	return target, nil
}

// ValidationCodeShowCueTargetDuplicate: the same audio.node id appears
// more than once in one output's own "targets" list (ADR-049). Its own
// code, on ValidationCodeEmergencyStopActionDuplicate's precedent, so a
// caller can tell "duplicate" apart from "unknown" without parsing prose.
const ValidationCodeShowCueTargetDuplicate = "show-cue-target-duplicate"

// decodeShowCueTargets decodes and validates the "target"/"targets" pair
// shared by outputs.audio and outputs.announcement (ADR-049, superseding
// ADR-045's single outputs.audio/announcement "target"; outputs.ltc keeps
// [decodeShowCueTarget]'s singular form unchanged). Exactly one of the two
// keys may be present: "target" (a non-empty string) decodes as a
// one-element list, "targets" (a JSON array of non-empty strings, which
// may be empty) decodes as given; both present is refused naming both
// keys. Absent, or an empty "targets", returns a nil slice, resolved later
// to the installation's default node, the pre-ADR-045 one-node behavior.
// A repeated id, or one naming no configured audio.node, is refused.
func decodeShowCueTargets(fields map[string]json.RawMessage, path string, audioNodeExists func(string) bool) ([]string, *ValidationError) {
	_, targetPresent := fields["target"]
	_, targetsPresent := fields["targets"]
	if targetPresent && targetsPresent {
		return nil, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: path,
			Detail: fmt.Sprintf(`%s must not declare both "target" and "targets": "target" is the deprecated one-element form of "targets"`, path),
		}
	}

	if targetPresent {
		// decodeShowCueTarget always errors on an absent-shaped result
		// here: "target" is known present, so it either refuses (null,
		// empty, unknown) or returns a non-empty, resolvable id.
		target, verr := decodeShowCueTarget(fields, path, audioNodeExists)
		if verr != nil {
			return nil, verr
		}
		return []string{target}, nil
	}

	targetsField := path + ".targets"
	raw, present := fields["targets"]
	if !present {
		return nil, nil
	}
	if isJSONNull(raw) {
		return nil, &ValidationError{
			Code: ValidationCodeFieldNull, Field: targetsField,
			Detail: targetsField + " must not be null; omit it, or use an empty array, to resolve the default target",
		}
	}
	var targets []string
	if err := json.Unmarshal(raw, &targets); err != nil {
		return nil, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: targetsField,
			Detail: targetsField + " must be a JSON array of audio.node ids",
		}
	}
	seen := make(map[string]bool, len(targets))
	for i, id := range targets {
		itemField := fmt.Sprintf("%s[%d]", targetsField, i)
		if id == "" {
			return nil, &ValidationError{Code: ValidationCodeFieldEmpty, Field: itemField, Detail: itemField + " must not be empty"}
		}
		if seen[id] {
			return nil, &ValidationError{
				Code: ValidationCodeShowCueTargetDuplicate, Field: itemField,
				Detail: fmt.Sprintf("target %q is already listed earlier in %s", id, targetsField),
			}
		}
		seen[id] = true
		if !audioNodeExists(id) {
			return nil, &ValidationError{
				Code: ValidationCodeFieldUnknownReference, Field: itemField,
				Detail: fmt.Sprintf("target %q is not a configured audio.node", id),
			}
		}
	}
	return targets, nil
}

func decodeShowCueAudioOutput(raw json.RawMessage, audioNodeExists func(string) bool, showAudioNodes []string, validateExcludeNodes bool) (ShowCueAudioOutput, *ValidationError) {
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
	targets, verr := decodeShowCueTargets(fields, "outputs.audio", audioNodeExists)
	if verr != nil {
		return ShowCueAudioOutput{}, verr
	}
	excludeNodes, verr := decodeExcludeNodes(fields, "outputs.audio", targets, showAudioNodes, validateExcludeNodes)
	if verr != nil {
		return ShowCueAudioOutput{}, verr
	}
	return ShowCueAudioOutput{Asset: asset, StartOffsetMillis: startOffsetMillis, Targets: targets, ExcludeNodes: excludeNodes}, nil
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

func decodeShowCueLTCOutput(raw json.RawMessage, audioNodeExists func(string) bool) (ShowCueLTCOutput, *ValidationError) {
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
	target, verr := decodeShowCueTarget(fields, "outputs.ltc", audioNodeExists)
	if verr != nil {
		return ShowCueLTCOutput{}, verr
	}
	return ShowCueLTCOutput{StartOffsetMillis: startOffsetMillis, Target: target}, nil
}

func decodeShowCueAnnouncementOutput(raw json.RawMessage, audioNodeExists func(string) bool, showAudioNodes []string, validateExcludeNodes bool) (ShowCueAnnouncementOutput, *ValidationError) {
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

	targets, verr := decodeShowCueTargets(fields, "outputs.announcement", audioNodeExists)
	if verr != nil {
		return ShowCueAnnouncementOutput{}, verr
	}
	excludeNodes, verr := decodeExcludeNodes(fields, "outputs.announcement", targets, showAudioNodes, validateExcludeNodes)
	if verr != nil {
		return ShowCueAnnouncementOutput{}, verr
	}

	return ShowCueAnnouncementOutput{Policy: policy, DuckGainDb: duckGainDb, FadeMillis: fadeMillis, Targets: targets, ExcludeNodes: excludeNodes}, nil
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

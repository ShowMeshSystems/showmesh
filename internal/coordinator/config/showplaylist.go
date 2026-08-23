package config

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// ShowPlaylistConfigKind is config_objects.kind and config_revisions.kind
// for a show.playlist object (TRACK-H-H1-SPEC.md section 3). Like
// show.cue, this is a collection: each object id is the Playlist's own
// identifier, chosen by the caller.
const ShowPlaylistConfigKind = "show.playlist"

// The two members of show.playlist.runner this seam accepts.
const (
	ShowPlaylistRunnerFPP           = "fpp"
	ShowPlaylistRunnerShowmeshAudio = "showmesh-audio"

	// showPlaylistRunnerShowmesh is NOT a member of showPlaylistRunners. It
	// exists only so decodeShowPlaylistPayload can name it explicitly and
	// answer with ValidationCodeNotImplemented rather than the generic
	// ValidationCodeFieldInvalid every other unrecognized runner gets —
	// TRACK-H-H1-SPEC.md section 3: "The future general showmesh runner is
	// reserved by ADR-043 and is refused here: a runner nothing implements
	// must not be authorable."
	showPlaylistRunnerShowmesh = "showmesh"
)

var showPlaylistRunners = map[string]bool{
	ShowPlaylistRunnerFPP:           true,
	ShowPlaylistRunnerShowmeshAudio: true,
}

// The three members of show.playlist.mismatchPolicy, and its default
// (H0.2).
const (
	ShowPlaylistMismatchPolicyHold            = "hold"
	ShowPlaylistMismatchPolicyBlackAndSilence = "blackAndSilence"
	ShowPlaylistMismatchPolicySafeCue         = "safeCue"
	ShowPlaylistMismatchPolicyDefault         = ShowPlaylistMismatchPolicyHold
)

var showPlaylistMismatchPolicies = map[string]bool{
	ShowPlaylistMismatchPolicyHold:            true,
	ShowPlaylistMismatchPolicyBlackAndSilence: true,
	ShowPlaylistMismatchPolicySafeCue:         true,
}

// The two members of show.playlist.showmeshAudio.repeat, and its default.
const (
	ShowPlaylistShowmeshAudioRepeatNone    = "none"
	ShowPlaylistShowmeshAudioRepeatAll     = "all"
	ShowPlaylistShowmeshAudioRepeatDefault = ShowPlaylistShowmeshAudioRepeatNone
)

var showPlaylistShowmeshAudioRepeats = map[string]bool{
	ShowPlaylistShowmeshAudioRepeatNone: true,
	ShowPlaylistShowmeshAudioRepeatAll:  true,
}

// maxPlaylistNameRunes bounds show.playlist.name, matching maxCueNameRunes.
const maxPlaylistNameRunes = 200

// maxPlaylistEntryPosition bounds entries[].fpp.position. An FPP playlist
// position is a small ordinal, never anything close to this ceiling; the
// bound exists so a value decodeRequiredNonNegativeInt's float round trip
// could otherwise mangle is refused outright rather than silently stored
// as something the operator never wrote (see decodeRequiredInt's own
// fix for the underlying defect).
const maxPlaylistEntryPosition = 100000

// showPlaylistTopLevelKeys is the complete set of keys
// DecodeShowPlaylistPayload recognizes at the top level of the request
// body.
var showPlaylistTopLevelKeys = map[string]bool{
	"show": true, "name": true, "runner": true, "mismatchPolicy": true,
	"safeCueRef": true, "fpp": true, "showmeshAudio": true, "entries": true,
}

// The recognized key sets for each nested object.
var (
	showPlaylistFPPKeys      = map[string]bool{"instanceUuid": true, "playlistName": true, "playlistHash": true}
	showPlaylistShowmeshKeys = map[string]bool{"repeat": true}
	showPlaylistEntryKeys    = map[string]bool{"id": true, "cue": true, "fpp": true}
	showPlaylistEntryFPPKeys = map[string]bool{"section": true, "position": true, "expectedSequenceFilename": true, "expectedMediaFilename": true}
)

// ShowPlaylistPayload is config_revisions.payload_json's decoded,
// VALIDATED shape for [ShowPlaylistConfigKind].
type ShowPlaylistPayload struct {
	Show           string                     `json:"show"`
	Name           string                     `json:"name"`
	Runner         string                     `json:"runner"`
	MismatchPolicy string                     `json:"mismatchPolicy,omitempty"`
	SafeCueRef     string                     `json:"safeCueRef,omitempty"`
	FPP            *ShowPlaylistFPPBinding    `json:"fpp,omitempty"`
	ShowmeshAudio  *ShowPlaylistShowmeshAudio `json:"showmeshAudio,omitempty"`
	Entries        []ShowPlaylistEntry        `json:"entries"`
}

// ShowPlaylistFPPBinding is show.playlist.fpp: the imported FPP playlist
// identity this Playlist is bound to. Required when Runner is "fpp",
// refused otherwise.
type ShowPlaylistFPPBinding struct {
	InstanceUUID string `json:"instanceUuid"`
	PlaylistName string `json:"playlistName"`
	// PlaylistHash is the imported canonical hash
	// (FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 1.3), 64 lowercase hex
	// characters. This seam validates its shape only; H2's import computes
	// it.
	PlaylistHash string `json:"playlistHash"`
}

// ShowPlaylistShowmeshAudio is show.playlist.showmeshAudio. Permitted only
// when Runner is "showmesh-audio".
type ShowPlaylistShowmeshAudio struct {
	Repeat string `json:"repeat"`
}

// ShowPlaylistEntry is one entry of show.playlist.entries.
type ShowPlaylistEntry struct {
	ID  string                `json:"id"`
	Cue string                `json:"cue"`
	FPP *ShowPlaylistEntryFPP `json:"fpp,omitempty"`
}

// ShowPlaylistEntryFPP is one entry's fpp binding. Section may be empty or
// absent, both meaning an FPP playlist's default, unnamed section.
// Position is zero-based, must be >= 0, and is bounded at
// maxPlaylistEntryPosition. ExpectedSequenceFilename and
// ExpectedMediaFilename are optional validation evidence, never identity
// (TRACK-H-H1-SPEC.md section 3.1: identity is derived, never authored).
type ShowPlaylistEntryFPP struct {
	Section                  string `json:"section"`
	Position                 int    `json:"position"`
	ExpectedSequenceFilename string `json:"expectedSequenceFilename,omitempty"`
	ExpectedMediaFilename    string `json:"expectedMediaFilename,omitempty"`
}

// EncodeShowPlaylistPayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeShowPlaylistPayload); this function does not re-validate.
func EncodeShowPlaylistPayload(p ShowPlaylistPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.playlist payload: %w", err)
	}
	return string(b), nil
}

// DecodeShowPlaylistPayload parses and validates raw against
// TRACK-H-H1-SPEC.md section 3. showExists reports whether "show" names an
// existing show config object. resolveCue reports whether a "cue"
// reference (entries[].cue, safeCueRef) names an existing show.cue object
// and, when it does, that Cue's own "show" — a caller-supplied lookup, in
// showmacro.go's resolveAction style, never something this package
// fetches itself (this package must never import api or store —
// importgraph_test.go enforces it).
func DecodeShowPlaylistPayload(raw string, showExists func(string) bool, resolveCue func(cueID string) (show string, ok bool)) (ShowPlaylistPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return ShowPlaylistPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, showPlaylistTopLevelKeys); verr != nil {
		return ShowPlaylistPayload{}, verr
	}

	show, verr := decodeRequiredString(top, "show", "show")
	if verr != nil {
		return ShowPlaylistPayload{}, verr
	}
	if verr := validateShowRef(show); verr != nil {
		return ShowPlaylistPayload{}, verr
	}
	if !showExists(show) {
		return ShowPlaylistPayload{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "show",
			Detail: fmt.Sprintf("show %q is not a configured show", show),
		}
	}

	name, verr := decodeRequiredString(top, "name", "name")
	if verr != nil {
		return ShowPlaylistPayload{}, verr
	}
	if utf8.RuneCountInString(name) > maxPlaylistNameRunes {
		return ShowPlaylistPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "name",
			Detail: fmt.Sprintf("name must be %d characters or fewer", maxPlaylistNameRunes),
		}
	}

	runner, verr := decodeRequiredString(top, "runner", "runner")
	if verr != nil {
		return ShowPlaylistPayload{}, verr
	}
	if runner == showPlaylistRunnerShowmesh {
		return ShowPlaylistPayload{}, &ValidationError{
			Code: ValidationCodeNotImplemented, Field: "runner",
			Detail: "runner \"showmesh\" is reserved but not yet implemented; a runner nothing implements must not be authorable",
		}
	}
	if !showPlaylistRunners[runner] {
		return ShowPlaylistPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "runner",
			Detail: "runner must be one of fpp or showmesh-audio",
		}
	}

	mismatchPolicy, safeCueRef, verr := decodeShowPlaylistMismatchPolicy(top, runner, show, resolveCue)
	if verr != nil {
		return ShowPlaylistPayload{}, verr
	}

	fppBinding, verr := decodeShowPlaylistFPPBinding(top, runner)
	if verr != nil {
		return ShowPlaylistPayload{}, verr
	}

	showmeshAudio, verr := decodeShowPlaylistShowmeshAudio(top, runner)
	if verr != nil {
		return ShowPlaylistPayload{}, verr
	}

	entries, verr := decodeShowPlaylistEntries(top, runner, show, resolveCue)
	if verr != nil {
		return ShowPlaylistPayload{}, verr
	}

	return ShowPlaylistPayload{
		Show: show, Name: name, Runner: runner,
		MismatchPolicy: mismatchPolicy, SafeCueRef: safeCueRef,
		FPP: fppBinding, ShowmeshAudio: showmeshAudio, Entries: entries,
	}, nil
}

// decodeShowPlaylistMismatchPolicy decodes "mismatchPolicy" and
// "safeCueRef" together: they are paired fields, checked as a pair rather
// than independently (H0.2 / TRACK-H-H1-SPEC.md section 3).
// mismatchPolicy is permitted only when runner is "fpp"; safeCueRef is
// required when the resolved policy is "safeCue" and refused otherwise,
// and must name a same-show Cue.
func decodeShowPlaylistMismatchPolicy(top map[string]json.RawMessage, runner, playlistShow string, resolveCue func(string) (string, bool)) (string, string, *ValidationError) {
	_, present := top["mismatchPolicy"]
	if present && runner != ShowPlaylistRunnerFPP {
		return "", "", &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "mismatchPolicy",
			Detail: "mismatchPolicy must be absent unless runner is \"fpp\": a ShowMesh-run playlist has no external authority to contradict it",
		}
	}

	var mismatchPolicy string
	if runner == ShowPlaylistRunnerFPP {
		v, verr := decodeDefaultedEnum(top, "mismatchPolicy", "mismatchPolicy", ShowPlaylistMismatchPolicyDefault, showPlaylistMismatchPolicies)
		if verr != nil {
			return "", "", verr
		}
		mismatchPolicy = v
	}

	_, safeCuePresent := top["safeCueRef"]
	if mismatchPolicy != ShowPlaylistMismatchPolicySafeCue {
		if safeCuePresent {
			return "", "", &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "safeCueRef",
				Detail: "safeCueRef must be absent unless mismatchPolicy is \"safeCue\"",
			}
		}
		return mismatchPolicy, "", nil
	}

	if !safeCuePresent {
		return "", "", &ValidationError{
			Code: ValidationCodeFieldRequired, Field: "safeCueRef",
			Detail: "safeCueRef is required when mismatchPolicy is \"safeCue\"",
		}
	}
	safeCueRef, verr := decodeRequiredString(top, "safeCueRef", "safeCueRef")
	if verr != nil {
		return "", "", verr
	}
	cueShow, ok := resolveCue(safeCueRef)
	if !ok {
		return "", "", &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "safeCueRef",
			Detail: fmt.Sprintf("safeCueRef %q does not resolve to an existing show.cue object", safeCueRef),
		}
	}
	if cueShow != playlistShow {
		return "", "", &ValidationError{
			Code: ValidationCodeCrossShowReference, Field: "safeCueRef",
			Detail: fmt.Sprintf("safeCueRef %q belongs to show %q, not this playlist's own show %q", safeCueRef, cueShow, playlistShow),
		}
	}

	return mismatchPolicy, safeCueRef, nil
}

// decodeShowPlaylistFPPBinding decodes the required-iff-fpp-runner "fpp"
// field.
func decodeShowPlaylistFPPBinding(top map[string]json.RawMessage, runner string) (*ShowPlaylistFPPBinding, *ValidationError) {
	_, present := top["fpp"]
	if runner != ShowPlaylistRunnerFPP {
		if present {
			return nil, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "fpp",
				Detail: "fpp must be absent unless runner is \"fpp\"",
			}
		}
		return nil, nil
	}
	if !present {
		return nil, &ValidationError{
			Code: ValidationCodeFieldRequired, Field: "fpp",
			Detail: "fpp is required when runner is \"fpp\"",
		}
	}

	fields, verr := decodeRequiredObject(top, "fpp", "fpp")
	if verr != nil {
		return nil, verr
	}
	if verr := rejectUnknownKeysUnder(fields, showPlaylistFPPKeys, "fpp"); verr != nil {
		return nil, verr
	}

	instanceUUID, verr := decodeRequiredString(fields, "instanceUuid", "fpp.instanceUuid")
	if verr != nil {
		return nil, verr
	}
	playlistName, verr := decodeRequiredString(fields, "playlistName", "fpp.playlistName")
	if verr != nil {
		return nil, verr
	}
	playlistHash, verr := decodeRequiredString(fields, "playlistHash", "fpp.playlistHash")
	if verr != nil {
		return nil, verr
	}
	if !fppidentity.IsHash64(playlistHash) {
		return nil, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "fpp.playlistHash",
			Detail: "playlistHash must be exactly 64 lowercase hex characters",
		}
	}

	return &ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: playlistName, PlaylistHash: playlistHash}, nil
}

// decodeShowPlaylistShowmeshAudio decodes the permitted-only-for-
// showmesh-audio-runner "showmeshAudio" field. Unlike "fpp", it is never
// required: absent under runner "showmesh-audio" takes its documented
// default (repeat "none").
func decodeShowPlaylistShowmeshAudio(top map[string]json.RawMessage, runner string) (*ShowPlaylistShowmeshAudio, *ValidationError) {
	_, present := top["showmeshAudio"]
	if runner != ShowPlaylistRunnerShowmeshAudio {
		if present {
			return nil, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "showmeshAudio",
				Detail: "showmeshAudio must be absent unless runner is \"showmesh-audio\"",
			}
		}
		return nil, nil
	}

	if !present {
		return &ShowPlaylistShowmeshAudio{Repeat: ShowPlaylistShowmeshAudioRepeatDefault}, nil
	}

	fields, verr := decodeRequiredObject(top, "showmeshAudio", "showmeshAudio")
	if verr != nil {
		return nil, verr
	}
	if verr := rejectUnknownKeysUnder(fields, showPlaylistShowmeshKeys, "showmeshAudio"); verr != nil {
		return nil, verr
	}
	repeat, verr := decodeDefaultedEnum(fields, "repeat", "showmeshAudio.repeat", ShowPlaylistShowmeshAudioRepeatDefault, showPlaylistShowmeshAudioRepeats)
	if verr != nil {
		return nil, verr
	}
	return &ShowPlaylistShowmeshAudio{Repeat: repeat}, nil
}

// decodeShowPlaylistEntries decodes the required, non-empty "entries"
// array. Duplicate entry ids and duplicate (section, position) pairs are
// both refused: the first because an entry id must be unique to be
// addressable, the second because two entries at the same section and
// position derive the identical entry key (TRACK-H-H1-SPEC.md section
// 3.1) and no runtime evidence could ever tell them apart. Two entries
// referencing the SAME Cue at different positions is accepted — that is
// the duplicate-filename case the entry key exists to resolve.
func decodeShowPlaylistEntries(top map[string]json.RawMessage, runner, playlistShow string, resolveCue func(string) (string, bool)) ([]ShowPlaylistEntry, *ValidationError) {
	entriesRaw, present := top["entries"]
	if !present {
		return nil, &ValidationError{Code: ValidationCodeFieldRequired, Field: "entries", Detail: "entries is required"}
	}
	if isJSONNull(entriesRaw) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: "entries", Detail: "entries must not be null"}
	}
	var rawEntries []json.RawMessage
	if err := json.Unmarshal(entriesRaw, &rawEntries); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "entries", Detail: "entries must be a JSON array"}
	}
	if len(rawEntries) == 0 {
		return nil, &ValidationError{Code: ValidationCodeEntriesEmpty, Field: "entries", Detail: "entries must contain at least one entry"}
	}

	entries := make([]ShowPlaylistEntry, 0, len(rawEntries))
	seenIDs := make(map[string]bool, len(rawEntries))
	type sectionPosition struct {
		section  string
		position int
	}
	seenPositions := make(map[sectionPosition]bool, len(rawEntries))

	for i, rawEntry := range rawEntries {
		field := fmt.Sprintf("entries[%d]", i)
		entry, verr := decodeShowPlaylistEntry(rawEntry, field, runner, playlistShow, resolveCue)
		if verr != nil {
			return nil, verr
		}
		if seenIDs[entry.ID] {
			return nil, &ValidationError{
				Code: ValidationCodeItemIDDuplicate, Field: field + ".id",
				Detail: fmt.Sprintf("entry id %q is used more than once", entry.ID),
			}
		}
		seenIDs[entry.ID] = true

		if entry.FPP != nil {
			key := sectionPosition{section: entry.FPP.Section, position: entry.FPP.Position}
			if seenPositions[key] {
				return nil, &ValidationError{
					Code: ValidationCodeEntryPositionDuplicate, Field: field + ".fpp.position",
					Detail: fmt.Sprintf("section %q position %d is used by more than one entry", entry.FPP.Section, entry.FPP.Position),
				}
			}
			seenPositions[key] = true
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

func decodeShowPlaylistEntry(raw json.RawMessage, field, runner, playlistShow string, resolveCue func(string) (string, bool)) (ShowPlaylistEntry, *ValidationError) {
	if isJSONNull(raw) {
		return ShowPlaylistEntry{}, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ShowPlaylistEntry{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON object", field)}
	}
	if verr := rejectUnknownKeysUnder(fields, showPlaylistEntryKeys, field); verr != nil {
		return ShowPlaylistEntry{}, verr
	}

	id, verr := decodeRequiredString(fields, "id", field+".id")
	if verr != nil {
		return ShowPlaylistEntry{}, verr
	}

	cue, verr := decodeRequiredString(fields, "cue", field+".cue")
	if verr != nil {
		return ShowPlaylistEntry{}, verr
	}
	cueShow, ok := resolveCue(cue)
	if !ok {
		return ShowPlaylistEntry{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: field + ".cue",
			Detail: fmt.Sprintf("cue %q does not resolve to an existing show.cue object", cue),
		}
	}
	if cueShow != playlistShow {
		return ShowPlaylistEntry{}, &ValidationError{
			Code: ValidationCodeCrossShowReference, Field: field + ".cue",
			Detail: fmt.Sprintf("cue %q belongs to show %q, not this playlist's own show %q", cue, cueShow, playlistShow),
		}
	}

	entryFPP, verr := decodeShowPlaylistEntryFPP(fields, field, runner)
	if verr != nil {
		return ShowPlaylistEntry{}, verr
	}

	return ShowPlaylistEntry{ID: id, Cue: cue, FPP: entryFPP}, nil
}

func decodeShowPlaylistEntryFPP(fields map[string]json.RawMessage, field, runner string) (*ShowPlaylistEntryFPP, *ValidationError) {
	_, present := fields["fpp"]
	if runner != ShowPlaylistRunnerFPP {
		if present {
			return nil, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: field + ".fpp",
				Detail: "fpp must be absent unless runner is \"fpp\"",
			}
		}
		return nil, nil
	}
	if !present {
		return nil, &ValidationError{
			Code: ValidationCodeFieldRequired, Field: field + ".fpp",
			Detail: "fpp is required when runner is \"fpp\"",
		}
	}

	fppFields, verr := decodeRequiredObject(fields, "fpp", field+".fpp")
	if verr != nil {
		return nil, verr
	}
	if verr := rejectUnknownKeysUnder(fppFields, showPlaylistEntryFPPKeys, field+".fpp"); verr != nil {
		return nil, verr
	}

	// section absent means the empty, unnamed default FPP section — H2
	// resolves observations whose section is often that default, so
	// requiring the operator to spell it out would make the common case
	// the awkward one.
	section, verr := decodeOptionalString(fppFields, "section", field+".fpp.section")
	if verr != nil {
		return nil, verr
	}

	position, verr := decodeRequiredNonNegativeInt(fppFields, "position", field+".fpp.position")
	if verr != nil {
		return nil, verr
	}
	if position > maxPlaylistEntryPosition {
		return nil, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: field + ".fpp.position",
			Detail: fmt.Sprintf("position must be at most %d", maxPlaylistEntryPosition),
		}
	}

	expectedSequenceFilename, verr := decodeOptionalString(fppFields, "expectedSequenceFilename", field+".fpp.expectedSequenceFilename")
	if verr != nil {
		return nil, verr
	}
	expectedMediaFilename, verr := decodeOptionalString(fppFields, "expectedMediaFilename", field+".fpp.expectedMediaFilename")
	if verr != nil {
		return nil, verr
	}

	return &ShowPlaylistEntryFPP{
		Section: section, Position: position,
		ExpectedSequenceFilename: expectedSequenceFilename,
		ExpectedMediaFilename:    expectedMediaFilename,
	}, nil
}

// --- Entry key derivation (TRACK-H-H1-SPEC.md section 3.1: "This seam
// therefore adds an exported derivation over the stored payload ... and
// stores nothing"). ---

// DerivePlaylistEntryKey computes the deterministic FPP entry key for the
// entry identified by entryID within p, from p's fpp binding and that
// entry's own fpp.section/fpp.position, reusing pkg/fppidentity rather
// than re-implementing RFC 8785. Taking an id and looking the entry up in
// p — rather than taking a caller-supplied [ShowPlaylistEntry] directly —
// refuses an entry that does not actually belong to p, which a
// caller-assembled entry value could otherwise smuggle past this
// function. It refuses to derive for a non-"fpp" runner, an unknown
// entryID, or an entry with no fpp binding: none of those carries the
// five identifying fields the key is a function of.
func DerivePlaylistEntryKey(p ShowPlaylistPayload, entryID string) (string, error) {
	if p.Runner != ShowPlaylistRunnerFPP {
		return "", fmt.Errorf("config: entry key derivation requires runner %q, got runner %q", ShowPlaylistRunnerFPP, p.Runner)
	}
	if p.FPP == nil {
		return "", fmt.Errorf("config: entry key derivation requires runner %q to carry an fpp binding, but this playlist's fpp binding is nil", ShowPlaylistRunnerFPP)
	}
	var entry *ShowPlaylistEntry
	for i := range p.Entries {
		if p.Entries[i].ID == entryID {
			entry = &p.Entries[i]
			break
		}
	}
	if entry == nil {
		return "", fmt.Errorf("config: entry %q does not belong to this playlist", entryID)
	}
	if entry.FPP == nil {
		return "", fmt.Errorf("config: entry %q has no fpp binding to derive a key from", entry.ID)
	}
	return fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{
		InstanceUUID: p.FPP.InstanceUUID,
		PlaylistName: p.FPP.PlaylistName,
		PlaylistHash: p.FPP.PlaylistHash,
		Section:      entry.FPP.Section,
		Position:     entry.FPP.Position,
	})
}

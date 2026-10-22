package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// show.sequencetest (ADR-067): one object per show, id = show id, mapping
// FPP sequence filenames to Cues of that show for testing outside a night.

const (
	ShowSequenceTestConfigKind = "show.sequencetest"
	ShowSequenceTestSourceAPI  = "api"

	ValidationCodeSequenceTestFilenameClaimed = "sequence-test-filename-claimed"

	sequenceTestMaxEntries     = 256
	sequenceTestMaxFilenameLen = 255
)

type SequenceTestEntry struct {
	SequenceFilename string `json:"sequenceFilename"`
	Cue              string `json:"cue"`
}

type SequenceTestPayload struct {
	Show    string              `json:"show"`
	Entries []SequenceTestEntry `json:"entries"`
}

func EncodeSequenceTestPayload(p SequenceTestPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.sequencetest payload: %w", err)
	}
	return string(b), nil
}

// DecodeSequenceTestPayload parses and validates raw for the object id
// showID. claimedBy maps a filename to the Cues a playlist entry of the show
// already plays for it; a row naming the filename with any other Cue is
// refused, as is one filename mapped to two Cues here.
func DecodeSequenceTestPayload(raw, showID string, showExists func(string) bool, resolveCue func(cueID string) (show string, ok bool), claimedBy map[string][]string) (SequenceTestPayload, *ValidationError) {
	var p SequenceTestPayload
	if verr := decodeStrict(raw, &p, "show.sequencetest"); verr != nil {
		return SequenceTestPayload{}, verr
	}
	if p.Show == "" {
		p.Show = showID
	}
	if p.Show != showID {
		return SequenceTestPayload{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "show", Detail: fmt.Sprintf("The show field says %s but the address names %s. Use the same show in both.", p.Show, showID)}
	}
	if showExists == nil || !showExists(showID) {
		return SequenceTestPayload{}, &ValidationError{Code: ValidationCodeFieldUnknownReference, Field: "show", Detail: fmt.Sprintf("Show %s does not exist. Create it first.", showID)}
	}
	if p.Entries == nil {
		p.Entries = []SequenceTestEntry{}
	}
	if len(p.Entries) > sequenceTestMaxEntries {
		return SequenceTestPayload{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "entries", Detail: fmt.Sprintf("There are more than %d test sequences. Remove some.", sequenceTestMaxEntries)}
	}
	mapped := map[string]string{}
	for i, e := range p.Entries {
		field := fmt.Sprintf("entries[%d]", i)
		e.SequenceFilename = strings.TrimSpace(e.SequenceFilename)
		p.Entries[i].SequenceFilename = e.SequenceFilename
		if e.SequenceFilename == "" {
			return SequenceTestPayload{}, &ValidationError{Code: ValidationCodeFieldEmpty, Field: field + ".sequenceFilename", Detail: fmt.Sprintf("Row %d has no sequence file. Enter the FPP sequence file name or remove the row.", i+1)}
		}
		if len(e.SequenceFilename) > sequenceTestMaxFilenameLen || strings.ContainsAny(e.SequenceFilename, "/\\") {
			return SequenceTestPayload{}, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field + ".sequenceFilename", Detail: fmt.Sprintf("Row %d has a sequence file name that is too long or contains a slash. Enter the file name only.", i+1)}
		}
		if e.Cue == "" {
			return SequenceTestPayload{}, &ValidationError{Code: ValidationCodeFieldEmpty, Field: field + ".cue", Detail: fmt.Sprintf("Row %d has no cue. Pick one or remove the row.", i+1)}
		}
		cueShow, ok := resolveCue(e.Cue)
		if !ok || cueShow != showID {
			return SequenceTestPayload{}, &ValidationError{Code: ValidationCodeFieldUnknownReference, Field: field + ".cue", Detail: fmt.Sprintf("Cue %s is not a cue of show %s. Pick a cue of this show.", e.Cue, showID)}
		}
		if prev, seen := mapped[e.SequenceFilename]; seen && prev != e.Cue {
			return SequenceTestPayload{}, &ValidationError{Code: ValidationCodeSequenceTestFilenameClaimed, Field: field + ".sequenceFilename", Detail: fmt.Sprintf("%s already plays cue %s in another row. Map each sequence file to one cue.", e.SequenceFilename, prev)}
		}
		mapped[e.SequenceFilename] = e.Cue
		for _, other := range claimedBy[e.SequenceFilename] {
			if other != e.Cue {
				return SequenceTestPayload{}, &ValidationError{Code: ValidationCodeSequenceTestFilenameClaimed, Field: field + ".sequenceFilename", Detail: fmt.Sprintf("%s already plays cue %s from a playlist of this show. Pick a different sequence file or map it to %s.", e.SequenceFilename, other, other)}
			}
		}
	}
	return p, nil
}

package fppidentity

import "encoding/json"

// This file is the ONE parser for a stored playlist definition's entries
// (TRACK-H-H2-SPEC.md section 4.1), shared by the coordinator's definition
// preview route (internal/coordinator/api/fppplaylistdefinitions.go) and
// its playlist readiness check (internal/coordinator/fppreconcile), so the
// two never disagree about what "entry 3" means for the same stored
// definition. It lives in pkg/fppidentity, not in either caller, because
// api must never be imported by fppreconcile (fppreconcile is called BY
// api's reconciliation read route) and duplicating the parsing rule in two
// packages is exactly the kind of drift this contract's own doc comment
// warns against for the two canonical hashes.

// DefinitionSections is H2 spec section 4 step 2's fixed read order:
// "leadIn, mainPlaylist, leadOut", never alphabetical, never as they
// happen to be encountered in the definition.
var DefinitionSections = []string{"leadIn", "mainPlaylist", "leadOut"}

// DefinitionEntry is one parsed entry: a (Section, Position) pair, each
// section positioned from zero independently — matching the entry-key
// derivation's own five-field identity — plus whatever Type/SequenceName/
// MediaName the definition happened to carry at that slot. All three may
// be empty: "an entry with no filenames is still an entry" (H2 spec
// section 4.1).
type DefinitionEntry struct {
	Section      string
	Position     int
	Type         string
	SequenceName string
	MediaName    string
}

// definitionRawEntry is one array element of a section, read permissively:
// H2 spec section 4.1 requires "does not require any other member and does
// not fail on members it does not recognize, because the definition is
// FPP's shape and not ShowMesh's." Fields this struct does not name
// (enabled, playOnce, and anything a future FPP adds) are silently ignored
// by encoding/json — this is NOT decoded with DisallowUnknownFields.
type definitionRawEntry struct {
	Type         string `json:"type"`
	SequenceName string `json:"sequenceName"`
	MediaName    string `json:"mediaName"`
}

// ParseDefinitionEntries implements H2 spec section 4.1: read leadIn,
// mainPlaylist, and leadOut, in that order, as arrays of objects; each
// section is positioned from zero independently. A section member that is
// absent is treated as empty, matching real fppd output — "does not
// require any other member" (section 4.1) extends to a whole section
// being absent, not merely to one entry's fields. A section present but
// not a JSON array is refused: that is not the shape section 4.1 describes
// reading, and silently skipping it would hide a definition this parser
// cannot actually represent.
func ParseDefinitionEntries(definitionJSON string) ([]DefinitionEntry, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(definitionJSON), &doc); err != nil {
		return nil, err
	}
	var out []DefinitionEntry
	for _, section := range DefinitionSections {
		raw, ok := doc[section]
		if !ok {
			continue
		}
		var rawEntries []definitionRawEntry
		if err := json.Unmarshal(raw, &rawEntries); err != nil {
			return nil, err
		}
		for position, e := range rawEntries {
			out = append(out, DefinitionEntry{
				Section:      section,
				Position:     position,
				Type:         e.Type,
				SequenceName: e.SequenceName,
				MediaName:    e.MediaName,
			})
		}
	}
	return out, nil
}

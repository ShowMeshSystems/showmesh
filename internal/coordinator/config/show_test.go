package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeShowPayloadValid(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "Halloween 2026", "notes": "the good one"}`, alwaysTrueAudioNodeExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Name != "Halloween 2026" || p.Notes != "the good one" {
		t.Fatalf("unexpected payload: %+v", p)
	}
}

func TestEncodeShowPayloadRoundTrips(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "Halloween 2026", "notes": ""}`, alwaysTrueAudioNodeExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	raw, err := EncodeShowPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if back["name"] != "Halloween 2026" {
		t.Fatalf("name did not round trip: %v", back["name"])
	}
}

func TestDecodeShowPayloadNameAbsent(t *testing.T) {
	_, verr := DecodeShowPayload(`{"notes": "x"}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "name" {
		t.Fatalf("expected field-required on name, got %+v", verr)
	}
}

func TestDecodeShowPayloadNameNull(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": null, "notes": "x"}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "name" {
		t.Fatalf("expected field-null on name, got %+v", verr)
	}
}

func TestDecodeShowPayloadNameEmpty(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "", "notes": "x"}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldEmpty || verr.Field != "name" {
		t.Fatalf("expected field-empty on name, got %+v", verr)
	}
}

func TestDecodeShowPayloadNameTooLong(t *testing.T) {
	long := strings.Repeat("a", maxShowNameRunes+1)
	_, verr := DecodeShowPayload(`{"name": "`+long+`", "notes": ""}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "name" {
		t.Fatalf("expected field-invalid on name, got %+v", verr)
	}
}

// TestDecodeShowPayloadNotesAbsentMeansEmpty is the load-bearing test for
// this kind's "PUT is a full replacement" rule: an absent notes key must
// decode to "", the same as an explicitly empty string, and must NEVER be
// treated as "leave whatever was there before" — Step 7 shipped exactly
// that defect for a different field and wiped every FPP endpoint.
func TestDecodeShowPayloadNotesAbsentMeansEmpty(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "Halloween 2026"}`, alwaysTrueAudioNodeExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Notes != "" {
		t.Fatalf("expected absent notes to decode to empty string, got %q", p.Notes)
	}
}

func TestDecodeShowPayloadNotesExplicitlyEmpty(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "Halloween 2026", "notes": ""}`, alwaysTrueAudioNodeExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Notes != "" {
		t.Fatalf("expected explicit empty notes to decode to empty string, got %q", p.Notes)
	}
}

func TestDecodeShowPayloadNotesNull(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "Halloween 2026", "notes": null}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "notes" {
		t.Fatalf("expected field-null on notes, got %+v", verr)
	}
}

func TestDecodeShowPayloadNotesTooLong(t *testing.T) {
	long := strings.Repeat("a", maxShowNotesRunes+1)
	_, verr := DecodeShowPayload(`{"name": "Halloween 2026", "notes": "`+long+`"}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "notes" {
		t.Fatalf("expected field-invalid on notes, got %+v", verr)
	}
}

func TestDecodeShowPayloadUnknownTopLevelKey(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "Halloween 2026", "surfaces": []}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key, got %+v", verr)
	}
}

func TestDecodeShowPayloadBodyNotObject(t *testing.T) {
	_, verr := DecodeShowPayload(`[1,2,3]`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeBodyInvalid {
		t.Fatalf("expected body-invalid, got %+v", verr)
	}
}

// An object id a "show" reference could never name must be refused at the
// write, not stored. Without this a show created as "Halloween 2026" saves
// fine and then rejects every surface that tries to point at it.
func TestValidateShowObjectIDMatchesTheReferenceRule(t *testing.T) {
	for _, bad := range []string{"", "Halloween 2026", "-leading", "trailing-", "UPPER", "has_underscore"} {
		if verr := ValidateShowObjectID("show id", bad); verr == nil {
			t.Fatalf("id %q was accepted as an object id but would be refused as a reference", bad)
		}
	}
	for _, good := range []string{"halloween-2026", "s", "a1"} {
		if verr := ValidateShowObjectID("show id", good); verr != nil {
			t.Fatalf("id %q was refused: %+v", good, verr)
		}
		if verr := validateShowRef(good); verr != nil {
			t.Fatalf("id %q passes as an object id but fails as a reference: %+v", good, verr)
		}
	}
}

// TestDecodeShowPayloadParticipationAbsentIsNotEmpty is this field pair's
// load-bearing test, and the exact opposite of
// TestDecodeShowPayloadNotesAbsentMeansEmpty above. A show that has never
// had participation recorded must decode to a nil selection, NOT to an
// empty one: every show written before this field existed lands here, and
// a consumer that reads "absent" as "no instance takes part" would report
// a show with nothing in it as correctly configured.
func TestDecodeShowPayloadParticipationAbsentIsNotEmpty(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "Halloween 2026"}`, alwaysTrueAudioNodeExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.FPPInstances != nil || p.ResolumeInstances != nil {
		t.Fatalf("absent participation must decode to nil, got fpp=%v resolume=%v", p.FPPInstances, p.ResolumeInstances)
	}
	if !p.ParticipationUnconfigured() {
		t.Fatal("a show with no selection recorded must report ParticipationUnconfigured")
	}
	if ids, selected := p.FPPParticipation(); selected || ids != nil {
		t.Fatalf("absent FPP selection must report selected=false, got ids=%v selected=%v", ids, selected)
	}
	if ids, selected := p.ResolumeParticipation(); selected || ids != nil {
		t.Fatalf("absent Resolume selection must report selected=false, got ids=%v selected=%v", ids, selected)
	}
}

// TestDecodeShowPayloadParticipationExplicitlyEmptyIsRecorded is the other
// half of the same rule: the owner's own example is a night with FPP hosts
// and no Resolume, so "no Resolume takes part" has to be expressible and
// has to be a different decoded value from never having chosen.
func TestDecodeShowPayloadParticipationExplicitlyEmptyIsRecorded(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "Halloween 2026", "fppInstances": ["fpp-a", "fpp-b"], "resolumeInstances": []}`, alwaysTrueAudioNodeExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.ParticipationUnconfigured() {
		t.Fatal("a show with a recorded selection must not report ParticipationUnconfigured")
	}
	ids, selected := p.ResolumeParticipation()
	if !selected {
		t.Fatal("an explicitly empty resolumeInstances must decode as SELECTED, not as absent")
	}
	if len(ids) != 0 {
		t.Fatalf("expected an empty Resolume selection, got %v", ids)
	}
	fppIDs, fppSelected := p.FPPParticipation()
	if !fppSelected || len(fppIDs) != 2 || fppIDs[0] != "fpp-a" || fppIDs[1] != "fpp-b" {
		t.Fatalf("unexpected FPP selection: ids=%v selected=%v", fppIDs, fppSelected)
	}
}

// TestEncodeShowPayloadParticipationRoundTripsAllThreeStates proves the
// distinction survives the store: payload_json is what a later read
// decodes, so a representation that flattened absent and empty on the way
// out would lose the fact no matter how carefully the decoder kept it.
func TestEncodeShowPayloadParticipationRoundTripsAllThreeStates(t *testing.T) {
	for _, tc := range []struct {
		name          string
		raw           string
		wantKeyAbsent bool
		wantSelected  bool
		wantLen       int
	}{
		{"absent", `{"name": "s"}`, true, false, 0},
		{"explicitly empty", `{"name": "s", "resolumeInstances": []}`, false, true, 0},
		{"populated", `{"name": "s", "resolumeInstances": ["arena-01"]}`, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, verr := DecodeShowPayload(tc.raw, alwaysTrueAudioNodeExists)
			if verr != nil {
				t.Fatalf("decode: %+v", verr)
			}
			encoded, err := EncodeShowPayload(p)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal([]byte(encoded), &top); err != nil {
				t.Fatalf("re-decode: %v", err)
			}
			if _, present := top["resolumeInstances"]; present == tc.wantKeyAbsent {
				t.Fatalf("resolumeInstances key present=%v in %s, want absent=%v", present, encoded, tc.wantKeyAbsent)
			}
			back, verr := DecodeShowPayload(encoded, alwaysTrueAudioNodeExists)
			if verr != nil {
				t.Fatalf("decode of encoded payload: %+v", verr)
			}
			ids, selected := back.ResolumeParticipation()
			if selected != tc.wantSelected || len(ids) != tc.wantLen {
				t.Fatalf("round trip lost the state: ids=%v selected=%v, want selected=%v len=%d", ids, selected, tc.wantSelected, tc.wantLen)
			}
		})
	}
}

func TestDecodeShowPayloadParticipationNullRejected(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "s", "fppInstances": null}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "fppInstances" {
		t.Fatalf("expected field-null on fppInstances, got %+v", verr)
	}
}

func TestDecodeShowPayloadParticipationNotAnArrayRejected(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "s", "fppInstances": "fpp-a"}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "fppInstances" {
		t.Fatalf("expected field-invalid on fppInstances, got %+v", verr)
	}
}

func TestDecodeShowPayloadParticipationEmptyEntryRejected(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "s", "fppInstances": ["fpp-a", ""]}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldEmpty || verr.Field != "fppInstances[1]" {
		t.Fatalf("expected field-empty on fppInstances[1], got %+v", verr)
	}
}

func TestDecodeShowPayloadParticipationBadInstanceIDRejected(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "s", "resolumeInstances": ["Arena 01"]}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "resolumeInstances[0]" {
		t.Fatalf("expected field-invalid on resolumeInstances[0], got %+v", verr)
	}
}

// TestDecodeShowPayloadParticipationDuplicateRejected: a repeated id is
// refused rather than silently deduplicated, so the operator sees the typo
// instead of the coordinator storing a list nobody typed.
func TestDecodeShowPayloadParticipationDuplicateRejected(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "s", "fppInstances": ["fpp-a", "fpp-b", "fpp-a"]}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeInstanceIDDuplicate || verr.Field != "fppInstances[2]" {
		t.Fatalf("expected instance-id-duplicate on fppInstances[2], got %+v", verr)
	}
}

// TestDecodeShowPayloadParticipationUnknownKeyRejected: a misspelled key
// would otherwise be dropped by the decoder and leave the show reading as
// unconfigured, which is exactly the failure this field pair exists to
// make visible.
func TestDecodeShowPayloadParticipationUnknownKeyRejected(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "s", "fppInstance": ["fpp-a"]}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key, got %+v", verr)
	}
}

// TestDecodeShowPayloadAudioNodesAbsentIsNil is ADR-049 decision 10's
// load-bearing test for this field: absent must decode to nil so a show
// with no audioNodes reads the same whether it predates the field or an
// operator explicitly cleared it.
func TestDecodeShowPayloadAudioNodesAbsentIsNil(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "s"}`, alwaysTrueAudioNodeExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.AudioNodes != nil {
		t.Fatalf("expected nil audioNodes, got %v", p.AudioNodes)
	}
}

func TestDecodeShowPayloadAudioNodesExplicitlyEmptyIsNil(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "s", "audioNodes": []}`, alwaysTrueAudioNodeExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.AudioNodes != nil {
		t.Fatalf("expected an explicitly empty audioNodes to decode to nil, got %v", p.AudioNodes)
	}
}

func TestDecodeShowPayloadAudioNodesPopulated(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "s", "audioNodes": ["m4", "pi"]}`, alwaysTrueAudioNodeExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if len(p.AudioNodes) != 2 || p.AudioNodes[0] != "m4" || p.AudioNodes[1] != "pi" {
		t.Fatalf("unexpected audioNodes: %v", p.AudioNodes)
	}
}

func TestDecodeShowPayloadAudioNodesUnknownReferenceRejected(t *testing.T) {
	notExists := func(string) bool { return false }
	_, verr := DecodeShowPayload(`{"name": "s", "audioNodes": ["m4"]}`, notExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "audioNodes[0]" {
		t.Fatalf("expected field-unknown-reference on audioNodes[0], got %+v", verr)
	}
}

func TestDecodeShowPayloadAudioNodesDuplicateRejected(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "s", "audioNodes": ["m4", "pi", "m4"]}`, alwaysTrueAudioNodeExists)
	if verr == nil || verr.Code != ValidationCodeInstanceIDDuplicate || verr.Field != "audioNodes[2]" {
		t.Fatalf("expected instance-id-duplicate on audioNodes[2], got %+v", verr)
	}
}

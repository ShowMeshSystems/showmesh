package config

import (
	"encoding/json"
	"testing"
)

// resolveCueFixture returns a resolveCue callback backed by a fixed map of
// cue id -> show, for tests that need more than one cue or more than one
// show.
func resolveCueFixture(m map[string]string) func(string) (string, bool) {
	return func(id string) (string, bool) {
		show, ok := m[id]
		return show, ok
	}
}

func alwaysTrueResolveCueSameShow(show string) func(string) (string, bool) {
	return func(string) (string, bool) { return show, true }
}

func alwaysFalseResolveCue(string) (string, bool) { return "", false }

func validPlaylistJSON() string {
	return `{
		"show": "halloween-2026",
		"name": "Main show",
		"runner": "fpp",
		"mismatchPolicy": "hold",
		"fpp": {
			"instanceUuid": "6f1c1a52-1b6c-4b53-9a0e-9f7c2f0d1b44",
			"playlistName": "Halloween Main",
			"playlistHash": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
		},
		"entries": [
			{
				"id": "e1",
				"cue": "thriller",
				"fpp": {"section": "mainPlaylist", "position": 0, "expectedSequenceFilename": "Thriller.fseq", "expectedMediaFilename": "Thriller.mp3"}
			}
		]
	}`
}

func TestDecodeShowPlaylistPayloadValid(t *testing.T) {
	p, verr := DecodeShowPlaylistPayload(validPlaylistJSON(), alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Show != "halloween-2026" || p.Name != "Main show" || p.Runner != ShowPlaylistRunnerFPP {
		t.Fatalf("unexpected payload: %+v", p)
	}
	if p.MismatchPolicy != ShowPlaylistMismatchPolicyHold {
		t.Fatalf("unexpected mismatchPolicy: %q", p.MismatchPolicy)
	}
	if p.FPP == nil || p.FPP.InstanceUUID != "6f1c1a52-1b6c-4b53-9a0e-9f7c2f0d1b44" || p.FPP.PlaylistName != "Halloween Main" {
		t.Fatalf("unexpected fpp binding: %+v", p.FPP)
	}
	if len(p.Entries) != 1 || p.Entries[0].ID != "e1" || p.Entries[0].Cue != "thriller" {
		t.Fatalf("unexpected entries: %+v", p.Entries)
	}
	if p.Entries[0].FPP == nil || p.Entries[0].FPP.Section != "mainPlaylist" || p.Entries[0].FPP.Position != 0 {
		t.Fatalf("unexpected entry fpp: %+v", p.Entries[0].FPP)
	}
}

func TestEncodeShowPlaylistPayloadRoundTrips(t *testing.T) {
	p, verr := DecodeShowPlaylistPayload(validPlaylistJSON(), alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	raw, err := EncodeShowPlaylistPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if back["name"] != "Main show" {
		t.Fatalf("name did not round trip: %v", back["name"])
	}
}

// TestEncodeShowPlaylistPayloadRoundTripsOutputsAndEntries closes a review
// test gap: the existing round-trip test only asserted "name" survived
// re-decoding. This one asserts the fpp binding and entries (including the
// entry's own fpp section/position) survive too.
func TestEncodeShowPlaylistPayloadRoundTripsOutputsAndEntries(t *testing.T) {
	p, verr := DecodeShowPlaylistPayload(validPlaylistJSON(), alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	raw, err := EncodeShowPlaylistPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back ShowPlaylistPayload
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if back.FPP == nil || *back.FPP != *p.FPP {
		t.Fatalf("fpp binding did not round trip: %+v, want %+v", back.FPP, p.FPP)
	}
	if len(back.Entries) != len(p.Entries) {
		t.Fatalf("entries did not round trip: %+v, want %+v", back.Entries, p.Entries)
	}
	for i := range p.Entries {
		if back.Entries[i].ID != p.Entries[i].ID || back.Entries[i].Cue != p.Entries[i].Cue {
			t.Fatalf("entry %d did not round trip: %+v, want %+v", i, back.Entries[i], p.Entries[i])
		}
		if back.Entries[i].FPP == nil || *back.Entries[i].FPP != *p.Entries[i].FPP {
			t.Fatalf("entry %d fpp did not round trip: %+v, want %+v", i, back.Entries[i].FPP, p.Entries[i].FPP)
		}
	}
}

func TestDecodeShowPlaylistPayloadShowUnknown(t *testing.T) {
	_, verr := DecodeShowPlaylistPayload(validPlaylistJSON(), alwaysFalse, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "show" {
		t.Fatalf("expected field-unknown-reference on show, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadUnknownTopLevelKey(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "showmesh-audio",
		"entries": [{"id": "e1", "cue": "c1"}],
		"extra": true
	}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key, got %+v", verr)
	}
}

// --- runner ---

func TestDecodeShowPlaylistPayloadRunnerShowmeshReservedRefused(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "showmesh",
		"entries": [{"id": "e1", "cue": "c1"}]
	}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeNotImplemented || verr.Field != "runner" {
		t.Fatalf("expected not-implemented on runner \"showmesh\", got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadRunnerUnknown(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "bogus",
		"entries": [{"id": "e1", "cue": "c1"}]
	}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "runner" {
		t.Fatalf("expected field-invalid on runner, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadShowmeshAudioRunnerValid(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "showmesh-audio",
		"showmeshAudio": {"repeat": "all"},
		"entries": [{"id": "e1", "cue": "c1"}]
	}`
	p, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.FPP != nil {
		t.Fatalf("expected no fpp binding for showmesh-audio runner, got %+v", p.FPP)
	}
	if p.ShowmeshAudio == nil || p.ShowmeshAudio.Repeat != "all" {
		t.Fatalf("unexpected showmeshAudio: %+v", p.ShowmeshAudio)
	}
	if p.Entries[0].FPP != nil {
		t.Fatalf("expected no entry fpp binding for showmesh-audio runner, got %+v", p.Entries[0].FPP)
	}
}

func TestDecodeShowPlaylistPayloadShowmeshAudioRunnerDefaultsRepeat(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "showmesh-audio",
		"entries": [{"id": "e1", "cue": "c1"}]
	}`
	p, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.ShowmeshAudio == nil || p.ShowmeshAudio.Repeat != ShowPlaylistShowmeshAudioRepeatNone {
		t.Fatalf("expected default repeat \"none\", got %+v", p.ShowmeshAudio)
	}
}

// --- runner-specific object present for the wrong runner ---

func TestDecodeShowPlaylistPayloadFPPPresentForShowmeshAudioRunnerRefused(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "showmesh-audio",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1"}]
	}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "fpp" {
		t.Fatalf("expected field-invalid on fpp, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadShowmeshAudioPresentForFPPRunnerRefused(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"showmeshAudio": {"repeat": "none"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "showmeshAudio" {
		t.Fatalf("expected field-invalid on showmeshAudio, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadFPPRequiredForFPPRunner(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "fpp" {
		t.Fatalf("expected field-required on fpp, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadPlaylistHashNotHex(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "not-hex"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "fpp.playlistHash" {
		t.Fatalf("expected field-invalid on fpp.playlistHash, got %+v", verr)
	}
}

// --- mismatchPolicy / safeCueRef pairing, both directions ---

func TestDecodeShowPlaylistPayloadMismatchPolicyRefusedForShowmeshAudioRunner(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "showmesh-audio",
		"mismatchPolicy": "hold",
		"entries": [{"id": "e1", "cue": "c1"}]
	}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "mismatchPolicy" {
		t.Fatalf("expected field-invalid on mismatchPolicy, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadMismatchPolicyDefaultsHold(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	p, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.MismatchPolicy != ShowPlaylistMismatchPolicyHold {
		t.Fatalf("expected default mismatchPolicy \"hold\", got %q", p.MismatchPolicy)
	}
}

func TestDecodeShowPlaylistPayloadSafeCueRefRequiredWhenPolicySafeCue(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"mismatchPolicy": "safeCue",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "safeCueRef" {
		t.Fatalf("expected field-required on safeCueRef, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadSafeCueRefRefusedWhenPolicyNotSafeCue(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"mismatchPolicy": "hold", "safeCueRef": "safety",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "safeCueRef" {
		t.Fatalf("expected field-invalid on safeCueRef, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadSafeCueRefValid(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"mismatchPolicy": "safeCue", "safeCueRef": "safety",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026", "safety": "halloween-2026"})
	p, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.SafeCueRef != "safety" {
		t.Fatalf("unexpected safeCueRef: %q", p.SafeCueRef)
	}
}

// TestDecodeShowPlaylistPayloadSafeCueRefUnknown closes a review test gap:
// safeCueRef naming a Cue that does not exist at all (resolveCue reports
// not-found), distinct from TestDecodeShowPlaylistPayloadSafeCueRefCrossShowRefused's
// "exists but belongs to a different show".
func TestDecodeShowPlaylistPayloadSafeCueRefUnknown(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"mismatchPolicy": "safeCue", "safeCueRef": "nope",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "safeCueRef" {
		t.Fatalf("expected field-unknown-reference on safeCueRef, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadSafeCueRefCrossShowRefused(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"mismatchPolicy": "safeCue", "safeCueRef": "safety",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026", "safety": "christmas-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeCrossShowReference || verr.Field != "safeCueRef" {
		t.Fatalf("expected cross-show-reference on safeCueRef, got %+v", verr)
	}
}

// TestDecodeShowPlaylistPayloadNestedUnknownKey closes a review test gap:
// only the top level was covered for unknown keys; a typo'd key inside a
// nested object (fpp) must be refused too.
func TestDecodeShowPlaylistPayloadNestedUnknownKey(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `", "extra": true},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key on a nested unknown key, got %+v", verr)
	}
}

// TestDecodeShowPlaylistPayloadNameTooLong closes a review test gap:
// maxPlaylistNameRunes had no test.
func TestDecodeShowPlaylistPayloadNameTooLong(t *testing.T) {
	name := make([]byte, maxPlaylistNameRunes+1)
	for i := range name {
		name[i] = 'a'
	}
	j := `{
		"show": "halloween-2026", "name": "` + string(name) + `", "runner": "showmesh-audio",
		"entries": [{"id": "e1", "cue": "c1"}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "name" {
		t.Fatalf("expected field-invalid on name, got %+v", verr)
	}
}

// --- entries ---

func TestDecodeShowPlaylistPayloadEntriesAbsent(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x", "runner": "showmesh-audio"}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "entries" {
		t.Fatalf("expected field-required on entries, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadEntriesEmpty(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x", "runner": "showmesh-audio", "entries": []}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeEntriesEmpty || verr.Field != "entries" {
		t.Fatalf("expected entries-empty, got %+v", verr)
	}
}

// TestDecodeShowPlaylistPayloadEntriesNotArray and
// TestDecodeShowPlaylistPayloadEntriesNull close a review test gap: entries
// present but not a JSON array, and entries explicitly null.
func TestDecodeShowPlaylistPayloadEntriesNotArray(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x", "runner": "showmesh-audio", "entries": "nope"}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "entries" {
		t.Fatalf("expected field-invalid on entries (not an array), got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadEntriesNull(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x", "runner": "showmesh-audio", "entries": null}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "entries" {
		t.Fatalf("expected field-null on entries, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadCueUnknown(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x", "runner": "showmesh-audio", "entries": [{"id": "e1", "cue": "nope"}]}`
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, alwaysFalseResolveCue)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "entries[0].cue" {
		t.Fatalf("expected field-unknown-reference on entries[0].cue, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadCueCrossShowRefused(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x", "runner": "showmesh-audio", "entries": [{"id": "e1", "cue": "c1"}]}`
	resolve := resolveCueFixture(map[string]string{"c1": "christmas-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeCrossShowReference || verr.Field != "entries[0].cue" {
		t.Fatalf("expected cross-show-reference on entries[0].cue, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadDuplicateEntryID(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "showmesh-audio",
		"entries": [{"id": "e1", "cue": "c1"}, {"id": "e1", "cue": "c2"}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026", "c2": "halloween-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeItemIDDuplicate {
		t.Fatalf("expected item-id-duplicate, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadDuplicateSectionPosition(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [
			{"id": "e1", "cue": "c1", "fpp": {"section": "main", "position": 0}},
			{"id": "e2", "cue": "c2", "fpp": {"section": "main", "position": 0}}
		]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026", "c2": "halloween-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeEntryPositionDuplicate {
		t.Fatalf("expected entry-position-duplicate, got %+v", verr)
	}
}

// TestDecodeShowPlaylistPayloadSameCueDifferentPositionsAccepted proves the
// duplicate-filename case the entry key exists to resolve: two entries
// referencing the SAME Cue at different positions is legitimate.
func TestDecodeShowPlaylistPayloadSameCueDifferentPositionsAccepted(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [
			{"id": "e1", "cue": "c1", "fpp": {"section": "main", "position": 0}},
			{"id": "e2", "cue": "c1", "fpp": {"section": "main", "position": 1}}
		]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	p, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if len(p.Entries) != 2 || p.Entries[0].Cue != p.Entries[1].Cue {
		t.Fatalf("unexpected entries: %+v", p.Entries)
	}
}

func TestDecodeShowPlaylistPayloadEntryFPPRequiredForFPPRunner(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1"}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "entries[0].fpp" {
		t.Fatalf("expected field-required on entries[0].fpp, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadEntryFPPRefusedForShowmeshAudioRunner(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "showmesh-audio",
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 0}}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "entries[0].fpp" {
		t.Fatalf("expected field-invalid on entries[0].fpp, got %+v", verr)
	}
}

func TestDecodeShowPlaylistPayloadEntryPositionNegative(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": -1}}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "entries[0].fpp.position" {
		t.Fatalf("expected field-invalid on entries[0].fpp.position, got %+v", verr)
	}
}

// TestDecodeShowPlaylistPayloadEntryPositionOverCeiling is item 7's own
// test: entries[].fpp.position is now bounded at maxPlaylistEntryPosition.
func TestDecodeShowPlaylistPayloadEntryPositionOverCeiling(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"section": "", "position": 100001}}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	_, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "entries[0].fpp.position" {
		t.Fatalf("expected field-invalid on entries[0].fpp.position (over ceiling), got %+v", verr)
	}
}

// TestDecodeShowPlaylistPayloadEntrySectionAbsentDefaultsEmpty is item 9's
// own test: entries[].fpp.section absent means the empty, unnamed default
// FPP section rather than being refused.
func TestDecodeShowPlaylistPayloadEntrySectionAbsentDefaultsEmpty(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [{"id": "e1", "cue": "c1", "fpp": {"position": 0}}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	p, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Entries[0].FPP == nil || p.Entries[0].FPP.Section != "" {
		t.Fatalf("expected empty default section, got %+v", p.Entries[0].FPP)
	}
}

// --- entry key derivation ---

// fixed64Hash is a hand-picked 64-character lowercase hex string
// (sha256("test")), reused as playlistHash across tests that only need a
// syntactically valid hash.
const fixed64Hash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestDerivePlaylistEntryKeyMatchesHandComputedExpectation(t *testing.T) {
	p, verr := DecodeShowPlaylistPayload(validPlaylistJSON(), alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	key, err := DerivePlaylistEntryKey(p, p.Entries[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Cross-checked against pkg/fppidentity.DeriveEntryKey directly for the
	// identical EntryIdentity (instanceUuid "6f1c1a52-1b6c-4b53-9a0e-
	// 9f7c2f0d1b44", playlistHash fixed64Hash, playlistName "Halloween
	// Main", position 0, section "mainPlaylist") — see this task's own
	// report for the exact command used to print it.
	const want = "82f2b39dfdf598970e333d1f3d13716242b202b250ff7151ae9bba11dd81f657"
	if key != want {
		t.Fatalf("DerivePlaylistEntryKey = %q, want %q", key, want)
	}
}

func TestDerivePlaylistEntryKeyRefusesNonFPPRunner(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "showmesh-audio",
		"entries": [{"id": "e1", "cue": "c1"}]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	p, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if _, err := DerivePlaylistEntryKey(p, p.Entries[0].ID); err == nil {
		t.Fatalf("expected an error deriving a key for a showmesh-audio runner Playlist")
	}
}

// TestDerivePlaylistEntryKeyRefusesUnknownEntryID is item 6's own test:
// DerivePlaylistEntryKey looks the entry up in p by id rather than trusting
// a caller-assembled ShowPlaylistEntry value, and must refuse an id that is
// not actually one of p's own entries.
func TestDerivePlaylistEntryKeyRefusesUnknownEntryID(t *testing.T) {
	p, verr := DecodeShowPlaylistPayload(validPlaylistJSON(), alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if _, err := DerivePlaylistEntryKey(p, "does-not-exist"); err == nil {
		t.Fatalf("expected an error deriving a key for an entry id not in p")
	}
}

// TestDerivePlaylistEntryKeyRefusesNilFPPBindingDistinctFromWrongRunner is
// item 10's own test: a "fpp" runner Playlist with a nil FPP binding is an
// impossible state DecodeShowPlaylistPayload never produces, but
// DerivePlaylistEntryKey must still report it distinctly from a
// wrong-runner refusal rather than printing `requires runner "fpp", got
// "fpp"`.
func TestDerivePlaylistEntryKeyRefusesNilFPPBindingDistinctFromWrongRunner(t *testing.T) {
	p, verr := DecodeShowPlaylistPayload(validPlaylistJSON(), alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	p.FPP = nil
	_, err := DerivePlaylistEntryKey(p, p.Entries[0].ID)
	if err == nil {
		t.Fatalf("expected an error deriving a key with a nil fpp binding")
	}
	if got := err.Error(); got == `config: entry key derivation requires runner "fpp", got "fpp"` {
		t.Fatalf("error does not distinguish a nil fpp binding from a wrong runner: %v", err)
	}
}

// TestDerivePlaylistEntryKeyDifferentPositionsDeriveDifferentKeys and
// TestDerivePlaylistEntryKeyUnchangedPayloadDerivesSameKeyTwice close two
// of the review's test gaps: two entries differing only in position must
// derive different keys (the whole reason the key is a function of
// position), and deriving twice from the same unchanged payload must
// derive the identical key both times.
func TestDerivePlaylistEntryKeyDifferentPositionsDeriveDifferentKeys(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + fixed64Hash + `"},
		"entries": [
			{"id": "e1", "cue": "c1", "fpp": {"section": "main", "position": 0}},
			{"id": "e2", "cue": "c1", "fpp": {"section": "main", "position": 1}}
		]
	}`
	resolve := resolveCueFixture(map[string]string{"c1": "halloween-2026"})
	p, verr := DecodeShowPlaylistPayload(j, alwaysTrueShowExists, resolve)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	key1, err := DerivePlaylistEntryKey(p, "e1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	key2, err := DerivePlaylistEntryKey(p, "e2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key1 == key2 {
		t.Fatalf("entries at different positions derived the same key %q", key1)
	}
}

func TestDerivePlaylistEntryKeyUnchangedPayloadDerivesSameKeyTwice(t *testing.T) {
	p, verr := DecodeShowPlaylistPayload(validPlaylistJSON(), alwaysTrueShowExists, alwaysTrueResolveCueSameShow("halloween-2026"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	key1, err := DerivePlaylistEntryKey(p, p.Entries[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	key2, err := DerivePlaylistEntryKey(p, p.Entries[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key1 != key2 {
		t.Fatalf("deriving twice from an unchanged payload produced different keys: %q vs %q", key1, key2)
	}
}

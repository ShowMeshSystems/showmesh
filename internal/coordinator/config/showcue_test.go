package config

import (
	"encoding/json"
	"testing"
)

func validCueJSON() string {
	return `{
		"show": "halloween-2026",
		"name": "Thriller",
		"outputs": {
			"render": {"sequence": "thriller"},
			"audio": {"asset": "thriller-audience", "startOffsetMillis": 0},
			"ltc": {"startOffsetMillis": 0},
			"announcement": {"policy": "duck", "duckGainDb": -18, "fadeMillis": 300}
		}
	}`
}

func TestDecodeShowCuePayloadValid(t *testing.T) {
	p, verr := DecodeShowCuePayload(validCueJSON(), alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Show != "halloween-2026" || p.Name != "Thriller" {
		t.Fatalf("unexpected payload: %+v", p)
	}
	if p.Outputs.Render == nil || p.Outputs.Render.Sequence != "thriller" {
		t.Fatalf("unexpected render output: %+v", p.Outputs.Render)
	}
	if p.Outputs.Audio == nil || p.Outputs.Audio.Asset != "thriller-audience" || p.Outputs.Audio.StartOffsetMillis != 0 {
		t.Fatalf("unexpected audio output: %+v", p.Outputs.Audio)
	}
	if p.Outputs.LTC == nil || p.Outputs.LTC.StartOffsetMillis != 0 {
		t.Fatalf("unexpected ltc output: %+v", p.Outputs.LTC)
	}
	if p.Outputs.Announcement == nil || p.Outputs.Announcement.Policy != "duck" ||
		p.Outputs.Announcement.DuckGainDb == nil || *p.Outputs.Announcement.DuckGainDb != -18 ||
		p.Outputs.Announcement.FadeMillis != 300 {
		t.Fatalf("unexpected announcement output: %+v", p.Outputs.Announcement)
	}
}

func TestEncodeShowCuePayloadRoundTrips(t *testing.T) {
	p, verr := DecodeShowCuePayload(validCueJSON(), alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	raw, err := EncodeShowCuePayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if back["name"] != "Thriller" {
		t.Fatalf("name did not round trip: %v", back["name"])
	}
}

func TestDecodeShowCuePayloadShowUnknown(t *testing.T) {
	_, verr := DecodeShowCuePayload(validCueJSON(), alwaysFalse)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "show" {
		t.Fatalf("expected field-unknown-reference on show, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadUnknownTopLevelKey(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x", "outputs": {"render": {"sequence": "a"}}, "extra": true}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadUnknownNestedKey(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x", "outputs": {"render": {"sequence": "a", "extra": true}}}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key on nested object, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadNameTooLong(t *testing.T) {
	name := make([]byte, maxCueNameRunes+1)
	for i := range name {
		name[i] = 'a'
	}
	j := `{"show": "halloween-2026", "name": "` + string(name) + `", "outputs": {"render": {"sequence": "a"}}}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "name" {
		t.Fatalf("expected field-invalid on name, got %+v", verr)
	}
}

// --- outputs: absent / null / empty are three distinct refusals ---

func TestDecodeShowCuePayloadOutputsAbsent(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x"}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "outputs" {
		t.Fatalf("expected field-required on outputs, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadOutputsNull(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x", "outputs": null}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "outputs" {
		t.Fatalf("expected field-null on outputs, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadOutputsEmpty(t *testing.T) {
	j := `{"show": "halloween-2026", "name": "x", "outputs": {}}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs" {
		t.Fatalf("expected field-invalid on outputs (empty), got %+v", verr)
	}
}

// --- ltc requires audio ---

func TestDecodeShowCuePayloadLTCWithoutAudio(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {"render": {"sequence": "a"}, "ltc": {"startOffsetMillis": 0}}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.ltc" {
		t.Fatalf("expected field-invalid on outputs.ltc, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadLTCOffsetOutOfBounds(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {
			"audio": {"asset": "a", "startOffsetMillis": 0},
			"ltc": {"startOffsetMillis": 86400001}
		}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.ltc.startOffsetMillis" {
		t.Fatalf("expected field-invalid on outputs.ltc.startOffsetMillis, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadLTCOffsetNegative(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {
			"audio": {"asset": "a", "startOffsetMillis": 0},
			"ltc": {"startOffsetMillis": -1}
		}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.ltc.startOffsetMillis" {
		t.Fatalf("expected field-invalid on outputs.ltc.startOffsetMillis, got %+v", verr)
	}
}

// --- announcement requires audio ---

func TestDecodeShowCuePayloadAnnouncementWithoutAudio(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {"render": {"sequence": "a"}, "announcement": {"policy": "mix", "fadeMillis": 0}}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.announcement" {
		t.Fatalf("expected field-invalid on outputs.announcement, got %+v", verr)
	}
}

// --- announcement policy / duckGainDb pairing, both directions ---

func TestDecodeShowCuePayloadDuckRequiresDuckGainDb(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {
			"audio": {"asset": "a", "startOffsetMillis": 0},
			"announcement": {"policy": "duck", "fadeMillis": 0}
		}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "outputs.announcement.duckGainDb" {
		t.Fatalf("expected field-required on outputs.announcement.duckGainDb, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadMixRefusesDuckGainDb(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {
			"audio": {"asset": "a", "startOffsetMillis": 0},
			"announcement": {"policy": "mix", "duckGainDb": -10, "fadeMillis": 0}
		}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.announcement.duckGainDb" {
		t.Fatalf("expected field-invalid on outputs.announcement.duckGainDb, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadInterruptRefusesDuckGainDb(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {
			"audio": {"asset": "a", "startOffsetMillis": 0},
			"announcement": {"policy": "interrupt", "duckGainDb": -10, "fadeMillis": 0}
		}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.announcement.duckGainDb" {
		t.Fatalf("expected field-invalid on outputs.announcement.duckGainDb, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadDuckGainDbNotNegative(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {
			"audio": {"asset": "a", "startOffsetMillis": 0},
			"announcement": {"policy": "duck", "duckGainDb": 0, "fadeMillis": 0}
		}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.announcement.duckGainDb" {
		t.Fatalf("expected field-invalid on outputs.announcement.duckGainDb (not negative), got %+v", verr)
	}
}

func TestDecodeShowCuePayloadDuckGainDbBelowFloor(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {
			"audio": {"asset": "a", "startOffsetMillis": 0},
			"announcement": {"policy": "duck", "duckGainDb": -61, "fadeMillis": 0}
		}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.announcement.duckGainDb" {
		t.Fatalf("expected field-invalid on outputs.announcement.duckGainDb (below floor), got %+v", verr)
	}
}

func TestDecodeShowCuePayloadFadeMillisOutOfBounds(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {
			"audio": {"asset": "a", "startOffsetMillis": 0},
			"announcement": {"policy": "mix", "fadeMillis": 60001}
		}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.announcement.fadeMillis" {
		t.Fatalf("expected field-invalid on outputs.announcement.fadeMillis, got %+v", verr)
	}
}

func TestDecodeShowCuePayloadAudioOffsetNegative(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {"audio": {"asset": "a", "startOffsetMillis": -1}}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.audio.startOffsetMillis" {
		t.Fatalf("expected field-invalid on outputs.audio.startOffsetMillis, got %+v", verr)
	}
}

// TestDecodeShowCuePayloadAudioOffsetDefaultsZero is item 8's own test:
// outputs.audio.startOffsetMillis is documented with default 0 and must
// actually default rather than being required.
func TestDecodeShowCuePayloadAudioOffsetDefaultsZero(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {"audio": {"asset": "a"}}
	}`
	p, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Outputs.Audio == nil || p.Outputs.Audio.StartOffsetMillis != 0 {
		t.Fatalf("expected default startOffsetMillis 0, got %+v", p.Outputs.Audio)
	}
}

// TestDecodeShowCuePayloadAudioOffsetOutOfBounds is item 7's own test:
// outputs.audio.startOffsetMillis is now bounded at the same 24 hour
// ceiling as outputs.ltc.startOffsetMillis.
func TestDecodeShowCuePayloadAudioOffsetOutOfBounds(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {"audio": {"asset": "a", "startOffsetMillis": 86400001}}
	}`
	_, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "outputs.audio.startOffsetMillis" {
		t.Fatalf("expected field-invalid on outputs.audio.startOffsetMillis (over ceiling), got %+v", verr)
	}
}

// --- H0.5 claim derivation ---

// TestDeriveShowCueClaimsAllFourOutputs uses a Cue declaring render,
// audio, ltc, AND announcement (validCueJSON). Per H0.5, declaring
// announcement alongside audio routes that audio through the announcement
// session rather than the exclusive program route, so program-audio-route
// is NOT among the claims — only announcement-session, ltc-output, and
// the two render-surface claims are.
func TestDeriveShowCueClaimsAllFourOutputs(t *testing.T) {
	p, verr := DecodeShowCuePayload(validCueJSON(), alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	claims, err := DeriveShowCueClaims(p, ShowCueClaimContext{
		ProgramAudioNode: "audio-01", ProgramAudioRoute: "line-out-1",
		LTCNode: "audio-01", LTCRoute: "line-out-2",
		AnnouncementNode: "audio-01",
		RenderSurfaceIDs: []string{"surface-b", "surface-a"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []ShowCueClaim{
		{Kind: ShowCueClaimKindAnnouncementSession, Node: "audio-01"},
		{Kind: ShowCueClaimKindLTCOutput, Node: "audio-01", Resource: "line-out-2"},
		{Kind: ShowCueClaimKindRenderSurface, Resource: "surface-a"},
		{Kind: ShowCueClaimKindRenderSurface, Resource: "surface-b"},
	}
	if len(claims) != len(want) {
		t.Fatalf("claims = %v, want %v", claims, want)
	}
	for i := range want {
		if claims[i] != want[i] {
			t.Fatalf("claims = %v, want %v", claims, want)
		}
	}
}

// TestDeriveShowCueClaimsAnnouncementOnlyClaimsSession asserts the actual
// H0.5 rule: a Cue declaring audio and announcement (and nothing else)
// claims exactly announcement-session, and NOT program-audio-route. An
// announcement Cue's audio plays through the announcement session, not the
// exclusive program route it would otherwise seize.
func TestDeriveShowCueClaimsAnnouncementOnlyClaimsSession(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {
			"audio": {"asset": "a", "startOffsetMillis": 0},
			"announcement": {"policy": "mix", "fadeMillis": 0}
		}
	}`
	p, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	claims, err := DeriveShowCueClaims(p, ShowCueClaimContext{
		ProgramAudioNode: "audio-01", ProgramAudioRoute: "line-out-1",
		AnnouncementNode: "audio-01",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []ShowCueClaim{{Kind: ShowCueClaimKindAnnouncementSession, Node: "audio-01"}}
	if len(claims) != len(want) || claims[0] != want[0] {
		t.Fatalf("claims = %v, want %v", claims, want)
	}
}

// TestDeriveShowCueClaimsRepeatedCallsAgree replaces a prior
// "IsDeterministic" test that called DeriveShowCueClaims twice on the same
// input and only ever checked the two results agreed with EACH OTHER: the
// function has no map iteration anywhere in it, so two calls could never
// disagree and the test could never fail. This version instead asserts
// each call's result against a fixed expectation, so a regression that
// makes the derivation itself wrong (not merely non-deterministic) is
// caught.
func TestDeriveShowCueClaimsRepeatedCallsAgree(t *testing.T) {
	p, verr := DecodeShowCuePayload(validCueJSON(), alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	ctx := ShowCueClaimContext{
		ProgramAudioNode: "n", ProgramAudioRoute: "r",
		LTCNode: "n", LTCRoute: "r2",
		AnnouncementNode: "n",
		RenderSurfaceIDs: []string{"s1"},
	}
	want := []ShowCueClaim{
		{Kind: ShowCueClaimKindAnnouncementSession, Node: "n"},
		{Kind: ShowCueClaimKindLTCOutput, Node: "n", Resource: "r2"},
		{Kind: ShowCueClaimKindRenderSurface, Resource: "s1"},
	}
	for i := 0; i < 2; i++ {
		claims, err := DeriveShowCueClaims(p, ctx)
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if len(claims) != len(want) {
			t.Fatalf("call %d: claims = %v, want %v", i, claims, want)
		}
		for j := range want {
			if claims[j] != want[j] {
				t.Fatalf("call %d: claims = %v, want %v", i, claims, want)
			}
		}
	}
}

// TestDeriveShowCueClaimsRenderSurfaceIDsDeduped is item 5's own test: a
// Cue's outputs.render.sequence expands to render-surface claims through
// the caller-supplied RenderSurfaceIDs, and a duplicate surface id in that
// list (a Show whose surfaces overlap in some caller-side expansion) must
// not produce two identical render-surface claims.
func TestDeriveShowCueClaimsRenderSurfaceIDsDeduped(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x",
		"outputs": {"render": {"sequence": "a"}}
	}`
	p, verr := DecodeShowCuePayload(j, alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	claims, err := DeriveShowCueClaims(p, ShowCueClaimContext{
		RenderSurfaceIDs: []string{"surface-a", "surface-b", "surface-a"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []ShowCueClaim{
		{Kind: ShowCueClaimKindRenderSurface, Resource: "surface-a"},
		{Kind: ShowCueClaimKindRenderSurface, Resource: "surface-b"},
	}
	if len(claims) != len(want) {
		t.Fatalf("claims = %v, want %v (surface-a must not repeat)", claims, want)
	}
	for i := range want {
		if claims[i] != want[i] {
			t.Fatalf("claims = %v, want %v", claims, want)
		}
	}
}

// TestDeriveShowCueClaimsRefusesUnpopulatedContext is item 4's own test:
// a declared output whose backing ShowCueClaimContext field is left empty
// must be refused, not silently emitted as a claim with an empty
// component — two unrelated Cues would otherwise collide on the identical
// claim.
func TestDeriveShowCueClaimsRefusesUnpopulatedContext(t *testing.T) {
	p, verr := DecodeShowCuePayload(validCueJSON(), alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if _, err := DeriveShowCueClaims(p, ShowCueClaimContext{}); err == nil {
		t.Fatalf("expected an error deriving claims from an unpopulated ShowCueClaimContext")
	}
}

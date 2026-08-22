package config

import (
	"strings"
	"testing"
)

var nightSessionTestEndpoints = []FPPEndpoint{{ID: "fpp-main", URL: "http://fpp-main.local"}}

func alwaysTrueAssetCurrent(string, string, string) bool  { return true }
func alwaysFalseAssetCurrent(string, string, string) bool { return false }
func alwaysTrueActionResolver(string) (string, bool)      { return "halloween-2026", true }
func alwaysFalseActionResolver(string) (string, bool)     { return "", false }

// alwaysTrueInterlockSignalResolver resolves any signal id to a confirmable
// mqtt action in this session's own show — the shape decodeNightInterlockRule
// requires. nightsitecontrol_test.go exercises every other combination
// (wrong show, non-mqtt, expect.kind "none").
func alwaysTrueInterlockSignalResolver(string) (NightInterlockSignalInfo, bool) {
	return NightInterlockSignalInfo{Show: "halloween-2026", Integration: ShowActionIntegrationMQTT, MQTTExpectKind: MQTTExpectKindBoolean}, true
}

// validNightSessionJSON is a minimal, fully valid payload used as the base
// for every negative test below (each test mutates one thing).
const validNightSessionJSON = `{
  "show": "halloween-2026",
  "label": "Halloween main loop",
  "showPlaylist": {"fppInstanceId": "fpp-main", "playlist": "halloween-show"},
  "resting": {
    "fppInstanceId": "fpp-main",
    "playlist": "halloween-resting",
    "timelineAsset": {"show": "halloween-2026", "sequence": "resting-loop", "target": "fpp-main"},
    "endOfNightRepeat": true,
    "backgroundAudio": {
      "items": [
        {"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"}
      ],
      "repeat": "playlist",
      "resume": "resume",
      "itemTransition": "sequential",
      "maxGainDb": -10
    }
  },
  "enterShow": {
    "cues": [
      {"name": "lighting-fade", "role": "lighting", "action": "lighting-fade-out", "offsetMs": -20000, "barrier": true}
    ],
    "blackoutHoldMs": 6000
  },
  "enterResting": {
    "cues": [
      {"name": "lighting-fade-in", "role": "lighting", "action": "lighting-fade-in", "offsetMs": 0}
    ],
    "blackoutAfterShowMs": 6000
  }
}`

func decodeValidNightSession(t *testing.T) NightSessionPayload {
	t.Helper()
	p, verr := DecodeNightSessionPayload(validNightSessionJSON, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr != nil {
		t.Fatalf("unexpected error on the base valid payload: %+v", verr)
	}
	return p
}

func TestDecodeNightSessionPayloadValid(t *testing.T) {
	p := decodeValidNightSession(t)
	if p.Show != "halloween-2026" || p.Label != "Halloween main loop" {
		t.Fatalf("unexpected payload: %+v", p)
	}
	if p.ShowPlaylist.FPPInstanceID != "fpp-main" || p.ShowPlaylist.Playlist != "halloween-show" {
		t.Fatalf("unexpected showPlaylist: %+v", p.ShowPlaylist)
	}
	if p.Resting.EndOfNightPlaylist != p.Resting.Playlist {
		t.Fatalf("expected endOfNightPlaylist to default to playlist, got %q vs %q", p.Resting.EndOfNightPlaylist, p.Resting.Playlist)
	}
	if p.Resting.BackgroundAudio == nil || len(p.Resting.BackgroundAudio.Items) != 1 {
		t.Fatalf("expected one background audio item, got %+v", p.Resting.BackgroundAudio)
	}
	if p.Resting.BackgroundAudio.Resume != NightSessionBackgroundResumeResume {
		t.Fatalf("expected resume enum spelling %q, got %q", NightSessionBackgroundResumeResume, p.Resting.BackgroundAudio.Resume)
	}
	if len(p.EnterShow.Cues) != 1 || p.EnterShow.Cues[0].OffsetMs != -20000 {
		t.Fatalf("unexpected enterShow cues: %+v", p.EnterShow.Cues)
	}
	// onFailure must resolve to its default, never left blank.
	if p.EnterShow.Cues[0].OnFailure != NightSessionCueOnFailureDefault {
		t.Fatalf("expected resolved onFailure default, got %q", p.EnterShow.Cues[0].OnFailure)
	}
}

// TestEncodeNightSessionPayloadRoundTrips re-decodes the encoded payload
// through DecodeNightSessionPayload itself, not a plain json.Unmarshal:
// the validator, not just the Go struct tags, is what a real PUT-after-GET
// actually exercises, and a bug where the validator rejects its own
// encoded output is invisible to a bare json.Unmarshal re-decode (review
// finding 1).
func TestEncodeNightSessionPayloadRoundTrips(t *testing.T) {
	p := decodeValidNightSession(t)
	raw, err := EncodeNightSessionPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr != nil {
		t.Fatalf("re-decode of the API's own encoded output must not fail: %+v (encoded: %s)", verr, raw)
	}
	if back.Resting.BackgroundAudio == nil || back.Resting.BackgroundAudio.Items[0].ItemID != "track-1" {
		t.Fatalf("background audio item did not round trip: %+v", back.Resting.BackgroundAudio)
	}
	if back.Resting.BackgroundAudio.Items[0].Asset.Sequence != "bg-track-1" {
		t.Fatalf("background audio asset ref did not round trip: %+v", back.Resting.BackgroundAudio.Items[0])
	}
}

// TestEncodeNightSessionPayloadRoundTripsCrossfadeZero is the review's
// finding 1, restated as a test that can actually fail: crossfadeMs is a
// legal 0 when itemTransition is "crossfade", and the validator requires
// the KEY present in that case. A plain json.Unmarshal re-decode (this
// file's own prior version of TestEncodeNightSessionPayloadRoundTrips)
// cannot catch a field the wire form silently dropped.
//
// Broken and confirmed to fail: reverted CrossfadeMs to a plain int with
// "omitempty" — this test failed with field-required on crossfadeMs on
// the re-decode, exactly the operator-facing "PUT what GET just returned"
// bug the review found end to end through the real route. Restored
// afterward.
func TestEncodeNightSessionPayloadRoundTripsCrossfadeZero(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"itemTransition": "sequential"`, `"itemTransition": "crossfade", "crossfadeMs": 0`, 1)
	p, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr != nil {
		t.Fatalf("initial decode: %+v", verr)
	}
	if p.Resting.BackgroundAudio.CrossfadeMs == nil || *p.Resting.BackgroundAudio.CrossfadeMs != 0 {
		t.Fatalf("expected crossfadeMs 0 to decode as present, got %v", p.Resting.BackgroundAudio.CrossfadeMs)
	}

	encoded, err := EncodeNightSessionPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, verr := DecodeNightSessionPayload(encoded, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr != nil {
		t.Fatalf("re-decode of the API's own encoded output must not fail: %+v (encoded: %s)", verr, encoded)
	}
	if back.Resting.BackgroundAudio.CrossfadeMs == nil || *back.Resting.BackgroundAudio.CrossfadeMs != 0 {
		t.Fatalf("crossfadeMs 0 did not survive the encode/decode round trip: %v", back.Resting.BackgroundAudio.CrossfadeMs)
	}
}

// TestEncodeNightSessionPayloadRoundTripsNonCrossfadeOmitsCrossfadeMs
// proves the other half of the same fix did not regress: when
// itemTransition is not "crossfade", crossfadeMs must be genuinely
// absent from the encoded wire form, because the validator rejects its
// mere presence in that case. A fix that simply dropped "omitempty"
// unconditionally would fail exactly this re-decode.
func TestEncodeNightSessionPayloadRoundTripsNonCrossfadeOmitsCrossfadeMs(t *testing.T) {
	p := decodeValidNightSession(t) // itemTransition: "sequential"
	raw, err := EncodeNightSessionPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(raw, "crossfadeMs") {
		t.Fatalf("crossfadeMs must not appear on the wire for a non-crossfade transition: %s", raw)
	}
	if _, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver); verr != nil {
		t.Fatalf("re-decode of the API's own encoded output must not fail: %+v", verr)
	}
}

// --- ADR-038 decision 1: no calendar field, anywhere. ---

func TestDecodeNightSessionPayloadRejectsTopLevelCalendarField(t *testing.T) {
	raw := `{"show":"x","label":"y","at":"20:00","showPlaylist":{},"resting":{},"enterShow":{},"enterResting":{}}`
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeCalendarFieldRejected || verr.Field != "at" {
		t.Fatalf("expected calendar-field-rejected on \"at\", got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadRejectsNestedCalendarField(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"blackoutHoldMs": 6000`, `"blackoutHoldMs": 6000, "schedule": "nightly"`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeCalendarFieldRejected {
		t.Fatalf("expected calendar-field-rejected for a nested \"schedule\" key, got %+v", verr)
	}
	if verr.Field != "enterShow.schedule" {
		t.Fatalf("expected the dotted nested path, got %q", verr.Field)
	}
}

func TestDecodeNightSessionPayloadRejectsCalendarFieldInsideCueList(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"barrier": true}`, `"barrier": true, "weekday": "friday"}`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeCalendarFieldRejected {
		t.Fatalf("expected calendar-field-rejected inside an array element, got %+v", verr)
	}
}

// --- RESTING-MODE.md §6.1: the FSEQ is the only duration authority. ---

func TestDecodeNightSessionPayloadRejectsDuplicateRestDuration(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"endOfNightRepeat": true`, `"endOfNightRepeat": true, "restDuration": 300`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeDuplicateRestDuration {
		t.Fatalf("expected duplicate-rest-duration, got %+v", verr)
	}
}

// --- siteControl / interlocks: Track F seam F6, real decoding. ---
// The detailed decode rules for both blocks live in
// nightsitecontrol_test.go; the tests here only re-confirm the two
// things nightsession.go itself is responsible for: an empty siteControl
// object is refused as pointless configuration (nightsitecontrol.go owns
// the field-by-field rules once siteControl is non-empty), and
// siteControl is a TOP-LEVEL key only — a nested "resting.siteControl"
// is refused by the ordinary unknown-key rule for that object, not
// silently accepted.

func TestDecodeNightSessionPayloadRejectsEmptySiteControl(t *testing.T) {
	raw := strings.TrimSuffix(validNightSessionJSON, "}") + `,"siteControl":{}}`
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "siteControl" {
		t.Fatalf("expected field-required on an empty siteControl, got %+v", verr)
	}
}

// TestDecodeNightSessionPayloadRejectsNestedSiteControlAsUnknownKey is
// review finding 8's original property, restated for real decoding:
// "resting" never accepted "siteControl" and still does not — a nested
// occurrence is refused by resting's own closed key set.
func TestDecodeNightSessionPayloadRejectsNestedSiteControlAsUnknownKey(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"endOfNightRepeat": true,`, `"endOfNightRepeat": true, "siteControl": {},`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey || verr.Field != "resting" || !strings.Contains(verr.Detail, "siteControl") {
		t.Fatalf("expected field-unknown-key naming siteControl under resting, got %+v", verr)
	}
}

// TestDecodeNightSessionPayloadEmptyInterlocksIsValid: an explicit empty
// array means the same thing omitting the key does — no interlocks
// configured — rather than being refused.
func TestDecodeNightSessionPayloadEmptyInterlocksIsValid(t *testing.T) {
	raw := strings.TrimSuffix(validNightSessionJSON, "}") + `,"interlocks":[]}`
	p, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr != nil {
		t.Fatalf("unexpected error on an empty interlocks array: %+v", verr)
	}
	if len(p.Interlocks) != 0 {
		t.Fatalf("expected zero interlocks, got %+v", p.Interlocks)
	}
}

func TestDecodeNightSessionPayloadAbsentSiteControlIsValid(t *testing.T) {
	// decodeValidNightSession already asserts this implicitly (no
	// siteControl/interlocks key anywhere in validNightSessionJSON); this
	// test names the property explicitly so a future edit to the fixture
	// cannot silently drop the coverage.
	decodeValidNightSession(t)
}

// --- FPP instance references. ---

func TestDecodeNightSessionPayloadUnknownShowPlaylistInstance(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"showPlaylist": {"fppInstanceId": "fpp-main"`, `"showPlaylist": {"fppInstanceId": "no-such-fpp"`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "showPlaylist.fppInstanceId" {
		t.Fatalf("expected field-unknown-reference on showPlaylist.fppInstanceId, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadUnknownRestingInstance(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"resting": {
    "fppInstanceId": "fpp-main",`, `"resting": {
    "fppInstanceId": "no-such-fpp",`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "resting.fppInstanceId" {
		t.Fatalf("expected field-unknown-reference on resting.fppInstanceId, got %+v", verr)
	}
}

// --- endOfNightPlaylist: absent, null, and explicitly empty are three
// different things (review finding 4). Absent means "use the default
// (the resting playlist)"; null and empty are both rejected, since an
// operator writing "" almost certainly meant to clear an override this
// field has no way to represent — omitting the key already means that.

func TestDecodeNightSessionPayloadEndOfNightPlaylistAbsentDefaultsToPlaylist(t *testing.T) {
	// validNightSessionJSON omits endOfNightPlaylist entirely.
	p := decodeValidNightSession(t)
	if p.Resting.EndOfNightPlaylist != p.Resting.Playlist {
		t.Fatalf("expected endOfNightPlaylist to default to playlist, got %q vs %q", p.Resting.EndOfNightPlaylist, p.Resting.Playlist)
	}
}

func TestDecodeNightSessionPayloadEndOfNightPlaylistExplicitValueHonored(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"endOfNightRepeat": true`, `"endOfNightRepeat": true, "endOfNightPlaylist": "halloween-late-night"`, 1)
	p, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Resting.EndOfNightPlaylist != "halloween-late-night" {
		t.Fatalf("expected the explicit override to be honored, got %q", p.Resting.EndOfNightPlaylist)
	}
}

func TestDecodeNightSessionPayloadEndOfNightPlaylistNullRejected(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"endOfNightRepeat": true`, `"endOfNightRepeat": true, "endOfNightPlaylist": null`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "resting.endOfNightPlaylist" {
		t.Fatalf("expected field-null on an explicit null endOfNightPlaylist, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadEndOfNightPlaylistEmptyRejected(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"endOfNightRepeat": true`, `"endOfNightRepeat": true, "endOfNightPlaylist": ""`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldEmpty || verr.Field != "resting.endOfNightPlaylist" {
		t.Fatalf("expected field-empty on an explicit empty endOfNightPlaylist, got %+v", verr)
	}
}

// --- Asset references (ADR-028). ---

func TestDecodeNightSessionPayloadDanglingTimelineAsset(t *testing.T) {
	_, verr := DecodeNightSessionPayload(validNightSessionJSON, nightSessionTestEndpoints, alwaysFalseAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "resting.timelineAsset" {
		t.Fatalf("expected field-unknown-reference on resting.timelineAsset, got %+v", verr)
	}
}

// --- Background audio. ---

func TestDecodeNightSessionPayloadBackgroundAudioEmptyItems(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON,
		`"items": [
        {"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"}
      ]`, `"items": []`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeBackgroundAudioItemsEmpty {
		t.Fatalf("expected background-audio-items-empty, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadBackgroundAudioDuplicateItemID(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON,
		`"items": [
        {"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"}
      ]`,
		`"items": [
        {"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"},
        {"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-2", "target": "audio-node-1"}
      ]`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeItemIDDuplicate {
		t.Fatalf("expected item-id-duplicate, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadBackgroundAudioResumeMustBeEnum(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"resume": "resume"`, `"resume": true`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "resting.backgroundAudio.resume" {
		t.Fatalf("expected field-invalid on resting.backgroundAudio.resume for a bool value, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadBackgroundAudioResumeRejectsUnknownValue(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"resume": "resume"`, `"resume": "continue"`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "resting.backgroundAudio.resume" {
		t.Fatalf("expected field-invalid on resting.backgroundAudio.resume, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadCrossfadeMsRequiredWithCrossfadeTransition(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"itemTransition": "sequential"`, `"itemTransition": "crossfade"`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "resting.backgroundAudio.crossfadeMs" {
		t.Fatalf("expected field-required on crossfadeMs when itemTransition is crossfade, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadCrossfadeMsAcceptedWithCrossfadeTransition(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"itemTransition": "sequential"`, `"itemTransition": "crossfade", "crossfadeMs": 500`, 1)
	p, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Resting.BackgroundAudio.CrossfadeMs == nil || *p.Resting.BackgroundAudio.CrossfadeMs != 500 {
		t.Fatalf("expected crossfadeMs 500, got %v", p.Resting.BackgroundAudio.CrossfadeMs)
	}
}

func TestDecodeNightSessionPayloadCrossfadeMsRejectedWithoutCrossfadeTransition(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"itemTransition": "sequential"`, `"itemTransition": "sequential", "crossfadeMs": 500`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "resting.backgroundAudio.crossfadeMs" {
		t.Fatalf("expected field-invalid on crossfadeMs present without crossfade transition, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadMaxGainDbMustNotBePositive(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"maxGainDb": -10`, `"maxGainDb": 1`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "resting.backgroundAudio.maxGainDb" {
		t.Fatalf("expected field-invalid on positive maxGainDb, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadBackgroundAudioIsOptional(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `,
    "backgroundAudio": {
      "items": [
        {"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"}
      ],
      "repeat": "playlist",
      "resume": "resume",
      "itemTransition": "sequential",
      "maxGainDb": -10
    }`, "", 1)
	p, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr != nil {
		t.Fatalf("unexpected error omitting backgroundAudio entirely: %+v", verr)
	}
	if p.Resting.BackgroundAudio != nil {
		t.Fatalf("expected nil BackgroundAudio, got %+v", p.Resting.BackgroundAudio)
	}
}

const nightSessionBackgroundAudioNullJSON = `{
  "show": "halloween-2026",
  "label": "Halloween main loop",
  "showPlaylist": {"fppInstanceId": "fpp-main", "playlist": "halloween-show"},
  "resting": {
    "fppInstanceId": "fpp-main",
    "playlist": "halloween-resting",
    "timelineAsset": {"show": "halloween-2026", "sequence": "resting-loop", "target": "fpp-main"},
    "endOfNightRepeat": true,
    "backgroundAudio": null
  },
  "enterShow": {"cues": [], "blackoutHoldMs": 0},
  "enterResting": {"cues": [], "blackoutAfterShowMs": 0}
}`

func TestDecodeNightSessionPayloadBackgroundAudioNullIsRejected(t *testing.T) {
	_, verr := DecodeNightSessionPayload(nightSessionBackgroundAudioNullJSON, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "resting.backgroundAudio" {
		t.Fatalf("expected field-null on an explicit null backgroundAudio, got %+v", verr)
	}
}

// --- Cues. ---

func TestDecodeNightSessionPayloadDanglingCueAction(t *testing.T) {
	_, verr := DecodeNightSessionPayload(validNightSessionJSON, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysFalseActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || !strings.HasSuffix(verr.Field, ".action") {
		t.Fatalf("expected field-unknown-reference on a cue's action, got %+v", verr)
	}
}

// TestDecodeNightSessionPayloadCueActionCrossShowRejected is the review's
// finding 2/ruling, restated as a test: a cue's action resolving to a
// DIFFERENT show than this session's own must be rejected, not merely
// checked for existence — ADR-027's namespace rule, which the prior
// ActionExists callback could not express because it never read or
// compared the action's own show.
func TestDecodeNightSessionPayloadCueActionCrossShowRejected(t *testing.T) {
	christmasResolver := func(string) (string, bool) { return "christmas-2026", true }
	_, verr := DecodeNightSessionPayload(validNightSessionJSON, nightSessionTestEndpoints, alwaysTrueAssetCurrent, christmasResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeCrossShowReference || !strings.HasSuffix(verr.Field, ".action") {
		t.Fatalf("expected cross-show-reference on a cue's action, got %+v", verr)
	}
}

// TestDecodeNightSessionPayloadTimelineAssetCrossShowRejected is the same
// ruling applied to resting.timelineAsset: an asset whose OWN "show"
// field names a different show than this session's must be rejected even
// when a current asset for that tuple genuinely exists.
func TestDecodeNightSessionPayloadTimelineAssetCrossShowRejected(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON,
		`"timelineAsset": {"show": "halloween-2026", "sequence": "resting-loop", "target": "fpp-main"}`,
		`"timelineAsset": {"show": "christmas-2026", "sequence": "resting-loop", "target": "fpp-main"}`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeCrossShowReference || verr.Field != "resting.timelineAsset.show" {
		t.Fatalf("expected cross-show-reference on resting.timelineAsset.show, got %+v", verr)
	}
}

// TestDecodeNightSessionPayloadBackgroundAudioItemCrossShowRejected is
// the same ruling applied to a backgroundAudio item.
func TestDecodeNightSessionPayloadBackgroundAudioItemCrossShowRejected(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON,
		`{"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"}`,
		`{"itemId": "track-1", "show": "christmas-2026", "sequence": "bg-track-1", "target": "audio-node-1"}`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeCrossShowReference {
		t.Fatalf("expected cross-show-reference on the background audio item, got %+v", verr)
	}
}

// TestDecodeNightSessionPayloadTwoBackgroundAudioItemsSameAssetIsLegal is
// the review's OTHER open question, ruled the reading was already
// correct (no code change): two items with distinct itemIds pointing at
// the identical (show, sequence, target) asset stay legal. Playing the
// same track twice in a resting playlist is a real thing an operator
// wants, and itemId — not the asset tuple — is the identity the pinned
// playlist travels on.
func TestDecodeNightSessionPayloadTwoBackgroundAudioItemsSameAssetIsLegal(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON,
		`"items": [
        {"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"}
      ]`,
		`"items": [
        {"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"},
        {"itemId": "track-2", "show": "halloween-2026", "sequence": "bg-track-1", "target": "audio-node-1"}
      ]`, 1)
	p, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if len(p.Resting.BackgroundAudio.Items) != 2 {
		t.Fatalf("expected both items to be accepted, got %+v", p.Resting.BackgroundAudio.Items)
	}
}

func TestDecodeNightSessionPayloadDuplicateCueName(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON,
		`{"name": "lighting-fade", "role": "lighting", "action": "lighting-fade-out", "offsetMs": -20000, "barrier": true}`,
		`{"name": "lighting-fade", "role": "lighting", "action": "lighting-fade-out", "offsetMs": -20000, "barrier": true},
     {"name": "lighting-fade", "role": "projection", "action": "projection-fade-out", "offsetMs": -20000}`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeCueNameDuplicate {
		t.Fatalf("expected cue-name-duplicate, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadCueOffsetMsIsSigned(t *testing.T) {
	p := decodeValidNightSession(t)
	if p.EnterShow.Cues[0].OffsetMs >= 0 {
		t.Fatalf("expected a negative offsetMs to decode as negative, got %d", p.EnterShow.Cues[0].OffsetMs)
	}
}

func TestDecodeNightSessionPayloadNegativeBlackoutHoldMsRejected(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"blackoutHoldMs": 6000`, `"blackoutHoldMs": -1`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "enterShow.blackoutHoldMs" {
		t.Fatalf("expected field-invalid on a negative blackoutHoldMs, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadNegativeBlackoutAfterShowMsRejected(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"blackoutAfterShowMs": 6000`, `"blackoutAfterShowMs": -1`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "enterResting.blackoutAfterShowMs" {
		t.Fatalf("expected field-invalid on a negative blackoutAfterShowMs, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadCueOnFailureExplicitAbort(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"barrier": true}`, `"barrier": true, "onFailure": "abort"}`, 1)
	p, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.EnterShow.Cues[0].OnFailure != NightSessionCueOnFailureAbort {
		t.Fatalf("expected explicit abort to be preserved, got %q", p.EnterShow.Cues[0].OnFailure)
	}
}

func TestDecodeNightSessionPayloadCueRoleMustBeRecognized(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"role": "lighting", "action": "lighting-fade-out"`, `"role": "smoke-machine", "action": "lighting-fade-out"`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || !strings.HasSuffix(verr.Field, ".role") {
		t.Fatalf("expected field-invalid on an unrecognized cue role, got %+v", verr)
	}
}

// --- absent / null / empty are three different things on every write. ---

func TestDecodeNightSessionPayloadLabelAbsentIsRejected(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"label": "Halloween main loop",`, "", 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "label" {
		t.Fatalf("expected field-required on an absent label, got %+v", verr)
	}
}

func TestDecodeNightSessionPayloadUnknownTopLevelKey(t *testing.T) {
	raw := strings.TrimSuffix(validNightSessionJSON, "}") + `,"notes":"extra"}`
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key, got %+v", verr)
	}
}

package config

import (
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/audio"
)

func validAudioSettingsPayloadJSON() string {
	return `{"driftIgnoreThresholdMs":25,"defaultFadeCurve":"linear","defaultFadeDurationMs":1500,"defaultMaxBackgroundGainDb":-6.02,"duckTargetGainDb":-13.98,"ltcFrameRate":"25","ltcDefaultStartOffset":"00:00:00:00"}`
}

func TestDecodeAudioSettingsPayloadAccepts(t *testing.T) {
	p, verr := DecodeAudioSettingsPayload(validAudioSettingsPayloadJSON())
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	want := AudioSettingsPayload{
		DriftIgnoreThresholdMs: 25, DefaultFadeCurve: "linear",
		DefaultFadeDurationMs: 1500, DefaultMaxBackgroundGainDb: -6.02,
		DuckTargetGainDb: -13.98,
		LTCFrameRate:     "25", LTCDefaultStartOffset: "00:00:00:00",
	}
	if p != want {
		t.Errorf("payload = %+v, want %+v", p, want)
	}
}

// TestDecodeAudioSettingsPayloadAcceptsEveryLTCFrameRate proves all four
// rates are accepted and a fifth is refused.
func TestDecodeAudioSettingsPayloadAcceptsEveryLTCFrameRate(t *testing.T) {
	for _, rate := range []string{"24", "25", "29.97", "30"} {
		t.Run(rate, func(t *testing.T) {
			raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"` + rate + `","ltcDefaultStartOffset":"00:00:00:00"}`
			p, verr := DecodeAudioSettingsPayload(raw)
			if verr != nil {
				t.Fatalf("unexpected error for rate %s: %v", rate, verr)
			}
			if p.LTCFrameRate != rate {
				t.Errorf("LTCFrameRate = %q, want %q", p.LTCFrameRate, rate)
			}
		})
	}
}

func TestDecodeAudioSettingsPayloadRejectsUnknownLTCFrameRate(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"60","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "ltcFrameRate" {
		t.Fatalf("verr = %v, want field-invalid on ltcFrameRate (60 is not in the closed vocabulary)", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsMalformedLTCOffset(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"not-a-timecode"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "ltcDefaultStartOffset" {
		t.Fatalf("verr = %v, want field-invalid on ltcDefaultStartOffset", verr)
	}
}

func TestEncodeDecodeAudioSettingsPayloadRoundTrips(t *testing.T) {
	want := AudioSettingsDefaultPayload
	raw, err := EncodeAudioSettingsPayload(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, verr := DecodeAudioSettingsPayload(raw)
	if verr != nil {
		t.Fatalf("decode: %v", verr)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestDecodeAudioSettingsPayloadRejectsUnknownTopLevelKey(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00","extra":true}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("verr = %v, want ValidationCodeFieldUnknownKey", verr)
	}
}

// TestDecodeAudioSettingsPayloadRejectsAbsentField proves every field is
// required on every write — an absent key is refused by name rather than
// silently defaulted or carried forward from the previous revision
// (ADR-039's absent/null/empty rule, applied per field).
func TestDecodeAudioSettingsPayloadRejectsAbsentField(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"driftIgnoreThresholdMs", `{"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultFadeCurve", `{"driftIgnoreThresholdMs":10,"defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultFadeDurationMs", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultMaxBackgroundGainDb", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"ltcFrameRate", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcDefaultStartOffset":"00:00:00:00"}`},
		{"ltcDefaultStartOffset", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30"}`},
		{"duckTargetGainDb", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := DecodeAudioSettingsPayload(tc.raw)
			if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != tc.name {
				t.Fatalf("verr = %v, want field-required on %s", verr, tc.name)
			}
		})
	}
}

// TestDecodeAudioSettingsPayloadRejectsNullField proves a JSON null is
// distinguished from an absent key (the "a JSON null is not an absent key"
// defect class), for every field.
func TestDecodeAudioSettingsPayloadRejectsNullField(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"driftIgnoreThresholdMs", `{"driftIgnoreThresholdMs":null,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultFadeCurve", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":null,"defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultFadeDurationMs", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":null,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultMaxBackgroundGainDb", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":null,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"ltcFrameRate", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":null,"ltcDefaultStartOffset":"00:00:00:00"}`},
		{"ltcDefaultStartOffset", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":null}`},
		{"duckTargetGainDb", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":null,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := DecodeAudioSettingsPayload(tc.raw)
			if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != tc.name {
				t.Fatalf("verr = %v, want field-null on %s", verr, tc.name)
			}
		})
	}
}

func TestDecodeAudioSettingsPayloadRejectsEmptyFadeCurve(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldEmpty || verr.Field != "defaultFadeCurve" {
		t.Fatalf("verr = %v, want field-empty on defaultFadeCurve", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsUnknownFadeCurve(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"exponential","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "defaultFadeCurve" {
		t.Fatalf("verr = %v, want field-invalid on defaultFadeCurve (only linear ships)", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsOutOfRangeDrift(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":999999,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "driftIgnoreThresholdMs" {
		t.Fatalf("verr = %v, want field-invalid on driftIgnoreThresholdMs", verr)
	}
}

// Attenuation below unity is ordinary in decibels, so the old
// "negative is invalid" rule is gone; the typo guard is now the +12 dB
// ceiling on the other end.
func TestDecodeAudioSettingsPayloadAcceptsAttenuatingCeiling(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-24,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	p, verr := DecodeAudioSettingsPayload(raw)
	if verr != nil {
		t.Fatalf("a ceiling of -24 dB must decode: %v", verr)
	}
	if p.DefaultMaxBackgroundGainDb != -24 {
		t.Fatalf("defaultMaxBackgroundGainDb = %v, want -24", p.DefaultMaxBackgroundGainDb)
	}
}

func TestDecodeAudioSettingsPayloadRejectsExcessiveGain(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":20,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "defaultMaxBackgroundGainDb" {
		t.Fatalf("verr = %v, want field-invalid on defaultMaxBackgroundGainDb", verr)
	}
}

// The floor matches duckTargetGainDb's -60 dB: without it an
// operator reaching for the same number as the duck field gets a ceiling
// so low every background bed goes inaudible, and a very large negative
// value underflows to 0 with an error naming neither the field nor its
// bound.
func TestDecodeAudioSettingsPayloadRejectsBelowFloorGain(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-61,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "defaultMaxBackgroundGainDb" {
		t.Fatalf("verr = %v, want field-invalid on defaultMaxBackgroundGainDb", verr)
	}
	if !strings.Contains(verr.Detail, "defaultMaxBackgroundGainDb") || !strings.Contains(verr.Detail, "decibel") || !strings.Contains(verr.Detail, "-60") {
		t.Fatalf("refusal must name defaultMaxBackgroundGainDb, decibels, and -60, got %q", verr.Detail)
	}
}

func TestDecodeAudioSettingsPayloadAcceptsGainAtFloor(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-60,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	p, verr := DecodeAudioSettingsPayload(raw)
	if verr != nil {
		t.Fatalf("a ceiling of -60 dB (the floor, inclusive) must decode: %v", verr)
	}
	if p.DefaultMaxBackgroundGainDb != -60 {
		t.Fatalf("defaultMaxBackgroundGainDb = %v, want -60", p.DefaultMaxBackgroundGainDb)
	}
}

// The very-large-negative case from the issue: without the floor this
// underflows CeilingFromDb to 0 and is refused by [audio.Ceiling]'s own
// validity check instead of the named floor error.
func TestDecodeAudioSettingsPayloadRejectsVeryLargeNegativeGainWithNamedFloorError(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-1000,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "defaultMaxBackgroundGainDb" {
		t.Fatalf("verr = %v, want field-invalid on defaultMaxBackgroundGainDb", verr)
	}
	if !strings.Contains(verr.Detail, "-60") {
		t.Fatalf("refusal of a very large negative gain must name the -60 dB floor, got %q", verr.Detail)
	}
}

// The two pre-decibel names are refused by name, each naming its
// replacement. Silently accepting them would reinterpret a halving as a
// half-decibel lift, which is the whole reason the names changed.
func TestDecodeAudioSettingsPayloadRefusesPreDecibelGainNames(t *testing.T) {
	for old, replacement := range map[string]string{
		"defaultMaxBackgroundGain": "defaultMaxBackgroundGainDb",
		"duckTargetGain":           "duckTargetGainDb",
	} {
		raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-13.98,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00","` + old + `":0.5}`
		_, verr := DecodeAudioSettingsPayload(raw)
		if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != old {
			t.Fatalf("verr = %v, want field-invalid on %s", verr, old)
		}
		if !strings.Contains(verr.Detail, replacement) {
			t.Fatalf("refusal of %s must name %s, got %q", old, replacement, verr.Detail)
		}
	}
}

// The silence floor is a legitimate operator choice (the bed goes fully
// silent under an announcement), so it must decode, while 0 dB or louder
// is refused: it would not duck anything, and anything below the floor is
// already silence. The VALUE is the owner's to choose by ear; these are
// the bounds, not the choice.
func TestDecodeAudioSettingsPayloadAcceptsSilenceFloorDuckAndRefusesUnity(t *testing.T) {
	silent := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-60,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	p, verr := DecodeAudioSettingsPayload(silent)
	if verr != nil {
		t.Fatalf("a duck depth at the silence floor must decode: %v", verr)
	}
	if p.DuckTargetGainDb != -60 {
		t.Fatalf("duckTargetGainDb = %v, want -60", p.DuckTargetGainDb)
	}
	if got := audio.GainFromDb(p.DuckTargetGainDb); got != 0 {
		t.Fatalf("the silence floor must convert to a linear gain of exactly 0, got %v", got)
	}

	for _, raw := range []string{
		`{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":0,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`,
		`{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":3,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`,
		`{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-61,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`,
	} {
		if _, verr := DecodeAudioSettingsPayload(raw); verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "duckTargetGainDb" {
			t.Fatalf("verr = %v, want field-invalid on duckTargetGainDb for %s", verr, raw)
		}
	}
}

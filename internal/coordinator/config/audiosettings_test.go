package config

import "testing"

func validAudioSettingsPayloadJSON() string {
	return `{"driftIgnoreThresholdMs":25,"defaultFadeCurve":"linear","defaultFadeDurationMs":1500,"defaultMaxBackgroundGain":0.5,"duckTargetGain":0.2,"ltcFrameRate":"25","ltcDefaultStartOffset":"00:00:00:00"}`
}

func TestDecodeAudioSettingsPayloadAccepts(t *testing.T) {
	p, verr := DecodeAudioSettingsPayload(validAudioSettingsPayloadJSON())
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	want := AudioSettingsPayload{
		DriftIgnoreThresholdMs: 25, DefaultFadeCurve: "linear",
		DefaultFadeDurationMs: 1500, DefaultMaxBackgroundGain: 0.5,
		DuckTargetGain: 0.2,
		LTCFrameRate:   "25", LTCDefaultStartOffset: "00:00:00:00",
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
			raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"` + rate + `","ltcDefaultStartOffset":"00:00:00:00"}`
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
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"60","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "ltcFrameRate" {
		t.Fatalf("verr = %v, want field-invalid on ltcFrameRate (60 is not in the closed vocabulary)", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsMalformedLTCOffset(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"not-a-timecode"}`
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
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00","extra":true}`
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
		{"driftIgnoreThresholdMs", `{"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultFadeCurve", `{"driftIgnoreThresholdMs":10,"defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultFadeDurationMs", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultMaxBackgroundGain", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"ltcFrameRate", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcDefaultStartOffset":"00:00:00:00"}`},
		{"ltcDefaultStartOffset", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30"}`},
		{"duckTargetGain", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
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
		{"driftIgnoreThresholdMs", `{"driftIgnoreThresholdMs":null,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultFadeCurve", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":null,"defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultFadeDurationMs", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":null,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"defaultMaxBackgroundGain", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":null,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
		{"ltcFrameRate", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":null,"ltcDefaultStartOffset":"00:00:00:00"}`},
		{"ltcDefaultStartOffset", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":null}`},
		{"duckTargetGain", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":null,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`},
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
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldEmpty || verr.Field != "defaultFadeCurve" {
		t.Fatalf("verr = %v, want field-empty on defaultFadeCurve", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsUnknownFadeCurve(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"exponential","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "defaultFadeCurve" {
		t.Fatalf("verr = %v, want field-invalid on defaultFadeCurve (only linear ships)", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsOutOfRangeDrift(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":999999,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "driftIgnoreThresholdMs" {
		t.Fatalf("verr = %v, want field-invalid on driftIgnoreThresholdMs", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsNegativeGain(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":-1,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "defaultMaxBackgroundGain" {
		t.Fatalf("verr = %v, want field-invalid on defaultMaxBackgroundGain", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsExcessiveGain(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":10,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "defaultMaxBackgroundGain" {
		t.Fatalf("verr = %v, want field-invalid on defaultMaxBackgroundGain", verr)
	}
}

// A duck depth of zero is a legitimate operator choice (the bed goes
// fully silent under an announcement), so it must decode, while unity or
// more is refused: it would not duck anything. The VALUE is the owner's to
// choose by ear; these are the bounds, not the choice.
func TestDecodeAudioSettingsPayloadAcceptsSilentDuckAndRefusesUnity(t *testing.T) {
	silent := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":0,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`
	p, verr := DecodeAudioSettingsPayload(silent)
	if verr != nil {
		t.Fatalf("a duck depth of 0 must decode: %v", verr)
	}
	if p.DuckTargetGain != 0 {
		t.Fatalf("duckTargetGain = %v, want 0", p.DuckTargetGain)
	}

	for _, raw := range []string{
		`{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":1,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`,
		`{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"duckTargetGain":-0.1,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`,
	} {
		if _, verr := DecodeAudioSettingsPayload(raw); verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "duckTargetGain" {
			t.Fatalf("verr = %v, want field-invalid on duckTargetGain for %s", verr, raw)
		}
	}
}

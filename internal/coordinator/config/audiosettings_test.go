package config

import "testing"

func validAudioSettingsPayloadJSON() string {
	return `{"driftIgnoreThresholdMs":25,"defaultFadeCurve":"linear","defaultFadeDurationMs":1500,"defaultMaxBackgroundGain":0.5}`
}

func TestDecodeAudioSettingsPayloadAccepts(t *testing.T) {
	p, verr := DecodeAudioSettingsPayload(validAudioSettingsPayloadJSON())
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	want := AudioSettingsPayload{
		DriftIgnoreThresholdMs: 25, DefaultFadeCurve: "linear",
		DefaultFadeDurationMs: 1500, DefaultMaxBackgroundGain: 0.5,
	}
	if p != want {
		t.Errorf("payload = %+v, want %+v", p, want)
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
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6,"extra":true}`
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
		{"driftIgnoreThresholdMs", `{"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6}`},
		{"defaultFadeCurve", `{"driftIgnoreThresholdMs":10,"defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6}`},
		{"defaultFadeDurationMs", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultMaxBackgroundGain":0.6}`},
		{"defaultMaxBackgroundGain", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000}`},
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
		{"driftIgnoreThresholdMs", `{"driftIgnoreThresholdMs":null,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6}`},
		{"defaultFadeCurve", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":null,"defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6}`},
		{"defaultFadeDurationMs", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":null,"defaultMaxBackgroundGain":0.6}`},
		{"defaultMaxBackgroundGain", `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":null}`},
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
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldEmpty || verr.Field != "defaultFadeCurve" {
		t.Fatalf("verr = %v, want field-empty on defaultFadeCurve", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsUnknownFadeCurve(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"exponential","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "defaultFadeCurve" {
		t.Fatalf("verr = %v, want field-invalid on defaultFadeCurve (only linear ships)", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsOutOfRangeDrift(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":999999,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "driftIgnoreThresholdMs" {
		t.Fatalf("verr = %v, want field-invalid on driftIgnoreThresholdMs", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsNegativeGain(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":-1}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "defaultMaxBackgroundGain" {
		t.Fatalf("verr = %v, want field-invalid on defaultMaxBackgroundGain", verr)
	}
}

func TestDecodeAudioSettingsPayloadRejectsExcessiveGain(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":10,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":10}`
	_, verr := DecodeAudioSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "defaultMaxBackgroundGain" {
		t.Fatalf("verr = %v, want field-invalid on defaultMaxBackgroundGain", verr)
	}
}

package config

import "testing"

func validRenderSettingsPayloadJSON() string {
	return `{"idleOutput":"hold","restartPolicy":{"initialDelaySeconds":2,"maxDelaySeconds":45,"maxConsecutiveFastFailures":6}}`
}

func TestDecodeRenderSettingsPayloadAccepts(t *testing.T) {
	p, verr := DecodeRenderSettingsPayload(validRenderSettingsPayloadJSON())
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	if p.IdleOutput != "hold" {
		t.Errorf("idleOutput = %q, want hold", p.IdleOutput)
	}
	if p.RestartPolicy != (RenderRestartPolicy{InitialDelaySeconds: 2, MaxDelaySeconds: 45, MaxConsecutiveFastFailures: 6}) {
		t.Errorf("restartPolicy = %+v, want {2 45 6}", p.RestartPolicy)
	}
}

func TestEncodeDecodeRenderSettingsPayloadRoundTrips(t *testing.T) {
	want := RenderSettingsDefaultPayload
	raw, err := EncodeRenderSettingsPayload(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, verr := DecodeRenderSettingsPayload(raw)
	if verr != nil {
		t.Fatalf("decode: %v", verr)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestDecodeRenderSettingsPayloadRejectsUnknownTopLevelKey(t *testing.T) {
	raw := `{"idleOutput":"black","restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5},"extra":true}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("verr = %v, want ValidationCodeFieldUnknownKey", verr)
	}
}

func TestDecodeRenderSettingsPayloadRejectsUnknownNestedKey(t *testing.T) {
	raw := `{"idleOutput":"black","restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5,"extra":1}}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("verr = %v, want ValidationCodeFieldUnknownKey", verr)
	}
}

// TestDecodeRenderSettingsPayloadRejectsAbsentIdleOutput proves an absent
// key is refused by name rather than silently defaulted — PUT is a full
// replacement (see DecodeRenderSettingsPayload's own doc comment).
func TestDecodeRenderSettingsPayloadRejectsAbsentIdleOutput(t *testing.T) {
	raw := `{"restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5}}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "idleOutput" {
		t.Fatalf("verr = %v, want field-required on idleOutput", verr)
	}
}

func TestDecodeRenderSettingsPayloadRejectsNullIdleOutput(t *testing.T) {
	raw := `{"idleOutput":null,"restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5}}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "idleOutput" {
		t.Fatalf("verr = %v, want field-null on idleOutput", verr)
	}
}

func TestDecodeRenderSettingsPayloadRejectsUnknownIdleOutputValue(t *testing.T) {
	raw := `{"idleOutput":"strobe","restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5}}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "idleOutput" {
		t.Fatalf("verr = %v, want field-invalid on idleOutput", verr)
	}
}

func TestDecodeRenderSettingsPayloadRejectsAbsentRestartPolicy(t *testing.T) {
	raw := `{"idleOutput":"black"}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "restartPolicy" {
		t.Fatalf("verr = %v, want field-required on restartPolicy", verr)
	}
}

func TestDecodeRenderSettingsPayloadRejectsAbsentRestartPolicyField(t *testing.T) {
	raw := `{"idleOutput":"black","restartPolicy":{"maxDelaySeconds":30,"maxConsecutiveFastFailures":5}}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "restartPolicy.initialDelaySeconds" {
		t.Fatalf("verr = %v, want field-required on restartPolicy.initialDelaySeconds", verr)
	}
}

func TestDecodeRenderSettingsPayloadRejectsOutOfRangeInitialDelay(t *testing.T) {
	raw := `{"idleOutput":"black","restartPolicy":{"initialDelaySeconds":0,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5}}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "restartPolicy.initialDelaySeconds" {
		t.Fatalf("verr = %v, want field-invalid on restartPolicy.initialDelaySeconds", verr)
	}
}

func TestDecodeRenderSettingsPayloadRejectsOutOfRangeMaxDelay(t *testing.T) {
	raw := `{"idleOutput":"black","restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":301,"maxConsecutiveFastFailures":5}}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "restartPolicy.maxDelaySeconds" {
		t.Fatalf("verr = %v, want field-invalid on restartPolicy.maxDelaySeconds", verr)
	}
}

func TestDecodeRenderSettingsPayloadRejectsOutOfRangeFastFailures(t *testing.T) {
	raw := `{"idleOutput":"black","restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":21}}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "restartPolicy.maxConsecutiveFastFailures" {
		t.Fatalf("verr = %v, want field-invalid on restartPolicy.maxConsecutiveFastFailures", verr)
	}
}

// TestDecodeRenderSettingsPayloadRejectsMaxDelayBelowInitialDelay proves the
// cross-field rule: a backoff ceiling smaller than its own floor is refused
// naming both numbers, mirroring showsurface.go's geometry/channelCount
// cross-field check.
func TestDecodeRenderSettingsPayloadRejectsMaxDelayBelowInitialDelay(t *testing.T) {
	raw := `{"idleOutput":"black","restartPolicy":{"initialDelaySeconds":10,"maxDelaySeconds":5,"maxConsecutiveFastFailures":5}}`
	_, verr := DecodeRenderSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "restartPolicy.maxDelaySeconds" {
		t.Fatalf("verr = %v, want field-invalid on restartPolicy.maxDelaySeconds", verr)
	}
}

func TestDecodeRenderSettingsPayloadRejectsBodyNotAnObject(t *testing.T) {
	_, verr := DecodeRenderSettingsPayload(`"not an object"`)
	if verr == nil || verr.Code != ValidationCodeBodyInvalid {
		t.Fatalf("verr = %v, want body-invalid", verr)
	}
}

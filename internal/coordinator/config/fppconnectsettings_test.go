package config

import "testing"

func validFPPConnectSettingsPayloadJSON() string {
	return `{"enabled":true,"maxFileBytes":1073741824,"maxAssetDirBytes":10737418240}`
}

func TestDecodeFPPConnectSettingsPayloadAccepts(t *testing.T) {
	p, verr := DecodeFPPConnectSettingsPayload(validFPPConnectSettingsPayloadJSON())
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	want := FPPConnectSettingsPayload{Enabled: true, MaxFileBytes: 1073741824, MaxAssetDirBytes: 10737418240}
	if p != want {
		t.Errorf("payload = %+v, want %+v", p, want)
	}
}

func TestEncodeDecodeFPPConnectSettingsPayloadRoundTrips(t *testing.T) {
	want := FPPConnectSettingsDefaultPayload
	raw, err := EncodeFPPConnectSettingsPayload(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, verr := DecodeFPPConnectSettingsPayload(raw)
	if verr != nil {
		t.Fatalf("decode: %v", verr)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestFPPConnectSettingsDefaultPayloadValues(t *testing.T) {
	if !FPPConnectSettingsDefaultPayload.Enabled {
		t.Error("FPPConnectSettingsDefaultPayload.Enabled = false, want true (builder default)")
	}
	if FPPConnectSettingsDefaultPayload.MaxFileBytes != 2*bytesPerGiB {
		t.Errorf("MaxFileBytes = %d, want %d (2 GiB)", FPPConnectSettingsDefaultPayload.MaxFileBytes, 2*bytesPerGiB)
	}
	if FPPConnectSettingsDefaultPayload.MaxAssetDirBytes != 20*bytesPerGiB {
		t.Errorf("MaxAssetDirBytes = %d, want %d (20 GiB)", FPPConnectSettingsDefaultPayload.MaxAssetDirBytes, 20*bytesPerGiB)
	}
}

func TestDecodeFPPConnectSettingsPayloadRejectsUnknownTopLevelKey(t *testing.T) {
	raw := `{"enabled":true,"maxFileBytes":1,"maxAssetDirBytes":1,"extra":true}`
	_, verr := DecodeFPPConnectSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("verr = %v, want ValidationCodeFieldUnknownKey", verr)
	}
}

func TestDecodeFPPConnectSettingsPayloadRejectsAbsentField(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"enabled", `{"maxFileBytes":1,"maxAssetDirBytes":1}`},
		{"maxFileBytes", `{"enabled":true,"maxAssetDirBytes":1}`},
		{"maxAssetDirBytes", `{"enabled":true,"maxFileBytes":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := DecodeFPPConnectSettingsPayload(tc.raw)
			if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != tc.name {
				t.Fatalf("verr = %v, want field-required on %s", verr, tc.name)
			}
		})
	}
}

func TestDecodeFPPConnectSettingsPayloadRejectsNullField(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"enabled", `{"enabled":null,"maxFileBytes":1,"maxAssetDirBytes":1}`},
		{"maxFileBytes", `{"enabled":true,"maxFileBytes":null,"maxAssetDirBytes":1}`},
		{"maxAssetDirBytes", `{"enabled":true,"maxFileBytes":1,"maxAssetDirBytes":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := DecodeFPPConnectSettingsPayload(tc.raw)
			if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != tc.name {
				t.Fatalf("verr = %v, want field-null on %s", verr, tc.name)
			}
		})
	}
}

func TestDecodeFPPConnectSettingsPayloadRejectsMaxFileBytesBelowOne(t *testing.T) {
	raw := `{"enabled":true,"maxFileBytes":0,"maxAssetDirBytes":10}`
	_, verr := DecodeFPPConnectSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "maxFileBytes" {
		t.Fatalf("verr = %v, want field-invalid on maxFileBytes", verr)
	}
}

func TestDecodeFPPConnectSettingsPayloadRejectsMaxAssetDirBytesBelowOne(t *testing.T) {
	raw := `{"enabled":true,"maxFileBytes":1,"maxAssetDirBytes":0}`
	_, verr := DecodeFPPConnectSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "maxAssetDirBytes" {
		t.Fatalf("verr = %v, want field-invalid on maxAssetDirBytes", verr)
	}
}

func TestDecodeFPPConnectSettingsPayloadRejectsAssetDirBelowFileCap(t *testing.T) {
	raw := `{"enabled":true,"maxFileBytes":1000,"maxAssetDirBytes":999}`
	_, verr := DecodeFPPConnectSettingsPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "maxAssetDirBytes" {
		t.Fatalf("verr = %v, want field-invalid on maxAssetDirBytes (below maxFileBytes)", verr)
	}
}

func TestDecodeFPPConnectSettingsPayloadAcceptsAssetDirEqualToFileCap(t *testing.T) {
	raw := `{"enabled":true,"maxFileBytes":1000,"maxAssetDirBytes":1000}`
	p, verr := DecodeFPPConnectSettingsPayload(raw)
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	if p.MaxAssetDirBytes != 1000 {
		t.Errorf("MaxAssetDirBytes = %d, want 1000", p.MaxAssetDirBytes)
	}
}

func TestDecodeFPPConnectSettingsPayloadAcceptsDisabled(t *testing.T) {
	raw := `{"enabled":false,"maxFileBytes":1,"maxAssetDirBytes":1}`
	p, verr := DecodeFPPConnectSettingsPayload(raw)
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	if p.Enabled {
		t.Error("Enabled = true, want false")
	}
}

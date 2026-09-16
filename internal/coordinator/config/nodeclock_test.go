package config

import (
	"reflect"
	"testing"
)

func validNodeClockManagedPayloadJSON() string {
	return `{"provider":"managed","interface":"eth0","domain":24}`
}

func TestDecodeNodeClockPayloadAcceptsManagedWithDefaults(t *testing.T) {
	p, verr := DecodeNodeClockPayload(validNodeClockManagedPayloadJSON())
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	want := NodeClockPayload{
		Provider: "managed", Interface: "eth0", Domain: 24,
		HoldoverLimitSeconds: DefaultNodeClockHoldoverLimitSeconds,
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("payload = %+v, want %+v", p, want)
	}
}

func TestDecodeNodeClockPayloadAcceptsManagedEveryField(t *testing.T) {
	raw := `{"provider":"managed","interface":"eth0","domain":24,"clientOnly":true,` +
		`"holdoverLimitSeconds":90,"priority1":100,"hardwareTimestamping":true}`
	p, verr := DecodeNodeClockPayload(raw)
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	want := NodeClockPayload{
		Provider: "managed", Interface: "eth0", Domain: 24,
		ClientOnly: true, HoldoverLimitSeconds: 90,
		Priority1: 100, HardwareTimestamping: true,
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("payload = %+v, want %+v", p, want)
	}
}

func TestDecodeNodeClockPayloadAcceptsExternal(t *testing.T) {
	raw := `{"provider":"external","interface":"eth0","domain":0,"externalUdsAddress":"/run/ptp/ro"}`
	p, verr := DecodeNodeClockPayload(raw)
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	if p.ExternalUDSAddress != "/run/ptp/ro" {
		t.Errorf("externalUdsAddress = %q", p.ExternalUDSAddress)
	}
}

func TestDecodeNodeClockPayloadRequiresFPPBaseURLForFPPProvider(t *testing.T) {
	raw := `{"provider":"fpp","interface":"eth0","domain":0}`
	_, verr := DecodeNodeClockPayload(raw)
	if verr == nil || verr.Field != "fppBaseUrl" {
		t.Fatalf("verr = %v, want a required-field error on fppBaseUrl", verr)
	}
}

func TestDecodeNodeClockPayloadAcceptsFPPWithBaseURL(t *testing.T) {
	raw := `{"provider":"fpp","interface":"eth0","domain":0,"fppBaseUrl":"http://fpp-host.local"}`
	p, verr := DecodeNodeClockPayload(raw)
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	if p.FPPBaseURL != "http://fpp-host.local" {
		t.Errorf("fppBaseUrl = %q", p.FPPBaseURL)
	}
}

func TestDecodeNodeClockPayloadRejectsUnknownProvider(t *testing.T) {
	raw := `{"provider":"bogus","interface":"eth0","domain":0}`
	_, verr := DecodeNodeClockPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "provider" {
		t.Fatalf("verr = %v, want ValidationCodeFieldInvalid on provider", verr)
	}
}

func TestDecodeNodeClockPayloadRejectsMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		field string
	}{
		{"no provider", `{"interface":"eth0","domain":0}`, "provider"},
		{"no interface", `{"provider":"managed","domain":0}`, "interface"},
		{"no domain", `{"provider":"managed","interface":"eth0"}`, "domain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := DecodeNodeClockPayload(tc.raw)
			if verr == nil || verr.Field != tc.field {
				t.Fatalf("verr = %v, want a required-field error on %s", verr, tc.field)
			}
		})
	}
}

func TestDecodeNodeClockPayloadRejectsDomainOutOfRange(t *testing.T) {
	raw := `{"provider":"managed","interface":"eth0","domain":300}`
	_, verr := DecodeNodeClockPayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "domain" {
		t.Fatalf("verr = %v, want ValidationCodeFieldInvalid on domain", verr)
	}
}

// TestDecodeNodeClockPayloadAcceptsPHCDeviceOnExternal proves the new
// field decodes on the one provider it applies to.
func TestDecodeNodeClockPayloadAcceptsPHCDeviceOnExternal(t *testing.T) {
	raw := `{"provider":"external","interface":"eno2","domain":0,"phcDevice":"/dev/ptp0"}`
	p, verr := DecodeNodeClockPayload(raw)
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	if p.PHCDevice != "/dev/ptp0" {
		t.Errorf("phcDevice = %q, want /dev/ptp0", p.PHCDevice)
	}
}

// TestDecodeNodeClockPayloadRejectsPHCDeviceOnManaged proves phcDevice is
// refused, not silently ignored, on the managed provider: an ignored field
// would read as an applied one.
func TestDecodeNodeClockPayloadRejectsPHCDeviceOnManaged(t *testing.T) {
	raw := `{"provider":"managed","interface":"eth0","domain":0,"phcDevice":"/dev/ptp0"}`
	_, verr := DecodeNodeClockPayload(raw)
	if verr == nil || verr.Field != "phcDevice" {
		t.Fatalf("verr = %v, want a refusal naming phcDevice", verr)
	}
}

// TestDecodeNodeClockPayloadRejectsPHCDeviceOnFPP mirrors the managed
// case for the third provider.
func TestDecodeNodeClockPayloadRejectsPHCDeviceOnFPP(t *testing.T) {
	raw := `{"provider":"fpp","interface":"eth0","domain":0,"fppBaseUrl":"http://fpp-host.local","phcDevice":"/dev/ptp0"}`
	_, verr := DecodeNodeClockPayload(raw)
	if verr == nil || verr.Field != "phcDevice" {
		t.Fatalf("verr = %v, want a refusal naming phcDevice", verr)
	}
}

// TestDecodeNodeClockPayloadRejectsPHCDeviceBadFormat proves the pattern
// is enforced, not merely non-empty: an interface name, a relative path,
// or a trailing slash must all be refused rather than accepted and
// mismatched against the real PHC index later at read time.
func TestDecodeNodeClockPayloadRejectsPHCDeviceBadFormat(t *testing.T) {
	cases := []string{"eth0", "ptp0", "/dev/ptp", "/dev/ptp0/", "/dev/ptpA", "dev/ptp0"}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			raw := `{"provider":"external","interface":"eno2","domain":0,"phcDevice":"` + bad + `"}`
			_, verr := DecodeNodeClockPayload(raw)
			if verr == nil || verr.Field != "phcDevice" {
				t.Fatalf("phcDevice %q: verr = %v, want a refusal naming phcDevice", bad, verr)
			}
		})
	}
}

// TestDecodeNodeClockPayloadOlderExternalStoredWithoutPHCDeviceDecodesUnchanged
// pins backward compatibility: a node.clock object written before this
// field existed has no "phcDevice" key at all, and must decode to the
// empty string, never an error.
func TestDecodeNodeClockPayloadOlderExternalStoredWithoutPHCDeviceDecodesUnchanged(t *testing.T) {
	raw := `{"provider":"external","interface":"eno2","domain":0,"externalUdsAddress":"/run/ptp/ro"}`
	p, verr := DecodeNodeClockPayload(raw)
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	if p.PHCDevice != "" {
		t.Errorf("phcDevice = %q, want empty for a payload predating this field", p.PHCDevice)
	}
}

func TestDecodeNodeClockPayloadRejectsUnknownTopLevelKey(t *testing.T) {
	raw := `{"provider":"managed","interface":"eth0","domain":0,"bogus":true}`
	_, verr := DecodeNodeClockPayload(raw)
	if verr == nil {
		t.Fatalf("expected an error for an unknown top-level key")
	}
}

func TestEncodeDecodeNodeClockPayloadRoundTrips(t *testing.T) {
	want := NodeClockPayload{
		Provider: "external", Interface: "eth0", Domain: 24,
		HoldoverLimitSeconds: 45, ExternalUDSAddress: "/run/ptp/ro",
	}
	raw, err := EncodeNodeClockPayload(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, verr := DecodeNodeClockPayload(raw)
	if verr != nil {
		t.Fatalf("decode: %v", verr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestEncodeDecodeNodeClockPayloadRoundTripsPHCDevice is
// TestEncodeDecodeNodeClockPayloadRoundTrips's own sibling for the new
// field.
func TestEncodeDecodeNodeClockPayloadRoundTripsPHCDevice(t *testing.T) {
	want := NodeClockPayload{
		Provider: "external", Interface: "eno2", Domain: 0,
		HoldoverLimitSeconds: DefaultNodeClockHoldoverLimitSeconds,
		ExternalUDSAddress:   "/run/ptp/ro", PHCDevice: "/dev/ptp0",
	}
	raw, err := EncodeNodeClockPayload(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, verr := DecodeNodeClockPayload(raw)
	if verr != nil {
		t.Fatalf("decode: %v", verr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestValidateNodeClockObjectID(t *testing.T) {
	if verr := ValidateNodeClockObjectID("node-1"); verr != nil {
		t.Errorf("unexpected error for a valid node id: %v", verr)
	}
	if verr := ValidateNodeClockObjectID(""); verr == nil {
		t.Errorf("expected an error for an empty node id")
	}
}

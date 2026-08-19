package config

import "testing"

func validAudioNodePayloadJSON() string {
	return `{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","clockDomain":"single-interface","clockDomainProvenance":"one physical interface, both routes on it"}`
}

func TestDecodeAudioNodePayloadAccepts(t *testing.T) {
	p, verr := DecodeAudioNodePayload(validAudioNodePayloadJSON())
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	want := AudioNodePayload{
		ProgramRoute: "hw:0,0", LTCRoute: "hw:0,0",
		ClockDomain: "single-interface", ClockDomainProvenance: "one physical interface, both routes on it",
	}
	if p != want {
		t.Errorf("payload = %+v, want %+v", p, want)
	}
}

func TestEncodeDecodeAudioNodePayloadRoundTrips(t *testing.T) {
	want := AudioNodePayload{
		ProgramRoute: "hw:1,0", LTCRoute: "hw:1,0",
		ClockDomain: "domain-a", ClockDomainProvenance: "datasheet",
	}
	raw, err := EncodeAudioNodePayload(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, verr := DecodeAudioNodePayload(raw)
	if verr != nil {
		t.Fatalf("decode: %v", verr)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestDecodeAudioNodePayloadRejectsUnknownTopLevelKey(t *testing.T) {
	raw := `{"programRoute":"a","ltcRoute":"a","clockDomain":"d","clockDomainProvenance":"p","extra":true}`
	_, verr := DecodeAudioNodePayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("verr = %v, want ValidationCodeFieldUnknownKey", verr)
	}
}

// TestDecodeAudioNodePayloadRejectsAbsentField proves every field is
// required on every write — PUT is a full replacement, matching every
// other collection kind in this package.
func TestDecodeAudioNodePayloadRejectsAbsentField(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"programRoute", `{"ltcRoute":"a","clockDomain":"d","clockDomainProvenance":"p"}`},
		{"ltcRoute", `{"programRoute":"a","clockDomain":"d","clockDomainProvenance":"p"}`},
		{"clockDomain", `{"programRoute":"a","ltcRoute":"a","clockDomainProvenance":"p"}`},
		{"clockDomainProvenance", `{"programRoute":"a","ltcRoute":"a","clockDomain":"d"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := DecodeAudioNodePayload(tc.raw)
			if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != tc.name {
				t.Fatalf("verr = %v, want field-required on %s", verr, tc.name)
			}
		})
	}
}

// TestDecodeAudioNodePayloadRejectsNullField proves a JSON null is
// distinguished from an absent key, for every field.
func TestDecodeAudioNodePayloadRejectsNullField(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"programRoute", `{"programRoute":null,"ltcRoute":"a","clockDomain":"d","clockDomainProvenance":"p"}`},
		{"ltcRoute", `{"programRoute":"a","ltcRoute":null,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"clockDomain", `{"programRoute":"a","ltcRoute":"a","clockDomain":null,"clockDomainProvenance":"p"}`},
		{"clockDomainProvenance", `{"programRoute":"a","ltcRoute":"a","clockDomain":"d","clockDomainProvenance":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := DecodeAudioNodePayload(tc.raw)
			if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != tc.name {
				t.Fatalf("verr = %v, want field-null on %s", verr, tc.name)
			}
		})
	}
}

// TestDecodeAudioNodePayloadRejectsEmptyField proves an explicitly empty
// string is refused too — absent, null, and empty are three distinct
// refusals, not one.
func TestDecodeAudioNodePayloadRejectsEmptyField(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"programRoute", `{"programRoute":"","ltcRoute":"a","clockDomain":"d","clockDomainProvenance":"p"}`},
		{"ltcRoute", `{"programRoute":"a","ltcRoute":"","clockDomain":"d","clockDomainProvenance":"p"}`},
		{"clockDomain", `{"programRoute":"a","ltcRoute":"a","clockDomain":"","clockDomainProvenance":"p"}`},
		{"clockDomainProvenance", `{"programRoute":"a","ltcRoute":"a","clockDomain":"d","clockDomainProvenance":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := DecodeAudioNodePayload(tc.raw)
			if verr == nil || verr.Code != ValidationCodeFieldEmpty || verr.Field != tc.name {
				t.Fatalf("verr = %v, want field-empty on %s", verr, tc.name)
			}
		})
	}
}

func TestValidateAudioNodeObjectID(t *testing.T) {
	if verr := ValidateAudioNodeObjectID("render-node-01"); verr != nil {
		t.Errorf("unexpected error for valid id: %v", verr)
	}
	if verr := ValidateAudioNodeObjectID("Not Valid!"); verr == nil {
		t.Errorf("expected error for invalid id")
	}
}

func TestValidateAudioNodePlacementAcceptsEvidencedRoutes(t *testing.T) {
	p := AudioNodePayload{ProgramRoute: "hw:0,0", LTCRoute: "hw:0,0"}
	if err := ValidateAudioNodePlacement(p, []string{"hw:0,0"}, []string{"hw:0,0"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateAudioNodePlacementRejectsNoEvidence(t *testing.T) {
	p := AudioNodePayload{ProgramRoute: "hw:0,0", LTCRoute: "hw:0,0"}
	err := ValidateAudioNodePlacement(p, nil, nil)
	if err != ErrAudioNodeNoEvidence {
		t.Errorf("err = %v, want ErrAudioNodeNoEvidence", err)
	}
}

func TestValidateAudioNodePlacementRejectsUnevidencedProgramRoute(t *testing.T) {
	p := AudioNodePayload{ProgramRoute: "hw:9,9", LTCRoute: "hw:0,0"}
	err := ValidateAudioNodePlacement(p, []string{"hw:0,0"}, []string{"hw:0,0"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestValidateAudioNodePlacementRejectsProgramOnlyRouteAsLTC proves the
// discrete-LTC refusal: a route that only achieved program-capable
// (>=1 channel) evidence, never LTC-capable (>=3 channel) evidence, is
// refused as an LTC route even though it is a real, evidenced route.
func TestValidateAudioNodePlacementRejectsProgramOnlyRouteAsLTC(t *testing.T) {
	p := AudioNodePayload{ProgramRoute: "hw:0,0", LTCRoute: "hw:0,0"}
	err := ValidateAudioNodePlacement(p, []string{"hw:0,0"}, nil)
	if err == nil {
		t.Fatal("expected error: hw:0,0 was never evidenced as LTC-capable")
	}
}

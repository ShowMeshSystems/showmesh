package config

import (
	"reflect"
	"testing"
)

func validAudioNodePayloadJSON() string {
	return `{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","programChannels":[1,2],"ltcChannel":3,` +
		`"clockDomain":"single-interface","clockDomainProvenance":"one physical interface, both routes on it"}`
}

func TestDecodeAudioNodePayloadAccepts(t *testing.T) {
	p, verr := DecodeAudioNodePayload(validAudioNodePayloadJSON())
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	want := AudioNodePayload{
		ProgramRoute: "hw:0,0", LTCRoute: "hw:0,0",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain: "single-interface", ClockDomainProvenance: "one physical interface, both routes on it",
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("payload = %+v, want %+v", p, want)
	}
}

// TestDecodeAudioNodePayloadAcceptsMonoProgram proves programChannels is
// not hardcoded to stereo: a single-element list is a valid mono layout.
func TestDecodeAudioNodePayloadAcceptsMonoProgram(t *testing.T) {
	raw := `{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","programChannels":[1],"ltcChannel":2,` +
		`"clockDomain":"d","clockDomainProvenance":"p"}`
	p, verr := DecodeAudioNodePayload(raw)
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	if !reflect.DeepEqual(p.ProgramChannels, []int{1}) || p.LTCChannel != 2 {
		t.Errorf("payload = %+v, want programChannels=[1] ltcChannel=2", p)
	}
}

func TestEncodeDecodeAudioNodePayloadRoundTrips(t *testing.T) {
	want := AudioNodePayload{
		ProgramRoute: "hw:1,0", LTCRoute: "hw:1,0",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
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
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestDecodeAudioNodePayloadRejectsUnknownTopLevelKey(t *testing.T) {
	raw := `{"programRoute":"a","ltcRoute":"a","programChannels":[1],"ltcChannel":2,` +
		`"clockDomain":"d","clockDomainProvenance":"p","extra":true}`
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
		{"programRoute", `{"ltcRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"ltcRoute", `{"programRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"programChannels", `{"programRoute":"a","ltcRoute":"a","ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"ltcChannel", `{"programRoute":"a","ltcRoute":"a","programChannels":[1],"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"clockDomain", `{"programRoute":"a","ltcRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomainProvenance":"p"}`},
		{"clockDomainProvenance", `{"programRoute":"a","ltcRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomain":"d"}`},
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
		{"programRoute", `{"programRoute":null,"ltcRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"ltcRoute", `{"programRoute":"a","ltcRoute":null,"programChannels":[1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"programChannels", `{"programRoute":"a","ltcRoute":"a","programChannels":null,"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"ltcChannel", `{"programRoute":"a","ltcRoute":"a","programChannels":[1],"ltcChannel":null,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"clockDomain", `{"programRoute":"a","ltcRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomain":null,"clockDomainProvenance":"p"}`},
		{"clockDomainProvenance", `{"programRoute":"a","ltcRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":null}`},
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
		{"programRoute", `{"programRoute":"","ltcRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"ltcRoute", `{"programRoute":"a","ltcRoute":"","programChannels":[1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"clockDomain", `{"programRoute":"a","ltcRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomain":"","clockDomainProvenance":"p"}`},
		{"clockDomainProvenance", `{"programRoute":"a","ltcRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":""}`},
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

// TestDecodeAudioNodePayloadRejectsEmptyProgramChannels proves an
// explicitly empty array is a third, distinct refusal from absent/null.
func TestDecodeAudioNodePayloadRejectsEmptyProgramChannels(t *testing.T) {
	raw := `{"programRoute":"a","ltcRoute":"a","programChannels":[],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`
	_, verr := DecodeAudioNodePayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldEmpty || verr.Field != "programChannels" {
		t.Fatalf("verr = %v, want field-empty on programChannels", verr)
	}
}

// TestDecodeAudioNodePayloadRejectsZeroProgramChannel proves a zero index
// is refused, not treated as a valid (if unusual) channel.
func TestDecodeAudioNodePayloadRejectsZeroProgramChannel(t *testing.T) {
	raw := `{"programRoute":"a","ltcRoute":"a","programChannels":[0,1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`
	_, verr := DecodeAudioNodePayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "programChannels[0]" {
		t.Fatalf("verr = %v, want field-invalid on programChannels[0]", verr)
	}
}

// TestDecodeAudioNodePayloadRejectsNegativeProgramChannel proves a
// negative index is refused.
func TestDecodeAudioNodePayloadRejectsNegativeProgramChannel(t *testing.T) {
	raw := `{"programRoute":"a","ltcRoute":"a","programChannels":[1,-2],"ltcChannel":3,"clockDomain":"d","clockDomainProvenance":"p"}`
	_, verr := DecodeAudioNodePayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "programChannels[1]" {
		t.Fatalf("verr = %v, want field-invalid on programChannels[1]", verr)
	}
}

// TestDecodeAudioNodePayloadRejectsDuplicateProgramChannel proves a
// repeated index within programChannels is refused with its own code.
func TestDecodeAudioNodePayloadRejectsDuplicateProgramChannel(t *testing.T) {
	raw := `{"programRoute":"a","ltcRoute":"a","programChannels":[1,1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`
	_, verr := DecodeAudioNodePayload(raw)
	if verr == nil || verr.Code != ValidationCodeAudioNodeChannelDuplicate {
		t.Fatalf("verr = %v, want ValidationCodeAudioNodeChannelDuplicate", verr)
	}
}

// TestDecodeAudioNodePayloadRejectsZeroLTCChannel proves ltcChannel is
// bound by the same positive-index rule as programChannels.
func TestDecodeAudioNodePayloadRejectsZeroLTCChannel(t *testing.T) {
	raw := `{"programRoute":"a","ltcRoute":"a","programChannels":[1,2],"ltcChannel":0,"clockDomain":"d","clockDomainProvenance":"p"}`
	_, verr := DecodeAudioNodePayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "ltcChannel" {
		t.Fatalf("verr = %v, want field-invalid on ltcChannel", verr)
	}
}

// TestDecodeAudioNodePayloadRejectsLTCChannelOverlappingProgram proves
// ltcChannel appearing in programChannels is refused with its own code,
// distinguishable from an ordinary bad value.
func TestDecodeAudioNodePayloadRejectsLTCChannelOverlappingProgram(t *testing.T) {
	raw := `{"programRoute":"a","ltcRoute":"a","programChannels":[1,2],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`
	_, verr := DecodeAudioNodePayload(raw)
	if verr == nil || verr.Code != ValidationCodeAudioNodeChannelOverlap || verr.Field != "ltcChannel" {
		t.Fatalf("verr = %v, want ValidationCodeAudioNodeChannelOverlap on ltcChannel", verr)
	}
}

// TestDecodeAudioNodePayloadRejectsRouteMismatch proves programRoute and
// ltcRoute naming different routes is refused with its own code: program
// and LTC leave through one interface in one clock domain.
func TestDecodeAudioNodePayloadRejectsRouteMismatch(t *testing.T) {
	raw := `{"programRoute":"hw:0,0","ltcRoute":"hw:1,0","programChannels":[1,2],"ltcChannel":3,"clockDomain":"d","clockDomainProvenance":"p"}`
	_, verr := DecodeAudioNodePayload(raw)
	if verr == nil || verr.Code != ValidationCodeAudioNodeRouteMismatch || verr.Field != "ltcRoute" {
		t.Fatalf("verr = %v, want ValidationCodeAudioNodeRouteMismatch on ltcRoute", verr)
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

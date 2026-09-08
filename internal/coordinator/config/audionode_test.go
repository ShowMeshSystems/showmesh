package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func validAudioNodePayloadJSON() string {
	return `{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","programChannels":[1,2],"ltcChannel":3,` +
		`"clockDomain":"single-interface","clockDomainProvenance":"one physical interface, both routes on it"}`
}

// TestDecodeAudioNodePayloadAccepts decodes validAudioNodePayloadJSON, the
// pre-ADR-045 wire shape with no "role" or "zone" key — the fixture every
// pre-existing one-node installation's stored audio.node object matches.
// It must still decode, defaulting Role to "program+ltc" (this installation
// already implicitly WAS the sole program+ltc node) rather than refusing an
// absent field newly required.
func TestDecodeAudioNodePayloadAccepts(t *testing.T) {
	p, verr := DecodeAudioNodePayload(validAudioNodePayloadJSON())
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	want := AudioNodePayload{
		ProgramRoute: "hw:0,0", LTCRoute: "hw:0,0",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain: "single-interface", ClockDomainProvenance: "one physical interface, both routes on it",
		Role: AudioNodeRoleProgramLTC,
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
		Role: AudioNodeRoleProgram,
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

// TestEncodeDecodeAudioNodePayloadRoundTripsZone is the "zone" role's own
// round trip: Zone is populated only when Role is "zone".
func TestEncodeDecodeAudioNodePayloadRoundTripsZone(t *testing.T) {
	zone := "porch"
	want := AudioNodePayload{
		ProgramRoute: "hw:2,0", LTCRoute: "hw:2,0",
		ProgramChannels: []int{1}, LTCChannel: 2,
		ClockDomain: "domain-b", ClockDomainProvenance: "datasheet",
		Role: AudioNodeRoleZone, Zone: &zone,
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

// TestDecodeAudioNodePayloadRoleDefaultsProgramLTC proves absence of "role"
// defaults to "program+ltc" — ADR-045's backward-compatibility rule for a
// pre-existing one-node installation's stored payload.
func TestDecodeAudioNodePayloadRoleDefaultsProgramLTC(t *testing.T) {
	p, verr := DecodeAudioNodePayload(validAudioNodePayloadJSON())
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	if p.Role != AudioNodeRoleProgramLTC {
		t.Fatalf("role = %q, want %q", p.Role, AudioNodeRoleProgramLTC)
	}
}

// TestDecodeAudioNodePayloadRoleZone proves an explicit "zone" role decodes
// with its zone name.
func TestDecodeAudioNodePayloadRoleZone(t *testing.T) {
	raw := `{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","programChannels":[1],"ltcChannel":2,` +
		`"clockDomain":"d","clockDomainProvenance":"p","role":"zone","zone":"porch"}`
	p, verr := DecodeAudioNodePayload(raw)
	if verr != nil {
		t.Fatalf("unexpected error: %v", verr)
	}
	if p.Role != AudioNodeRoleZone || p.Zone == nil || *p.Zone != "porch" {
		t.Fatalf("unexpected role/zone: role=%q zone=%v", p.Role, p.Zone)
	}
}

// TestDecodeAudioNodePayloadRejectsUnknownRole proves "role" is a closed
// enum.
func TestDecodeAudioNodePayloadRejectsUnknownRole(t *testing.T) {
	raw := `{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","programChannels":[1],"ltcChannel":2,` +
		`"clockDomain":"d","clockDomainProvenance":"p","role":"surround"}`
	_, verr := DecodeAudioNodePayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "role" {
		t.Fatalf("verr = %v, want field-invalid on role", verr)
	}
}

// TestDecodeAudioNodePayloadRejectsZoneOutsideZoneRole proves "zone" is
// refused whenever role is not "zone" — an ignored field would read as an
// applied one, matching show.cue's outputs.announcement.duckGainDb
// precedent.
func TestDecodeAudioNodePayloadRejectsZoneOutsideZoneRole(t *testing.T) {
	raw := `{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","programChannels":[1],"ltcChannel":2,` +
		`"clockDomain":"d","clockDomainProvenance":"p","role":"program","zone":"porch"}`
	_, verr := DecodeAudioNodePayload(raw)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "zone" {
		t.Fatalf("verr = %v, want field-invalid on zone", verr)
	}
}

// --- ValidateAudioNodeRoleUniqueness (ADR-045: one program+ltc node) ---

func TestValidateAudioNodeRoleUniquenessAllowsSoleProgramLTC(t *testing.T) {
	p := AudioNodePayload{Role: AudioNodeRoleProgramLTC}
	if err := ValidateAudioNodeRoleUniqueness("node-a", p, map[string]string{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidateAudioNodeRoleUniquenessAllowsSelfRewrite proves re-writing the
// existing sole program+ltc node's own revision is not refused against
// itself: existingRoles is documented to exclude id, and this test pins
// that the safeguard also holds if a caller passed it in anyway.
func TestValidateAudioNodeRoleUniquenessAllowsSelfRewrite(t *testing.T) {
	p := AudioNodePayload{Role: AudioNodeRoleProgramLTC}
	existing := map[string]string{"node-a": AudioNodeRoleProgramLTC}
	if err := ValidateAudioNodeRoleUniqueness("node-a", p, existing); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidateAudioNodeRoleUniquenessRefusesSecondProgramLTC is ADR-045's
// own mandated refusal: a second program+ltc node is refused, naming BOTH
// node ids.
func TestValidateAudioNodeRoleUniquenessRefusesSecondProgramLTC(t *testing.T) {
	p := AudioNodePayload{Role: AudioNodeRoleProgramLTC}
	existing := map[string]string{"node-a": AudioNodeRoleProgramLTC}
	err := ValidateAudioNodeRoleUniqueness("node-b", p, existing)
	if err == nil {
		t.Fatalf("expected an error for a second program+ltc node")
	}
	if !strings.Contains(err.Error(), "node-a") || !strings.Contains(err.Error(), "node-b") {
		t.Fatalf("error must name both node ids, got: %v", err)
	}
}

// TestValidateAudioNodeRoleUniquenessAllowsMultipleZones proves the
// uniqueness rule is scoped to "program+ltc" only: any number of "zone" (or
// "program") nodes may coexist.
func TestValidateAudioNodeRoleUniquenessAllowsMultipleZones(t *testing.T) {
	p := AudioNodePayload{Role: AudioNodeRoleZone}
	existing := map[string]string{
		"node-a": AudioNodeRoleProgramLTC,
		"node-b": AudioNodeRoleZone,
	}
	if err := ValidateAudioNodeRoleUniqueness("node-c", p, existing); err != nil {
		t.Fatalf("unexpected error: %v", err)
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

// TestDecodeAudioNodePayloadAcceptsProgramOnly proves a node with no LTC
// at all decodes: both ltcRoute and ltcChannel omitted together. This is
// the only shape a two-output interface can be declared in, because
// ADR-018 needs a channel discrete from the program pair to carry LTC
// and such a device has none to spare.
func TestDecodeAudioNodePayloadAcceptsProgramOnly(t *testing.T) {
	raw := `{"programRoute":"hw:CARD=USB,DEV=0","programChannels":[1,2],"clockDomain":"solo","clockDomainProvenance":"single interface"}`
	p, verr := DecodeAudioNodePayload(raw)
	if verr != nil {
		t.Fatalf("verr = %v, want nil", verr)
	}
	if p.LTCRoute != "" {
		t.Errorf("LTCRoute = %q, want empty on a program-only node", p.LTCRoute)
	}
	if p.LTCChannel != 0 {
		t.Errorf("LTCChannel = %d, want 0 on a program-only node", p.LTCChannel)
	}
	if p.ProgramRoute != "hw:CARD=USB,DEV=0" || len(p.ProgramChannels) != 2 {
		t.Errorf("program half decoded wrong: %+v", p)
	}
}

// TestEncodeDecodeProgramOnlyRoundTrips proves a program-only payload
// survives the store: it must not come back with an empty ltcRoute and a
// zero ltcChannel present as keys, or the re-decode would refuse it.
func TestEncodeDecodeProgramOnlyRoundTrips(t *testing.T) {
	want := AudioNodePayload{
		ProgramRoute: "hw:CARD=USB,DEV=0", ProgramChannels: []int{1, 2},
		ClockDomain: "solo", ClockDomainProvenance: "single interface",
	}
	encoded, err := EncodeAudioNodePayload(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, verr := DecodeAudioNodePayload(encoded)
	if verr != nil {
		t.Fatalf("re-decode of %s: %v", encoded, verr)
	}
	if got.LTCRoute != "" || got.LTCChannel != 0 {
		t.Errorf("round trip invented LTC: %+v", got)
	}
	if got.ProgramRoute != want.ProgramRoute || got.ClockDomain != want.ClockDomain {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestDecodeAudioNodePayloadRejectsHalfDeclaredLTC proves one of the LTC
// pair without the other is refused rather than half-honoured: naming a
// route with no channel, or a channel with no route, is an operator
// mistake with two plausible readings.
func TestDecodeAudioNodePayloadRejectsHalfDeclaredLTC(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantField string
	}{
		{"route without channel", `{"programRoute":"a","ltcRoute":"a","programChannels":[1],"clockDomain":"d","clockDomainProvenance":"p"}`, "ltcChannel"},
		{"channel without route", `{"programRoute":"a","ltcChannel":2,"programChannels":[1],"clockDomain":"d","clockDomainProvenance":"p"}`, "ltcRoute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := DecodeAudioNodePayload(tc.raw)
			if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != tc.wantField {
				t.Fatalf("verr = %v, want field-required on %s", verr, tc.wantField)
			}
		})
	}
}

// TestValidateAudioNodePlacementAcceptsProgramOnlyOnTwoOutputDevice is
// the defect this whole shape exists for: a node whose only routes are
// two-channel advertises an EMPTY LTC-capable route list, so before
// program-only declarations existed no audio.node could be placed on it
// at all.
func TestValidateAudioNodePlacementAcceptsProgramOnlyOnTwoOutputDevice(t *testing.T) {
	p := AudioNodePayload{
		ProgramRoute: "hw:CARD=USB,DEV=0", ProgramChannels: []int{1, 2},
		ClockDomain: "solo", ClockDomainProvenance: "single interface",
	}
	if err := ValidateAudioNodePlacement(p, []string{"hw:CARD=USB,DEV=0", "hw:CARD=Headphones,DEV=0"}, nil); err != nil {
		t.Fatalf("placement = %v, want accepted", err)
	}
}

// TestValidateAudioNodePlacementNoEvidenceStillFiresForProgramOnly proves
// the more basic refusal survives: "this node has advertised nothing at
// all" is distinct from "advertised something, but not this route", and
// making the LTC pair optional must not let a program-only payload be
// placed against a node with no probe evidence whatsoever.
func TestValidateAudioNodePlacementNoEvidenceStillFiresForProgramOnly(t *testing.T) {
	p := AudioNodePayload{
		ProgramRoute: "hw:CARD=USB,DEV=0", ProgramChannels: []int{1, 2},
		ClockDomain: "solo", ClockDomainProvenance: "single interface",
	}
	err := ValidateAudioNodePlacement(p, nil, nil)
	if !errors.Is(err, ErrAudioNodeNoEvidence) {
		t.Fatalf("placement = %v, want ErrAudioNodeNoEvidence", err)
	}
}

// TestValidateAudioNodePlacementRejectsProgramOnlyUnevidencedRoute
// proves the program half is still checked against probe evidence when
// no LTC is declared: dropping LTC must not drop every check with it.
func TestValidateAudioNodePlacementRejectsProgramOnlyUnevidencedRoute(t *testing.T) {
	p := AudioNodePayload{
		ProgramRoute: "hw:CARD=TYPO,DEV=0", ProgramChannels: []int{1, 2},
		ClockDomain: "solo", ClockDomainProvenance: "single interface",
	}
	if err := ValidateAudioNodePlacement(p, []string{"hw:CARD=USB,DEV=0"}, nil); err == nil {
		t.Fatal("placement accepted an unevidenced program route on a program-only node")
	}
}

// TestValidateAudioNodePlacementStillRejectsUnevidencedLTCRoute proves
// making the pair optional did not weaken the LTC case: a declaration
// that DOES name an LTC route is refused exactly as before when the node
// never advertised that route as LTC-capable.
func TestValidateAudioNodePlacementStillRejectsUnevidencedLTCRoute(t *testing.T) {
	p := AudioNodePayload{
		ProgramRoute: "hw:CARD=USB,DEV=0", LTCRoute: "hw:CARD=USB,DEV=0",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain: "solo", ClockDomainProvenance: "single interface",
	}
	if err := ValidateAudioNodePlacement(p, []string{"hw:CARD=USB,DEV=0"}, nil); err == nil {
		t.Fatal("placement accepted an LTC route the node never advertised as LTC-capable")
	}
}

// TestDecodeAudioNodePayloadRejectsAbsentField proves every required
// field is required on every write — PUT is a full replacement, matching
// every other collection kind in this package. ltcRoute and ltcChannel
// are covered separately by
// TestDecodeAudioNodePayloadRejectsHalfDeclaredLTC, being the one pair
// that may be omitted together.
func TestDecodeAudioNodePayloadRejectsAbsentField(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"programRoute", `{"ltcRoute":"a","programChannels":[1],"ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`},
		{"programChannels", `{"programRoute":"a","ltcRoute":"a","ltcChannel":2,"clockDomain":"d","clockDomainProvenance":"p"}`},
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
		// Both LTC keys present but null. The pair is "present" as far as
		// the optional-together check is concerned, so this must reach
		// the null refusal rather than being read as a program-only
		// declaration: an operator who typed null meant to say something,
		// and silently treating it as "no LTC" would hide the mistake.
		{"ltcRoute", `{"programRoute":"a","ltcRoute":null,"programChannels":[1],"ltcChannel":null,"clockDomain":"d","clockDomainProvenance":"p"}`},
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

// TestRouteMismatchDescriptionMatchesTheRuleEnforced pins api/openapi.yaml's
// own description of "audio-node-route-mismatch" against the rule this file
// actually enforces. The published text said the opposite for a while (that
// the two routes naming the SAME device is what gets refused), so a client
// author reading the contract would have built exactly inverted validation.
func TestRouteMismatchDescriptionMatchesTheRuleEnforced(t *testing.T) {
	// The rule, restated from the code below it rather than from prose.
	same := `{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","programChannels":[1,2],"ltcChannel":3,"clockDomain":"d","clockDomainProvenance":"p"}`
	if _, verr := DecodeAudioNodePayload(same); verr != nil {
		t.Fatalf("two routes naming the SAME device must be accepted, got %v", verr)
	}
	differing := `{"programRoute":"hw:0,0","ltcRoute":"hw:1,0","programChannels":[1,2],"ltcChannel":3,"clockDomain":"d","clockDomainProvenance":"p"}`
	if _, verr := DecodeAudioNodePayload(differing); verr == nil || verr.Code != ValidationCodeAudioNodeRouteMismatch {
		t.Fatalf("two routes naming DIFFERENT devices must be refused, got %v", verr)
	}

	spec, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	const described = `"audio-node-route-mismatch" (a non-empty ltcRoute naming a`
	if !strings.Contains(string(spec), described) {
		t.Fatal("api/openapi.yaml no longer describes audio-node-route-mismatch as refusing a DIFFERING ltcRoute; the published contract and this validation have drifted apart again")
	}
	if strings.Contains(string(spec), `"audio-node-route-mismatch" (programRoute and ltcRoute naming the`) {
		t.Fatal("api/openapi.yaml has returned to describing audio-node-route-mismatch backwards")
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

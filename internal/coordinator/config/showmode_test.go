package config

import "testing"

func TestDecodeShowModePayloadAcceptsBothMembers(t *testing.T) {
	for _, mode := range []string{ShowModeProgram, ShowModeShow} {
		got, verr := DecodeShowModePayload(`{"mode":"` + mode + `"}`)
		if verr != nil {
			t.Fatalf("DecodeShowModePayload(%q) returned %v", mode, verr)
		}
		if got.Mode != mode {
			t.Fatalf("DecodeShowModePayload(%q) = %q", mode, got.Mode)
		}
	}
}

// The enum is CLOSED (ADR-033 decision 1): a member outside it needs an
// amendment to that record, not a payload that happens to parse. "unknown"
// is called out by name because it is a real value elsewhere in this
// system (the node-side held-value state) and must never be writable here.
func TestDecodeShowModePayloadRejectsNonMembers(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"unknown"}`,
		`{"mode":"setup"}`,
		`{"mode":"SHOW"}`,
		`{"mode":""}`,
		`{"mode":1}`,
	} {
		if _, verr := DecodeShowModePayload(raw); verr == nil {
			t.Fatalf("DecodeShowModePayload(%s) accepted a non-member", raw)
		}
	}
}

// A full-replacement PUT refuses an absent required key by name rather
// than treating it as "leave it as it was" or "use the default".
func TestDecodeShowModePayloadRefusesAbsentMode(t *testing.T) {
	_, verr := DecodeShowModePayload(`{}`)
	if verr == nil {
		t.Fatal("DecodeShowModePayload({}) accepted a body with no mode key")
	}
	if verr.Field != "mode" {
		t.Fatalf("validation error names field %q, want mode", verr.Field)
	}
}

func TestDecodeShowModePayloadRejectsUnknownTopLevelKeys(t *testing.T) {
	_, verr := DecodeShowModePayload(`{"mode":"show","nodeId":"dev-node-01"}`)
	if verr == nil {
		t.Fatal("DecodeShowModePayload accepted an unknown top-level key")
	}
}

func TestDecodeShowModePayloadRejectsNonObjects(t *testing.T) {
	for _, raw := range []string{``, `[]`, `"show"`, `null`, `{`} {
		if _, verr := DecodeShowModePayload(raw); verr == nil {
			t.Fatalf("DecodeShowModePayload(%q) accepted a non-object body", raw)
		}
	}
}

// The fresh-install default is program (owner ruling), and it is a
// DIFFERENT thing from the node-side "unknown behaves as show" rule. A
// test pins it because collapsing the two is the specific mistake this
// build is asked not to make.
func TestShowModeDefaultIsProgram(t *testing.T) {
	if ShowModeDefault != ShowModeProgram {
		t.Fatalf("ShowModeDefault = %q, want program", ShowModeDefault)
	}
	if ShowModeDefaultPayload.Mode != ShowModeProgram {
		t.Fatalf("ShowModeDefaultPayload.Mode = %q, want program", ShowModeDefaultPayload.Mode)
	}
}

func TestValidShowMode(t *testing.T) {
	for _, mode := range []string{ShowModeProgram, ShowModeShow} {
		if !ValidShowMode(mode) {
			t.Fatalf("ValidShowMode(%q) = false", mode)
		}
	}
	for _, mode := range []string{"unknown", "", "Program", "show "} {
		if ValidShowMode(mode) {
			t.Fatalf("ValidShowMode(%q) = true", mode)
		}
	}
}

func TestEncodeShowModePayloadRoundTrips(t *testing.T) {
	raw, err := EncodeShowModePayload(ShowModePayload{Mode: ShowModeShow})
	if err != nil {
		t.Fatalf("EncodeShowModePayload: %v", err)
	}
	back, verr := DecodeShowModePayload(raw)
	if verr != nil {
		t.Fatalf("DecodeShowModePayload(%s): %v", raw, verr)
	}
	if back.Mode != ShowModeShow {
		t.Fatalf("round trip produced %q", back.Mode)
	}
}

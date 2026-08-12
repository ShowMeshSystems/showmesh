package config

import "testing"

func TestEncodeFPPEndpointsPayloadRoundTrips(t *testing.T) {
	in := []FPPEndpoint{{ID: "player-01", URL: "http://10.0.1.20"}, {ID: "shed", URL: "http://10.0.1.21"}}

	raw, err := EncodeFPPEndpointsPayload(in)
	if err != nil {
		t.Fatalf("EncodeFPPEndpointsPayload() error = %v", err)
	}

	got, err := DecodeFPPEndpointsPayload(raw)
	if err != nil {
		t.Fatalf("DecodeFPPEndpointsPayload() error = %v", err)
	}
	if !FPPEndpointsEqual(got, in) {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
}

// TestEncodeFPPEndpointsPayloadNilBecomesEmptyArray proves a nil input
// slice never encodes as JSON `null` — the exact "null is not an absent
// key" defect class CLAUDE.md's Step 5 lesson names. Before trusting this
// test, EncodeFPPEndpointsPayload's nil guard was removed and the raw
// output confirmed to contain "endpoints":null instead of
// "endpoints":[].
func TestEncodeFPPEndpointsPayloadNilBecomesEmptyArray(t *testing.T) {
	raw, err := EncodeFPPEndpointsPayload(nil)
	if err != nil {
		t.Fatalf("EncodeFPPEndpointsPayload(nil) error = %v", err)
	}
	if raw != `{"endpoints":[]}` {
		t.Errorf("EncodeFPPEndpointsPayload(nil) = %q, want {\"endpoints\":[]}", raw)
	}

	got, err := DecodeFPPEndpointsPayload(raw)
	if err != nil {
		t.Fatalf("DecodeFPPEndpointsPayload() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("decoded = %+v, want empty, non-nil", got)
	}
}

func TestDecodeFPPEndpointsPayloadRejectsMalformedJSON(t *testing.T) {
	if _, err := DecodeFPPEndpointsPayload("not json"); err == nil {
		t.Fatalf("DecodeFPPEndpointsPayload(%q) error = nil, want an error", "not json")
	}
}

// TestFPPEndpointsEqualIsOrderInsensitive is the exact property BUILD-PLAN
// Step 7 seam A's spec names for the env->store disagreement rule:
// "comparison is over the set of id and url pairs, order-insensitive."
// Before trusting this test, FPPEndpointsEqual's sort step was removed
// (replaced with a positional comparison) and this exact input confirmed
// to report false when it must report true.
func TestFPPEndpointsEqualIsOrderInsensitive(t *testing.T) {
	a := []FPPEndpoint{{ID: "shed", URL: "http://10.0.1.21"}, {ID: "player-01", URL: "http://10.0.1.20"}}
	b := []FPPEndpoint{{ID: "player-01", URL: "http://10.0.1.20"}, {ID: "shed", URL: "http://10.0.1.21"}}

	if !FPPEndpointsEqual(a, b) {
		t.Errorf("FPPEndpointsEqual(%+v, %+v) = false, want true (same set, different order)", a, b)
	}
}

func TestFPPEndpointsEqualDetectsADifferentURL(t *testing.T) {
	a := []FPPEndpoint{{ID: "shed", URL: "http://10.0.1.21"}}
	b := []FPPEndpoint{{ID: "shed", URL: "http://10.0.1.99"}}

	if FPPEndpointsEqual(a, b) {
		t.Errorf("FPPEndpointsEqual(%+v, %+v) = true, want false (different url for the same id)", a, b)
	}
}

func TestFPPEndpointsEqualDetectsADifferentLength(t *testing.T) {
	a := []FPPEndpoint{{ID: "shed", URL: "http://10.0.1.21"}}
	b := []FPPEndpoint{{ID: "shed", URL: "http://10.0.1.21"}, {ID: "player-01", URL: "http://10.0.1.20"}}

	if FPPEndpointsEqual(a, b) {
		t.Errorf("FPPEndpointsEqual(%+v, %+v) = true, want false (different length)", a, b)
	}
}

func TestValidateFPPEndpointsExportedMatchesInternalValidation(t *testing.T) {
	valid := []FPPEndpoint{{ID: "player-01", URL: "http://10.0.1.20"}}
	if err := ValidateFPPEndpoints(valid); err != nil {
		t.Errorf("ValidateFPPEndpoints(%+v) error = %v, want nil", valid, err)
	}

	invalid := []FPPEndpoint{{ID: "player-01", URL: "not a url"}}
	if err := ValidateFPPEndpoints(invalid); err == nil {
		t.Errorf("ValidateFPPEndpoints(%+v) error = nil, want an error for a malformed URL", invalid)
	}
}

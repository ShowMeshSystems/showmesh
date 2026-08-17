package config

import "testing"

func TestEncodeResolumeInstancesPayloadRoundTrips(t *testing.T) {
	in := []ResolumeInstance{{ID: "arena-1", URL: "http://10.0.1.30:8080"}}

	raw, err := EncodeResolumeInstancesPayload(in)
	if err != nil {
		t.Fatalf("EncodeResolumeInstancesPayload() error = %v", err)
	}

	got, err := DecodeResolumeInstancesPayload(raw)
	if err != nil {
		t.Fatalf("DecodeResolumeInstancesPayload() error = %v", err)
	}
	if !ResolumeInstancesEqual(got, in) {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
}

// TestEncodeResolumeInstancesPayloadNilBecomesEmptyArray mirrors
// TestEncodeFPPEndpointsPayloadNilBecomesEmptyArray's identical "null is
// not an absent key" proof, one layer up from a decoded struct field —
// this project has shipped the opposite twice already.
func TestEncodeResolumeInstancesPayloadNilBecomesEmptyArray(t *testing.T) {
	raw, err := EncodeResolumeInstancesPayload(nil)
	if err != nil {
		t.Fatalf("EncodeResolumeInstancesPayload(nil) error = %v", err)
	}
	if raw != `{"instances":[]}` {
		t.Errorf("EncodeResolumeInstancesPayload(nil) = %q, want {\"instances\":[]}", raw)
	}

	got, err := DecodeResolumeInstancesPayload(raw)
	if err != nil {
		t.Fatalf("DecodeResolumeInstancesPayload() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("decoded = %+v, want empty, non-nil", got)
	}
}

func TestDecodeResolumeInstancesPayloadRejectsMalformedJSON(t *testing.T) {
	if _, err := DecodeResolumeInstancesPayload("not json"); err == nil {
		t.Fatalf("DecodeResolumeInstancesPayload(%q) error = nil, want an error", "not json")
	}
}

// TestResolumeInstancesEqualIsOrderInsensitive mirrors
// TestFPPEndpointsEqualIsOrderInsensitive's identical property, for the
// identical env->store disagreement rule (resolumeinstancessync.go).
func TestResolumeInstancesEqualIsOrderInsensitive(t *testing.T) {
	a := []ResolumeInstance{{ID: "arena-1", URL: "http://10.0.1.30:8080"}, {ID: "arena-2", URL: "http://10.0.1.31:8080"}}
	b := []ResolumeInstance{{ID: "arena-2", URL: "http://10.0.1.31:8080"}, {ID: "arena-1", URL: "http://10.0.1.30:8080"}}

	if !ResolumeInstancesEqual(a, b) {
		t.Errorf("ResolumeInstancesEqual(%+v, %+v) = false, want true (same set, different order)", a, b)
	}
}

func TestResolumeInstancesEqualDetectsADifferentURL(t *testing.T) {
	a := []ResolumeInstance{{ID: "arena-1", URL: "http://10.0.1.30:8080"}}
	b := []ResolumeInstance{{ID: "arena-1", URL: "http://10.0.1.99:8080"}}

	if ResolumeInstancesEqual(a, b) {
		t.Errorf("ResolumeInstancesEqual(%+v, %+v) = true, want false (different url for the same id)", a, b)
	}
}

func TestValidateResolumeInstancesAcceptsOneValidInstance(t *testing.T) {
	valid := []ResolumeInstance{{ID: "arena-1", URL: "http://10.0.1.30:8080"}}
	if err := ValidateResolumeInstances(valid, nil); err != nil {
		t.Errorf("ValidateResolumeInstances(%+v, nil) error = %v, want nil", valid, err)
	}
}

func TestValidateResolumeInstancesAcceptsZeroInstances(t *testing.T) {
	if err := ValidateResolumeInstances(nil, nil); err != nil {
		t.Errorf("ValidateResolumeInstances(nil, nil) error = %v, want nil", err)
	}
}

// TestValidateResolumeInstancesRejectsMoreThanOne is Track G seam G-2's own
// spec, verbatim: "Validation rejects more than one instance with a reason
// naming the limit ... the scope limit lives in validation, never in the
// schema."
func TestValidateResolumeInstancesRejectsMoreThanOne(t *testing.T) {
	two := []ResolumeInstance{
		{ID: "arena-1", URL: "http://10.0.1.30:8080"},
		{ID: "arena-2", URL: "http://10.0.1.31:8080"},
	}
	err := ValidateResolumeInstances(two, nil)
	if err == nil {
		t.Fatalf("ValidateResolumeInstances(%+v, nil) error = nil, want a refusal naming the limit", two)
	}
	if !containsAll(err.Error(), "1", "instance") {
		t.Errorf("error = %q, want it to name the limit (1 instance)", err.Error())
	}
}

func TestValidateResolumeInstancesRejectsMalformedURL(t *testing.T) {
	invalid := []ResolumeInstance{{ID: "arena-1", URL: "not a url"}}
	if err := ValidateResolumeInstances(invalid, nil); err == nil {
		t.Errorf("ValidateResolumeInstances(%+v, nil) error = nil, want an error for a malformed URL", invalid)
	}
}

func TestValidateResolumeInstancesRejectsInvalidID(t *testing.T) {
	invalid := []ResolumeInstance{{ID: "not an id!", URL: "http://10.0.1.30:8080"}}
	if err := ValidateResolumeInstances(invalid, nil); err == nil {
		t.Errorf("ValidateResolumeInstances(%+v, nil) error = nil, want an error for an invalid id", invalid)
	}
}

// TestValidateResolumeInstancesRejectsFPPEndpointCollision is the seam's
// own load-bearing check: both collectors share one collector.Runner keyed
// by id, so a duplicate id makes an out-of-band poll nudge retarget the
// wrong device. Proves ValidateResolumeInstances actually calls
// ValidateResolumeIDAgainstFPPEndpoints rather than merely validating shape.
func TestValidateResolumeInstancesRejectsFPPEndpointCollision(t *testing.T) {
	fppEndpoints := []FPPEndpoint{{ID: "arena-1", URL: "http://10.0.1.20"}}
	colliding := []ResolumeInstance{{ID: "arena-1", URL: "http://10.0.1.30:8080"}}

	err := ValidateResolumeInstances(colliding, fppEndpoints)
	if err == nil {
		t.Fatalf("ValidateResolumeInstances(%+v, %+v) error = nil, want a collision refusal", colliding, fppEndpoints)
	}
	if !containsAll(err.Error(), "arena-1") {
		t.Errorf("error = %q, want it to name the colliding id", err.Error())
	}
}

func TestValidateResolumeInstancesRejectsDuplicateIDs(t *testing.T) {
	// Unreachable at maxResolumeInstances == 1 through the length check
	// alone if it ran first — this proves the per-entry duplicate check
	// exists independently, matching validateFPPEndpoints' identical
	// belt-and-suspenders shape, in case the limit is ever raised.
	dup := []ResolumeInstance{
		{ID: "arena-1", URL: "http://10.0.1.30:8080"},
		{ID: "arena-1", URL: "http://10.0.1.31:8080"},
	}
	if err := ValidateResolumeInstances(dup, nil); err == nil {
		t.Errorf("ValidateResolumeInstances(%+v, nil) error = nil, want an error for a duplicate id", dup)
	}
}

package store

import "testing"

// TestParseCallerIntentRoundTripsEveryKind proves [FormatCallerIntent] and
// [ParseCallerIntent] round trip for every tagged kind this package knows
// about.
func TestParseCallerIntentRoundTripsEveryKind(t *testing.T) {
	for _, kind := range callerIntentTags {
		formatted := FormatCallerIntent(kind, "payload-for-"+string(kind))
		gotKind, gotPayload, ok := ParseCallerIntent(formatted)
		if !ok {
			t.Fatalf("ParseCallerIntent(%q) ok = false, want true", formatted)
		}
		if gotKind != kind || gotPayload != "payload-for-"+string(kind) {
			t.Errorf("ParseCallerIntent(%q) = (%q, %q), want (%q, %q)", formatted, gotKind, gotPayload, kind, "payload-for-"+string(kind))
		}
	}
}

// TestParseCallerIntentNeverGuessesAnUntaggedValue proves the rule this
// file's own doc comment states: "" and any value that does not carry one
// of this package's own recognized tags must come back with ok == false,
// never a kind guessed from the value's own shape (a bare decimal digit
// string that LOOKS like [CallerIntentRevision]'s payload, or a bare JSON
// object that LOOKS like one of the identity kinds' payloads).
func TestParseCallerIntentNeverGuessesAnUntaggedValue(t *testing.T) {
	for _, s := range []string{
		"",
		"3",
		`{"node":"render-01"}`,
		"not-a-recognized-tag:payload",
		"revision", // the bare tag word, with no separator or payload
	} {
		kind, payload, ok := ParseCallerIntent(s)
		if ok {
			t.Errorf("ParseCallerIntent(%q) ok = true, want false (untagged)", s)
		}
		if kind != CallerIntentUntagged {
			t.Errorf("ParseCallerIntent(%q) kind = %q, want CallerIntentUntagged", s, kind)
		}
		if payload != s && s != "" {
			t.Errorf("ParseCallerIntent(%q) payload = %q, want the input unchanged", s, payload)
		}
	}
}

// TestCallerIntentPayloadRejectsAnotherKindsTag proves the cross-family
// guard [CallerIntentPayload] exists for: a call site that already knows
// (from TargetKind/Action) which single kind a row can belong to must
// never be handed another kind's payload just because that value happens
// to carry SOME recognized tag. This is what keeps
// [CallerIntentRenderRequest] and [CallerIntentCueCatalogDeploy], the two
// JSON-shaped kinds, from being cross-read as each other.
func TestCallerIntentPayloadRejectsAnotherKindsTag(t *testing.T) {
	renderValue := FormatCallerIntent(CallerIntentRenderRequest, `{"action":"apply","node":"render-01"}`)

	payload, tagged := CallerIntentPayload(CallerIntentCueCatalogDeploy, renderValue)
	if tagged {
		t.Fatalf("CallerIntentPayload(CallerIntentCueCatalogDeploy, %q) tagged = true, want false (this value is tagged CallerIntentRenderRequest, not CueCatalogDeploy)", renderValue)
	}
	if payload != renderValue {
		t.Errorf("CallerIntentPayload(CallerIntentCueCatalogDeploy, %q) payload = %q, want the value unchanged when the kind does not match", renderValue, payload)
	}

	// The matching kind still extracts cleanly.
	payload, tagged = CallerIntentPayload(CallerIntentRenderRequest, renderValue)
	if !tagged {
		t.Fatalf("CallerIntentPayload(CallerIntentRenderRequest, %q) tagged = false, want true", renderValue)
	}
	if payload != `{"action":"apply","node":"render-01"}` {
		t.Errorf("CallerIntentPayload(CallerIntentRenderRequest, %q) payload = %q, want the untagged JSON", renderValue, payload)
	}
}

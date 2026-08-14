package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// This file is Step 8 section 2's own regression suite: CLAUDE.md records
// that this project has shipped the absent-vs-null-vs-empty bug FOUR
// times already, twice in Step 7 alone. Every branch decodeFPPCommandParams
// documents is exercised here directly (white-box, no HTTP server) because
// these branches are cheap to prove wrong and this is exactly the bug this
// project keeps shipping.

func decodeTop(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		t.Fatalf("decode top-level body %q: %v", body, err)
	}
	return top
}

func TestDecodeFPPCommandParamsAbsentUsesDefaults(t *testing.T) {
	// stopPlaylistGracefully has exactly one param, and it is OPTIONAL —
	// the primitive to prove "params entirely absent" falls back to every
	// default rather than refusing, with no required field to mask that.
	top := decodeTop(t, `{"action":"stopPlaylistGracefully","idempotencyKey":"k"}`)
	got, problem := decodeFPPCommandParams(primitiveStopPlaylistGracefully, top)
	if problem != nil {
		t.Fatalf("problem = %+v, want nil (params absent must fall back to defaults, never refuse)", *problem)
	}
	if got["afterLoop"] != false {
		t.Errorf("afterLoop = %v, want false (default)", got["afterLoop"])
	}
}

// TestDecodeFPPCommandParamsAbsentStillEnforcesRequiredFields proves the
// OTHER half: a primitive with a REQUIRED param (startPlaylist's
// "playlist") still refuses when params is absent entirely — "absent
// falls back to defaults" must never be read as "absent means every
// field, including required ones, is somehow satisfied."
func TestDecodeFPPCommandParamsAbsentStillEnforcesRequiredFields(t *testing.T) {
	top := decodeTop(t, `{"action":"startPlaylist","idempotencyKey":"k"}`)
	_, problem := decodeFPPCommandParams(primitiveStartPlaylist, top)
	if problem == nil {
		t.Fatal("problem = nil, want a 400 — startPlaylist's playlist param is required and params was entirely absent")
	}
	if !strings.Contains(problem.Detail, "params.playlist is required") {
		t.Errorf("detail = %q, want it to name playlist as required", problem.Detail)
	}
}

func TestDecodeFPPCommandParamsEmptyObjectUsesDefaults(t *testing.T) {
	top := decodeTop(t, `{"action":"stopPlaylistGracefully","idempotencyKey":"k","params":{}}`)
	got, problem := decodeFPPCommandParams(primitiveStopPlaylistGracefully, top)
	if problem != nil {
		t.Fatalf("problem = %+v, want nil", *problem)
	}
	if got["afterLoop"] != false {
		t.Errorf("afterLoop = %v, want false (default)", got["afterLoop"])
	}
}

func TestDecodeFPPCommandParamsExplicitNullIsAlways400(t *testing.T) {
	for _, primitive := range fppCommandPrimitives {
		t.Run(primitive.WireAction, func(t *testing.T) {
			top := decodeTop(t, `{"action":"`+primitive.WireAction+`","idempotencyKey":"k","params":null}`)
			_, problem := decodeFPPCommandParams(primitive, top)
			if problem == nil {
				t.Fatalf("problem = nil, want a 400 — explicit params:null must never be treated as absent")
			}
			if problem.Status != 400 {
				t.Errorf("status = %d, want 400", problem.Status)
			}
			if !strings.Contains(problem.Detail, "not the same as an omitted field") {
				t.Errorf("detail = %q, want it to say null is not the same as omitted", problem.Detail)
			}
		})
	}
}

func TestDecodeFPPCommandParamsRequiredFieldAbsentIs400(t *testing.T) {
	top := decodeTop(t, `{"action":"setVolume","idempotencyKey":"k","params":{}}`)
	_, problem := decodeFPPCommandParams(primitiveSetVolume, top)
	if problem == nil {
		t.Fatal("problem = nil, want a 400 (volume is required)")
	}
	if !strings.Contains(problem.Detail, "params.volume is required and was not provided") {
		t.Errorf("detail = %q, want it to name volume as required and absent", problem.Detail)
	}
}

func TestDecodeFPPCommandParamsRequiredFieldNullIsADifferent400(t *testing.T) {
	top := decodeTop(t, `{"action":"setVolume","idempotencyKey":"k","params":{"volume":null}}`)
	_, problem := decodeFPPCommandParams(primitiveSetVolume, top)
	if problem == nil {
		t.Fatal("problem = nil, want a 400 (volume must not be null)")
	}
	if !strings.Contains(problem.Detail, "params.volume is required and must not be null") {
		t.Errorf("detail = %q, want a message distinct from the absent case", problem.Detail)
	}
	// And the message text must actually DIFFER from the absent case —
	// the whole point of this step's own instruction.
	topAbsent := decodeTop(t, `{"action":"setVolume","idempotencyKey":"k","params":{}}`)
	_, absentProblem := decodeFPPCommandParams(primitiveSetVolume, topAbsent)
	if absentProblem.Detail == problem.Detail {
		t.Error("absent and null produced the IDENTICAL detail text, want two different messages")
	}
}

func TestDecodeFPPCommandParamsRequiredStringEmptyIsAThirdDifferent400(t *testing.T) {
	top := decodeTop(t, `{"action":"startPlaylist","idempotencyKey":"k","params":{"playlist":""}}`)
	_, problem := decodeFPPCommandParams(primitiveStartPlaylist, top)
	if problem == nil {
		t.Fatal("problem = nil, want a 400 (empty playlist name)")
	}
	if !strings.Contains(problem.Detail, "must not be an empty string") {
		t.Errorf("detail = %q, want it to name the empty-string rule", problem.Detail)
	}

	topAbsent := decodeTop(t, `{"action":"startPlaylist","idempotencyKey":"k","params":{}}`)
	_, absentProblem := decodeFPPCommandParams(primitiveStartPlaylist, topAbsent)
	topNull := decodeTop(t, `{"action":"startPlaylist","idempotencyKey":"k","params":{"playlist":null}}`)
	_, nullProblem := decodeFPPCommandParams(primitiveStartPlaylist, topNull)

	if problem.Detail == absentProblem.Detail || problem.Detail == nullProblem.Detail || absentProblem.Detail == nullProblem.Detail {
		t.Errorf("expected THREE distinct messages for absent/null/empty, got:\nabsent=%q\nnull=%q\nempty=%q",
			absentProblem.Detail, nullProblem.Detail, problem.Detail)
	}
}

func TestDecodeFPPCommandParamsOptionalFieldAbsentUsesDefault(t *testing.T) {
	top := decodeTop(t, `{"action":"startPlaylist","idempotencyKey":"k","params":{"playlist":"x"}}`)
	got, problem := decodeFPPCommandParams(primitiveStartPlaylist, top)
	if problem != nil {
		t.Fatalf("problem = %+v, want nil", *problem)
	}
	if got["repeat"] != false {
		t.Errorf("repeat = %v, want false (default)", got["repeat"])
	}
}

func TestDecodeFPPCommandParamsOptionalFieldNullIs400(t *testing.T) {
	top := decodeTop(t, `{"action":"startPlaylist","idempotencyKey":"k","params":{"playlist":"x","repeat":null}}`)
	_, problem := decodeFPPCommandParams(primitiveStartPlaylist, top)
	if problem == nil {
		t.Fatal("problem = nil, want a 400 — an optional param present as null must never silently become its default")
	}
	if !strings.Contains(problem.Detail, "must not be null") {
		t.Errorf("detail = %q, want it to say null is refused", problem.Detail)
	}
}

func TestDecodeFPPCommandParamsUnknownKeyIs400(t *testing.T) {
	top := decodeTop(t, `{"action":"startPlaylist","idempotencyKey":"k","params":{"playlist":"x","repeaat":true}}`)
	_, problem := decodeFPPCommandParams(primitiveStartPlaylist, top)
	if problem == nil {
		t.Fatal("problem = nil, want a 400 — a typo'd key must never silently apply its own default")
	}
	if !strings.Contains(problem.Detail, "repeaat") {
		t.Errorf("detail = %q, want it to name the unknown key \"repeaat\"", problem.Detail)
	}
}

func TestDecodeFPPCommandParamsNonEmptyParamsForZeroParamActionIs400(t *testing.T) {
	top := decodeTop(t, `{"action":"stopPlaylist","idempotencyKey":"k","params":{"foo":1}}`)
	_, problem := decodeFPPCommandParams(primitiveStopPlaylist, top)
	if problem == nil {
		t.Fatal("problem = nil, want a 400 — stopPlaylist takes no parameters")
	}
	if !strings.Contains(problem.Detail, "takes no parameters") {
		t.Errorf("detail = %q, want it to say this action takes no parameters", problem.Detail)
	}
}

func TestDecodeFPPCommandParamsZeroParamActionAbsentParamsSucceeds(t *testing.T) {
	top := decodeTop(t, `{"action":"stopPlaylist","idempotencyKey":"k"}`)
	got, problem := decodeFPPCommandParams(primitiveStopPlaylist, top)
	if problem != nil {
		t.Fatalf("problem = %+v, want nil", *problem)
	}
	if len(got) != 0 {
		t.Errorf("got %d params, want 0", len(got))
	}
}

func TestDecodeFPPCommandParamsSetVolumeFractionIs400(t *testing.T) {
	top := decodeTop(t, `{"action":"setVolume","idempotencyKey":"k","params":{"volume":55.5}}`)
	_, problem := decodeFPPCommandParams(primitiveSetVolume, top)
	if problem == nil {
		t.Fatal("problem = nil, want a 400 — a fractional volume must never be silently truncated")
	}
	if !strings.Contains(problem.Detail, "fractional part") {
		t.Errorf("detail = %q, want it to name the fractional-part rule", problem.Detail)
	}
}

func TestDecodeFPPCommandParamsSetVolumeIntegerSucceeds(t *testing.T) {
	top := decodeTop(t, `{"action":"setVolume","idempotencyKey":"k","params":{"volume":55}}`)
	got, problem := decodeFPPCommandParams(primitiveSetVolume, top)
	if problem != nil {
		t.Fatalf("problem = %+v, want nil", *problem)
	}
	if got["volume"] != int64(55) {
		t.Errorf("volume = %v (%T), want int64(55)", got["volume"], got["volume"])
	}
}

func TestDecodeFPPCommandParamsWrongJSONTypeIs400(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"string for bool", `{"action":"stopPlaylistGracefully","idempotencyKey":"k","params":{"afterLoop":"true"}}`},
		{"bool for string", `{"action":"startPlaylist","idempotencyKey":"k","params":{"playlist":true}}`},
		{"string for int", `{"action":"setVolume","idempotencyKey":"k","params":{"volume":"55"}}`},
	}
	prims := map[string]fppPrimitive{
		"stopPlaylistGracefully": primitiveStopPlaylistGracefully,
		"startPlaylist":          primitiveStartPlaylist,
		"setVolume":              primitiveSetVolume,
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			top := decodeTop(t, tc.body)
			var action string
			_ = json.Unmarshal(top["action"], &action)
			_, problem := decodeFPPCommandParams(prims[action], top)
			if problem == nil {
				t.Fatalf("problem = nil, want a 400 for a wrong JSON type")
			}
		})
	}
}

// TestCanonicalParamsJSONIsOrderAndDefaultInvariant proves the property
// section 5's idempotency rule depends on: a client that omits a
// defaulted field and one that sends the default explicitly must produce
// the IDENTICAL canonical JSON, so the store-level byte comparison this
// endpoint's replay-conflict check uses is correct.
func TestCanonicalParamsJSONIsOrderAndDefaultInvariant(t *testing.T) {
	topOmitted := decodeTop(t, `{"action":"startPlaylist","idempotencyKey":"k","params":{"playlist":"x"}}`)
	omitted, problem := decodeFPPCommandParams(primitiveStartPlaylist, topOmitted)
	if problem != nil {
		t.Fatalf("problem = %+v, want nil", *problem)
	}
	topExplicit := decodeTop(t, `{"action":"startPlaylist","idempotencyKey":"k","params":{"playlist":"x","repeat":false,"ifBusy":"refuse"}}`)
	explicit, problem := decodeFPPCommandParams(primitiveStartPlaylist, topExplicit)
	if problem != nil {
		t.Fatalf("problem = %+v, want nil", *problem)
	}

	omittedJSON, err := canonicalParamsJSON(omitted)
	if err != nil {
		t.Fatalf("canonicalParamsJSON(omitted): %v", err)
	}
	explicitJSON, err := canonicalParamsJSON(explicit)
	if err != nil {
		t.Fatalf("canonicalParamsJSON(explicit): %v", err)
	}
	if omittedJSON != explicitJSON {
		t.Errorf("canonical JSON differs: omitted=%q explicit=%q, want identical", omittedJSON, explicitJSON)
	}
}

func TestCanonicalParamsJSONZeroParamActionIsEmptyObject(t *testing.T) {
	got, err := canonicalParamsJSON(map[string]any{})
	if err != nil {
		t.Fatalf("canonicalParamsJSON: %v", err)
	}
	if got != "{}" {
		t.Errorf("got %q, want \"{}\"", got)
	}
	// nil map must behave identically — never a JSON "null".
	got2, err := canonicalParamsJSON(nil)
	if err != nil {
		t.Fatalf("canonicalParamsJSON(nil): %v", err)
	}
	if got2 != "{}" {
		t.Errorf("got %q for nil map, want \"{}\" (never JSON null)", got2)
	}
}

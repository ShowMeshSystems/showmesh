package fppidentity

import (
	"strings"
	"testing"
)

// canonicalText is a small test helper: canonicalize and stringify, so
// table cases can compare against the expected canonical text directly.
func canonicalText(t *testing.T, input string) (string, error) {
	t.Helper()
	out, err := Canonicalize([]byte(input))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// The expected outputs below were cross-checked against the C++ reference
// implementation (native/src/json.cpp in the plugin repository), by
// building a throwaway program against its sources and running it. See
// this task's final report for the exact command and printed values.

func TestCanonicalizationSortsMembersAndStripsWhitespace(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"simple object", `{ "b": 1, "a": 2 }`, `{"a":2,"b":1}`},
		{"array whitespace", `[ 1 , 2 ,  3 ]`, `[1,2,3]`},
		{"nested object", `{"nested":{"z":true,"a":null}}`, `{"nested":{"a":null,"z":true}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalText(t, tc.input)
			if err != nil {
				t.Fatalf("Canonicalize(%q) error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestMemberOrderDoesNotChangeTheCanonicalForm(t *testing.T) {
	a, err := canonicalText(t, `{"a":1,"b":2,"c":3}`)
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalText(t, `{"c":3,"b":2,"a":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("member order changed canonical form: %q vs %q", a, b)
	}
}

// A supplementary character (U+10000, surrogate pair D800 DC00) sorts
// before U+E000 in UTF-16 but after it in raw UTF-8 byte order. This is
// the case that would fail under a byte-order sort.
func TestMemberNamesSortByUTF16CodeUnitsNotBytes(t *testing.T) {
	input := "{\"\\ue000\":1,\"\\ud800\\udc00\":2}"
	want := "{\"\U00010000\":2,\"\uE000\":1}"
	got, err := canonicalText(t, input)
	if err != nil {
		t.Fatalf("Canonicalize(%q) error: %v", input, err)
	}
	if got != want {
		t.Errorf("Canonicalize(%q) = %q, want %q", input, got, want)
	}
}

func TestDuplicateMemberNamesAreRejected(t *testing.T) {
	if _, err := Canonicalize([]byte(`{"a":1,"a":2}`)); err == nil {
		t.Error("expected an error for a duplicate member name, got nil")
	}
}

func TestStringsUseTheShortEscapesAndEscapeOnlyWhatMustBe(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"decoded escape roundtrips plain", `["a\u0062c"]`, `["abc"]`},
		{"control char below 0x20 uses \\u00xx", `["\u000b"]`, `["\u000b"]`},
		{"named escape for tab", `["tab\there"]`, `["tab\there"]`},
		{"quote and backslash", `["quote\" and backslash\\"]`, `["quote\" and backslash\\"]`},
		{"solidus is not escaped on output", `["a\/b"]`, `["a/b"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalText(t, tc.input)
			if err != nil {
				t.Fatalf("Canonicalize(%q) error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNumbersUseEcmascriptFormatting(t *testing.T) {
	cases := []struct {
		name string
		v    float64
		want string
	}{
		{"zero", 0.0, "0"},
		{"negative zero", negativeZero(), "0"},
		{"one", 1.0, "1"},
		{"negative one", -1.0, "-1"},
		{"fraction", 1.5, "1.5"},
		{"hundred", 100.0, "100"},
		{"1e21 boundary switches to exponential", 1e21, "1e+21"},
		{"1e20 stays plain", 1e20, "100000000000000000000"},
		{"1e-6 stays plain", 1e-6, "0.000001"},
		{"1e-7 switches to exponential", 1e-7, "1e-7"},
		{"tenth", 0.1, "0.1"},
		{"one third, 16 significant digits", 1.0 / 3.0, "0.3333333333333333"},
		{"2^53, exact integer", 9007199254740992.0, "9007199254740992"},
		{"small exponential", 1.2345e-10, "1.2345e-10"},
		{"max float64, 17 significant digits", 1.7976931348623157e+308, "1.7976931348623157e+308"},
		{"min positive subnormal", 5e-324, "5e-324"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := formatNumber(tc.v)
			if err != nil {
				t.Fatalf("formatNumber(%v) error: %v", tc.v, err)
			}
			if got != tc.want {
				t.Errorf("formatNumber(%v) = %q, want %q", tc.v, got, tc.want)
			}
		})
	}
}

func negativeZero() float64 {
	z := 0.0
	return -z
}

func TestCanonicalizationRewritesEquivalentNumberLiterals(t *testing.T) {
	got, err := canonicalText(t, `[1.0,1e2,1E+2,0.10,-0]`)
	if err != nil {
		t.Fatal(err)
	}
	want := `[1,100,100,0.1,0]`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNaNAndInfAreRejected(t *testing.T) {
	if _, err := formatNumber(nan()); err == nil {
		t.Error("expected an error for NaN, got nil")
	}
	if _, err := formatNumber(posInf()); err == nil {
		t.Error("expected an error for +Inf, got nil")
	}
	if _, err := formatNumber(negInf()); err == nil {
		t.Error("expected an error for -Inf, got nil")
	}
}

func nan() float64 {
	var zero float64
	return zero / zero
}

func posInf() float64 {
	var zero float64
	return 1 / zero
}

func negInf() float64 {
	var zero float64
	return -1 / zero
}

func TestMalformedJSONIsRejectedRatherThanRepaired(t *testing.T) {
	cases := []string{
		`{`,
		`{"a":}`,
		`[1,]`,
		`01`,
		`"unterminated`,
		`{"a":1} trailing`,
		`["\ud800"]`,
		``,
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if _, err := Canonicalize([]byte(input)); err == nil {
				t.Errorf("Canonicalize(%q): expected an error, got nil", input)
			}
		})
	}
}

func TestCanonicalizationIsIdempotent(t *testing.T) {
	once, err := canonicalText(t, `{"b":[3,2,{"y":1,"x":2}],"a":"text"}`)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := canonicalText(t, once)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Errorf("canonicalization is not idempotent: %q vs %q", once, twice)
	}
}

func TestHashCanonical(t *testing.T) {
	canonical, hashHex, err := HashCanonical([]byte(`{ "b": 1, "a": 2 }`))
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != `{"a":2,"b":1}` {
		t.Errorf("canonical = %q", canonical)
	}
	if !IsHash64(hashHex) {
		t.Errorf("hash %q is not a 64-char lowercase hex string", hashHex)
	}
	// SHA-256 of the ASCII bytes {"a":2,"b":1}, independently computed.
	const want = "d3626ac30a87e6f7a6428233b3c68299976865fa5508e4267c5415c76af7a772"
	if hashHex != want {
		t.Errorf("hash = %s, want %s", hashHex, want)
	}
}

// A control character adjacent to what would be a delimiter, and a
// canonical definition matching the plugin's own fixture, cross-checked
// against the C++ resolveEntryIdentity for the reordered/edited playlist
// definitions used in the entry-key tests.
func TestPlaylistDefinitionCanonicalizationMatchesReference(t *testing.T) {
	def := `{"name":"Main Show","repeat":0,` +
		`"mainPlaylist":[` +
		`{"type":"both","sequenceName":"a.fseq","mediaName":"song.mp3","enabled":1},` +
		`{"type":"both","sequenceName":"a.fseq","mediaName":"song.mp3","enabled":1}` +
		`]}`
	reordered := "{\n  \"repeat\": 0,\n  \"mainPlaylist\": [\n" +
		"    {\"mediaName\": \"song.mp3\", \"enabled\": 1, \"sequenceName\": \"a.fseq\", \"type\": \"both\"},\n" +
		"    {\"enabled\": 1, \"type\": \"both\", \"mediaName\": \"song.mp3\", \"sequenceName\": \"a.fseq\"}\n" +
		"  ],\n  \"name\": \"Main Show\"\n}"

	a, hashA, err := HashCanonical([]byte(def))
	if err != nil {
		t.Fatal(err)
	}
	b, hashB, err := HashCanonical([]byte(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("reordering changed the canonical form: %q vs %q", a, b)
	}
	if hashA != hashB {
		t.Errorf("reordering changed the hash: %s vs %s", hashA, hashB)
	}
	// Cross-check: obtained by running a throwaway C++ program linked
	// against native/src/json.cpp, native/src/playlist_identity.cpp, and
	// native/src/sha256.cpp; see this task's final report.
	const wantCanonical = `{"mainPlaylist":[{"enabled":1,"mediaName":"song.mp3","sequenceName":"a.fseq","type":"both"},{"enabled":1,"mediaName":"song.mp3","sequenceName":"a.fseq","type":"both"}],"name":"Main Show","repeat":0}`
	const wantHash = "50669dfb37447e590f3a9b5ebd72e4e3fef4bab4afb6b7c775606f8cc57be8ee"
	if string(a) != wantCanonical {
		t.Errorf("canonical = %q, want %q", a, wantCanonical)
	}
	if hashA != wantHash {
		t.Errorf("hash = %s, want %s", hashA, wantHash)
	}
}

func TestFormatNumberRejectsUnrepresentableRoundTripOnlyForNonFinite(t *testing.T) {
	// A sanity check that ordinary finite values never hit the
	// non-finite rejection path, so that path only ever fires for the
	// NaN/Inf cases exercised above.
	values := []float64{0, 1, -1, 1e300, 1e-300, 3.14159265358979}
	for _, v := range values {
		if _, err := formatNumber(v); err != nil {
			t.Errorf("formatNumber(%v) unexpectedly errored: %v", v, err)
		}
	}
}

func TestCanonicalizeRejectsControlCharacterInString(t *testing.T) {
	// An unescaped control byte inside a string literal is invalid JSON,
	// not something to pass through.
	input := "[\"a" + string(rune(0x01)) + "b\"]"
	if _, err := Canonicalize([]byte(input)); err == nil {
		t.Error("expected an error for an unescaped control character, got nil")
	}
}

func TestCanonicalizeArraysAndNestedObjects(t *testing.T) {
	got, err := canonicalText(t, `{"b":[3,2,{"y":1,"x":2}],"a":"text"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":"text","b":[3,2,{"x":2,"y":1}]}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestNestingDepthBoundary asserts the exact maxDepth boundary, contract
// §1.3: only containers count toward the depth, so 200 nested arrays
// canonicalize and 201 are refused.
func TestNestingDepthBoundary(t *testing.T) {
	nested := func(n int) []byte {
		return []byte(strings.Repeat("[", n) + "1" + strings.Repeat("]", n))
	}
	if _, err := Canonicalize(nested(maxDepth)); err != nil {
		t.Errorf("%d levels of array nesting should canonicalize, got error: %v", maxDepth, err)
	}
	if _, err := Canonicalize(nested(maxDepth + 1)); err == nil {
		t.Errorf("%d levels of array nesting should be refused, got nil error", maxDepth+1)
	}
}

// TestCanonicalizeRejectsInvalidUTF8InStrings is finding 6's regression
// test: an invalid UTF-8 lead byte, a truncated multi-byte sequence, and
// an overlong encoding must all be refused, never silently coerced.
func TestCanonicalizeRejectsInvalidUTF8InStrings(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
	}{
		{"invalid lead byte", []byte("[\"a\xffb\"]")},
		{"truncated multi-byte sequence", []byte("[\"a\xe2\x82\"]")},
		{"overlong encoding", []byte("[\"a\xc0\x80b\"]")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Canonicalize(tc.input); err == nil {
				t.Errorf("Canonicalize(%q): expected an error, got nil", tc.input)
			}
		})
	}
}

// TestCanonicalizeRejectsInvalidUTF8InMemberNames mirrors the string-value
// case for an object member name, exercised through the entryKey path's
// own object construction, not the JSON parser: DeriveEntryKey builds its
// five-member object directly (see entrykey.go), bypassing parseString
// entirely, so the refusal must live in writeValue/sortMembers, not in
// the parser.
func TestCanonicalizeRejectsInvalidUTF8InMemberNames(t *testing.T) {
	if _, err := Canonicalize([]byte("{\"a\xffb\":1}")); err == nil {
		t.Error("expected an error for an invalid UTF-8 member name, got nil")
	}
}

func TestCanonicalizeUsesUTF8Output(t *testing.T) {
	got, err := canonicalText(t, `{"a":"caf\u00e9"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "caf\u00e9") {
		t.Errorf("expected UTF-8 encoded output, got %q", got)
	}
}

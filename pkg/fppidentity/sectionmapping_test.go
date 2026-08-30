package fppidentity

import "testing"

func TestCanonicalSectionMapsFPPRuntimeStrings(t *testing.T) {
	cases := []struct {
		runtime string
		want    string
	}{
		{"LeadIn", "leadIn"},
		{"MainPlaylist", "mainPlaylist"},
		{"LeadOut", "leadOut"},
	}
	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			if got := CanonicalSection(tc.runtime); got != tc.want {
				t.Errorf("CanonicalSection(%q) = %q, want %q", tc.runtime, got, tc.want)
			}
		})
	}
}

// FPP's "New" runtime section, and any other string FPP might report that
// is not one of the three definition sections, has no canonical mapping
// and must not be invented one: contract section 1.2's passthrough rule.
func TestCanonicalSectionPassesUnrecognizedValuesThroughUnchanged(t *testing.T) {
	for _, s := range []string{"New", "", "leadIn", "Whatever"} {
		if got := CanonicalSection(s); got != s {
			t.Errorf("CanonicalSection(%q) = %q, want %q unchanged", s, got, s)
		}
	}
}

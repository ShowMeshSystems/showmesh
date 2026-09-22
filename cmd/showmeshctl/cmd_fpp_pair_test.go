package main

import "testing"

// TestNormalizePairingCodeAcceptsEveryWrittenForm: the code is read off a
// screen and typed by hand, so case, the dash and stray spaces must not
// decide whether pairing works.
func TestNormalizePairingCodeAcceptsEveryWrittenForm(t *testing.T) {
	for _, typed := range []string{
		"CC6W-TAB6", "cc6w-tab6", "CC6WTAB6", "cc6wtab6", " cc6w tab6 ", "Cc6W-tAb6",
	} {
		got, ok := normalizePairingCode(typed)
		if !ok {
			t.Errorf("normalizePairingCode(%q) refused a valid code", typed)
			continue
		}
		if got != "CC6W-TAB6" {
			t.Errorf("normalizePairingCode(%q) = %q, want CC6W-TAB6", typed, got)
		}
	}
}

// TestNormalizePairingCodeRefusesWhatIsNotACode. I, L, O and U are not in
// the alphabet, so a code carrying one was misread.
func TestNormalizePairingCodeRefusesWhatIsNotACode(t *testing.T) {
	for _, typed := range []string{"", "CC6W", "CC6W-TAB6X", "CC6W-TABI", "CC6W-TAB!", "OOOO-LLLL"} {
		if got, ok := normalizePairingCode(typed); ok {
			t.Errorf("normalizePairingCode(%q) = %q, want a refusal", typed, got)
		}
	}
}

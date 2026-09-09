package audio

import (
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestSetSettingsRejectsNonPositiveFadeDuration verifies that
// DefaultFadeDurationMs <= 0 never lands as given: a zero duration would
// make every gain fade dispatched under it a zero-duration ramp.
func TestSetSettingsRejectsNonPositiveFadeDuration(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)

	bad := DefaultSettings
	bad.DefaultFadeDurationMs = 0
	m.SetSettings(bad)

	got := m.SettingsSnapshot()
	if got.DefaultFadeDurationMs != DefaultSettings.DefaultFadeDurationMs {
		t.Fatalf("DefaultFadeDurationMs = %d, want the default %d substituted for the rejected 0", got.DefaultFadeDurationMs, DefaultSettings.DefaultFadeDurationMs)
	}
	state, fields, reason := m.SettingsSubstitution()
	if state != SettingsSubstituted {
		t.Fatalf("state = %q, want %q", state, SettingsSubstituted)
	}
	if len(fields) != 1 || fields[0] != "DefaultFadeDurationMs" {
		t.Fatalf("fields = %v, want exactly [DefaultFadeDurationMs]", fields)
	}
	if reason == "" {
		t.Fatal("a rejected field must be observable, not a silent substitution")
	}
}

// TestSetSettingsRejectsMalformedLTCStartOffset verifies that a malformed
// LTCDefaultStartOffset is caught at configuration time rather than
// discovered later at LTC start.
func TestSetSettingsRejectsMalformedLTCStartOffset(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)

	bad := DefaultSettings
	bad.LTCDefaultStartOffset = pkgaudio.LTCTimecode("not-a-timecode")
	m.SetSettings(bad)

	got := m.SettingsSnapshot()
	if got.LTCDefaultStartOffset != DefaultSettings.LTCDefaultStartOffset {
		t.Fatalf("LTCDefaultStartOffset = %q, want the default %q substituted for the rejected value", got.LTCDefaultStartOffset, DefaultSettings.LTCDefaultStartOffset)
	}
	state, fields, reason := m.SettingsSubstitution()
	if state != SettingsSubstituted {
		t.Fatalf("state = %q, want %q", state, SettingsSubstituted)
	}
	if len(fields) != 1 || fields[0] != "LTCDefaultStartOffset" {
		t.Fatalf("fields = %v, want exactly [LTCDefaultStartOffset]", fields)
	}
	if reason == "" {
		t.Fatal("a rejected field must be observable, not a silent substitution")
	}
}

// TestSetSettingsPreservesEveryOtherValidFieldWhenOneIsRejected verifies
// that one bad field falls back on its own — it must never discard
// every other value an operator actually set.
func TestSetSettingsPreservesEveryOtherValidFieldWhenOneIsRejected(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)

	s := Settings{
		DefaultFadeCurve:          pkgaudio.FadeCurveLinear,
		DefaultFadeDurationMs:     -1, // the only invalid field
		DefaultMaxBackgroundGain:  pkgaudio.Ceiling(0.3),
		DuckFadeDurationMs:        DefaultSettings.DuckFadeDurationMs,
		DuckRestoreFadeDurationMs: DefaultSettings.DuckRestoreFadeDurationMs,
		LTCFrameRate:              pkgaudio.LTCFrameRate25,
		LTCDefaultStartOffset:     pkgaudio.LTCTimecode("02:00:00:00"),
	}
	m.SetSettings(s)

	got := m.SettingsSnapshot()
	if got.DefaultFadeDurationMs != DefaultSettings.DefaultFadeDurationMs {
		t.Fatalf("DefaultFadeDurationMs = %d, want the default substituted", got.DefaultFadeDurationMs)
	}
	if got.DefaultMaxBackgroundGain != pkgaudio.Ceiling(0.3) {
		t.Fatalf("DefaultMaxBackgroundGain = %v, want the operator's own valid value 0.3 preserved", got.DefaultMaxBackgroundGain)
	}
	if got.LTCFrameRate != pkgaudio.LTCFrameRate25 {
		t.Fatalf("LTCFrameRate = %q, want the operator's own valid value preserved", got.LTCFrameRate)
	}
	if got.LTCDefaultStartOffset != pkgaudio.LTCTimecode("02:00:00:00") {
		t.Fatalf("LTCDefaultStartOffset = %q, want the operator's own valid value preserved", got.LTCDefaultStartOffset)
	}

	state, fields, reason := m.SettingsSubstitution()
	if state != SettingsSubstituted {
		t.Fatalf("state = %q, want %q", state, SettingsSubstituted)
	}
	if len(fields) != 1 || fields[0] != "DefaultFadeDurationMs" {
		t.Fatalf("fields = %v, want exactly [DefaultFadeDurationMs], naming only the one bad field", fields)
	}
	if reason == "" {
		t.Fatal("reason is empty, want a stated reason for the one substituted field")
	}
}

// TestSetSettingsClearsSubstitutionOnceCorrected verifies that a later,
// fully valid SetSettings call clears the retained substitution record
// from an earlier bad one -- the observable record must track the CURRENT
// settings, not accumulate forever.
func TestSetSettingsClearsSubstitutionOnceCorrected(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)

	bad := DefaultSettings
	bad.DefaultFadeDurationMs = 0
	m.SetSettings(bad)
	if state, _, _ := m.SettingsSubstitution(); state != SettingsSubstituted {
		t.Fatalf("precondition: state = %q, want %q", state, SettingsSubstituted)
	}

	m.SetSettings(DefaultSettings)
	state, fields, reason := m.SettingsSubstitution()
	if state != SettingsAccepted {
		t.Fatalf("state after a fully valid SetSettings = %q, want %q", state, SettingsAccepted)
	}
	if len(fields) != 0 {
		t.Fatalf("fields after a fully valid SetSettings = %v, want none", fields)
	}
	if reason != "" {
		t.Fatalf("reason after a fully valid SetSettings = %q, want empty", reason)
	}
}

package audio

import (
	"fmt"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// Settings is the subset of the coordinator's audio.settings
// configuration (ADR-039) this package's session logic consults: the
// default fade a gain fade uses when a caller names none, the default
// ceiling applied to a background session that declares none, how far a
// ducked session is lowered, and the LTC frame rate and default start
// offset a show session's LTC run uses absent its own override. The drift threshold has no consumer in this
// package yet.
type Settings struct {
	DefaultFadeCurve         pkgaudio.FadeCurve
	DefaultFadeDurationMs    int
	DefaultMaxBackgroundGain pkgaudio.Ceiling

	// DuckTargetGain is how far a session is lowered while a
	// higher-priority session ducks it, in the same linear-multiplier
	// unit as [pkgaudio.Gain]. PROVISIONAL VALUE, NOT MEASURED: nobody
	// has heard it on the real speakers yet (RES-007). Mute is
	// unaffected, it silences unconditionally.
	DuckTargetGain pkgaudio.Gain

	LTCFrameRate          pkgaudio.LTCFrameRate
	LTCDefaultStartOffset pkgaudio.LTCTimecode

	// Configured reports whether these values came from a real
	// audio.settings.configure push (true) or are still [DefaultSettings]
	// (false). clampToCeilingLocked only applies DefaultMaxBackgroundGain
	// once Configured is true. DuckTargetGain is NOT gated on it: the
	// owner declined to bless the full-silence duck this package used to
	// hardcode, so an unconfigured node ducks to DefaultSettings' own
	// provisional depth rather than to silence. Reason for the ceiling
	// gate: an operator who has never written
	// audio.settings gets today's existing behavior (no ceiling implied
	// for a background session that declares none) rather than a
	// silently-applied guess. The fade curve/duration fallback is not
	// gated on this — DefaultSettings' own values there already match
	// this package's pre-existing hardcoded defaults, so there is no
	// behavior to preserve by withholding them.
	Configured bool
}

// DefaultSettings mirrors internal/coordinator/config's own
// AudioSettingsDefaultPayload (independently reproduced, not imported —
// this package has no coordinator dependency, matching this codebase's
// standing rule that every wire boundary decodes independently), so a
// node that has never received an audio.settings.configure push still
// fades and ceils exactly as the coordinator's documented default would.
var DefaultSettings = Settings{
	DefaultFadeCurve:         pkgaudio.FadeCurveLinear,
	DefaultFadeDurationMs:    1000,
	DefaultMaxBackgroundGain: pkgaudio.Ceiling(0.6),
	DuckTargetGain:           pkgaudio.Gain(0.25),
	LTCFrameRate:             pkgaudio.LTCFrameRate30,
	LTCDefaultStartOffset:    pkgaudio.LTCTimecode("00:00:00:00"),
}

// validDuckTargetGain reports why g is not a usable duck depth: a valid
// gain strictly below unity, since a duck lowers a session and a gain of
// 1 or more would not duck anything.
func validDuckTargetGain(g pkgaudio.Gain) error {
	if err := g.Validate(); err != nil {
		return err
	}
	if g >= pkgaudio.Gain(1) {
		return fmt.Errorf("audio: duck target gain must be below 1: got %v", float64(g))
	}
	return nil
}

// invalidSettingsFields validates s field by field against every wire
// boundary this package independently reproduces — the coordinator
// validates the same values on write, but that does not exempt this
// package from checking what actually arrives over MQTT. Returns one
// message per field that failed, naming the field and why.
func invalidSettingsFields(s Settings) []string {
	var issues []string
	if s.DefaultFadeDurationMs <= 0 {
		issues = append(issues, fmt.Sprintf("DefaultFadeDurationMs %d is not positive", s.DefaultFadeDurationMs))
	}
	if err := s.DefaultFadeCurve.Validate(); err != nil {
		issues = append(issues, "DefaultFadeCurve: "+err.Error())
	}
	if err := s.DefaultMaxBackgroundGain.Validate(); err != nil {
		issues = append(issues, "DefaultMaxBackgroundGain: "+err.Error())
	}
	if err := validDuckTargetGain(s.DuckTargetGain); err != nil {
		issues = append(issues, "DuckTargetGain: "+err.Error())
	}
	if err := s.LTCFrameRate.Validate(); err != nil {
		issues = append(issues, "LTCFrameRate: "+err.Error())
	}
	if err := s.LTCDefaultStartOffset.Validate(); err != nil {
		issues = append(issues, "LTCDefaultStartOffset: "+err.Error())
	}
	return issues
}

// SetSettings replaces m's current Settings — [internal/agent]'s
// audio.settings.configure operation is the only caller. Safe to call
// concurrently with any session dispatch.
//
// A field that fails its own wire-boundary validation is never accepted
// as given — every other field an operator actually set still lands, and
// only the bad one falls back to [DefaultSettings]'s value for that
// field. The substitution is logged and retained for
// [Manager.SettingsValidationIssues]; no coordinator surface reports it
// yet, so today it is visible on the node and nowhere else.
func (m *Manager) SetSettings(s Settings) {
	s.Configured = true
	issues := invalidSettingsFields(s)
	if len(issues) > 0 {
		if s.DefaultFadeDurationMs <= 0 {
			s.DefaultFadeDurationMs = DefaultSettings.DefaultFadeDurationMs
		}
		if s.DefaultFadeCurve.Validate() != nil {
			s.DefaultFadeCurve = DefaultSettings.DefaultFadeCurve
		}
		if s.DefaultMaxBackgroundGain.Validate() != nil {
			s.DefaultMaxBackgroundGain = DefaultSettings.DefaultMaxBackgroundGain
		}
		if validDuckTargetGain(s.DuckTargetGain) != nil {
			s.DuckTargetGain = DefaultSettings.DuckTargetGain
		}
		if s.LTCFrameRate.Validate() != nil {
			s.LTCFrameRate = DefaultSettings.LTCFrameRate
		}
		if s.LTCDefaultStartOffset.Validate() != nil {
			s.LTCDefaultStartOffset = DefaultSettings.LTCDefaultStartOffset
		}
		for _, issue := range issues {
			m.logf("audio settings: rejected invalid value, using default instead: %s", issue)
		}
	}

	m.settingsMu.Lock()
	m.settings = s
	m.settingsIssues = issues
	m.settingsMu.Unlock()
}

// SettingsSnapshot returns m's current Settings, [DefaultSettings] until
// [Manager.SetSettings] is ever called.
func (m *Manager) SettingsSnapshot() Settings {
	m.settingsMu.RLock()
	defer m.settingsMu.RUnlock()
	return m.settings
}

// SettingsValidationIssues returns the field-level problems the most
// recent [Manager.SetSettings] call found, or nil once a call arrives
// with none. Each entry names the field that fell back to its
// [DefaultSettings] value and why. Nothing in production reads this yet.
func (m *Manager) SettingsValidationIssues() []string {
	m.settingsMu.RLock()
	defer m.settingsMu.RUnlock()
	return m.settingsIssues
}

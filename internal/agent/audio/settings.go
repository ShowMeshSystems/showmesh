package audio

import (
	"fmt"
	"strings"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// Settings is the subset of the coordinator's audio.settings
// configuration (ADR-039) this package's session logic consults: the
// default fade a gain fade uses when a caller names none, the default
// ceiling applied to a background session that declares none, how far a
// ducked session is lowered, and the LTC frame rate and default start
// offset a show session's LTC run uses absent its own override, and the
// drift threshold a scheduled session's timeline judges a discontinuity
// against.
type Settings struct {
	// DriftIgnoreThresholdMs is how large a scheduled session's timeline
	// error has to be before a discontinuity is answered with a seek
	// (RES-019 section 6). It is ONLY ever a magnitude filter on a
	// discontinuity whose cause has already been established: no value
	// here can by itself cause a seek. See timeline.go.
	//
	// This package picks no value for it. Zero, or any non-positive
	// value, means no usable threshold has been pushed to this node, and
	// a timeline with no usable threshold reports its error and never
	// seeks. [DefaultSettings] therefore leaves it zero rather than
	// mirroring the coordinator's own stored default the way every other
	// field here does.
	DriftIgnoreThresholdMs int

	DefaultFadeCurve         pkgaudio.FadeCurve
	DefaultFadeDurationMs    int
	DefaultMaxBackgroundGain pkgaudio.Ceiling

	// DuckTargetGain is how far a session is lowered while a
	// higher-priority session ducks it, in the same linear-multiplier
	// unit as [pkgaudio.Gain]. PROVISIONAL VALUE, NOT MEASURED: nobody
	// has heard it on the real speakers yet (RES-007). Mute is
	// unaffected, it silences unconditionally.
	DuckTargetGain pkgaudio.Gain

	// DuckFadeDurationMs is how long a session takes to fade DOWN to
	// DuckTargetGain once a higher-priority session starts ducking it —
	// fast, broadcast-style attack, since an announcement is already
	// talking over the bed by the time the duck starts.
	DuckFadeDurationMs int

	// DuckRestoreFadeDurationMs is how long a session takes to fade back
	// UP once the last ducker releases it — slower than
	// DuckFadeDurationMs by design: an abrupt return to full level once
	// nothing is talking over the bed reads as jarring in a way the
	// duck-down does not.
	DuckRestoreFadeDurationMs int

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
	DefaultFadeCurve:          pkgaudio.FadeCurveLinear,
	DefaultFadeDurationMs:     1000,
	DefaultMaxBackgroundGain:  pkgaudio.Ceiling(0.6),
	DuckTargetGain:            pkgaudio.Gain(0.25),
	DuckFadeDurationMs:        200,
	DuckRestoreFadeDurationMs: 800,
	LTCFrameRate:              pkgaudio.LTCFrameRate30,
	LTCDefaultStartOffset:     pkgaudio.LTCTimecode("00:00:00:00"),
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

// SettingsState is [Manager.SettingsSubstitution]'s own report of whether
// the most recent [Manager.SetSettings] call applied its revision as given
// or fell back on at least one field's [DefaultSettings] value --
// node.audio.settings.state (docs/build/IDENTIFIER-REGISTER.md), matching
// the state/reason pairing [EngineRestoreState] and the LTC generator's
// own state already use rather than inventing a second convention.
type SettingsState string

const (
	// SettingsAccepted is the zero value: every field of the most recent
	// SetSettings call passed its own wire-boundary validation and landed
	// as given.
	SettingsAccepted SettingsState = "accepted"

	// SettingsSubstituted means at least one field of the most recent
	// SetSettings call failed its own validation and was replaced by its
	// [DefaultSettings] value; every other field the operator set still
	// landed.
	SettingsSubstituted SettingsState = "substituted"
)

// settingsFieldIssue is one field of a Settings value that failed its own
// wire-boundary validation: field is the exact Settings struct field name
// (what node.audio.settings.substituted_fields names), message is the
// full description this package's log line and
// node.audio.settings.reason carry.
type settingsFieldIssue struct {
	field   string
	message string
}

// invalidSettingsFields validates s field by field against every wire
// boundary this package independently reproduces — the coordinator
// validates the same values on write, but that does not exempt this
// package from checking what actually arrives over MQTT. Returns one
// issue per field that failed, naming the field and why.
func invalidSettingsFields(s Settings) []settingsFieldIssue {
	var issues []settingsFieldIssue
	if s.DefaultFadeDurationMs <= 0 {
		issues = append(issues, settingsFieldIssue{"DefaultFadeDurationMs", fmt.Sprintf("DefaultFadeDurationMs %d is not positive", s.DefaultFadeDurationMs)})
	}
	if err := s.DefaultFadeCurve.Validate(); err != nil {
		issues = append(issues, settingsFieldIssue{"DefaultFadeCurve", "DefaultFadeCurve: " + err.Error()})
	}
	if err := s.DefaultMaxBackgroundGain.Validate(); err != nil {
		issues = append(issues, settingsFieldIssue{"DefaultMaxBackgroundGain", "DefaultMaxBackgroundGain: " + err.Error()})
	}
	if err := validDuckTargetGain(s.DuckTargetGain); err != nil {
		issues = append(issues, settingsFieldIssue{"DuckTargetGain", "DuckTargetGain: " + err.Error()})
	}
	if s.DuckFadeDurationMs <= 0 {
		issues = append(issues, settingsFieldIssue{"DuckFadeDurationMs", fmt.Sprintf("DuckFadeDurationMs %d is not positive", s.DuckFadeDurationMs)})
	}
	if s.DuckRestoreFadeDurationMs <= 0 {
		issues = append(issues, settingsFieldIssue{"DuckRestoreFadeDurationMs", fmt.Sprintf("DuckRestoreFadeDurationMs %d is not positive", s.DuckRestoreFadeDurationMs)})
	}
	if err := s.LTCFrameRate.Validate(); err != nil {
		issues = append(issues, settingsFieldIssue{"LTCFrameRate", "LTCFrameRate: " + err.Error()})
	}
	if err := s.LTCDefaultStartOffset.Validate(); err != nil {
		issues = append(issues, settingsFieldIssue{"LTCDefaultStartOffset", "LTCDefaultStartOffset: " + err.Error()})
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
// [Manager.SettingsSubstitution], which internal/agent's own audio report
// carries onto node.audio.settings.state/.substituted_fields/.reason.
func (m *Manager) SetSettings(s Settings) {
	s.Configured = true
	issues := invalidSettingsFields(s)
	var fields []string
	var messages []string
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
		if s.DuckFadeDurationMs <= 0 {
			s.DuckFadeDurationMs = DefaultSettings.DuckFadeDurationMs
		}
		if s.DuckRestoreFadeDurationMs <= 0 {
			s.DuckRestoreFadeDurationMs = DefaultSettings.DuckRestoreFadeDurationMs
		}
		if s.LTCFrameRate.Validate() != nil {
			s.LTCFrameRate = DefaultSettings.LTCFrameRate
		}
		if s.LTCDefaultStartOffset.Validate() != nil {
			s.LTCDefaultStartOffset = DefaultSettings.LTCDefaultStartOffset
		}
		for _, issue := range issues {
			fields = append(fields, issue.field)
			messages = append(messages, issue.message)
			m.logf("audio settings: rejected invalid value, using default instead: %s", issue.message)
		}
	}

	state := SettingsAccepted
	var reason string
	if len(issues) > 0 {
		state = SettingsSubstituted
		reason = strings.Join(messages, "; ")
	}

	m.settingsMu.Lock()
	m.settings = s
	m.settingsState = state
	m.settingsSubstitutedFields = fields
	m.settingsReason = reason
	m.settingsMu.Unlock()
}

// SettingsSnapshot returns m's current Settings, [DefaultSettings] until
// [Manager.SetSettings] is ever called.
func (m *Manager) SettingsSnapshot() Settings {
	m.settingsMu.RLock()
	defer m.settingsMu.RUnlock()
	return m.settings
}

// SettingsSubstitution returns whether the most recent [Manager.
// SetSettings] call applied its revision as given ([SettingsAccepted]) or
// fell back on at least one field's [DefaultSettings] value
// ([SettingsSubstituted]), which Settings struct fields were substituted,
// and why, in one joined string. fields is nil and reason is "" whenever
// state is [SettingsAccepted], including before SetSettings is ever
// called. internal/agent's own audio report is this accessor's one
// caller, carrying it onto node.audio.settings.state/.substituted_fields/
// .reason (docs/build/IDENTIFIER-REGISTER.md).
func (m *Manager) SettingsSubstitution() (state SettingsState, fields []string, reason string) {
	m.settingsMu.RLock()
	defer m.settingsMu.RUnlock()
	state = m.settingsState
	if state == "" {
		state = SettingsAccepted
	}
	return state, m.settingsSubstitutedFields, m.settingsReason
}

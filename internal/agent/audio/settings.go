package audio

import (
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// Settings is the subset of the coordinator's audio.settings
// configuration (ADR-039) this package's session logic consults: the
// default fade a gain fade uses when a caller names none, and the
// default ceiling applied to a background session that declares none.
// The drift threshold and LTC fields have no consumer in this package
// yet.
type Settings struct {
	DefaultFadeCurve         pkgaudio.FadeCurve
	DefaultFadeDurationMs    int
	DefaultMaxBackgroundGain pkgaudio.Ceiling

	// Configured reports whether these values came from a real
	// audio.settings.configure push (true) or are still [DefaultSettings]
	// (false). clampToCeilingLocked only applies DefaultMaxBackgroundGain
	// once Configured is true: an operator who has never written
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
}

// SetSettings replaces m's current Settings — [internal/agent]'s
// audio.settings.configure operation is the only caller. Safe to call
// concurrently with any session dispatch.
func (m *Manager) SetSettings(s Settings) {
	s.Configured = true
	m.settingsMu.Lock()
	m.settings = s
	m.settingsMu.Unlock()
}

// SettingsSnapshot returns m's current Settings, [DefaultSettings] until
// [Manager.SetSettings] is ever called.
func (m *Manager) SettingsSnapshot() Settings {
	m.settingsMu.RLock()
	defer m.settingsMu.RUnlock()
	return m.settings
}

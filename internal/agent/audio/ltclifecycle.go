package audio

import (
	"context"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// ltcOwner tracks which session, if any, currently owns this node's one
// LTC run — [LTCGenerator] is handle-less because there is exactly one,
// so only the session that started it may stop it, and a lower-priority
// session's own pause or stop must never silence a show session it does
// not own.
type ltcOwner struct {
	mu    sync.Mutex
	id    pkgaudio.SessionID
	owned bool
}

func (o *ltcOwner) set(id pkgaudio.SessionID) {
	o.mu.Lock()
	o.id, o.owned = id, true
	o.mu.Unlock()
}

// release clears ownership when id currently holds it, reporting whether
// it did — the guard every stop path uses before touching the engine.
func (o *ltcOwner) release(id pkgaudio.SessionID) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.owned || o.id != id {
		return false
	}
	o.owned = false
	return true
}

// isShowSessionLocked reports whether s's desired source role is
// [pkgaudio.SourceRoleShow] — the only role that ever drives LTC.
// Background, announcement, and manual sessions never start, realign, or
// stop it. Caller holds s.mu.
func isShowSessionLocked(s *Session) bool {
	return s.desired.SourceRole != nil && *s.desired.SourceRole == pkgaudio.SourceRoleShow
}

// resolveLTCSpec resolves the frame rate and default start offset this
// node's audio.settings currently authorize, reporting ok=false with a
// stated reason when settings have never been configured or carry no
// usable frame rate — LTC must never run against a plausible-looking
// default in that case.
func (m *Manager) resolveLTCSpec() (rate pkgaudio.LTCFrameRate, defaultOffset pkgaudio.LTCTimecode, ok bool, reason string) {
	settings := m.SettingsSnapshot()
	if !settings.Configured {
		return "", "", false, "audio.settings has never been configured; LTC has no frame rate to run at"
	}
	if err := settings.LTCFrameRate.Validate(); err != nil {
		return "", "", false, "configured audio.settings carries no usable LTC frame rate: " + err.Error()
	}
	return settings.LTCFrameRate, settings.LTCDefaultStartOffset, true, ""
}

// startLTCLocked starts, or realigns, this node's LTC run for s at
// position — the timecode for s's own show playhead. A no-op for
// anything but a show-role session, an engine that cannot generate LTC,
// or unconfigured settings. A [LTCGenerator.StartLTC] failure is logged
// and never propagated: program audio continues, and the failure is
// reported as LTC evidence via [LTCObservation] instead — see
// [Manager.ObserveLTC]. Caller holds s.mu.
func (m *Manager) startLTCLocked(ctx context.Context, s *Session, position time.Duration) {
	if !isShowSessionLocked(s) {
		return
	}
	gen, ok := m.engine.(LTCGenerator)
	if !ok {
		return
	}
	rate, defaultOffset, ok, reason := m.resolveLTCSpec()
	if !ok {
		m.logLTC(s.id, "audio: LTC not started", reason)
		return
	}
	base := ResolveLTCStartOffset(s.desired.LTCStartOffset, defaultOffset)
	tc, err := base.Advance(position, rate)
	if err != nil {
		m.logLTC(s.id, "audio: could not resolve this session's LTC start timecode", err.Error())
		return
	}
	if _, err := gen.StartLTC(ctx, LTCSpec{FrameRate: rate, StartTimecode: tc}); err != nil {
		m.logLTC(s.id, "audio: StartLTC failed", err.Error())
		return
	}
	m.ltc.set(s.id)
}

// stopLTCLocked stops this node's LTC run when s currently owns it — a
// no-op for any other session. A [LTCGenerator.StopLTC] failure is
// logged and never propagated, the same rule [Manager.startLTCLocked]
// follows. Caller holds s.mu.
func (m *Manager) stopLTCLocked(ctx context.Context, s *Session) {
	if !m.ltc.release(s.id) {
		return
	}
	gen, ok := m.engine.(LTCGenerator)
	if !ok {
		return
	}
	if _, err := gen.StopLTC(ctx); err != nil {
		m.logLTC(s.id, "audio: StopLTC failed", err.Error())
	}
}

func (m *Manager) logLTC(id pkgaudio.SessionID, msg, reason string) {
	if m.logger != nil {
		m.logger.Warn(msg, "session", string(id), "reason", reason)
	}
}

// ObserveLTC returns this node's fresh LTC evidence straight from the
// wired engine, satisfying the report loop's ltcObserver interface (see
// internal/agent/audioreport.go).
func (m *Manager) ObserveLTC(ctx context.Context) LTCObservation {
	return ObserveEngineLTC(ctx, m.engine, m.now())
}

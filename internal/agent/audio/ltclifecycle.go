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

// LTCClaimState is a session's own standing relationship to this node's
// one LTC run (audio_session.ltc.claim.state): "held" while it owns the
// run, "refused" when its own attempt to claim it was turned away
// because another session still holds it, "none" for a session that has
// never attempted a claim (never a show session, or a show session that
// released or never started one). A refused session's only OTHER
// evidence used to be a warn-level log line on the node — invisible to
// every coordinator surface; this field is what makes the refusal
// itself, not just the resulting "no LTC for this session" absence,
// legible to an operator looking at the session that was turned away.
type LTCClaimState string

const (
	LTCClaimNone    LTCClaimState = "none"
	LTCClaimHeld    LTCClaimState = "held"
	LTCClaimRefused LTCClaimState = "refused"
)

// claim gives id the LTC run when it is free or already id's own,
// reporting the current owner when another session holds it. LTC is never
// taken from a session that still holds it: re-anchoring a running show's
// timecode from under it is a show-visible failure, and doing it silently
// is worse than not doing it at all.
func (o *ltcOwner) claim(id pkgaudio.SessionID) (holder pkgaudio.SessionID, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.owned && o.id != id {
		return o.id, false
	}
	o.id, o.owned = id, true
	return id, true
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

// resetForRestore unconditionally clears ownership, regardless of who
// currently holds it — see [Manager.RestoreAll]'s doc comment.
func (o *ltcOwner) resetForRestore() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.id, o.owned = "", false
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
	if holder, free := m.ltc.claim(s.id); !free {
		reason := "this node's one LTC run is held by session " + string(holder)
		s.ltcClaimState, s.ltcClaimReason = LTCClaimRefused, reason
		m.logLTC(s.id, "audio: LTC not started", reason)
		return
	}
	s.ltcClaimState, s.ltcClaimReason = LTCClaimHeld, ""
	startCtx, cancel := boundedEngineCallContext(ctx)
	_, err = gen.StartLTC(startCtx, LTCSpec{FrameRate: rate, StartTimecode: tc})
	cancel()
	if err != nil {
		m.ltc.release(s.id)
		s.ltcClaimState, s.ltcClaimReason = LTCClaimNone, ""
		m.logLTC(s.id, "audio: StartLTC failed", err.Error())
	}
}

// stopLTCLocked clears s's own standing claim on this node's one LTC run
// — s stopped, or stopped being a show-role session, either way s earns
// no further claim evidence — and stops the engine's run too when s is
// the one that actually held it; a no-op on the engine for any other
// session. A refused session never held the run, so it must still leave
// here with its claim reset to [LTCClaimNone]: without that, a session
// turned away once kept reporting "refused" forever, naming a holder
// that may itself have long since released the run. A
// [LTCGenerator.StopLTC] failure is logged and never propagated, the
// same rule [Manager.startLTCLocked] follows. Caller holds s.mu.
func (m *Manager) stopLTCLocked(ctx context.Context, s *Session) {
	held := m.ltc.release(s.id)
	s.ltcClaimState, s.ltcClaimReason = LTCClaimNone, ""
	if !held {
		return
	}
	gen, ok := m.engine.(LTCGenerator)
	if !ok {
		return
	}
	stopCtx, cancel := boundedEngineCallContext(ctx)
	_, err := gen.StopLTC(stopCtx)
	cancel()
	if err != nil {
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

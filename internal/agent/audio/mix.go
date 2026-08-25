package audio

import (
	"context"
	"fmt"
	"math"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// mutedGain is what a session is driven to while muted. Mute means
// silence and is not configurable; how far a DUCK lowers a session is
// [Settings.DuckTargetGain], a separate operator-set value.
const mutedGain = pkgaudio.Gain(0)

// configuredGainLocked returns the gain last requested through
// audio.gain.set or audio.gain.fade, or unity when neither has ever run.
// Mute and duck never write this field, it is the value
// [Session.effectiveGainLocked] composes with the active suppression
// reasons, not the value driven to the engine. Caller holds s.mu.
func (s *Session) configuredGainLocked() pkgaudio.Gain {
	if s.desired.Gain != nil {
		return *s.desired.Gain
	}
	return pkgaudio.Gain(1)
}

// effectiveGainLocked derives s's current output gain from the
// configured gain and every active reason to reduce it: mute silences
// unconditionally, otherwise an active duck lowers to the operator's
// configured duck depth, otherwise the configured gain applies. A duck
// never raises a session: a bed already quieter than the duck depth
// keeps its own configured gain. The ceiling is reapplied on
// every call, not only when the configured gain was last set, so a
// ceiling lowered mid-duck or mid-mute still bounds the value the next
// time it is computed. This is the single source of truth for what the
// engine should read; nothing else in this package derives a gain to
// drive. Caller holds s.mu.
func (s *Session) effectiveGainLocked() pkgaudio.Gain {
	g := s.configuredGainLocked()
	switch {
	case s.muted:
		g = mutedGain
	case len(s.duckedByAll) > 0:
		if duck := s.mgr.SettingsSnapshot().DuckTargetGain; duck < g {
			g = duck
		}
	}
	if result, err := s.clampToCeilingLocked(g); err == nil {
		return result.Effective
	}
	return g
}

// applyEffectiveGainLocked drives the engine to s's current
// [Session.effectiveGainLocked] when a handle is loaded. This is the
// ONLY place in this package that calls engine.SetGain for a gain-only
// change: every path that can change the configured gain (GainSet,
// GainFade's immediate target) or the set of active suppression reasons
// (mute, unmute, duck start, duck end) calls this afterward instead of
// computing or dispatching a gain itself, so the engine is always driven
// from one derivation, never from a value hand-copied into a second
// field. Caller holds s.mu.
func (s *Session) applyEffectiveGainLocked(ctx context.Context) pkgaudio.OutcomeResult {
	effective := s.effectiveGainLocked()
	if !s.handleLoaded {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeGain}
	}
	dispatchedAt := s.mgr.now()
	gainCtx, cancel := boundedEngineCallContext(ctx)
	obs, err := s.mgr.engine.SetGain(gainCtx, s.handle, effective)
	cancel()
	if err != nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()}
	}
	if obs.ObservedAt.Before(dispatchedAt) {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: "engine evidence predates this dispatch"}
	}
	if obs.Gain != effective {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: fmt.Sprintf("engine reports gain %v, expected %v", obs.Gain, effective)}
	}
	return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeGain}
}

// applyEffectiveGainBestEffortLocked is [Session.applyEffectiveGainLocked]
// for a caller that already reports its own outcome through other means
// (duck and mute transitions report a Ducked or gain outcome, not this
// one): it logs a failure or a mismatch rather than returning one.
// Caller holds s.mu.
func (m *Manager) applyEffectiveGainBestEffortLocked(ctx context.Context, s *Session) {
	if result := s.applyEffectiveGainLocked(ctx); result.Outcome != pkgaudio.OutcomeGain {
		m.logf("audio session %s: gain apply %s: %s", s.id, result.Outcome, result.Reason)
	}
}

// clampToCeilingLocked applies s's own declared ceiling if it has one;
// otherwise, for a background-role session, once a real
// audio.settings.configure has been delivered
// ([Settings.Configured]), applies its DefaultMaxBackgroundGain — the
// operator-configured ceiling a background bed gets when it declares
// none itself. Before any audio.settings has ever been delivered, or for
// any other session role, a session with no declared ceiling stays
// unclamped, matching this package's pre-existing behavior. Reports the
// clamp either way so a caller can carry it as outcome evidence rather
// than silently applying an unreported value. Caller holds s.mu.
func (s *Session) clampToCeilingLocked(requested pkgaudio.Gain) (pkgaudio.CeilingResult, error) {
	ceiling := s.desired.Ceiling
	if ceiling == nil && s.desired.SourceRole != nil && *s.desired.SourceRole == pkgaudio.SourceRoleBackground {
		settings := s.mgr.SettingsSnapshot()
		if settings.Configured {
			c := settings.DefaultMaxBackgroundGain
			ceiling = &c
		}
	}
	if ceiling == nil {
		if err := requested.Validate(); err != nil {
			return pkgaudio.CeilingResult{}, err
		}
		return pkgaudio.CeilingResult{Requested: requested, Effective: requested}, nil
	}
	return pkgaudio.ApplyCeiling(requested, *ceiling)
}

// GainSet is audio.gain.set: it changes id's configured gain, clamped to
// its ceiling (ruling: the ceiling is enforced at the point gain takes
// effect, on every path, not only at validation), and applies the
// resulting effective gain to the engine.
func (m *Manager) GainSet(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision, requested pkgaudio.Gain) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()
	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		return m.gateAvailability(s.setGainLocked(ctx, requested))
	})
	s.mu.Unlock()
	return res.outcome
}

// setGainLocked clamps requested to s's ceiling, records it as s's
// configured gain, and applies the current effective gain to the
// engine, which is the configured value verbatim only when s is neither
// muted nor ducked; otherwise the active suppression's own target is
// what actually reaches the engine, so a gain.set landing mid-duck or
// mid-mute can never make a suppressed session audible. Caller holds
// s.mu.
func (s *Session) setGainLocked(ctx context.Context, requested pkgaudio.Gain) pkgaudio.OutcomeResult {
	result, err := s.clampToCeilingLocked(requested)
	if err != nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: err.Error()}
	}
	configured := result.Effective
	s.desired.Gain = &configured
	s.persistBestEffortLocked("state change")

	outcome := s.applyEffectiveGainLocked(ctx)
	if result.Clamped && outcome.Outcome == pkgaudio.OutcomeGain {
		outcome.Reason = fmt.Sprintf("gain clamped to ceiling: requested %v, effective %v", result.Requested, result.Effective)
	}
	return outcome
}

// GainFade is audio.gain.fade: it schedules a fade toward target,
// clamped to id's ceiling, and dispatches it to the engine. The
// returned outcome reports the fade as dispatched, never as complete —
// completion is observed later by [Manager.watchTick] via
// [EngineObservation.FadeActive], never inferred from fade.Duration
// having elapsed.
func (m *Manager) GainFade(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision, curve pkgaudio.FadeCurve, duration time.Duration, target pkgaudio.Gain) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()
	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		return m.gateAvailability(s.startFadeLocked(ctx, invocation, curve, duration, target))
	})
	s.mu.Unlock()
	return res.outcome
}

// startFadeLocked clamps target to s's ceiling, records it as s's
// configured gain immediately, the same instant GainSet would, and
// dispatches an engine fade toward the CURRENT effective gain, not the
// raw configured target: while s is muted or ducked, that is the
// suppressed gain, so a fade requested mid-suppression cannot ramp a
// suppressed session back up before the suppression actually releases.
// Releasing mute or a duck later drives the engine to the by-then
// configured gain directly, through [Session.applyEffectiveGainLocked],
// never by resuming this fade's own ramp. Caller holds s.mu.
func (s *Session) startFadeLocked(ctx context.Context, invocation pkgaudio.InvocationID, curve pkgaudio.FadeCurve, duration time.Duration, target pkgaudio.Gain) pkgaudio.OutcomeResult {
	result, err := s.clampToCeilingLocked(target)
	if err != nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: err.Error()}
	}
	configured := result.Effective
	fade := pkgaudio.Fade{Curve: curve, Duration: duration, TargetGain: configured}
	if err := fade.Validate(); err != nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: err.Error()}
	}
	if !s.handleLoaded {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "no active playback to fade"}
	}

	f := fade
	s.desired.Fade = &f
	s.desired.Gain = &configured
	s.fadePending = true
	s.fadeInvocation = invocation
	s.fadeHandleNeverFaded = false
	s.fadeState = FadeStateInProgress
	s.persistBestEffortLocked("state change")

	engineFade := fade
	engineFade.TargetGain = s.effectiveGainLocked()
	s.fadeDispatchedTarget = engineFade.TargetGain
	dispatchedAt := s.mgr.now()
	obs, err := s.mgr.engine.Fade(ctx, s.handle, engineFade)
	if err != nil {
		s.fadePending = false
		s.fadeInvocation = ""
		s.fadeState = FadeStateNone
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()}
	}
	if obs.ObservedAt.Before(dispatchedAt) {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: "engine evidence predates this dispatch"}
	}
	reason := "fade dispatched, not yet complete"
	if result.Clamped {
		reason = fmt.Sprintf("fade target clamped to ceiling: requested %v, effective %v", result.Requested, result.Effective)
	}
	return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeGain, Reason: reason}
}

// checkFadeCompletionLocked reads s's engine evidence and, exactly once
// per dispatched fade, resolves s.fadePending from a FadeActive
// transition true-to-false, never from fade.Duration having elapsed. It
// also writes the fade's terminal outcome back onto the invocation that
// dispatched it: [pkgaudio.OutcomeFadeComplete] when the engine's own
// evidence shows s.fadeDispatchedTarget actually reached,
// [pkgaudio.OutcomeUnconfirmable] otherwise, gated through
// [Manager.gateAvailability] exactly as every other outcome in this
// package is. Judged against the target actually dispatched, not
// against the CURRENT effective gain: a mute or a duck landing mid-fade
// cancels the ramp by driving the engine to its own target through
// [Session.applyEffectiveGainLocked]'s own SetGain call, and a fade
// judged against the current effective gain at that point would compare
// the engine's evidence to a question the dispatched fade was never
// asked, reporting a cancelled fade as complete. s's own configured
// gain was already recorded at dispatch time in
// [Session.startFadeLocked]; this never rewrites it from the engine's
// observed value, which would reintroduce the desired/observed
// conflation this package's evidence rules forbid. Caller holds s.mu.
func (s *Session) checkFadeCompletionLocked(ctx context.Context) {
	if !s.fadePending || !s.handleLoaded {
		return
	}
	if s.fadeHandleNeverFaded {
		s.resolveFadeInterruptedByRestartLocked()
		return
	}
	obsCtx, cancel := boundedObserveContext(ctx)
	obs, err := s.mgr.engine.Observe(obsCtx, s.handle)
	cancel()
	if err != nil {
		// A failing Observe here is the same class of evidence
		// [Manager.watchTick]'s identical poll already treats as a fault:
		// this session's own engine handle just failed to answer, mid-fade.
		s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
		return
	}
	if obs.FadeActive {
		return
	}

	target := s.fadeDispatchedTarget
	var outcome pkgaudio.OutcomeResult
	if gainsEqual(obs.Gain, target) {
		outcome = pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFadeComplete}
	} else {
		outcome = pkgaudio.OutcomeResult{
			Outcome: pkgaudio.OutcomeUnconfirmable,
			Reason:  fmt.Sprintf("engine reports gain %v once the fade resolved, expected target %v", obs.Gain, target),
		}
	}
	if s.fadeInvocation != "" {
		s.rememberExecutedResultLocked(s.fadeInvocation, s.mgr.gateAvailability(outcome))
	}

	s.fadePending = false
	s.fadeInvocation = ""
	s.fadeState = FadeStateComplete
	s.persistBestEffortLocked("state change")
}

// resolveFadeInterruptedByRestartLocked answers a fade that survived a
// restart onto a handle never driven through it. s's configured gain is
// left as restored rather than read back from that handle. Caller holds
// s.mu.
func (s *Session) resolveFadeInterruptedByRestartLocked() {
	if s.fadeInvocation != "" {
		s.rememberExecutedResultLocked(s.fadeInvocation, s.mgr.gateAvailability(pkgaudio.OutcomeResult{
			Outcome: pkgaudio.OutcomeUnconfirmable,
			Reason:  "fade was interrupted by a restart before it reached its target",
		}))
	}
	s.fadePending = false
	s.fadeInvocation = ""
	s.fadeHandleNeverFaded = false
	s.fadeState = FadeStateNone
	s.persistBestEffortLocked("state change")
}

// resolveFadePendingStrandedLocked resolves a still-pending fade to
// Unconfirmable with reason when the session leaves Playing for a reason
// other than the fade's own completion — [Session.advanceLocked]'s two
// no-successor-item branches, where a fade dispatched on the last item
// would otherwise never reach a terminal outcome. Caller holds s.mu.
func (s *Session) resolveFadePendingStrandedLocked(reason string) {
	if !s.fadePending {
		return
	}
	if s.fadeInvocation != "" {
		s.rememberExecutedResultLocked(s.fadeInvocation, s.mgr.gateAvailability(pkgaudio.OutcomeResult{
			Outcome: pkgaudio.OutcomeUnconfirmable,
			Reason:  reason,
		}))
	}
	s.fadePending = false
	s.fadeInvocation = ""
	s.fadeHandleNeverFaded = false
	s.fadeState = FadeStateNone
}

// gainEpsilon bounds how far an engine's reported gain may sit from a
// fade's target and still count as having reached it. A real backend
// computes a ramp in floating point, so exact equality would report a
// completed fade as unconfirmable forever; the fake stores exact values
// and cannot expose that.
const gainEpsilon = 1e-6

func gainsEqual(a, b pkgaudio.Gain) bool {
	return math.Abs(float64(a-b)) <= gainEpsilon
}

// Mute is audio.output.mute: it marks id muted and applies the
// resulting effective gain, which is mutedGain regardless of what the
// configured gain or any active duck says. Idempotent: muting an
// already-muted session reports the existing mute rather than
// re-applying it.
func (m *Manager) Mute(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()
	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		return m.gateAvailability(s.muteLocked(ctx))
	})
	s.mu.Unlock()
	return res.outcome
}

func (s *Session) muteLocked(ctx context.Context) pkgaudio.OutcomeResult {
	if s.muted {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeGain, Reason: "already muted"}
	}
	s.muted = true
	s.persistBestEffortLocked("state change")
	return s.applyEffectiveGainLocked(ctx)
}

// Unmute is audio.output.unmute: it clears muted and applies the
// resulting effective gain, the configured gain re-clamped to whatever
// ceiling is current now, when no duck is active, or the duck's own
// target when one still is, because the announcement is still playing.
// Idempotent: unmuting an unmuted session is a no-op success, never a
// refusal.
func (m *Manager) Unmute(ctx context.Context, id pkgaudio.SessionID, invocation pkgaudio.InvocationID, revision pkgaudio.Revision) pkgaudio.OutcomeResult {
	s, ok := m.get(id)
	if !ok {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "session does not exist"}
	}
	s.mu.Lock()
	res := s.dispatch(invocation, revision, func() pkgaudio.OutcomeResult {
		return m.gateAvailability(s.unmuteLocked(ctx))
	})
	s.mu.Unlock()
	return res.outcome
}

func (s *Session) unmuteLocked(ctx context.Context) pkgaudio.OutcomeResult {
	if !s.muted {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeGain, Reason: "not muted"}
	}
	s.muted = false
	s.persistBestEffortLocked("state change")
	return s.applyEffectiveGainLocked(ctx)
}

// duckLowerPriority runs after a session with role duckerRole and mix
// policy Duck reaches Playing: it ducks every OTHER currently Playing
// session whose role priority is strictly lower, skipping a session
// already ducked by someone else. It is called with no session lock
// held — each target's own mu is acquired and released in turn, one
// session at a time, so this can never hold two sessions' locks
// simultaneously (the deadlock a duck and a counter-duck racing each
// other would otherwise risk).
func (m *Manager) duckLowerPriority(ctx context.Context, duckerID pkgaudio.SessionID, duckerRole pkgaudio.SourceRole) {
	for _, t := range m.otherSessions(duckerID) {
		t.mu.Lock()
		if t.state == pkgaudio.StatePlaying {
			var role pkgaudio.SourceRole
			if t.desired.SourceRole != nil {
				role = *t.desired.SourceRole
			}
			if pkgaudio.OutranksForMixing(duckerRole, role) {
				m.duckOneLocked(ctx, t, duckerID)
			}
		}
		t.mu.Unlock()
	}
}

// submitToActivePolicies is [Manager.duckLowerPriority]'s mirror image,
// run for a session that has just reached Playing: it finds every OTHER
// currently-Playing session whose role outranks this one and whose mix
// policy is Duck or Interrupt, and applies that policy to the session
// that just started.
//
// Without it, mixing would be resolved only at the moment a ducker
// starts, so a lower-priority session starting UNDER a playing
// announcement would never be ducked at all. That is not a corner case:
// the night controller's own enterResting cue list runs before its
// background-audio tick, and background audio needs several ticks to
// reach Playing, so an enterResting announcement is normally already
// playing when the bed starts.
//
// Same no-lock-held-on-entry discipline as [Manager.duckLowerPriority],
// and for the same reason: every other session's state is read under its
// own lock, released before this session's own lock is taken, so no two
// session locks are ever held at once.
func (m *Manager) submitToActivePolicies(ctx context.Context, id pkgaudio.SessionID, role pkgaudio.SourceRole) {
	var duckers, interrupters []pkgaudio.SessionID
	for _, o := range m.otherSessions(id) {
		o.mu.Lock()
		if o.state == pkgaudio.StatePlaying && o.desired.MixPolicy != nil {
			var otherRole pkgaudio.SourceRole
			if o.desired.SourceRole != nil {
				otherRole = *o.desired.SourceRole
			}
			if pkgaudio.OutranksForMixing(otherRole, role) {
				switch *o.desired.MixPolicy {
				case pkgaudio.MixPolicyDuck:
					duckers = append(duckers, o.id)
				case pkgaudio.MixPolicyInterrupt:
					interrupters = append(interrupters, o.id)
				}
			}
		}
		o.mu.Unlock()
	}
	if len(duckers) == 0 && len(interrupters) == 0 {
		return
	}
	s, ok := m.get(id)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-checked under s's own lock: everything above ran with s
	// unlocked, and a session that has already left Playing in the
	// meantime must not be ducked or suspended on the strength of stale
	// evidence.
	if s.state != pkgaudio.StatePlaying {
		return
	}
	// Duck before interrupt: a session that is both ducked and suspended
	// must come back at the ducked gain the still-playing ducker owns,
	// not at the gain it had before either applied.
	for _, duckerID := range duckers {
		m.duckOneLocked(ctx, s, duckerID)
	}
	for _, interrupterID := range interrupters {
		m.interruptOneLocked(ctx, s, interrupterID)
	}
}

// duckOneLocked adds duckerID to t's set of active duckers and applies
// the resulting effective gain, the configured duck depth, whether this
// is the first ducker or one more added on top of an existing set. A target
// already ducked by someone else just gains a second member; the actual
// gain does not move again until the set is empty, which is
// [Manager.removeDuckerLocked]'s own job (two overlapping announcements
// must not let the first one to stop restore background gain out from
// under the second). Caller holds t.mu.
func (m *Manager) duckOneLocked(ctx context.Context, t *Session, duckerID pkgaudio.SessionID) {
	if _, already := t.duckedByAll[duckerID]; already {
		return
	}
	if t.duckedByAll == nil {
		t.duckedByAll = make(map[pkgaudio.SessionID]struct{}, 1)
	}
	t.duckedByAll[duckerID] = struct{}{}
	m.applyEffectiveGainBestEffortLocked(ctx, t)
	t.persistBestEffortLocked("state change")
}

// restoreDucked runs after a ducking session leaves Playing (stop,
// clear, or natural completion): it removes duckerID from every other
// session's ducking set, restoring gain only for a session that has no
// duckers left. Same no-lock-held-on-entry discipline as
// [Manager.duckLowerPriority].
func (m *Manager) restoreDucked(ctx context.Context, duckerID pkgaudio.SessionID) {
	for _, t := range m.otherSessions(duckerID) {
		t.mu.Lock()
		m.removeDuckerLocked(ctx, t, duckerID)
		t.mu.Unlock()
	}
}

// removeDuckerLocked removes duckerID from t's ducking set, or does
// nothing when duckerID is not a member — that membership check is the
// entire exactly-once guarantee, since it is the same check
// [Manager.restoreOne] runs on a crash-recovered session, and there is
// exactly one code path either caller runs. Once the set is empty, the
// resulting effective gain is applied: the configured gain, re-clamped
// to the ceiling in force right now, if t is not muted, or nothing
// driven to the engine at all if it is, releasing a duck must never
// make a muted session audible, and mute's own eventual unmute reads the
// configured gain fresh rather than a value this function would
// otherwise have to hand it. Caller holds t.mu.
func (m *Manager) removeDuckerLocked(ctx context.Context, t *Session, duckerID pkgaudio.SessionID) {
	if _, ok := t.duckedByAll[duckerID]; !ok {
		return
	}
	delete(t.duckedByAll, duckerID)
	if len(t.duckedByAll) > 0 {
		t.persistBestEffortLocked("state change")
		return
	}
	m.applyEffectiveGainBestEffortLocked(ctx, t)
	t.persistBestEffortLocked("state change")
}

// otherSessions returns every live session except exclude, snapshotted
// under m.mu so the subsequent per-session locking in
// [Manager.duckLowerPriority]/[Manager.restoreDucked] never holds m.mu
// and a session's mu at once.
func (m *Manager) otherSessions(exclude pkgaudio.SessionID) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		if id == exclude {
			continue
		}
		out = append(out, s)
	}
	return out
}

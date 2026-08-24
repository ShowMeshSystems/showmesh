package audio

import (
	"context"
	"fmt"
	"math"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// duckTargetGain is what a ducked session's gain is set to. SHOWMESH
// HYPOTHESIS, NOT MEASURED: full silence is the simplest correct
// starting behavior; RES-007's bench is what should decide whether a
// partial duck is audibly preferable.
const duckTargetGain = pkgaudio.Gain(0)

// effectiveGainLocked returns s's current desired gain, or unity when
// none has ever been set. Caller holds s.mu.
func (s *Session) effectiveGainLocked() pkgaudio.Gain {
	if s.desired.Gain != nil {
		return *s.desired.Gain
	}
	return pkgaudio.Gain(1)
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

// GainSet is audio.gain.set: it changes id's gain immediately, clamped
// to its ceiling (ruling: the ceiling is enforced at the point gain
// takes effect, on every path, not only at validation).
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

// setGainLocked clamps requested to s's ceiling, applies it to the
// engine when a handle is loaded, and always records the clamped value
// as s's desired gain even with no handle loaded — matching Apply's own
// gain-without-playback semantics. Caller holds s.mu.
func (s *Session) setGainLocked(ctx context.Context, requested pkgaudio.Gain) pkgaudio.OutcomeResult {
	result, err := s.clampToCeilingLocked(requested)
	if err != nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: err.Error()}
	}
	effective := result.Effective
	s.desired.Gain = &effective
	s.rememberIntendedGainWhileSuppressedLocked(effective)
	s.persistBestEffortLocked("state change")

	if !s.handleLoaded {
		reason := ""
		if result.Clamped {
			reason = fmt.Sprintf("gain clamped to ceiling: requested %v, effective %v", result.Requested, result.Effective)
		}
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeGain, Reason: reason}
	}

	dispatchedAt := s.mgr.now()
	obs, err := s.mgr.engine.SetGain(ctx, s.handle, effective)
	if err != nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()}
	}
	if obs.ObservedAt.Before(dispatchedAt) {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: "engine evidence predates this dispatch"}
	}
	if obs.Gain != effective {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: fmt.Sprintf("engine reports gain %v, expected %v", obs.Gain, effective)}
	}
	reason := ""
	if result.Clamped {
		reason = fmt.Sprintf("gain clamped to ceiling: requested %v, effective %v", result.Requested, result.Effective)
	}
	return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeGain, Reason: reason}
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

func (s *Session) startFadeLocked(ctx context.Context, invocation pkgaudio.InvocationID, curve pkgaudio.FadeCurve, duration time.Duration, target pkgaudio.Gain) pkgaudio.OutcomeResult {
	result, err := s.clampToCeilingLocked(target)
	if err != nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: err.Error()}
	}
	fade := pkgaudio.Fade{Curve: curve, Duration: duration, TargetGain: result.Effective}
	if err := fade.Validate(); err != nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: err.Error()}
	}
	if !s.handleLoaded {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "no active playback to fade"}
	}

	f := fade
	s.desired.Fade = &f
	s.rememberIntendedGainWhileSuppressedLocked(result.Effective)
	s.fadePending = true
	s.fadeInvocation = invocation
	s.fadeHandleNeverFaded = false
	s.fadeState = FadeStateInProgress
	s.persistBestEffortLocked("state change")

	dispatchedAt := s.mgr.now()
	obs, err := s.mgr.engine.Fade(ctx, s.handle, fade)
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
// per dispatched fade, resolves s.fadePending and s.desired.Gain from a
// FadeActive transition true-to-false — never from fade.Duration having
// elapsed. It also writes the fade's terminal outcome back onto the
// invocation that dispatched it: [pkgaudio.OutcomeFadeComplete] when the
// engine's own evidence shows the target gain actually reached,
// [pkgaudio.OutcomeUnconfirmable] otherwise, gated through
// [Manager.gateAvailability] exactly as every other outcome in this
// package is — this is the reachable half of "declared in the
// vocabulary": the value becomes OutcomeFadeComplete the moment a real
// Engine reports the gain reached, not before. Caller holds s.mu.
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

	target := pkgaudio.Gain(0)
	if s.desired.Fade != nil {
		target = s.desired.Fade.TargetGain
	}
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

	gain := obs.Gain
	s.desired.Gain = &gain
	s.fadePending = false
	s.fadeInvocation = ""
	s.fadeState = FadeStateComplete
	s.persistBestEffortLocked("state change")
}

// resolveFadeInterruptedByRestartLocked answers a fade that survived a
// restart onto a handle never driven through it. desired.Gain is left as
// restored rather than read back from that handle. Caller holds s.mu.
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

// Mute is audio.output.mute: it saves id's current gain and drives its
// gain to zero. Idempotent — muting an already-muted session reports the
// existing mute rather than re-saving over it, so a repeated mute can
// never lose the original pre-mute gain.
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
	prior := s.preSuppressionGainLocked()
	s.preMuteGain = &prior
	s.muted = true
	return s.applyGainForMuteLocked(ctx, duckTargetGain)
}

// Unmute is audio.output.unmute: it restores id's pre-mute gain,
// re-clamped to whatever ceiling is current now (a ceiling change while
// muted must not be bypassed by the restore). Idempotent — unmuting an
// unmuted session is a no-op success, never a refusal.
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
	restore := pkgaudio.Gain(1)
	if s.preMuteGain != nil {
		restore = *s.preMuteGain
	}
	s.muted = false
	s.preMuteGain = nil
	// Mute and duck are independent reasons to be quieter than the
	// configured gain: unmuting while a duck is still active must land
	// on the duck's own level, because the announcement is still
	// playing, not on the gain mute was hiding underneath it.
	if len(s.duckedByAll) > 0 {
		restore = duckTargetGain
	}
	return s.applyGainForMuteLocked(ctx, restore)
}

// applyGainForMuteLocked is [Session.muteLocked] and [Session.
// unmuteLocked]'s shared tail: clamp to ceiling, persist the mute/unmute
// bookkeeping fields already mutated by the caller together with the new
// gain, and drive the engine when a handle is loaded.
func (s *Session) applyGainForMuteLocked(ctx context.Context, requested pkgaudio.Gain) pkgaudio.OutcomeResult {
	result, err := s.clampToCeilingLocked(requested)
	if err != nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: err.Error()}
	}
	effective := result.Effective
	s.desired.Gain = &effective
	s.persistBestEffortLocked("state change")

	if !s.handleLoaded {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeGain}
	}
	dispatchedAt := s.mgr.now()
	obs, err := s.mgr.engine.SetGain(ctx, s.handle, effective)
	if err != nil {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeFailed, Reason: err.Error()}
	}
	if obs.ObservedAt.Before(dispatchedAt) || obs.Gain != effective {
		return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: "engine evidence does not corroborate the requested gain"}
	}
	return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeGain}
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

// preSuppressionGainLocked returns the gain s would carry if neither mute
// nor duck were currently suppressing it: the currently-active
// suppression's own remembered restore target, or the live desired gain
// when neither is active. Mute is checked first because a session that is
// both muted and ducked always applies mute on top — see
// [Session.unmuteLocked] and [Manager.removeDuckerLocked].
//
// This exists to seed a NEWLY applied suppression's own restore memory:
// [Session.muteLocked] muting a session that is currently ducked, or
// [Manager.duckOneLocked] ducking a session that is currently muted, must
// each capture the gain the other suppression is already holding, not the
// already-suppressed live value — capturing the live value is the exact
// defect this composition exists to prevent (muting during a duck used to
// capture the duck level as preMuteGain, so unmuting restored the ducked
// level forever instead of the configured gain once the duck also
// released). Caller holds s.mu.
func (s *Session) preSuppressionGainLocked() pkgaudio.Gain {
	if s.muted && s.preMuteGain != nil {
		return *s.preMuteGain
	}
	if len(s.duckedByAll) > 0 && s.preDuckGain != nil {
		return *s.preDuckGain
	}
	return s.effectiveGainLocked()
}

// rememberIntendedGainWhileSuppressedLocked keeps a muted and/or ducked
// session's restore targets in step with the newest gain anyone has
// actually asked for.
//
// [Manager.removeDuckerLocked] and [Session.unmuteLocked] return a
// session to preDuckGain/preMuteGain, each captured once, at the moment
// mute or the first ducker arrived. Without this, a gain change that
// lands DURING a duck or a mute moved the session's desired gain and was
// then silently replayed away by the restore, putting the session back at
// whatever it held before. That is not merely a lost setting: a bed whose
// configured gain never reached this node before the duck sits at the
// default of unity, and the restore would put it back there, playing
// louder than the configured maximum until something else changed it. A
// failure path is exactly when that happens, because a failure path is
// what makes a caller retry a gain step late.
//
// Only the restore targets move here. Whether a gain change should also
// be allowed to drive the engine out of a duck or a mute while either is
// still active is a separate question about what audio.gain.set means
// mid-suppression, deliberately not decided here. Caller holds s.mu.
func (s *Session) rememberIntendedGainWhileSuppressedLocked(effective pkgaudio.Gain) {
	if len(s.duckedByAll) > 0 {
		g := effective
		s.preDuckGain = &g
	}
	if s.muted {
		g := effective
		s.preMuteGain = &g
	}
}

// duckOneLocked adds duckerID to t's set of active duckers. t's gain is
// only ever captured and driven to [duckTargetGain] on the transition
// from zero duckers to one — a target already ducked by someone else
// just gains a second member, so a later removal of one ducker does not
// restore a target the other ducker is still legitimately suppressing
// (two overlapping announcements must not let the first one
// to stop restore background gain out from under the second). Caller
// holds t.mu.
func (m *Manager) duckOneLocked(ctx context.Context, t *Session, duckerID pkgaudio.SessionID) {
	if _, already := t.duckedByAll[duckerID]; already {
		return
	}
	if len(t.duckedByAll) == 0 {
		prior := t.preSuppressionGainLocked()
		t.preDuckGain = &prior
		t.desired.Gain = ptrGain(duckTargetGain)
		if t.handleLoaded {
			if _, err := m.engine.SetGain(ctx, t.handle, duckTargetGain); err != nil {
				m.logf("audio session %s: duck gain set failed: %v", t.id, err)
			}
		}
	}
	if t.duckedByAll == nil {
		t.duckedByAll = make(map[pkgaudio.SessionID]struct{}, 1)
	}
	t.duckedByAll[duckerID] = struct{}{}
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
// exactly one code path either caller runs. t's gain and preDuckGain are
// only restored once the set becomes empty: any remaining ducker still
// legitimately owns t's suppressed gain. Caller holds t.mu.
func (m *Manager) removeDuckerLocked(ctx context.Context, t *Session, duckerID pkgaudio.SessionID) {
	if _, ok := t.duckedByAll[duckerID]; !ok {
		return
	}
	delete(t.duckedByAll, duckerID)
	if len(t.duckedByAll) > 0 {
		t.persistBestEffortLocked("state change")
		return
	}
	restore := pkgaudio.Gain(1)
	if t.preDuckGain != nil {
		restore = *t.preDuckGain
	}
	t.preDuckGain = nil

	if t.muted {
		// Mute is still active: the duck's own memory of the configured
		// gain becomes mute's restore target, but nothing is driven
		// audibly until unmute — releasing a duck must never make a
		// muted session audible.
		g := restore
		t.preMuteGain = &g
		t.persistBestEffortLocked("state change")
		return
	}

	result, err := t.clampToCeilingLocked(restore)
	effective := restore
	if err == nil {
		effective = result.Effective
	}
	t.desired.Gain = &effective
	if t.handleLoaded {
		if _, err := m.engine.SetGain(ctx, t.handle, effective); err != nil {
			m.logf("audio session %s: duck restore gain set failed: %v", t.id, err)
		}
	}
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

func ptrGain(g pkgaudio.Gain) *pkgaudio.Gain { return &g }

package api

import (
	"context"
	"errors"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// An announcement cue's own duck/mix/interrupt orchestration around
// resting.backgroundAudio (RESTING-MODE.md section 8, AUDIO-ENGINE.md
// section 9), called from nightAdvanceCueList around its own ordinary
// per-cue dispatch (nightRunCue, nightcuerun.go) for every cue whose Role
// is [config.NightSessionCueRoleAnnouncement]. It never replaces that
// dispatch; it only makes room for it beforehand (duck or interrupt-
// suspend) and restores or lets background audio resume afterward.
//
// "mix" needs neither step: the announcement's own target session
// declares its own mix policy, and background audio is left untouched.
//
// Durability and revision-sharing both come from nightbackgroundaudio.go
// unchanged: every step here goes through nightRunAudioCommand with a
// phase under the nightPhaseRestingBackground family, so it shares that
// session's ONE revision counter and its own commit-then-dispatch/crash-
// hook discipline. A duck step and its own restore are the SAME
// mechanism the cue outbox already proves for cues, applied to gain.set/
// gain.fade instead of a cue's own action.
//
// Restore retries under a fresh attempt whenever it resolves without
// confirming, so a refused or failed restore is visible (a new row, not
// a silently final one) and eventually lands rather than stranding the
// bed at duck gain for the rest of the night - the defect class this
// seam exists to close.

func nightAnnouncementPolicy(cue config.NightSessionCue, payload config.NightSessionPayload) string {
	if cue.AnnouncementPolicy != nil {
		return *cue.AnnouncementPolicy
	}
	if payload.AnnouncementDefaultPolicy != "" {
		return payload.AnnouncementDefaultPolicy
	}
	return config.NightSessionAnnouncementPolicyDefault
}

// nightAdvanceAnnouncementDuck runs BEFORE the announcement cue's own
// dispatch: for "duck", fades background audio down; for "interrupt",
// pauses or stops it per resume policy (nightBackgroundSuspendKind), and
// nightAdvanceBackgroundAudio's own interrupt gate restarts it once this
// announcement cue's own row resolves; for "mix", a no-op.
func (h *handlers) nightAdvanceAnnouncementDuck(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue, payload config.NightSessionPayload) {
	ba := payload.Resting.BackgroundAudio
	if ba == nil {
		return
	}
	policy := nightAnnouncementPolicy(cue, payload)
	if policy == config.NightSessionAnnouncementPolicyMix {
		return
	}

	nodeID := ba.OutputNodeID()
	sessionID := nightBackgroundAudioSessionID(rec)
	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: announcement: failed to read background-audio history", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		return
	}
	pbHistory := nightBackgroundAudioPlaybackHistory(history)
	// Never duck or interrupt a background session that was never
	// started, or is already suspended: there is nothing to make room
	// for.
	if len(pbHistory) == 0 {
		return
	}
	if latest := pbHistory[len(pbHistory)-1]; latest.Row.State == nightCueStateResolved && latest.Row.Outcome == nightCueOutcomeConfirmed {
		switch latest.Step.Kind {
		case nightBGStepPause, nightBGStepStop, nightBGStepInterruptPause, nightBGStepInterruptStop:
			return
		}
	}

	revision := nightNextBackgroundAudioRevision(history)
	switch policy {
	case config.NightSessionAnnouncementPolicyDuck:
		gain, _ := nightBackgroundCeilingGain(ba.MaxGainDb)
		duckGain := float64(gain) * nightAnnouncementDuckFraction
		target := nightAudioTarget(nodeID, sessionID, "audio.gain.fade", map[string]any{
			"targetGain": duckGain, "durationMs": nightAnnouncementFadeMs, "curve": string(pkgaudio.FadeCurveLinear),
		})
		phase := nightPhaseAnnouncementDuck + ":" + cuePhase
		if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cue.Name, target, revision, history); err != nil {
			h.logWarn("night loop: announcement: duck failed", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		}
	case config.NightSessionAnnouncementPolicyInterrupt:
		kind := nightBackgroundSuspendKind(ba.Resume)
		action := "audio.session.stop"
		interruptKind := nightBGStepInterruptStop
		if kind == nightBGStepPause {
			action = "audio.session.pause"
			interruptKind = nightBGStepInterruptPause
		}
		// Attempt 1: the announcement's own name is the row's cueName,
		// UNCHANGED across any later retry attempt
		// (nightBackgroundAudioResuspend), so nightAdvanceBackgroundAudio's
		// own gate can always find this exact announcement cue's row by
		// name, never by parsing a combined string.
		phase := nightInterruptPhase(interruptKind, cuePhase, 1)
		target := nightAudioTarget(nodeID, sessionID, action, map[string]any{})
		if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cue.Name, target, revision, history); err != nil {
			h.logWarn("night loop: announcement: interrupt suspend failed", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		}
	}
}

// nightAdvanceAnnouncementRestore runs AFTER the announcement cue's own
// dispatch, every tick, until a restore attempt confirms. For
// "interrupt" there is nothing to do here: nightAdvanceBackgroundAudio's
// own interrupt gate already resumes/restarts background audio once this
// announcement cue's own row resolves (nightbackgroundaudio.go). For
// "duck", this durably restores gain to the configured ceiling once BOTH
// the duck step and the announcement cue's own row are resolved -
// unconditionally, regardless of either one's own outcome - and RETRIES
// under a new attempt whenever an attempt resolves without confirming,
// so a refused or failed restore is visible and eventually lands rather
// than being silently treated as done.
func (h *handlers) nightAdvanceAnnouncementRestore(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue, payload config.NightSessionPayload) {
	ba := payload.Resting.BackgroundAudio
	if ba == nil {
		return
	}
	policy := nightAnnouncementPolicy(cue, payload)
	if policy != config.NightSessionAnnouncementPolicyDuck {
		return
	}

	duckPhase := nightPhaseAnnouncementDuck + ":" + cuePhase
	duckRow, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, duckPhase, cue.Name)
	if err != nil || duckRow.State != nightCueStateResolved {
		return // never ducked (background audio was not running), or the duck itself has not resolved yet.
	}
	cueRow, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, cuePhase, cue.Name)
	if err != nil {
		return
	}
	// Ambiguous is ALSO terminal (nightcue.go's own vocabulary: "never
	// retried automatically"), not merely "not yet resolved" - an
	// announcement that lands ambiguous must still release the bed, or
	// the exact stranding defect this seam exists to close happens again
	// under a different outcome name.
	if cueRow.State != nightCueStateResolved && cueRow.State != nightCueStateAmbiguous {
		return // the announcement itself has not reached a terminal state yet.
	}

	nodeID := ba.OutputNodeID()
	sessionID := nightBackgroundAudioSessionID(rec)
	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: announcement: failed to read background-audio history for restore", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		return
	}

	// Find the current attempt: the first attempt number (starting at 1)
	// whose row either does not exist yet, is still in flight, or is
	// resolved but not confirmed - the LATEST such attempt is the one to
	// act on. A confirmed attempt at any number means the restore is
	// done.
	for attempt := 1; attempt <= nightMaxAnnouncementRestoreAttempts; attempt++ {
		phase := nightAnnouncementRestorePhase(cuePhase, attempt)
		row, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cue.Name)
		switch {
		case errors.Is(err, store.ErrNightCueOutboxNotFound):
			_, ceiling := nightBackgroundCeilingGain(ba.MaxGainDb)
			revision := nightNextBackgroundAudioRevision(history)
			target := nightAudioTarget(nodeID, sessionID, "audio.gain.fade", map[string]any{
				"targetGain": float64(ceiling), "durationMs": nightAnnouncementFadeMs, "curve": string(pkgaudio.FadeCurveLinear),
			})
			if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cue.Name, target, revision, history); err != nil {
				h.logWarn("night loop: announcement: restore failed", "sessionId", rec.ID, "cue", cue.Name, "attempt", attempt, "error", err)
			}
			return
		case err != nil:
			h.logWarn("night loop: announcement: failed to read restore attempt row", "sessionId", rec.ID, "cue", cue.Name, "attempt", attempt, "error", err)
			return
		case row.State == nightCueStatePending || row.State == nightCueStateDispatched:
			_, ceiling := nightBackgroundCeilingGain(ba.MaxGainDb)
			target := nightAudioTarget(nodeID, sessionID, "audio.gain.fade", map[string]any{
				"targetGain": float64(ceiling), "durationMs": nightAnnouncementFadeMs, "curve": string(pkgaudio.FadeCurveLinear),
			})
			if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cue.Name, target, row.ActionRevision, history); err != nil {
				h.logWarn("night loop: announcement: restore resume failed", "sessionId", rec.ID, "cue", cue.Name, "attempt", attempt, "error", err)
			}
			return
		case row.State == nightCueStateResolved && row.Outcome == nightCueOutcomeConfirmed:
			return // this attempt landed; done.
		default:
			// Resolved without confirming (refused, failed, unconfirmable,
			// or ambiguous): visible in this attempt's own row, and the
			// loop continues to mint the NEXT attempt rather than treating
			// this as final.
			continue
		}
	}
	h.logWarn("night loop: announcement: restore exhausted its retry budget without confirming", "sessionId", rec.ID, "cue", cue.Name, "attempts", nightMaxAnnouncementRestoreAttempts)
}

const (
	// nightAnnouncementDuckFraction and nightAnnouncementFadeMs are
	// SHOWMESH HYPOTHESES, NOT MEASURED, matching this codebase's own
	// convention for a constant no bench data yet justifies (compare
	// audioCommandConfirmDeadline's identical posture, audiodispatch.go).
	nightAnnouncementDuckFraction = 0.25
	nightAnnouncementFadeMs       = 500.0
)

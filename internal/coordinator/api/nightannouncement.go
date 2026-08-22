package api

import (
	"context"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// An announcement cue's own duck/mix/interrupt orchestration around
// resting.backgroundAudio (RESTING-MODE.md §8, AUDIO-ENGINE.md §9),
// called from nightAdvanceCueList around its own ordinary per-cue
// dispatch (nightRunCue, nightcuerun.go) for every cue whose Role is
// [config.NightSessionCueRoleAnnouncement]. It never replaces that
// dispatch; it only makes room for it beforehand (duck or stop) and
// restores afterward (restore or restart) when backgroundAudio is
// configured at all.
//
// "mix" needs neither step: the announcement's own target session
// declares its own mix policy, and background audio is left untouched.
//
// Durability is the SAME night_cue_outbox reuse nightbackgroundaudio.go
// establishes: a duck/interrupt-stop step and its own restore/restart are
// each their own durable, resumable step under [nightPhaseAnnouncement],
// scoped by the announcement cue's own (phase, name) so two different
// announcement cues can never collide. Restore is UNCONDITIONAL: it is
// attempted once the duck step AND the announcement cue's own row are
// both resolved, regardless of either one's outcome - a failed duck or a
// failed announcement must never leave background audio stranded at duck
// gain for the rest of the night, which is exactly the defect class this
// seam's own report names.

func nightAnnouncementPolicy(cue config.NightSessionCue, payload config.NightSessionPayload) string {
	if cue.AnnouncementPolicy != nil {
		return *cue.AnnouncementPolicy
	}
	if payload.AnnouncementDefaultPolicy != "" {
		return payload.AnnouncementDefaultPolicy
	}
	return config.NightSessionAnnouncementPolicyDefault
}

func nightAnnouncementDuckCueName(cuePhase, cueName string) string {
	return "ann-duck-" + cuePhase + "-" + cueName
}
func nightAnnouncementRestoreCueName(cuePhase, cueName string) string {
	return "ann-restore-" + cuePhase + "-" + cueName
}

// nightAdvanceAnnouncementDuck runs BEFORE the announcement cue's own
// dispatch: for "duck", fades background audio down; for "interrupt",
// stops it outright (gated, on the way back, by
// nightAdvanceBackgroundAudio's own interrupt-stop check); for "mix", a
// no-op.
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
	// Never duck or interrupt a background session that was never
	// started, or is already stopped: there is nothing to make room for.
	if len(history) == 0 {
		return
	}
	if latest := history[len(history)-1]; latest.Step.Kind == "stop" && latest.Row.State == nightCueStateResolved {
		return
	}

	revision := nightNextBackgroundAudioRevision(history)
	switch policy {
	case config.NightSessionAnnouncementPolicyDuck:
		gain, _ := nightBackgroundCeilingGain(ba.MaxGainDb)
		duckGain := float64(gain) * nightAnnouncementDuckFraction
		target := nightAudioTarget(nodeID, sessionID, "audio.gain.fade", map[string]any{
			"targetGain": duckGain, "durationMs": nightAnnouncementFadeMs, "curve": string(pkgaudio.FadeCurveLinear),
		})
		if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseAnnouncement, nightAnnouncementDuckCueName(cuePhase, cue.Name), target, revision, history); err != nil {
			h.logWarn("night loop: announcement: duck failed", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		}
	case config.NightSessionAnnouncementPolicyInterrupt:
		target := nightAudioTarget(nodeID, sessionID, "audio.session.stop", map[string]any{})
		cueName := nightBackgroundAudioCueNameInterruptStop(int(revision), cuePhase, cue.Name)
		if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseAnnouncement, cueName, target, revision, history); err != nil {
			h.logWarn("night loop: announcement: interrupt stop failed", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		}
	}
}

// nightAdvanceAnnouncementRestore runs AFTER the announcement cue's own
// dispatch, every tick, until it commits its own restore step. For
// "interrupt" there is nothing to do here: nightAdvanceBackgroundAudio's
// own interrupt-stop gate already restarts background audio once this
// announcement cue's own row resolves (nightbackgroundaudio.go). For
// "duck", this durably restores gain to the configured ceiling once BOTH
// the duck step and the announcement cue's own row are resolved -
// unconditionally, regardless of either one's own outcome.
func (h *handlers) nightAdvanceAnnouncementRestore(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue, payload config.NightSessionPayload) {
	ba := payload.Resting.BackgroundAudio
	if ba == nil {
		return
	}
	policy := nightAnnouncementPolicy(cue, payload)
	if policy != config.NightSessionAnnouncementPolicyDuck {
		return
	}

	duckRow, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncement, nightAnnouncementDuckCueName(cuePhase, cue.Name))
	if err != nil || duckRow.State != nightCueStateResolved {
		return // never ducked (background audio was not running), or the duck itself has not resolved yet.
	}
	cueRow, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, cuePhase, cue.Name)
	if err != nil || cueRow.State != nightCueStateResolved {
		return // the announcement itself has not resolved yet.
	}

	nodeID := ba.OutputNodeID()
	sessionID := nightBackgroundAudioSessionID(rec)
	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: announcement: failed to read background-audio history for restore", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		return
	}
	_, ceiling := nightBackgroundCeilingGain(ba.MaxGainDb)
	revision := nightNextBackgroundAudioRevision(history)
	target := nightAudioTarget(nodeID, sessionID, "audio.gain.fade", map[string]any{
		"targetGain": float64(ceiling), "durationMs": nightAnnouncementFadeMs, "curve": string(pkgaudio.FadeCurveLinear),
	})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseAnnouncement, nightAnnouncementRestoreCueName(cuePhase, cue.Name), target, revision, history); err != nil {
		h.logWarn("night loop: announcement: restore failed", "sessionId", rec.ID, "cue", cue.Name, "error", err)
	}
}

const (
	// nightAnnouncementDuckFraction and nightAnnouncementFadeMs are
	// SHOWMESH HYPOTHESES, NOT MEASURED, matching this codebase's own
	// convention for a constant no bench data yet justifies (compare
	// audioCommandConfirmDeadline's identical posture, audiodispatch.go).
	nightAnnouncementDuckFraction = 0.25
	nightAnnouncementFadeMs       = 500.0
)

package api

import (
	"context"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
)

// The write that makes a night lighting cue's fade actually happen, and
// the outcome text the outbox row carries for it.

// nightTransitionGainWriter performs one transition-gain write against
// target's FPP instance. Only a test ever substitutes it; production
// leaves it nil and [handlers.writeNightTransitionGain] runs.
type nightTransitionGainWriter func(ctx context.Context, target config.ShowActionTarget, fade nightLightingFade, requestID string) (fppcommand.TransitionGainOutcome, error)

// nightLightingFadeForCue is [nightLightingFadeFor] with one added
// obligation: a lighting cue that authored a fade for a phase with no
// defined direction declines the write out loud. Absent, unknown and
// deliberately declined must stay tellable apart, and an author who gets
// silence never learns their fade did nothing.
func (h *handlers) nightLightingFadeForCue(cue config.NightSessionCue, phase string) *nightLightingFade {
	fade := nightLightingFadeFor(cue, phase)
	if fade == nil && cue.Role == config.NightSessionCueRoleLighting && cue.FadeDurationMs != nil {
		h.logWarn("night loop: lighting fade not performed because this phase has no defined fade direction",
			"cue", cue.Name, "phase", phase)
	}
	return fade
}

// writeNightTransitionGain resolves target.InstanceID against the same
// FPP inventory show.action binding checks read (currentFPPEndpoints) and
// writes the gain there.
func (h *handlers) writeNightTransitionGain(ctx context.Context, target config.ShowActionTarget, fade nightLightingFade, requestID string) (fppcommand.TransitionGainOutcome, error) {
	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		return fppcommand.TransitionGainOutcome{}, fmt.Errorf("api: listing fpp endpoints for the transition gain write: %w", err)
	}
	baseURL := ""
	for _, ep := range endpoints {
		if ep.ID == target.InstanceID {
			baseURL = ep.URL
			break
		}
	}
	if baseURL == "" {
		return fppcommand.TransitionGainOutcome{}, fmt.Errorf("api: fpp instance %q is not a configured FPP endpoint", target.InstanceID)
	}
	client, err := fppcommand.New(baseURL, fppcommand.Options{})
	if err != nil {
		return fppcommand.TransitionGainOutcome{}, fmt.Errorf("api: building the fpp client for the transition gain write: %w", err)
	}
	return client.SetTransitionGain(ctx, fade.TargetPercent, fade.FadeSeconds, requestID)
}

// nightGainResult is what one gain write leaves for the outbox row.
// note is always non-empty once a write was attempted; failed marks a
// write that did not take, so the row can never read as a clean success.
type nightGainResult struct {
	note   string
	failed bool
}

// nightApplyLightingFade performs fade's gain write for one cue and
// reports what to record.
//
// requestID is the cue's own [nightCueIdempotencyKey], the SAME string
// already dispatched as idemKey, and the two must never become
// independent values. That key derives from cue identity alone ("session
// + cycle + phase + cue", and its own doc comment already says it doubles
// as the FPP command's IdempotencyKey), so a resume after a coordinator
// restart regenerates exactly the same string; the plugin then treats the
// repeat as an idempotent no-op, returning applied:false with the gain
// unchanged rather than restarting the fade. A separately minted id would
// restart a fade every resume.
//
// This is a direct client call and not a dispatchable action on purpose:
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 2.1 makes the transition
// gain the coordinator's alone, and a registered FPP Action would be
// discoverable and schedulable in FPP's own UI, handing every operator
// and every schedule entry a way to fight the coordinator over one value.
// Do not "fix" this into an operation; that reintroduces the second
// writer.
func (h *handlers) nightApplyLightingFade(ctx context.Context, cueName string, target config.ShowActionTarget, fade nightLightingFade, requestID string) nightGainResult {
	if target.Integration != config.ShowActionIntegrationFPP {
		// The gain lives in the FPP plugin, so a cue whose action targets
		// anything else has nowhere to write it. No write, and the cue
		// still runs on its own action's outcome.
		h.logWarn("night loop: lighting fade not performed because this cue's action does not target an FPP instance",
			"cue", cueName, "integration", target.Integration)
		return nightGainResult{}
	}

	write := h.nightGainWriter
	if write == nil {
		write = h.writeNightTransitionGain
	}
	outcome, err := write(ctx, target, fade, requestID)
	if err != nil {
		return nightGainResult{
			note:   fmt.Sprintf("transition gain write to %d%% over %ds failed: %v", fade.TargetPercent, fade.FadeSeconds, err),
			failed: true,
		}
	}
	// Recorded on both paths. applied=false is an idempotent repeat of a
	// requestId already applied, which is the key working as designed on a
	// resumed transition; a row that stored nothing there would make the
	// resume look like it never acted.
	return nightGainResult{note: fmt.Sprintf(
		"transition gain to %d%% over %ds: applied=%t, ceiling %d, effective output %d",
		fade.TargetPercent, fade.FadeSeconds, outcome.Applied, outcome.Ceiling, outcome.EffectiveOutput)}
}

// nightCueReasonWith appends a gain note to a dispatch's own reason,
// keeping both readable rather than letting either overwrite the other.
func nightCueReasonWith(reason, note string) string {
	switch {
	case note == "":
		return reason
	case reason == "":
		return note
	default:
		return reason + "; " + note
	}
}

// nightCueOutcomeWithGainFailure downgrades an otherwise positive cue
// outcome when the gain write failed: a lighting cue whose fade did not
// happen did not do what it was authored to do, and reporting "confirmed"
// there would be reporting success from the action alone. An outcome that
// is already a specific negative (failed, refused, ambiguous) keeps its
// own more precise word.
func nightCueOutcomeWithGainFailure(outcome string) string {
	switch outcome {
	case nightCueOutcomeConfirmed, nightCueOutcomeUnconfirmed, nightCueOutcomeUnconfirmable:
		return nightCueOutcomeFailed
	default:
		return outcome
	}
}

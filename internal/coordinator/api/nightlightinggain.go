package api

import (
	"context"
	"fmt"
	"strings"
	"sync"

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

// nightGainResult is what one cue's gain writes leave for the outbox row.
// note is always non-empty once a write was attempted; failed marks any FPP
// whose write did not take, so the row can never read as a clean success.
type nightGainResult struct {
	note   string
	failed bool
	// accepted lists the FPPs whose write took.
	accepted []string
	// What the first accepted write reported. Logged, never shown.
	applied                  bool
	ceiling, effectiveOutput int
}

// nightGainRequestID is one FPP's requestId for a cue's gain write. It is
// derived from the cue's idempotency key alone, so a resume after a restart
// regenerates it and the plugin treats the repeat as a no-op.
func nightGainRequestID(cueKey, instanceID string) string {
	return cueKey + ":" + instanceID
}

// nightApplyLightingFade writes fade's gain to every FPP in fpp.endpoints
// (ADR-062 decision 7) and reports what to record. One FPP failing does not
// stop the others.
//
// requestID is the cue's own [nightCueIdempotencyKey]; each FPP's write
// carries [nightGainRequestID] of it, never a freshly minted id, which
// would restart a fade on every resume.
//
// This is a direct client call and not a dispatchable action on purpose:
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 2.1 makes the transition
// gain the coordinator's alone. Do not "fix" this into an operation; that
// reintroduces the second writer.
func (h *handlers) nightApplyLightingFade(ctx context.Context, cueName string, target config.ShowActionTarget, fade nightLightingFade, requestID string) nightGainResult {
	if target.Integration != config.ShowActionIntegrationFPP {
		// A cue whose action targets anything else is not a lighting fade
		// on FPP. No write, and the cue still runs on its own action's outcome.
		h.logWarn("night loop: lighting fade not performed because this cue's action does not target an FPP instance",
			"cue", cueName, "integration", target.Integration)
		return nightGainResult{}
	}
	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil || len(endpoints) == 0 {
		h.logWarn("night loop: lighting fade has no FPP to write to", "cue", cueName, "error", err)
		return nightGainResult{
			note:   fmt.Sprintf("The lights could not be faded to %d%% because no FPP player is configured. Check the FPP players in Settings.", fade.TargetPercent),
			failed: true,
		}
	}

	write := h.nightGainWriter
	if write == nil {
		write = h.writeNightTransitionGain
	}
	outcomes := make([]fppcommand.TransitionGainOutcome, len(endpoints))
	errs := make([]error, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := config.ShowActionTarget{Integration: config.ShowActionIntegrationFPP, InstanceID: ep.ID}
			outcomes[i], errs[i] = write(ctx, t, fade, nightGainRequestID(requestID, ep.ID))
		}()
	}
	wg.Wait()

	var res nightGainResult
	var failedOn []string
	for i, ep := range endpoints {
		if errs[i] != nil {
			h.logWarn("night loop: lighting fade could not be written", "cue", cueName, "instanceId", ep.ID, "targetPercent", fade.TargetPercent, "error", errs[i])
			failedOn = append(failedOn, ep.ID)
			continue
		}
		if len(res.accepted) == 0 {
			res.applied, res.ceiling, res.effectiveOutput = outcomes[i].Applied, int(outcomes[i].Ceiling), int(outcomes[i].EffectiveOutput)
		}
		res.accepted = append(res.accepted, ep.ID)
		h.logDebug("night loop: lighting fade written", "cue", cueName, "instanceId", ep.ID, "applied", outcomes[i].Applied, "ceiling", outcomes[i].Ceiling, "effectiveOutput", outcomes[i].EffectiveOutput)
	}
	if len(failedOn) > 0 {
		failed := strings.Join(failedOn, ", ")
		res.failed = true
		res.note = fmt.Sprintf("The lights on %s could not be faded to %d%%. Check the brightness on those players.", failed, fade.TargetPercent)
		if len(res.accepted) > 0 {
			res.note = fmt.Sprintf("The lights on %s could not be faded to %d%%, but %s took the fade. Check the brightness on %s.",
				failed, fade.TargetPercent, strings.Join(res.accepted, ", "), failed)
		}
		return res
	}
	// Recorded on a repeat too, so a resumed transition shows it acted.
	res.note = fmt.Sprintf("The lights are fading to %d%% over %d seconds.", fade.TargetPercent, fade.FadeSeconds)
	return res
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
		return reason + nightReasonNoteSeparator + note
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

// nightCueOutcomeWithGain is a resolved cue's outcome and stored reason once
// its fade is counted. The cue's own failure comes first; the fade's comes
// first only when the fade is what failed the cue.
func nightCueOutcomeWithGain(outcome, reason string, gain nightGainResult) (string, string) {
	if !gain.failed {
		return outcome, nightCueReasonWith(reason, gain.note)
	}
	withGain := nightCueOutcomeWithGainFailure(outcome)
	if withGain == outcome {
		return outcome, nightCueReasonWith(reason, gain.note)
	}
	return withGain, nightCueReasonWith(gain.note, reason)
}

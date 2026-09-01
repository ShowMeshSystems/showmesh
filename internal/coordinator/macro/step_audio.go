package macro

import (
	"context"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// dispatchAudioStep dispatches one audio-integration step through
// [api.AudioActionDispatcher.Dispatch] (via the audioActionDispatcher
// interface, macro.go) — the SAME in-process dispatch/confirm/audit core
// every audio.session.*/audio.gain.*/audio.output.* HTTP route and
// nightDispatchCueAudio (internal/coordinator/api/nightcue.go) already use.
// This package writes no audit entry of its own here: unlike an MQTT or
// Resolume step, [api.AudioActionDispatcher.Dispatch] already owns its full
// dispatch-then-outcome audit pair internally, matching dispatchFPPStep's
// identical "the seam already audits itself" posture one file over.
//
// action.Revision — the pinned show.action revision, never the live one —
// becomes pkg/audio's own Revision, exactly as nightDispatchCueAudio's own
// doc comment explains: pkg/audio's RevisionState.Apply refuses to apply a
// revision that does not strictly advance the session's own desired
// revision, so a delayed retry can never rewind a newer command that has
// already landed.
func (e *Executor) dispatchAudioStep(ctx context.Context, run store.MacroRunRecord, step store.MacroRunStepRecord, action resolvedAction, issuer api.FPPCommandIssuer) stepResult {
	target := action.Payload.Target
	if e.audioActions == nil {
		return stepResult{
			outcome:       outcomeFailed,
			outcomeState:  audioStateNotConfigured,
			outcomeReason: "no audio action dispatcher is configured on this coordinator",
			resolvedAt:    ptrTime(e.now()),
		}
	}

	stepKey := stepIdempotencyKey(run.ID, step.StepIndex)
	params := make(map[string]any, len(target.Params)+3)
	for k, v := range target.Params {
		params[k] = v
	}
	// An authored show.action carries its gain in decibels; the node
	// expects the linear multiplier it has always received — see
	// nightDispatchCueAudio's identical conversion at its own dispatch
	// point (internal/coordinator/api/nightcue.go).
	api.ConvertAuthoredAudioGainParams(target.AudioAction, params)
	params["sessionId"] = target.AudioSessionID
	params["invocationId"] = stepKey
	params["revision"] = uint64(action.Revision)

	audioNodeID := ""
	if len(target.AudioNodeIDs) > 0 {
		audioNodeID = target.AudioNodeIDs[0]
	}
	result, problem, err := e.audioActions.Dispatch(ctx, api.AudioDispatchInput{
		Action: target.AudioAction, NodeID: audioNodeID, SessionID: target.AudioSessionID,
		Params: params, Revision: uint64(action.Revision), IdempotencyKey: stepKey,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID, ClientAddr: issuer.ClientAddr,
	})
	var res stepResult
	if err != nil {
		res = stepResult{
			outcome:       outcomeFailed,
			outcomeState:  audioStateInternalError,
			outcomeReason: "this step could not be dispatched because of an internal coordinator error",
		}
	} else if problem != nil {
		res = stepResult{
			outcome:       outcomeFailed,
			outcomeState:  "refused",
			outcomeReason: problem.Detail,
		}
	} else {
		res = mapAudioSessionCommandResult(result)
	}
	if res.resolvedAt == nil {
		t := e.now()
		res.resolvedAt = &t
	}
	return res
}

// mapAudioSessionCommandResult translates one
// [v1.AudioSessionCommandResult] into this package's own five-value step
// vocabulary, mirroring mapResolumeActionResult's identical role one file
// over. outcomeState is the raw [pkgaudio.Outcome] wire word — vocab.go's
// own doc comment states why this is kept, not collapsed.
func mapAudioSessionCommandResult(result v1.AudioSessionCommandResult) stepResult {
	res := stepResult{
		outcomeState:  result.Outcome,
		outcomeReason: result.Reason,
		attrDegraded:  result.AttributionDegraded,
	}
	switch pkgaudio.Outcome(result.Outcome) {
	case pkgaudio.OutcomeStarted, pkgaudio.OutcomePosition, pkgaudio.OutcomeGain,
		pkgaudio.OutcomeFadeComplete, pkgaudio.OutcomeStopped, pkgaudio.OutcomeCompleted:
		res.outcome = outcomeConfirmed
	case pkgaudio.OutcomeUnconfirmable:
		res.outcome = outcomeUnconfirmable
	default:
		// Refused and every other/unrecognized word both land here,
		// mirroring mapResolumeActionResult's identical "refused is this
		// package's own failed" collapse one file over.
		res.outcome = outcomeFailed
	}

	if result.DispatchedAt != "" {
		if t, err := parseAudioResultTime(result.DispatchedAt); err == nil {
			res.dispatchedAt = &t
		}
	}
	if result.ResolvedAt != nil && *result.ResolvedAt != "" {
		if t, err := parseAudioResultTime(*result.ResolvedAt); err == nil {
			res.resolvedAt = &t
		}
	}
	return res
}

// parseAudioResultTime parses one of [v1.AudioSessionCommandResult]'s RFC
// 3339 timestamp fields, mirroring api.parseTime's identical layout (that
// function is unexported, and this package must not import api's internals
// beyond its own declared seams).
func parseAudioResultTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

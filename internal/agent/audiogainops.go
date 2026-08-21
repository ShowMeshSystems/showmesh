package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file wires the four remaining reserved audio.gain.*/audio.output.*
// operations into the agent's allowlist, against an [audio.Manager],
// exactly as audiosessionops.go wires the nine audio.session.*
// operations — see that file's doc comment for gateAvailability's role,
// unchanged here: nothing in this file can report a gain or mute command
// as succeeded playback while the wired Engine is unavailable.

// audioGainOperations builds the four allowlist entries against mgr.
// mgr is nil-safe at construction, matching audioSessionOperations's own
// nil-disables convention.
func audioGainOperations(mgr *audio.Manager) map[string]OperationFunc {
	return map[string]OperationFunc{
		string(pkgaudio.OperationGainSet):      sessionOp(mgr, gainSet),
		string(pkgaudio.OperationGainFade):     sessionOp(mgr, gainFade),
		string(pkgaudio.OperationOutputMute):   sessionOp(mgr, outputMute),
		string(pkgaudio.OperationOutputUnmute): sessionOp(mgr, outputUnmute),
	}
}

// gainSignalFor names the evidence signal a gain-affecting operation
// reports: audio_session.gain.ceiling when the ceiling actually clamped
// the value, audio_session.gain.effective otherwise — the ceiling is
// reported as its own signal only when it did something, per the
// standing rule that a clamp must be visible evidence, not a silently
// applied value.
func gainSignalFor(reason string) string {
	if reason != "" {
		return "audio_session.gain.ceiling"
	}
	return "audio_session.gain.effective"
}

func gainSet(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, params map[string]any) (pkgaudio.OutcomeResult, string, error) {
	raw, ok := params["gain"]
	if !ok {
		return pkgaudio.OutcomeResult{}, "", fmt.Errorf("audio.gain.set: params.gain is required")
	}
	f, ok := raw.(float64)
	if !ok {
		return pkgaudio.OutcomeResult{}, "", fmt.Errorf("audio.gain.set: params.gain must be a number, got %T", raw)
	}
	outcome := mgr.GainSet(ctx, id, inv, rev, pkgaudio.Gain(f))
	return outcome, gainSignalFor(outcome.Reason), nil
}

var audioGainFadeKnownKeys = map[string]bool{"targetGain": true, "durationMs": true, "curve": true}

func gainFade(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, params map[string]any) (pkgaudio.OutcomeResult, string, error) {
	body := map[string]any{}
	for k, v := range params {
		if audioSessionCommonKeys[k] {
			continue
		}
		body[k] = v
	}
	if err := rejectUnknownKeys("audio.gain.fade", body, audioGainFadeKnownKeys); err != nil {
		return pkgaudio.OutcomeResult{}, "", err
	}

	rawTarget, ok := body["targetGain"]
	if !ok {
		return pkgaudio.OutcomeResult{}, "", fmt.Errorf("audio.gain.fade: params.targetGain is required")
	}
	target, ok := rawTarget.(float64)
	if !ok {
		return pkgaudio.OutcomeResult{}, "", fmt.Errorf("audio.gain.fade: params.targetGain must be a number, got %T", rawTarget)
	}

	settings := mgr.SettingsSnapshot()

	durationMs := float64(settings.DefaultFadeDurationMs)
	if rawDuration, ok := body["durationMs"]; ok {
		durationMs, ok = rawDuration.(float64)
		if !ok || durationMs <= 0 {
			return pkgaudio.OutcomeResult{}, "", fmt.Errorf("audio.gain.fade: params.durationMs must be a positive number, got %v", rawDuration)
		}
	}

	curve := settings.DefaultFadeCurve
	if rawCurve, ok := body["curve"]; ok {
		v, ok := rawCurve.(string)
		if !ok || v == "" {
			return pkgaudio.OutcomeResult{}, "", fmt.Errorf("audio.gain.fade: params.curve must be a non-empty string, got %T", rawCurve)
		}
		curve = pkgaudio.FadeCurve(v)
	}

	outcome := mgr.GainFade(ctx, id, inv, rev, curve, time.Duration(durationMs)*time.Millisecond, pkgaudio.Gain(target))
	return outcome, "audio_session.fade.state", nil
}

func outputMute(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, error) {
	return mgr.Mute(ctx, id, inv, rev), "audio_session.mix.state", nil
}

func outputUnmute(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, error) {
	return mgr.Unmute(ctx, id, inv, rev), "audio_session.mix.state", nil
}

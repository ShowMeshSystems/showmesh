package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file wires the nine audio.session.* operations into the agent's
// allowlist, against an [audio.Manager]. The Manager's own
// gateAvailability forces every one of these to report Unconfirmable
// while the wired Engine is unavailable — true of every Engine this
// repository ships (see internal/agent/audio.FakeEngine) — so nothing
// here can report a session command as succeeded playback.

// audioSessionCommonKeys are the three params every session operation
// requires: sessionId names the session, invocationId and revision go
// through [pkgaudio.RevisionState], the session layer's own idempotency
// and anti-rewind ledger. invocationId is
// the caller's own stable identity for this logical intent — a caller
// SHOULD set it equal to the command envelope's own idempotency key, but
// this package does not enforce that convention structurally (see this
// file's doc comment on OperationFunc's signature not carrying the
// envelope).
var audioSessionCommonKeys = map[string]bool{"sessionId": true, "invocationId": true, "revision": true}

func parseAudioSessionCommon(action string, params map[string]any) (pkgaudio.SessionID, pkgaudio.InvocationID, pkgaudio.Revision, error) {
	rawSession, ok := params["sessionId"]
	if !ok {
		return "", "", 0, fmt.Errorf("%s: params.sessionId is required", action)
	}
	sessionID, ok := rawSession.(string)
	if !ok || sessionID == "" {
		return "", "", 0, fmt.Errorf("%s: params.sessionId must be a non-empty string, got %T", action, rawSession)
	}

	rawInv, ok := params["invocationId"]
	if !ok {
		return "", "", 0, fmt.Errorf("%s: params.invocationId is required", action)
	}
	invocation, ok := rawInv.(string)
	if !ok || invocation == "" {
		return "", "", 0, fmt.Errorf("%s: params.invocationId must be a non-empty string, got %T", action, rawInv)
	}

	rawRev, ok := params["revision"]
	if !ok {
		return "", "", 0, fmt.Errorf("%s: params.revision is required", action)
	}
	revF, ok := rawRev.(float64)
	if !ok || revF < 0 {
		return "", "", 0, fmt.Errorf("%s: params.revision must be a non-negative number, got %v", action, rawRev)
	}

	return pkgaudio.SessionID(sessionID), pkgaudio.InvocationID(invocation), pkgaudio.Revision(revF), nil
}

// audioSessionOperations builds the nine allowlist entries against mgr.
// mgr is nil-safe at construction (a node with no configured asset
// directory never wires audio session commands — see agent.go), matching
// render's identical nil-disables convention in newOperationRegistry.
func audioSessionOperations(mgr *audio.Manager) map[string]OperationFunc {
	return map[string]OperationFunc{
		string(pkgaudio.OperationSessionApply):   sessionOp(mgr, applySession),
		string(pkgaudio.OperationSessionPrepare): sessionOp(mgr, prepareSession),
		string(pkgaudio.OperationSessionStart):   sessionOp(mgr, startSession),
		string(pkgaudio.OperationSessionPause):   sessionOp(mgr, pauseSession),
		string(pkgaudio.OperationSessionResume):  sessionOp(mgr, resumeSession),
		string(pkgaudio.OperationSessionSeek):    sessionOp(mgr, seekSession),
		string(pkgaudio.OperationSessionAdvance): sessionOp(mgr, advanceSession),
		string(pkgaudio.OperationSessionStop):    sessionOp(mgr, stopSession),
		string(pkgaudio.OperationSessionClear):   sessionOp(mgr, clearSession),
	}
}

// sessionExec is one operation's body once sessionId/invocationId/revision
// are parsed and any operation-specific params are extracted.
type sessionExec func(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, params map[string]any) (pkgaudio.OutcomeResult, string, error)

// sessionOp is every session OperationFunc's shared shape: parse the
// common three params, run exec, and turn the resulting
// [pkgaudio.OutcomeResult] into an [OperationResult] whose Confirmed is
// true only for a genuine success outcome — Unconfirmable, Refused, and
// Failed all report Confirmed:false, matching OperationResult's own
// "read-back evidence corroborates the request" contract.
func sessionOp(mgr *audio.Manager, exec sessionExec) OperationFunc {
	return func(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
		if mgr == nil {
			return OperationResult{}, fmt.Errorf("audio session operations are not wired on this node (no asset directory configured)")
		}
		id, inv, rev, err := parseAudioSessionCommon("audio.session", params)
		if err != nil {
			return OperationResult{}, err
		}

		dispatchedAt := now()
		outcome, signal, err := exec(ctx, mgr, id, inv, rev, params)
		if err != nil {
			return OperationResult{}, err
		}
		if err := outcome.Validate(); err != nil {
			return OperationResult{}, fmt.Errorf("audio session operation produced an invalid outcome: %w", err)
		}

		confirmed := outcome.Outcome != pkgaudio.OutcomeRefused &&
			outcome.Outcome != pkgaudio.OutcomeFailed &&
			outcome.Outcome != pkgaudio.OutcomeUnconfirmable

		return OperationResult{
			Confirmed: confirmed,
			Signal:    signal,
			Value: map[string]any{
				"sessionId": string(id),
				"outcome":   string(outcome.Outcome),
				"reason":    outcome.Reason,
			},
			ExecutedAt: dispatchedAt,
			ObservedAt: now(),
		}, nil
	}
}

func applySession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, params map[string]any) (pkgaudio.OutcomeResult, string, error) {
	req, err := parseApplyRequest("audio.session.apply", params)
	if err != nil {
		return pkgaudio.OutcomeResult{}, "", err
	}
	return mgr.Apply(ctx, id, inv, rev, req), "node.audio_session.apply", nil
}

func prepareSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, error) {
	return mgr.Prepare(ctx, id, inv, rev), "node.audio_session.prepare", nil
}

func startSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, error) {
	return mgr.Start(ctx, id, inv, rev), "node.audio_session.start", nil
}

func pauseSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, error) {
	return mgr.Pause(ctx, id, inv, rev), "node.audio_session.pause", nil
}

func resumeSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, error) {
	return mgr.Resume(ctx, id, inv, rev), "node.audio_session.resume", nil
}

func seekSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, params map[string]any) (pkgaudio.OutcomeResult, string, error) {
	raw, ok := params["positionMs"]
	if !ok {
		return pkgaudio.OutcomeResult{}, "", fmt.Errorf("audio.session.seek: params.positionMs is required")
	}
	f, ok := raw.(float64)
	if !ok || f < 0 {
		return pkgaudio.OutcomeResult{}, "", fmt.Errorf("audio.session.seek: params.positionMs must be a non-negative number, got %v", raw)
	}
	position := time.Duration(f) * time.Millisecond
	return mgr.Seek(ctx, id, inv, rev, position), "node.audio_session.seek", nil
}

func advanceSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, error) {
	return mgr.Advance(ctx, id, inv, rev), "node.audio_session.advance", nil
}

func stopSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, error) {
	return mgr.Stop(ctx, id, inv, rev), "node.audio_session.stop", nil
}

func clearSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, error) {
	return mgr.Clear(ctx, id, inv, rev), "node.audio_session.clear", nil
}

// parseApplyRequest builds a [pkgaudio.ApplyRequest] from apply's own
// params, on top of the common sessionId/invocationId/revision.
// Supported fields: sourceRole, media (a MediaRef object, mirroring
// audio.media.probe's own field names), playlist (ownerKind, ownerId,
// ownerRevision, repeat, resume, requestedTransition, items — items reuse
// the exact object shape audio.media.probe's params.items already
// defines), outputs (a string array), and mixPolicy. media and playlist
// are mutually exclusive, matching [pkgaudio.SessionDesiredState.Validate].
// Gain, ceiling, fade, and bookmark are not wired here: the first three
// belong to the separate audio.gain.set/audio.gain.fade surface,
// and a bookmark is session-internal state this package manages itself
// (Pause writes one; nothing here accepts one from a caller).
// expiresInMs additionally refreshes the session's retirement deadline
// ([pkgaudio.SessionDesiredState.Expiry]) to this agent's own now() plus
// the given duration: a coordinator-stamped field an operator need not
// send.
var audioSessionApplyKnownKeys = map[string]bool{
	"sourceRole": true, "media": true, "playlist": true, "outputs": true,
	"ltcStartOffset": true, "mixPolicy": true, "expiresInMs": true,
}

func parseApplyRequest(action string, params map[string]any) (pkgaudio.ApplyRequest, error) {
	body := map[string]any{}
	for k, v := range params {
		if audioSessionCommonKeys[k] {
			continue
		}
		body[k] = v
	}
	if err := rejectUnknownKeys(action, body, audioSessionApplyKnownKeys); err != nil {
		return pkgaudio.ApplyRequest{}, err
	}

	var req pkgaudio.ApplyRequest

	if raw, ok := body["sourceRole"]; ok {
		v, ok := raw.(string)
		if !ok || v == "" {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.sourceRole must be a non-empty string, got %T", action, raw)
		}
		req.SourceRole = pkgaudio.SetField(pkgaudio.SourceRole(v))
	}

	if raw, ok := body["mixPolicy"]; ok {
		v, ok := raw.(string)
		if !ok || v == "" {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.mixPolicy must be a non-empty string, got %T", action, raw)
		}
		policy := pkgaudio.MixPolicy(v)
		if err := policy.Validate(); err != nil {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.mixPolicy: %w", action, err)
		}
		req.MixPolicy = pkgaudio.SetField(policy)
	}

	_, hasMedia := body["media"]
	_, hasPlaylist := body["playlist"]
	if hasMedia && hasPlaylist {
		return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.media and params.playlist are mutually exclusive", action)
	}

	if hasMedia {
		m, ok := body["media"].(map[string]any)
		if !ok {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.media must be an object, got %T", action, body["media"])
		}
		ref, err := parseMediaRef(action, m)
		if err != nil {
			return pkgaudio.ApplyRequest{}, err
		}
		req.Media = pkgaudio.SetField(ref)
	}

	if hasPlaylist {
		p, ok := body["playlist"].(map[string]any)
		if !ok {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.playlist must be an object, got %T", action, body["playlist"])
		}
		ref, err := parsePlaylistRef(action, p)
		if err != nil {
			return pkgaudio.ApplyRequest{}, err
		}
		req.Playlist = pkgaudio.SetField(ref)
	}

	if raw, ok := body["outputs"]; ok {
		list, ok := raw.([]any)
		if !ok {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.outputs must be an array, got %T", action, raw)
		}
		outputs := make([]string, 0, len(list))
		for i, o := range list {
			s, ok := o.(string)
			if !ok || s == "" {
				return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.outputs[%d] must be a non-empty string, got %T", action, i, o)
			}
			outputs = append(outputs, s)
		}
		req.Outputs = pkgaudio.SetField(outputs)
	}

	if raw, ok := body["ltcStartOffset"]; ok {
		v, ok := raw.(string)
		if !ok || v == "" {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.ltcStartOffset must be a non-empty HH:MM:SS:FF string, got %T", action, raw)
		}
		tc := pkgaudio.LTCTimecode(v)
		if err := tc.Validate(); err != nil {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.ltcStartOffset: %w", action, err)
		}
		req.LTCStartOffset = pkgaudio.SetField(tc)
	}

	if raw, ok := body["expiresInMs"]; ok {
		ms, ok := raw.(float64)
		if !ok || ms <= 0 {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.expiresInMs must be a positive number, got %T", action, raw)
		}
		// The deadline is computed HERE, on the agent's own clock, and
		// carried onward as an absolute value: the coordinator sends a
		// relative TTL so neither side's clock has to agree with the
		// other's, but nothing downstream (Merge included) touches a
		// clock again once this line has run.
		req.Expiry = pkgaudio.SetField(time.Now().Add(time.Duration(ms) * time.Millisecond))
	}

	return req, nil
}

var audioSessionPlaylistKnownKeys = map[string]bool{
	"ownerKind": true, "ownerId": true, "ownerRevision": true,
	"repeat": true, "resume": true, "requestedTransition": true, "items": true,
}

func parsePlaylistRef(action string, p map[string]any) (pkgaudio.PlaylistRef, error) {
	if err := rejectUnknownKeys(action, p, audioSessionPlaylistKnownKeys); err != nil {
		return pkgaudio.PlaylistRef{}, err
	}

	str := func(key string, required bool) (string, error) {
		raw, ok := p[key]
		if !ok {
			if required {
				return "", fmt.Errorf("%s: params.playlist.%s is required", action, key)
			}
			return "", nil
		}
		v, ok := raw.(string)
		if !ok || v == "" {
			return "", fmt.Errorf("%s: params.playlist.%s must be a non-empty string, got %T", action, key, raw)
		}
		return v, nil
	}

	ownerKind, err := str("ownerKind", true)
	if err != nil {
		return pkgaudio.PlaylistRef{}, err
	}
	ownerID, err := str("ownerId", true)
	if err != nil {
		return pkgaudio.PlaylistRef{}, err
	}

	var ownerRevision pkgaudio.Revision
	if raw, ok := p["ownerRevision"]; ok {
		f, ok := raw.(float64)
		if !ok || f < 0 {
			return pkgaudio.PlaylistRef{}, fmt.Errorf("%s: params.playlist.ownerRevision must be a non-negative number, got %v", action, raw)
		}
		ownerRevision = pkgaudio.Revision(f)
	}

	repeat := pkgaudio.RepeatNone
	if v, err := str("repeat", false); err != nil {
		return pkgaudio.PlaylistRef{}, err
	} else if v != "" {
		repeat = pkgaudio.RepeatMode(v)
	}
	resume := pkgaudio.ResumePolicyRestart
	if v, err := str("resume", false); err != nil {
		return pkgaudio.PlaylistRef{}, err
	} else if v != "" {
		resume = pkgaudio.ResumePolicy(v)
	}
	transition := pkgaudio.ItemTransitionSequential
	if v, err := str("requestedTransition", false); err != nil {
		return pkgaudio.PlaylistRef{}, err
	} else if v != "" {
		transition = pkgaudio.ItemTransition(v)
	}

	rawItems, ok := p["items"]
	if !ok {
		return pkgaudio.PlaylistRef{}, fmt.Errorf("%s: params.playlist.items is required", action)
	}
	list, ok := rawItems.([]any)
	if !ok || len(list) == 0 {
		return pkgaudio.PlaylistRef{}, fmt.Errorf("%s: params.playlist.items must be a non-empty array", action)
	}
	items := make([]pkgaudio.PlaylistItem, 0, len(list))
	for i, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			return pkgaudio.PlaylistRef{}, fmt.Errorf("%s: params.playlist.items[%d] must be an object, got %T", action, i, raw)
		}
		if err := rejectUnknownKeys(action, m, audioMediaProbeItemKnownKeys); err != nil {
			return pkgaudio.PlaylistRef{}, err
		}
		item, err := parseMediaProbeItem(action, m, i)
		if err != nil {
			return pkgaudio.PlaylistRef{}, err
		}
		items = append(items, item)
	}

	return pkgaudio.PlaylistRef{
		OwnerKind: ownerKind, OwnerID: ownerID, OwnerRevision: ownerRevision,
		Items: items, Repeat: repeat, Resume: resume, RequestedTransition: transition,
	}, nil
}

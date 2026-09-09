package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
	// A wire revision decodes as json.Number (mqttproto.DecodeCmdPayload);
	// ParseUint rejects a negative, fractional, or out-of-range literal.
	// float64 stays valid for a caller building params without the wire.
	var rev uint64
	switch v := rawRev.(type) {
	case json.Number:
		parsed, err := strconv.ParseUint(v.String(), 10, 64)
		if err != nil {
			return "", "", 0, fmt.Errorf("%s: params.revision must be a non-negative whole number, got %v", action, rawRev)
		}
		rev = parsed
	case float64:
		if v < 0 {
			return "", "", 0, fmt.Errorf("%s: params.revision must be a non-negative number, got %v", action, rawRev)
		}
		rev = uint64(v)
	default:
		return "", "", 0, fmt.Errorf("%s: params.revision must be a non-negative number, got %v", action, rawRev)
	}

	return pkgaudio.SessionID(sessionID), pkgaudio.InvocationID(invocation), pkgaudio.Revision(rev), nil
}

// maxExactFloat64Integer is 2^53-1, the largest integer a float64 holds
// exactly, and the same bound ui/src/api/bigint.ts uses on the operator
// side for the same reason.
const maxExactFloat64Integer = 9007199254740991

// parseScheduledAtNs reads audio.session.start's optional media-clock
// start instant. Absent is not an error: a command without it starts on
// arrival exactly as every command did before this seam existed.
//
// The value arrives as a json.Number, not a float64 like every other
// param, because mqttproto.DecodeCmdPayload preserves it exactly (see
// that package's exactIntegerParams). Nanoseconds since an epoch are
// around 1.79e18 and float64 cannot hold that as an exact integer, so a
// float64 here would already be a rounded instant by the time this
// function saw it. A float64 IS still accepted, for a caller building
// params in process rather than off the wire, but only when it converts
// back exactly: a rounded literal is refused rather than started
// against.
func parseScheduledAtNs(params map[string]any) (int64, bool, error) {
	raw, ok := params[pkgaudio.ParamScheduledAtNs]
	if !ok {
		return 0, false, nil
	}
	switch v := raw.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(v.String(), 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("audio.session.start: params.%s must be a whole number of nanoseconds on this node's media clock, got %v", pkgaudio.ParamScheduledAtNs, raw)
		}
		return parsed, true, nil
	case float64:
		asInt := int64(v)
		if float64(asInt) != v {
			return 0, false, fmt.Errorf("audio.session.start: params.%s (%v) is not a whole number of nanoseconds", pkgaudio.ParamScheduledAtNs, raw)
		}
		// Above float64's exactly-representable integer range this value
		// cannot be trusted to be the one that was sent, whether or not
		// it happens to be integral, so it is refused rather than started
		// against. A real media-clock reading is around 1.79e18 and is
		// ALWAYS in this range, which is why the wire form is an exact
		// integer and this branch exists only for a caller building
		// params in process.
		if asInt > maxExactFloat64Integer || asInt < -maxExactFloat64Integer {
			return 0, false, fmt.Errorf("audio.session.start: params.%s (%v) arrived as a float64 beyond %d, where a float64 has already rounded it; send it as an exact JSON integer",
				pkgaudio.ParamScheduledAtNs, raw, int64(maxExactFloat64Integer))
		}
		return asInt, true, nil
	default:
		return 0, false, fmt.Errorf("audio.session.start: params.%s must be a whole number of nanoseconds on this node's media clock, got %T", pkgaudio.ParamScheduledAtNs, raw)
	}
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
// are parsed and any operation-specific params are extracted. The
// returned map is this operation's own extra result evidence, merged
// into [OperationResult.Value] alongside the three fields every session
// operation reports; nil for the operations that have none.
type sessionExec func(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, params map[string]any) (pkgaudio.OutcomeResult, string, map[string]any, error)

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
		outcome, signal, extra, err := exec(ctx, mgr, id, inv, rev, params)
		if err != nil {
			return OperationResult{}, err
		}
		if err := outcome.Validate(); err != nil {
			return OperationResult{}, fmt.Errorf("audio session operation produced an invalid outcome: %w", err)
		}

		value := map[string]any{
			"sessionId": string(id),
			"outcome":   string(outcome.Outcome),
			"reason":    outcome.Reason,
		}
		for k, v := range extra {
			value[k] = v
		}
		return OperationResult{
			Confirmed:  outcomeConfirmed(outcome),
			Signal:     signal,
			Value:      value,
			ExecutedAt: dispatchedAt,
			ObservedAt: now(),
		}, nil
	}
}

// outcomeConfirmed reports whether outcome is a genuine success:
// Unconfirmable, Refused, and Failed are all Confirmed:false, matching
// OperationResult's own "read-back evidence corroborates the request"
// contract. Shared by every audio.session.* operation and by
// audio.node.silence, which reports its own Confirmed as true only when
// every session it touched individually confirms this way.
func outcomeConfirmed(outcome pkgaudio.OutcomeResult) bool {
	return outcome.Outcome != pkgaudio.OutcomeRefused &&
		outcome.Outcome != pkgaudio.OutcomeFailed &&
		outcome.Outcome != pkgaudio.OutcomeUnconfirmable
}

func applySession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, params map[string]any) (pkgaudio.OutcomeResult, string, map[string]any, error) {
	req, err := parseApplyRequest("audio.session.apply", params)
	if err != nil {
		return pkgaudio.OutcomeResult{}, "", nil, err
	}
	return mgr.Apply(ctx, id, inv, rev, req), "node.audio_session.apply", nil, nil
}

// prepareSession reports its preroll latency alongside readiness: a
// coordinator picking a start instant needs to know how long this node
// actually takes to open, decode and preroll the item, and a node that
// has never prepared reports no value at all rather than a zero.
func prepareSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, map[string]any, error) {
	outcome := mgr.Prepare(ctx, id, inv, rev)
	extra := map[string]any{}
	if preroll, known := mgr.PrerollLatency(id); known {
		extra[pkgaudio.ResultPrerollMs] = preroll.Milliseconds()
	}
	addMediaClockReadiness(ctx, mgr, extra)
	return outcome, "node.audio_session.prepare", extra, nil
}

// addMediaClockReadiness writes this node's own media-clock reading onto
// a prepare result, sampled here rather than earlier so it accompanies
// the readiness message it is published with (RES-019 section 6: the node
// holding the clock reports now() with each readiness message, and the
// coordinator offsets its start instant from that sample).
//
// The validity flag and reason are ALWAYS written, including when the
// reading is good, so a consumer never has to read an absent field as a
// claim. The instant and its error bound are written only when they are
// real: an invalid reading carries no number at all rather than a zero
// that would read as an epoch.
func addMediaClockReadiness(ctx context.Context, mgr *audio.Manager, extra map[string]any) {
	now := mgr.MediaNow(ctx)
	extra[pkgaudio.ResultMediaClockValid] = now.Valid
	extra[pkgaudio.ResultMediaClockReason] = now.Reason
	if !now.Valid {
		return
	}
	extra[pkgaudio.ResultMediaClockNowNs] = now.Time.UnixNano()
	extra[pkgaudio.ResultMediaClockErrorBoundKnown] = now.ErrorBoundKnown
	if now.ErrorBoundKnown {
		extra[pkgaudio.ResultMediaClockErrorBoundNs] = now.ErrorBoundNs
	}
}

// startSession honours pkg/audio's ParamScheduledAtNs when the command
// carries it: T0 on THIS node's media clock, in nanoseconds. The param is
// optional, and a command without it starts on arrival exactly as before
// this seam existed.
func startSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, params map[string]any) (pkgaudio.OutcomeResult, string, map[string]any, error) {
	atNs, present, err := parseScheduledAtNs(params)
	if err != nil {
		return pkgaudio.OutcomeResult{}, "", nil, err
	}
	if !present {
		return mgr.Start(ctx, id, inv, rev), "node.audio_session.start", nil, nil
	}
	return mgr.StartAt(ctx, id, inv, rev, atNs), "node.audio_session.start", nil, nil
}

func pauseSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, map[string]any, error) {
	return mgr.Pause(ctx, id, inv, rev), "node.audio_session.pause", nil, nil
}

func resumeSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, map[string]any, error) {
	return mgr.Resume(ctx, id, inv, rev), "node.audio_session.resume", nil, nil
}

func seekSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, params map[string]any) (pkgaudio.OutcomeResult, string, map[string]any, error) {
	raw, ok := params["positionMs"]
	if !ok {
		return pkgaudio.OutcomeResult{}, "", nil, fmt.Errorf("audio.session.seek: params.positionMs is required")
	}
	f, ok := raw.(float64)
	if !ok || f < 0 {
		return pkgaudio.OutcomeResult{}, "", nil, fmt.Errorf("audio.session.seek: params.positionMs must be a non-negative number, got %v", raw)
	}
	position := time.Duration(f) * time.Millisecond
	return mgr.Seek(ctx, id, inv, rev, position), "node.audio_session.seek", nil, nil
}

func advanceSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, map[string]any, error) {
	return mgr.Advance(ctx, id, inv, rev), "node.audio_session.advance", nil, nil
}

func stopSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, map[string]any, error) {
	return mgr.Stop(ctx, id, inv, rev), "node.audio_session.stop", nil, nil
}

func clearSession(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, inv pkgaudio.InvocationID, rev pkgaudio.Revision, _ map[string]any) (pkgaudio.OutcomeResult, string, map[string]any, error) {
	return mgr.Clear(ctx, id, inv, rev), "node.audio_session.clear", nil, nil
}

// parseApplyRequest builds a [pkgaudio.ApplyRequest] from apply's own
// params, on top of the common sessionId/invocationId/revision.
// Supported fields: sourceRole, media (a MediaRef object, mirroring
// audio.media.probe's own field names), playlist (ownerKind, ownerId,
// ownerRevision, repeat, resume, requestedTransition, items — items reuse
// the exact object shape audio.media.probe's params.items already
// defines), outputs (a string array), mixPolicy, and ceiling (a linear
// [pkgaudio.Ceiling], optional; a caller that omits it leaves the
// session's ceiling exactly as it was). This agent's own
// rejectUnknownKeys refuses the WHOLE apply, not merely the ceiling
// field, if a caller sends "ceiling" against an agent build that
// predates this key: the coordinator's own safety net against that is
// capability-gated, never sending "ceiling" to a node whose live
// advertisement does not confirm "audio.playback.ceiling"
// (internal/coordinator/api/nightbackgroundaudio.go's
// audioNodeConfirmsCeiling), not anything enforced here. Gain, fade, and
// bookmark are not wired here: gain and fade belong to the separate
// audio.gain.set/audio.gain.fade surface, and a bookmark is
// session-internal state this package manages itself (Pause writes one;
// nothing here accepts one from a caller). expiresInMs additionally
// refreshes the session's retirement deadline
// ([pkgaudio.SessionDesiredState.Expiry]) to this agent's own now() plus
// the given duration: a coordinator-stamped field an operator need not
// send.
var audioSessionApplyKnownKeys = map[string]bool{
	"sourceRole": true, "media": true, "playlist": true, "outputs": true,
	"ltcStartOffset": true, "mixPolicy": true, "expiresInMs": true, "ceiling": true,
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

	if raw, ok := body["ceiling"]; ok {
		f, ok := raw.(float64)
		if !ok {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.ceiling must be a number, got %T", action, raw)
		}
		ceiling := pkgaudio.Ceiling(f)
		if err := ceiling.Validate(); err != nil {
			return pkgaudio.ApplyRequest{}, fmt.Errorf("%s: params.ceiling: %w", action, err)
		}
		req.Ceiling = pkgaudio.SetField(ceiling)
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

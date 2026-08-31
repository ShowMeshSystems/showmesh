package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// Cue resolution and classification. nightcuerun.go owns the commit/
// dispatch/recover mechanics that use these.

// The night_cue_outbox.state vocabulary. "pending" and "dispatched" are
// both non-terminal; never treat either as satisfying a barrier.
const (
	nightCueStatePending    = "pending"    // outbox row committed; dispatch not yet attempted.
	nightCueStateDispatched = "dispatched" // a dispatch attempt was made; outcome not yet resolved.
	nightCueStateResolved   = "resolved"   // outcome captured - see the outcome column for confirmed/unconfirmed/etc.
	nightCueStateAmbiguous  = "ambiguous"  // terminal, unresolved by construction; never retried automatically.

	// nightCueStateNotDispatched is a wire-only state (no outbox row
	// exists yet): the current cycle has not reached this cue's offset,
	// or the session has never run. Never written to night_cue_outbox.
	nightCueStateNotDispatched = "not_dispatched"
)

// The night_cue_outbox.outcome vocabulary, for resolved/ambiguous rows.
// Mirrors ADR-029 decision 4; "ambiguous" is recovery's own addition.
const (
	nightCueOutcomeConfirmed     = "confirmed"
	nightCueOutcomeUnconfirmed   = "unconfirmed"
	nightCueOutcomeUnconfirmable = "unconfirmable"
	nightCueOutcomeFailed        = "failed"
	nightCueOutcomeRefused       = "refused"
	nightCueOutcomeAmbiguous     = "ambiguous"
)

// nightCueIdempotencyKey derives one cue's stable invocation identity
// (session + cycle + phase + cue), used both as the outbox row's own
// identity and, for fpp, as the FPP command's IdempotencyKey.
func nightCueIdempotencyKey(sessionID string, cycle int64, phase, cueName string) string {
	return fmt.Sprintf("night-cue:%s:%d:%s:%s", sessionID, cycle, phase, cueName)
}

// nightResolveShowAction reads and decodes actionID's current show.action
// revision. The returned revision is what a caller pins at commit."
func nightResolveShowAction(ctx context.Context, cfg ConfigStore, actionID string) (config.ShowActionPayload, int64, error) {
	obj, err := cfg.GetConfigObject(ctx, config.ShowActionConfigKind, actionID)
	if err != nil {
		return config.ShowActionPayload{}, 0, fmt.Errorf("api: get show.action object %q: %w", actionID, err)
	}
	if obj.CurrentRevision == 0 {
		return config.ShowActionPayload{}, 0, fmt.Errorf("api: show.action %q has no active revision", actionID)
	}
	rev, err := cfg.GetConfigRevision(ctx, config.ShowActionConfigKind, actionID, obj.CurrentRevision)
	if err != nil {
		return config.ShowActionPayload{}, 0, fmt.Errorf("api: get show.action %q revision %d: %w", actionID, obj.CurrentRevision, err)
	}
	var payload config.ShowActionPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return config.ShowActionPayload{}, 0, fmt.Errorf("api: decode show.action %q payload: %w", actionID, err)
	}
	return payload, obj.CurrentRevision, nil
}

// nightResolveShowActionRevision reads actionID at a SPECIFIC, already-
// pinned revision - recovery must not silently move onto whatever
// revision happens to be current by the time it runs.
func nightResolveShowActionRevision(ctx context.Context, cfg ConfigStore, actionID string, revision int64) (config.ShowActionPayload, error) {
	rev, err := cfg.GetConfigRevision(ctx, config.ShowActionConfigKind, actionID, revision)
	if err != nil {
		return config.ShowActionPayload{}, fmt.Errorf("api: get show.action %q revision %d: %w", actionID, revision, err)
	}
	var payload config.ShowActionPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return config.ShowActionPayload{}, fmt.Errorf("api: decode show.action %q payload: %w", actionID, err)
	}
	return payload, nil
}

// nightCueConfirmable reports whether target's own adapter is wired to
// BOTH dispatch and confirm its effect. An mqtt action qualifies only when
// it declares an expected response; one that declares none can publish but
// can never report more than "unconfirmable".
func nightCueConfirmable(target config.ShowActionTarget) bool {
	switch target.Integration {
	case config.ShowActionIntegrationFPP, config.ShowActionIntegrationResolume, config.ShowActionIntegrationAudio:
		return true
	case config.ShowActionIntegrationMQTT:
		return target.Expect != nil && target.Expect.Kind != config.MQTTExpectKindNone
	default:
		return false
	}
}

// nightCueAllowedAsFirstOutwardCue reports whether action may sit at
// §7.1.1's own commit boundary: either its adapter can confirm the effect
// ([nightCueConfirmable]), or the action itself declares idempotent true.
// A declared-false or undeclared idempotency never substitutes for
// confirmability - see [config.ShowActionPayload.Idempotent]'s own doc
// comment for why absent must never read as true.
func nightCueAllowedAsFirstOutwardCue(action config.ShowActionPayload) bool {
	return nightCueConfirmable(action.Target) || (action.Idempotent != nil && *action.Idempotent)
}

// nightCueRetryableByIdentity reports whether recovery may safely re-issue
// target under the SAME identity after an unresolved crash. fpp and audio
// both qualify: dispatchFPPCommand's and executeAudioSessionDispatch's own
// idempotency-first replay (audiodispatch.go's InsertCommand duplicate-key
// path, resolveAudioSessionReplay) can never re-send under the same key -
// a retry with the same idemKey, action, node, and params returns the
// FIRST attempt's own recorded outcome rather than dispatching again.
// Resolume and mqtt carry no comparable key and stay ambiguous.
func nightCueRetryableByIdentity(target config.ShowActionTarget) bool {
	return target.Integration == config.ShowActionIntegrationFPP || target.Integration == config.ShowActionIntegrationAudio
}

// nightCueDispatchResult is one dispatch attempt's outcome, in the
// vocabulary this file's own outboxState/nightCueOutcome* constants share
// with night_cue_outbox's own columns.
type nightCueDispatchResult struct {
	dispatched   bool
	dispatchedAt *time.Time
	resolved     bool // true when outcome is final and should be persisted as nightCueStateResolved.
	outcome      string
	reason       string
}

// nightDispatchCueTarget dispatches target through its integration's own
// adapter. It never inspects night_cue_outbox; the caller owns timing.
// actionRevision is the cue's own pinned show.action revision (never the
// live one) - only the audio branch currently needs it, to carry as
// pkg/audio's own Revision.
func (h *handlers) nightDispatchCueTarget(ctx context.Context, now time.Time, issuer FPPCommandIssuer, target config.ShowActionTarget, idemKey string, actionRevision int64) nightCueDispatchResult {
	switch target.Integration {
	case config.ShowActionIntegrationFPP:
		return h.nightDispatchCueFPP(ctx, now, issuer, target, idemKey)
	case config.ShowActionIntegrationResolume:
		return h.nightDispatchCueResolume(ctx, now, target)
	case config.ShowActionIntegrationMQTT:
		return h.nightDispatchCueMQTT(ctx, now, target)
	case config.ShowActionIntegrationAudio:
		return h.nightDispatchCueAudio(ctx, now, issuer, target, idemKey, actionRevision)
	default:
		return nightCueDispatchResult{
			dispatched: false, resolved: true,
			outcome: nightCueOutcomeFailed,
			reason:  fmt.Sprintf("unrecognized show.action integration %q", target.Integration),
		}
	}
}

func (h *handlers) nightDispatchCueFPP(ctx context.Context, now time.Time, issuer FPPCommandIssuer, target config.ShowActionTarget, idemKey string) nightCueDispatchResult {
	outcome, problem, err := h.dispatchFPPCommand(ctx, now, FPPCommandInput{
		InstanceID:                  target.InstanceID,
		Action:                      target.Primitive,
		Params:                      target.Params,
		IdempotencyKey:              idemKey,
		Issuer:                      issuer,
		NeverWithholdOnAuditFailure: true,
	})
	if err != nil {
		// A plain error always precedes dispatchFPPCommand's own
		// command-row insert, so nothing reached FPP: resolved failed,
		// never left dispatched for a later tick to find and misread.
		return nightCueDispatchResult{dispatched: false, resolved: true, outcome: nightCueOutcomeFailed, reason: "this cue could not be dispatched: " + err.Error()}
	}
	if problem != nil {
		return nightCueDispatchResult{dispatched: false, resolved: true, outcome: nightCueOutcomeRefused, reason: problem.Detail}
	}
	if outcome.Outcome == "" {
		// A Replay observed mid-flight (FPPCommandOutcome.Outcome's own
		// doc comment) - an fpp command row exists under this key but has
		// not resolved yet. Not yet resolved, not an error: the caller
		// leaves the row in nightCueStateDispatched and a later tick
		// retries this same call, which dispatchFPPCommand's own
		// idempotency-first design still never turns into a second send.
		return nightCueDispatchResult{dispatched: true, dispatchedAt: outcome.DispatchedAt, resolved: false}
	}
	finalOutcome := nightCueOutcomeUnconfirmed
	if outcome.Outcome == "confirmed" {
		finalOutcome = nightCueOutcomeConfirmed
	}
	return nightCueDispatchResult{
		dispatched: true, dispatchedAt: outcome.DispatchedAt, resolved: true,
		outcome: finalOutcome, reason: outcome.OutcomeReason,
	}
}

// nightDispatchCueMQTT publishes through the same adapter the ad hoc
// action-invocation path uses. The outcome is always resolved: an mqtt
// action carries no retry identity, so a later tick must never re-publish
// it, and an ambiguous post-send result stays ambiguous for an operator.
func (h *handlers) nightDispatchCueMQTT(ctx context.Context, now time.Time, target config.ShowActionTarget) nightCueDispatchResult {
	res := DispatchMQTTAction(ctx, h.deps.MQTTBrokers, target, func() time.Time { return now })
	var outcome string
	switch res.Outcome {
	case outcomeWordConfirmed:
		outcome = nightCueOutcomeConfirmed
	case outcomeWordUnconfirmed:
		outcome = nightCueOutcomeUnconfirmed
	case outcomeWordUnconfirmable:
		outcome = nightCueOutcomeUnconfirmable
	default:
		outcome = nightCueOutcomeFailed
	}
	reason := res.OutcomeReason
	if res.OutcomeState != "" {
		reason = res.OutcomeState + ": " + reason
	}
	dispatchedAt := now
	var at *time.Time
	if res.PublishAttempted {
		at = &dispatchedAt
	}
	return nightCueDispatchResult{
		dispatched: res.PublishAttempted, dispatchedAt: at, resolved: true,
		outcome: outcome, reason: reason,
	}
}

func (h *handlers) nightDispatchCueResolume(ctx context.Context, now time.Time, target config.ShowActionTarget) nightCueDispatchResult {
	res, err := h.deps.ResolumeActions.Dispatch(ctx, target.Action, target.Ref, now)
	if err != nil {
		// Dispatch's own error contract is a dependency failure that
		// provably sent nothing: resolved failed, never left dispatched.
		return nightCueDispatchResult{dispatched: false, resolved: true, outcome: nightCueOutcomeFailed, reason: "this cue could not be dispatched: " + err.Error()}
	}
	var outcome string
	switch res.Outcome {
	case ResolumeOutcomeConfirmed:
		outcome = nightCueOutcomeConfirmed
	case ResolumeOutcomeUnconfirmed:
		outcome = nightCueOutcomeUnconfirmed
	case ResolumeOutcomeUnconfirmable:
		outcome = nightCueOutcomeUnconfirmable
	case ResolumeOutcomeRefused:
		outcome = nightCueOutcomeRefused
	default: // ResolumeOutcomeFailed and anything unrecognized.
		outcome = nightCueOutcomeFailed
	}
	return nightCueDispatchResult{
		dispatched: res.Dispatched, dispatchedAt: res.DispatchedAt, resolved: true,
		outcome: outcome, reason: res.Reason,
	}
}

// nightAudioCueOutcome maps a pkg/audio.Outcome (as reported on
// [v1.AudioSessionCommandResult.Outcome]) onto night_cue_outbox's own
// outcome vocabulary, the same way mapResultOutcome/audioOutcomeShouldPersist
// already do for a direct audio.session.* API call (audiodispatch.go). An
// evidence-backed observation confirms the cue; unconfirmable and refused
// map onto their own like-named outbox outcomes; anything else (failed, or
// a string this coordinator does not recognize) is treated as failed
// rather than silently confirmed.
func nightAudioCueOutcome(outcome string) string {
	switch pkgaudio.Outcome(outcome) {
	case pkgaudio.OutcomeStarted, pkgaudio.OutcomePosition, pkgaudio.OutcomeGain,
		pkgaudio.OutcomeFadeComplete, pkgaudio.OutcomeStopped, pkgaudio.OutcomeCompleted:
		return nightCueOutcomeConfirmed
	case pkgaudio.OutcomeUnconfirmable:
		return nightCueOutcomeUnconfirmable
	case pkgaudio.OutcomeRefused:
		return nightCueOutcomeRefused
	default:
		return nightCueOutcomeFailed
	}
}

// nightDispatchCueAudio dispatches target through the SAME
// executeAudioSessionDispatch machinery a direct audio.session.* API call
// uses (audiodispatch.go) - never a parallel dispatch path. idemKey (the
// cue's own stable invocation identity, [nightCueIdempotencyKey]) becomes
// both the command's idempotency key and, via params["invocationId"],
// pkg/audio's own InvocationID, so a crash-recovery replay can never play
// something twice (executeAudioSessionDispatch's own InsertCommand
// duplicate-key path returns the first attempt's recorded outcome rather
// than redispatching). actionRevision - the pinned show.action revision,
// never the live one - becomes params["revision"]: pkg/audio's
// RevisionState.Apply refuses to apply a revision that does not strictly
// advance the session's own desired revision, so a delayed retry can
// never rewind a newer command that has already landed.
func (h *handlers) nightDispatchCueAudio(ctx context.Context, now time.Time, issuer FPPCommandIssuer, target config.ShowActionTarget, idemKey string, actionRevision int64) nightCueDispatchResult {
	params := make(map[string]any, len(target.Params)+3)
	for k, v := range target.Params {
		params[k] = v
	}
	// An authored show.action carries its gain in decibels; the night
	// background-audio controller builds its own targets already linear.
	// See audiogaindb.go for why converting only when a decibel key is
	// present is right on this path and would be wrong on the HTTP one.
	convertAuthoredAudioGainParams(target.AudioAction, params)
	params["sessionId"] = target.AudioSessionID
	params["invocationId"] = idemKey
	params["revision"] = uint64(actionRevision)

	result, problem, err := h.executeAudioSessionDispatch(ctx, now, audioDispatchInput{
		Action: target.AudioAction, NodeID: target.AudioNodeID, SessionID: target.AudioSessionID,
		Params: params, Revision: uint64(actionRevision), IdempotencyKey: idemKey,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID, ClientAddr: issuer.ClientAddr,
	})
	if err != nil {
		if errors.Is(err, broker.ErrResponseFailedBeforePublish) {
			// Nothing was ever published (executeAudioSessionDispatch's
			// own markUndispatched path): resolved failed is correct here,
			// mirroring nightDispatchCueFPP's identical treatment for an
			// error that provably sent nothing.
			return nightCueDispatchResult{dispatched: false, resolved: true, outcome: nightCueOutcomeFailed, reason: "this cue could not be dispatched: " + err.Error()}
		}
		// Any OTHER error from executeAudioSessionDispatch arrives AFTER
		// its own markDispatched call (audiodispatch.go's own await-result
		// error branch): the command was published and may have reached
		// the node, and its outcome is genuinely unknown - recording this
		// as a definite failure would be fabricating evidence exactly the
		// way dispatchFPPCommand's own "Replay observed mid-flight" case
		// (nightDispatchCueFPP, one function up) already avoids. Leaving
		// this unresolved lets a later tick retry under the SAME
		// idempotency key, which audio's own replay-by-identity dedup
		// (executeAudioSessionDispatch's InsertCommand duplicate-key path)
		// answers from the first attempt's own recorded outcome rather
		// than sending a second command.
		return nightCueDispatchResult{dispatched: true, resolved: false}
	}
	if problem != nil {
		// A structural refusal (e.g. the audit store was unavailable, or
		// the idempotency key was reused against different params) means
		// nothing new was sent to the node.
		return nightCueDispatchResult{dispatched: false, resolved: true, outcome: nightCueOutcomeRefused, reason: problem.Detail}
	}
	var dispatchedAt *time.Time
	if result.DispatchedAt != "" {
		if t, perr := parseTime(result.DispatchedAt); perr == nil {
			dispatchedAt = &t
		}
	}
	return nightCueDispatchResult{
		dispatched: dispatchedAt != nil, dispatchedAt: dispatchedAt, resolved: true,
		outcome: nightAudioCueOutcome(result.Outcome), reason: result.Reason,
	}
}

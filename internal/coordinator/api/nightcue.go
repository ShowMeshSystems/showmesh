package api

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// Cue resolution and classification. nightcuerun.go owns the commit/
// dispatch/recover mechanics that use these.

// The night_cue_outbox.state vocabulary. "pending" and "dispatched" are
// both non-terminal; never treat either as satisfying a barrier.
const (
	nightCueStatePending    = "pending"    // outbox row committed; dispatch not yet attempted.
	nightCueStateDispatched = "dispatched" // a dispatch attempt was made; outcome not yet resolved.
	nightCueStateResolved   = "resolved"   // outcome captured — see the outcome column for confirmed/unconfirmed/etc.
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
// pinned revision — recovery must not silently move onto whatever
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
	case config.ShowActionIntegrationFPP, config.ShowActionIntegrationResolume:
		return true
	case config.ShowActionIntegrationMQTT:
		return target.Expect != nil && target.Expect.Kind != config.MQTTExpectKindNone
	default:
		return false
	}
}

// nightCueRetryableByIdentity reports whether recovery may safely re-issue
// target under the SAME identity after an unresolved crash. Only fpp
// qualifies: dispatchFPPCommand's own idempotency-first replay can never
// re-send. Resolume and mqtt carry no comparable key and stay ambiguous.
func nightCueRetryableByIdentity(target config.ShowActionTarget) bool {
	return target.Integration == config.ShowActionIntegrationFPP
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
func (h *handlers) nightDispatchCueTarget(ctx context.Context, now time.Time, issuer FPPCommandIssuer, target config.ShowActionTarget, idemKey string) nightCueDispatchResult {
	switch target.Integration {
	case config.ShowActionIntegrationFPP:
		return h.nightDispatchCueFPP(ctx, now, issuer, target, idemKey)
	case config.ShowActionIntegrationResolume:
		return h.nightDispatchCueResolume(ctx, now, target)
	case config.ShowActionIntegrationMQTT:
		return h.nightDispatchCueMQTT(ctx, now, target)
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
		// doc comment) — an fpp command row exists under this key but has
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

package macro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// mqttReasonMaxBytes bounds how much of a response payload this package
// records in a step's own operator-facing OutcomeReason (the "text" kind's
// confirmed reason echoes the payload back) — STEP-9-SPEC.md section 7.3:
// "the payload is recorded, bounded." Not a protocol limit, just this
// package's own defense against an oversized MQTT payload turning into an
// oversized database row.
const mqttReasonMaxBytes = 512

// dispatchMQTTStep dispatches one MQTT-integration step (STEP-9-SPEC.md
// section 7): write the DISPATCH audit entry (subject to the per-step
// audit exemption — see this function's own middle section), then publish
// and, for every expect.kind except "none", wait for a live matching
// response through [mqttRegistry.AwaitResponse] (broker/response.go),
// which already handles STEP-9-SPEC.md section 7.2's retained-message trap
// (subscribe before publish, discard every RETAIN=1 delivery, deadline
// measured from the publish) — this function does not re-implement any of
// that; it only decides what a resolved (or failed, or expired) response
// MEANS, per section 7.3's five-mode contract (resolveMQTTPayload below).
//
// Unlike an FPP step, there is no pre-existing in-process dispatch seam
// that already writes an audit entry on this package's behalf — the
// broker package (Wave 1c) is a transport primitive only (its own top
// comment: "this package provides only the transport-level primitive").
// So this function writes both the dispatch and outcome audit entries
// itself, directly against [identity.Service], mirroring
// dispatchFPPCommand's own shape (write DISPATCH before attempting
// anything; write OUTCOME best-effort afterward, regardless of safety
// class) one level up, in the one integration where nothing else does it.
func (e *Executor) dispatchMQTTStep(ctx context.Context, run store.MacroRunRecord, step store.MacroRunStepRecord, action resolvedAction, issuer api.FPPCommandIssuer) stepResult {
	target := action.Payload.Target
	if target.Publish == nil || target.Expect == nil {
		// Unreachable given write-time validation (config.decodeMQTTTarget
		// requires both), answered defensively rather than panicking on a
		// nil pointer if a pre-Step-9 or hand-edited row ever disagrees.
		return stepResult{
			outcome:       outcomeFailed,
			outcomeState:  "malformed_action",
			outcomeReason: "this mqtt action is missing its publish or expect definition",
			resolvedAt:    ptrTime(e.now()),
		}
	}

	now := e.now()
	stepKey := stepIdempotencyKey(run.ID, step.StepIndex)
	dispatchEntry := identity.AuditEntry{
		Timestamp:      now,
		PrincipalID:    issuer.PrincipalID,
		PrincipalName:  issuer.PrincipalName,
		Form:           issuer.Form,
		CredentialID:   issuer.CredentialID,
		ClientAddr:     issuer.ClientAddr,
		Action:         "mqtt.publish",
		Target:         target.Broker + ":" + target.Publish.Topic,
		IdempotencyKey: stepKey,
		Kind:           identity.AuditDispatch,
		Params:         map[string]any{"runId": run.ID, "stepId": step.StepID, "stepIndex": step.StepIndex},
	}

	// OWNER DECISION, 2026-08-14: a macro run never withholds a command
	// because the audit store is down, whatever this step's safety class.
	// This branch used to refuse any step whose class was not one of
	// ADR-024 decision 11's three, which meant an unwritable audit_log
	// could punch a hole in a running show. Attribution is downgraded and
	// said out loud instead: the step publishes, carries
	// AttributionDegraded onto the run, and logs the cause. See
	// [api.FPPCommandInput.NeverWithholdOnAuditFailure] for the FPP half of
	// the same rule and the owner's own wording of it.
	attrDegraded := false
	if auditErr := e.identity.WriteAudit(ctx, dispatchEntry); auditErr != nil {
		attrDegraded = true
		e.logWarn("mqtt step dispatched with degraded attribution: audit store unwritable, and a macro run never withholds a command for that",
			"runId", run.ID, "stepId", step.StepID, "safetyClass", action.Payload.SafetyClass, "cause", errString(auditErr))
	}

	dispatchedAt := e.now()
	res := e.publishAndAwait(ctx, target)
	// Only a step that actually put something on a wire gets a
	// dispatchedAt; see stepResult.publishAttempted's own doc comment.
	if res.publishAttempted {
		res.dispatchedAt = &dispatchedAt
	}
	if res.resolvedAt == nil {
		res.resolvedAt = ptrTime(e.now())
	}
	res.attrDegraded = res.attrDegraded || attrDegraded

	outcomeEntry := identity.AuditEntry{
		Timestamp:      *res.resolvedAt,
		PrincipalID:    issuer.PrincipalID,
		PrincipalName:  issuer.PrincipalName,
		Form:           issuer.Form,
		CredentialID:   issuer.CredentialID,
		ClientAddr:     issuer.ClientAddr,
		Action:         "mqtt.publish",
		Target:         target.Broker + ":" + target.Publish.Topic,
		IdempotencyKey: stepKey,
		Kind:           identity.AuditOutcome,
		Outcome:        res.outcome,
		OutcomeState:   res.outcomeState,
		OutcomeReason:  res.outcomeReason,
		Params:         map[string]any{"runId": run.ID, "stepId": step.StepID, "stepIndex": step.StepIndex},
	}
	// Best-effort, always — mirroring dispatchFPPCommand's own outcome
	// audit entry, "for EVERY primitive regardless of SafetyClass": the
	// exemption governs whether the STEP dispatches, never whether its
	// outcome is worth trying to record.
	if err := e.identity.WriteAudit(ctx, outcomeEntry); err != nil {
		res.attrDegraded = true
		e.logWarn("failed to write mqtt step outcome audit entry", "runId", run.ID, "stepId", step.StepID, "error", err)
	}

	return res
}

// publishAndAwait performs target's publish and, for every expect.kind
// except "none", the wait for a live matching response.
func (e *Executor) publishAndAwait(ctx context.Context, target config.ShowActionTarget) stepResult {
	if e.brokers == nil {
		return stepResult{
			outcome:       outcomeFailed,
			outcomeState:  mqttStateUnknownBroker,
			outcomeReason: fmt.Sprintf("no integration broker is configured for this deployment; action names broker %q", target.Broker),
		}
	}

	qos := byte(target.Publish.QoS)

	if target.Expect.Kind == config.MQTTExpectKindNone {
		if err := e.brokers.Publish(ctx, target.Broker, target.Publish.Topic, qos, target.Publish.Retain, []byte(target.Publish.Payload)); err != nil {
			return e.mqttPublishErrorResult(err, target.Broker)
		}
		return stepResult{
			outcome:          outcomeUnconfirmable,
			outcomeState:     mqttStateUnconfirmableByKind,
			outcomeReason:    "this action declares no expected response",
			publishAttempted: true,
		}
	}

	req := broker.ResponseRequest{
		PublishTopic:   target.Publish.Topic,
		PublishPayload: []byte(target.Publish.Payload),
		PublishQoS:     qos,
		PublishRetain:  target.Publish.Retain,
		ResponseTopic:  target.Expect.Topic,
		ResponseQoS:    mqttResponseQoS,
		Deadline:       time.Duration(target.Expect.DeadlineSeconds) * time.Second,
		// Accept any live delivery; resolveMQTTPayload below decides
		// confirmed/negative-answer/malformed from what actually arrived.
		// See this file's own top comment and STEP-9-SPEC.md's wave 2
		// brief item 4: "write Match to accept any live delivery ... and
		// decide confirmed / negative-answer / malformed from the
		// returned payload. That is what makes 'malformed under a typed
		// contract is failed, not unconfirmed' implementable at all."
		Match: func(broker.Message) bool { return true },
	}

	msg, err := e.brokers.AwaitResponse(ctx, target.Broker, req)
	switch {
	case err == nil && msg.Retained:
		// Defense in depth. [broker.BrokerManager.AwaitResponse]'s own
		// contract already guarantees Retained is always false on a
		// successful return (broker/response.go's dispatchToWaiters
		// discards every RETAIN=1 delivery before a Matcher, or a
		// caller, ever sees it — STEP-9-SPEC.md section 7.2's own
		// "single most important line of code in this file"). This
		// package trusts that contract and does not re-implement it, but
		// still refuses to treat a retained delivery as confirmation if
		// that contract is ever violated by a future defect one layer
		// down, mirroring dispatchFPPCommand's own "never trust a
		// caller's own claim that its params were already validated"
		// precedent. This branch should never fire in production; it
		// exists so a violation resolves unconfirmed rather than a false
		// confirmed.
		e.logError("mqtt response waiter returned a retained delivery on success; treating as unconfirmed rather than trusting it",
			"broker", target.Broker, "topic", target.Expect.Topic)
		return stepResult{
			outcome:      outcomeUnconfirmed,
			outcomeState: mqttStateDeadlineExceeded,
			outcomeReason: fmt.Sprintf("the only delivery observed on %q was a retained replay, which cannot confirm this dispatch",
				target.Expect.Topic),
			publishAttempted: true,
		}
	case err == nil:
		res := resolveMQTTPayload(*target.Expect, msg.Payload)
		res.publishAttempted = true
		return res
	case errors.Is(err, broker.ErrResponseDeadlineExceeded):
		return stepResult{
			outcome:      outcomeUnconfirmed,
			outcomeState: mqttStateDeadlineExceeded,
			outcomeReason: fmt.Sprintf("no live response arrived on %q within %d seconds",
				target.Expect.Topic, target.Expect.DeadlineSeconds),
			publishAttempted: true,
		}
	case errors.Is(err, broker.ErrUnknownBroker):
		return stepResult{
			outcome:       outcomeFailed,
			outcomeState:  mqttStateUnknownBroker,
			outcomeReason: fmt.Sprintf("broker %q is not registered with this coordinator", target.Broker),
		}
	default:
		return e.mqttPublishErrorResult(err, target.Broker)
	}
}

// mqttPublishErrorResult classifies every AwaitResponse/Publish error this
// package has not already special-cased (broker.ErrBrokerUnavailable,
// broker.ErrPublishNotAuthorized, broker.ErrInvalidResponseTopic, a
// transport-level publish failure, or ctx's own cancellation) as "failed":
// STEP-9-SPEC.md section 7.3 states only two explicit mappings (deadline
// expiry is unconfirmed; ErrUnknownBroker is failed); this package's own
// judgment call, stated here and in this builder's report, is that every
// OTHER failure to even attempt the exchange is a concrete inability to
// run this step as declared, not a monitoring gap — the "unconfirmed"
// state is reserved for the one specific case ADR-031 decision 2 defines
// it around: an attempt was made and evidence for it did not arrive in
// time.
//
// It also decides whether this step ever put anything on a wire. An
// unknown identifier and a down connection both fail before any packet
// leaves the process, so neither earns a dispatchedAt; every other error
// here happened at or after the attempt, and nil-ing those would be the
// opposite lie.
func (e *Executor) mqttPublishErrorResult(err error, brokerID string) stepResult {
	attempted := !errors.Is(err, broker.ErrUnknownBroker) && !errors.Is(err, broker.ErrBrokerUnavailable)
	return stepResult{
		outcome:          outcomeFailed,
		outcomeState:     mqttStateTransportError,
		outcomeReason:    fmt.Sprintf("could not publish or subscribe on broker %q", brokerID),
		publishAttempted: attempted,
	}
}

// resolveMQTTPayload implements STEP-9-SPEC.md section 7.3's five-mode
// response contract for whatever live, non-retained payload actually
// arrived (retained deliveries never reach here — [broker.BrokerManager.AwaitResponse]
// discards them unconditionally before Match is ever consulted, per
// section 7.2 rule 2).
func resolveMQTTPayload(expect config.ShowActionMQTTExpect, payload []byte) stepResult {
	switch expect.Kind {
	case config.MQTTExpectKindBoolean:
		var b bool
		if err := json.Unmarshal(payload, &b); err != nil {
			return stepResult{outcome: outcomeFailed, outcomeState: mqttStateMalformedPayload,
				outcomeReason: "the response payload was not a valid JSON boolean"}
		}
		if !b {
			return stepResult{outcome: outcomeFailed, outcomeState: mqttStateNegativeAnswer,
				outcomeReason: "the external system reported false"}
		}
		return stepResult{outcome: outcomeConfirmed, outcomeState: mqttStateConfirmed,
			outcomeReason: "the external system reported true"}

	case config.MQTTExpectKindNumber:
		var f float64
		if err := json.Unmarshal(payload, &f); err != nil {
			return stepResult{outcome: outcomeFailed, outcomeState: mqttStateMalformedPayload,
				outcomeReason: "the response payload was not a valid JSON number"}
		}
		if expect.Value != nil {
			want, werr := strconv.ParseFloat(*expect.Value, 64)
			if werr != nil || f != want {
				return stepResult{outcome: outcomeFailed, outcomeState: mqttStateNegativeAnswer,
					outcomeReason: fmt.Sprintf("the response number %v did not equal the expected value %s", f, *expect.Value)}
			}
		}
		return stepResult{outcome: outcomeConfirmed, outcomeState: mqttStateConfirmed,
			outcomeReason: fmt.Sprintf("the external system reported %v", f)}

	case config.MQTTExpectKindText:
		if !utf8.Valid(payload) {
			return stepResult{outcome: outcomeFailed, outcomeState: mqttStateMalformedPayload,
				outcomeReason: "the response payload was not valid UTF-8"}
		}
		return stepResult{outcome: outcomeConfirmed, outcomeState: mqttStateConfirmed,
			outcomeReason: "received: " + truncateForReason(payload)}

	case config.MQTTExpectKindMatch:
		want := ""
		if expect.Value != nil {
			want = *expect.Value
		}
		if string(payload) != want {
			return stepResult{outcome: outcomeFailed, outcomeState: mqttStateNegativeAnswer,
				outcomeReason: fmt.Sprintf("the response payload %q did not equal the expected value %q", truncateForReason(payload), want)}
		}
		return stepResult{outcome: outcomeConfirmed, outcomeState: mqttStateConfirmed,
			outcomeReason: "the response payload matched the expected value"}

	default:
		// Unreachable given write-time validation of expect.kind's closed
		// enum; answered rather than silently treated as confirmed.
		return stepResult{outcome: outcomeFailed, outcomeState: "unknown_expect_kind",
			outcomeReason: fmt.Sprintf("this action's expect.kind %q is not recognized", expect.Kind)}
	}
}

func truncateForReason(payload []byte) string {
	if len(payload) <= mqttReasonMaxBytes {
		return string(payload)
	}
	return string(payload[:mqttReasonMaxBytes]) + "... (truncated)"
}

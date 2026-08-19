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

// mqttReasonMaxBytes bounds how much of a response payload lands in a
// step's operator-facing OutcomeReason, so an oversized MQTT payload
// cannot turn into an oversized database row.
const mqttReasonMaxBytes = 512

// dispatchMQTTStep writes the DISPATCH audit entry, publishes, and — for
// every expect.kind except "none" — waits for a live matching response via
// [mqttRegistry.AwaitResponse], then writes the OUTCOME audit entry. This
// package owns both audit entries itself: unlike an FPP step there is no
// pre-existing dispatch seam that writes one on its behalf.
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

	// A macro run never withholds a command because the audit store is
	// down: attribution is degraded and logged instead of refusing to
	// dispatch. See [api.FPPCommandInput.NeverWithholdOnAuditFailure] for
	// the FPP half of the same rule.
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
	// Best-effort always: the audit exemption governs whether the step
	// dispatches, never whether its outcome is worth trying to record.
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
		// Accept any live delivery; resolveMQTTPayload below classifies
		// confirmed/negative-answer/malformed from what actually arrived.
		Match: func(broker.Message) bool { return true },
	}

	msg, err := e.brokers.AwaitResponse(ctx, target.Broker, req)
	switch {
	case err == nil && msg.Retained:
		// Defense in depth: [broker.BrokerManager.AwaitResponse] should
		// never return Retained=true on success. Refuse to treat one as
		// confirmation if that contract is ever violated one layer down.
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

// mqttPublishErrorResult classifies every AwaitResponse/Publish error not
// already special-cased above as "failed": "unconfirmed" is reserved for
// an attempt that was made but whose evidence did not arrive in time.
// publishAttempted is false only for an error that fired before anything
// left this process (unknown broker, down connection, or a subscribe/
// deadline failure preceding the publish); every other error happened at
// or after the attempt.
func (e *Executor) mqttPublishErrorResult(err error, brokerID string) stepResult {
	attempted := !errors.Is(err, broker.ErrUnknownBroker) &&
		!errors.Is(err, broker.ErrBrokerUnavailable) &&
		!errors.Is(err, broker.ErrResponseFailedBeforePublish)
	return stepResult{
		outcome:          outcomeFailed,
		outcomeState:     mqttStateTransportError,
		outcomeReason:    fmt.Sprintf("could not publish or subscribe on broker %q", brokerID),
		publishAttempted: attempted,
	}
}

// resolveMQTTPayload implements the five-mode response contract for
// whatever live, non-retained payload actually arrived.
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

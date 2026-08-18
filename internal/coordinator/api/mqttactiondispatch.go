package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// DispatchMQTTAction is the mqtt action-invocation dispatch core,
// deliberately parallel to (not shared with) macro/step_mqtt.go's
// publishAndAwait/resolveMQTTPayload/mqttPublishErrorResult — cross-
// package extraction was not taken because macro imports api, not the
// reverse. The shared primitive both call, unchanged, is
// [broker.Registry] itself; TestMQTTDispatchParityBetweenMacroAndAPI
// (internal/coordinator/macro) guards the two implementations against
// drifting apart.

// MQTTBrokerRegistry mirrors internal/coordinator/macro's identical
// mqttRegistry interface one package over. *broker.Registry satisfies
// this with no adapter.
type MQTTBrokerRegistry interface {
	Publish(ctx context.Context, id, topic string, qos byte, retain bool, payload []byte) error
	AwaitResponse(ctx context.Context, id string, req broker.ResponseRequest) (broker.Message, error)
}

// mqttActionReasonMaxBytes mirrors step_mqtt.go's identical
// mqttReasonMaxBytes: bounds how much of a response payload lands in an
// operator-facing OutcomeReason.
const mqttActionReasonMaxBytes = 512

// mqttActionResponseQoS mirrors step_mqtt.go's identical mqttResponseQoS.
const mqttActionResponseQoS = 1

// MQTTActionResult is [DispatchMQTTAction]'s own outcome: Outcome is one
// of confirmed/unconfirmed/unconfirmable/failed (never "refused", which
// this function never produces). PublishAttempted is true only once
// something actually reached the wire.
type MQTTActionResult struct {
	Outcome          string
	OutcomeReason    string
	PublishAttempted bool
	ResolvedAt       time.Time
}

// DispatchMQTTAction publishes target's declared message and, for every
// expect.kind except "none", waits for a live matching response —
// STEP-9-SPEC.md section 7's own contract, applied here to one ad hoc
// invocation rather than one macro step. An unwired brokers
// (h.deps.MQTTBrokers defaults to noMQTTBrokerRegistry — see api.go) is
// detected below, before either Publish or AwaitResponse is ever called.
func DispatchMQTTAction(ctx context.Context, brokers MQTTBrokerRegistry, target config.ShowActionTarget, now func() time.Time) MQTTActionResult {
	if target.Publish == nil || target.Expect == nil {
		// Unreachable given write-time validation; answered rather than
		// panicking on a nil pointer.
		return MQTTActionResult{Outcome: outcomeWordFailed, OutcomeReason: "this mqtt action is missing its publish or expect definition", ResolvedAt: now()}
	}

	// Mirrors macro/step_mqtt.go's publishAndAwait's identical
	// e.brokers == nil check: an unwired registry never puts anything on
	// the wire, so this returns before calling Publish/AwaitResponse at
	// all, exactly like that branch does — never PublishAttempted: true
	// for a dispatch that never happened.
	if _, unwired := brokers.(noMQTTBrokerRegistry); unwired {
		return MQTTActionResult{
			Outcome:       outcomeWordFailed,
			OutcomeReason: fmt.Sprintf("no integration broker is configured for this deployment; action names broker %q", target.Broker),
			ResolvedAt:    now(),
		}
	}

	qos := byte(target.Publish.QoS)

	if target.Expect.Kind == config.MQTTExpectKindNone {
		if err := brokers.Publish(ctx, target.Broker, target.Publish.Topic, qos, target.Publish.Retain, []byte(target.Publish.Payload)); err != nil {
			return mqttActionPublishErrorResult(err, target.Broker, now())
		}
		return MQTTActionResult{Outcome: outcomeWordUnconfirmable, OutcomeReason: "this action declares no expected response", PublishAttempted: true, ResolvedAt: now()}
	}

	req := broker.ResponseRequest{
		PublishTopic:   target.Publish.Topic,
		PublishPayload: []byte(target.Publish.Payload),
		PublishQoS:     qos,
		PublishRetain:  target.Publish.Retain,
		ResponseTopic:  target.Expect.Topic,
		ResponseQoS:    mqttActionResponseQoS,
		Deadline:       time.Duration(target.Expect.DeadlineSeconds) * time.Second,
		Match:          func(broker.Message) bool { return true },
	}

	msg, err := brokers.AwaitResponse(ctx, target.Broker, req)
	switch {
	case err == nil && msg.Retained:
		// Defense in depth: AwaitResponse already guarantees Retained is
		// false on success; this refuses to treat one as confirmation if
		// that contract is ever violated one layer down.
		return MQTTActionResult{
			Outcome: outcomeWordUnconfirmed,
			OutcomeReason: fmt.Sprintf("the only delivery observed on %q was a retained replay, which cannot confirm this dispatch",
				target.Expect.Topic),
			PublishAttempted: true, ResolvedAt: now(),
		}
	case err == nil:
		res := resolveMQTTActionPayload(*target.Expect, msg.Payload)
		res.PublishAttempted = true
		res.ResolvedAt = now()
		return res
	case errors.Is(err, broker.ErrResponseDeadlineExceeded):
		return MQTTActionResult{
			Outcome: outcomeWordUnconfirmed,
			OutcomeReason: fmt.Sprintf("no live response arrived on %q within %d seconds",
				target.Expect.Topic, target.Expect.DeadlineSeconds),
			PublishAttempted: true, ResolvedAt: now(),
		}
	case errors.Is(err, broker.ErrUnknownBroker):
		return MQTTActionResult{
			Outcome:       outcomeWordFailed,
			OutcomeReason: fmt.Sprintf("broker %q is not registered with this coordinator", target.Broker),
			ResolvedAt:    now(),
		}
	default:
		return mqttActionPublishErrorResult(err, target.Broker, now())
	}
}

// mqttActionPublishErrorResult mirrors step_mqtt.go's identical
// mqttPublishErrorResult: every AwaitResponse/Publish error not already
// special-cased above is "failed", and PublishAttempted is false only for
// an error that fired before anything left this process.
func mqttActionPublishErrorResult(err error, brokerID string, resolvedAt time.Time) MQTTActionResult {
	attempted := !errors.Is(err, broker.ErrUnknownBroker) && !errors.Is(err, broker.ErrBrokerUnavailable)
	return MQTTActionResult{
		Outcome:          outcomeWordFailed,
		OutcomeReason:    fmt.Sprintf("could not publish or subscribe on broker %q", brokerID),
		PublishAttempted: attempted,
		ResolvedAt:       resolvedAt,
	}
}

// resolveMQTTActionPayload mirrors step_mqtt.go's identical
// resolveMQTTPayload: STEP-9-SPEC.md section 7.3's five-mode response
// contract for whatever live, non-retained payload actually arrived.
func resolveMQTTActionPayload(expect config.ShowActionMQTTExpect, payload []byte) MQTTActionResult {
	switch expect.Kind {
	case config.MQTTExpectKindBoolean:
		var b bool
		if err := json.Unmarshal(payload, &b); err != nil {
			return MQTTActionResult{Outcome: outcomeWordFailed, OutcomeReason: "the response payload was not a valid JSON boolean"}
		}
		if !b {
			return MQTTActionResult{Outcome: outcomeWordFailed, OutcomeReason: "the external system reported false"}
		}
		return MQTTActionResult{Outcome: outcomeWordConfirmed, OutcomeReason: "the external system reported true"}

	case config.MQTTExpectKindNumber:
		var f float64
		if err := json.Unmarshal(payload, &f); err != nil {
			return MQTTActionResult{Outcome: outcomeWordFailed, OutcomeReason: "the response payload was not a valid JSON number"}
		}
		if expect.Value != nil {
			want, werr := strconv.ParseFloat(*expect.Value, 64)
			if werr != nil || f != want {
				return MQTTActionResult{Outcome: outcomeWordFailed, OutcomeReason: fmt.Sprintf("the response number %v did not equal the expected value %s", f, *expect.Value)}
			}
		}
		return MQTTActionResult{Outcome: outcomeWordConfirmed, OutcomeReason: fmt.Sprintf("the external system reported %v", f)}

	case config.MQTTExpectKindText:
		if !utf8.Valid(payload) {
			return MQTTActionResult{Outcome: outcomeWordFailed, OutcomeReason: "the response payload was not valid UTF-8"}
		}
		return MQTTActionResult{Outcome: outcomeWordConfirmed, OutcomeReason: "received: " + truncateMQTTActionReason(payload)}

	case config.MQTTExpectKindMatch:
		want := ""
		if expect.Value != nil {
			want = *expect.Value
		}
		if string(payload) != want {
			return MQTTActionResult{Outcome: outcomeWordFailed, OutcomeReason: fmt.Sprintf("the response payload %q did not equal the expected value %q", truncateMQTTActionReason(payload), want)}
		}
		return MQTTActionResult{Outcome: outcomeWordConfirmed, OutcomeReason: "the response payload matched the expected value"}

	default:
		// Unreachable given write-time validation of expect.kind's closed
		// enum; answered rather than silently treated as confirmed.
		return MQTTActionResult{Outcome: outcomeWordFailed, OutcomeReason: fmt.Sprintf("this action's expect.kind %q is not recognized", expect.Kind)}
	}
}

func truncateMQTTActionReason(payload []byte) string {
	if len(payload) <= mqttActionReasonMaxBytes {
		return string(payload)
	}
	return string(payload[:mqttActionReasonMaxBytes]) + "... (truncated)"
}

// noMQTTBrokerRegistry is [Dependencies.MQTTBrokers]'s nil-safe default,
// matching [noResolumeActionDispatcher]'s identical "an unwired WRITE
// dependency refuses loudly, never fabricates success" posture.
type noMQTTBrokerRegistry struct{}

var errMQTTBrokersNotConfigured = errors.New("api: no MQTTBrokerRegistry was wired into this API's Dependencies")

func (noMQTTBrokerRegistry) Publish(context.Context, string, string, byte, bool, []byte) error {
	return errMQTTBrokersNotConfigured
}

func (noMQTTBrokerRegistry) AwaitResponse(context.Context, string, broker.ResponseRequest) (broker.Message, error) {
	return broker.Message{}, errMQTTBrokersNotConfigured
}

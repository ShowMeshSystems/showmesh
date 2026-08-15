package macro

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

func TestResolveMQTTPayloadBoolean(t *testing.T) {
	expect := config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean}

	res := resolveMQTTPayload(expect, []byte("true"))
	if res.outcome != outcomeConfirmed {
		t.Fatalf("true: outcome = %q, want confirmed", res.outcome)
	}

	res = resolveMQTTPayload(expect, []byte("false"))
	if res.outcome != outcomeFailed || res.outcomeState != mqttStateNegativeAnswer {
		t.Fatalf("false: outcome=%q state=%q, want failed/negative_answer", res.outcome, res.outcomeState)
	}

	res = resolveMQTTPayload(expect, []byte("not json"))
	if res.outcome != outcomeFailed || res.outcomeState != mqttStateMalformedPayload {
		t.Fatalf("malformed: outcome=%q state=%q, want failed/malformed_payload", res.outcome, res.outcomeState)
	}
}

func TestResolveMQTTPayloadNumber(t *testing.T) {
	noValue := config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNumber}
	res := resolveMQTTPayload(noValue, []byte("42"))
	if res.outcome != outcomeConfirmed {
		t.Fatalf("bare number: outcome = %q, want confirmed", res.outcome)
	}

	withValue := config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNumber, Value: strPtr("42")}
	res = resolveMQTTPayload(withValue, []byte("42"))
	if res.outcome != outcomeConfirmed {
		t.Fatalf("matching number: outcome = %q, want confirmed", res.outcome)
	}
	res = resolveMQTTPayload(withValue, []byte("43"))
	if res.outcome != outcomeFailed || res.outcomeState != mqttStateNegativeAnswer {
		t.Fatalf("mismatched number: outcome=%q state=%q, want failed/negative_answer", res.outcome, res.outcomeState)
	}
	res = resolveMQTTPayload(withValue, []byte("\"not a number\""))
	if res.outcome != outcomeFailed || res.outcomeState != mqttStateMalformedPayload {
		t.Fatalf("malformed number: outcome=%q state=%q, want failed/malformed_payload", res.outcome, res.outcomeState)
	}
}

func TestResolveMQTTPayloadText(t *testing.T) {
	expect := config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindText}
	res := resolveMQTTPayload(expect, []byte("anything at all"))
	if res.outcome != outcomeConfirmed {
		t.Fatalf("valid utf8: outcome = %q, want confirmed", res.outcome)
	}
	res = resolveMQTTPayload(expect, []byte{0xff, 0xfe, 0xfd})
	if res.outcome != outcomeFailed || res.outcomeState != mqttStateMalformedPayload {
		t.Fatalf("invalid utf8: outcome=%q state=%q, want failed/malformed_payload", res.outcome, res.outcomeState)
	}
}

func TestResolveMQTTPayloadMatch(t *testing.T) {
	expect := config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindMatch, Value: strPtr("on")}
	res := resolveMQTTPayload(expect, []byte("on"))
	if res.outcome != outcomeConfirmed {
		t.Fatalf("exact match: outcome = %q, want confirmed", res.outcome)
	}
	res = resolveMQTTPayload(expect, []byte("off"))
	if res.outcome != outcomeFailed || res.outcomeState != mqttStateNegativeAnswer {
		t.Fatalf("mismatch: outcome=%q state=%q, want failed/negative_answer", res.outcome, res.outcomeState)
	}
}

// TestExpectKindNoneNeverConfirms is acceptance criterion 5: an
// expect.kind:none step reports unconfirmable, on every run, and never
// confirmed. This is the required break-test 5 (see this builder's own
// report for the sibling break-tests): broken by making publishAndAwait
// return outcomeConfirmed for
// MQTTExpectKindNone instead of outcomeUnconfirmable, which made this
// test fail as expected before being reverted.
func TestExpectKindNoneNeverConfirms(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	published := false
	brokers := &fakeBrokers{publishFn: func(ctx context.Context, id, topic string, qos byte, retain bool, payload []byte) error {
		published = true
		return nil
	}}
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, brokers)

	putAction(t, st, "a1", mqttAction("home-automation", "none", config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNone}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	run := submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if !published {
		t.Fatalf("expect.kind:none must still publish")
	}
	if run.Steps[0].Outcome != outcomeUnconfirmable {
		t.Fatalf("outcome = %q, want %q", run.Steps[0].Outcome, outcomeUnconfirmable)
	}
	if run.Steps[0].Outcome == outcomeConfirmed {
		t.Fatalf("expect.kind:none must never report confirmed")
	}
}

// TestMQTTUnknownBrokerIsFailedNotUnconfirmed pins STEP-9-SPEC.md wave 2
// brief item 4's explicit rule: broker.ErrUnknownBroker is failed, with a
// reason naming the broker identifier, never unconfirmed.
func TestMQTTUnknownBrokerIsFailedNotUnconfirmed(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	brokers := &fakeBrokers{awaitFn: func(ctx context.Context, id string, req broker.ResponseRequest) (broker.Message, error) {
		return broker.Message{}, broker.ErrUnknownBroker
	}}
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, brokers)

	putAction(t, st, "a1", mqttAction("nonexistent-broker", "none",
		config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	run := submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if run.Steps[0].Outcome != outcomeFailed {
		t.Fatalf("outcome = %q, want %q", run.Steps[0].Outcome, outcomeFailed)
	}
}

// TestMQTTDeadlineExceededIsUnconfirmed pins the other explicit rule from
// the same item: deadline expiry is unconfirmed, not failed.
func TestMQTTDeadlineExceededIsUnconfirmed(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	brokers := &fakeBrokers{awaitFn: func(ctx context.Context, id string, req broker.ResponseRequest) (broker.Message, error) {
		return broker.Message{}, broker.ErrResponseDeadlineExceeded
	}}
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, brokers)

	putAction(t, st, "a1", mqttAction("home-automation", "none",
		config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	run := submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if run.Steps[0].Outcome != outcomeUnconfirmed {
		t.Fatalf("outcome = %q, want %q", run.Steps[0].Outcome, outcomeUnconfirmed)
	}
}

// TestMQTTRetainedDeliveryDoesNotConfirm is this package's own
// defense-in-depth over [broker.BrokerManager.AwaitResponse]'s contract
// (see publishAndAwait's own doc comment): even though AwaitResponse
// itself guarantees Retained is always false on success, this package
// does not trust that unconditionally — if a fake (standing in for a
// future defect one layer down) violates it, the step must still resolve
// unconfirmed, never confirmed. This is this task's own required
// break-test 4 (STEP-9-SPEC.md section 7.2's retained-message trap, "the
// discard path resolving unconfirmed rather than confirmed"): see this
// builder's own report for what was changed in step_mqtt.go to make this
// test fail, and that it did.
func TestMQTTRetainedDeliveryDoesNotConfirm(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	brokers := &fakeBrokers{awaitFn: func(ctx context.Context, id string, req broker.ResponseRequest) (broker.Message, error) {
		return broker.Message{Topic: req.ResponseTopic, Payload: []byte("true"), Retained: true}, nil
	}}
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, brokers)

	putAction(t, st, "a1", mqttAction("home-automation", "none",
		config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	run := submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if run.Steps[0].Outcome == outcomeConfirmed {
		t.Fatalf("a retained delivery must never confirm a step, even one carrying a true/matching payload")
	}
	if run.Steps[0].Outcome != outcomeUnconfirmed {
		t.Fatalf("outcome = %q, want %q", run.Steps[0].Outcome, outcomeUnconfirmed)
	}
}

// TestMQTTBrokerRoutingUsesOneBrokerForBothPublishAndWait is acceptance
// criterion 19's unit-level slice: Registry.AwaitResponse is called with
// the action's own declared broker identifier, never a hardcoded or
// mismatched one.
func TestMQTTBrokerRoutingUsesOneBrokerForBothPublishAndWait(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	var gotBrokerID string
	brokers := &fakeBrokers{awaitFn: func(ctx context.Context, id string, req broker.ResponseRequest) (broker.Message, error) {
		gotBrokerID = id
		return broker.Message{Topic: req.ResponseTopic, Payload: []byte("true")}, nil
	}}
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, brokers)

	putAction(t, st, "a1", mqttAction("broker-b", "none",
		config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5}))
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	submitAndWait(t, e, api.MacroSubmitRequest{MacroObjectID: "m1", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer()})

	if gotBrokerID != "broker-b" {
		t.Fatalf("AwaitResponse called with broker %q, want %q", gotBrokerID, "broker-b")
	}
}

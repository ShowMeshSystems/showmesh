package macro

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// This file is the guard the E7-1 review named: without it, this
// package's own publishAndAwait and api.DispatchMQTTAction are a second
// "precedence.go" — two independently-written implementations of the
// identical publish/await/classify contract that prose comments merely
// claim agree. This drives BOTH over one input table and asserts equal
// (outcome, reason, publishAttempted) for every case.

// dualFakeMQTTBroker implements both this package's own mqttRegistry and
// api.MQTTBrokerRegistry — the two interfaces are structurally identical
// (same two methods, same signatures), so one fake satisfies both with no
// adapter, the same property this seam's own report already notes for
// *broker.Registry itself.
type dualFakeMQTTBroker struct {
	msg broker.Message
	err error
}

func (f *dualFakeMQTTBroker) Publish(context.Context, string, string, byte, bool, []byte) error {
	return f.err
}

func (f *dualFakeMQTTBroker) AwaitResponse(context.Context, string, broker.ResponseRequest) (broker.Message, error) {
	if f.err != nil {
		return broker.Message{}, f.err
	}
	return f.msg, nil
}

func strPtrParity(s string) *string { return &s }

func TestMQTTDispatchParityBetweenMacroAndAPI(t *testing.T) {
	fixedNow := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixedNow }

	cases := []struct {
		name   string
		target config.ShowActionTarget
		broker *dualFakeMQTTBroker
	}{
		{
			name: "kind none publishes and reports unconfirmable",
			target: config.ShowActionTarget{
				Broker:  "home-automation",
				Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
				Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNone},
			},
			broker: &dualFakeMQTTBroker{},
		},
		{
			name: "boolean true confirms",
			target: config.ShowActionTarget{
				Broker:  "home-automation",
				Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
				Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5},
			},
			broker: &dualFakeMQTTBroker{msg: broker.Message{Payload: []byte("true")}},
		},
		{
			name: "boolean false fails",
			target: config.ShowActionTarget{
				Broker:  "home-automation",
				Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
				Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5},
			},
			broker: &dualFakeMQTTBroker{msg: broker.Message{Payload: []byte("false")}},
		},
		{
			name: "match mismatch fails",
			target: config.ShowActionTarget{
				Broker:  "home-automation",
				Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
				Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindMatch, Topic: "resp", Value: strPtrParity("on"), DeadlineSeconds: 5},
			},
			broker: &dualFakeMQTTBroker{msg: broker.Message{Payload: []byte("off")}},
		},
		{
			name: "match exact confirms",
			target: config.ShowActionTarget{
				Broker:  "home-automation",
				Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
				Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindMatch, Topic: "resp", Value: strPtrParity("on"), DeadlineSeconds: 5},
			},
			broker: &dualFakeMQTTBroker{msg: broker.Message{Payload: []byte("on")}},
		},
		{
			name: "retained delivery is unconfirmed defense in depth",
			target: config.ShowActionTarget{
				Broker:  "home-automation",
				Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
				Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5},
			},
			broker: &dualFakeMQTTBroker{msg: broker.Message{Payload: []byte("true"), Retained: true}},
		},
		{
			name: "deadline exceeded is unconfirmed and attempted",
			target: config.ShowActionTarget{
				Broker:  "home-automation",
				Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
				Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5},
			},
			broker: &dualFakeMQTTBroker{err: broker.ErrResponseDeadlineExceeded},
		},
		{
			name: "unknown broker is failed and never attempted",
			target: config.ShowActionTarget{
				Broker:  "gone",
				Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
				Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5},
			},
			broker: &dualFakeMQTTBroker{err: broker.ErrUnknownBroker},
		},
		{
			name: "broker unavailable is failed and never attempted",
			target: config.ShowActionTarget{
				Broker:  "home-automation",
				Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
				Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5},
			},
			broker: &dualFakeMQTTBroker{err: broker.ErrBrokerUnavailable},
		},
		{
			name: "an unrecognized transport error is failed but attempted",
			target: config.ShowActionTarget{
				Broker:  "home-automation",
				Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
				Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5},
			},
			broker: &dualFakeMQTTBroker{err: errors.New("connection reset")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Executor{brokers: tc.broker, logger: testLogger()}
			macroRes := e.publishAndAwait(context.Background(), tc.target)

			apiRes := api.DispatchMQTTAction(context.Background(), tc.broker, tc.target, now)

			if macroRes.outcome != apiRes.Outcome {
				t.Errorf("outcome: macro = %q, api = %q, want equal", macroRes.outcome, apiRes.Outcome)
			}
			if macroRes.outcomeReason != apiRes.OutcomeReason {
				t.Errorf("outcomeReason: macro = %q, api = %q, want equal", macroRes.outcomeReason, apiRes.OutcomeReason)
			}
			if macroRes.publishAttempted != apiRes.PublishAttempted {
				t.Errorf("publishAttempted: macro = %v, api = %v, want equal", macroRes.publishAttempted, apiRes.PublishAttempted)
			}
		})
	}
}

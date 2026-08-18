package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

func mqttTarget(kind string, value *string) config.ShowActionTarget {
	return config.ShowActionTarget{
		Integration: config.ShowActionIntegrationMQTT, Broker: "home-automation",
		Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
		Expect:  &config.ShowActionMQTTExpect{Kind: kind, Topic: "resp", Value: value, DeadlineSeconds: 5},
	}
}

func strp(s string) *string { return &s }

func TestDispatchMQTTActionNoneIsUnconfirmable(t *testing.T) {
	brokers := &fakeMQTTBrokerRegistry{}
	target := config.ShowActionTarget{
		Integration: config.ShowActionIntegrationMQTT, Broker: "home-automation",
		Publish: &config.ShowActionMQTTPublish{Topic: "cmd", Payload: "1", QoS: 1},
		Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNone},
	}
	res := DispatchMQTTAction(context.Background(), brokers, target, func() time.Time { return testNow })
	if res.Outcome != outcomeWordUnconfirmable {
		t.Fatalf("Outcome = %q, want unconfirmable", res.Outcome)
	}
	if !res.PublishAttempted {
		t.Fatalf("PublishAttempted = false, want true")
	}
}

func TestDispatchMQTTActionBooleanConfirmedAndFailed(t *testing.T) {
	brokers := &fakeMQTTBrokerRegistry{msg: broker.Message{Payload: []byte("true")}}
	res := DispatchMQTTAction(context.Background(), brokers, mqttTarget(config.MQTTExpectKindBoolean, nil), func() time.Time { return testNow })
	if res.Outcome != outcomeWordConfirmed {
		t.Fatalf("Outcome = %q, want confirmed", res.Outcome)
	}

	brokers2 := &fakeMQTTBrokerRegistry{msg: broker.Message{Payload: []byte("false")}}
	res2 := DispatchMQTTAction(context.Background(), brokers2, mqttTarget(config.MQTTExpectKindBoolean, nil), func() time.Time { return testNow })
	if res2.Outcome != outcomeWordFailed {
		t.Fatalf("Outcome = %q, want failed for a false answer", res2.Outcome)
	}
}

func TestDispatchMQTTActionMatchConfirmedAndFailed(t *testing.T) {
	brokers := &fakeMQTTBrokerRegistry{msg: broker.Message{Payload: []byte("on")}}
	res := DispatchMQTTAction(context.Background(), brokers, mqttTarget(config.MQTTExpectKindMatch, strp("on")), func() time.Time { return testNow })
	if res.Outcome != outcomeWordConfirmed {
		t.Fatalf("Outcome = %q, want confirmed", res.Outcome)
	}

	res2 := DispatchMQTTAction(context.Background(), brokers, mqttTarget(config.MQTTExpectKindMatch, strp("off")), func() time.Time { return testNow })
	if res2.Outcome != outcomeWordFailed {
		t.Fatalf("Outcome = %q, want failed for a mismatched value", res2.Outcome)
	}
}

func TestDispatchMQTTActionDeadlineExceededIsUnconfirmed(t *testing.T) {
	brokers := &fakeMQTTBrokerRegistry{err: broker.ErrResponseDeadlineExceeded}
	res := DispatchMQTTAction(context.Background(), brokers, mqttTarget(config.MQTTExpectKindText, nil), func() time.Time { return testNow })
	if res.Outcome != outcomeWordUnconfirmed {
		t.Fatalf("Outcome = %q, want unconfirmed", res.Outcome)
	}
	if !res.PublishAttempted {
		t.Fatalf("PublishAttempted = false, want true (the publish leg still ran)")
	}
}

func TestDispatchMQTTActionUnknownBrokerIsFailedAndNeverAttempted(t *testing.T) {
	brokers := &fakeMQTTBrokerRegistry{err: broker.ErrUnknownBroker}
	res := DispatchMQTTAction(context.Background(), brokers, mqttTarget(config.MQTTExpectKindText, nil), func() time.Time { return testNow })
	if res.Outcome != outcomeWordFailed {
		t.Fatalf("Outcome = %q, want failed", res.Outcome)
	}
	if res.PublishAttempted {
		t.Fatalf("PublishAttempted = true, want false (an unknown broker never puts anything on the wire)")
	}
}

func TestDispatchMQTTActionRetainedDeliveryIsUnconfirmedNotConfirmed(t *testing.T) {
	brokers := &fakeMQTTBrokerRegistry{msg: broker.Message{Payload: []byte("true"), Retained: true}}
	res := DispatchMQTTAction(context.Background(), brokers, mqttTarget(config.MQTTExpectKindBoolean, nil), func() time.Time { return testNow })
	if res.Outcome != outcomeWordUnconfirmed {
		t.Fatalf("Outcome = %q, want unconfirmed (defense in depth against a retained delivery)", res.Outcome)
	}
}

package agent

import (
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// TestBuildWillMessageTopicRetainQoSAndPayload covers the Task D spec's
// "the will payload encodes correctly and is the right topic, retain, and
// QoS" requirement without dialing a broker: buildWillMessage is a pure
// function of nodeID (aside from wall-clock SentAt), so it is tested
// directly rather than through newMQTTConn (which does start a real
// autopaho connection attempt and is exercised only by the integration
// task per CLAUDE.md's Step 2 build order).
func TestBuildWillMessageTopicRetainQoSAndPayload(t *testing.T) {
	will, err := buildWillMessage("media-03")
	if err != nil {
		t.Fatalf("buildWillMessage() error = %v", err)
	}

	wantTopic, err := mqttproto.LWTTopic("media-03")
	if err != nil {
		t.Fatalf("LWTTopic() error = %v", err)
	}
	if will.Topic != wantTopic {
		t.Errorf("Topic = %q, want %q", will.Topic, wantTopic)
	}
	if will.QoS != mqttproto.LWTDeliveryPolicy.QoS {
		t.Errorf("QoS = %d, want %d", will.QoS, mqttproto.LWTDeliveryPolicy.QoS)
	}
	if will.Retain != mqttproto.LWTDeliveryPolicy.Retain {
		t.Errorf("Retain = %v, want %v (mqttproto.LWTDeliveryPolicy.Retain)", will.Retain, mqttproto.LWTDeliveryPolicy.Retain)
	}

	env, err := mqttproto.DecodeEnvelope(will.Payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope(will.Payload) error = %v", err)
	}
	if env.NodeID != "media-03" {
		t.Errorf("envelope NodeID = %q, want %q", env.NodeID, "media-03")
	}
	lwt, err := mqttproto.DecodeLWTPayload(env)
	if err != nil {
		t.Fatalf("DecodeLWTPayload() error = %v", err)
	}
	if lwt.Online {
		t.Errorf("Online = true, want false: a Last Will always represents 'not online'")
	}
	if lwt.Reason == "" {
		t.Errorf("Reason is empty, want a description of an unexpected disconnect")
	}
}

func TestBuildWillMessageInvalidNodeID(t *testing.T) {
	if _, err := buildWillMessage(""); err == nil {
		t.Fatalf("buildWillMessage(\"\") error = nil, want an error for an invalid node ID")
	}
}

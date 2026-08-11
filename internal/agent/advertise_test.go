package agent

import (
	"context"
	"testing"
	"time"

	agentconfig "github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/pkg/capability"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

func decodeHello(t *testing.T, payload []byte) (mqttproto.Envelope, mqttproto.HelloPayload) {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	hello, err := mqttproto.DecodeHelloPayload(env)
	if err != nil {
		t.Fatalf("DecodeHelloPayload() error = %v", err)
	}
	return env, hello
}

func decodeLWT(t *testing.T, payload []byte) (mqttproto.Envelope, mqttproto.LWTPayload) {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	lwt, err := mqttproto.DecodeLWTPayload(env)
	if err != nil {
		t.Fatalf("DecodeLWTPayload() error = %v", err)
	}
	return env, lwt
}

func TestPublishHelloTopicRetainQoSAndPayload(t *testing.T) {
	pub := newFakePublisher()
	cfg := agentconfig.Config{
		NodeID:    "media-03",
		NodeLabel: "Media Node 03",
		// Capabilities intentionally left empty: this is the production
		// default (see Config.Capabilities' doc comment).
	}
	startedAt := time.Date(2026, 8, 10, 11, 0, 0, 0, time.UTC)

	if err := publishHello(context.Background(), pub, cfg, "boot-1", startedAt); err != nil {
		t.Fatalf("publishHello() error = %v", err)
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	c := calls[0]

	wantTopic, err := mqttproto.HelloTopic("media-03")
	if err != nil {
		t.Fatalf("HelloTopic() error = %v", err)
	}
	if c.topic != wantTopic {
		t.Errorf("topic = %q, want %q", c.topic, wantTopic)
	}
	if c.qos != mqttproto.HelloDeliveryPolicy.QoS {
		t.Errorf("qos = %d, want %d", c.qos, mqttproto.HelloDeliveryPolicy.QoS)
	}
	if c.retain != mqttproto.HelloDeliveryPolicy.Retain {
		t.Errorf("retain = %v, want %v", c.retain, mqttproto.HelloDeliveryPolicy.Retain)
	}

	_, hello := decodeHello(t, c.payload)
	if hello.Label != "Media Node 03" {
		t.Errorf("Label = %q, want %q", hello.Label, "Media Node 03")
	}
	if hello.BootID != "boot-1" {
		t.Errorf("BootID = %q, want %q", hello.BootID, "boot-1")
	}
	if !hello.StartedAt.Equal(startedAt) {
		t.Errorf("StartedAt = %v, want %v", hello.StartedAt, startedAt)
	}
	if len(hello.Capabilities) != 0 {
		t.Errorf("Capabilities = %v, want empty (advertising a capability the agent does not have is exactly what this must never do)", hello.Capabilities)
	}
}

func TestPublishHelloAdvertisesConfiguredCapabilities(t *testing.T) {
	pub := newFakePublisher()
	cfg := agentconfig.Config{
		NodeID:       "media-03",
		Capabilities: capability.Set{{ID: "matrix.render", Version: 1}},
	}

	if err := publishHello(context.Background(), pub, cfg, "boot-1", time.Now()); err != nil {
		t.Fatalf("publishHello() error = %v", err)
	}

	_, hello := decodeHello(t, pub.snapshot()[0].payload)
	if len(hello.Capabilities) != 1 || hello.Capabilities[0].ID != "matrix.render" {
		t.Errorf("Capabilities = %v, want the one configured capability to be advertised (this env var exists precisely to allow that override)", hello.Capabilities)
	}
}

func TestPublishOnlineTopicRetainQoSAndPayload(t *testing.T) {
	pub := newFakePublisher()

	if err := publishOnline(context.Background(), pub, "media-03"); err != nil {
		t.Fatalf("publishOnline() error = %v", err)
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	c := calls[0]

	wantTopic, err := mqttproto.LWTTopic("media-03")
	if err != nil {
		t.Fatalf("LWTTopic() error = %v", err)
	}
	if c.topic != wantTopic {
		t.Errorf("topic = %q, want %q", c.topic, wantTopic)
	}
	if c.qos != mqttproto.LWTDeliveryPolicy.QoS {
		t.Errorf("qos = %d, want %d", c.qos, mqttproto.LWTDeliveryPolicy.QoS)
	}
	if c.retain != mqttproto.LWTDeliveryPolicy.Retain {
		t.Errorf("retain = %v, want %v (mqttproto.LWTDeliveryPolicy.Retain)", c.retain, mqttproto.LWTDeliveryPolicy.Retain)
	}

	_, lwt := decodeLWT(t, c.payload)
	if !lwt.Online {
		t.Errorf("Online = false, want true")
	}
}

func TestPublishOfflineTopicRetainQoSAndPayload(t *testing.T) {
	pub := newFakePublisher()

	if err := publishOffline(context.Background(), pub, "media-03", "clean shutdown"); err != nil {
		t.Fatalf("publishOffline() error = %v", err)
	}

	c := pub.snapshot()[0]
	wantTopic, _ := mqttproto.LWTTopic("media-03")
	if c.topic != wantTopic {
		t.Errorf("topic = %q, want %q", c.topic, wantTopic)
	}
	if c.qos != mqttproto.LWTDeliveryPolicy.QoS {
		t.Errorf("qos = %d, want %d", c.qos, mqttproto.LWTDeliveryPolicy.QoS)
	}
	if c.retain != mqttproto.LWTDeliveryPolicy.Retain {
		t.Errorf("retain = %v, want %v (mqttproto.LWTDeliveryPolicy.Retain)", c.retain, mqttproto.LWTDeliveryPolicy.Retain)
	}

	_, lwt := decodeLWT(t, c.payload)
	if lwt.Online {
		t.Errorf("Online = true, want false")
	}
	if lwt.Reason != "clean shutdown" {
		t.Errorf("Reason = %q, want %q", lwt.Reason, "clean shutdown")
	}
}

func TestPublishAdvertisementPublishesHelloThenOnline(t *testing.T) {
	pub := newFakePublisher()
	cfg := agentconfig.Config{NodeID: "media-03"}

	publishAdvertisement(context.Background(), pub, cfg, "boot-1", time.Now(), discardLogger())

	calls := pub.snapshot()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2 (hello, then lwt online=true)", len(calls))
	}

	helloTopic, _ := mqttproto.HelloTopic("media-03")
	lwtTopic, _ := mqttproto.LWTTopic("media-03")

	if calls[0].topic != helloTopic {
		t.Errorf("first publish topic = %q, want hello topic %q", calls[0].topic, helloTopic)
	}
	if calls[1].topic != lwtTopic {
		t.Errorf("second publish topic = %q, want lwt topic %q", calls[1].topic, lwtTopic)
	}
}

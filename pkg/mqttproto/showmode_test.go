package mqttproto

import (
	"strings"
	"testing"
	"time"
)

func TestShowModeTopicIsOneRetainedEventTopic(t *testing.T) {
	topic := ShowModeTopic()
	if topic != "showmesh/events/show_mode" {
		t.Fatalf("ShowModeTopic() = %q", topic)
	}
	// It has to parse as a topic this package already understands, so a
	// subscriber on the events family is not surprised by it.
	parsed, err := ParseTopic(topic)
	if err != nil {
		t.Fatalf("ParseTopic(%q): %v", topic, err)
	}
	if parsed.Kind != TopicKindEvent {
		t.Fatalf("ParseTopic(%q).Kind = %v, want Event", topic, parsed.Kind)
	}
	if parsed.NodeID != "" {
		t.Fatalf("ParseTopic(%q).NodeID = %q, want empty: the mode is not node-scoped", topic, parsed.NodeID)
	}
	// Retained, because the mode is state: a node connecting after the mode
	// was last set must learn it immediately.
	if !ShowModeDeliveryPolicy.Retain || ShowModeDeliveryPolicy.QoS != 1 {
		t.Fatalf("ShowModeDeliveryPolicy = %+v, want retained QoS 1", ShowModeDeliveryPolicy)
	}
}

func TestNewShowModeMessageRoundTrips(t *testing.T) {
	now := time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC)
	for _, mode := range []string{ShowModeProgram, ShowModeShow} {
		m, err := NewShowModeMessage(mode, 7, now)
		if err != nil {
			t.Fatalf("NewShowModeMessage(%q): %v", mode, err)
		}
		raw, err := EncodeShowModeMessage(m)
		if err != nil {
			t.Fatalf("EncodeShowModeMessage: %v", err)
		}
		back, err := DecodeShowModeMessage(raw)
		if err != nil {
			t.Fatalf("DecodeShowModeMessage(%s): %v", raw, err)
		}
		if back.Mode != mode || back.Revision != 7 || back.Schema != SchemaShowModeV1 {
			t.Fatalf("round trip produced %+v", back)
		}
		if !back.PublishedAt.Equal(now) {
			t.Fatalf("publishedAt = %v, want %v", back.PublishedAt, now)
		}
	}
}

// "unknown" is a receiver's word for a mode it was never told. Nothing may
// ever publish it, so neither the constructor nor the decoder accepts it.
func TestShowModeMessageRefusesUnknownAndOtherNonMembers(t *testing.T) {
	now := time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC)
	for _, mode := range []string{"unknown", "", "setup", "SHOW"} {
		if _, err := NewShowModeMessage(mode, 0, now); err == nil {
			t.Fatalf("NewShowModeMessage(%q) accepted a non-member", mode)
		}
	}
	raw := `{"schema":"showmesh.showmode/v1","messageId":"m1","mode":"unknown","revision":1,"publishedAt":"2026-08-23T21:00:00Z"}`
	if _, err := DecodeShowModeMessage([]byte(raw)); err == nil {
		t.Fatal("DecodeShowModeMessage accepted mode unknown")
	}
}

func TestDecodeShowModeMessageRefusesMalformedPayloads(t *testing.T) {
	cases := map[string]string{
		"empty":          ``,
		"whitespace":     `   `,
		"not json":       `{`,
		"wrong schema":   `{"schema":"showmesh.node.hello/v1","messageId":"m1","mode":"show","publishedAt":"2026-08-23T21:00:00Z"}`,
		"no messageId":   `{"schema":"showmesh.showmode/v1","mode":"show","publishedAt":"2026-08-23T21:00:00Z"}`,
		"zero published": `{"schema":"showmesh.showmode/v1","messageId":"m1","mode":"show"}`,
		"negative rev":   `{"schema":"showmesh.showmode/v1","messageId":"m1","mode":"show","revision":-1,"publishedAt":"2026-08-23T21:00:00Z"}`,
	}
	for name, raw := range cases {
		if _, err := DecodeShowModeMessage([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeShowModeMessage accepted %q", name, raw)
		}
	}
}

// Additive schema posture: an unknown field is tolerated, matching
// DecodeEnvelope.
func TestDecodeShowModeMessageToleratesUnknownFields(t *testing.T) {
	raw := `{"schema":"showmesh.showmode/v1","messageId":"m1","mode":"show","revision":2,` +
		`"publishedAt":"2026-08-23T21:00:00Z","somethingLater":{"a":1}}`
	m, err := DecodeShowModeMessage([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeShowModeMessage: %v", err)
	}
	if m.Mode != ShowModeShow {
		t.Fatalf("mode = %q", m.Mode)
	}
}

func TestEncodeShowModeMessageRefusesToPublishAnInvalidMessage(t *testing.T) {
	_, err := EncodeShowModeMessage(ShowModeMessage{Schema: SchemaShowModeV1, MessageID: "m1", Mode: "unknown"})
	if err == nil {
		t.Fatal("EncodeShowModeMessage accepted an invalid message")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Fatalf("error = %v, want it to name the offending field", err)
	}
}

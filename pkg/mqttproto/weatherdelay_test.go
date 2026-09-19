package mqttproto

import (
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// TestWeatherDelayKindsMatchPkgWeatherdelay keeps the copied kind literals
// equal to pkg/weatherdelay's.
func TestWeatherDelayKindsMatchPkgWeatherdelay(t *testing.T) {
	if WeatherDelayKindDelay != weatherdelay.KindDelay {
		t.Fatalf("WeatherDelayKindDelay = %q, want %q", WeatherDelayKindDelay, weatherdelay.KindDelay)
	}
	if WeatherDelayKindCancelNight != weatherdelay.KindCancelNight {
		t.Fatalf("WeatherDelayKindCancelNight = %q, want %q", WeatherDelayKindCancelNight, weatherdelay.KindCancelNight)
	}
}

func TestWeatherDelayTopicIsOneRetainedEventTopic(t *testing.T) {
	topic := WeatherDelayTopic()
	if topic != "showmesh/events/weather_delay" {
		t.Fatalf("WeatherDelayTopic() = %q", topic)
	}
	parsed, err := ParseTopic(topic)
	if err != nil {
		t.Fatalf("ParseTopic(%q): %v", topic, err)
	}
	if parsed.Kind != TopicKindEvent || parsed.NodeID != "" {
		t.Fatalf("ParseTopic(%q) = %+v, want an unscoped Event topic", topic, parsed)
	}
	if !WeatherDelayDeliveryPolicy.Retain || WeatherDelayDeliveryPolicy.QoS != 1 {
		t.Fatalf("WeatherDelayDeliveryPolicy = %+v, want retained QoS 1", WeatherDelayDeliveryPolicy)
	}
}

func TestWeatherDelayDarkTopicIsPerGroupAndNotRetained(t *testing.T) {
	topic, err := WeatherDelayDarkTopic("lighting")
	if err != nil {
		t.Fatalf("WeatherDelayDarkTopic: %v", err)
	}
	if topic != "showmesh/events/weather_delay/dark/lighting" {
		t.Fatalf("WeatherDelayDarkTopic(%q) = %q", "lighting", topic)
	}
	parsed, err := ParseTopic(topic)
	if err != nil {
		t.Fatalf("ParseTopic(%q): %v", topic, err)
	}
	if parsed.Kind != TopicKindEvent {
		t.Fatalf("ParseTopic(%q).Kind = %v, want Event", topic, parsed.Kind)
	}
	if WeatherDelayDarkDeliveryPolicy.Retain || WeatherDelayDarkDeliveryPolicy.QoS != 1 {
		t.Fatalf("WeatherDelayDarkDeliveryPolicy = %+v, want non-retained QoS 1", WeatherDelayDarkDeliveryPolicy)
	}
}

func TestWeatherDelayDarkTopicRejectsInvalidGroupID(t *testing.T) {
	if _, err := WeatherDelayDarkTopic(""); err == nil {
		t.Fatal("WeatherDelayDarkTopic(\"\") accepted an empty group id")
	}
	if _, err := WeatherDelayDarkTopic("bad/group"); err == nil {
		t.Fatal("WeatherDelayDarkTopic accepted a group id containing '/'")
	}
}

func TestNewWeatherDelayMessageRoundTripsActive(t *testing.T) {
	now := time.Date(2026, 9, 19, 21, 0, 0, 0, time.UTC)
	started := now.Add(-time.Minute)
	plan := WeatherDelayPlan{
		Delay:       &WeatherDelayAlertAssetRef{AssetID: "asset-delay", ContentHash: "sha256:abc", Filename: "delay.wav"},
		CancelNight: &WeatherDelayAlertAssetRef{AssetID: "asset-cancel", ContentHash: "sha256:def", Filename: "cancel.wav"},
		RepeatCount: 10, NodeIDs: []string{"node-a"},
	}
	m, err := NewWeatherDelayMessage(true, WeatherDelayKindDelay, started, "op-1", 3, plan, now)
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage: %v", err)
	}
	raw, err := EncodeWeatherDelayMessage(m)
	if err != nil {
		t.Fatalf("EncodeWeatherDelayMessage: %v", err)
	}
	back, err := DecodeWeatherDelayMessage(raw)
	if err != nil {
		t.Fatalf("DecodeWeatherDelayMessage(%s): %v", raw, err)
	}
	if !back.Active || back.Kind != WeatherDelayKindDelay || back.StartedBy != "op-1" || back.Revision != 3 {
		t.Fatalf("round trip produced %+v", back)
	}
	if !back.StartedAt.Equal(started) {
		t.Fatalf("startedAt = %v, want %v", back.StartedAt, started)
	}
	if back.Plan.Delay == nil || *back.Plan.Delay != *plan.Delay {
		t.Fatalf("Plan.Delay = %+v, want %+v", back.Plan.Delay, plan.Delay)
	}
	if back.Plan.CancelNight == nil || *back.Plan.CancelNight != *plan.CancelNight {
		t.Fatalf("Plan.CancelNight = %+v, want %+v", back.Plan.CancelNight, plan.CancelNight)
	}
	if back.Plan.RepeatCount != 10 || len(back.Plan.NodeIDs) != 1 || back.Plan.NodeIDs[0] != "node-a" {
		t.Fatalf("Plan = %+v", back.Plan)
	}
}

func TestNewWeatherDelayMessageRoundTripsNotActive(t *testing.T) {
	now := time.Date(2026, 9, 19, 21, 0, 0, 0, time.UTC)
	m, err := NewWeatherDelayMessage(false, "", time.Time{}, "", 0, WeatherDelayPlan{}, now)
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage: %v", err)
	}
	raw, err := EncodeWeatherDelayMessage(m)
	if err != nil {
		t.Fatalf("EncodeWeatherDelayMessage: %v", err)
	}
	back, err := DecodeWeatherDelayMessage(raw)
	if err != nil {
		t.Fatalf("DecodeWeatherDelayMessage(%s): %v", raw, err)
	}
	if back.Active || back.Kind != "" || back.StartedBy != "" {
		t.Fatalf("round trip produced %+v, want a fully cleared state", back)
	}
	if back.Plan.Delay != nil || back.Plan.CancelNight != nil || back.Plan.RepeatCount != 0 {
		t.Fatalf("Plan = %+v, want the absent zero plan", back.Plan)
	}
}

func TestNewWeatherDelayMessageRejectsActiveMissingFields(t *testing.T) {
	now := time.Date(2026, 9, 19, 21, 0, 0, 0, time.UTC)
	if _, err := NewWeatherDelayMessage(true, "resume", now, "op-1", 1, WeatherDelayPlan{}, now); err == nil {
		t.Fatal("accepted a kind outside the closed vocabulary")
	}
	if _, err := NewWeatherDelayMessage(true, WeatherDelayKindDelay, time.Time{}, "op-1", 1, WeatherDelayPlan{}, now); err == nil {
		t.Fatal("accepted a zero startedAt while active")
	}
	if _, err := NewWeatherDelayMessage(true, WeatherDelayKindDelay, now, "", 1, WeatherDelayPlan{}, now); err == nil {
		t.Fatal("accepted an empty startedBy while active")
	}
}

func TestWeatherDelayPlanValidateAcceptsAbsentPlanAndEntries(t *testing.T) {
	if err := (WeatherDelayPlan{}).Validate(); err != nil {
		t.Fatalf("Validate() on the zero plan = %v, want nil", err)
	}
	if err := (WeatherDelayPlan{RepeatCount: 10}).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestWeatherDelayPlanValidateRejectsRepeatCountOutOfRange(t *testing.T) {
	for _, n := range []int{-1, 51} {
		if err := (WeatherDelayPlan{RepeatCount: n}).Validate(); err == nil {
			t.Fatalf("Validate() with repeatCount %d = nil, want error", n)
		}
	}
}

func TestWeatherDelayPlanValidateRejectsIncompleteAssetRef(t *testing.T) {
	cases := []WeatherDelayAlertAssetRef{
		{ContentHash: "h", Filename: "f"},
		{AssetID: "a", Filename: "f"},
		{AssetID: "a", ContentHash: "h"},
	}
	for _, ref := range cases {
		ref := ref
		if err := (WeatherDelayPlan{Delay: &ref}).Validate(); err == nil {
			t.Fatalf("Validate() with incomplete delay ref %+v = nil, want error", ref)
		}
		if err := (WeatherDelayPlan{CancelNight: &ref}).Validate(); err == nil {
			t.Fatalf("Validate() with incomplete cancelNight ref %+v = nil, want error", ref)
		}
	}
}

func TestDecodeWeatherDelayMessageRefusesMalformedPayloads(t *testing.T) {
	cases := map[string]string{
		"empty":           ``,
		"not json":        `{`,
		"wrong schema":    `{"schema":"showmesh.showmode/v1","messageId":"m1","active":false,"revision":0,"publishedAt":"2026-09-19T21:00:00Z"}`,
		"no messageId":    `{"schema":"showmesh.weatherdelay/v1","active":false,"revision":0,"publishedAt":"2026-09-19T21:00:00Z"}`,
		"active no kind":  `{"schema":"showmesh.weatherdelay/v1","messageId":"m1","active":true,"startedAt":"2026-09-19T21:00:00Z","startedBy":"op-1","revision":1,"publishedAt":"2026-09-19T21:00:00Z"}`,
		"inactive w/kind": `{"schema":"showmesh.weatherdelay/v1","messageId":"m1","active":false,"kind":"delay","revision":0,"publishedAt":"2026-09-19T21:00:00Z"}`,
	}
	for name, raw := range cases {
		if _, err := DecodeWeatherDelayMessage([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeWeatherDelayMessage accepted %q", name, raw)
		}
	}
}

func TestNewWeatherDelayDarkMessageRoundTrips(t *testing.T) {
	now := time.Date(2026, 9, 19, 21, 5, 0, 0, time.UTC)
	m, err := NewWeatherDelayDarkMessage("lighting", true, now)
	if err != nil {
		t.Fatalf("NewWeatherDelayDarkMessage: %v", err)
	}
	raw, err := EncodeWeatherDelayDarkMessage(m)
	if err != nil {
		t.Fatalf("EncodeWeatherDelayDarkMessage: %v", err)
	}
	back, err := DecodeWeatherDelayDarkMessage(raw)
	if err != nil {
		t.Fatalf("DecodeWeatherDelayDarkMessage(%s): %v", raw, err)
	}
	if back.GroupID != "lighting" || !back.Dark {
		t.Fatalf("round trip produced %+v", back)
	}
}

func TestDecodeWeatherDelayDarkMessageRefusesMalformedPayloads(t *testing.T) {
	cases := map[string]string{
		"empty":       ``,
		"not json":    `{`,
		"bad groupId": `{"schema":"showmesh.weatherdelay.dark/v1","messageId":"m1","groupId":"bad/group","dark":true,"publishedAt":"2026-09-19T21:00:00Z"}`,
	}
	for name, raw := range cases {
		if _, err := DecodeWeatherDelayDarkMessage([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeWeatherDelayDarkMessage accepted %q", name, raw)
		}
	}
}

func TestEncodeWeatherDelayMessageOmitsStartedAtWhileNotActive(t *testing.T) {
	m, err := NewWeatherDelayMessage(false, "", time.Time{}, "", 1, WeatherDelayPlan{}, time.Now())
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage: %v", err)
	}
	b, err := EncodeWeatherDelayMessage(m)
	if err != nil {
		t.Fatalf("EncodeWeatherDelayMessage: %v", err)
	}
	if strings.Contains(string(b), "startedAt") {
		t.Fatalf("encoded inactive message carries startedAt: %s", b)
	}
}

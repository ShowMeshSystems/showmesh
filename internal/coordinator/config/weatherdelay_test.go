package config

import (
	"strings"
	"testing"
)

func TestDecodeWeatherDelayPayloadEmptyBodyIsFullDefault(t *testing.T) {
	p, verr := DecodeWeatherDelayPayload(`{}`)
	if verr != nil {
		t.Fatalf("DecodeWeatherDelayPayload(\"{}\") = %v", verr)
	}
	if p.Alert.RepeatCount != WeatherDelayDefaultPayload.Alert.RepeatCount {
		t.Fatalf("Alert.RepeatCount = %d, want %d", p.Alert.RepeatCount, WeatherDelayDefaultPayload.Alert.RepeatCount)
	}
	if len(p.PowerGroups) != 0 {
		t.Fatalf("PowerGroups = %+v, want empty", p.PowerGroups)
	}
	if p.Triggers != WeatherDelayDefaultPayload.Triggers {
		t.Fatalf("Triggers = %+v, want %+v", p.Triggers, WeatherDelayDefaultPayload.Triggers)
	}
}

func TestEncodeDecodeWeatherDelayPayloadRoundTrips(t *testing.T) {
	want := WeatherDelayPayload{
		Alert: WeatherDelayAlertPayload{
			DelayAssetID: "asset-delay", CancelNightAssetID: "asset-cancel",
			RepeatCount: 5, NodeIDs: []string{"node-a", "node-b"},
		},
		PowerGroups: []WeatherDelayPowerGroupPayload{
			{
				ID: "lighting", Label: "Lighting", FPPInstanceIDs: []string{"fpp-1"},
				ResolumeInstanceIDs: []string{}, RenderNodeIDs: []string{},
				Heartbeat: WeatherDelayHeartbeatPayload{Enabled: true, IntervalSeconds: 15},
			},
		},
		Triggers: WeatherDelayTriggersPayload{AnswerWindowSeconds: 45, CancelAnswerWindowSeconds: 200, RestartMinutes: 20},
	}
	raw, err := EncodeWeatherDelayPayload(want)
	if err != nil {
		t.Fatalf("EncodeWeatherDelayPayload: %v", err)
	}
	got, verr := DecodeWeatherDelayPayload(raw)
	if verr != nil {
		t.Fatalf("DecodeWeatherDelayPayload(%s): %v", raw, verr)
	}
	if got.Alert.DelayAssetID != want.Alert.DelayAssetID || got.Alert.RepeatCount != want.Alert.RepeatCount {
		t.Fatalf("Alert = %+v, want %+v", got.Alert, want.Alert)
	}
	if len(got.PowerGroups) != 1 || got.PowerGroups[0].ID != "lighting" || !got.PowerGroups[0].Heartbeat.Enabled {
		t.Fatalf("PowerGroups = %+v", got.PowerGroups)
	}
	if got.Triggers != want.Triggers {
		t.Fatalf("Triggers = %+v, want %+v", got.Triggers, want.Triggers)
	}
}

func TestDecodeWeatherDelayPayloadRejectsUnknownKeys(t *testing.T) {
	cases := []string{
		`{"bogus":true}`,
		`{"alert":{"bogus":1}}`,
		`{"powerGroups":[{"id":"g1","bogus":1}]}`,
		`{"powerGroups":[{"id":"g1","heartbeat":{"bogus":1}}]}`,
		`{"triggers":{"bogus":1}}`,
	}
	for _, raw := range cases {
		if _, verr := DecodeWeatherDelayPayload(raw); verr == nil {
			t.Errorf("DecodeWeatherDelayPayload(%s) accepted an unknown key", raw)
		}
	}
}

func TestDecodeWeatherDelayPayloadRejectsRepeatCountOutOfRange(t *testing.T) {
	cases := []string{`{"alert":{"repeatCount":0}}`, `{"alert":{"repeatCount":51}}`}
	for _, raw := range cases {
		if _, verr := DecodeWeatherDelayPayload(raw); verr == nil {
			t.Errorf("DecodeWeatherDelayPayload(%s) accepted an out-of-range repeatCount", raw)
		}
	}
}

func TestDecodeWeatherDelayPayloadRejectsDuplicatePowerGroupIDs(t *testing.T) {
	raw := `{"powerGroups":[{"id":"g1"},{"id":"g1"}]}`
	if _, verr := DecodeWeatherDelayPayload(raw); verr == nil {
		t.Fatal("DecodeWeatherDelayPayload accepted a duplicate power group id")
	}
}

func TestDecodeWeatherDelayPayloadRejectsInvalidPowerGroupID(t *testing.T) {
	raw := `{"powerGroups":[{"id":"Not Valid"}]}`
	if _, verr := DecodeWeatherDelayPayload(raw); verr == nil {
		t.Fatal("DecodeWeatherDelayPayload accepted an invalid power group id")
	}
}

func TestDecodeWeatherDelayPayloadRejectsNullSections(t *testing.T) {
	cases := []string{`{"alert":null}`, `{"powerGroups":null}`, `{"triggers":null}`}
	for _, raw := range cases {
		if _, verr := DecodeWeatherDelayPayload(raw); verr == nil {
			t.Errorf("DecodeWeatherDelayPayload(%s) accepted a null section", raw)
		}
	}
}

func TestDecodeWeatherDelayPayloadNotifyDefaultsToNoWebhook(t *testing.T) {
	p, verr := DecodeWeatherDelayPayload(`{}`)
	if verr != nil {
		t.Fatalf("DecodeWeatherDelayPayload(\"{}\") = %v", verr)
	}
	if p.Notify.WebhookURL != "" {
		t.Fatalf("Notify.WebhookURL = %q, want empty", p.Notify.WebhookURL)
	}
}

func TestDecodeWeatherDelayPayloadAcceptsHTTPSWebhook(t *testing.T) {
	p, verr := DecodeWeatherDelayPayload(`{"notify":{"webhookUrl":"https://example.com/hook"}}`)
	if verr != nil {
		t.Fatalf("DecodeWeatherDelayPayload = %v", verr)
	}
	if p.Notify.WebhookURL != "https://example.com/hook" {
		t.Fatalf("Notify.WebhookURL = %q, want https://example.com/hook", p.Notify.WebhookURL)
	}
}

func TestDecodeWeatherDelayPayloadRejectsBadWebhookURLs(t *testing.T) {
	cases := []string{
		`{"notify":{"webhookUrl":"ftp://example.com/hook"}}`,
		`{"notify":{"webhookUrl":"not a url"}}`,
		`{"notify":{"webhookUrl":"https://"}}`,
		`{"notify":{"webhookUrl":"https://user:pass@example.com/hook"}}`,
		`{"notify":null}`,
		`{"notify":{"bogus":1}}`,
	}
	for _, raw := range cases {
		if _, verr := DecodeWeatherDelayPayload(raw); verr == nil {
			t.Errorf("DecodeWeatherDelayPayload(%s) accepted a bad webhook configuration", raw)
		}
	}
}

func TestDecodeWeatherDelayPayloadRejectsOversizeWebhookURL(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", weatherDelayWebhookURLMaxLength)
	raw := `{"notify":{"webhookUrl":"` + long + `"}}`
	if _, verr := DecodeWeatherDelayPayload(raw); verr == nil {
		t.Fatal("DecodeWeatherDelayPayload accepted an oversize webhook URL")
	}
}

func TestDecodeWeatherDelayPayloadRejectsMalformedBody(t *testing.T) {
	if _, verr := DecodeWeatherDelayPayload(`not json`); verr == nil {
		t.Fatal("DecodeWeatherDelayPayload accepted malformed JSON")
	}
}

func TestDecodeWeatherDelayPayloadHeartbeatDefaults(t *testing.T) {
	p, verr := DecodeWeatherDelayPayload(`{"powerGroups":[{"id":"g1"}]}`)
	if verr != nil {
		t.Fatalf("DecodeWeatherDelayPayload: %v", verr)
	}
	hb := p.PowerGroups[0].Heartbeat
	if hb.Enabled || hb.IntervalSeconds != weatherDelayDefaultHeartbeatIntervalSeconds {
		t.Fatalf("Heartbeat default = %+v", hb)
	}
}

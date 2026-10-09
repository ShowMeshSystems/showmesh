package mqttproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHealthPayloadWithoutWeatherDelayIsNoReport(t *testing.T) {
	env, err := NewHealthEnvelope(nil, "node-a", HealthPayload{BootID: "boot-1"})
	if err != nil {
		t.Fatalf("NewHealthEnvelope: %v", err)
	}
	if strings.Contains(string(env.Payload), "weatherDelay") {
		t.Fatalf("payload %s carries a weatherDelay member; a node with nothing to report must omit it", env.Payload)
	}
	got, err := DecodeHealthPayload(env)
	if err != nil {
		t.Fatalf("DecodeHealthPayload: %v", err)
	}
	if got.WeatherDelay != nil {
		t.Fatalf("WeatherDelay = %+v, want nil: an absent member is no report, never not delayed", got.WeatherDelay)
	}
}

func TestHealthPayloadWeatherDelayRoundTrips(t *testing.T) {
	startedAt := time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC)
	want := HealthWeatherDelay{Active: true, Kind: WeatherDelayKindCancelNight, StartedAt: startedAt, HeldRevision: 4, Revision: 5}
	env, err := NewHealthEnvelope(nil, "node-a", HealthPayload{BootID: "boot-1", WeatherDelay: &want})
	if err != nil {
		t.Fatalf("NewHealthEnvelope: %v", err)
	}
	got, err := DecodeHealthPayload(env)
	if err != nil {
		t.Fatalf("DecodeHealthPayload: %v", err)
	}
	if got.WeatherDelay == nil || *got.WeatherDelay != want {
		t.Fatalf("WeatherDelay = %+v, want %+v", got.WeatherDelay, want)
	}
}

// A coordinator built before the member existed decodes into a struct
// without it, and must still accept the heartbeat.
func TestHealthPayloadWeatherDelayIsIgnoredByAnOlderDecoder(t *testing.T) {
	env, err := NewHealthEnvelope(nil, "node-a", HealthPayload{
		BootID: "boot-1", Sequence: 3,
		WeatherDelay: &HealthWeatherDelay{Active: true, Kind: WeatherDelayKindDelay, HeldRevision: 1, Revision: 1},
	})
	if err != nil {
		t.Fatalf("NewHealthEnvelope: %v", err)
	}
	var older struct {
		BootID   string `json:"bootId"`
		Sequence uint64 `json:"sequence"`
	}
	if err := json.Unmarshal(env.Payload, &older); err != nil {
		t.Fatalf("an older decoder refused the payload: %v", err)
	}
	if older.BootID != "boot-1" || older.Sequence != 3 {
		t.Fatalf("older decoder read %+v, want bootId boot-1 and sequence 3", older)
	}
}

func TestHealthWeatherDelayValidate(t *testing.T) {
	cases := []struct {
		name    string
		report  HealthWeatherDelay
		wantErr bool
	}{
		{"not active", HealthWeatherDelay{HeldRevision: 2, Revision: 3}, false},
		{"active delay", HealthWeatherDelay{Active: true, Kind: WeatherDelayKindDelay}, false},
		{"active with no kind", HealthWeatherDelay{Active: true}, true},
		{"active with an unknown kind", HealthWeatherDelay{Active: true, Kind: "storm"}, true},
		{"negative held revision", HealthWeatherDelay{HeldRevision: -1}, true},
		{"negative revision", HealthWeatherDelay{Revision: -1}, true},
		{"revision at the bound", HealthWeatherDelay{HeldRevision: MaxHealthWeatherDelayRevision, Revision: MaxHealthWeatherDelayRevision}, false},
		{"revision with no room left above it", HealthWeatherDelay{Active: true, Kind: WeatherDelayKindDelay, Revision: 9223372036854775806}, true},
		{"held revision above the bound", HealthWeatherDelay{HeldRevision: MaxHealthWeatherDelayRevision + 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.report.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidHealthWeatherDelay) {
				t.Fatalf("Validate() = %v, want it to wrap ErrInvalidHealthWeatherDelay", err)
			}
		})
	}
}

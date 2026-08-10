package config

import (
	"log/slog"
	"strings"
	"testing"
)

// lookupFrom builds a lookup function for LoadConfigFrom backed by an
// explicit map, so tests never depend on (and cannot be broken by) the
// ambient process environment. A key absent from env reports "unset", which
// is what lets the defaults test genuinely exercise the unset path
// regardless of what SHOWMESH_* variables happen to be exported on the
// machine or CI runner running the test.
func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

// redactedConfig renders a Config safely for test failure messages: it goes
// through LogValue so a failing assertion can never print
// SHOWMESH_MQTT_PASSWORD in the clear (a bare %+v on Config would).
func redactedConfig(c Config) string {
	return c.LogValue().String()
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}

	want := Config{
		HTTPAddr:     ":8080",
		MQTTBroker:   "tcp://localhost:1883",
		MQTTClientID: "showmesh-coordinator",
		MQTTUsername: "",
		MQTTPassword: "",
		DataDir:      "/var/lib/showmesh",
		LogLevel:     "info",
	}

	if cfg != want {
		t.Errorf("LoadConfigFrom(unset) = %s, want %s", redactedConfig(cfg), redactedConfig(want))
	}
}

func TestLoadConfigOverridesFromEnv(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_HTTP_ADDR":      ":9090",
		"SHOWMESH_MQTT_BROKER":    "tcp://broker.example.com:1883",
		"SHOWMESH_MQTT_CLIENT_ID": "test-client",
		"SHOWMESH_MQTT_USERNAME":  "alice",
		"SHOWMESH_MQTT_PASSWORD":  "s3cret",
		"SHOWMESH_DATA_DIR":       "/data/showmesh",
		"SHOWMESH_LOG_LEVEL":      "debug",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}

	want := Config{
		HTTPAddr:     ":9090",
		MQTTBroker:   "tcp://broker.example.com:1883",
		MQTTClientID: "test-client",
		MQTTUsername: "alice",
		MQTTPassword: "s3cret",
		DataDir:      "/data/showmesh",
		LogLevel:     "debug",
	}

	if cfg != want {
		t.Errorf("LoadConfigFrom(overrides) = %s, want %s", redactedConfig(cfg), redactedConfig(want))
	}
}

func TestLoadConfigValidationFailures(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantVar string
	}{
		{
			name:    "empty http addr",
			env:     map[string]string{"SHOWMESH_HTTP_ADDR": ""},
			wantVar: EnvHTTPAddr,
		},
		{
			name:    "empty mqtt broker",
			env:     map[string]string{"SHOWMESH_MQTT_BROKER": ""},
			wantVar: "SHOWMESH_MQTT_BROKER",
		},
		{
			name:    "invalid mqtt broker url",
			env:     map[string]string{"SHOWMESH_MQTT_BROKER": "://not a url:::"},
			wantVar: "SHOWMESH_MQTT_BROKER",
		},
		{
			name:    "mqtt broker missing scheme",
			env:     map[string]string{"SHOWMESH_MQTT_BROKER": "broker-host:1883"},
			wantVar: "SHOWMESH_MQTT_BROKER",
		},
		{
			name:    "mqtt broker bare host and port, no scheme separator",
			env:     map[string]string{"SHOWMESH_MQTT_BROKER": "localhost:1883"},
			wantVar: "SHOWMESH_MQTT_BROKER",
		},
		{
			name:    "mqtt broker unknown scheme",
			env:     map[string]string{"SHOWMESH_MQTT_BROKER": "ftp://localhost:1883"},
			wantVar: "SHOWMESH_MQTT_BROKER",
		},
		{
			name:    "mqtt broker scheme with no host",
			env:     map[string]string{"SHOWMESH_MQTT_BROKER": "tcp://"},
			wantVar: "SHOWMESH_MQTT_BROKER",
		},
		{
			name:    "mqtt broker opaque scheme with no host",
			env:     map[string]string{"SHOWMESH_MQTT_BROKER": "tcp:1883"},
			wantVar: "SHOWMESH_MQTT_BROKER",
		},
		{
			name:    "invalid log level",
			env:     map[string]string{"SHOWMESH_LOG_LEVEL": "trace"},
			wantVar: "SHOWMESH_LOG_LEVEL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfigFrom(lookupFrom(tt.env))
			if err == nil {
				t.Fatalf("LoadConfigFrom() error = nil, want error mentioning %s", tt.wantVar)
			}

			if !strings.Contains(err.Error(), tt.wantVar) {
				t.Errorf("LoadConfigFrom() error = %q, want it to mention %s", err.Error(), tt.wantVar)
			}
		})
	}
}

func TestLoadConfigValidBrokerSchemes(t *testing.T) {
	for _, scheme := range validBrokerSchemesList {
		t.Run(scheme, func(t *testing.T) {
			env := map[string]string{
				"SHOWMESH_MQTT_BROKER": scheme + "://broker.example.com:1883",
			}

			cfg, err := LoadConfigFrom(lookupFrom(env))
			if err != nil {
				t.Fatalf("LoadConfigFrom() error = %v, want nil for accepted scheme %q", err, scheme)
			}
			if cfg.MQTTBroker != env["SHOWMESH_MQTT_BROKER"] {
				t.Errorf("MQTTBroker = %q, want %q", cfg.MQTTBroker, env["SHOWMESH_MQTT_BROKER"])
			}
		})
	}
}

func TestConfigLogValueRedactsPassword(t *testing.T) {
	cfg := Config{
		HTTPAddr:     ":8080",
		MQTTBroker:   "tcp://localhost:1883",
		MQTTClientID: "showmesh-coordinator",
		MQTTUsername: "alice",
		MQTTPassword: "s3cret-value-must-not-appear",
		DataDir:      "/data/showmesh",
		LogLevel:     "info",
	}

	rendered := renderLogValue(t, cfg)

	if strings.Contains(rendered, cfg.MQTTPassword) {
		t.Fatalf("Config.LogValue() output contains the raw password: %s", rendered)
	}
	if !strings.Contains(rendered, redactedPassword) {
		t.Errorf("Config.LogValue() output = %s, want it to contain the redaction placeholder %q", rendered, redactedPassword)
	}
	// The rest of the config should still be present and useful for
	// debugging; only the password is sensitive.
	if !strings.Contains(rendered, "alice") {
		t.Errorf("Config.LogValue() output = %s, want it to still contain the non-sensitive username", rendered)
	}
}

func TestConfigLogValueEmptyPassword(t *testing.T) {
	cfg := Config{MQTTPassword: ""}

	rendered := renderLogValue(t, cfg)

	if strings.Contains(rendered, redactedPassword) {
		t.Errorf("Config.LogValue() output = %s, want no redaction placeholder when password is unset", rendered)
	}
}

// renderLogValue runs cfg through a real slog JSON handler, the same path
// production logging takes, and returns the emitted line. This is what
// proves LogValue is actually wired up as a slog.LogValuer and not just a
// method that happens to exist.
func renderLogValue(t *testing.T, cfg Config) string {
	t.Helper()

	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("config", "cfg", cfg)

	return buf.String()
}

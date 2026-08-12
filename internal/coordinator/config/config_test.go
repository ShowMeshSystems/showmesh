package config

import (
	"fmt"
	"log/slog"
	"reflect"
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
		FPPEndpoints: nil,

		// SHOWMESH_FPP_MQTT_BROKER_URL and friends are unset, so the FPP
		// MQTT collector's fields are all zero except FPPMQTTTopicPrefix,
		// which defaults even when the broker itself is not configured
		// (see FPPMQTTTopicPrefix's doc comment).
		FPPMQTTTopicPrefix: "falcon/player",
	}

	// reflect.DeepEqual, not ==: FPPEndpoints is a slice, which made Config
	// stop being comparable with == the moment that field was added. This
	// is the only change needed here — Config's other fields are still
	// compared exactly as before.
	if !reflect.DeepEqual(cfg, want) {
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
		FPPEndpoints: nil,

		FPPMQTTTopicPrefix: "falcon/player",
	}

	if !reflect.DeepEqual(cfg, want) {
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
		{
			name:    "fpp endpoints entry missing equals",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01"},
			wantVar: "SHOWMESH_FPP_ENDPOINTS",
		},
		{
			name:    "fpp endpoints entry with empty id",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "=http://10.0.1.20"},
			wantVar: "SHOWMESH_FPP_ENDPOINTS",
		},
		{
			name:    "fpp endpoints entry with empty url",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01="},
			wantVar: "SHOWMESH_FPP_ENDPOINTS",
		},
		{
			name:    "fpp endpoints stray trailing comma",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=http://10.0.1.20,"},
			wantVar: "SHOWMESH_FPP_ENDPOINTS",
		},
		{
			// mqttproto.ValidateNodeID rejects uppercase; this proves
			// Validate actually calls it rather than accepting anything
			// non-empty.
			name:    "fpp endpoints invalid instance id",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "Player_01=http://10.0.1.20"},
			wantVar: "SHOWMESH_FPP_ENDPOINTS",
		},
		{
			name:    "fpp endpoints url with no scheme",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=10.0.1.20"},
			wantVar: "SHOWMESH_FPP_ENDPOINTS",
		},
		{
			name:    "fpp endpoints url with unsupported scheme",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=ftp://10.0.1.20"},
			wantVar: "SHOWMESH_FPP_ENDPOINTS",
		},
		{
			name:    "fpp endpoints url with no host",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=http://"},
			wantVar: "SHOWMESH_FPP_ENDPOINTS",
		},
		{
			// The leak this closes: contract section 2 forbids leaking a
			// credential or a full URL with userinfo anywhere downstream
			// (logs, error reasons, the API). Rejecting it here, at the
			// only entry point, means no downstream consumer has to
			// remember to scrub it.
			name:    "fpp endpoints url with userinfo",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=http://user:pass@10.0.1.20"},
			wantVar: "SHOWMESH_FPP_ENDPOINTS",
		},
		{
			name:    "fpp endpoints duplicate instance id",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=http://10.0.1.20,player-01=http://10.0.1.21"},
			wantVar: "SHOWMESH_FPP_ENDPOINTS",
		},
		{
			name:    "fpp mqtt hosts entry missing equals",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "player-01"},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			name:    "fpp mqtt hosts entry with empty id",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "=FPP-Main"},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			name:    "fpp mqtt hosts entry with empty hostname",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "player-01="},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			name:    "fpp mqtt hosts invalid instance id",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "Player_01=FPP-Main"},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			// The topic-injection check parseFPPMQTTHosts's doc comment
			// describes: a HostName is placed directly into an MQTT topic
			// filter, so '/' must be rejected the same way ValidateNodeID
			// rejects it for an instance id.
			name:    "fpp mqtt hosts hostname with slash",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "player-01=FPP/Main"},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			name:    "fpp mqtt hosts hostname with wildcard",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "player-01=FPP+Main"},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			name:    "fpp mqtt hosts duplicate instance id",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "player-01=FPP-Main,player-01=FPP-Other"},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			// The load-bearing cross-check (contract section 4.4): every id
			// in SHOWMESH_FPP_MQTT_HOSTS must also be a configured FPP REST
			// endpoint, named specifically so an operator sees exactly
			// which id is unmatched.
			name: "fpp mqtt hosts id not in fpp endpoints",
			env: map[string]string{
				"SHOWMESH_FPP_ENDPOINTS":  "player-01=http://10.0.1.20",
				"SHOWMESH_FPP_MQTT_HOSTS": "shed=FPP-Shed",
			},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			name:    "fpp mqtt broker url invalid",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_BROKER_URL": "://not a url:::"},
			wantVar: "SHOWMESH_FPP_MQTT_BROKER_URL",
		},
		{
			name:    "fpp mqtt broker url missing scheme",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_BROKER_URL": "mqtt.example:1883"},
			wantVar: "SHOWMESH_FPP_MQTT_BROKER_URL",
		},
		{
			name:    "fpp mqtt broker url unsupported scheme",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_BROKER_URL": "ftp://mqtt.example:1883"},
			wantVar: "SHOWMESH_FPP_MQTT_BROKER_URL",
		},
		{
			name:    "fpp mqtt broker url with no host",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_BROKER_URL": "tcp://"},
			wantVar: "SHOWMESH_FPP_MQTT_BROKER_URL",
		},
		{
			// Broker URL set but no hosts configured: very likely a missing
			// SHOWMESH_FPP_MQTT_HOSTS, rejected at startup rather than
			// silently constructing a collector that ingests nothing.
			name:    "fpp mqtt broker url set with no hosts",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_BROKER_URL": "tcp://mqtt.example:1883"},
			wantVar: "SHOWMESH_FPP_MQTT_BROKER_URL",
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

func TestLoadConfigFPPEndpointsParsed(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_ENDPOINTS": "player-01=http://10.0.1.20,shed=http://10.0.1.21:80",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}

	want := []FPPEndpoint{
		{ID: "player-01", URL: "http://10.0.1.20"},
		{ID: "shed", URL: "http://10.0.1.21:80"},
	}
	if !reflect.DeepEqual(cfg.FPPEndpoints, want) {
		t.Errorf("FPPEndpoints = %+v, want %+v", cfg.FPPEndpoints, want)
	}
}

func TestLoadConfigFPPEndpointsUnsetIsNilNotError(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.FPPEndpoints != nil {
		t.Errorf("FPPEndpoints = %+v, want nil when SHOWMESH_FPP_ENDPOINTS is unset", cfg.FPPEndpoints)
	}
}

func TestLoadConfigFPPEndpointsToleratesWhitespace(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_ENDPOINTS": " player-01 = http://10.0.1.20 , shed=http://10.0.1.21 ",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	want := []FPPEndpoint{
		{ID: "player-01", URL: "http://10.0.1.20"},
		{ID: "shed", URL: "http://10.0.1.21"},
	}
	if !reflect.DeepEqual(cfg.FPPEndpoints, want) {
		t.Errorf("FPPEndpoints = %+v, want %+v", cfg.FPPEndpoints, want)
	}
}

func TestConfigLogValueFPPEndpointsNamesInstancesOnly(t *testing.T) {
	cfg := Config{
		FPPEndpoints: []FPPEndpoint{
			{ID: "player-01", URL: "http://10.0.1.20"},
		},
	}

	rendered := renderLogValue(t, cfg)

	if !strings.Contains(rendered, "player-01") {
		t.Errorf("Config.LogValue() output = %s, want it to name the configured instance id", rendered)
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

// --- Step 3 Task D: the versioned public control API (ADR-014) ---

func TestLoadConfigAPITokenAndAllowedOriginsDefaults(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.APIToken != "" {
		t.Errorf("APIToken = %q, want empty when SHOWMESH_API_TOKEN is unset", cfg.APIToken)
	}
	if cfg.APIAllowedOrigins != nil {
		t.Errorf("APIAllowedOrigins = %v, want nil when SHOWMESH_API_ALLOWED_ORIGINS is unset", cfg.APIAllowedOrigins)
	}
}

func TestLoadConfigAPIAllowedOriginsSplitsAndTrims(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_API_ALLOWED_ORIGINS": " https://ui.example.com , https://alt.example.com,,http://localhost:5173 ",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}

	want := []string{"https://ui.example.com", "https://alt.example.com", "http://localhost:5173"}
	if !reflect.DeepEqual(cfg.APIAllowedOrigins, want) {
		t.Errorf("APIAllowedOrigins = %v, want %v", cfg.APIAllowedOrigins, want)
	}
}

func TestLoadConfigAPIToken(t *testing.T) {
	env := map[string]string{"SHOWMESH_API_TOKEN": "top-secret-token"}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.APIToken != "top-secret-token" {
		t.Errorf("APIToken = %q, want %q", cfg.APIToken, "top-secret-token")
	}
}

// TestConfigLogValueRedactsAPIToken mirrors
// TestConfigLogValueRedactsPassword: APIToken is exactly as sensitive as
// MQTTPassword (contract section 6.8) and must go through the same
// enforced-by-code redaction, not a doc comment's promise.
func TestConfigLogValueRedactsAPIToken(t *testing.T) {
	cfg := Config{APIToken: "s3cret-api-token-must-not-appear"}

	rendered := renderLogValue(t, cfg)

	if strings.Contains(rendered, cfg.APIToken) {
		t.Fatalf("Config.LogValue() output contains the raw API token: %s", rendered)
	}
	if !strings.Contains(rendered, redactedPassword) {
		t.Errorf("Config.LogValue() output = %s, want it to contain the redaction placeholder %q", rendered, redactedPassword)
	}
}

func TestConfigLogValueEmptyAPIToken(t *testing.T) {
	cfg := Config{APIToken: ""}

	rendered := renderLogValue(t, cfg)

	if strings.Contains(rendered, redactedPassword) {
		t.Errorf("Config.LogValue() output = %s, want no redaction placeholder when APIToken is unset", rendered)
	}
}

// --- Step 5 Seam B: SHOWMESH_FPP_MQTT_* ---

func TestLoadConfigFPPMQTTUnsetIsDisabled(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.FPPMQTTBrokerURL != "" {
		t.Errorf("FPPMQTTBrokerURL = %q, want empty when SHOWMESH_FPP_MQTT_BROKER_URL is unset", cfg.FPPMQTTBrokerURL)
	}
	if cfg.FPPMQTTHosts != nil {
		t.Errorf("FPPMQTTHosts = %v, want nil when SHOWMESH_FPP_MQTT_HOSTS is unset", cfg.FPPMQTTHosts)
	}
	// The topic prefix defaults even with the collector disabled: it is a
	// pure string default, not a signal that the feature is active (see
	// FPPMQTTTopicPrefix's doc comment).
	if cfg.FPPMQTTTopicPrefix != "falcon/player" {
		t.Errorf("FPPMQTTTopicPrefix = %q, want default %q even when the collector is disabled", cfg.FPPMQTTTopicPrefix, "falcon/player")
	}
}

// TestLoadConfigFPPMQTTBrokerURLWithCredentialsIsRejectedWithoutEchoingThem
// pins the fix for a review finding: SHOWMESH_FPP_MQTT_PASSWORD was redacted
// in LogValue while the same secret supplied inside the broker URL was
// echoed into three validation errors and logged verbatim on every startup.
//
// The second case is the one that makes the ordering load-bearing rather
// than cosmetic. A value that is BOTH malformed AND credentialed reaches
// url.Parse, whose *url.Error embeds the offending URL in its own message,
// so a later careful format string cannot help. The userinfo check must run
// before the parse.
func TestLoadConfigFPPMQTTBrokerURLWithCredentialsIsRejectedWithoutEchoingThem(t *testing.T) {
	const secret = "supersecretpassword"

	for name, brokerURL := range map[string]string{
		"well formed": "tcp://showmesh:" + secret + "@mqtt.example:1883",
		// A control character in the host makes url.Parse itself fail.
		"malformed and credentialed": "tcp://showmesh:" + secret + "@mqtt\x7f.example:1883",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfigFrom(lookupFrom(map[string]string{
				"SHOWMESH_FPP_ENDPOINTS":       "main=http://192.168.133.159",
				"SHOWMESH_FPP_MQTT_BROKER_URL": brokerURL,
				"SHOWMESH_FPP_MQTT_HOSTS":      "main=FPP-Main",
			}))
			if err == nil {
				t.Fatal("LoadConfigFrom() error = nil, want a rejection of credentials embedded in the broker URL")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error text leaks the embedded password: %v", err)
			}
		})
	}
}

// TestConfigLogValueRedactsBrokerURLUserinfo covers the logging half. The
// control-plane broker URL is deliberately NOT rejected for carrying
// userinfo (see validateFPPMQTTConfig's comment), so redaction is the only
// thing standing between it and every log line.
func TestConfigLogValueRedactsBrokerURLUserinfo(t *testing.T) {
	const secret = "controlplanesecret"

	cfg := Config{
		MQTTBroker:       "tcp://showmesh:" + secret + "@broker.example:1883",
		FPPMQTTBrokerURL: "tcp://showmesh:" + secret + "@mqtt.example:1883",
	}

	logged := fmt.Sprintf("%v", cfg.LogValue())
	if strings.Contains(logged, secret) {
		t.Errorf("LogValue() leaks a password embedded in a broker URL: %s", logged)
	}
	// Redaction must not destroy the operator-useful part: the host is how
	// they tell which broker the coordinator is talking to.
	for _, host := range []string{"broker.example:1883", "mqtt.example:1883"} {
		if !strings.Contains(logged, host) {
			t.Errorf("LogValue() = %s, want it to still name the broker host %q", logged, host)
		}
	}
}

func TestLoadConfigFPPMQTTFullyConfigured(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_ENDPOINTS":         "main=http://192.168.133.159,remote01=http://192.168.133.70",
		"SHOWMESH_FPP_MQTT_BROKER_URL":   "tcp://mqtt.example:1883",
		"SHOWMESH_FPP_MQTT_USERNAME":     "showmesh",
		"SHOWMESH_FPP_MQTT_PASSWORD":     "s3cret",
		"SHOWMESH_FPP_MQTT_TOPIC_PREFIX": "falcon/player",
		"SHOWMESH_FPP_MQTT_HOSTS":        "main=FPP-Main,remote01=FPP-remote-01",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}

	if cfg.FPPMQTTBrokerURL != "tcp://mqtt.example:1883" {
		t.Errorf("FPPMQTTBrokerURL = %q, want %q", cfg.FPPMQTTBrokerURL, "tcp://mqtt.example:1883")
	}
	if cfg.FPPMQTTUsername != "showmesh" {
		t.Errorf("FPPMQTTUsername = %q, want %q", cfg.FPPMQTTUsername, "showmesh")
	}
	if cfg.FPPMQTTPassword != "s3cret" {
		t.Errorf("FPPMQTTPassword = %q, want %q", cfg.FPPMQTTPassword, "s3cret")
	}
	if cfg.FPPMQTTTopicPrefix != "falcon/player" {
		t.Errorf("FPPMQTTTopicPrefix = %q, want %q", cfg.FPPMQTTTopicPrefix, "falcon/player")
	}
	wantHosts := map[string]string{"main": "FPP-Main", "remote01": "FPP-remote-01"}
	if !reflect.DeepEqual(cfg.FPPMQTTHosts, wantHosts) {
		t.Errorf("FPPMQTTHosts = %v, want %v", cfg.FPPMQTTHosts, wantHosts)
	}
}

func TestLoadConfigFPPMQTTTopicPrefixCustom(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_MQTT_TOPIC_PREFIX": "custom/prefix",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.FPPMQTTTopicPrefix != "custom/prefix" {
		t.Errorf("FPPMQTTTopicPrefix = %q, want %q", cfg.FPPMQTTTopicPrefix, "custom/prefix")
	}
}

// TestLoadConfigFPPMQTTHostsWithoutBrokerURLStillCrossChecked proves the
// id<->FPPEndpoints cross-check runs even when FPPMQTTBrokerURL is unset
// (validateFPPMQTTConfig's doc comment: "independent of whether
// FPPMQTTBrokerURL is set"). Before trusting this test, the behavior it
// names was broken (the check made conditional on BrokerURL != "") and
// confirmed to fail; see the Step 5 Seam B report for that verification.
func TestLoadConfigFPPMQTTHostsWithoutBrokerURLStillCrossChecked(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_MQTT_HOSTS": "shed=FPP-Shed",
	}

	_, err := LoadConfigFrom(lookupFrom(env))
	if err == nil {
		t.Fatalf("LoadConfigFrom() error = nil, want an error naming the unmatched id even with no broker URL configured")
	}
	if !strings.Contains(err.Error(), "SHOWMESH_FPP_MQTT_HOSTS") || !strings.Contains(err.Error(), "shed") {
		t.Errorf("LoadConfigFrom() error = %q, want it to name SHOWMESH_FPP_MQTT_HOSTS and the unmatched id %q", err.Error(), "shed")
	}
}

func TestLoadConfigFPPMQTTHostsToleratesWhitespace(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_ENDPOINTS":  "main=http://192.168.133.159",
		"SHOWMESH_FPP_MQTT_HOSTS": " main = FPP-Main ",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	want := map[string]string{"main": "FPP-Main"}
	if !reflect.DeepEqual(cfg.FPPMQTTHosts, want) {
		t.Errorf("FPPMQTTHosts = %v, want %v", cfg.FPPMQTTHosts, want)
	}
}

func TestConfigLogValueRedactsFPPMQTTPassword(t *testing.T) {
	cfg := Config{FPPMQTTPassword: "s3cret-fpp-mqtt-value-must-not-appear"}

	rendered := renderLogValue(t, cfg)

	if strings.Contains(rendered, cfg.FPPMQTTPassword) {
		t.Fatalf("Config.LogValue() output contains the raw FPP MQTT password: %s", rendered)
	}
	if !strings.Contains(rendered, redactedPassword) {
		t.Errorf("Config.LogValue() output = %s, want it to contain the redaction placeholder %q", rendered, redactedPassword)
	}
}

func TestConfigLogValueEmptyFPPMQTTPassword(t *testing.T) {
	cfg := Config{FPPMQTTPassword: ""}

	rendered := renderLogValue(t, cfg)

	if strings.Contains(rendered, redactedPassword) {
		t.Errorf("Config.LogValue() output = %s, want no redaction placeholder when FPPMQTTPassword is unset", rendered)
	}
}

func TestConfigLogValueFPPMQTTHostsPresent(t *testing.T) {
	cfg := Config{FPPMQTTHosts: map[string]string{"main": "FPP-Main"}}

	rendered := renderLogValue(t, cfg)

	if !strings.Contains(rendered, "FPP-Main") {
		t.Errorf("Config.LogValue() output = %s, want it to name the configured HostName", rendered)
	}
}

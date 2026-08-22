package config

import (
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
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

		// SHOWMESH_RESOLUME_URL is unset (the collector never runs), but
		// SHOWMESH_RESOLUME_ID still defaults — the identical
		// "defaults regardless of whether the feature is active" posture
		// FPPMQTTTopicPrefix already has, see ResolumeID's doc comment.
		ResolumeID: "resolume",

		// Track E seam E5/E6: SHOWMESH_ASSET_CONTENT_BASE_URL is unset (the
		// sync service never runs), but the other four asset fields still
		// default — the identical "defaults regardless of whether the
		// feature is active" posture ResolumeID already has.
		AssetDir:               "/var/lib/showmesh/assets",
		AssetMaxUploadBytes:    assetstore.DefaultMaxUploadBytes,
		AssetSyncInterval:      defaultAssetSyncInterval,
		AssetInventoryInterval: defaultAssetInventoryInterval,
		// SHOWMESH_RESOLUME_RECOVERY_SETTLE also defaults regardless of
		// whether the collector is active — see its own doc comment.
		ResolumeRecoverySettle: defaultResolumeRecoverySettle,
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

		AssetDir:               "/data/showmesh/assets",
		AssetMaxUploadBytes:    assetstore.DefaultMaxUploadBytes,
		AssetSyncInterval:      defaultAssetSyncInterval,
		AssetInventoryInterval: defaultAssetInventoryInterval,
		FPPMQTTTopicPrefix:     "falcon/player",
		ResolumeID:             "resolume",
		ResolumeRecoverySettle: defaultResolumeRecoverySettle,
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
			// non-empty. The semantic check below this line runs through
			// [validateFPPEndpoints], which names the "fpp.endpoints"
			// configuration kind rather than the env var: the same
			// function backs the store-backed config:write surface, and
			// the remedy is the same in both directions.
			name:    "fpp endpoints invalid instance id",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "Player_01=http://10.0.1.20"},
			wantVar: "fpp.endpoints",
		},
		{
			name:    "fpp endpoints url with no scheme",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=10.0.1.20"},
			wantVar: "fpp.endpoints",
		},
		{
			name:    "fpp endpoints url with unsupported scheme",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=ftp://10.0.1.20"},
			wantVar: "fpp.endpoints",
		},
		{
			name:    "fpp endpoints url with no host",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=http://"},
			wantVar: "fpp.endpoints",
		},
		{
			// The leak this closes: contract section 2 forbids leaking a
			// credential or a full URL with userinfo anywhere downstream
			// (logs, error reasons, the API). Rejecting it here, at the
			// only entry point, means no downstream consumer has to
			// remember to scrub it.
			name:    "fpp endpoints url with userinfo",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=http://user:pass@10.0.1.20"},
			wantVar: "fpp.endpoints",
		},
		{
			name:    "fpp endpoints duplicate instance id",
			env:     map[string]string{"SHOWMESH_FPP_ENDPOINTS": "player-01=http://10.0.1.20,player-01=http://10.0.1.21"},
			wantVar: "fpp.endpoints",
		},
		{
			name:    "fpp mqtt hosts entry missing equals",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "player-01"},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			name:    "fpp mqtt hosts entry with empty id",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "=fpp-player"},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			name:    "fpp mqtt hosts entry with empty hostname",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "player-01="},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			name:    "fpp mqtt hosts invalid instance id",
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "Player_01=fpp-player"},
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
			env:     map[string]string{"SHOWMESH_FPP_MQTT_HOSTS": "player-01=fpp-player,player-01=FPP-Other"},
			wantVar: "SHOWMESH_FPP_MQTT_HOSTS",
		},
		{
			// The load-bearing cross-check (contract section 4.4): every id
			// in SHOWMESH_FPP_MQTT_HOSTS must also be a configured FPP
			// endpoint, named specifically so an operator sees exactly
			// which id is unmatched. The message leads with the
			// store-backed remedy (ADR-039 decision 1) rather than the env
			// var, since [ValidateFPPMQTTHostIDs] backs the store-backed
			// config:write surface too.
			name: "fpp mqtt hosts id not in fpp endpoints",
			env: map[string]string{
				"SHOWMESH_FPP_ENDPOINTS":  "player-01=http://10.0.1.20",
				"SHOWMESH_FPP_MQTT_HOSTS": "shed=FPP-Shed",
			},
			wantVar: "shed",
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
		{
			name:    "resolume url invalid",
			env:     map[string]string{"SHOWMESH_RESOLUME_URL": "://not a url:::"},
			wantVar: "SHOWMESH_RESOLUME_URL",
		},
		{
			name:    "resolume url missing scheme",
			env:     map[string]string{"SHOWMESH_RESOLUME_URL": "127.0.0.1:9080"},
			wantVar: "SHOWMESH_RESOLUME_URL",
		},
		{
			name:    "resolume url unsupported scheme",
			env:     map[string]string{"SHOWMESH_RESOLUME_URL": "ftp://127.0.0.1:9080"},
			wantVar: "SHOWMESH_RESOLUME_URL",
		},
		{
			name:    "resolume url with no host",
			env:     map[string]string{"SHOWMESH_RESOLUME_URL": "http://"},
			wantVar: "SHOWMESH_RESOLUME_URL",
		},
		{
			name:    "resolume url with userinfo",
			env:     map[string]string{"SHOWMESH_RESOLUME_URL": "http://user:pass@127.0.0.1:9080"},
			wantVar: "SHOWMESH_RESOLUME_URL",
		},
		{
			// mqttproto.ValidateNodeID rejects uppercase; proves Validate
			// actually checks SHOWMESH_RESOLUME_ID's syntax rather than
			// accepting anything non-empty, mirroring the identical FPP
			// endpoint id check above.
			name:    "resolume id invalid syntax",
			env:     map[string]string{"SHOWMESH_RESOLUME_URL": "http://127.0.0.1:9080", "SHOWMESH_RESOLUME_ID": "Resolume_1"},
			wantVar: "SHOWMESH_RESOLUME_ID",
		},
		{
			// The load-bearing collision guard this seam adds: the
			// Resolume collector and every FPP collector share one
			// collector.Runner keyed by this exact id string (see
			// ValidateResolumeIDAgainstFPPEndpoints's doc comment).
			name: "resolume id collides with an fpp endpoint id",
			env: map[string]string{
				"SHOWMESH_FPP_ENDPOINTS": "player-01=http://10.0.1.20",
				"SHOWMESH_RESOLUME_URL":  "http://127.0.0.1:9080",
				"SHOWMESH_RESOLUME_ID":   "player-01",
			},
			wantVar: "SHOWMESH_RESOLUME_ID",
		},
		{
			name:    "asset dir empty",
			env:     map[string]string{"SHOWMESH_ASSET_DIR": ""},
			wantVar: "SHOWMESH_ASSET_DIR",
		},
		{
			name:    "asset max upload bytes not an integer",
			env:     map[string]string{"SHOWMESH_ASSET_MAX_UPLOAD_BYTES": "not-a-number"},
			wantVar: "SHOWMESH_ASSET_MAX_UPLOAD_BYTES",
		},
		{
			name:    "asset max upload bytes zero",
			env:     map[string]string{"SHOWMESH_ASSET_MAX_UPLOAD_BYTES": "0"},
			wantVar: "SHOWMESH_ASSET_MAX_UPLOAD_BYTES",
		},
		{
			name:    "asset max upload bytes negative",
			env:     map[string]string{"SHOWMESH_ASSET_MAX_UPLOAD_BYTES": "-1"},
			wantVar: "SHOWMESH_ASSET_MAX_UPLOAD_BYTES",
		},
		{
			name:    "asset sync interval not a duration",
			env:     map[string]string{"SHOWMESH_ASSET_SYNC_INTERVAL": "soon"},
			wantVar: "SHOWMESH_ASSET_SYNC_INTERVAL",
		},
		{
			name:    "asset sync interval zero",
			env:     map[string]string{"SHOWMESH_ASSET_SYNC_INTERVAL": "0s"},
			wantVar: "SHOWMESH_ASSET_SYNC_INTERVAL",
		},
		{
			name:    "asset inventory interval not a duration",
			env:     map[string]string{"SHOWMESH_ASSET_INVENTORY_INTERVAL": "soon"},
			wantVar: "SHOWMESH_ASSET_INVENTORY_INTERVAL",
		},
		{
			name:    "asset inventory interval zero",
			env:     map[string]string{"SHOWMESH_ASSET_INVENTORY_INTERVAL": "0s"},
			wantVar: "SHOWMESH_ASSET_INVENTORY_INTERVAL",
		},
		{
			name:    "asset content base url invalid",
			env:     map[string]string{"SHOWMESH_ASSET_CONTENT_BASE_URL": "://not a url:::"},
			wantVar: "SHOWMESH_ASSET_CONTENT_BASE_URL",
		},
		{
			name:    "asset content base url missing scheme",
			env:     map[string]string{"SHOWMESH_ASSET_CONTENT_BASE_URL": "coordinator.example:8080"},
			wantVar: "SHOWMESH_ASSET_CONTENT_BASE_URL",
		},
		{
			name:    "asset content base url unsupported scheme",
			env:     map[string]string{"SHOWMESH_ASSET_CONTENT_BASE_URL": "ftp://coordinator.example:8080"},
			wantVar: "SHOWMESH_ASSET_CONTENT_BASE_URL",
		},
		{
			name:    "asset content base url with no host",
			env:     map[string]string{"SHOWMESH_ASSET_CONTENT_BASE_URL": "http://"},
			wantVar: "SHOWMESH_ASSET_CONTENT_BASE_URL",
		},
		{
			name:    "asset content base url with userinfo",
			env:     map[string]string{"SHOWMESH_ASSET_CONTENT_BASE_URL": "http://user:pass@coordinator.example:8080"},
			wantVar: "SHOWMESH_ASSET_CONTENT_BASE_URL",
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

func TestLoadConfigAPIAllowedOriginsDefaultsToNil(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
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

// --- Step 6: ADR-024 decision 2 — SHOWMESH_API_TOKEN retirement ---

// TestLoadConfigRefusesToStartWithAPITokenSet is BUILD-PLAN Step 6's own
// acceptance criterion, at the config layer: "A coordinator started with
// SHOWMESH_API_TOKEN still set refuses to start and names the migration."
// This is deliberately a table across several non-empty values, not one
// case, because "refuses to start" is the harshest of the three behaviors
// ADR-024 decision 2 considered and rejecting it silently (by only testing
// one string) would be exactly the kind of coin-flip test CLAUDE.md warns
// against for a security-relevant refusal.
func TestLoadConfigRefusesToStartWithAPITokenSet(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
	}{
		{"short token", "x"},
		{"realistic-looking token", "top-secret-token"},
		{"whitespace-only value", " "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"SHOWMESH_API_TOKEN": tt.value}
			_, err := LoadConfigFrom(lookupFrom(env))
			if err == nil {
				t.Fatalf("LoadConfigFrom() error = nil, want a refusal naming the ADR-024 migration")
			}
			if !strings.Contains(err.Error(), "SHOWMESH_API_TOKEN") {
				t.Errorf("error = %q, want it to name SHOWMESH_API_TOKEN", err.Error())
			}
			if !strings.Contains(err.Error(), "ADR-024") {
				t.Errorf("error = %q, want it to name the ADR-024 migration", err.Error())
			}
		})
	}
}

// TestLoadConfigToleratesEmptySHOWMESHAPIToken proves the deliberate,
// documented exception in checkAPITokenRetired: a present-but-EMPTY
// SHOWMESH_API_TOKEN (a blank .env line, not a removed one) never had the
// old "reads require a bearer token" meaning under ADR-021 in the first
// place, so it must not trip the refusal — only a genuinely non-empty
// value does.
func TestLoadConfigToleratesEmptySHOWMESHAPIToken(t *testing.T) {
	env := map[string]string{"SHOWMESH_API_TOKEN": ""}
	if _, err := LoadConfigFrom(lookupFrom(env)); err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil for a present-but-empty SHOWMESH_API_TOKEN", err)
	}
}

// --- Step 6: ADR-024 read-closure, secure-cookie, and login-limiter config ---

func TestLoadConfigADR024FieldsDefaultToZeroValue(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.CloseReads {
		t.Errorf("CloseReads = true, want false (reads open) when SHOWMESH_API_CLOSE_READS is unset")
	}
	if cfg.SecureCookie {
		t.Errorf("SecureCookie = true, want false when SHOWMESH_API_SECURE_COOKIE is unset")
	}
	if cfg.TrustClientAddr {
		t.Errorf("TrustClientAddr = true, want false when SHOWMESH_API_TRUST_CLIENT_ADDR is unset")
	}
	if cfg.LoginConcurrency != 0 || cfg.LoginQueueWait != 0 || cfg.LoginPerSourceDelay != 0 || cfg.LoginMaxDelay != 0 {
		t.Errorf("login limiter fields = %+v, want all zero (api.Options.withDefaults supplies the real defaults)", cfg)
	}
}

func TestLoadConfigADR024FieldsFromEnv(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_API_CLOSE_READS":            "true",
		"SHOWMESH_API_SECURE_COOKIE":          "true",
		"SHOWMESH_API_TRUST_CLIENT_ADDR":      "true",
		"SHOWMESH_API_LOGIN_CONCURRENCY":      "8",
		"SHOWMESH_API_LOGIN_QUEUE_WAIT":       "3s",
		"SHOWMESH_API_LOGIN_PER_SOURCE_DELAY": "500ms",
		"SHOWMESH_API_LOGIN_MAX_DELAY":        "10s",
	}
	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if !cfg.CloseReads {
		t.Errorf("CloseReads = false, want true")
	}
	if !cfg.SecureCookie {
		t.Errorf("SecureCookie = false, want true")
	}
	if !cfg.TrustClientAddr {
		t.Errorf("TrustClientAddr = false, want true")
	}
	if cfg.LoginConcurrency != 8 {
		t.Errorf("LoginConcurrency = %d, want 8", cfg.LoginConcurrency)
	}
	if cfg.LoginQueueWait != 3*time.Second {
		t.Errorf("LoginQueueWait = %v, want 3s", cfg.LoginQueueWait)
	}
	if cfg.LoginPerSourceDelay != 500*time.Millisecond {
		t.Errorf("LoginPerSourceDelay = %v, want 500ms", cfg.LoginPerSourceDelay)
	}
	if cfg.LoginMaxDelay != 10*time.Second {
		t.Errorf("LoginMaxDelay = %v, want 10s", cfg.LoginMaxDelay)
	}
}

func TestLoadConfigADR024FieldsInvalidValuesAreErrors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantVar string
	}{
		{"invalid bool", map[string]string{"SHOWMESH_API_CLOSE_READS": "sorta"}, "SHOWMESH_API_CLOSE_READS"},
		{"invalid int", map[string]string{"SHOWMESH_API_LOGIN_CONCURRENCY": "many"}, "SHOWMESH_API_LOGIN_CONCURRENCY"},
		{"invalid duration", map[string]string{"SHOWMESH_API_LOGIN_QUEUE_WAIT": "a while"}, "SHOWMESH_API_LOGIN_QUEUE_WAIT"},
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

// TestConfigLogValueDoesNotRedactADR024Fields proves the seven new fields
// are NOT run through redactedPassword — none of them is a credential
// (see each Config field's doc comment) — so a debugging operator can see
// their effective values directly rather than a placeholder.
func TestConfigLogValueDoesNotRedactADR024Fields(t *testing.T) {
	cfg := Config{CloseReads: true, SecureCookie: true, TrustClientAddr: true, LoginConcurrency: 8}

	rendered := renderLogValue(t, cfg)

	if !strings.Contains(rendered, "api_close_reads") || !strings.Contains(rendered, "true") {
		t.Errorf("Config.LogValue() output = %s, want api_close_reads:true visible, not redacted", rendered)
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
				"SHOWMESH_FPP_ENDPOINTS":       "main=http://192.0.2.10",
				"SHOWMESH_FPP_MQTT_BROKER_URL": brokerURL,
				"SHOWMESH_FPP_MQTT_HOSTS":      "main=fpp-player",
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
		"SHOWMESH_FPP_ENDPOINTS":         "main=http://192.0.2.10,remote01=http://192.0.2.11",
		"SHOWMESH_FPP_MQTT_BROKER_URL":   "tcp://mqtt.example:1883",
		"SHOWMESH_FPP_MQTT_USERNAME":     "showmesh",
		"SHOWMESH_FPP_MQTT_PASSWORD":     "s3cret",
		"SHOWMESH_FPP_MQTT_TOPIC_PREFIX": "falcon/player",
		"SHOWMESH_FPP_MQTT_HOSTS":        "main=fpp-player,remote01=fpp-remote-a",
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
	wantHosts := map[string]string{"main": "fpp-player", "remote01": "fpp-remote-a"}
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
//
// SHOWMESH_FPP_ENDPOINTS is set here (unlike this test's original form)
// specifically so this stays a test of the broker-URL axis, not the
// endpoints-empty-defers-to-the-store axis Step 7 seam A adds — see
// [TestLoadConfigFPPMQTTHostsWithEmptyEndpointsDeferredNotRejected] below
// for that one.
func TestLoadConfigFPPMQTTHostsWithoutBrokerURLStillCrossChecked(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_ENDPOINTS":  "main=http://192.0.2.10",
		"SHOWMESH_FPP_MQTT_HOSTS": "shed=FPP-Shed",
	}

	_, err := LoadConfigFrom(lookupFrom(env))
	if err == nil {
		t.Fatalf("LoadConfigFrom() error = nil, want an error naming the unmatched id even with no broker URL configured")
	}
	if !strings.Contains(err.Error(), "shed") || !strings.Contains(err.Error(), "showmeshctl fpp-endpoints set") {
		t.Errorf("LoadConfigFrom() error = %q, want it to name the unmatched id %q and the store-backed remedy", err.Error(), "shed")
	}
}

// TestLoadConfigFPPMQTTHostsWithEmptyEndpointsDeferredNotRejected is Step 7
// seam A's own test: RES-008 D1 moves SHOWMESH_FPP_ENDPOINTS's authority
// into the store, so a fully migrated deployment legitimately runs with
// this variable unset while SHOWMESH_FPP_MQTT_HOSTS still names real
// instance ids — ids this package cannot see from the environment alone.
// validateFPPMQTTConfig must defer the cross-check rather than reject here
// (see that function's doc comment and [config.ValidateFPPMQTTHostIDs],
// which internal/coordinator's configsync.go calls against the resolved,
// authoritative endpoint list instead). Before trusting this test, this
// exact input was confirmed to return an error prior to that change.
func TestLoadConfigFPPMQTTHostsWithEmptyEndpointsDeferredNotRejected(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_MQTT_HOSTS": "shed=FPP-Shed",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil: an empty SHOWMESH_FPP_ENDPOINTS must defer this cross-check "+
			"to the coordinator's store-authoritative re-validation rather than reject here", err)
	}
	if len(cfg.FPPEndpoints) != 0 {
		t.Fatalf("FPPEndpoints = %v, want empty", cfg.FPPEndpoints)
	}
}

func TestLoadConfigFPPMQTTHostsToleratesWhitespace(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_ENDPOINTS":  "main=http://192.0.2.10",
		"SHOWMESH_FPP_MQTT_HOSTS": " main = fpp-player ",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	want := map[string]string{"main": "fpp-player"}
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
	cfg := Config{FPPMQTTHosts: map[string]string{"main": "fpp-player"}}

	rendered := renderLogValue(t, cfg)

	if !strings.Contains(rendered, "fpp-player") {
		t.Errorf("Config.LogValue() output = %s, want it to name the configured HostName", rendered)
	}
}

// --- Track D seam D-1: SHOWMESH_RESOLUME_URL / SHOWMESH_RESOLUME_ID ---

// TestLoadConfigResolumeUnsetIsDisabled proves the feature-flag shape
// [Config.ResolumeURL]'s doc comment claims: with SHOWMESH_RESOLUME_URL
// unset, ResolumeURL is empty (the collector never gets constructed —
// see internal/coordinator's own wiring), while ResolumeID still carries
// its default — exactly [Config.FPPMQTTTopicPrefix]'s own "defaults
// regardless of whether the feature is active" posture, applied here.
func TestLoadConfigResolumeUnsetIsDisabled(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.ResolumeURL != "" {
		t.Errorf("ResolumeURL = %q, want empty when SHOWMESH_RESOLUME_URL is unset", cfg.ResolumeURL)
	}
	if cfg.ResolumeID != defaultResolumeID {
		t.Errorf("ResolumeID = %q, want default %q even when the collector is disabled", cfg.ResolumeID, defaultResolumeID)
	}
}

// TestLoadConfigResolumeDefaultID proves SHOWMESH_RESOLUME_ID defaults to
// "resolume" when the URL is set but the id is not.
func TestLoadConfigResolumeDefaultID(t *testing.T) {
	env := map[string]string{"SHOWMESH_RESOLUME_URL": "http://127.0.0.1:9080"}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.ResolumeURL != "http://127.0.0.1:9080" {
		t.Errorf("ResolumeURL = %q, want %q", cfg.ResolumeURL, "http://127.0.0.1:9080")
	}
	if cfg.ResolumeID != "resolume" {
		t.Errorf("ResolumeID = %q, want default %q", cfg.ResolumeID, "resolume")
	}
}

// TestLoadConfigResolumeExplicitID proves an explicit SHOWMESH_RESOLUME_ID
// overrides the default, and — the port TRACK-D-ADAPTER-SPEC.md and the
// bench capture both flag as deployment configuration, not a protocol
// constant — that a non-8080 port (9080, the operator's own installation)
// round-trips unchanged rather than being silently defaulted or rejected.
func TestLoadConfigResolumeExplicitID(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_RESOLUME_URL": "http://10.0.1.30:9080",
		"SHOWMESH_RESOLUME_ID":  "resolume-main",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.ResolumeURL != "http://10.0.1.30:9080" {
		t.Errorf("ResolumeURL = %q, want %q", cfg.ResolumeURL, "http://10.0.1.30:9080")
	}
	if cfg.ResolumeID != "resolume-main" {
		t.Errorf("ResolumeID = %q, want %q", cfg.ResolumeID, "resolume-main")
	}
}

// TestLoadConfigResolumeIDCollisionWithFPPEndpoint is this seam's own
// load-bearing regression test. Before trusting it: broken by temporarily
// removing the ValidateResolumeIDAgainstFPPEndpoints call from
// validateResolumeConfig and confirmed to fail (see this task's own
// report) — a config accepting a colliding id would let the Resolume
// collector's Add call silently overwrite the FPP endpoint's own entry in
// the shared collector.Runner's nudge map (see
// ValidateResolumeIDAgainstFPPEndpoints's doc comment for the exact
// mechanism), which no test exercising Validate() in isolation could ever
// catch once the guard were removed — only this test, asserting the error
// itself, can.
func TestLoadConfigResolumeIDCollisionWithFPPEndpoint(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_ENDPOINTS": "player-01=http://10.0.1.20,shed=http://10.0.1.21",
		"SHOWMESH_RESOLUME_URL":  "http://10.0.1.30:9080",
		"SHOWMESH_RESOLUME_ID":   "shed",
	}

	_, err := LoadConfigFrom(lookupFrom(env))
	if err == nil {
		t.Fatal("LoadConfigFrom() error = nil, want an error naming the id collision between the Resolume id and the FPP endpoint id")
	}
	if !strings.Contains(err.Error(), "resolume id") || !strings.Contains(err.Error(), "shed") {
		t.Errorf("LoadConfigFrom() error = %q, want it to name the colliding id %q", err.Error(), "shed")
	}
}

// TestLoadConfigResolumeIDCollisionIgnoredWhenURLUnset proves the
// collision guard is gated on ResolumeURL exactly like every other
// Resolume check (see validateResolumeConfig's doc comment): an operator
// who has SHOWMESH_RESOLUME_ID left over from a previous configuration,
// with the URL now unset, must not be blocked from starting over an id
// that can never actually collide with anything, because the collector
// this id would apply to never gets constructed.
func TestLoadConfigResolumeIDCollisionIgnoredWhenURLUnset(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_FPP_ENDPOINTS": "shed=http://10.0.1.21",
		"SHOWMESH_RESOLUME_ID":   "shed",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil: the collision guard must not fire while ResolumeURL is unset", err)
	}
	if cfg.ResolumeID != "shed" {
		t.Errorf("ResolumeID = %q, want %q", cfg.ResolumeID, "shed")
	}
}

// TestConfigLogValueDoesNotLeakResolumeURLIsIrrelevant is deliberately not
// named "Redacts": ResolumeURL carries no credential (Validate rejects
// userinfo outright, see the "resolume url with userinfo" case in
// TestLoadConfigValidationFailures), so LogValue logs it in the clear —
// this test pins that the host/port stay visible for operator debugging,
// the opposite property from the broker-URL redaction tests above.
func TestConfigLogValueResolumeFieldsVisible(t *testing.T) {
	cfg := Config{ResolumeURL: "http://10.0.1.30:9080", ResolumeID: "resolume-main"}

	rendered := renderLogValue(t, cfg)

	if !strings.Contains(rendered, "10.0.1.30:9080") {
		t.Errorf("Config.LogValue() output = %s, want it to name the configured Resolume host", rendered)
	}
	if !strings.Contains(rendered, "resolume-main") {
		t.Errorf("Config.LogValue() output = %s, want it to name the configured Resolume id", rendered)
	}
}

// --- Track E seam E5/E6: the asset manifest and sync service (ADR-028) ---

// TestLoadConfigAssetDefaults proves every asset field defaults sensibly
// with nothing set: AssetDir under DataDir, AssetContentBaseURL empty (the
// sync service does not run), and the other three fields at their stated
// defaults.
func TestLoadConfigAssetDefaults(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.AssetDir != "/var/lib/showmesh/assets" {
		t.Errorf("AssetDir = %q, want %q", cfg.AssetDir, "/var/lib/showmesh/assets")
	}
	if cfg.AssetMaxUploadBytes != assetstore.DefaultMaxUploadBytes {
		t.Errorf("AssetMaxUploadBytes = %d, want %d", cfg.AssetMaxUploadBytes, assetstore.DefaultMaxUploadBytes)
	}
	if cfg.AssetContentBaseURL != "" {
		t.Errorf("AssetContentBaseURL = %q, want empty when SHOWMESH_ASSET_CONTENT_BASE_URL is unset", cfg.AssetContentBaseURL)
	}
	if cfg.AssetSyncInterval != defaultAssetSyncInterval {
		t.Errorf("AssetSyncInterval = %s, want default %s", cfg.AssetSyncInterval, defaultAssetSyncInterval)
	}
	if cfg.AssetInventoryInterval != defaultAssetInventoryInterval {
		t.Errorf("AssetInventoryInterval = %s, want default %s", cfg.AssetInventoryInterval, defaultAssetInventoryInterval)
	}
}

// TestLoadConfigAssetDirDefaultsUnderDataDir proves AssetDir's default
// follows SHOWMESH_DATA_DIR rather than being fixed against
// DefaultDataDir, so a deployment that relocates its data directory does
// not end up with assets on a different volume than everything else.
func TestLoadConfigAssetDirDefaultsUnderDataDir(t *testing.T) {
	env := map[string]string{"SHOWMESH_DATA_DIR": "/mnt/showmesh-data"}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.AssetDir != "/mnt/showmesh-data/assets" {
		t.Errorf("AssetDir = %q, want %q", cfg.AssetDir, "/mnt/showmesh-data/assets")
	}
}

// TestLoadConfigAssetOverridesFromEnv proves every asset field is
// independently overridable, and that a non-empty
// SHOWMESH_ASSET_CONTENT_BASE_URL round-trips (this is the "sync service
// runs" case every other asset test deliberately leaves unset).
func TestLoadConfigAssetOverridesFromEnv(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_ASSET_DIR":                "/data/assets",
		"SHOWMESH_ASSET_MAX_UPLOAD_BYTES":   "1048576",
		"SHOWMESH_ASSET_CONTENT_BASE_URL":   "https://coordinator.example:8443",
		"SHOWMESH_ASSET_SYNC_INTERVAL":      "90s",
		"SHOWMESH_ASSET_INVENTORY_INTERVAL": "30s",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.AssetDir != "/data/assets" {
		t.Errorf("AssetDir = %q, want %q", cfg.AssetDir, "/data/assets")
	}
	if cfg.AssetMaxUploadBytes != 1048576 {
		t.Errorf("AssetMaxUploadBytes = %d, want %d", cfg.AssetMaxUploadBytes, 1048576)
	}
	if cfg.AssetContentBaseURL != "https://coordinator.example:8443" {
		t.Errorf("AssetContentBaseURL = %q, want %q", cfg.AssetContentBaseURL, "https://coordinator.example:8443")
	}
	if cfg.AssetSyncInterval != 90*time.Second {
		t.Errorf("AssetSyncInterval = %s, want %s", cfg.AssetSyncInterval, 90*time.Second)
	}
	if cfg.AssetInventoryInterval != 30*time.Second {
		t.Errorf("AssetInventoryInterval = %s, want %s", cfg.AssetInventoryInterval, 30*time.Second)
	}
}

// TestConfigLogValueAssetContentBaseURLVisibleNoUserinfoPossible mirrors
// TestConfigLogValueResolumeFieldsVisible: AssetContentBaseURL carries no
// credential (validateAssetConfig rejects userinfo outright, see the
// "asset content base url with userinfo" case in
// TestLoadConfigValidationFailures), so LogValue logs it in the clear.
func TestConfigLogValueAssetContentBaseURLVisible(t *testing.T) {
	cfg := Config{AssetContentBaseURL: "https://coordinator.example:8443"}

	rendered := renderLogValue(t, cfg)

	if !strings.Contains(rendered, "coordinator.example:8443") {
		t.Errorf("Config.LogValue() output = %s, want it to name the configured asset content base URL", rendered)
	}
}

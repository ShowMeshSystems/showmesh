package config

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// lookupFrom builds a lookup function for LoadConfigFrom backed by an
// explicit map, so tests never depend on (and cannot be broken by) the
// ambient process environment. A key absent from env reports "unset".
func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

// fixedHostname builds a hostname source for LoadConfigFrom that returns h
// with no error, so tests can exercise the hostname-fallback path without
// depending on the real machine's hostname.
func fixedHostname(h string) func() (string, error) {
	return func() (string, error) { return h, nil }
}

// unreachableHostname is used where the test expects SHOWMESH_NODE_ID to
// always be set, so a call into the hostname source would indicate a bug.
func unreachableHostname(t *testing.T) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		t.Fatal("hostname() called, want SHOWMESH_NODE_ID to have been used instead")
		return "", nil
	}
}

func redactedConfig(c Config) string {
	return c.LogValue().String()
}

func TestLoadConfigDefaultsWithExplicitNodeID(t *testing.T) {
	env := map[string]string{envNodeID: "media-03"}
	cfg, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}

	want := Config{
		NodeID:       "media-03",
		NodeLabel:    "",
		MQTTBroker:   "tcp://localhost:1883",
		MQTTClientID: "showmesh-agent-media-03",
		MQTTUsername: "",
		MQTTPassword: "",
		LogLevel:     "info",
		Capabilities: capability.Set{},
	}

	if !configsEqual(cfg, want) {
		t.Errorf("LoadConfigFrom(unset) = %s, want %s", redactedConfig(cfg), redactedConfig(want))
	}
}

func TestLoadConfigFallsBackToHostname(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(nil), fixedHostname("media-07"))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.NodeID != "media-07" {
		t.Errorf("NodeID = %q, want %q (from hostname)", cfg.NodeID, "media-07")
	}
	if cfg.MQTTClientID != "showmesh-agent-media-07" {
		t.Errorf("MQTTClientID = %q, want default derived from node ID", cfg.MQTTClientID)
	}
}

func TestLoadConfigRejectsInvalidHostnameAsNodeID(t *testing.T) {
	// A real-world hostname is frequently not a valid node ID: uppercase,
	// dots, underscores. This must fail at config load with a message that
	// names the offending value and tells the operator to set SHOWMESH_NODE_ID.
	_, err := LoadConfigFrom(lookupFrom(nil), fixedHostname("Media-Node_03.local"))
	if err == nil {
		t.Fatalf("LoadConfigFrom() error = nil, want an error for an invalid hostname-derived node ID")
	}
	if !strings.Contains(err.Error(), envNodeID) {
		t.Errorf("error = %q, want it to mention %s", err.Error(), envNodeID)
	}
	if !strings.Contains(err.Error(), "Media-Node_03.local") {
		t.Errorf("error = %q, want it to name the offending hostname", err.Error())
	}
}

func TestLoadConfigRejectsInvalidExplicitNodeID(t *testing.T) {
	env := map[string]string{envNodeID: "Not_Valid"}
	_, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t))
	if err == nil {
		t.Fatalf("LoadConfigFrom() error = nil, want an error for an invalid explicit node ID")
	}
	if !strings.Contains(err.Error(), envNodeID) {
		t.Errorf("error = %q, want it to mention %s", err.Error(), envNodeID)
	}
}

func TestLoadConfigEmptyExplicitNodeIDFallsBackToHostname(t *testing.T) {
	// An explicitly-set-but-empty SHOWMESH_NODE_ID is treated the same as
	// unset, matching how every other variable in this package works
	// (getEnvDefault only distinguishes "present in the map" from
	// "absent", but an empty override is never useful here).
	env := map[string]string{envNodeID: ""}
	cfg, err := LoadConfigFrom(lookupFrom(env), fixedHostname("media-09"))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.NodeID != "media-09" {
		t.Errorf("NodeID = %q, want fallback to hostname %q", cfg.NodeID, "media-09")
	}
}

func TestLoadConfigOverridesFromEnv(t *testing.T) {
	env := map[string]string{
		envNodeID:           "media-03",
		envNodeLabel:        "Media Node 03",
		envMQTTBroker:       "tcp://broker.example.com:1883",
		envMQTTClientID:     "test-client",
		envMQTTUsername:     "alice",
		envMQTTPassword:     "s3cret",
		envLogLevel:         "debug",
		envNodeCapabilities: "matrix.render:2,transport.ndi.send",
	}

	cfg, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}

	want := Config{
		NodeID:       "media-03",
		NodeLabel:    "Media Node 03",
		MQTTBroker:   "tcp://broker.example.com:1883",
		MQTTClientID: "test-client",
		MQTTUsername: "alice",
		MQTTPassword: "s3cret",
		LogLevel:     "debug",
		Capabilities: capability.Set{
			{ID: "matrix.render", Version: 2},
			{ID: "transport.ndi.send", Version: 1},
		},
	}

	if !configsEqual(cfg, want) {
		t.Errorf("LoadConfigFrom(overrides) = %s, want %s", redactedConfig(cfg), redactedConfig(want))
	}
}

func TestLoadConfigCapabilitiesValidationFailures(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "invalid id syntax", raw: "NotValid"},
		{name: "non integer version", raw: "matrix.render:abc"},
		{name: "duplicate id", raw: "matrix.render,matrix.render"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{envNodeID: "media-03", envNodeCapabilities: tt.raw}
			_, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t))
			if err == nil {
				t.Fatalf("LoadConfigFrom() error = nil, want an error for capabilities %q", tt.raw)
			}
			if !strings.Contains(err.Error(), envNodeCapabilities) {
				t.Errorf("error = %q, want it to mention %s", err.Error(), envNodeCapabilities)
			}
		})
	}
}

func TestLoadConfigValidationFailures(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantVar string
	}{
		{
			name:    "empty mqtt broker",
			env:     map[string]string{envNodeID: "media-03", envMQTTBroker: ""},
			wantVar: envMQTTBroker,
		},
		{
			name:    "mqtt broker missing scheme",
			env:     map[string]string{envNodeID: "media-03", envMQTTBroker: "broker-host:1883"},
			wantVar: envMQTTBroker,
		},
		{
			name:    "mqtt broker unknown scheme",
			env:     map[string]string{envNodeID: "media-03", envMQTTBroker: "ftp://localhost:1883"},
			wantVar: envMQTTBroker,
		},
		{
			name:    "invalid log level",
			env:     map[string]string{envNodeID: "media-03", envLogLevel: "trace"},
			wantVar: envLogLevel,
		},
		{
			// mqtt.go's newMQTTConn only sets credentials on the connection
			// when MQTTUsername is non-empty, so a password with no
			// username would otherwise be silently dropped rather than
			// sent to the broker; this must fail at config load instead of
			// surfacing as a confusing broker-side auth failure (or,
			// against an anonymous-allowed broker, an unauthenticated
			// connect the operator never intended).
			name:    "mqtt password without username",
			env:     map[string]string{envNodeID: "media-03", envMQTTPassword: "s3cret"},
			wantVar: envMQTTPassword,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfigFrom(lookupFrom(tt.env), unreachableHostname(t))
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
				envNodeID:     "media-03",
				envMQTTBroker: scheme + "://broker.example.com:1883",
			}

			cfg, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t))
			if err != nil {
				t.Fatalf("LoadConfigFrom() error = %v, want nil for accepted scheme %q", err, scheme)
			}
			if cfg.MQTTBroker != env[envMQTTBroker] {
				t.Errorf("MQTTBroker = %q, want %q", cfg.MQTTBroker, env[envMQTTBroker])
			}
		})
	}
}

func TestConfigLogValueRedactsPassword(t *testing.T) {
	cfg := Config{
		NodeID:       "media-03",
		MQTTBroker:   "tcp://localhost:1883",
		MQTTClientID: "showmesh-agent-media-03",
		MQTTUsername: "alice",
		MQTTPassword: "s3cret-value-must-not-appear",
		LogLevel:     "info",
	}

	rendered := renderLogValue(t, cfg)

	if strings.Contains(rendered, cfg.MQTTPassword) {
		t.Fatalf("Config.LogValue() output contains the raw password: %s", rendered)
	}
	if !strings.Contains(rendered, redactedPassword) {
		t.Errorf("Config.LogValue() output = %s, want it to contain the redaction placeholder %q", rendered, redactedPassword)
	}
	if !strings.Contains(rendered, "alice") {
		t.Errorf("Config.LogValue() output = %s, want it to still contain the non-sensitive username", rendered)
	}
}

func TestConfigLogValueEmptyPassword(t *testing.T) {
	cfg := Config{NodeID: "media-03", MQTTPassword: ""}

	rendered := renderLogValue(t, cfg)

	if strings.Contains(rendered, redactedPassword) {
		t.Errorf("Config.LogValue() output = %s, want no redaction placeholder when password is unset", rendered)
	}
}

func renderLogValue(t *testing.T, cfg Config) string {
	t.Helper()

	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("config", "cfg", cfg)

	return buf.String()
}

// configsEqual compares two Configs field by field via reflect-free
// equality: Config embeds a capability.Set (a slice), so a bare == is not
// usable the way it is for internal/coordinator/config.Config.
func configsEqual(a, b Config) bool {
	if a.NodeID != b.NodeID ||
		a.NodeLabel != b.NodeLabel ||
		a.MQTTBroker != b.MQTTBroker ||
		a.MQTTClientID != b.MQTTClientID ||
		a.MQTTUsername != b.MQTTUsername ||
		a.MQTTPassword != b.MQTTPassword ||
		a.LogLevel != b.LogLevel {
		return false
	}
	if len(a.Capabilities) != len(b.Capabilities) {
		return false
	}
	for i := range a.Capabilities {
		// Capability.Attributes is a map, so Capability is not comparable
		// with !=; parseCapabilities never sets Attributes, so comparing
		// ID and Version is sufficient here.
		if a.Capabilities[i].ID != b.Capabilities[i].ID || a.Capabilities[i].Version != b.Capabilities[i].Version {
			return false
		}
	}
	return true
}

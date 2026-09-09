package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"

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
		NodeID:                 "media-03",
		NodeLabel:              "",
		MQTTBroker:             "tcp://localhost:1883",
		MQTTClientID:           "showmesh-agent-media-03",
		MQTTUsername:           "",
		MQTTPassword:           "",
		LogLevel:               "info",
		Capabilities:           capability.Set{},
		AssetDir:               "./assets",
		AgentAPIToken:          "",
		AssetInventoryInterval: 2 * time.Minute,
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
		envNodeID:                 "media-03",
		envNodeLabel:              "Media Node 03",
		envMQTTBroker:             "tcp://broker.example.com:1883",
		envMQTTClientID:           "test-client",
		envMQTTUsername:           "alice",
		envMQTTPassword:           "s3cret",
		envLogLevel:               "debug",
		envNodeCapabilities:       "matrix.render:2,transport.ndi.send",
		envAssetDir:               "/var/lib/showmesh/assets",
		envAgentAPIToken:          "tok-abc",
		envAssetInventoryInterval: "30s",
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
		AssetDir:               "/var/lib/showmesh/assets",
		AgentAPIToken:          "tok-abc",
		AssetInventoryInterval: 30 * time.Second,
	}

	if !configsEqual(cfg, want) {
		t.Errorf("LoadConfigFrom(overrides) = %s, want %s", redactedConfig(cfg), redactedConfig(want))
	}
}

// TestLoadConfigAssetInventoryIntervalValidationFailures proves a malformed
// SHOWMESH_ASSET_INVENTORY_INTERVAL is a startup error naming the variable,
// matching this package's posture for every other typed field.
//
// Every case also asserts the raw offending value appears in the error.
// Config.Validate has its own, independent "AssetInventoryInterval must be
// positive" guard (defense in depth against a future caller constructing a
// Config directly, bypassing LoadConfigFrom's env parsing), and an earlier
// version of this test asserted only "the error mentions the variable name"
// — which every one of these three cases satisfies EVEN WITH the
// LoadConfigFrom-level parse/positivity checks deleted, because
// Validate's own fallback message also mentions the variable name. Checking
// for the raw value pins this test to the parse-time error specifically,
// so deleting either guard on its own is caught: deleting LoadConfigFrom's
// checks makes this test fail (Validate's message never includes the raw
// string), and deleting Validate's own check (proven separately) does not
// mask a LoadConfigFrom regression either.
func TestLoadConfigAssetInventoryIntervalValidationFailures(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "not a duration", raw: "soon"},
		{name: "zero", raw: "0s"},
		{name: "negative", raw: "-5m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{envNodeID: "media-03", envAssetInventoryInterval: tt.raw}
			_, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t))
			if err == nil {
				t.Fatalf("LoadConfigFrom() error = nil, want an error for %s %q", envAssetInventoryInterval, tt.raw)
			}
			if !strings.Contains(err.Error(), envAssetInventoryInterval) {
				t.Errorf("error = %q, want it to mention %s", err.Error(), envAssetInventoryInterval)
			}
			if !strings.Contains(err.Error(), tt.raw) {
				t.Errorf("error = %q, want it to name the offending raw value %q (pins this to LoadConfigFrom's own parse-time check, not Validate's generic fallback)", err.Error(), tt.raw)
			}
		})
	}
}

// TestConfigValidateRejectsNonPositiveAssetInventoryInterval exercises
// Config.Validate's own AssetInventoryInterval guard directly, independent
// of LoadConfigFrom's env-parsing layer — the defense-in-depth check
// TestLoadConfigAssetInventoryIntervalValidationFailures's tightened
// raw-value assertion deliberately does NOT exercise (that test now pins
// itself to the OTHER layer). A Config built directly (as a future caller
// might, bypassing LoadConfigFrom) with a non-positive interval must still
// be rejected.
func TestConfigValidateRejectsNonPositiveAssetInventoryInterval(t *testing.T) {
	base := Config{
		NodeID:                 "media-03",
		MQTTBroker:             defaultBroker,
		LogLevel:               defaultLogLevel,
		AssetDir:               defaultAssetDir,
		AssetInventoryInterval: 0,
	}
	if err := base.Validate(); err == nil {
		t.Fatalf("Validate() error = nil, want an error for a zero AssetInventoryInterval")
	} else if !strings.Contains(err.Error(), envAssetInventoryInterval) {
		t.Errorf("error = %q, want it to mention %s", err.Error(), envAssetInventoryInterval)
	}
}

// TestLoadConfigEmptyAssetDirRejected proves an explicitly-empty
// SHOWMESH_ASSET_DIR is a startup error rather than a silent empty-string
// asset directory (which would resolve every asset path relative to the
// process's working directory with no visible warning).
func TestLoadConfigEmptyAssetDirRejected(t *testing.T) {
	env := map[string]string{envNodeID: "media-03", envAssetDir: ""}
	_, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t))
	if err == nil {
		t.Fatalf("LoadConfigFrom() error = nil, want an error for an empty %s", envAssetDir)
	}
	if !strings.Contains(err.Error(), envAssetDir) {
		t.Errorf("error = %q, want it to mention %s", err.Error(), envAssetDir)
	}
}

// TestConfigLogValueRedactsAgentAPIToken mirrors
// TestConfigLogValueRedactsPassword for the new agent API token field.
func TestConfigLogValueRedactsAgentAPIToken(t *testing.T) {
	cfg := Config{
		NodeID:        "media-03",
		AgentAPIToken: "s3cret-token-must-not-appear",
	}

	rendered := renderLogValue(t, cfg)

	if strings.Contains(rendered, cfg.AgentAPIToken) {
		t.Fatalf("Config.LogValue() output contains the raw token: %s", rendered)
	}
	if !strings.Contains(rendered, redactedPassword) {
		t.Errorf("Config.LogValue() output = %s, want it to contain the redaction placeholder %q", rendered, redactedPassword)
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

// TestLoadConfigFPPConnectListenAddrDefault proves the default is ":80":
// xLights hardcodes port 80 in both the discovery and upload URLs it
// builds (RES-003 section 10.4), so an unset override must still bind the
// port xLights will actually contact.
func TestLoadConfigFPPConnectListenAddrDefault(t *testing.T) {
	env := map[string]string{envNodeID: "media-03"}
	cfg, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.FPPConnectListenAddr != ":80" {
		t.Errorf("FPPConnectListenAddr = %q, want %q", cfg.FPPConnectListenAddr, ":80")
	}
}

// TestLoadConfigFPPConnectListenAddrOverride proves
// SHOWMESH_FPPCONNECT_LISTEN_ADDR overrides the default, the escape hatch
// ADR-044 decision 5 grants dev stacks and tests that cannot bind :80.
func TestLoadConfigFPPConnectListenAddrOverride(t *testing.T) {
	env := map[string]string{envNodeID: "media-03", envFPPConnectListenAddr: "127.0.0.1:8080"}
	cfg, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t))
	if err != nil {
		t.Fatalf("LoadConfigFrom() error = %v, want nil", err)
	}
	if cfg.FPPConnectListenAddr != "127.0.0.1:8080" {
		t.Errorf("FPPConnectListenAddr = %q, want %q", cfg.FPPConnectListenAddr, "127.0.0.1:8080")
	}
}

// TestLoadConfigFPPConnectListenAddrEmptyIsRejected proves an explicitly
// empty override is refused rather than silently accepted: a bind address
// must be known before the process starts (ADR-039 decision 9), and an
// empty string is not one.
func TestLoadConfigFPPConnectListenAddrEmptyIsRejected(t *testing.T) {
	env := map[string]string{envNodeID: "media-03", envFPPConnectListenAddr: ""}
	_, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t))
	if err == nil {
		t.Fatal("LoadConfigFrom() error = nil, want an error for an empty SHOWMESH_FPPCONNECT_LISTEN_ADDR")
	}
	if !strings.Contains(err.Error(), envFPPConnectListenAddr) {
		t.Errorf("error = %q, want it to mention %s", err.Error(), envFPPConnectListenAddr)
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
		a.LogLevel != b.LogLevel ||
		a.AssetDir != b.AssetDir ||
		a.AgentAPIToken != b.AgentAPIToken ||
		a.AssetInventoryInterval != b.AssetInventoryInterval {
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

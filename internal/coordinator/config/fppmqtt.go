package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Track G seam G-3 (ADR-039): the "fpp.mqtt" configuration kind for the
// Step 5 FPP MQTT collector, replacing SHOWMESH_FPP_MQTT_BROKER_URL/
// USERNAME/PASSWORD/TOPIC_PREFIX/HOSTS. Mirrors fppendpoints.go's shape.
// The broker password is deliberately absent from this file's payload
// (ADR-039 decision 7): it lives in a separate mutable file, never in an
// immutable config_revisions row — see fppmqttsecret.go.

const (
	// FPPMQTTConfigKind is config_objects.kind/config_revisions.kind and
	// the second path segment of GET/PUT /api/v1/config/fpp.mqtt.
	FPPMQTTConfigKind = "fpp.mqtt"

	// FPPMQTTConfigObjectID is the single config_objects.id this kind ever
	// uses — one FPP MQTT collector configuration per coordinator.
	FPPMQTTConfigObjectID = "default"

	FPPMQTTSourceAPI          = "api"
	FPPMQTTSourceEnvMigration = "env_migration"
)

// FPPMQTTConfig is fpp.mqtt's non-secret shape: everything
// SHOWMESH_FPP_MQTT_* carried except the password.
type FPPMQTTConfig struct {
	BrokerURL   string
	Username    string
	TopicPrefix string
	Hosts       map[string]string
}

// Configured reports whether c describes an active collector — the same
// test coordinator.go's construction gate uses (BrokerURL != "").
func (c FPPMQTTConfig) Configured() bool { return c.BrokerURL != "" }

// FPPMQTTPayload is config_revisions.payload_json's decoded shape for
// [FPPMQTTConfigKind]. PasswordSet is a REFERENCE, not the secret itself
// (ADR-039 decision 7): whether a password was set as of this revision,
// never its value.
type FPPMQTTPayload struct {
	BrokerURL   string            `json:"brokerURL"`
	Username    string            `json:"username"`
	TopicPrefix string            `json:"topicPrefix"`
	Hosts       map[string]string `json:"hosts"`
	PasswordSet bool              `json:"passwordSet"`
}

// EncodeFPPMQTTPayload marshals cfg and passwordSet into config_revisions'
// payload_json column shape. Hosts is never nil in the encoded output.
func EncodeFPPMQTTPayload(cfg FPPMQTTConfig, passwordSet bool) (string, error) {
	hosts := cfg.Hosts
	if hosts == nil {
		hosts = map[string]string{}
	}
	b, err := json.Marshal(FPPMQTTPayload{
		BrokerURL:   cfg.BrokerURL,
		Username:    cfg.Username,
		TopicPrefix: cfg.TopicPrefix,
		Hosts:       hosts,
		PasswordSet: passwordSet,
	})
	if err != nil {
		return "", fmt.Errorf("config: encode fpp.mqtt payload: %w", err)
	}
	return string(b), nil
}

// DecodeFPPMQTTPayload is [EncodeFPPMQTTPayload]'s inverse, returning the
// non-secret config plus the passwordSet marker.
func DecodeFPPMQTTPayload(raw string) (FPPMQTTConfig, bool, error) {
	var payload FPPMQTTPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return FPPMQTTConfig{}, false, fmt.Errorf("config: decode fpp.mqtt payload: %w", err)
	}
	hosts := payload.Hosts
	if hosts == nil {
		hosts = map[string]string{}
	}
	return FPPMQTTConfig{
		BrokerURL:   payload.BrokerURL,
		Username:    payload.Username,
		TopicPrefix: payload.TopicPrefix,
		Hosts:       hosts,
	}, payload.PasswordSet, nil
}

// FPPMQTTConfigEqual compares the non-secret fields only. The env->store
// migration disagreement check compares the password separately (see
// coordinator's fppmqttsync.go), since it never lives in this struct.
func FPPMQTTConfigEqual(a, b FPPMQTTConfig) bool {
	if a.BrokerURL != b.BrokerURL || a.Username != b.Username || a.TopicPrefix != b.TopicPrefix {
		return false
	}
	return fppMQTTHostsEqual(a.Hosts, b.Hosts)
}

func fppMQTTHostsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// ValidateFPPMQTTConfigKind validates one fpp.mqtt payload: the same
// broker URL/host shape [validateFPPMQTTConfig] already applies to the
// environment-sourced Config, applied to the store-backed struct, plus
// [fppmqtt.New]'s own duplicate-HostName rejection so a shape accepted
// here can never fail construction later.
func ValidateFPPMQTTConfigKind(cfg FPPMQTTConfig, fppEndpoints []FPPEndpoint) error {
	if cfg.BrokerURL != "" {
		if brokerURLHasUserinfo(cfg.BrokerURL) {
			return fmt.Errorf("fpp.mqtt: brokerURL must not embed credentials in the URL; set username/password instead")
		}
		u, err := url.Parse(cfg.BrokerURL)
		if err != nil {
			return fmt.Errorf("fpp.mqtt: brokerURL %q is not a valid URL: %w", cfg.BrokerURL, err)
		}
		if !validBrokerSchemes[u.Scheme] {
			return fmt.Errorf("fpp.mqtt: brokerURL %q must use one of the schemes %s", cfg.BrokerURL, strings.Join(validBrokerSchemesList, ", "))
		}
		if u.Host == "" {
			return fmt.Errorf("fpp.mqtt: brokerURL %q must include a host", cfg.BrokerURL)
		}
		if len(cfg.Hosts) == 0 {
			return fmt.Errorf("fpp.mqtt: brokerURL is set but hosts is empty")
		}
	}

	seenHostNames := make(map[string]string, len(cfg.Hosts))
	for id, hostName := range cfg.Hosts {
		if err := mqttproto.ValidateNodeID(id); err != nil {
			return fmt.Errorf("fpp.mqtt: instance id %q: %w", id, err)
		}
		if err := validateFPPMQTTHostName(hostName); err != nil {
			return fmt.Errorf("fpp.mqtt: instance %q: %w", id, err)
		}
		if existing, dup := seenHostNames[hostName]; dup {
			return fmt.Errorf("fpp.mqtt: HostName %q is configured for both instance %q and %q", hostName, existing, id)
		}
		seenHostNames[hostName] = id
	}

	if len(cfg.Hosts) == 0 {
		return nil
	}
	return ValidateFPPMQTTHostIDs(cfg.Hosts, fppEndpoints)
}

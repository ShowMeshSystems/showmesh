package config

import (
	"strings"
	"testing"
)

func TestEncodeDecodeFPPMQTTPayloadRoundTrip(t *testing.T) {
	cfg := FPPMQTTConfig{
		BrokerURL: "tcp://broker:1883", Username: "showmesh",
		TopicPrefix: "falcon/player", Hosts: map[string]string{"player-01": "FPP-Player"},
	}
	raw, err := EncodeFPPMQTTPayload(cfg, true)
	if err != nil {
		t.Fatalf("EncodeFPPMQTTPayload: %v", err)
	}
	got, passwordSet, err := DecodeFPPMQTTPayload(raw)
	if err != nil {
		t.Fatalf("DecodeFPPMQTTPayload: %v", err)
	}
	if !FPPMQTTConfigEqual(got, cfg) {
		t.Errorf("round trip = %+v, want %+v", got, cfg)
	}
	if !passwordSet {
		t.Errorf("passwordSet = false, want true")
	}
}

func TestEncodeFPPMQTTPayloadNeverNullHosts(t *testing.T) {
	raw, err := EncodeFPPMQTTPayload(FPPMQTTConfig{}, false)
	if err != nil {
		t.Fatalf("EncodeFPPMQTTPayload: %v", err)
	}
	cfg, passwordSet, err := DecodeFPPMQTTPayload(raw)
	if err != nil {
		t.Fatalf("DecodeFPPMQTTPayload: %v", err)
	}
	if cfg.Hosts == nil {
		t.Errorf("decoded Hosts = nil, want a non-nil empty map")
	}
	if passwordSet {
		t.Errorf("passwordSet = true, want false")
	}
}

func TestFPPMQTTConfigEqual(t *testing.T) {
	a := FPPMQTTConfig{BrokerURL: "tcp://b:1883", Username: "u", TopicPrefix: "p", Hosts: map[string]string{"x": "y"}}
	b := FPPMQTTConfig{BrokerURL: "tcp://b:1883", Username: "u", TopicPrefix: "p", Hosts: map[string]string{"x": "y"}}
	if !FPPMQTTConfigEqual(a, b) {
		t.Errorf("identical configs reported unequal")
	}
	c := b
	c.Username = "different"
	if FPPMQTTConfigEqual(a, c) {
		t.Errorf("differing username reported equal")
	}
	d := b
	d.Hosts = map[string]string{"x": "different"}
	if FPPMQTTConfigEqual(a, d) {
		t.Errorf("differing host value reported equal")
	}
	e := b
	e.Hosts = map[string]string{"other": "y"}
	if FPPMQTTConfigEqual(a, e) {
		t.Errorf("differing host key reported equal")
	}
}

func TestValidateFPPMQTTConfigKindRejectsBrokerWithNoHosts(t *testing.T) {
	cfg := FPPMQTTConfig{BrokerURL: "tcp://broker:1883"}
	if err := ValidateFPPMQTTConfigKind(cfg, nil); err == nil {
		t.Fatalf("ValidateFPPMQTTConfigKind: want an error for a broker URL with no hosts")
	}
}

func TestValidateFPPMQTTConfigKindRejectsUserinfo(t *testing.T) {
	cfg := FPPMQTTConfig{
		BrokerURL: "tcp://user:pass@broker:1883",
		Hosts:     map[string]string{"player-01": "FPP-Player"},
	}
	if err := ValidateFPPMQTTConfigKind(cfg, nil); err == nil {
		t.Fatalf("ValidateFPPMQTTConfigKind: want an error for userinfo embedded in the broker URL")
	}
}

func TestValidateFPPMQTTConfigKindRejectsHostIDNotInFPPEndpoints(t *testing.T) {
	cfg := FPPMQTTConfig{
		BrokerURL: "tcp://broker:1883",
		Hosts:     map[string]string{"player-01": "FPP-Player"},
	}
	if err := ValidateFPPMQTTConfigKind(cfg, nil); err == nil {
		t.Fatalf("ValidateFPPMQTTConfigKind: want an error when the host id is not a configured fpp.endpoints id")
	}
	if err := ValidateFPPMQTTConfigKind(cfg, []FPPEndpoint{{ID: "player-01", URL: "http://player-01"}}); err != nil {
		t.Fatalf("ValidateFPPMQTTConfigKind: unexpected error once the host id is a configured fpp.endpoints id: %v", err)
	}
}

func TestValidateFPPMQTTConfigKindRejectsDuplicateHostName(t *testing.T) {
	cfg := FPPMQTTConfig{
		Hosts: map[string]string{"player-01": "FPP-Player", "player-02": "FPP-Player"},
	}
	if err := ValidateFPPMQTTConfigKind(cfg, nil); err == nil {
		t.Fatalf("ValidateFPPMQTTConfigKind: want an error when two instance ids share one HostName")
	}
}

func TestValidateFPPMQTTConfigKindAllowsUnconfigured(t *testing.T) {
	if err := ValidateFPPMQTTConfigKind(FPPMQTTConfig{}, nil); err != nil {
		t.Fatalf("ValidateFPPMQTTConfigKind: unexpected error for a fully empty (unconfigured) payload: %v", err)
	}
}

// TestValidateFPPMQTTConfigKindHostIDNotInFPPEndpointsNamesTheStoreRemedy
// pins ADR-039's fix (decision 1): reached through the store-backed
// config:write surface (`showmeshctl fpp-mqtt set --host`), an unmatched
// host id must lead with the `showmeshctl config set` / fpp.endpoints
// remedy. SHOWMESH_FPP_MQTT_HOSTS still gets named too (a startup failure
// here is often a typo in that variable, not a missing endpoint), but only
// after the store remedy, and SHOWMESH_FPP_ENDPOINTS stays a trailing
// hedge for a coordinator that has not migrated.
func TestValidateFPPMQTTConfigKindHostIDNotInFPPEndpointsNamesTheStoreRemedy(t *testing.T) {
	cfg := FPPMQTTConfig{
		BrokerURL: "tcp://broker:1883",
		Hosts:     map[string]string{"fpp1": "fpp-one"},
	}
	err := ValidateFPPMQTTConfigKind(cfg, nil)
	if err == nil {
		t.Fatalf("ValidateFPPMQTTConfigKind: want an error for host id %q with no configured fpp.endpoints", "fpp1")
	}
	got := err.Error()
	if !strings.Contains(got, "fpp1") {
		t.Errorf("error = %q, want it to name the unmatched instance id %q", got, "fpp1")
	}
	storeIdx := strings.Index(got, "showmeshctl config set")
	if storeIdx < 0 || !strings.Contains(got, "fpp.endpoints") {
		t.Fatalf("error = %q, want it to lead with the store-backed remedy (showmeshctl config set / fpp.endpoints)", got)
	}
	if !strings.Contains(got, "SHOWMESH_FPP_MQTT_HOSTS") {
		t.Errorf("error = %q, want it to also name SHOWMESH_FPP_MQTT_HOSTS as a possible typo", got)
	}
	if idx := strings.Index(got, "SHOWMESH_FPP_ENDPOINTS"); idx >= 0 && idx < storeIdx {
		t.Errorf("error = %q, want the store remedy to lead and SHOWMESH_FPP_ENDPOINTS to appear only as a trailing hedge", got)
	}
}

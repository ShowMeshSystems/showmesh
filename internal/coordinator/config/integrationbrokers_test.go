package config

import "testing"

func TestParseIntegrationBrokersUnsetReturnsNil(t *testing.T) {
	brokers, err := parseIntegrationBrokers("", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if brokers != nil {
		t.Fatalf("expected nil brokers for unset env, got %+v", brokers)
	}
}

func TestParseIntegrationBrokersOnePair(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_INTEGRATION_BROKER_HOME_AUTOMATION_USERNAME": "showmesh",
		"SHOWMESH_INTEGRATION_BROKER_HOME_AUTOMATION_PASSWORD": "secret",
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	brokers, err := parseIntegrationBrokers("home-automation=tcp://10.0.0.5:1883", lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(brokers) != 1 {
		t.Fatalf("expected 1 broker, got %d", len(brokers))
	}
	b := brokers[0]
	if b.ID != "home-automation" || b.URL != "tcp://10.0.0.5:1883" || b.Username != "showmesh" || b.Password != "secret" {
		t.Fatalf("unexpected broker: %+v", b)
	}
}

func TestParseIntegrationBrokersMultiplePairs(t *testing.T) {
	brokers, err := parseIntegrationBrokers("a=tcp://host-a:1883,b=tcp://host-b:1883", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(brokers) != 2 {
		t.Fatalf("expected 2 brokers, got %d", len(brokers))
	}
}

func TestParseIntegrationBrokersDuplicateIdentifierIsError(t *testing.T) {
	_, err := parseIntegrationBrokers("a=tcp://host-a:1883,a=tcp://host-b:1883", func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("expected an error for a duplicate identifier")
	}
}

func TestParseIntegrationBrokersEmptyIdentifierIsError(t *testing.T) {
	_, err := parseIntegrationBrokers("=tcp://host-a:1883", func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("expected an error for an empty identifier")
	}
}

func TestParseIntegrationBrokersMalformedURLIsError(t *testing.T) {
	cases := []string{
		"a=not-a-url-at-all-with-no-scheme",
		"a=ftp://host:21", // unsupported scheme
		"a=tcp://",        // no host
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := parseIntegrationBrokers(raw, func(string) (string, bool) { return "", false })
			if err == nil {
				t.Fatalf("expected an error for %q", raw)
			}
		})
	}
}

func TestParseIntegrationBrokersRejectsEmbeddedUserinfo(t *testing.T) {
	_, err := parseIntegrationBrokers("a=tcp://user:pass@host:1883", func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("expected an error for a URL embedding userinfo")
	}
}

func TestParseIntegrationBrokersMalformedEntryShape(t *testing.T) {
	cases := []string{
		"nokey",
		"id=",
		"=url",
		"a=tcp://x,,b=tcp://y",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := parseIntegrationBrokers(raw, func(string) (string, bool) { return "", false })
			if err == nil {
				t.Fatalf("expected an error for %q", raw)
			}
		})
	}
}

// TestParseIntegrationBrokersNeverAutoRegistersControlPlaneBroker confirms
// the wave 2 shared contract section 5's rule directly: the control-plane
// broker (SHOWMESH_MQTT_BROKER) is never seeded into IntegrationBrokers,
// regardless of whether it is set — this parser never reads
// envMQTTBroker/MQTTBroker at all, and this test is a guard against a
// future edit accidentally wiring the two together.
func TestParseIntegrationBrokersNeverAutoRegistersControlPlaneBroker(t *testing.T) {
	env := map[string]string{envMQTTBroker: "tcp://control-plane-broker:1883"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	brokers, err := parseIntegrationBrokers("", lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(brokers) != 0 {
		t.Fatalf("expected no integration brokers when SHOWMESH_INTEGRATION_BROKERS is unset, got %+v", brokers)
	}

	brokers, err = parseIntegrationBrokers("home-automation=tcp://10.0.0.5:1883", lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, b := range brokers {
		if b.URL == "tcp://control-plane-broker:1883" {
			t.Fatalf("control-plane broker leaked into declared integration brokers: %+v", brokers)
		}
	}
}

func TestLoadConfigFromWiresIntegrationBrokers(t *testing.T) {
	env := map[string]string{
		"SHOWMESH_INTEGRATION_BROKERS":                         "home-automation=tcp://10.0.0.5:1883",
		"SHOWMESH_INTEGRATION_BROKER_HOME_AUTOMATION_USERNAME": "showmesh",
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	cfg, err := LoadConfigFrom(lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.IntegrationBrokers) != 1 || cfg.IntegrationBrokers[0].ID != "home-automation" {
		t.Fatalf("unexpected IntegrationBrokers: %+v", cfg.IntegrationBrokers)
	}
	if cfg.IntegrationBrokers[0].Username != "showmesh" {
		t.Fatalf("unexpected username: %q", cfg.IntegrationBrokers[0].Username)
	}
}

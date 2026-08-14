package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is Step 9 wave 2 Builder A's own addition, per the wave 2
// shared contract section 5: SHOWMESH_INTEGRATION_BROKERS, the deployment's
// declared set of external MQTT brokers a show.action's "mqtt" target may
// name (STEP-9-SPEC.md section 2.10). This is a SECOND, unrelated broker
// declaration from Config.MQTTBroker (the ADR-008 control-plane broker) and
// from Config.FPPMQTTBrokerURL (Step 5's single FPP-ingestion broker):
// SHOWMESH_INTEGRATION_BROKERS can name several, each addressed by an
// operator-chosen identifier, and — the rule that matters most — THE
// CONTROL-PLANE BROKER IS NEVER AUTO-REGISTERED UNDER ANY IDENTIFIER HERE.
// STEP-9-SPEC.md section 2.10 and ADR-008: the integration broker is an
// integration target, never a control-plane participant, and there is no
// default — an action naming no broker is rejected at write time
// (showaction.go's decodeMQTTTarget). This file's own parser never reads
// Config.MQTTBroker or envMQTTBroker at all, which is what makes that
// guarantee true rather than merely documented.

// IntegrationBroker is one declared external MQTT broker.
type IntegrationBroker struct {
	// ID is the identifier a show.action's target.broker field names —
	// validated with the same [mqttproto.ValidateNodeID] grammar
	// SHOWMESH_FPP_ENDPOINTS's instance ids already use (lowercase
	// letters, digits, hyphens), which is also exactly what makes the
	// upper-cased, hyphen-to-underscore env var derivation below
	// unambiguous.
	ID string

	// URL is the broker's connection URL, e.g. "tcp://10.0.0.5:1883". Never
	// carries userinfo — see brokerURLHasUserinfo's existing use in this
	// package for MQTTBroker and FPPMQTTBrokerURL, applied identically
	// here.
	URL string

	// Username and Password are this broker's own optional credentials,
	// from SHOWMESH_INTEGRATION_BROKER_<ID>_USERNAME/_PASSWORD. Password is
	// exactly as sensitive as MQTTPassword/FPPMQTTPassword and must never
	// appear in an error, a log line, or LogValue's output in the clear.
	Username string
	Password string
}

const (
	// envIntegrationBrokers is SHOWMESH_INTEGRATION_BROKERS, a
	// comma-separated list of "id=url" pairs, e.g.
	// "home-automation=tcp://10.0.0.5:1883". Unset or empty means no
	// integration brokers are declared, and every mqtt show.action target
	// is rejected at write time for naming an undeclared broker.
	envIntegrationBrokers = "SHOWMESH_INTEGRATION_BROKERS"
)

// integrationBrokerUsernameEnv and integrationBrokerPasswordEnv derive the
// per-identifier credential env var names, per the wave 2 shared contract
// section 5: "<ID> is the identifier upper-cased with '-' replaced by
// '_'." mqttproto.ValidateNodeID (already enforced on id before either of
// these is ever called — see parseIntegrationBrokers) guarantees id
// contains only lowercase letters, digits, and hyphens, so this
// transformation is total and unambiguous: no other character survives
// validation that upper-casing or the hyphen replacement could collide on.
func integrationBrokerUsernameEnv(id string) string {
	return "SHOWMESH_INTEGRATION_BROKER_" + integrationBrokerEnvSuffix(id) + "_USERNAME"
}

func integrationBrokerPasswordEnv(id string) string {
	return "SHOWMESH_INTEGRATION_BROKER_" + integrationBrokerEnvSuffix(id) + "_PASSWORD"
}

func integrationBrokerEnvSuffix(id string) string {
	return strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
}

// parseIntegrationBrokers parses and validates SHOWMESH_INTEGRATION_BROKERS
// plus each declared identifier's own credential pair, per the wave 2
// shared contract section 5: "Duplicate identifiers, an empty identifier,
// and a malformed URL are each a startup error." An empty/unset raw string
// returns (nil, nil): no brokers declared, not an error — mirroring
// parseFPPEndpoints's identical convention for the analogous case.
func parseIntegrationBrokers(raw string, lookup func(string) (string, bool)) ([]IntegrationBroker, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	entries := strings.Split(raw, ",")
	brokers := make([]IntegrationBroker, 0, len(entries))
	seen := make(map[string]bool, len(entries))

	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("%s: contains an empty entry (check for a stray comma)", envIntegrationBrokers)
		}

		id, rawURL, ok := strings.Cut(entry, "=")
		id = strings.TrimSpace(id)
		rawURL = strings.TrimSpace(rawURL)
		if !ok || id == "" || rawURL == "" {
			return nil, fmt.Errorf("%s: entry %q must have the form id=url", envIntegrationBrokers, entry)
		}

		if err := mqttproto.ValidateNodeID(id); err != nil {
			return nil, fmt.Errorf("%s: identifier %q: %w", envIntegrationBrokers, id, err)
		}
		if seen[id] {
			return nil, fmt.Errorf("%s: duplicate identifier %q", envIntegrationBrokers, id)
		}
		seen[id] = true

		if brokerURLHasUserinfo(rawURL) {
			return nil, fmt.Errorf("%s: identifier %q: url must not embed credentials; set %s and %s instead",
				envIntegrationBrokers, id, integrationBrokerUsernameEnv(id), integrationBrokerPasswordEnv(id))
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("%s: identifier %q: url %q is not valid: %w", envIntegrationBrokers, id, rawURL, err)
		}
		if !validBrokerSchemes[parsed.Scheme] {
			return nil, fmt.Errorf("%s: identifier %q: url %q must use one of the schemes %s",
				envIntegrationBrokers, id, rawURL, strings.Join(validBrokerSchemesList, ", "))
		}
		if parsed.Host == "" {
			return nil, fmt.Errorf("%s: identifier %q: url %q must include a host", envIntegrationBrokers, id, rawURL)
		}

		username := getEnvDefault(lookup, integrationBrokerUsernameEnv(id), "")
		password := getEnvDefault(lookup, integrationBrokerPasswordEnv(id), "")

		brokers = append(brokers, IntegrationBroker{ID: id, URL: rawURL, Username: username, Password: password})
	}

	return brokers, nil
}

// integrationBrokerIDs renders brokers as just their ids, for logging —
// mirrors fppEndpointIDs's identical reasoning: the URLs are not secret
// (already rejected for userinfo above) but a struct dump is less useful
// for debugging than the id list, and this stays stable if
// IntegrationBroker ever grows a sensitive field.
func integrationBrokerIDs(brokers []IntegrationBroker) []string {
	ids := make([]string, len(brokers))
	for i, b := range brokers {
		ids[i] = b.ID
	}
	return ids
}

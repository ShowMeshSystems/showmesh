package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const (
	// IntegrationBrokerConfigKind is the stored kind behind /api/v1/config/integration.broker/{id}.
	IntegrationBrokerConfigKind = "integration.broker"
	// IntegrationBrokerPasswordCredentialField is the credentials-table field holding a broker's password.
	IntegrationBrokerPasswordCredentialField = "password"

	IntegrationBrokerSourceAPI       = "api"
	IntegrationBrokerSourceMigration = "migration"

	// BuiltInIntegrationBrokerID names the built-in broker in the broker list.
	// It is never stored, and no stored broker may use it.
	BuiltInIntegrationBrokerID = "built-in"
	// BuiltInIntegrationBrokerUsername is the built-in broker login integration publishes use.
	BuiltInIntegrationBrokerUsername = "integration"
	// BuiltInIntegrationTopicPrefix is the only topic tree the integration login may use.
	BuiltInIntegrationTopicPrefix = "showmesh-actions/"
)

// IntegrationBrokerPayload is the stored payload. The password lives in the
// credentials table; PasswordSet only records that one was saved.
type IntegrationBrokerPayload struct {
	URL         string `json:"url"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"passwordSet"`
}

// EncodeIntegrationBrokerPayload marshals p into the stored payload shape.
func EncodeIntegrationBrokerPayload(p IntegrationBrokerPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode integration.broker payload: %w", err)
	}
	return string(b), nil
}

// DecodeIntegrationBrokerPayload is [EncodeIntegrationBrokerPayload]'s inverse.
func DecodeIntegrationBrokerPayload(raw string) (IntegrationBrokerPayload, error) {
	var p IntegrationBrokerPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return IntegrationBrokerPayload{}, fmt.Errorf("config: decode integration.broker payload: %w", err)
	}
	return p, nil
}

// IntegrationBrokerIDProblem is the operator's one line for a broker id that cannot be saved, or "".
func IntegrationBrokerIDProblem(id string) string {
	if id == BuiltInIntegrationBrokerID {
		return "The built-in broker is always listed and cannot be changed here. Pick another id."
	}
	if ValidateShowObjectID("broker id", id) != nil {
		return "The broker id must be 1 to 64 lowercase letters, digits or hyphens, and cannot start or end with a hyphen."
	}
	return ""
}

// IntegrationBrokerURLProblem is the operator's one line for a broker address that cannot be saved, or "".
func IntegrationBrokerURLProblem(raw string) string {
	if raw == "" {
		return "A broker needs an address, such as tcp://10.0.0.5:1883. Add one and save again."
	}
	if brokerURLHasUserinfo(raw) {
		return "The broker address cannot include a login. Put the username and password in their own fields."
	}
	u, err := url.Parse(raw)
	if err != nil || !validBrokerSchemes[u.Scheme] || u.Host == "" {
		return fmt.Sprintf("The broker address must look like tcp://host:1883 and start with %s.", strings.Join(validBrokerSchemesList, ", "))
	}
	return ""
}

// IntegrationTopicProblem is the operator's one line for a topic broker b cannot use, or "".
func IntegrationTopicProblem(b IntegrationBroker, topic string) string {
	if b.TopicPrefix == "" || (strings.HasPrefix(topic, b.TopicPrefix) && len(topic) > len(b.TopicPrefix)) {
		return ""
	}
	return fmt.Sprintf("Topic %s is outside what this broker allows. Start the topic with %s and save again.", topic, b.TopicPrefix)
}

package v1

// IntegrationBroker is one MQTT broker an mqtt show.action may name. The
// password is never returned; PasswordSet reports whether one is saved.
type IntegrationBroker struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"passwordSet"`
	// BuiltIn marks the built-in broker, which is always listed and cannot be edited.
	BuiltIn bool `json:"builtIn"`
	// TopicPrefix, when not empty, is the only topic tree actions on this broker may use.
	TopicPrefix            string  `json:"topicPrefix"`
	Revision               int64   `json:"revision"`
	UpdatedAt              *string `json:"updatedAt"`
	CreatedByPrincipalName *string `json:"createdByPrincipalName"`
	Source                 string  `json:"source"`
}

// IntegrationBrokersListResponse is GET /api/v1/config/integration.broker.
type IntegrationBrokersListResponse struct {
	ServerTime string              `json:"serverTime"`
	Brokers    []IntegrationBroker `json:"brokers"`
	// EditsBlocked is empty when brokers can be edited, and otherwise says why not.
	EditsBlocked string `json:"editsBlocked"`
}

// IntegrationBrokerResponse is GET or PUT /api/v1/config/integration.broker/{id}.
type IntegrationBrokerResponse struct {
	ServerTime string            `json:"serverTime"`
	Broker     IntegrationBroker `json:"broker"`
}

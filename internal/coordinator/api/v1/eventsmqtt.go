package v1

// ConfigEventsMQTTPayload is the events.mqtt payload: whether coordinator
// events are published, to which integration broker, under which topic
// prefix. On a PUT any field may be left out to keep its stored value.
type ConfigEventsMQTTPayload struct {
	Enabled     bool   `json:"enabled"`
	Broker      string `json:"broker"`
	TopicPrefix string `json:"topicPrefix"`
}

// EventsMQTTConfigResponse is the body of GET and PUT /config/events.mqtt.
// Revision 0 means nothing is stored and the payload is the default.
type EventsMQTTConfigResponse struct {
	ServerTime             string                  `json:"serverTime"`
	Kind                   string                  `json:"kind"`
	Revision               int64                   `json:"revision"`
	Payload                ConfigEventsMQTTPayload `json:"payload"`
	UpdatedAt              string                  `json:"updatedAt"`
	CreatedByPrincipalID   *string                 `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                 `json:"createdByPrincipalName"`
	Source                 string                  `json:"source"`
}

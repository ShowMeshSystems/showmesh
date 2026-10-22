package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// EventsMQTTConfigKind is the stored kind behind GET and PUT /api/v1/config/events.mqtt.
	EventsMQTTConfigKind = "events.mqtt"
	// EventsMQTTConfigObjectID is the one object id this kind uses.
	EventsMQTTConfigObjectID = "default"

	// EventsMQTTSourceAPI labels a revision written through the API.
	EventsMQTTSourceAPI = "api"
	// DefaultEventsTopicPrefix is the topic prefix used until an operator sets one.
	DefaultEventsTopicPrefix = "showmesh"

	maxEventsTopicPrefixBytes = 128
)

// EventsMQTT is whether coordinator events are published, and to which integration broker and topic prefix.
type EventsMQTT struct {
	Enabled     bool   `json:"enabled"`
	Broker      string `json:"broker"`
	TopicPrefix string `json:"topicPrefix"`
}

// DefaultEventsMQTT is what the coordinator uses before anything is stored: off.
func DefaultEventsMQTT() EventsMQTT {
	return EventsMQTT{TopicPrefix: DefaultEventsTopicPrefix}
}

// EncodeEventsMQTTPayload marshals s into the stored payload shape.
func EncodeEventsMQTTPayload(s EventsMQTT) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("config: encode events.mqtt payload: %w", err)
	}
	return string(b), nil
}

// DecodeEventsMQTTPayload is [EncodeEventsMQTTPayload]'s inverse; a missing prefix reads as the default.
func DecodeEventsMQTTPayload(raw string) (EventsMQTT, error) {
	var s EventsMQTT
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return EventsMQTT{}, fmt.Errorf("config: decode events.mqtt payload: %w", err)
	}
	if s.TopicPrefix == "" {
		s.TopicPrefix = DefaultEventsTopicPrefix
	}
	return s, nil
}

// EventsMQTTProblem is the operator's one line for settings that cannot be saved, or "" when they can.
func EventsMQTTProblem(s EventsMQTT, brokers []IntegrationBroker) string {
	if p := eventsTopicPrefixProblem(s.TopicPrefix); p != "" {
		return p
	}
	if s.Broker == "" {
		if s.Enabled {
			return "Pick a broker before turning this on."
		}
		return ""
	}
	for _, b := range brokers {
		if b.ID != s.Broker {
			continue
		}
		// Both topics sit under prefix/, so checking that one path covers them.
		if IntegrationTopicProblem(b, s.TopicPrefix+"/") != "" {
			return fmt.Sprintf("The topic prefix %s is outside what the %s broker allows. Start it with %s, such as %sevents, and save again.", s.TopicPrefix, b.ID, b.TopicPrefix, b.TopicPrefix)
		}
		return ""
	}
	return fmt.Sprintf("The broker %s is not set up on this coordinator. Pick one from the MQTT brokers list in Settings.", s.Broker)
}

func eventsTopicPrefixProblem(prefix string) string {
	switch {
	case prefix == "":
		return "The topic prefix cannot be empty."
	case len(prefix) > maxEventsTopicPrefixBytes:
		return fmt.Sprintf("The topic prefix is too long. Keep it under %d characters.", maxEventsTopicPrefixBytes)
	case strings.ContainsAny(prefix, "+#\x00"):
		return "The topic prefix cannot contain + or #. Use plain words separated by /."
	case strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") || strings.Contains(prefix, "//"):
		return "The topic prefix cannot start or end with /, or have an empty part. Use plain words separated by /."
	}
	return ""
}

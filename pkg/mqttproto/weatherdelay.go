package mqttproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The weather delay state is retained on one installation-wide events
// topic, like show mode. The per-power-group dark heartbeat is not retained:
// a power controller acts on its silence, so a stale message must not linger.

// SchemaWeatherDelayV1 is the schema string of [WeatherDelayMessage],
// published retained on [WeatherDelayTopic].
const SchemaWeatherDelayV1 = "showmesh.weatherdelay/v1"

// SchemaWeatherDelayDarkV1 is the schema string of
// [WeatherDelayDarkMessage], published non-retained on
// [WeatherDelayDarkTopic].
const SchemaWeatherDelayDarkV1 = "showmesh.weatherdelay.dark/v1"

// The wire spelling of weatherdelay.KindDelay and KindCancelNight, copied
// so this package imports no vocabulary package. A test keeps them equal.
const (
	WeatherDelayKindDelay       = "delay"
	WeatherDelayKindCancelNight = "cancelNight"
)

var weatherDelayKinds = map[string]bool{
	WeatherDelayKindDelay:       true,
	WeatherDelayKindCancelNight: true,
}

// weatherDelayTopicSubpath and weatherDelayDarkTopicSubpath are the events
// subpaths the state and per-group dark heartbeat are published on.
const (
	weatherDelayTopicSubpath     = "weather_delay"
	weatherDelayDarkTopicSubpath = "weather_delay/dark"
)

// WeatherDelayDeliveryPolicy is retained QoS 1, like [ShowModeDeliveryPolicy].
var WeatherDelayDeliveryPolicy = DeliveryPolicy{Retain: true, QoS: 1}

// WeatherDelayDarkDeliveryPolicy is QoS 1 and not retained.
var WeatherDelayDarkDeliveryPolicy = DeliveryPolicy{Retain: false, QoS: 1}

// WeatherDelayTopic is "showmesh/events/weather_delay", the one retained
// topic for the installation-wide state.
func WeatherDelayTopic() string {
	return eventsPrefix + "/" + weatherDelayTopicSubpath
}

// WeatherDelayDarkTopic builds "showmesh/events/weather_delay/dark/<group-id>",
// one power group's dark heartbeat topic (ADR-053 decision 10).
func WeatherDelayDarkTopic(groupID string) (string, error) {
	if err := validateWeatherDelayGroupID(groupID); err != nil {
		return "", err
	}
	return eventsPrefix + "/" + weatherDelayDarkTopicSubpath + "/" + groupID, nil
}

// validateWeatherDelayGroupID accepts exactly one topic segment, so a group
// id can never add a topic level or a wildcard.
func validateWeatherDelayGroupID(groupID string) error {
	if !subpathSegmentPattern.MatchString(groupID) {
		return fmt.Errorf("%w: group id %q must match [a-z0-9][a-z0-9_-]*", ErrInvalidSubpath, groupID)
	}
	return nil
}

// WeatherDelayAlertAssetRef names one alert asset a node plays. All three
// fields are required, so a node can check the bytes it holds and open them.
type WeatherDelayAlertAssetRef struct {
	AssetID     string `json:"assetId"`
	ContentHash string `json:"contentHash"`
	Filename    string `json:"filename"`
}

// ErrInvalidWeatherDelayAlertAssetRef is wrapped by every error
// [WeatherDelayAlertAssetRef.Validate] returns.
var ErrInvalidWeatherDelayAlertAssetRef = errors.New("mqttproto: invalid weather delay alert asset ref")

// Validate reports whether r's three fields are all present and whether
// Filename is a plain file name. A node joins Filename to its own asset
// directory, so a name carrying a path separator or a parent reference
// would name a file outside it.
func (r WeatherDelayAlertAssetRef) Validate() error {
	switch {
	case r.AssetID == "":
		return fmt.Errorf("%w: assetId is empty", ErrInvalidWeatherDelayAlertAssetRef)
	case r.ContentHash == "":
		return fmt.Errorf("%w: contentHash is empty", ErrInvalidWeatherDelayAlertAssetRef)
	case r.Filename == "":
		return fmt.Errorf("%w: filename is empty", ErrInvalidWeatherDelayAlertAssetRef)
	case r.Filename != filepath.Base(r.Filename) || r.Filename == "." || r.Filename == ".." ||
		strings.ContainsAny(r.Filename, `/\`):
		return fmt.Errorf("%w: filename %q must be a plain file name", ErrInvalidWeatherDelayAlertAssetRef, r.Filename)
	}
	return nil
}

// The alert repeat bounds, copied from internal/coordinator/config because
// nodes cannot import coordinator packages.
const (
	weatherDelayPlanMinRepeatCount = 1
	weatherDelayPlanMaxRepeatCount = 50
)

// WeatherDelayPlan is the alert a node plays, sent on every state message so
// a node that later gets only a signed start already knows what to play.
// A nil asset means none is configured; RepeatCount 0 means unset.
type WeatherDelayPlan struct {
	Delay       *WeatherDelayAlertAssetRef `json:"delay,omitempty"`
	CancelNight *WeatherDelayAlertAssetRef `json:"cancelNight,omitempty"`
	RepeatCount int                        `json:"repeatCount"`
	NodeIDs     []string                   `json:"nodeIds,omitempty"`
}

// ErrInvalidWeatherDelayPlan is wrapped by every error
// [WeatherDelayPlan.Validate] returns.
var ErrInvalidWeatherDelayPlan = errors.New("mqttproto: invalid weather delay plan")

// Validate accepts the zero plan. A present asset ref must be complete and a
// non-zero RepeatCount must be in [1, 50].
func (p WeatherDelayPlan) Validate() error {
	if p.Delay != nil {
		if err := p.Delay.Validate(); err != nil {
			return fmt.Errorf("%w: delay: %v", ErrInvalidWeatherDelayPlan, err)
		}
	}
	if p.CancelNight != nil {
		if err := p.CancelNight.Validate(); err != nil {
			return fmt.Errorf("%w: cancelNight: %v", ErrInvalidWeatherDelayPlan, err)
		}
	}
	if p.RepeatCount != 0 && (p.RepeatCount < weatherDelayPlanMinRepeatCount || p.RepeatCount > weatherDelayPlanMaxRepeatCount) {
		return fmt.Errorf("%w: repeatCount %d must be between %d and %d", ErrInvalidWeatherDelayPlan, p.RepeatCount, weatherDelayPlanMinRepeatCount, weatherDelayPlanMaxRepeatCount)
	}
	return nil
}

// WeatherDelayMessage is the showmesh.weatherdelay/v1 payload: the wire form
// of weatherdelay.State plus the alert plan.
type WeatherDelayMessage struct {
	Schema    string    `json:"schema"`
	MessageID string    `json:"messageId"`
	Active    bool      `json:"active"`
	Kind      string    `json:"kind,omitempty"`
	StartedAt time.Time `json:"startedAt,omitzero"`
	StartedBy string    `json:"startedBy,omitempty"`

	Revision    int64            `json:"revision"`
	Plan        WeatherDelayPlan `json:"plan"`
	PublishedAt time.Time        `json:"publishedAt"`
}

// ErrInvalidWeatherDelayMessage is wrapped by every error
// [WeatherDelayMessage.Validate] and [DecodeWeatherDelayMessage] return.
var ErrInvalidWeatherDelayMessage = errors.New("mqttproto: invalid weather delay message")

// Validate checks the schema, id, revision, publishedAt and plan, and that
// kind, startedAt and startedBy are set while Active and unset otherwise.
func (m WeatherDelayMessage) Validate() error {
	switch {
	case m.Schema != SchemaWeatherDelayV1:
		return fmt.Errorf("%w: schema %q, want %q", ErrInvalidWeatherDelayMessage, m.Schema, SchemaWeatherDelayV1)
	case m.MessageID == "":
		return fmt.Errorf("%w: messageId is empty", ErrInvalidWeatherDelayMessage)
	case m.PublishedAt.IsZero():
		return fmt.Errorf("%w: publishedAt is zero", ErrInvalidWeatherDelayMessage)
	case m.Revision < 0:
		return fmt.Errorf("%w: revision %d is negative", ErrInvalidWeatherDelayMessage, m.Revision)
	}
	if err := m.Plan.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWeatherDelayMessage, err)
	}
	if m.Active {
		if !weatherDelayKinds[m.Kind] {
			return fmt.Errorf("%w: kind %q must be one of %q or %q", ErrInvalidWeatherDelayMessage, m.Kind, WeatherDelayKindDelay, WeatherDelayKindCancelNight)
		}
		if m.StartedAt.IsZero() {
			return fmt.Errorf("%w: startedAt is zero while active", ErrInvalidWeatherDelayMessage)
		}
		if m.StartedBy == "" {
			return fmt.Errorf("%w: startedBy is empty while active", ErrInvalidWeatherDelayMessage)
		}
		return nil
	}
	if m.Kind != "" || !m.StartedAt.IsZero() || m.StartedBy != "" {
		return fmt.Errorf("%w: kind, startedAt, and startedBy must all be unset while not active", ErrInvalidWeatherDelayMessage)
	}
	return nil
}

// NewWeatherDelayMessage builds a validated [WeatherDelayMessage] with a
// fresh message id and now stamped in UTC.
func NewWeatherDelayMessage(active bool, kind string, startedAt time.Time, startedBy string, revision int64, plan WeatherDelayPlan, now time.Time) (WeatherDelayMessage, error) {
	m := WeatherDelayMessage{
		Schema: SchemaWeatherDelayV1, MessageID: uuid.NewString(),
		Active: active, Kind: kind, StartedAt: startedAt, StartedBy: startedBy,
		Revision: revision, Plan: plan, PublishedAt: now.UTC(),
	}
	if !startedAt.IsZero() {
		m.StartedAt = startedAt.UTC()
	}
	if err := m.Validate(); err != nil {
		return WeatherDelayMessage{}, err
	}
	return m, nil
}

// EncodeWeatherDelayMessage marshals m after validating it.
func EncodeWeatherDelayMessage(m WeatherDelayMessage) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("mqttproto: encode weather delay message: %w", err)
	}
	return b, nil
}

// DecodeWeatherDelayMessage parses and validates data. An empty payload is
// refused rather than read as any state.
func DecodeWeatherDelayMessage(data []byte) (WeatherDelayMessage, error) {
	if len(data) > maxEnvelopeSize {
		return WeatherDelayMessage{}, fmt.Errorf("%w: %w", ErrInvalidWeatherDelayMessage, ErrEnvelopeTooLarge)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return WeatherDelayMessage{}, fmt.Errorf("%w: empty payload", ErrInvalidWeatherDelayMessage)
	}
	var m WeatherDelayMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return WeatherDelayMessage{}, fmt.Errorf("%w: %v", ErrInvalidWeatherDelayMessage, err)
	}
	if err := m.Validate(); err != nil {
		return WeatherDelayMessage{}, err
	}
	return m, nil
}

// WeatherDelayDarkMessage is the showmesh.weatherdelay.dark/v1 payload: one
// power group's dark confirmation, published only while a delay is active.
type WeatherDelayDarkMessage struct {
	Schema      string    `json:"schema"`
	MessageID   string    `json:"messageId"`
	GroupID     string    `json:"groupId"`
	Dark        bool      `json:"dark"`
	PublishedAt time.Time `json:"publishedAt"`
}

// ErrInvalidWeatherDelayDarkMessage is wrapped by every error
// [WeatherDelayDarkMessage.Validate] and [DecodeWeatherDelayDarkMessage]
// return.
var ErrInvalidWeatherDelayDarkMessage = errors.New("mqttproto: invalid weather delay dark message")

// Validate checks the schema, id, publishedAt, and that the group id is one
// topic segment.
func (m WeatherDelayDarkMessage) Validate() error {
	switch {
	case m.Schema != SchemaWeatherDelayDarkV1:
		return fmt.Errorf("%w: schema %q, want %q", ErrInvalidWeatherDelayDarkMessage, m.Schema, SchemaWeatherDelayDarkV1)
	case m.MessageID == "":
		return fmt.Errorf("%w: messageId is empty", ErrInvalidWeatherDelayDarkMessage)
	case m.PublishedAt.IsZero():
		return fmt.Errorf("%w: publishedAt is zero", ErrInvalidWeatherDelayDarkMessage)
	}
	if err := validateWeatherDelayGroupID(m.GroupID); err != nil {
		return fmt.Errorf("%w: group id: %v", ErrInvalidWeatherDelayDarkMessage, err)
	}
	return nil
}

// NewWeatherDelayDarkMessage builds a validated [WeatherDelayDarkMessage]
// with a fresh message id and now stamped in UTC.
func NewWeatherDelayDarkMessage(groupID string, dark bool, now time.Time) (WeatherDelayDarkMessage, error) {
	m := WeatherDelayDarkMessage{Schema: SchemaWeatherDelayDarkV1, MessageID: uuid.NewString(), GroupID: groupID, Dark: dark, PublishedAt: now.UTC()}
	if err := m.Validate(); err != nil {
		return WeatherDelayDarkMessage{}, err
	}
	return m, nil
}

// EncodeWeatherDelayDarkMessage marshals m after validating it.
func EncodeWeatherDelayDarkMessage(m WeatherDelayDarkMessage) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("mqttproto: encode weather delay dark message: %w", err)
	}
	return b, nil
}

// DecodeWeatherDelayDarkMessage parses data and validates it.
func DecodeWeatherDelayDarkMessage(data []byte) (WeatherDelayDarkMessage, error) {
	if len(data) > maxEnvelopeSize {
		return WeatherDelayDarkMessage{}, fmt.Errorf("%w: %w", ErrInvalidWeatherDelayDarkMessage, ErrEnvelopeTooLarge)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return WeatherDelayDarkMessage{}, fmt.Errorf("%w: empty payload", ErrInvalidWeatherDelayDarkMessage)
	}
	var m WeatherDelayDarkMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return WeatherDelayDarkMessage{}, fmt.Errorf("%w: %v", ErrInvalidWeatherDelayDarkMessage, err)
	}
	if err := m.Validate(); err != nil {
		return WeatherDelayDarkMessage{}, err
	}
	return m, nil
}

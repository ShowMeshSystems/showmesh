package mqttproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// This file carries ADR-053's weather-delay state to nodes, on
// showmode.go's own precedent: retained, events-family, not wrapped in an
// [Envelope] (the state is installation-wide, not node-scoped — see
// showmode.go's header comment for the full reasoning, which applies here
// unchanged).
//
// WeatherDelayDarkTopic is the one deliberate departure: it is PER POWER
// GROUP (ADR-053 decision 10) and NOT retained. A late-joining subscriber
// (an installation's own power controller) must not act on a stale "dark"
// heartbeat left over from a previous delay — decision 10's own words are
// "a controller that stops hearing a group's heartbeat during a delay cuts
// that group", which only works if silence, not a retained message,
// carries the meaning. Retaining it would mean a controller that starts
// after a delay ended still sees the last "dark" it was ever told and
// never cuts power for the wrong reason, but also never clears the
// stale message on its own the way an [EventDeliveryPolicy] topic
// otherwise would.

// SchemaWeatherDelayV1 is the schema string of [WeatherDelayMessage],
// published retained on [WeatherDelayTopic].
const SchemaWeatherDelayV1 = "showmesh.weatherdelay/v1"

// SchemaWeatherDelayDarkV1 is the schema string of
// [WeatherDelayDarkMessage], published non-retained on
// [WeatherDelayDarkTopic].
const SchemaWeatherDelayDarkV1 = "showmesh.weatherdelay.dark/v1"

// The two members of ADR-053 decision 1's closed vocabulary, the wire
// spelling of [weatherdelay.KindDelay]/[weatherdelay.KindCancelNight].
// This package does not import pkg/weatherdelay (it carries no
// dependency on any other package's own vocabulary type, mirroring
// showmode.go's own closed, self-contained enum), so the two are
// duplicated as literals here; TestWeatherDelayKindsMatchPkgWeatherdelay
// is what keeps them from drifting apart.
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

// WeatherDelayDeliveryPolicy is retained QoS 1: the state is state, per
// [ShowModeDeliveryPolicy]'s own identical reasoning.
var WeatherDelayDeliveryPolicy = DeliveryPolicy{Retain: true, QoS: 1}

// WeatherDelayDarkDeliveryPolicy is QoS 1, NOT retained: see this file's
// header comment for why a stale "dark" heartbeat must never survive past
// the delay that produced it.
var WeatherDelayDarkDeliveryPolicy = DeliveryPolicy{Retain: false, QoS: 1}

// WeatherDelayTopic is "showmesh/events/weather_delay", the single
// retained topic the installation-wide weather-delay state is published
// on. Takes no arguments, mirroring [ShowModeTopic]: there is exactly one
// weather-delay state for the installation (ADR-053 decision 2).
func WeatherDelayTopic() string {
	return eventsPrefix + "/" + weatherDelayTopicSubpath
}

// WeatherDelayDarkTopic builds
// "showmesh/events/weather_delay/dark/<group-id>", the per-power-group
// dark-confirmation heartbeat (ADR-053 decision 10). groupID is the
// operator-configured power group id, validated with the same
// [subpathSegmentPattern] every other topic segment in this package uses.
func WeatherDelayDarkTopic(groupID string) (string, error) {
	if err := validateWeatherDelayGroupID(groupID); err != nil {
		return "", err
	}
	return eventsPrefix + "/" + weatherDelayDarkTopicSubpath + "/" + groupID, nil
}

// validateWeatherDelayGroupID enforces [subpathSegmentPattern] on groupID
// as ONE segment, deliberately stricter than [validateSubpath] (which
// permits internal '/' to express hierarchy): a group id placed directly
// into a topic path must never itself contain a level separator, or an
// operator-chosen id could inject an extra topic level.
func validateWeatherDelayGroupID(groupID string) error {
	if !subpathSegmentPattern.MatchString(groupID) {
		return fmt.Errorf("%w: group id %q must match [a-z0-9][a-z0-9_-]*", ErrInvalidSubpath, groupID)
	}
	return nil
}

// WeatherDelayAlertAssetRef names one alert asset a node opens: the
// coordinator's asset id, its content hash (so a node can confirm it holds
// the right bytes without a separate fetch round trip), and the runtime
// filename to open. All three are required whenever a ref is present — an
// asset a node cannot verify or cannot open is not a usable reference.
type WeatherDelayAlertAssetRef struct {
	AssetID     string `json:"assetId"`
	ContentHash string `json:"contentHash"`
	Filename    string `json:"filename"`
}

// ErrInvalidWeatherDelayAlertAssetRef is wrapped by every error
// [WeatherDelayAlertAssetRef.Validate] returns.
var ErrInvalidWeatherDelayAlertAssetRef = errors.New("mqttproto: invalid weather delay alert asset ref")

// Validate reports whether r's three fields are all present.
func (r WeatherDelayAlertAssetRef) Validate() error {
	switch {
	case r.AssetID == "":
		return fmt.Errorf("%w: assetId is empty", ErrInvalidWeatherDelayAlertAssetRef)
	case r.ContentHash == "":
		return fmt.Errorf("%w: contentHash is empty", ErrInvalidWeatherDelayAlertAssetRef)
	case r.Filename == "":
		return fmt.Errorf("%w: filename is empty", ErrInvalidWeatherDelayAlertAssetRef)
	}
	return nil
}

// weatherDelayPlanMinRepeatCount and weatherDelayPlanMaxRepeatCount mirror
// internal/coordinator/config's own weatherDelayMinRepeatCount/
// weatherDelayMaxRepeatCount (ADR-053 decision 7): this package cannot
// import that one (agent/plugin-facing, config is coordinator-only), so
// the bound is duplicated as a literal here, the same
// [WeatherDelayKindDelay]/[WeatherDelayKindCancelNight] duplication this
// file's own doc comment already explains.
const (
	weatherDelayPlanMinRepeatCount = 1
	weatherDelayPlanMaxRepeatCount = 50
)

// WeatherDelayPlan is what a node needs to play the configured alert
// without a further round trip once it receives a bare signed start
// (ADR-053 decision 9): which asset for each kind, how many times, and
// which nodes. Present on every [WeatherDelayMessage], even one reporting
// "not active" — a node that later receives a signed start already knows
// what to play. Delay/CancelNight are nil when that kind's alert asset is
// not configured (ADR-053 decision 13: every part is independently
// optional); RepeatCount 0 means nothing has ever configured a value (the
// coordinator's own resolved default is 10, never republished as this
// zero value once show.weatherdelay has actually been written).
type WeatherDelayPlan struct {
	Delay       *WeatherDelayAlertAssetRef `json:"delay,omitempty"`
	CancelNight *WeatherDelayAlertAssetRef `json:"cancelNight,omitempty"`
	RepeatCount int                        `json:"repeatCount"`
	NodeIDs     []string                   `json:"nodeIds,omitempty"`
}

// ErrInvalidWeatherDelayPlan is wrapped by every error
// [WeatherDelayPlan.Validate] returns.
var ErrInvalidWeatherDelayPlan = errors.New("mqttproto: invalid weather delay plan")

// Validate reports whether p is well-formed: an absent plan (the zero
// value) and absent Delay/CancelNight entries are both valid — this is
// deliberately permissive, not "required once a plan exists", because a
// coordinator that has never configured show.weatherdelay still publishes
// this message with its own zero Plan. A present Delay/CancelNight ref
// must itself be complete ([WeatherDelayAlertAssetRef.Validate]), and a
// non-zero RepeatCount must fall in [1, 50] (ADR-053 decision 7) — only a
// RepeatCount that was actually SET to something out of range is refused.
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

// WeatherDelayMessage is the payload of the showmesh.weatherdelay/v1
// schema: the wire projection of [weatherdelay.State], carried directly
// rather than importing that type, mirroring [ShowModeMessage]'s own
// self-contained shape.
type WeatherDelayMessage struct {
	Schema    string    `json:"schema"`
	MessageID string    `json:"messageId"`
	Active    bool      `json:"active"`
	Kind      string    `json:"kind,omitempty"`
	StartedAt time.Time `json:"startedAt,omitempty"`
	StartedBy string    `json:"startedBy,omitempty"`

	// Revision is the coordinator's weather-delay state revision this
	// value came from, mirroring [ShowModeMessage.Revision]'s own
	// informational, non-gating role.
	Revision int64 `json:"revision"`

	// Plan is the alert plan a node needs to play without a further round
	// trip once it receives a bare signed start. See [WeatherDelayPlan].
	Plan WeatherDelayPlan `json:"plan"`

	// PublishedAt is the coordinator's clock at build time, mirroring
	// [ShowModeMessage.PublishedAt]'s identical "not freshness evidence on
	// the receiving side" rule.
	PublishedAt time.Time `json:"publishedAt"`
}

// ErrInvalidWeatherDelayMessage is wrapped by every error
// [WeatherDelayMessage.Validate] and [DecodeWeatherDelayMessage] return.
var ErrInvalidWeatherDelayMessage = errors.New("mqttproto: invalid weather delay message")

// Validate reports whether m is well-formed: the expected schema, a
// non-empty message id, a non-negative revision, a non-zero publishedAt,
// and — while Active — a kind from the closed vocabulary, a non-zero
// startedAt, and a non-empty startedBy; while not Active, none of those
// three, mirroring [weatherdelay.State.Validate]'s identical rule.
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
// fresh message id and now stamped in UTC. plan is carried through
// verbatim — see [WeatherDelayPlan]'s own doc comment for why it is
// present even when active is false.
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

// EncodeWeatherDelayMessage marshals m after validating it, mirroring
// [EncodeShowModeMessage]'s identical "never let a malformed message reach
// the retained topic" rule.
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

// DecodeWeatherDelayMessage parses data and validates it, mirroring
// [DecodeShowModeMessage]'s identical shape, including its "an empty
// payload is refused rather than read as any state" rule.
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

// WeatherDelayDarkMessage is the payload of the
// showmesh.weatherdelay.dark/v1 schema: one power group's own current dark
// confirmation (ADR-053 decision 10). Published only while a delay is
// active; a group that has never confirmed dark since the delay started
// simply has no message published for it yet, never a "false" placeholder.
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

// Validate reports whether m is well-formed: the expected schema, a
// non-empty message id, a group id valid as a topic segment, and a
// non-zero publishedAt.
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

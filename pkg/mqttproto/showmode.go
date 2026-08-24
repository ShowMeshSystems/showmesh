package mqttproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// This file carries ADR-033's installation-wide operating mode to nodes.
//
// WHY THE EVENTS FAMILY, RETAINED. ADR-008 fixes six topic families and
// says "retained QoS 1 for state". The mode is state, it is published by
// the coordinator, and it is not node-scoped, which leaves exactly one
// family that fits: showmesh/events/... , the coordinator-published one.
// Retained rather than [EventDeliveryPolicy]'s non-retained default, and
// that is not a departure from ADR-008 but an application of its own
// sentence: EventDeliveryPolicy's doc comment argues non-retained for "a
// lifecycle or alert event ... a point-in-time notification, not state",
// which the mode is not. Retention is what lets a node that connects after
// the mode was last set learn it immediately instead of sitting in
// "unknown" until the next republish.
//
// WHY NOT THE CMD TOPIC. The per-node cmd path (internal/coordinator/
// audioconfigpush) is the existing coordinator-to-node configuration push,
// and it is the wrong shape here twice over: it is per-node, so an
// installation-wide value would be N messages that can disagree, and it
// would need a new agent operation name for a value that commands nothing.
//
// WHY NOT render.surface.apply's PARAMETERS. That path bakes a resolved
// configuration value into a dispatched command, which would tear down and
// rebuild frame writers on every mode flip. A value whose entire purpose is
// to be safe during a show must not restart the show's own pipelines when
// it changes.
//
// WHY NOT AN [Envelope]. Every other payload in this package travels inside
// one, and Envelope is node-scoped by construction: NodeID is required, and
// [CheckNodeID] exists to reconcile it against the node id in the topic.
// The mode belongs to the installation, not to a node, so there is no
// honest value for that field. Rather than invent a sentinel node id that
// every receiver would then have to special-case, this message carries its
// own schema and message id and is documented here as the deliberate
// exception.

// SchemaShowModeV1 is the schema string of [ShowModeMessage], published
// retained on [ShowModeTopic].
const SchemaShowModeV1 = "showmesh.showmode/v1"

// The mode values that may appear on the wire. ADR-033's vocabulary is
// closed, and "unknown" is deliberately absent: it is a RECEIVER's word for
// a mode it has never been told, never a value anything publishes.
const (
	ShowModeProgram = "program"
	ShowModeShow    = "show"
)

// showModeTopicSubpath is the events subpath the mode is published on. An
// underscore rather than a dot or a hyphen because [subpathSegmentPattern]
// permits it and the rest of this package's subpaths are single words.
const showModeTopicSubpath = "show_mode"

// ShowModeDeliveryPolicy is retained QoS 1: the mode is state, per ADR-008's
// own "retained QoS 1 for state". See this file's header comment for why
// this differs from [EventDeliveryPolicy].
var ShowModeDeliveryPolicy = DeliveryPolicy{Retain: true, QoS: 1}

// ShowModeTopic is "showmesh/events/show_mode", the single retained topic
// the installation-wide operating mode is published on. It takes no
// arguments because there is exactly one mode for the installation
// (ADR-033 decision 1): a per-node topic here would be a way to express a
// disagreement that must not be expressible.
func ShowModeTopic() string {
	return eventsPrefix + "/" + showModeTopicSubpath
}

// ShowModeMessage is the payload of the showmesh.showmode/v1 schema. See
// this file's header comment for why it is not wrapped in an [Envelope].
type ShowModeMessage struct {
	// Schema is always [SchemaShowModeV1]. Carried in the message itself,
	// rather than in an envelope around it, so a receiver can still refuse
	// a shape it does not understand.
	Schema string `json:"schema"`

	// MessageID is a UUIDv4, freshly generated per message.
	MessageID string `json:"messageId"`

	// Mode is [ShowModeProgram] or [ShowModeShow], never "unknown".
	Mode string `json:"mode"`

	// Revision is the coordinator's show.mode configuration revision this
	// value came from, or 0 when nothing has ever been written and the
	// coordinator is publishing its built-in default. Informational: a
	// receiver must not use it to decide whether to accept the message,
	// because a coordinator republishes the same revision on every tick.
	Revision int64 `json:"revision"`

	// PublishedAt is the COORDINATOR's clock at the moment the message was
	// built, in UTC. Like [Envelope.SentAt], it is not evidence of
	// freshness on the receiving side: the two clocks are not known to
	// agree, so a receiver judges freshness by its own receipt time.
	PublishedAt time.Time `json:"publishedAt"`
}

// ErrInvalidShowModeMessage is wrapped by every error
// [ShowModeMessage.Validate] and [DecodeShowModeMessage] return.
var ErrInvalidShowModeMessage = errors.New("mqttproto: invalid show mode message")

// Validate reports whether m is a well-formed show mode message: the
// expected schema, a non-empty message id, a mode inside ADR-033's closed
// vocabulary, and a non-zero publishedAt.
func (m ShowModeMessage) Validate() error {
	switch {
	case m.Schema != SchemaShowModeV1:
		return fmt.Errorf("%w: schema %q, want %q", ErrInvalidShowModeMessage, m.Schema, SchemaShowModeV1)
	case m.MessageID == "":
		return fmt.Errorf("%w: messageId is empty", ErrInvalidShowModeMessage)
	case m.Mode != ShowModeProgram && m.Mode != ShowModeShow:
		return fmt.Errorf("%w: mode %q must be one of %q or %q",
			ErrInvalidShowModeMessage, m.Mode, ShowModeProgram, ShowModeShow)
	case m.PublishedAt.IsZero():
		return fmt.Errorf("%w: publishedAt is zero", ErrInvalidShowModeMessage)
	case m.Revision < 0:
		return fmt.Errorf("%w: revision %d is negative", ErrInvalidShowModeMessage, m.Revision)
	}
	return nil
}

// NewShowModeMessage builds a validated [ShowModeMessage] with a fresh
// message id and now stamped in UTC.
func NewShowModeMessage(mode string, revision int64, now time.Time) (ShowModeMessage, error) {
	m := ShowModeMessage{
		Schema:      SchemaShowModeV1,
		MessageID:   uuid.NewString(),
		Mode:        mode,
		Revision:    revision,
		PublishedAt: now.UTC(),
	}
	if err := m.Validate(); err != nil {
		return ShowModeMessage{}, err
	}
	return m, nil
}

// EncodeShowModeMessage marshals m after validating it, so a malformed
// message can never reach the retained topic where every future subscriber
// would replay it.
func EncodeShowModeMessage(m ShowModeMessage) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("mqttproto: encode show mode message: %w", err)
	}
	return b, nil
}

// DecodeShowModeMessage parses data and validates it. Unknown JSON fields
// are tolerated, matching [DecodeEnvelope]'s additive-schema posture; an
// empty payload is refused rather than read as any mode, because clearing
// a retained topic is not a way to say "program".
func DecodeShowModeMessage(data []byte) (ShowModeMessage, error) {
	if len(data) > maxEnvelopeSize {
		return ShowModeMessage{}, fmt.Errorf("%w: %w", ErrInvalidShowModeMessage, ErrEnvelopeTooLarge)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return ShowModeMessage{}, fmt.Errorf("%w: empty payload", ErrInvalidShowModeMessage)
	}
	var m ShowModeMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return ShowModeMessage{}, fmt.Errorf("%w: %v", ErrInvalidShowModeMessage, err)
	}
	if err := m.Validate(); err != nil {
		return ShowModeMessage{}, err
	}
	return m, nil
}

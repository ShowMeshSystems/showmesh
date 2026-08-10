package mqttproto

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Envelope is the one versioned JSON envelope every ShowMesh MQTT payload
// travels inside, so the coordinator can parse the envelope, dispatch on
// Schema, and skip whatever it does not understand.
//
// Field naming is camelCase, matching the JSON the coordinator's HTTP
// handlers already emit (buildDate, observedAgeSecs in
// internal/coordinator/server.go): one convention across the project, and
// it lines up with the generated TypeScript types ADR-015 requires.
type Envelope struct {
	// Schema identifies both the payload's shape and its version, e.g.
	// "showmesh.node.hello/v1". See [UnsupportedSchemaError].
	Schema string `json:"schema"`

	// MessageID is a UUIDv4, freshly generated per message. Constructors in
	// this package (e.g. [NewHelloEnvelope]) generate it; nothing else in
	// this package interprets it beyond presence.
	MessageID string `json:"messageId"`

	// NodeID identifies the node this message concerns. It must match the
	// node ID embedded in the topic the message arrived on; see
	// [CheckNodeID].
	NodeID string `json:"nodeId"`

	// SentAt is the SENDER's clock at the moment the message was
	// constructed, in UTC. It is NOT evidence of freshness on the receiving
	// side: the sender's and receiver's clocks are not known to agree. Per
	// ADR-011, the coordinator must stamp its own receipt time as the
	// observation time and use that, not SentAt, to judge staleness.
	//
	// SentAt carries an additional trap for the LWT payload specifically:
	// see [LWTPayload]'s doc comment.
	SentAt time.Time `json:"sentAt"`

	// Payload is the schema-specific body, left as raw JSON so a caller can
	// inspect Schema before deciding how (or whether) to decode it. Use
	// [DecodeHelloPayload], [DecodeHealthPayload], or [DecodeLWTPayload].
	Payload json.RawMessage `json:"payload"`
}

// ErrEnvelopeInvalidJSON is wrapped by [DecodeEnvelope] when data is not
// syntactically valid JSON, or does not unmarshal into the shape Envelope
// expects (e.g. sentAt is not an RFC3339 timestamp).
var ErrEnvelopeInvalidJSON = errors.New("mqttproto: envelope is not valid JSON")

// ErrEnvelopeMissingField is wrapped by [DecodeEnvelope] and
// [Envelope.Validate] when a required envelope field is empty or zero.
var ErrEnvelopeMissingField = errors.New("mqttproto: envelope is missing a required field")

// Validate reports whether e has every field a well-formed envelope
// requires: non-empty schema, messageId, and nodeId (with nodeId
// additionally checked against [ValidateNodeID]), and a non-zero sentAt.
// It does NOT check that Schema is one this package recognizes; that is
// [DecodeHelloPayload]/[DecodeHealthPayload]/[DecodeLWTPayload]'s job, via
// [UnsupportedSchemaError], because "which schemas exist" is a concern
// specific to each payload decoder, not the envelope itself.
func (e Envelope) Validate() error {
	switch {
	case e.Schema == "":
		return fmt.Errorf("%w: schema", ErrEnvelopeMissingField)
	case e.MessageID == "":
		return fmt.Errorf("%w: messageId", ErrEnvelopeMissingField)
	case e.NodeID == "":
		return fmt.Errorf("%w: nodeId", ErrEnvelopeMissingField)
	case e.SentAt.IsZero():
		return fmt.Errorf("%w: sentAt", ErrEnvelopeMissingField)
	}
	if err := ValidateNodeID(e.NodeID); err != nil {
		return fmt.Errorf("mqttproto: envelope nodeId: %w", err)
	}
	return nil
}

// DecodeEnvelope parses data as an [Envelope] and validates its required
// fields (see [Envelope.Validate]).
//
// Unknown JSON fields in data are always tolerated, never rejected:
// DecodeEnvelope does not use a json.Decoder with DisallowUnknownFields,
// because additive changes within a schema version are the entire point of
// versioning it. Required fields are validated explicitly above instead of
// relying on strict decoding to catch their absence.
//
// DecodeEnvelope does not check Schema against the vocabulary this package
// knows; that is deferred to a payload-specific decoder
// ([DecodeHelloPayload] and friends), which returns a typed
// [UnsupportedSchemaError] a caller can log and skip.
func DecodeEnvelope(data []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, fmt.Errorf("%w: %w", ErrEnvelopeInvalidJSON, err)
	}
	if err := env.Validate(); err != nil {
		return Envelope{}, err
	}
	// SentAt's doc comment says UTC, and every constructor in this package
	// stamps .UTC(), but json.Unmarshal of an RFC3339 timestamp preserves
	// whatever offset the sender used. Normalize here so a decoded envelope
	// always satisfies its own doc comment, regardless of what a node sent.
	env.SentAt = env.SentAt.UTC()
	return env, nil
}

// UnsupportedSchemaError is returned by [DecodeHelloPayload],
// [DecodeHealthPayload], and [DecodeLWTPayload] when an envelope's Schema
// is not the one the decoder expects: either a genuinely unrecognized
// schema, or simply the wrong decoder called against a valid envelope of a
// different schema. It deliberately does not wrap
// [ErrEnvelopeInvalidJSON] or [ErrEnvelopeMissingField]: the envelope
// itself parsed and validated cleanly, so this is "a message I do not
// understand", never "a malformed message" or a panic. A caller such as
// the coordinator's dispatch loop should log Got and Want and move on
// rather than treat this as corruption.
type UnsupportedSchemaError struct {
	Got  string
	Want string
}

func (e *UnsupportedSchemaError) Error() string {
	return fmt.Sprintf("mqttproto: unsupported schema %q, want %q", e.Got, e.Want)
}

// ErrNodeIDMismatch is wrapped by [CheckNodeID] when an envelope's NodeID
// disagrees with the node ID embedded in the topic it arrived on.
var ErrNodeIDMismatch = errors.New("mqttproto: envelope nodeId does not match topic node ID")

// CheckNodeID rejects a message whose envelope NodeID disagrees with
// topicNodeID, the node ID parsed from the topic the message arrived on
// (see [ParseTopic]). This exists as an explicit function, rather than
// something left for every caller to remember, because a node that is
// compromised, misconfigured, or simply buggy could otherwise publish a
// hello/health/lwt envelope under one node's identity on another node's
// retained topic.
func CheckNodeID(env Envelope, topicNodeID string) error {
	if env.NodeID != topicNodeID {
		return fmt.Errorf("%w: envelope nodeId %q, topic node ID %q", ErrNodeIDMismatch, env.NodeID, topicNodeID)
	}
	return nil
}

package mqttproto

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestDecodeEnvelopeToleratesUnknownField(t *testing.T) {
	data := []byte(`{
		"schema": "showmesh.node.hello/v1",
		"messageId": "11111111-1111-4111-8111-111111111111",
		"nodeId": "media-03",
		"sentAt": "2026-08-10T12:00:00Z",
		"payload": {},
		"somethingFromTheFuture": {"nested": [1,2,3]}
	}`)

	env, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v, want nil (unknown fields must be tolerated)", err)
	}
	if env.NodeID != "media-03" {
		t.Fatalf("NodeID = %q, want media-03", env.NodeID)
	}
}

func TestDecodeEnvelopeMissingFields(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "missing schema", json: `{"messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z","payload":{}}`},
		{name: "missing messageId", json: `{"schema":"showmesh.node.hello/v1","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z","payload":{}}`},
		{name: "missing nodeId", json: `{"schema":"showmesh.node.hello/v1","messageId":"m","sentAt":"2026-08-10T12:00:00Z","payload":{}}`},
		{name: "missing sentAt", json: `{"schema":"showmesh.node.hello/v1","messageId":"m","nodeId":"media-03","payload":{}}`},
		{name: "invalid nodeId", json: `{"schema":"showmesh.node.hello/v1","messageId":"m","nodeId":"Bad/ID","sentAt":"2026-08-10T12:00:00Z","payload":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeEnvelope([]byte(tt.json))
			if err == nil {
				t.Fatalf("DecodeEnvelope() = nil error, want error")
			}
		})
	}
}

func TestDecodeEnvelopeInvalidJSON(t *testing.T) {
	_, err := DecodeEnvelope([]byte(`{not json`))
	if !errors.Is(err, ErrEnvelopeInvalidJSON) {
		t.Fatalf("DecodeEnvelope() error = %v, want errors.Is(err, ErrEnvelopeInvalidJSON)", err)
	}
}

func TestDecodePayloadUnsupportedSchema(t *testing.T) {
	env := Envelope{
		Schema:    "showmesh.node.health/v1",
		MessageID: "m",
		NodeID:    "media-03",
		SentAt:    time.Unix(0, 0),
		Payload:   json.RawMessage(`{}`),
	}

	_, err := DecodeHelloPayload(env)
	if err == nil {
		t.Fatalf("DecodeHelloPayload() = nil error, want *UnsupportedSchemaError")
	}
	var schemaErr *UnsupportedSchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("DecodeHelloPayload() error = %v (%T), want *UnsupportedSchemaError", err, err)
	}
	if schemaErr.Got != "showmesh.node.health/v1" || schemaErr.Want != SchemaNodeHelloV1 {
		t.Fatalf("UnsupportedSchemaError = %+v", schemaErr)
	}
}

func TestDecodeEnvelopeNormalizesSentAtToUTC(t *testing.T) {
	// -04:00 is 2026-08-10T12:00:00Z, the same instant every other test in
	// this file spells as "...T12:00:00Z". Envelope.SentAt's doc comment
	// promises UTC and every constructor stamps .UTC(), but json.Unmarshal
	// of an RFC3339 timestamp preserves the sender's own offset, so a
	// decoded envelope must be normalized to make that promise true
	// regardless of what a node actually sent.
	data := []byte(`{
		"schema": "showmesh.node.hello/v1",
		"messageId": "11111111-1111-4111-8111-111111111111",
		"nodeId": "media-03",
		"sentAt": "2026-08-10T08:00:00-04:00",
		"payload": {}
	}`)

	env, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	if env.SentAt.Location() != time.UTC {
		t.Fatalf("SentAt.Location() = %v, want time.UTC", env.SentAt.Location())
	}
	want := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if !env.SentAt.Equal(want) {
		t.Fatalf("SentAt = %v, want %v", env.SentAt, want)
	}
}

// TestDecodeHelloPayloadRejectsEmptyOrNullPayload proves the specific bug
// this test guards against: json.Unmarshal([]byte("null"), &p) is a
// documented no-op, so without an explicit check, an envelope with
// "payload":null (or no "payload" key at all) would decode into a fully
// zero-valued HelloPayload and be accepted as a genuine capability
// advertisement, with an empty BootID indistinguishable from a real one.
func TestDecodeHelloPayloadRejectsEmptyOrNullPayload(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			name: "null payload",
			json: `{"schema":"showmesh.node.hello/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z","payload":null}`,
		},
		{
			name: "absent payload key",
			json: `{"schema":"showmesh.node.hello/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := DecodeEnvelope([]byte(tt.json))
			if err != nil {
				t.Fatalf("DecodeEnvelope() error = %v, want nil: the envelope itself is well-formed", err)
			}
			_, err = DecodeHelloPayload(env)
			if !errors.Is(err, ErrPayloadEmpty) {
				t.Fatalf("DecodeHelloPayload() error = %v, want errors.Is(err, ErrPayloadEmpty)", err)
			}
		})
	}
}

// TestDecodeHealthPayloadRejectsEmptyOrNullPayload is
// [TestDecodeHelloPayloadRejectsEmptyOrNullPayload]'s counterpart for
// health: the same null-decodes-to-zero-value trap applies to
// DecodeHealthPayload.
func TestDecodeHealthPayloadRejectsEmptyOrNullPayload(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			name: "null payload",
			json: `{"schema":"showmesh.node.health/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z","payload":null}`,
		},
		{
			name: "absent payload key",
			json: `{"schema":"showmesh.node.health/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := DecodeEnvelope([]byte(tt.json))
			if err != nil {
				t.Fatalf("DecodeEnvelope() error = %v, want nil: the envelope itself is well-formed", err)
			}
			_, err = DecodeHealthPayload(env)
			if !errors.Is(err, ErrPayloadEmpty) {
				t.Fatalf("DecodeHealthPayload() error = %v, want errors.Is(err, ErrPayloadEmpty)", err)
			}
		})
	}
}

// TestDecodeLWTPayloadRejectsEmptyOrNullPayload is the same guard for
// DecodeLWTPayload, which has no Validate method of its own but must still
// reject a null/absent payload rather than silently decode one into a
// zero-valued LWTPayload{Online: false}.
func TestDecodeLWTPayloadRejectsEmptyOrNullPayload(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			name: "null payload",
			json: `{"schema":"showmesh.node.lwt/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z","payload":null}`,
		},
		{
			name: "absent payload key",
			json: `{"schema":"showmesh.node.lwt/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := DecodeEnvelope([]byte(tt.json))
			if err != nil {
				t.Fatalf("DecodeEnvelope() error = %v, want nil: the envelope itself is well-formed", err)
			}
			_, err = DecodeLWTPayload(env)
			if !errors.Is(err, ErrPayloadEmpty) {
				t.Fatalf("DecodeLWTPayload() error = %v, want errors.Is(err, ErrPayloadEmpty)", err)
			}
		})
	}
}

// TestDecodeHelloPayloadValidation exercises [HelloPayload.Validate] via
// DecodeHelloPayload: each required field, dropped one at a time from an
// otherwise-valid payload, must produce an error wrapping
// [ErrPayloadMissingField].
func TestDecodeHelloPayloadValidation(t *testing.T) {
	base := HelloPayload{
		Label:        "Media Node 03",
		Platform:     "linux-amd64",
		AgentVersion: "0.1.0",
		BootID:       "boot-1",
		StartedAt:    time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name    string
		mutate  func(p HelloPayload) HelloPayload
		wantErr error
	}{
		{name: "valid payload", mutate: func(p HelloPayload) HelloPayload { return p }},
		{
			name:    "missing platform",
			mutate:  func(p HelloPayload) HelloPayload { p.Platform = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "missing agentVersion",
			mutate:  func(p HelloPayload) HelloPayload { p.AgentVersion = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "missing bootId",
			mutate:  func(p HelloPayload) HelloPayload { p.BootID = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "zero startedAt",
			mutate:  func(p HelloPayload) HelloPayload { p.StartedAt = time.Time{}; return p },
			wantErr: ErrPayloadMissingField,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := NewHelloEnvelope(fixedClock(time.Now()), "media-03", tt.mutate(base))
			if err != nil {
				t.Fatalf("NewHelloEnvelope() error = %v", err)
			}
			data, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("json.Marshal(env) error = %v", err)
			}
			decoded, err := DecodeEnvelope(data)
			if err != nil {
				t.Fatalf("DecodeEnvelope() error = %v", err)
			}

			_, err = DecodeHelloPayload(decoded)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("DecodeHelloPayload() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DecodeHelloPayload() error = %v, want errors.Is(err, %v)", err, tt.wantErr)
			}
		})
	}
}

// TestDecodeHealthPayloadValidation is
// [TestDecodeHelloPayloadValidation]'s counterpart for health: BootID is
// HealthPayload's one required field.
func TestDecodeHealthPayloadValidation(t *testing.T) {
	env, err := NewHealthEnvelope(fixedClock(time.Now()), "media-03", HealthPayload{BootID: "", Sequence: 3})
	if err != nil {
		t.Fatalf("NewHealthEnvelope() error = %v", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error = %v", err)
	}
	decoded, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}

	_, err = DecodeHealthPayload(decoded)
	if !errors.Is(err, ErrPayloadMissingField) {
		t.Fatalf("DecodeHealthPayload() error = %v, want errors.Is(err, ErrPayloadMissingField)", err)
	}
}

// TestDecodePayloadUnsupportedSchemaVersion is the regression this package
// was most missing a test for: TestDecodePayloadUnsupportedSchema exercises
// a wrong-decoder-for-a-different-schema case (health/v1 through
// DecodeHelloPayload), not an actually unknown schema *version* of the
// right schema family, which is the case most likely to regress as new
// versions are added.
func TestDecodePayloadUnsupportedSchemaVersion(t *testing.T) {
	env := Envelope{
		Schema:    "showmesh.node.hello/v2",
		MessageID: "m",
		NodeID:    "media-03",
		SentAt:    time.Unix(0, 0),
		Payload:   json.RawMessage(`{"platform":"linux-amd64","agentVersion":"0.2.0","bootId":"boot-2","startedAt":"2026-08-10T12:00:00Z"}`),
	}

	_, err := DecodeHelloPayload(env)
	var schemaErr *UnsupportedSchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("DecodeHelloPayload() error = %v (%T), want *UnsupportedSchemaError", err, err)
	}
	if schemaErr.Got != "showmesh.node.hello/v2" || schemaErr.Want != SchemaNodeHelloV1 {
		t.Fatalf("UnsupportedSchemaError = %+v", schemaErr)
	}
}

func TestCheckNodeIDMismatch(t *testing.T) {
	env := Envelope{NodeID: "media-03"}
	if err := CheckNodeID(env, "media-03"); err != nil {
		t.Fatalf("CheckNodeID() = %v, want nil for matching IDs", err)
	}

	err := CheckNodeID(env, "media-04")
	if !errors.Is(err, ErrNodeIDMismatch) {
		t.Fatalf("CheckNodeID() error = %v, want errors.Is(err, ErrNodeIDMismatch)", err)
	}
}

// TestEnvelopeRoundTripThroughTopic exercises the full path a coordinator
// would take: parse the topic, decode the envelope, and cross-check the
// two node IDs agree. The envelope is the sole carrier of node identity;
// HelloPayload itself carries no NodeID field to disagree with it.
func TestEnvelopeRoundTripThroughTopic(t *testing.T) {
	topicStr, err := HelloTopic("media-03")
	if err != nil {
		t.Fatalf("HelloTopic() error = %v", err)
	}
	topic, err := ParseTopic(topicStr)
	if err != nil {
		t.Fatalf("ParseTopic() error = %v", err)
	}

	env, err := NewHelloEnvelope(fixedClock(time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)), "media-03", HelloPayload{
		Label:        "Media Node 03",
		Platform:     "linux-amd64",
		AgentVersion: "0.1.0",
		BootID:       "boot-1",
		StartedAt:    time.Date(2026, 8, 10, 11, 59, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewHelloEnvelope() error = %v", err)
	}

	if err := CheckNodeID(env, topic.NodeID); err != nil {
		t.Fatalf("CheckNodeID() = %v, want nil", err)
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error = %v", err)
	}
	decoded, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	payload, err := DecodeHelloPayload(decoded)
	if err != nil {
		t.Fatalf("DecodeHelloPayload() error = %v", err)
	}
	if payload.Label != "Media Node 03" {
		t.Fatalf("Label = %q, want %q", payload.Label, "Media Node 03")
	}
}

// TestEnvelopeTopicNodeIDMismatchRejected proves a decoded envelope whose
// nodeId disagrees with the node ID embedded in the topic it arrived on is
// rejected, per the requirement that the coordinator must not trust a
// payload/topic pair whose two identities disagree.
func TestEnvelopeTopicNodeIDMismatchRejected(t *testing.T) {
	topicStr, err := HelloTopic("media-03")
	if err != nil {
		t.Fatalf("HelloTopic() error = %v", err)
	}
	topic, err := ParseTopic(topicStr)
	if err != nil {
		t.Fatalf("ParseTopic() error = %v", err)
	}

	// Envelope claims a different node than the topic it arrived on.
	env, err := NewHelloEnvelope(fixedClock(time.Now()), "media-99", HelloPayload{Label: "Impersonator"})
	if err != nil {
		t.Fatalf("NewHelloEnvelope() error = %v", err)
	}

	err = CheckNodeID(env, topic.NodeID)
	if !errors.Is(err, ErrNodeIDMismatch) {
		t.Fatalf("CheckNodeID() error = %v, want errors.Is(err, ErrNodeIDMismatch)", err)
	}
}

func TestNewEnvelopeConstructorsStampRequiredFields(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC)
	clock := fixedClock(now)

	helloEnv, err := NewHelloEnvelope(clock, "media-03", HelloPayload{})
	if err != nil {
		t.Fatalf("NewHelloEnvelope() error = %v", err)
	}
	if helloEnv.Schema != SchemaNodeHelloV1 {
		t.Fatalf("Schema = %q, want %q", helloEnv.Schema, SchemaNodeHelloV1)
	}
	if helloEnv.MessageID == "" {
		t.Fatalf("MessageID is empty")
	}
	if !helloEnv.SentAt.Equal(now) {
		t.Fatalf("SentAt = %v, want %v", helloEnv.SentAt, now)
	}
	if helloEnv.NodeID != "media-03" {
		t.Fatalf("NodeID = %q, want media-03", helloEnv.NodeID)
	}

	healthEnv, err := NewHealthEnvelope(clock, "media-03", HealthPayload{BootID: "b1", Sequence: 1})
	if err != nil {
		t.Fatalf("NewHealthEnvelope() error = %v", err)
	}
	if healthEnv.Schema != SchemaNodeHealthV1 {
		t.Fatalf("Schema = %q, want %q", healthEnv.Schema, SchemaNodeHealthV1)
	}

	lwtEnv, err := NewLWTEnvelope(clock, "media-03", LWTPayload{Online: false, Reason: "unexpected disconnect"})
	if err != nil {
		t.Fatalf("NewLWTEnvelope() error = %v", err)
	}
	if lwtEnv.Schema != SchemaNodeLWTV1 {
		t.Fatalf("Schema = %q, want %q", lwtEnv.Schema, SchemaNodeLWTV1)
	}
	if lwtEnv.NodeID != "media-03" {
		t.Fatalf("NodeID = %q, want media-03", lwtEnv.NodeID)
	}

	lwtPayload, err := DecodeLWTPayload(lwtEnv)
	if err != nil {
		t.Fatalf("DecodeLWTPayload() error = %v", err)
	}
	if lwtPayload.Reason != "unexpected disconnect" {
		t.Fatalf("Reason = %q", lwtPayload.Reason)
	}
}

func TestNewEnvelopeConstructorsUseTimeNowWhenClockNil(t *testing.T) {
	before := time.Now()
	env, err := NewHelloEnvelope(nil, "media-03", HelloPayload{})
	if err != nil {
		t.Fatalf("NewHelloEnvelope() error = %v", err)
	}
	after := time.Now()
	if env.SentAt.Before(before.Add(-time.Second)) || env.SentAt.After(after.Add(time.Second)) {
		t.Fatalf("SentAt = %v, want between %v and %v", env.SentAt, before, after)
	}
}

func TestNewEnvelopeConstructorsRejectInvalidNodeID(t *testing.T) {
	_, err := NewHelloEnvelope(fixedClock(time.Now()), "Bad/ID", HelloPayload{})
	if !errors.Is(err, ErrInvalidNodeID) {
		t.Fatalf("NewHelloEnvelope() error = %v, want errors.Is(err, ErrInvalidNodeID)", err)
	}
}

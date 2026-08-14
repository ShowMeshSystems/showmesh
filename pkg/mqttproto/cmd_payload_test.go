package mqttproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// This file covers CmdPayload, ResultPayload, and AgentEchoPayload — the
// three schemas added once pkg/command's command model stopped being a
// stub (see doc.go's "Six schemas" section). It follows the exact pattern
// envelope_test.go already established for HelloPayload/HealthPayload/
// LWTPayload: Validate() rejects each missing required field individually,
// Decode round-trips through JSON, and every decoder rejects an empty or
// literal-null payload — this project has shipped the "JSON null is not an
// absent key" bug four times, and every payload decoder in this package
// has an explicit test guarding against it.

func validCmdPayload() CmdPayload {
	return CmdPayload{
		CommandID:          "cmd-1",
		IdempotencyKey:     "idem-1",
		Action:             "agent.echo",
		Target:             CmdTarget{Kind: "node", ID: "media-03"},
		Params:             map[string]any{"value": "hello"},
		Issuer:             CmdIssuer{PrincipalID: "principal-1", PrincipalName: "operator"},
		ConfirmationMethod: "evidence",
	}
}

// TestCmdPayloadValidation exercises [CmdPayload.Validate] directly: each
// required field, dropped one at a time from an otherwise-valid payload,
// must produce an error wrapping [ErrPayloadMissingField]. Deadline is
// deliberately NOT in this table — nil is the correct "no deadline" state,
// not a defect (see CmdPayload.Deadline's doc comment), so a nil Deadline
// must NOT fail Validate.
func TestCmdPayloadValidation(t *testing.T) {
	base := validCmdPayload()

	tests := []struct {
		name    string
		mutate  func(p CmdPayload) CmdPayload
		wantErr error
	}{
		{name: "valid payload", mutate: func(p CmdPayload) CmdPayload { return p }},
		{name: "nil deadline is valid, not missing", mutate: func(p CmdPayload) CmdPayload { p.Deadline = nil; return p }},
		{
			name:    "missing commandId",
			mutate:  func(p CmdPayload) CmdPayload { p.CommandID = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "missing idempotencyKey",
			mutate:  func(p CmdPayload) CmdPayload { p.IdempotencyKey = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "missing action",
			mutate:  func(p CmdPayload) CmdPayload { p.Action = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "missing target.kind",
			mutate:  func(p CmdPayload) CmdPayload { p.Target.Kind = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "missing target.id",
			mutate:  func(p CmdPayload) CmdPayload { p.Target.ID = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "missing issuer.principalId",
			mutate:  func(p CmdPayload) CmdPayload { p.Issuer.PrincipalID = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "missing confirmationMethod",
			mutate:  func(p CmdPayload) CmdPayload { p.ConfirmationMethod = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mutate(base).Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error = %v, want errors.Is(err, %v)", err, tt.wantErr)
			}
		})
	}
}

// TestCmdPayloadRoundTrip proves NewCmdEnvelope -> marshal -> DecodeEnvelope
// -> DecodeCmdPayload reproduces every field, including a set Deadline.
func TestCmdPayloadRoundTrip(t *testing.T) {
	deadline := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	p := validCmdPayload()
	p.Deadline = &deadline
	p.RequestedRevision = "rev-7"

	env, err := NewCmdEnvelope(fixedClock(time.Now()), "media-03", p)
	if err != nil {
		t.Fatalf("NewCmdEnvelope() error = %v", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error = %v", err)
	}
	decoded, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	got, err := DecodeCmdPayload(decoded)
	if err != nil {
		t.Fatalf("DecodeCmdPayload() error = %v", err)
	}

	if got.CommandID != p.CommandID || got.IdempotencyKey != p.IdempotencyKey || got.Action != p.Action {
		t.Fatalf("got = %+v, want fields matching %+v", got, p)
	}
	if got.Target != p.Target {
		t.Fatalf("Target = %+v, want %+v", got.Target, p.Target)
	}
	if got.Issuer != p.Issuer {
		t.Fatalf("Issuer = %+v, want %+v", got.Issuer, p.Issuer)
	}
	if got.RequestedRevision != p.RequestedRevision {
		t.Fatalf("RequestedRevision = %q, want %q", got.RequestedRevision, p.RequestedRevision)
	}
	if got.Deadline == nil || !got.Deadline.Equal(deadline) {
		t.Fatalf("Deadline = %v, want %v", got.Deadline, deadline)
	}
	if v, ok := got.Params["value"]; !ok || v != "hello" {
		t.Fatalf("Params[\"value\"] = %v, ok=%v, want \"hello\", true", v, ok)
	}
}

// TestCmdPayloadRoundTripNilDeadline proves a nil Deadline round-trips as
// nil, not as a zero time — the exact distinction CmdPayload.Deadline's
// doc comment and Validate's deliberate absence of a presence check exist
// to protect.
func TestCmdPayloadRoundTripNilDeadline(t *testing.T) {
	p := validCmdPayload()
	p.Deadline = nil

	env, err := NewCmdEnvelope(fixedClock(time.Now()), "media-03", p)
	if err != nil {
		t.Fatalf("NewCmdEnvelope() error = %v", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error = %v", err)
	}
	decoded, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	got, err := DecodeCmdPayload(decoded)
	if err != nil {
		t.Fatalf("DecodeCmdPayload() error = %v", err)
	}
	if got.Deadline != nil {
		t.Fatalf("Deadline = %v, want nil", got.Deadline)
	}
}

// TestCmdPayloadParamsNilVersusEmptyStayDistinguishableOnTheWire proves
// dropping `omitempty` from CmdPayload.Params's JSON tag did what it was
// supposed to: a nil Params map and an explicitly-set, empty
// map[string]any{} now marshal to two different wire values ("params":null
// vs "params":{}) rather than both collapsing into "no params key at all"
// the way `omitempty` on a map does. This is the same "absent, null, and
// explicitly empty are three different things" rule this project has
// shipped as a real bug four times (see CLAUDE.md and Params's own doc
// comment) — Deadline already gets this right (a real *time.Time,
// deliberately never `omitempty`); this test is Params's counterpart.
func TestCmdPayloadParamsNilVersusEmptyStayDistinguishableOnTheWire(t *testing.T) {
	nilParams := validCmdPayload()
	nilParams.Params = nil

	emptyParams := validCmdPayload()
	emptyParams.Params = map[string]any{}

	nilEnv, err := NewCmdEnvelope(fixedClock(time.Now()), "media-03", nilParams)
	if err != nil {
		t.Fatalf("NewCmdEnvelope() error = %v", err)
	}
	emptyEnv, err := NewCmdEnvelope(fixedClock(time.Now()), "media-03", emptyParams)
	if err != nil {
		t.Fatalf("NewCmdEnvelope() error = %v", err)
	}

	if !strings.Contains(string(nilEnv.Payload), `"params":null`) {
		t.Fatalf("nil Params payload = %s, want it to contain \"params\":null", nilEnv.Payload)
	}
	if !strings.Contains(string(emptyEnv.Payload), `"params":{}`) {
		t.Fatalf("empty Params payload = %s, want it to contain \"params\":{}", emptyEnv.Payload)
	}
	if string(nilEnv.Payload) == string(emptyEnv.Payload) {
		t.Fatalf("nil and explicitly-empty Params marshaled identically: %s", nilEnv.Payload)
	}

	// Both still decode and validate cleanly: Validate() must not require
	// Params to be non-nil — a future operation may legitimately take none.
	nilData, err := json.Marshal(nilEnv)
	if err != nil {
		t.Fatalf("json.Marshal(nilEnv) error = %v", err)
	}
	nilDecoded, err := DecodeEnvelope(nilData)
	if err != nil {
		t.Fatalf("DecodeEnvelope(nil Params) error = %v", err)
	}
	if _, err := DecodeCmdPayload(nilDecoded); err != nil {
		t.Fatalf("DecodeCmdPayload(nil Params) error = %v, want nil", err)
	}
}

// TestDecodeCmdPayloadRejectsEmptyOrNullPayload is
// TestDecodeHelloPayloadRejectsEmptyOrNullPayload's counterpart for cmd:
// json.Unmarshal([]byte("null"), &p) is a documented no-op, so without an
// explicit check, an envelope with "payload":null (or no "payload" key at
// all) would decode into a fully zero-valued CmdPayload — this guard is
// what turns that into a rejected decode instead of a phantom command with
// an empty CommandID, empty Action, and no idempotency key at all.
func TestDecodeCmdPayloadRejectsEmptyOrNullPayload(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			name: "null payload",
			json: `{"schema":"showmesh.node.cmd/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z","payload":null}`,
		},
		{
			name: "absent payload key",
			json: `{"schema":"showmesh.node.cmd/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := DecodeEnvelope([]byte(tt.json))
			if err != nil {
				t.Fatalf("DecodeEnvelope() error = %v, want nil: the envelope itself is well-formed", err)
			}
			_, err = DecodeCmdPayload(env)
			if !errors.Is(err, ErrPayloadEmpty) {
				t.Fatalf("DecodeCmdPayload() error = %v, want errors.Is(err, ErrPayloadEmpty)", err)
			}
		})
	}
}

// TestDecodeCmdPayloadWrongSchema proves DecodeCmdPayload rejects a
// well-formed envelope of a different schema with a typed
// [*UnsupportedSchemaError] rather than silently decoding the wrong shape.
func TestDecodeCmdPayloadWrongSchema(t *testing.T) {
	env, err := NewHelloEnvelope(fixedClock(time.Now()), "media-03", HelloPayload{
		Platform: "linux-amd64", AgentVersion: "0.1.0", BootID: "b1", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("NewHelloEnvelope() error = %v", err)
	}
	_, err = DecodeCmdPayload(env)
	var schemaErr *UnsupportedSchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("DecodeCmdPayload() error = %v (%T), want *UnsupportedSchemaError", err, err)
	}
	if schemaErr.Want != SchemaNodeCmdV1 {
		t.Fatalf("UnsupportedSchemaError.Want = %q, want %q", schemaErr.Want, SchemaNodeCmdV1)
	}
}

func validResultPayload() ResultPayload {
	return ResultPayload{
		CommandID:      "cmd-1",
		IdempotencyKey: "idem-1",
		Action:         "agent.echo",
		Outcome:        OutcomeConfirmed,
		ReceivedAt:     time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
		RespondedAt:    time.Date(2026, 8, 13, 12, 0, 1, 0, time.UTC),
	}
}

// TestResultPayloadValidation exercises [ResultPayload.Validate]: missing
// required fields, an outcome outside the closed vocabulary, and a
// non-confirmed outcome with no reason.
func TestResultPayloadValidation(t *testing.T) {
	base := validResultPayload()

	tests := []struct {
		name    string
		mutate  func(p ResultPayload) ResultPayload
		wantErr error
	}{
		{name: "valid confirmed payload, no reason required", mutate: func(p ResultPayload) ResultPayload { return p }},
		{
			name: "valid unconfirmed payload with reason",
			mutate: func(p ResultPayload) ResultPayload {
				p.Outcome = OutcomeUnconfirmed
				p.Reason = "no fresh evidence yet"
				return p
			},
		},
		{
			name:    "missing commandId",
			mutate:  func(p ResultPayload) ResultPayload { p.CommandID = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "missing idempotencyKey",
			mutate:  func(p ResultPayload) ResultPayload { p.IdempotencyKey = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "missing action",
			mutate:  func(p ResultPayload) ResultPayload { p.Action = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "outcome outside the closed vocabulary",
			mutate:  func(p ResultPayload) ResultPayload { p.Outcome = "success"; return p },
			wantErr: ErrPayloadInvalidOutcome,
		},
		{
			name:    "refused outcome with empty reason",
			mutate:  func(p ResultPayload) ResultPayload { p.Outcome = OutcomeRefused; p.Reason = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "failed outcome with empty reason",
			mutate:  func(p ResultPayload) ResultPayload { p.Outcome = OutcomeFailed; p.Reason = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
		{
			name:    "unconfirmed outcome with empty reason",
			mutate:  func(p ResultPayload) ResultPayload { p.Outcome = OutcomeUnconfirmed; p.Reason = ""; return p },
			wantErr: ErrPayloadMissingField,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mutate(base).Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error = %v, want errors.Is(err, %v)", err, tt.wantErr)
			}
		})
	}
}

// TestResultPayloadRoundTrip proves NewResultEnvelope -> marshal ->
// DecodeEnvelope -> DecodeResultPayload reproduces every field, including
// nested Evidence with its own ObservedAt pointer.
func TestResultPayloadRoundTrip(t *testing.T) {
	observedAt := time.Date(2026, 8, 13, 12, 0, 0, 500000000, time.UTC)
	executedAt := time.Date(2026, 8, 13, 12, 0, 0, 200000000, time.UTC)
	p := ResultPayload{
		CommandID:      "cmd-1",
		IdempotencyKey: "idem-1",
		Action:         "agent.echo",
		Outcome:        OutcomeConfirmed,
		Evidence: &ResultEvidence{
			Signal:      "node.agent.echo_value",
			Value:       "hello",
			ObservedAt:  &observedAt,
			CollectedAt: observedAt,
		},
		ReceivedAt:  time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
		ExecutedAt:  &executedAt,
		RespondedAt: time.Date(2026, 8, 13, 12, 0, 0, 600000000, time.UTC),
	}

	env, err := NewResultEnvelope(fixedClock(time.Now()), "media-03", p)
	if err != nil {
		t.Fatalf("NewResultEnvelope() error = %v", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error = %v", err)
	}
	decoded, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	got, err := DecodeResultPayload(decoded)
	if err != nil {
		t.Fatalf("DecodeResultPayload() error = %v", err)
	}

	if got.CommandID != p.CommandID || got.Outcome != p.Outcome {
		t.Fatalf("got = %+v, want fields matching %+v", got, p)
	}
	if got.Evidence == nil {
		t.Fatalf("Evidence is nil, want non-nil")
	}
	if got.Evidence.Signal != p.Evidence.Signal {
		t.Fatalf("Evidence.Signal = %q, want %q", got.Evidence.Signal, p.Evidence.Signal)
	}
	if got.Evidence.Value != p.Evidence.Value {
		t.Fatalf("Evidence.Value = %v, want %v", got.Evidence.Value, p.Evidence.Value)
	}
	if got.Evidence.ObservedAt == nil || !got.Evidence.ObservedAt.Equal(observedAt) {
		t.Fatalf("Evidence.ObservedAt = %v, want %v", got.Evidence.ObservedAt, observedAt)
	}
	if got.ExecutedAt == nil || !got.ExecutedAt.Equal(executedAt) {
		t.Fatalf("ExecutedAt = %v, want %v", got.ExecutedAt, executedAt)
	}
}

// TestDecodeResultPayloadRejectsEmptyOrNullPayload is
// TestDecodeCmdPayloadRejectsEmptyOrNullPayload's counterpart for result.
func TestDecodeResultPayloadRejectsEmptyOrNullPayload(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			name: "null payload",
			json: `{"schema":"showmesh.node.result/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z","payload":null}`,
		},
		{
			name: "absent payload key",
			json: `{"schema":"showmesh.node.result/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := DecodeEnvelope([]byte(tt.json))
			if err != nil {
				t.Fatalf("DecodeEnvelope() error = %v, want nil: the envelope itself is well-formed", err)
			}
			_, err = DecodeResultPayload(env)
			if !errors.Is(err, ErrPayloadEmpty) {
				t.Fatalf("DecodeResultPayload() error = %v, want errors.Is(err, ErrPayloadEmpty)", err)
			}
		})
	}
}

// TestDecodeResultPayloadWrongSchema mirrors
// TestDecodeCmdPayloadWrongSchema for DecodeResultPayload.
func TestDecodeResultPayloadWrongSchema(t *testing.T) {
	env, err := NewCmdEnvelope(fixedClock(time.Now()), "media-03", validCmdPayload())
	if err != nil {
		t.Fatalf("NewCmdEnvelope() error = %v", err)
	}
	_, err = DecodeResultPayload(env)
	var schemaErr *UnsupportedSchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("DecodeResultPayload() error = %v (%T), want *UnsupportedSchemaError", err, err)
	}
	if schemaErr.Want != SchemaNodeResultV1 {
		t.Fatalf("UnsupportedSchemaError.Want = %q, want %q", schemaErr.Want, SchemaNodeResultV1)
	}
}

// TestAgentEchoPayloadRoundTrip proves NewAgentEchoEnvelope round-trips
// through JSON. AgentEchoPayload has no Validate method (matching
// LWTPayload — see its doc comment), so this is the only coverage this
// payload needs beyond the shared envelope-level tests.
func TestAgentEchoPayloadRoundTrip(t *testing.T) {
	appliedAt := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	p := AgentEchoPayload{Value: "hello world", AppliedAt: appliedAt}

	env, err := NewAgentEchoEnvelope(fixedClock(time.Now()), "media-03", p)
	if err != nil {
		t.Fatalf("NewAgentEchoEnvelope() error = %v", err)
	}
	if env.Schema != SchemaNodeAgentEchoV1 {
		t.Fatalf("Schema = %q, want %q", env.Schema, SchemaNodeAgentEchoV1)
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error = %v", err)
	}
	decoded, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}

	var got AgentEchoPayload
	if err := json.Unmarshal(decoded.Payload, &got); err != nil {
		t.Fatalf("json.Unmarshal(decoded.Payload) error = %v", err)
	}
	if got.Value != p.Value {
		t.Fatalf("Value = %q, want %q", got.Value, p.Value)
	}
	if !got.AppliedAt.Equal(appliedAt) {
		t.Fatalf("AppliedAt = %v, want %v", got.AppliedAt, appliedAt)
	}
}

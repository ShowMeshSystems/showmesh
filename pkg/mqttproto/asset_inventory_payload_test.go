package mqttproto

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// This file covers AssetInventoryPayload (Track E), following the pattern
// cmd_payload_test.go already established for CmdPayload/ResultPayload:
// Validate() is exercised directly, Decode round-trips through JSON, and
// the decoder rejects an empty or literal-null payload.

func validAssetInventoryPayload() AssetInventoryPayload {
	return AssetInventoryPayload{
		Complete: true,
		Reason:   "",
		Assets: []AssetInventoryEntry{
			{
				ContentHash: "sha256:abc123",
				Filename:    "Thriller.fseq",
				SizeBytes:   1234567,
				VerifiedAt:  time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
			},
		},
	}
}

// TestAssetInventoryPayloadValidation proves Reason is required whenever
// Complete is false, and that a Complete:true payload needs no Reason —
// this is the wire-level enforcement of this seam's central rule: complete
// has to be earned, and a false completeness always carries a reason.
func TestAssetInventoryPayloadValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(p AssetInventoryPayload) AssetInventoryPayload
		wantErr error
	}{
		{name: "complete true, no reason needed", mutate: func(p AssetInventoryPayload) AssetInventoryPayload { return p }},
		{
			name: "complete false with reason is valid",
			mutate: func(p AssetInventoryPayload) AssetInventoryPayload {
				p.Complete = false
				p.Reason = "could not read asset directory"
				p.Assets = nil
				return p
			},
		},
		{
			name: "complete false with empty reason is invalid",
			mutate: func(p AssetInventoryPayload) AssetInventoryPayload {
				p.Complete = false
				p.Reason = ""
				return p
			},
			wantErr: ErrPayloadMissingField,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mutate(validAssetInventoryPayload()).Validate()
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

// TestAssetInventoryPayloadRoundTrip proves NewAssetInventoryEnvelope ->
// marshal -> DecodeEnvelope -> DecodeAssetInventoryPayload reproduces every
// field, including a nested asset's VerifiedAt.
func TestAssetInventoryPayloadRoundTrip(t *testing.T) {
	p := validAssetInventoryPayload()

	env, err := NewAssetInventoryEnvelope(fixedClock(time.Now()), "media-03", p)
	if err != nil {
		t.Fatalf("NewAssetInventoryEnvelope() error = %v", err)
	}
	if env.Schema != SchemaNodeAssetInventoryV1 {
		t.Fatalf("Schema = %q, want %q", env.Schema, SchemaNodeAssetInventoryV1)
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error = %v", err)
	}
	decoded, err := DecodeEnvelope(data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	got, err := DecodeAssetInventoryPayload(decoded)
	if err != nil {
		t.Fatalf("DecodeAssetInventoryPayload() error = %v", err)
	}

	if got.Complete != p.Complete || got.Reason != p.Reason {
		t.Fatalf("got = %+v, want fields matching %+v", got, p)
	}
	if len(got.Assets) != 1 {
		t.Fatalf("len(Assets) = %d, want 1", len(got.Assets))
	}
	if got.Assets[0].ContentHash != p.Assets[0].ContentHash ||
		got.Assets[0].Filename != p.Assets[0].Filename ||
		got.Assets[0].SizeBytes != p.Assets[0].SizeBytes ||
		!got.Assets[0].VerifiedAt.Equal(p.Assets[0].VerifiedAt) {
		t.Fatalf("Assets[0] = %+v, want %+v", got.Assets[0], p.Assets[0])
	}
}

// TestAssetInventoryPayloadNilVersusEmptyAssetsStayDistinguishable mirrors
// TestCmdPayloadParamsNilVersusEmptyStayDistinguishableOnTheWire: Assets has
// no `omitempty`, so a nil slice and an explicit empty slice marshal to two
// different wire values, matching this project's standing "absent, null,
// and explicitly empty are three different things" rule.
func TestAssetInventoryPayloadNilVersusEmptyAssetsStayDistinguishable(t *testing.T) {
	nilAssets := AssetInventoryPayload{Complete: true}
	emptyAssets := AssetInventoryPayload{Complete: true, Assets: []AssetInventoryEntry{}}

	nilEnv, err := NewAssetInventoryEnvelope(fixedClock(time.Now()), "media-03", nilAssets)
	if err != nil {
		t.Fatalf("NewAssetInventoryEnvelope() error = %v", err)
	}
	emptyEnv, err := NewAssetInventoryEnvelope(fixedClock(time.Now()), "media-03", emptyAssets)
	if err != nil {
		t.Fatalf("NewAssetInventoryEnvelope() error = %v", err)
	}

	if string(nilEnv.Payload) == string(emptyEnv.Payload) {
		t.Fatalf("nil and explicitly-empty Assets marshaled identically: %s", nilEnv.Payload)
	}
}

// TestDecodeAssetInventoryPayloadRejectsEmptyOrNullPayload mirrors
// TestDecodeCmdPayloadRejectsEmptyOrNullPayload for asset inventory.
func TestDecodeAssetInventoryPayloadRejectsEmptyOrNullPayload(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			name: "null payload",
			json: `{"schema":"showmesh.node.asset.inventory/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-16T12:00:00Z","payload":null}`,
		},
		{
			name: "absent payload key",
			json: `{"schema":"showmesh.node.asset.inventory/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-16T12:00:00Z"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := DecodeEnvelope([]byte(tt.json))
			if err != nil {
				t.Fatalf("DecodeEnvelope() error = %v, want nil: the envelope itself is well-formed", err)
			}
			_, err = DecodeAssetInventoryPayload(env)
			if !errors.Is(err, ErrPayloadEmpty) {
				t.Fatalf("DecodeAssetInventoryPayload() error = %v, want errors.Is(err, ErrPayloadEmpty)", err)
			}
		})
	}
}

// TestDecodeAssetInventoryPayloadWrongSchema mirrors
// TestDecodeCmdPayloadWrongSchema for DecodeAssetInventoryPayload.
func TestDecodeAssetInventoryPayloadWrongSchema(t *testing.T) {
	env, err := NewCmdEnvelope(fixedClock(time.Now()), "media-03", validCmdPayload())
	if err != nil {
		t.Fatalf("NewCmdEnvelope() error = %v", err)
	}
	_, err = DecodeAssetInventoryPayload(env)
	var schemaErr *UnsupportedSchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("DecodeAssetInventoryPayload() error = %v (%T), want *UnsupportedSchemaError", err, err)
	}
	if schemaErr.Want != SchemaNodeAssetInventoryV1 {
		t.Fatalf("UnsupportedSchemaError.Want = %q, want %q", schemaErr.Want, SchemaNodeAssetInventoryV1)
	}
}

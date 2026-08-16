package config

import (
	"encoding/json"
	"testing"
)

func TestDecodeShowActivePayloadValid(t *testing.T) {
	p, verr := DecodeShowActivePayload(`{"show": "halloween-2026"}`, alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Show != "halloween-2026" {
		t.Fatalf("unexpected payload: %+v", p)
	}
}

func TestEncodeShowActivePayloadRoundTrips(t *testing.T) {
	p, verr := DecodeShowActivePayload(`{"show": "halloween-2026"}`, alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	raw, err := EncodeShowActivePayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if back["show"] != "halloween-2026" {
		t.Fatalf("show did not round trip: %v", back["show"])
	}
}

func TestDecodeShowActivePayloadShowAbsent(t *testing.T) {
	_, verr := DecodeShowActivePayload(`{}`, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "show" {
		t.Fatalf("expected field-required on show, got %+v", verr)
	}
}

func TestDecodeShowActivePayloadShowNull(t *testing.T) {
	_, verr := DecodeShowActivePayload(`{"show": null}`, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "show" {
		t.Fatalf("expected field-null on show, got %+v", verr)
	}
}

func TestDecodeShowActivePayloadShowEmpty(t *testing.T) {
	_, verr := DecodeShowActivePayload(`{"show": ""}`, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldEmpty || verr.Field != "show" {
		t.Fatalf("expected field-empty on show, got %+v", verr)
	}
}

func TestDecodeShowActivePayloadShowUnknown(t *testing.T) {
	_, verr := DecodeShowActivePayload(`{"show": "no-such-show"}`, alwaysFalse)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "show" {
		t.Fatalf("expected field-unknown-reference on show, got %+v", verr)
	}
}

func TestDecodeShowActivePayloadUnknownTopLevelKey(t *testing.T) {
	_, verr := DecodeShowActivePayload(`{"show": "halloween-2026", "activatedAt": "2026-08-16"}`, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key, got %+v", verr)
	}
}

// TestShowActiveObjectIDIsFixed guards against ShowActiveObjectID ever
// being derived from a configuration value: it must always equal "active",
// full stop, regardless of what test data surrounds it in this suite.
func TestShowActiveObjectIDIsFixed(t *testing.T) {
	if ShowActiveObjectID != "active" {
		t.Fatalf("expected ShowActiveObjectID to be the fixed constant %q, got %q", "active", ShowActiveObjectID)
	}
}

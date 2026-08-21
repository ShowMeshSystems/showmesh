package config

import (
	"encoding/json"
	"testing"
)

func alwaysTrueNightSessionExists(string) bool  { return true }
func alwaysFalseNightSessionExists(string) bool { return false }

func TestDecodeNightSessionActivePayloadValid(t *testing.T) {
	p, verr := DecodeNightSessionActivePayload(`{"session": "halloween-main"}`, alwaysTrueNightSessionExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Session != "halloween-main" {
		t.Fatalf("unexpected payload: %+v", p)
	}
}

func TestEncodeNightSessionActivePayloadRoundTrips(t *testing.T) {
	p, verr := DecodeNightSessionActivePayload(`{"session": "halloween-main"}`, alwaysTrueNightSessionExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	raw, err := EncodeNightSessionActivePayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if back["session"] != "halloween-main" {
		t.Fatalf("session did not round trip: %v", back["session"])
	}
}

func TestDecodeNightSessionActivePayloadSessionAbsentIsRejected(t *testing.T) {
	_, verr := DecodeNightSessionActivePayload(`{}`, alwaysTrueNightSessionExists)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "session" {
		t.Fatalf("expected field-required on session, got %+v", verr)
	}
}

func TestDecodeNightSessionActivePayloadSessionNull(t *testing.T) {
	_, verr := DecodeNightSessionActivePayload(`{"session": null}`, alwaysTrueNightSessionExists)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "session" {
		t.Fatalf("expected field-null on session, got %+v", verr)
	}
}

// TestDecodeNightSessionActivePayloadEmptySessionClears is the
// zero-to-one-and-back-to-zero transition ADR-039 rule 4 requires: an
// explicit empty string is accepted (unlike every other required string in
// this package) and means "no active night session", never "invalid".
func TestDecodeNightSessionActivePayloadEmptySessionClears(t *testing.T) {
	p, verr := DecodeNightSessionActivePayload(`{"session": ""}`, alwaysFalseNightSessionExists)
	if verr != nil {
		t.Fatalf("unexpected error clearing the pointer: %+v", verr)
	}
	if p.Session != "" {
		t.Fatalf("expected an empty Session, got %q", p.Session)
	}
}

func TestDecodeNightSessionActivePayloadUnknownSessionRejected(t *testing.T) {
	_, verr := DecodeNightSessionActivePayload(`{"session": "no-such-session"}`, alwaysFalseNightSessionExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "session" {
		t.Fatalf("expected field-unknown-reference on session, got %+v", verr)
	}
}

func TestDecodeNightSessionActivePayloadUnknownTopLevelKey(t *testing.T) {
	_, verr := DecodeNightSessionActivePayload(`{"session": "halloween-main", "activatedAt": "2026-08-16"}`, alwaysTrueNightSessionExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key, got %+v", verr)
	}
}

// TestNightSessionActiveObjectIDIsFixed guards against
// NightSessionActiveObjectID ever being derived from a configuration
// value — see the reserved identifier list in the seam spec ("default").
func TestNightSessionActiveObjectIDIsFixed(t *testing.T) {
	if NightSessionActiveObjectID != "default" {
		t.Fatalf("expected NightSessionActiveObjectID to be the fixed constant %q, got %q", "default", NightSessionActiveObjectID)
	}
}

package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeShowPayloadValid(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "Halloween 2026", "notes": "the good one"}`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Name != "Halloween 2026" || p.Notes != "the good one" {
		t.Fatalf("unexpected payload: %+v", p)
	}
}

func TestEncodeShowPayloadRoundTrips(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "Halloween 2026", "notes": ""}`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	raw, err := EncodeShowPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if back["name"] != "Halloween 2026" {
		t.Fatalf("name did not round trip: %v", back["name"])
	}
}

func TestDecodeShowPayloadNameAbsent(t *testing.T) {
	_, verr := DecodeShowPayload(`{"notes": "x"}`)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "name" {
		t.Fatalf("expected field-required on name, got %+v", verr)
	}
}

func TestDecodeShowPayloadNameNull(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": null, "notes": "x"}`)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "name" {
		t.Fatalf("expected field-null on name, got %+v", verr)
	}
}

func TestDecodeShowPayloadNameEmpty(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "", "notes": "x"}`)
	if verr == nil || verr.Code != ValidationCodeFieldEmpty || verr.Field != "name" {
		t.Fatalf("expected field-empty on name, got %+v", verr)
	}
}

func TestDecodeShowPayloadNameTooLong(t *testing.T) {
	long := strings.Repeat("a", maxShowNameRunes+1)
	_, verr := DecodeShowPayload(`{"name": "` + long + `", "notes": ""}`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "name" {
		t.Fatalf("expected field-invalid on name, got %+v", verr)
	}
}

// TestDecodeShowPayloadNotesAbsentMeansEmpty is the load-bearing test for
// this kind's "PUT is a full replacement" rule: an absent notes key must
// decode to "", the same as an explicitly empty string, and must NEVER be
// treated as "leave whatever was there before" — Step 7 shipped exactly
// that defect for a different field and wiped every FPP endpoint.
func TestDecodeShowPayloadNotesAbsentMeansEmpty(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "Halloween 2026"}`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Notes != "" {
		t.Fatalf("expected absent notes to decode to empty string, got %q", p.Notes)
	}
}

func TestDecodeShowPayloadNotesExplicitlyEmpty(t *testing.T) {
	p, verr := DecodeShowPayload(`{"name": "Halloween 2026", "notes": ""}`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Notes != "" {
		t.Fatalf("expected explicit empty notes to decode to empty string, got %q", p.Notes)
	}
}

func TestDecodeShowPayloadNotesNull(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "Halloween 2026", "notes": null}`)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "notes" {
		t.Fatalf("expected field-null on notes, got %+v", verr)
	}
}

func TestDecodeShowPayloadNotesTooLong(t *testing.T) {
	long := strings.Repeat("a", maxShowNotesRunes+1)
	_, verr := DecodeShowPayload(`{"name": "Halloween 2026", "notes": "` + long + `"}`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "notes" {
		t.Fatalf("expected field-invalid on notes, got %+v", verr)
	}
}

func TestDecodeShowPayloadUnknownTopLevelKey(t *testing.T) {
	_, verr := DecodeShowPayload(`{"name": "Halloween 2026", "surfaces": []}`)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key, got %+v", verr)
	}
}

func TestDecodeShowPayloadBodyNotObject(t *testing.T) {
	_, verr := DecodeShowPayload(`[1,2,3]`)
	if verr == nil || verr.Code != ValidationCodeBodyInvalid {
		t.Fatalf("expected body-invalid, got %+v", verr)
	}
}

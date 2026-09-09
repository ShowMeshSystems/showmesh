package mqttproto

import (
	"errors"
	"testing"
	"time"
)

func validClockPayload() ClockPayload {
	now := time.Now()
	return ClockPayload{
		State:     "locked",
		Provider:  "external",
		Role:      "follower",
		RoleKnown: true,
		Owner:     "external (unidentified)",
		Interface: "eth0",
		Domain:    24, DomainKnown: true,
		GrandmasterIdentity: "3cecef.fffe.a1b2c3", GMKnown: true,
		Timescale:   "ptp",
		OffsetNs:    -42,
		OffsetKnown: true,
		ObservedAt:  &now,
	}
}

func TestClockPayloadValidateAcceptsMinimalValidPayload(t *testing.T) {
	p := validClockPayload()
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestClockPayloadValidateRequiresState(t *testing.T) {
	p := validClockPayload()
	p.State = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(empty state) = %v, want ErrPayloadMissingField", err)
	}
}

func TestClockPayloadValidateRequiresReasonWhenNotLocked(t *testing.T) {
	p := validClockPayload()
	p.State = "acquiring"
	p.Reason = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(acquiring, no reason) = %v, want ErrPayloadMissingField", err)
	}
	p.Reason = "not yet locked"
	if err := p.Validate(); err != nil {
		t.Errorf("Validate(acquiring, with reason) = %v, want nil", err)
	}
}

func TestClockPayloadValidateNoReasonRequiredWhenLocked(t *testing.T) {
	p := validClockPayload()
	p.State = "locked"
	p.Reason = ""
	if err := p.Validate(); err != nil {
		t.Errorf("Validate(locked, no reason) = %v, want nil", err)
	}
}

func TestClockPayloadValidateRequiresProvider(t *testing.T) {
	p := validClockPayload()
	p.Provider = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(empty provider) = %v, want ErrPayloadMissingField", err)
	}
}

func TestClockPayloadValidateRequiresTimescale(t *testing.T) {
	p := validClockPayload()
	p.Timescale = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(empty timescale) = %v, want ErrPayloadMissingField", err)
	}
}

func TestClockPayloadValidateRequiresMismatchReasonWhenMismatch(t *testing.T) {
	p := validClockPayload()
	p.Mismatch = true
	p.MismatchReason = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(mismatch, no reason) = %v, want ErrPayloadMissingField", err)
	}
	p.MismatchReason = "locked to a different domain than declared"
	if err := p.Validate(); err != nil {
		t.Errorf("Validate(mismatch, with reason) = %v, want nil", err)
	}
}

func TestClockPayloadValidateRequiresObservedAt(t *testing.T) {
	p := validClockPayload()
	p.ObservedAt = nil
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(nil observedAt) = %v, want ErrPayloadMissingField", err)
	}
}

func TestNewClockEnvelopeRoundTrips(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }
	p := validClockPayload()
	env, err := NewClockEnvelope(now, "node-1", p)
	if err != nil {
		t.Fatalf("NewClockEnvelope: %v", err)
	}
	if env.Schema != SchemaNodeClockV1 {
		t.Fatalf("Schema = %q, want %q", env.Schema, SchemaNodeClockV1)
	}
	got, err := DecodeClockPayload(env)
	if err != nil {
		t.Fatalf("DecodeClockPayload: %v", err)
	}
	if got.State != p.State || got.GrandmasterIdentity != p.GrandmasterIdentity {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, p)
	}
}

func TestNewClockEnvelopeRefusesInvalidPayload(t *testing.T) {
	now := func() time.Time { return time.Now() }
	p := validClockPayload()
	p.State = ""
	if _, err := NewClockEnvelope(now, "node-1", p); err == nil {
		t.Fatalf("expected NewClockEnvelope to refuse an invalid payload")
	}
}

func TestDecodeClockPayloadRejectsWrongSchema(t *testing.T) {
	env, err := NewAudioEnvelope(func() time.Time { return time.Now() }, "node-1", validAudioPayload())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeClockPayload(env); err == nil {
		t.Fatalf("expected an UnsupportedSchemaError")
	}
}

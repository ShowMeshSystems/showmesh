package mqttproto

import (
	"errors"
	"testing"
	"time"
)

func validAudioPayload() AudioPayload {
	now := time.Now()
	return AudioPayload{
		EngineAvailable:    true,
		HardwareEnumerated: true,
		DeviceAvailable:    true,
		OutputsCount:       1,
		ProgramAvailable:   true,
		LTCAvailable:       false,
		LTCReason:          "no route achieved 3 or more channels",
		Routes: []AudioRouteReport{
			{Device: "hw:CARD=PCH,DEV=0", Available: true, Channels: 2, Rate: 48000, Format: "S16LE"},
		},
		DiscoveredAt:       &now,
		ObservedAt:         &now,
		Sessions:           []AudioSessionReport{},
		LTCGeneratorState:  "stopped",
		LTCGeneratorReason: "no generator has ever been started on this node",
	}
}

func TestAudioPayloadValidateRejectsNilRoutes(t *testing.T) {
	p := validAudioPayload()
	p.Routes = nil
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(nil routes) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateAcceptsEmptyRoutes(t *testing.T) {
	p := validAudioPayload()
	p.Routes = []AudioRouteReport{}
	p.DeviceAvailable = false
	p.DeviceReason = "no real hardware card found"
	p.ProgramAvailable = false
	p.ProgramReason = "no route achieved 1 or more channels"
	p.OutputsCount = 0
	if err := p.Validate(); err != nil {
		t.Errorf("Validate(empty routes, engine-only) = %v, want nil", err)
	}
}

func TestAudioPayloadValidateRejectsTooManyRoutes(t *testing.T) {
	p := validAudioPayload()
	routes := make([]AudioRouteReport, maxAudioRoutes+1)
	for i := range routes {
		routes[i] = AudioRouteReport{Device: "hw:CARD=X,DEV=0", Available: true, Channels: 2, Rate: 48000, Format: "S16LE"}
	}
	p.Routes = routes
	if err := p.Validate(); !errors.Is(err, ErrPayloadTooLarge) {
		t.Errorf("Validate(%d routes) = %v, want ErrPayloadTooLarge", len(routes), err)
	}
}

func TestAudioPayloadValidateRequiresEngineReasonWhenUnavailable(t *testing.T) {
	p := validAudioPayload()
	p.EngineAvailable = false
	p.EngineReason = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(engine unavailable, no reason) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRequiresDeviceReasonWhenUnavailable(t *testing.T) {
	p := validAudioPayload()
	p.DeviceAvailable = false
	p.DeviceReason = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(device unavailable, no reason) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRequiresProgramReasonWhenUnavailable(t *testing.T) {
	p := validAudioPayload()
	p.ProgramAvailable = false
	p.ProgramReason = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(program unavailable, no reason) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRequiresLTCReasonWhenUnavailable(t *testing.T) {
	p := validAudioPayload()
	p.LTCAvailable = false
	p.LTCReason = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(ltc unavailable, no reason) = %v, want ErrPayloadMissingField", err)
	}
}

// TestAudioPayloadValidateRequiresHardwareEnumeratedReasonWhenFalse proves
// "we could not enumerate" carries its own reason, distinct from "we
// enumerated and found nothing".
func TestAudioPayloadValidateRequiresHardwareEnumeratedReasonWhenFalse(t *testing.T) {
	p := validAudioPayload()
	p.HardwareEnumerated = false
	p.HardwareEnumeratedReason = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(hardware not enumerated, no reason) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRequiresObservedAt(t *testing.T) {
	p := validAudioPayload()
	p.ObservedAt = nil
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(nil observedAt) = %v, want ErrPayloadMissingField", err)
	}
}

// TestAudioPayloadValidateAcceptsNilDiscoveredAt verifies the additive-
// only contract (ADR-020, constraint 21): a retained payload from an
// older agent that never sent discoveredAt must still validate, so an
// upgraded coordinator does not reject the whole payload and lose every
// discovery-backed signal for that node. nil means genuinely unknown,
// matching ObservedAt's own convention, never a validation failure.
func TestAudioPayloadValidateAcceptsNilDiscoveredAt(t *testing.T) {
	p := validAudioPayload()
	p.DiscoveredAt = nil
	if err := p.Validate(); err != nil {
		t.Errorf("Validate(nil discoveredAt) = %v, want no error", err)
	}
}

func TestAudioPayloadValidateRequiresRouteDevice(t *testing.T) {
	p := validAudioPayload()
	p.Routes[0].Device = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(route with no device) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRequiresRouteReasonWhenUnavailable(t *testing.T) {
	p := validAudioPayload()
	p.Routes[0].Available = false
	p.Routes[0].Reason = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(unavailable route with no reason) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRejectsNilSessions(t *testing.T) {
	p := validAudioPayload()
	p.Sessions = nil
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(nil sessions) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateAcceptsEmptySessions(t *testing.T) {
	p := validAudioPayload()
	p.Sessions = []AudioSessionReport{}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate(empty sessions) = %v, want nil", err)
	}
}

func TestAudioPayloadValidateRejectsTooManySessions(t *testing.T) {
	p := validAudioPayload()
	sessions := make([]AudioSessionReport, maxAudioSessions+1)
	for i := range sessions {
		sessions[i] = AudioSessionReport{SessionID: "s", Fault: "none"}
	}
	p.Sessions = sessions
	if err := p.Validate(); !errors.Is(err, ErrPayloadTooLarge) {
		t.Errorf("Validate(too many sessions) = %v, want ErrPayloadTooLarge", err)
	}
}

func TestAudioPayloadValidateRequiresSessionID(t *testing.T) {
	p := validAudioPayload()
	p.Sessions = []AudioSessionReport{{Fault: "none"}}
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(session with no id) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRequiresFaultReasonWhenFaulted(t *testing.T) {
	p := validAudioPayload()
	p.Sessions = []AudioSessionReport{{SessionID: "s1", Fault: "pipeline_crash", FaultReason: ""}}
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(faulted session with no reason) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRequiresLTCClaimReasonWhenRefused(t *testing.T) {
	p := validAudioPayload()
	p.Sessions = []AudioSessionReport{{SessionID: "s1", Fault: "none", LTCClaimState: "refused", LTCClaimReason: ""}}
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(refused claim with no reason) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateAllowsHeldClaimWithNoReason(t *testing.T) {
	p := validAudioPayload()
	p.Sessions = []AudioSessionReport{{SessionID: "s1", Fault: "none", LTCClaimState: "held", LTCClaimReason: ""}}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate(held claim, no reason) = %v, want nil", err)
	}
}

func TestAudioPayloadValidateRequiresLTCGeneratorState(t *testing.T) {
	p := validAudioPayload()
	p.LTCGeneratorState = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(no ltcGeneratorState) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRequiresLTCGeneratorReasonWhenNotRunning(t *testing.T) {
	p := validAudioPayload()
	p.LTCGeneratorState = "failed"
	p.LTCGeneratorReason = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(failed with no reason) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateAllowsRunningWithNoReason(t *testing.T) {
	p := validAudioPayload()
	p.LTCGeneratorState = "running"
	p.LTCGeneratorReason = ""
	if err := p.Validate(); err != nil {
		t.Errorf("Validate(running, no reason) = %v, want nil", err)
	}
}

func TestAudioPayloadValidateRequiresFrameRateWhenKnown(t *testing.T) {
	p := validAudioPayload()
	p.LTCFrameRateKnown = true
	p.LTCFrameRate = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(frameRateKnown, no rate) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRequiresTimecodeWhenKnown(t *testing.T) {
	p := validAudioPayload()
	p.LTCTimecodeKnown = true
	p.LTCTimecode = ""
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(timecodeKnown, no timecode) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRequiresGlitchCountsSinceWhenKnown(t *testing.T) {
	p := validAudioPayload()
	p.EngineGlitchCountsKnown = true
	p.EngineGlitchCountsSince = nil
	if err := p.Validate(); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("Validate(glitchCountsKnown, no since) = %v, want ErrPayloadMissingField", err)
	}
}

func TestAudioPayloadValidateRejectsNonzeroGlitchCountsWhenNotKnown(t *testing.T) {
	p := validAudioPayload()
	p.EngineGlitchCountsKnown = false
	p.EngineStreamWarningCount = 1
	if err := p.Validate(); !errors.Is(err, ErrPayloadInconsistentField) {
		t.Errorf("Validate(not known, nonzero stream count) = %v, want ErrPayloadInconsistentField", err)
	}
}

func TestAudioPayloadValidateRejectsSinceSetWhenNotKnown(t *testing.T) {
	p := validAudioPayload()
	now := time.Now()
	p.EngineGlitchCountsKnown = false
	p.EngineGlitchCountsSince = &now
	if err := p.Validate(); !errors.Is(err, ErrPayloadInconsistentField) {
		t.Errorf("Validate(not known, since set) = %v, want ErrPayloadInconsistentField", err)
	}
}

func TestAudioPayloadValidateAcceptsKnownGlitchCountsWithSince(t *testing.T) {
	p := validAudioPayload()
	now := time.Now()
	p.EngineGlitchCountsKnown = true
	p.EngineGlitchCountsSince = &now
	p.EngineStreamWarningCount = 3
	p.EngineQosDropCount = 5
	if err := p.Validate(); err != nil {
		t.Errorf("Validate(known, since set, nonzero counts) = %v, want nil", err)
	}
}

func TestNewAudioEnvelopeRejectsInvalidPayload(t *testing.T) {
	bad := validAudioPayload()
	bad.Routes = nil
	if _, err := NewAudioEnvelope(time.Now, "node-1", bad); !errors.Is(err, ErrPayloadMissingField) {
		t.Errorf("NewAudioEnvelope(invalid) = %v, want wrapping ErrPayloadMissingField", err)
	}
}

func TestNewAudioEnvelopeAndDecodeAudioPayloadRoundTrip(t *testing.T) {
	want := validAudioPayload()
	env, err := NewAudioEnvelope(time.Now, "node-1", want)
	if err != nil {
		t.Fatalf("NewAudioEnvelope: %v", err)
	}
	if env.Schema != SchemaNodeAudioV1 {
		t.Errorf("env.Schema = %q, want %q", env.Schema, SchemaNodeAudioV1)
	}

	got, err := DecodeAudioPayload(env)
	if err != nil {
		t.Fatalf("DecodeAudioPayload: %v", err)
	}
	if got.OutputsCount != want.OutputsCount || len(got.Routes) != len(want.Routes) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if got.ObservedAt == nil || !got.ObservedAt.Equal(*want.ObservedAt) {
		t.Errorf("round trip ObservedAt = %v, want %v", got.ObservedAt, want.ObservedAt)
	}
}

func TestDecodeAudioPayloadWrongSchema(t *testing.T) {
	env, err := NewRenderEnvelope(time.Now, "node-1", RenderPayload{Surfaces: []RenderSurfaceReport{}})
	if err != nil {
		t.Fatalf("NewRenderEnvelope: %v", err)
	}
	_, err = DecodeAudioPayload(env)
	var unsupported *UnsupportedSchemaError
	if !errors.As(err, &unsupported) {
		t.Errorf("DecodeAudioPayload(wrong schema) = %v, want *UnsupportedSchemaError", err)
	}
}

func TestDecodeAudioPayloadEmpty(t *testing.T) {
	env := Envelope{Schema: SchemaNodeAudioV1, NodeID: "node-1", MessageID: "m1", SentAt: time.Now()}
	_, err := DecodeAudioPayload(env)
	if !errors.Is(err, ErrPayloadEmpty) {
		t.Errorf("DecodeAudioPayload(empty payload) = %v, want ErrPayloadEmpty", err)
	}
}

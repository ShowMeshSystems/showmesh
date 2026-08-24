package mqttproto

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// This file covers F8: NewRenderEnvelope must refuse to build an envelope
// carrying a RenderPayload its own decoder (DecodeRenderPayload, via
// RenderPayload.Validate) would reject, rather than publishing it and
// letting the receiver discover the defect.

// TestNewRenderEnvelopeRejectsInvalidPayload proves the producer-side guard:
// a non-running surface with an empty Reason must never make it into a
// built envelope.
func TestNewRenderEnvelopeRejectsInvalidPayload(t *testing.T) {
	bad := RenderPayload{
		Surfaces: []RenderSurfaceReport{
			{
				SurfaceID:     "surface-1",
				PipelineState: RenderPipelineStateStarting,
				Reason:        "", // invalid: required whenever state != running
				ObservedAt:    time.Now(),
			},
		},
	}

	_, err := NewRenderEnvelope(time.Now, "node-1", bad)
	if err == nil {
		t.Fatalf("NewRenderEnvelope returned no error for a payload RenderPayload.Validate rejects")
	}
	if !errors.Is(err, ErrPayloadMissingField) {
		t.Fatalf("error = %v, want wrapping ErrPayloadMissingField", err)
	}
}

// TestNewRenderEnvelopeRejectsFalseTransportAvailableWithNoReason proves
// TransportReason's identical required-whenever-false rule (Track B seam
// B4): a surface reporting TransportAvailable=false with an empty
// TransportReason must never make it into a built envelope, matching
// Reason's rule for PipelineState one field up.
func TestNewRenderEnvelopeRejectsFalseTransportAvailableWithNoReason(t *testing.T) {
	available := false
	bad := RenderPayload{
		Surfaces: []RenderSurfaceReport{
			{
				SurfaceID:          "surface-1",
				PipelineState:      RenderPipelineStateRunning,
				Transport:          "ndi",
				TransportAvailable: &available,
				TransportReason:    "", // invalid: required whenever transportAvailable is false
				ObservedAt:         time.Now(),
			},
		},
	}

	_, err := NewRenderEnvelope(time.Now, "node-1", bad)
	if err == nil {
		t.Fatalf("NewRenderEnvelope returned no error for a payload RenderPayload.Validate rejects")
	}
	if !errors.Is(err, ErrPayloadMissingField) {
		t.Fatalf("error = %v, want wrapping ErrPayloadMissingField", err)
	}
}

// TestNewRenderEnvelopeAcceptsValidPayload proves the guard does not
// misfire on an ordinary valid payload (running with no reason, and a
// non-running state with a reason).
func TestNewRenderEnvelopeAcceptsValidPayload(t *testing.T) {
	good := RenderPayload{
		GstLaunchAvailable: true,
		Surfaces: []RenderSurfaceReport{
			{
				SurfaceID:     "surface-1",
				PipelineState: RenderPipelineStateRunning,
				Reason:        "",
				ObservedAt:    time.Now(),
			},
			{
				SurfaceID:     "surface-2",
				PipelineState: RenderPipelineStateStarting,
				Reason:        "pipeline started; PLAYING not yet observed",
				ObservedAt:    time.Now(),
			},
		},
	}

	env, err := NewRenderEnvelope(time.Now, "node-1", good)
	if err != nil {
		t.Fatalf("NewRenderEnvelope returned an error for a valid payload: %v", err)
	}
	if _, err := DecodeRenderPayload(env); err != nil {
		t.Fatalf("DecodeRenderPayload on the built envelope: %v", err)
	}
}

// TestRenderPayloadValidateRejectsNilSurfaces proves the absent/null vs
// explicitly-empty distinction this field's own doc comment claims but
// nothing previously enforced: a zero-value RenderPayload (Surfaces == nil,
// exactly what an absent or literal-null "surfaces" key decodes to) must be
// refused, never silently treated as "this node holds no surfaces" —
// CLAUDE.md's most-repeated defect shape in this codebase.
func TestRenderPayloadValidateRejectsNilSurfaces(t *testing.T) {
	p := RenderPayload{GstLaunchAvailable: true, Surfaces: nil}
	err := p.Validate()
	if err == nil {
		t.Fatalf("Validate() returned no error for a nil Surfaces payload")
	}
	if !errors.Is(err, ErrPayloadMissingField) {
		t.Fatalf("error = %v, want wrapping ErrPayloadMissingField", err)
	}
}

// TestRenderPayloadValidateAcceptsExplicitlyEmptySurfaces is
// TestRenderPayloadValidateRejectsNilSurfaces's counterpart: a node that
// holds no assignment sends a real, explicit "surfaces":[], and that must
// keep validating exactly as it always has.
func TestRenderPayloadValidateAcceptsExplicitlyEmptySurfaces(t *testing.T) {
	p := RenderPayload{GstLaunchAvailable: true, Surfaces: []RenderSurfaceReport{}}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() returned an error for an explicit, empty Surfaces slice: %v", err)
	}
}

// TestRenderPayloadValidateRejectsIdleWithNoIdleMode proves finding 7's
// wire-side rule: a surface reporting Drawing == RenderDrawingIdle with an
// empty IdleMode must be refused, matching Reason's and TransportReason's
// identical required-whenever-the-flag-says-so pattern one field up.
func TestRenderPayloadValidateRejectsIdleWithNoIdleMode(t *testing.T) {
	p := RenderPayload{
		Surfaces: []RenderSurfaceReport{
			{
				SurfaceID:     "surface-1",
				PipelineState: RenderPipelineStateRunning,
				Drawing:       RenderDrawingIdle,
				IdleMode:      "", // invalid: required whenever drawing is "idle"
				ObservedAt:    time.Now(),
			},
		},
	}
	err := p.Validate()
	if err == nil {
		t.Fatalf("Validate() returned no error for drawing=idle with an empty idleMode")
	}
	if !errors.Is(err, ErrPayloadMissingField) {
		t.Fatalf("error = %v, want wrapping ErrPayloadMissingField", err)
	}
}

// TestRenderPayloadValidateRejectsFailureWithNoFailureOutput proves the
// failure drawing state carries IdleMode's identical rule: a report that
// says a surface failed must also say what that failure put on the wire,
// because "alert" and "black" look nothing alike to the audience.
func TestRenderPayloadValidateRejectsFailureWithNoFailureOutput(t *testing.T) {
	p := RenderPayload{
		Surfaces: []RenderSurfaceReport{
			{
				SurfaceID:     "surface-1",
				PipelineState: RenderPipelineStateRunning,
				Drawing:       RenderDrawingFailure,
				FailureOutput: "", // invalid: required whenever drawing is "failure"
				ObservedAt:    time.Now(),
			},
		},
	}
	err := p.Validate()
	if err == nil {
		t.Fatalf("Validate() returned no error for drawing=failure with an empty failureOutput")
	}
	if !errors.Is(err, ErrPayloadMissingField) {
		t.Fatalf("error = %v, want wrapping ErrPayloadMissingField", err)
	}
}

// TestRenderPayloadValidateAcceptsFailureDrawing proves the third drawing
// state is a legal report, with no idleMode: a coverage-gap failure is not
// an idle cycle, and reporting it as one is what let a broken assignment
// read as normal.
func TestRenderPayloadValidateAcceptsFailureDrawing(t *testing.T) {
	for _, out := range []string{RenderFailureOutputAlert, RenderFailureOutputBlack} {
		p := RenderPayload{
			Surfaces: []RenderSurfaceReport{
				{
					SurfaceID:     "surface-1",
					PipelineState: RenderPipelineStateRunning,
					Drawing:       RenderDrawingFailure,
					FailureOutput: out,
					ObservedAt:    time.Now(),
				},
			},
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("Validate() with failureOutput %q returned an error: %v", out, err)
		}
	}
}

// TestRenderPayloadValidateRejectsUnrecognizedFailureOutput proves
// FailureOutput's own closed vocabulary is enforced rather than merely
// documented, the same way Drawing's is below.
func TestRenderPayloadValidateRejectsUnrecognizedFailureOutput(t *testing.T) {
	p := RenderPayload{
		Surfaces: []RenderSurfaceReport{
			{
				SurfaceID:     "surface-1",
				PipelineState: RenderPipelineStateRunning,
				Drawing:       RenderDrawingFailure,
				FailureOutput: "magenta",
				ObservedAt:    time.Now(),
			},
		},
	}
	if err := p.Validate(); err == nil {
		t.Fatalf("Validate() returned no error for an unrecognized failureOutput")
	}
}

// TestRenderPayloadValidateRejectsUnrecognizedDrawing proves Drawing's
// closed vocabulary is actually enforced, not merely documented.
func TestRenderPayloadValidateRejectsUnrecognizedDrawing(t *testing.T) {
	p := RenderPayload{
		Surfaces: []RenderSurfaceReport{
			{
				SurfaceID:     "surface-1",
				PipelineState: RenderPipelineStateRunning,
				Drawing:       "sideways", // not content, idle, or empty
				ObservedAt:    time.Now(),
			},
		},
	}
	err := p.Validate()
	if err == nil {
		t.Fatalf("Validate() returned no error for an unrecognized drawing value")
	}
	if !errors.Is(err, ErrPayloadInvalidDrawing) {
		t.Fatalf("error = %v, want wrapping ErrPayloadInvalidDrawing", err)
	}
}

// TestDecodeRenderPayloadRejectsAbsentSurfacesKey drives the defect at the
// wire, not just through the Go struct: a hand-built JSON object with no
// "surfaces" key at all (unreachable from this package's own constructors,
// which always emit the key — see RenderPayload.Surfaces's doc comment —
// but reachable from any other producer, malicious or buggy) must be
// refused by DecodeRenderPayload, not silently decoded as "this node holds
// no surfaces".
func TestDecodeRenderPayloadRejectsAbsentSurfacesKey(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"gstLaunchPath":      "/usr/bin/gst-launch-1.0",
		"gstLaunchAvailable": true,
		// "surfaces" deliberately omitted.
	})
	if err != nil {
		t.Fatalf("marshal raw payload: %v", err)
	}
	env, err := newEnvelope(time.Now, SchemaNodeRenderV1, "node-1", json.RawMessage(raw))
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	env.Payload = raw // newEnvelope would otherwise re-marshal the RawMessage; keep it exact.

	_, err = DecodeRenderPayload(env)
	if err == nil {
		t.Fatalf("DecodeRenderPayload returned no error for a payload with no \"surfaces\" key")
	}
	if !errors.Is(err, ErrPayloadMissingField) {
		t.Fatalf("error = %v, want wrapping ErrPayloadMissingField", err)
	}
}

// TestDecodeRenderPayloadRejectsNullSurfacesKey is
// TestDecodeRenderPayloadRejectsAbsentSurfacesKey's sibling: a literal
// `"surfaces": null` must be refused identically, not treated as "no
// surfaces" either.
func TestDecodeRenderPayloadRejectsNullSurfacesKey(t *testing.T) {
	raw := []byte(`{"gstLaunchPath":"","gstLaunchAvailable":true,"surfaces":null}`)
	env, err := newEnvelope(time.Now, SchemaNodeRenderV1, "node-1", json.RawMessage(raw))
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	env.Payload = raw

	_, err = DecodeRenderPayload(env)
	if err == nil {
		t.Fatalf("DecodeRenderPayload returned no error for a literal \"surfaces\": null")
	}
	if !errors.Is(err, ErrPayloadMissingField) {
		t.Fatalf("error = %v, want wrapping ErrPayloadMissingField", err)
	}
}

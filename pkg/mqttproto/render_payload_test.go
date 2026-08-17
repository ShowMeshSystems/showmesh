package mqttproto

import (
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

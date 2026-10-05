package noderender

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// TestSurfaceSubjectAbsentIsNotApplicable pins every surface signal ADR-056
// moved: its state and reason when its subject does not exist, the report
// time it carries, and that it goes stale when the node stops reporting.
func TestSurfaceSubjectAbsentIsNotApplicable(t *testing.T) {
	const noAssignment = "This surface holds no render assignment."
	const noAuthorization = "This surface's assignment carries no catalog authorization."

	cases := []struct {
		name       string
		surface    func(sf *mqttproto.RenderSurfaceReport)
		signals    []observation.SignalID
		observedAt time.Time
		wantReason string
	}{
		{
			name:    "no assignment",
			surface: func(sf *mqttproto.RenderSurfaceReport) {},
			signals: contentSignals, observedAt: sampleContentObservedAt, wantReason: noAssignment,
		},
		{
			name:    "assignment not applied by a cue",
			surface: func(sf *mqttproto.RenderSurfaceReport) { sf.FSEQFilename = "show.fseq" },
			signals: []observation.SignalID{SignalSurfaceContentCueID}, observedAt: sampleContentObservedAt,
			wantReason: "This surface's assignment was not applied by a cue.",
		},
		{
			name:    "assignment with no catalog authorization",
			surface: func(sf *mqttproto.RenderSurfaceReport) { sf.FSEQFilename = "show.fseq" },
			signals: []observation.SignalID{
				SignalSurfaceContentCatalogRevision, SignalSurfaceContentShow, SignalSurfaceContentGeneration,
			},
			observedAt: sampleContentObservedAt, wantReason: noAuthorization,
		},
		{
			name: "drawing idle output",
			surface: func(sf *mqttproto.RenderSurfaceReport) {
				sf.Drawing, sf.IdleMode = mqttproto.RenderDrawingIdle, mqttproto.RenderIdleOutputBlack
			},
			signals: []observation.SignalID{SignalSurfaceTimelinePositionMS}, observedAt: sampleFramesObservedAt,
			wantReason: "This surface is not drawing content, so there is no timeline position.",
		},
		{
			name: "drawing idle output has no failure",
			surface: func(sf *mqttproto.RenderSurfaceReport) {
				sf.Drawing, sf.IdleMode = mqttproto.RenderDrawingIdle, mqttproto.RenderIdleOutputBlack
			},
			signals: []observation.SignalID{SignalSurfaceOutputFailure}, observedAt: sampleFramesObservedAt,
			wantReason: "This surface has not failed to draw a frame.",
		},
		{
			name: "drawing a failure has no idle mode",
			surface: func(sf *mqttproto.RenderSurfaceReport) {
				sf.Drawing, sf.FailureOutput = mqttproto.RenderDrawingFailure, mqttproto.RenderFailureOutputAlert
			},
			signals: []observation.SignalID{SignalSurfaceOutputIdleMode}, observedAt: sampleFramesObservedAt,
			wantReason: "This surface is not drawing idle output.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := samplePayload(mqttproto.RenderPipelineStateRunning)
			payload.Surfaces[0].FSEQFilename, payload.Surfaces[0].CueID = "", ""
			payload.Surfaces[0].CatalogRevision, payload.Surfaces[0].Show = "", ""
			payload.Surfaces[0].ContentObservedAt = sampleContentObservedAt
			c.surface(&payload.Surfaces[0])
			st := NewStore()
			st.Put("render-01", payload, false, time.Now())
			obs, _ := New(st).Poll(context.Background())

			for _, sig := range c.signals {
				got := findObs(t, obs, sig)
				if state := got.StateAt(c.observedAt); state != observation.StateNotApplicable {
					t.Errorf("%s state = %q, want %q", sig, state, observation.StateNotApplicable)
				}
				if got.Reason != c.wantReason {
					t.Errorf("%s reason = %q, want %q", sig, got.Reason, c.wantReason)
				}
				if got.ObservedAt == nil || !got.ObservedAt.Equal(c.observedAt) {
					t.Errorf("%s observedAt = %v, want the report's own %s", sig, got.ObservedAt, c.observedAt)
				}
				if state := got.StateAt(c.observedAt.Add(DefaultValidFor + time.Second)); state != observation.StateStale {
					t.Errorf("%s state after the node went quiet = %q, want %q", sig, state, observation.StateStale)
				}
			}
		})
	}
}

// TestSurfaceMissingReadingStaysNotCollected is the other half: a surface
// whose assignment the node could not read still owes a reading.
func TestSurfaceMissingReadingStaysNotCollected(t *testing.T) {
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].FSEQFilename = ""
	payload.Surfaces[0].ContentIdentityReason = "persisted assignment is malformed"
	payload.Surfaces[0].ContentObservedAt = sampleContentObservedAt
	st := NewStore()
	st.Put("render-01", payload, false, time.Now())
	obs, _ := New(st).Poll(context.Background())

	for _, sig := range contentSignals {
		got := findObs(t, obs, sig)
		if got.Absence != observation.StateNotCollected || got.Reason != "persisted assignment is malformed" {
			t.Errorf("%s = %q (%q), want %q with the node's reason", sig, got.Absence, got.Reason, observation.StateNotCollected)
		}
	}
}

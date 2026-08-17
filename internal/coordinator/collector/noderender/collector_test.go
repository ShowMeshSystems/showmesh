package noderender

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func boolPtr(b bool) *bool { return &b }

func samplePayload(state string) mqttproto.RenderPayload {
	return mqttproto.RenderPayload{
		GstLaunchPath:      "/usr/bin/gst-launch-1.0",
		GstLaunchAvailable: true,
		Surfaces: []mqttproto.RenderSurfaceReport{
			{
				SurfaceID:           "garage",
				PipelineState:       state,
				Reason:              "",
				Since:               time.Unix(1000, 0).UTC(),
				RestartCount:        2,
				ConsecutiveFailures: 0,
				FramesWritten:       120,
				FramesLate:          3,
				FramesDropped:       1,
				Transport:           "ndi",
				TransportAvailable:  boolPtr(true),
				ObservedAt:          time.Unix(2000, 0).UTC(),
			},
		},
	}
}

func findObs(t *testing.T, obs []observation.Observation, sig observation.SignalID) observation.Observation {
	t.Helper()
	for _, o := range obs {
		if o.Signal == sig {
			return o
		}
	}
	t.Fatalf("no observation found for signal %q among %d observations", sig, len(obs))
	return observation.Observation{}
}

// TestPollLiveDeliveryUsesReceiptTime proves the live branch of buildValue:
// a non-retained report's ObservedAt is the coordinator's own receipt time,
// never left nil.
func TestPollLiveDeliveryUsesReceiptTime(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	st.Put("render-01", samplePayload(mqttproto.RenderPipelineStateRunning), false, receivedAt)

	c := New(st)
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll: complete = false, want true")
	}

	state := findObs(t, obs, SignalSurfacePipelineState)
	if state.ObservedAt == nil {
		t.Fatalf("live delivery: ObservedAt is nil, want %s", receivedAt)
	}
	if !state.ObservedAt.Equal(receivedAt) {
		t.Errorf("live delivery: ObservedAt = %s, want %s", state.ObservedAt, receivedAt)
	}
	if state.Value != mqttproto.RenderPipelineStateRunning {
		t.Errorf("pipeline state value = %v, want %q", state.Value, mqttproto.RenderPipelineStateRunning)
	}
	if state.Resource.Kind != observation.ResourceSurface || state.Resource.ID != "garage" {
		t.Errorf("resource = %+v, want kind=surface id=garage", state.Resource)
	}
}

// TestPollRetainedDeliveryIsUnknownAge is this package's own version of
// fppmqtt's identically-named test: a retained delivery must never be
// stamped with a receipt time as though that were when the condition
// became true (ADR-011).
func TestPollRetainedDeliveryIsUnknownAge(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	st.Put("render-01", samplePayload(mqttproto.RenderPipelineStateRunning), true, receivedAt)

	c := New(st)
	obs, _ := c.Poll(context.Background())

	state := findObs(t, obs, SignalSurfacePipelineState)
	if state.ObservedAt != nil {
		t.Errorf("retained delivery: ObservedAt = %s, want nil (unknown age)", state.ObservedAt)
	}
	if state.StateAt(receivedAt) != observation.StateUnknownAge {
		t.Errorf("retained delivery: StateAt = %s, want unknown_age", state.StateAt(receivedAt))
	}
}

// TestPollTransportUnprobedIsNotCollected proves the contract's sharpest
// rule for this seam: a nil TransportAvailable must render as
// not_collected, never as available or unavailable.
func TestPollTransportUnprobedIsNotCollected(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].TransportAvailable = nil
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	transport := findObs(t, obs, SignalSurfaceTransportAvailable)
	if transport.Absence != observation.StateNotCollected {
		t.Errorf("unprobed transport: Absence = %q, want %q", transport.Absence, observation.StateNotCollected)
	}
	if transport.Value != nil {
		t.Errorf("unprobed transport: Value = %v, want nil", transport.Value)
	}
	if transport.Reason == "" {
		t.Errorf("unprobed transport: Reason is empty, want a stated reason (absent evidence must be stated, never omitted)")
	}
}

// TestPollProbedTransportRendersBool proves the other half of the same
// rule: once B4 has actually probed, the result is a real bool, not stuck
// permanently at not_collected.
func TestPollProbedTransportRendersBool(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].TransportAvailable = boolPtr(false)
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	transport := findObs(t, obs, SignalSurfaceTransportAvailable)
	if transport.Absence != "" {
		t.Errorf("probed transport: Absence = %q, want empty", transport.Absence)
	}
	if v, ok := transport.Value.(bool); !ok || v != false {
		t.Errorf("probed transport: Value = %v, want false", transport.Value)
	}
}

// TestPollStaleIsNeverHealthy proves ADR-011's core rule survives this
// package specifically: a live report that has aged past DefaultValidFor
// must report StateStale, never current.
func TestPollStaleIsNeverHealthy(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	st.Put("render-01", samplePayload(mqttproto.RenderPipelineStateRunning), false, receivedAt)

	c := New(st)
	obs, _ := c.Poll(context.Background())
	state := findObs(t, obs, SignalSurfacePipelineState)

	fresh := receivedAt.Add(DefaultValidFor - time.Second)
	if state.StateAt(fresh) != observation.StateCurrent {
		t.Errorf("just before ValidFor elapses: StateAt = %s, want current", state.StateAt(fresh))
	}

	stale := receivedAt.Add(DefaultValidFor + time.Second)
	if state.StateAt(stale) != observation.StateStale {
		t.Errorf("after ValidFor elapses: StateAt = %s, want stale", state.StateAt(stale))
	}
}

// TestPollNoSurfacesProducesNoObservations proves a node with no surface
// assignment (or GstLaunchAvailable=false and Surfaces empty) reports
// nothing, rather than fabricating a surface out of thin air.
func TestPollNoSurfacesProducesNoObservations(t *testing.T) {
	st := NewStore()
	st.Put("render-01", mqttproto.RenderPayload{GstLaunchAvailable: false, Surfaces: nil}, false, time.Now())

	c := New(st)
	obs, complete := c.Poll(context.Background())
	if len(obs) != 0 {
		t.Errorf("Poll with no surfaces: got %d observations, want 0", len(obs))
	}
	if !complete {
		t.Errorf("Poll: complete = false, want true")
	}
}

// TestPollUnknownNodeProducesNoObservations proves Poll never invents
// evidence for a node that has never published.
func TestPollUnknownNodeProducesNoObservations(t *testing.T) {
	st := NewStore()
	c := New(st)
	obs, complete := c.Poll(context.Background())
	if len(obs) != 0 {
		t.Errorf("Poll on empty store: got %d observations, want 0", len(obs))
	}
	if !complete {
		t.Errorf("Poll: complete = false, want true")
	}
}

// TestNodeRenderObservationsMatchesPoll proves the read-time path
// (Store.NodeRenderObservations, the node-view synthesis this package's
// doc comment promises) renders the identical evidence Poll would for the
// same node, so GET /api/v1/nodes/{id} and GET /api/v1/observations can
// never show two different answers for the same underlying report.
func TestNodeRenderObservationsMatchesPoll(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	st.Put("render-01", samplePayload(mqttproto.RenderPipelineStateFailed), false, receivedAt)
	st.Put("render-02", samplePayload(mqttproto.RenderPipelineStateRunning), true, receivedAt)

	c := New(st)
	polled, _ := c.Poll(context.Background())

	fromRead01 := st.NodeRenderObservations("render-01")
	fromRead02 := st.NodeRenderObservations("render-02")

	if len(fromRead01)+len(fromRead02) != len(polled) {
		t.Fatalf("read-time total = %d, Poll total = %d, want equal", len(fromRead01)+len(fromRead02), len(polled))
	}

	state01 := findObs(t, fromRead01, SignalSurfacePipelineState)
	if state01.Value != mqttproto.RenderPipelineStateFailed {
		t.Errorf("render-01 state = %v, want %q", state01.Value, mqttproto.RenderPipelineStateFailed)
	}
	state02 := findObs(t, fromRead02, SignalSurfacePipelineState)
	if state02.ObservedAt != nil {
		t.Errorf("render-02 (retained): ObservedAt = %s, want nil", state02.ObservedAt)
	}
}

// TestNodeRenderObservationsUnknownNodeReturnsNil proves a node that has
// never published render evidence is nil, not a fabricated empty-but-real
// report.
func TestNodeRenderObservationsUnknownNodeReturnsNil(t *testing.T) {
	st := NewStore()
	if got := st.NodeRenderObservations("never-seen"); got != nil {
		t.Errorf("NodeRenderObservations(unknown node) = %v, want nil", got)
	}
}

// TestReasonAndCountsAreExposed is a broad sanity check that every signal
// in the contract's list is actually produced with the right value, not
// just the two spotlighted above.
func TestReasonAndCountsAreExposed(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateFailed)
	payload.Surfaces[0].Reason = "gst-launch-1.0 exited 1"
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	cases := map[observation.SignalID]any{
		SignalSurfaceReason:              "gst-launch-1.0 exited 1",
		SignalSurfaceRestartCount:        int64(2),
		SignalSurfaceConsecutiveFailures: int64(0),
		SignalSurfaceFramesWritten:       int64(120),
		SignalSurfaceFramesLate:          int64(3),
		SignalSurfaceFramesDropped:       int64(1),
	}
	for sig, want := range cases {
		got := findObs(t, obs, sig)
		if got.Value != want {
			t.Errorf("%s = %v, want %v", sig, got.Value, want)
		}
	}
}

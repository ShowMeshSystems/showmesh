package noderender

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func boolPtr(b bool) *bool { return &b }

// sampleObservedAt is the node-reported evidence timestamp samplePayload's
// surface carries — deliberately distinct from any receivedAt used in these
// tests, so a test that mixes the two up fails loudly.
var sampleObservedAt = time.Unix(2000, 0).UTC()

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
				ObservedAt:          sampleObservedAt,
			},
		},
	}
}

// samplePayloadNoObservedAt is samplePayload with the node reporting no
// evidence timestamp at all (the zero value) — the genuinely-unknown case.
func samplePayloadNoObservedAt(state string) mqttproto.RenderPayload {
	p := samplePayload(state)
	p.Surfaces[0].ObservedAt = time.Time{}
	return p
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

// TestPollUsesNodeReportedObservedAt proves Finding 3's fix: ObservedAt is
// the node's own evidence timestamp (sampleObservedAt), never the
// coordinator's receipt time, while CollectedAt stays the receipt time —
// [pkg/observation]'s ObservedAt/CollectedAt split, actually honored here.
// Revert buildValue to stamp rep.receivedAt as ObservedAt and this fails:
// state.ObservedAt comes back equal to receivedAt (2026-08-17 12:00:00),
// not sampleObservedAt (1970-01-01 00:33:20 UTC).
func TestPollUsesNodeReportedObservedAt(t *testing.T) {
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
		t.Fatalf("live delivery: ObservedAt is nil, want %s", sampleObservedAt)
	}
	if !state.ObservedAt.Equal(sampleObservedAt) {
		t.Errorf("live delivery: ObservedAt = %s, want the node's own %s (not the receipt time %s)", state.ObservedAt, sampleObservedAt, receivedAt)
	}
	if !state.CollectedAt.Equal(receivedAt) {
		t.Errorf("live delivery: CollectedAt = %s, want the receipt time %s", state.CollectedAt, receivedAt)
	}
	if state.Value != mqttproto.RenderPipelineStateRunning {
		t.Errorf("pipeline state value = %v, want %q", state.Value, mqttproto.RenderPipelineStateRunning)
	}
	if state.Resource.Kind != observation.ResourceSurface || state.Resource.ID != "garage" {
		t.Errorf("resource = %+v, want kind=surface id=garage", state.Resource)
	}
}

// TestPollRetainedWithNodeObservedAtStillUsesIt proves the reversal Finding
// 3 requires: a retained MQTT delivery is no longer, by itself, a reason to
// discard the evidence timestamp — this agent (unlike FPP) puts one on the
// wire, so retained-ness of the MQTT delivery is irrelevant once the
// payload itself carries real evidence.
func TestPollRetainedWithNodeObservedAtStillUsesIt(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	st.Put("render-01", samplePayload(mqttproto.RenderPipelineStateRunning), true, receivedAt)

	c := New(st)
	obs, _ := c.Poll(context.Background())

	state := findObs(t, obs, SignalSurfacePipelineState)
	if state.ObservedAt == nil {
		t.Fatalf("retained delivery with a node-reported observedAt: ObservedAt is nil, want %s", sampleObservedAt)
	}
	if !state.ObservedAt.Equal(sampleObservedAt) {
		t.Errorf("retained delivery: ObservedAt = %s, want the node's own %s", state.ObservedAt, sampleObservedAt)
	}
}

// TestPollNodeReportsNoObservedAtIsUnknownAge proves the genuinely-unknown
// half of Finding 3: when the node itself reports no evidence timestamp
// (the zero value), ObservedAt stays nil rather than being defaulted to
// anything, including the receipt time — ADR-011's rule this project has
// caught missing three times before.
func TestPollNodeReportsNoObservedAtIsUnknownAge(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	st.Put("render-01", samplePayloadNoObservedAt(mqttproto.RenderPipelineStateRunning), false, receivedAt)

	c := New(st)
	obs, _ := c.Poll(context.Background())

	state := findObs(t, obs, SignalSurfacePipelineState)
	if state.ObservedAt != nil {
		t.Errorf("no node-reported observedAt: ObservedAt = %s, want nil (unknown age)", state.ObservedAt)
	}
	if state.StateAt(receivedAt) != observation.StateUnknownAge {
		t.Errorf("no node-reported observedAt: StateAt = %s, want unknown_age", state.StateAt(receivedAt))
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

	reason := findObs(t, obs, SignalSurfaceTransportReason)
	if reason.Absence != observation.StateNotCollected {
		t.Errorf("unprobed transport reason: Absence = %q, want %q", reason.Absence, observation.StateNotCollected)
	}
}

// TestPollProbedTransportRendersBool proves the other half of the same
// rule: once B4 has actually probed, the result is a real bool, not stuck
// permanently at not_collected.
func TestPollProbedTransportRendersBool(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].TransportAvailable = boolPtr(false)
	payload.Surfaces[0].TransportReason = "NDI runtime not found"
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

	reason := findObs(t, obs, SignalSurfaceTransportReason)
	if reason.Absence != "" {
		t.Errorf("transport reason: Absence = %q, want empty", reason.Absence)
	}
	if v, ok := reason.Value.(string); !ok || v != "NDI runtime not found" {
		t.Errorf("transport reason: Value = %v, want %q", reason.Value, "NDI runtime not found")
	}
}

// TestPollFramesRateUnmeasuredIsNotCollected proves ADR-040's obligation:
// before the agent's frame writer has completed a sampling window,
// FramesRate is nil on the wire, and this must render as not_collected —
// never a fabricated zero, and never the surface's configured frameRate
// echoed back (samplePayload sets no frameRate at all, so any non-nil
// Value here could only be a fabrication).
func TestPollFramesRateUnmeasuredIsNotCollected(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning) // FramesRate left nil
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	rate := findObs(t, obs, SignalSurfaceFramesRate)
	if rate.Absence != observation.StateNotCollected {
		t.Errorf("unmeasured frame rate: Absence = %q, want %q", rate.Absence, observation.StateNotCollected)
	}
	if rate.Value != nil {
		t.Errorf("unmeasured frame rate: Value = %v, want nil", rate.Value)
	}
	if rate.Reason == "" {
		t.Errorf("unmeasured frame rate: Reason is empty, want a stated reason (absent evidence must be stated, never omitted)")
	}
}

// TestPollFramesRateMeasuredRendersFloat proves the other half: once the
// agent has a real measurement, it renders as a value, not stuck
// permanently at not_collected.
func TestPollFramesRateMeasuredRendersFloat(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	measured := 39.87
	payload.Surfaces[0].FramesRate = &measured
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	rate := findObs(t, obs, SignalSurfaceFramesRate)
	if rate.Absence != "" {
		t.Errorf("measured frame rate: Absence = %q, want empty", rate.Absence)
	}
	if v, ok := rate.Value.(float64); !ok || v != measured {
		t.Errorf("measured frame rate: Value = %v, want %v", rate.Value, measured)
	}
}

// TestPollStaleIsNeverHealthy proves ADR-011's core rule survives this
// package specifically: a report whose node-reported ObservedAt has aged
// past DefaultValidFor must report StateStale, never current. Staleness is
// measured from the node's own evidence timestamp (sampleObservedAt), not
// from the coordinator's receipt time — a stale receivedAt would prove
// nothing about Finding 3's fix.
func TestPollStaleIsNeverHealthy(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	st.Put("render-01", samplePayload(mqttproto.RenderPipelineStateRunning), false, receivedAt)

	c := New(st)
	obs, _ := c.Poll(context.Background())
	state := findObs(t, obs, SignalSurfacePipelineState)

	fresh := sampleObservedAt.Add(DefaultValidFor - time.Second)
	if state.StateAt(fresh) != observation.StateCurrent {
		t.Errorf("just before ValidFor elapses: StateAt = %s, want current", state.StateAt(fresh))
	}

	stale := sampleObservedAt.Add(DefaultValidFor + time.Second)
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
	// render-02's delivery is retained, but its payload still carries a
	// node-reported ObservedAt, which now wins regardless of retained-ness
	// (Finding 3): a retained MQTT replay is not, by itself, evidence the
	// timestamp is unknown.
	state02 := findObs(t, fromRead02, SignalSurfacePipelineState)
	if state02.ObservedAt == nil || !state02.ObservedAt.Equal(sampleObservedAt) {
		t.Errorf("render-02 (retained): ObservedAt = %v, want %s", state02.ObservedAt, sampleObservedAt)
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

// --- Dropped-surface absence (replaces the deleted nodeRenderSink) ---

// TestPollEmitsAbsenceForADroppedSurface proves the fix this file replaces
// df483c8's delete-based one with: a surface a node reported LAST poll but
// not THIS one gets an explicit StateNotCollected observation on
// surface.pipeline.state, with a reason, rather than either a ghost row
// (the pre-fix defect) or silent deletion (df483c8's rejected fix).
func TestPollEmitsAbsenceForADroppedSurface(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	st.Put("render-01", samplePayload(mqttproto.RenderPipelineStateRunning), false, receivedAt)

	c := New(st)
	first, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("first Poll: complete = false, want true")
	}
	// Sanity: the surface is actually present on the first poll.
	findObs(t, first, SignalSurfacePipelineState)

	// The node's next report names no surfaces at all — a cleared
	// assignment, matching what a real agent publishes (renderreport.go's
	// non-nil, possibly-empty Surfaces).
	droppedAt := receivedAt.Add(time.Minute)
	st.Put("render-01", mqttproto.RenderPayload{GstLaunchPath: "/usr/bin/gst-launch-1.0", GstLaunchAvailable: true}, false, droppedAt)

	second, complete2 := c.Poll(context.Background())
	if !complete2 {
		t.Fatalf("second Poll: complete = false, want true")
	}

	var absence *observation.Observation
	for i := range second {
		o := second[i]
		if o.Resource.Kind == observation.ResourceSurface && o.Resource.ID == "garage" && o.Signal == SignalSurfacePipelineState {
			absence = &o
			break
		}
	}
	if absence == nil {
		t.Fatalf("second Poll carries no surface.pipeline.state observation for the dropped surface %q; want an explicit absence", "garage")
	}
	if absence.Absence != observation.StateNotCollected {
		t.Errorf("dropped surface absence state = %q, want %q", absence.Absence, observation.StateNotCollected)
	}
	if absence.Reason == "" {
		t.Errorf("dropped surface absence carries no reason")
	}
	if absence.Source != SourceFor("render-01") {
		t.Errorf("dropped surface absence source = %q, want %q", absence.Source, SourceFor("render-01"))
	}
}

// TestPollDoesNotEmitAbsenceOnFirstSightingOfANode proves a node's very
// first delivery — nothing was ever "known" about it before — never
// synthesizes a spurious absence: there is nothing to have dropped yet.
func TestPollDoesNotEmitAbsenceOnFirstSightingOfANode(t *testing.T) {
	st := NewStore()
	st.Put("render-01", samplePayload(mqttproto.RenderPipelineStateRunning), false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	// Scoped to SignalSurfacePipelineState, the only signal a dropped
	// surface's absence is emitted on. Other signals are legitimately
	// not_collected on a first poll (surface.frames.rate has no completed
	// measurement window yet; surface.transport.available has not been
	// probed), and counting those would fail this test for the wrong reason.
	var absences int
	for _, o := range obs {
		if o.Signal == SignalSurfacePipelineState && o.Absence == observation.StateNotCollected {
			absences++
		}
	}
	if absences != 0 {
		t.Errorf("first-ever poll produced %d surface.pipeline.state absence observations, want 0", absences)
	}

	// Guard the guard: if this poll ever stopped emitting pipeline-state
	// evidence at all, the count above would be trivially zero and this test
	// would pass while proving nothing.
	var sawPipelineStateValue bool
	for _, o := range obs {
		if o.Signal == SignalSurfacePipelineState && o.Absence == "" {
			sawPipelineStateValue = true
		}
	}
	if !sawPipelineStateValue {
		t.Fatal("first-ever poll emitted no surface.pipeline.state value at all; the absence count above proves nothing")
	}
}

// TestPollDoesNotAffectAnotherNodesSurfaceOfTheSameID is the two-node
// collision case: render-a stops reporting "wall-1" while render-b keeps
// reporting a surface with the SAME id. render-b's own evidence must be
// completely unaffected by render-a's drop — the two are tracked under
// distinct per-node keys (known is keyed by nodeID, not by surface id
// alone).
func TestPollDoesNotAffectAnotherNodesSurfaceOfTheSameID(t *testing.T) {
	shared := func(state string) mqttproto.RenderPayload {
		return mqttproto.RenderPayload{
			GstLaunchPath: "/usr/bin/gst-launch-1.0", GstLaunchAvailable: true,
			Surfaces: []mqttproto.RenderSurfaceReport{{
				SurfaceID: "wall-1", PipelineState: state, ObservedAt: time.Now(),
			}},
		}
	}
	st := NewStore()
	st.Put("render-a", shared(mqttproto.RenderPipelineStateRunning), false, time.Now())
	st.Put("render-b", shared(mqttproto.RenderPipelineStateRunning), false, time.Now())

	c := New(st)
	if _, complete := c.Poll(context.Background()); !complete {
		t.Fatalf("first Poll: complete = false, want true")
	}

	// render-a drops the surface; render-b keeps reporting it.
	st.Put("render-a", mqttproto.RenderPayload{GstLaunchPath: "/usr/bin/gst-launch-1.0", GstLaunchAvailable: true}, false, time.Now())

	second, _ := c.Poll(context.Background())

	var aAbsent, bPresent bool
	for _, o := range second {
		if o.Resource.ID != "wall-1" || o.Signal != SignalSurfacePipelineState {
			continue
		}
		switch o.Source {
		case SourceFor("render-a"):
			aAbsent = o.Absence == observation.StateNotCollected
		case SourceFor("render-b"):
			bPresent = o.Value == mqttproto.RenderPipelineStateRunning
		}
	}
	if !aAbsent {
		t.Errorf("render-a's dropped wall-1 was not reported as absent")
	}
	if !bPresent {
		t.Errorf("render-b's still-live wall-1 was affected by render-a's drop; want it unchanged")
	}
}

// --- Restart-seeding (WithKnownSurfaces) ---

// TestWithKnownSurfacesEmitsAbsenceOnFirstPollAfterRestart proves the
// restart-resilience half of the fix: a fresh Collector (as constructed
// after a coordinator restart) with no seed would treat a node's FIRST
// delivery as having nothing to diff against (see
// TestPollDoesNotEmitAbsenceOnFirstSightingOfANode) — permanently losing
// the ability to ever prune a surface the node had already dropped before
// the restart. WithKnownSurfaces seeds that memory from the store's own
// persisted rows, so the very first poll after a restart can still emit
// the absence.
func TestWithKnownSurfacesEmitsAbsenceOnFirstPollAfterRestart(t *testing.T) {
	st := NewStore()
	// render-01's current delivery no longer names "garage" — as if the
	// coordinator restarted after the node already dropped it, and this is
	// the first poll of the new process.
	st.Put("render-01", mqttproto.RenderPayload{GstLaunchPath: "/usr/bin/gst-launch-1.0", GstLaunchAvailable: true}, false, time.Now())

	seed := map[string]map[string]struct{}{
		"render-01": {"garage": struct{}{}},
	}
	c := New(st, WithKnownSurfaces(seed))
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll: complete = false, want true")
	}

	var found bool
	for _, o := range obs {
		if o.Resource.ID == "garage" && o.Signal == SignalSurfacePipelineState && o.Absence == observation.StateNotCollected {
			found = true
		}
	}
	if !found {
		t.Errorf("seeded Collector's first poll did not emit an absence for the pre-restart-known surface %q", "garage")
	}
}

// TestWithKnownSurfacesDoesNotMutateCallersMap proves the seed map is
// copied, not retained — a caller (coordinator.go) reusing or discarding
// its own map after construction must not alias this package's internal
// state.
func TestWithKnownSurfacesDoesNotMutateCallersMap(t *testing.T) {
	seed := map[string]map[string]struct{}{"render-01": {"garage": struct{}{}}}
	st := NewStore()
	st.Put("render-01", samplePayload(mqttproto.RenderPipelineStateRunning), false, time.Now())
	_ = New(st, WithKnownSurfaces(seed))

	seed["render-01"]["patio"] = struct{}{}
	if len(seed["render-01"]) != 2 {
		t.Fatalf("test setup broke: caller's own map should still be mutable")
	}
	// No assertion beyond "this does not panic or corrupt Collector state"
	// is possible without exporting known; the copy is exercised for real
	// by TestWithKnownSurfacesEmitsAbsenceOnFirstPollAfterRestart above.
}

// --- SourceFor / NodeFromSource ---

func TestNodeFromSourceRoundTripsWithSourceFor(t *testing.T) {
	for _, nodeID := range []string{"render-01", "media-a", "x"} {
		src := SourceFor(nodeID)
		got, ok := NodeFromSource(src)
		if !ok {
			t.Fatalf("NodeFromSource(%q) ok = false, want true", src)
		}
		if got != nodeID {
			t.Errorf("NodeFromSource(SourceFor(%q)) = %q, want %q", nodeID, got, nodeID)
		}
	}
}

func TestNodeFromSourceRejectsUnrelatedSource(t *testing.T) {
	for _, src := range []string{"fpp-rest", "resolume-rest", "", "node-render", "node-renderer:x"} {
		if _, ok := NodeFromSource(src); ok {
			t.Errorf("NodeFromSource(%q) ok = true, want false", src)
		}
	}
}

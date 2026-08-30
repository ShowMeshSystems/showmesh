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

// sampleFramesObservedAt is samplePayload's frame-counter evidence
// timestamp, deliberately distinct from sampleObservedAt (the pipeline
// lifecycle one) so a test that mixes the two up fails loudly. This is the
// timestamp this issue's fix threads through independently of
// sampleObservedAt.
var sampleFramesObservedAt = time.Unix(2200, 0).UTC()

// sampleContentObservedAt is samplePayload's content-identity evidence
// timestamp — deliberately distinct from both sampleObservedAt (pipeline
// lifecycle) and sampleFramesObservedAt (frame counters), so a test that
// mixes any of the three up fails loudly. This is the timestamp behind the
// content-signal regression fixed below: a cue activation swaps the frame
// writer without transitioning PipelineState, so sampleObservedAt alone
// cannot prove the content signals are fresh.
var sampleContentObservedAt = time.Unix(2400, 0).UTC()

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
				FramesObservedAt:    sampleFramesObservedAt,
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
// Renamed from TestPollLiveDeliveryUsesReceiptTime, which asserted the OLD,
// now-wrong behaviour: buildValue used to collapse ObservedAt and
// CollectedAt onto the coordinator's own receipt time, which is exactly the
// evidence-vs-collection conflation ADR-011/ADR-003 forbid. Revert buildValue
// to stamp rep.receivedAt as ObservedAt and this fails: state.ObservedAt
// comes back equal to receivedAt (2026-08-17 12:00:00), not sampleObservedAt
// (1970-01-01 00:33:20 UTC).
func TestPollUsesNodeReportedObservedAt(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	nodeObservedAt := payload.Surfaces[0].ObservedAt
	st.Put("render-01", payload, false, receivedAt)

	c := New(st)
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll: complete = false, want true")
	}

	state := findObs(t, obs, SignalSurfacePipelineState)
	if state.ObservedAt == nil {
		t.Fatalf("live delivery: ObservedAt is nil, want %s", nodeObservedAt)
	}
	if !state.ObservedAt.Equal(nodeObservedAt) {
		t.Errorf("live delivery: ObservedAt = %s, want the node-reported %s (not receivedAt %s)", state.ObservedAt, nodeObservedAt, receivedAt)
	}
	if !state.CollectedAt.Equal(receivedAt) {
		t.Errorf("live delivery: CollectedAt = %s, want the coordinator's own receipt time %s", state.CollectedAt, receivedAt)
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
// measured from ObservedAt (the node-reported evidence timestamp, per
// TestPollUsesNodeReportedObservedAt), not from receivedAt — this test's
// windows are computed against samplePayload's own sf.ObservedAt for
// exactly that reason.
func TestPollStaleIsNeverHealthy(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	nodeObservedAt := payload.Surfaces[0].ObservedAt
	st.Put("render-01", payload, false, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))

	c := New(st)
	obs, _ := c.Poll(context.Background())
	state := findObs(t, obs, SignalSurfacePipelineState)

	fresh := nodeObservedAt.Add(DefaultValidFor - time.Second)
	if state.StateAt(fresh) != observation.StateCurrent {
		t.Errorf("just before ValidFor elapses: StateAt = %s, want current", state.StateAt(fresh))
	}

	stale := nodeObservedAt.Add(DefaultValidFor + time.Second)
	if state.StateAt(stale) != observation.StateStale {
		t.Errorf("after ValidFor elapses: StateAt = %s, want stale", state.StateAt(stale))
	}
}

// TestPollFramesSignalsAgeFromTheirOwnObservedAtNotPipelineState is this
// issue's own regression test: FramesWritten/FramesLate/FramesDropped/
// FramesRate must be judged for staleness against sf.FramesObservedAt, the
// frame writer's own window-close evidence timestamp, never against
// sf.ObservedAt, the pipeline-lifecycle timestamp that only moves on a
// state transition (setState). Before the fix, every render signal shared
// sf.ObservedAt: on a real node, PipelineState stayed "running" (no further
// transitions) while frame counts kept climbing every 15s report, so
// ObservedAt never advanced again and every signal read stale 45s after
// the apply, on a pipeline sustaining 39.997fps. Here, samplePayload gives
// the two timestamps deliberately different values (sampleObservedAt vs.
// sampleFramesObservedAt), and the frame signals' own StateAt windows must
// track sampleFramesObservedAt, not sampleObservedAt.
func TestPollFramesSignalsAgeFromTheirOwnObservedAtNotPipelineState(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	measured := 39.997
	payload.Surfaces[0].FramesRate = &measured
	st.Put("render-01", payload, false, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))

	c := New(st)
	obs, _ := c.Poll(context.Background())

	for _, sig := range []observation.SignalID{
		SignalSurfaceFramesWritten,
		SignalSurfaceFramesLate,
		SignalSurfaceFramesDropped,
		SignalSurfaceFramesRate,
	} {
		o := findObs(t, obs, sig)

		fresh := sampleFramesObservedAt.Add(DefaultValidFor - time.Second)
		if o.StateAt(fresh) != observation.StateCurrent {
			t.Errorf("%s just before its own ValidFor elapses: StateAt = %s, want current", sig, o.StateAt(fresh))
		}

		stale := sampleFramesObservedAt.Add(DefaultValidFor + time.Second)
		if o.StateAt(stale) != observation.StateStale {
			t.Errorf("%s after its own ValidFor elapses: StateAt = %s, want stale", sig, o.StateAt(stale))
		}

		// A window pinned at sampleObservedAt (the DIFFERENT, older
		// pipeline-lifecycle timestamp) would already have gone stale by
		// sampleObservedAt+DefaultValidFor+1s. Prove the frame signal does
		// NOT use that timestamp: it must still read current there,
		// because sampleFramesObservedAt is later.
		pipelineWindowStale := sampleObservedAt.Add(DefaultValidFor + time.Second)
		if o.StateAt(pipelineWindowStale) != observation.StateCurrent {
			t.Errorf("%s at pipeline-state's own stale boundary (%s): StateAt = %s, want current, this signal must age from its own FramesObservedAt, not sf.ObservedAt",
				sig, pipelineWindowStale, o.StateAt(pipelineWindowStale))
		}
	}
}

// TestPollFramesZeroObservedAtIsUnknownAge proves ADR-011's converse this
// issue must respect: until the frame writer's first sampling window
// closes, sf.FramesObservedAt is the zero value, and the four frame
// signals must render as unknown age, never "now", and never borrowing
// sf.ObservedAt (which may itself be set, from an earlier pipeline
// transition) as a substitute.
func TestPollFramesZeroObservedAtIsUnknownAge(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].FramesObservedAt = time.Time{}
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	for _, sig := range []observation.SignalID{
		SignalSurfaceFramesWritten,
		SignalSurfaceFramesLate,
		SignalSurfaceFramesDropped,
	} {
		o := findObs(t, obs, sig)
		if o.ObservedAt != nil {
			t.Errorf("%s: ObservedAt = %v, want nil (FramesObservedAt is zero: age is genuinely unknown)", sig, o.ObservedAt)
		}
		if o.StateAt(time.Now()) != observation.StateUnknownAge {
			t.Errorf("%s: StateAt(now) = %s, want unknown_age", sig, o.StateAt(time.Now()))
		}
	}
}

// TestPollNoSurfacesProducesNoSurfaceObservations proves a node with no
// surface assignment (or GstLaunchAvailable=false and Surfaces empty)
// reports no SURFACE observations, rather than fabricating a surface out
// of thin air. Renamed from TestPollNoSurfacesProducesNoObservations: this
// node still produces its two node.multisync.* observations (finding 7),
// since a MultiSync bind status exists independently of whether any
// surface is currently assigned — those are asserted separately as
// StateNotCollected here (MultiSyncObservedAt is zero in this fixture,
// meaning this node has never reported a real bind outcome).
func TestPollNoSurfacesProducesNoSurfaceObservations(t *testing.T) {
	st := NewStore()
	st.Put("render-01", mqttproto.RenderPayload{GstLaunchAvailable: false, Surfaces: nil}, false, time.Now())

	c := New(st)
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Errorf("Poll: complete = false, want true")
	}
	for _, o := range obs {
		if o.Resource.Kind == observation.ResourceSurface {
			t.Errorf("Poll with no surfaces produced a surface observation: %+v, want none", o)
		}
	}

	listening := findObs(t, obs, SignalNodeMultiSyncListening)
	if listening.Resource.Kind != observation.ResourceNode || listening.Resource.ID != "render-01" {
		t.Errorf("node.multisync.listening resource = %+v, want kind=node id=render-01", listening.Resource)
	}
	if listening.Absence != observation.StateNotCollected {
		t.Errorf("node.multisync.listening: Absence = %q, want %q (this node has never reported a real bind outcome)", listening.Absence, observation.StateNotCollected)
	}
	reason := findObs(t, obs, SignalNodeMultiSyncReason)
	if reason.Absence != observation.StateNotCollected {
		t.Errorf("node.multisync.reason: Absence = %q, want %q", reason.Absence, observation.StateNotCollected)
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

// TestPollFailureDrawingRendersFailureOutput proves the coverage-gap
// failure reaches an operator's dashboard as a failure with the output it
// actually drew, not as an idle cycle: a node whose assignment is broken
// used to report drawing=idle with idleMode=black, which is exactly what a
// healthy surface reports.
func TestPollFailureDrawingRendersFailureOutput(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].Drawing = mqttproto.RenderDrawingFailure
	payload.Surfaces[0].FailureOutput = mqttproto.RenderFailureOutputAlert
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	mode := findObs(t, obs, SignalSurfaceOutputMode)
	if v, ok := mode.Value.(string); !ok || v != mqttproto.RenderDrawingFailure {
		t.Errorf("surface.output.mode: Value = %v, want %q", mode.Value, mqttproto.RenderDrawingFailure)
	}

	failure := findObs(t, obs, SignalSurfaceOutputFailure)
	if failure.Absence != "" {
		t.Errorf("surface.output.failure: Absence = %q, want empty", failure.Absence)
	}
	if v, ok := failure.Value.(string); !ok || v != mqttproto.RenderFailureOutputAlert {
		t.Errorf("surface.output.failure: Value = %v, want %q", failure.Value, mqttproto.RenderFailureOutputAlert)
	}

	idle := findObs(t, obs, SignalSurfaceOutputIdleMode)
	if idle.Absence != observation.StateNotCollected {
		t.Errorf("surface.output.idle_mode during a failure: Absence = %q, want %q", idle.Absence, observation.StateNotCollected)
	}
}

// TestPollIdleDrawingLeavesFailureOutputNotCollected is the counterpart: a
// healthy idle states that the failure signal does not apply rather than
// fabricating a value for it.
func TestPollIdleDrawingLeavesFailureOutputNotCollected(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].Drawing = mqttproto.RenderDrawingIdle
	payload.Surfaces[0].IdleMode = mqttproto.RenderIdleOutputBlack
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	failure := findObs(t, obs, SignalSurfaceOutputFailure)
	if failure.Absence != observation.StateNotCollected {
		t.Errorf("surface.output.failure while idle: Absence = %q, want %q", failure.Absence, observation.StateNotCollected)
	}
	if failure.Reason == "" {
		t.Errorf("surface.output.failure while idle: Reason is empty, want a stated reason")
	}

	idle := findObs(t, obs, SignalSurfaceOutputIdleMode)
	if v, ok := idle.Value.(string); !ok || v != mqttproto.RenderIdleOutputBlack {
		t.Errorf("surface.output.idle_mode while idle: Value = %v, want %q", idle.Value, mqttproto.RenderIdleOutputBlack)
	}
}

// TestPollContentIdentityProducesAllFourSignals proves a surface reporting
// full content identity (an assignment applied by a cue activation, with a
// catalog authorization tuple) renders all four surface.content.* signals
// as real values, the whole point: a content swap provable from the
// node's own evidence, not inferred from pipelineState or frame counters.
func TestPollContentIdentityProducesAllFourSignals(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].FSEQFilename = "halloween-01.fseq"
	payload.Surfaces[0].FSEQContentHash = "sha256:deadbeef"
	payload.Surfaces[0].CueID = "cue-42"
	payload.Surfaces[0].CatalogRevision = "rev-7"
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	filename := findObs(t, obs, SignalSurfaceContentFSEQFilename)
	if filename.Absence != "" {
		t.Errorf("surface.content.fseq_filename: Absence = %q, want empty", filename.Absence)
	}
	if v, ok := filename.Value.(string); !ok || v != "halloween-01.fseq" {
		t.Errorf("surface.content.fseq_filename: Value = %v, want %q", filename.Value, "halloween-01.fseq")
	}

	hash := findObs(t, obs, SignalSurfaceContentFSEQContentHash)
	if hash.Absence != "" {
		t.Errorf("surface.content.fseq_content_hash: Absence = %q, want empty", hash.Absence)
	}
	if v, ok := hash.Value.(string); !ok || v != "sha256:deadbeef" {
		t.Errorf("surface.content.fseq_content_hash: Value = %v, want %q", hash.Value, "sha256:deadbeef")
	}

	cueID := findObs(t, obs, SignalSurfaceContentCueID)
	if cueID.Absence != "" {
		t.Errorf("surface.content.cue_id: Absence = %q, want empty", cueID.Absence)
	}
	if v, ok := cueID.Value.(string); !ok || v != "cue-42" {
		t.Errorf("surface.content.cue_id: Value = %v, want %q", cueID.Value, "cue-42")
	}

	revision := findObs(t, obs, SignalSurfaceContentCatalogRevision)
	if revision.Absence != "" {
		t.Errorf("surface.content.catalog_revision: Absence = %q, want empty", revision.Absence)
	}
	if v, ok := revision.Value.(string); !ok || v != "rev-7" {
		t.Errorf("surface.content.catalog_revision: Value = %v, want %q", revision.Value, "rev-7")
	}
}

// TestPollContentIdentityProducesShowAndGeneration proves: a surface
// reporting a full authorization tuple emits surface.content.show
// and surface.content.generation too, stamped from the same
// ContentObservedAt evidence time as the other four content signals.
func TestPollContentIdentityProducesShowAndGeneration(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].FSEQFilename = "halloween-01.fseq"
	payload.Surfaces[0].FSEQContentHash = "sha256:deadbeef"
	payload.Surfaces[0].CueID = "cue-42"
	payload.Surfaces[0].CatalogRevision = "rev-7"
	payload.Surfaces[0].Show = "halloween-2026"
	payload.Surfaces[0].Generation = 1
	payload.Surfaces[0].ContentObservedAt = sampleContentObservedAt
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	show := findObs(t, obs, SignalSurfaceContentShow)
	if show.Absence != "" {
		t.Errorf("surface.content.show: Absence = %q, want empty", show.Absence)
	}
	if v, ok := show.Value.(string); !ok || v != "halloween-2026" {
		t.Errorf("surface.content.show: Value = %v, want %q", show.Value, "halloween-2026")
	}
	if show.ObservedAt == nil || !show.ObservedAt.Equal(sampleContentObservedAt) {
		t.Errorf("surface.content.show: ObservedAt = %v, want %v (content evidence time, not pipeline state's)", show.ObservedAt, sampleContentObservedAt)
	}

	generation := findObs(t, obs, SignalSurfaceContentGeneration)
	if generation.Absence != "" {
		t.Errorf("surface.content.generation: Absence = %q, want empty", generation.Absence)
	}
	if v, ok := generation.Value.(int64); !ok || v != 1 {
		t.Errorf("surface.content.generation: Value = %v, want 1", generation.Value)
	}
}

// TestPollNoContentIdentityLeavesShowAndGenerationNotCollected extends
// TestPollNoContentIdentityIsNotCollected to the Show/generation signals: a
// surface holding no assignment at all must state absence for these too,
// never a fabricated "" or 0.
func TestPollNoContentIdentityLeavesShowAndGenerationNotCollected(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	for _, sig := range []observation.SignalID{SignalSurfaceContentShow, SignalSurfaceContentGeneration} {
		o := findObs(t, obs, sig)
		if o.Absence != observation.StateNotCollected {
			t.Errorf("%s: Absence = %q, want %q", sig, o.Absence, observation.StateNotCollected)
		}
		if o.Reason == "" {
			t.Errorf("%s: Reason is empty, want a stated reason", sig)
		}
	}
}

// TestPollNoContentIdentityIsNotCollected proves a surface reporting no
// FSEQ at all (the field mqttproto's own doc comment states means "no
// assignment held") renders all four content signals as an explicit
// NotCollected absence, never a fabricated or stale value.
func TestPollNoContentIdentityIsNotCollected(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	// samplePayload's surface already carries no FSEQFilename/CueID/etc:
	// this is the zero-value "no assignment held" case.
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	for _, sig := range []observation.SignalID{
		SignalSurfaceContentFSEQFilename,
		SignalSurfaceContentFSEQContentHash,
		SignalSurfaceContentCueID,
		SignalSurfaceContentCatalogRevision,
	} {
		o := findObs(t, obs, sig)
		if o.Absence != observation.StateNotCollected {
			t.Errorf("%s: Absence = %q, want %q", sig, o.Absence, observation.StateNotCollected)
		}
		if o.Reason == "" {
			t.Errorf("%s: Reason is empty, want a stated reason", sig)
		}
	}
}

// TestPollWithheldContentIdentityStatesTheNodesReasonNotNoAssignment proves
// that a surface whose node withheld a real but malformed persisted
// assignment (mqttproto.RenderSurfaceReport.ContentIdentityReason set,
// FSEQFilename left empty by internal/agent/renderreport.go's
// applyContentIdentity) reports the node's own reason on all six
// content-identity signals, never the "this surface holds no render
// assignment" text that TestPollNoContentIdentityIsNotCollected's true
// no-assignment case gets — that text would tell an operator this surface
// holds no assignment when it actually holds a broken one.
func TestPollWithheldContentIdentityStatesTheNodesReasonNotNoAssignment(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	const withheldReason = "persisted assignment has a fseqFilename but no fseqContentHash (hand-edited or pre-content-identity-contract assignments.json); content identity withheld"
	payload.Surfaces[0].ContentIdentityReason = withheldReason
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	for _, sig := range []observation.SignalID{
		SignalSurfaceContentFSEQFilename,
		SignalSurfaceContentFSEQContentHash,
		SignalSurfaceContentCueID,
		SignalSurfaceContentCatalogRevision,
		SignalSurfaceContentShow,
		SignalSurfaceContentGeneration,
	} {
		o := findObs(t, obs, sig)
		if o.Absence != observation.StateNotCollected {
			t.Errorf("%s: Absence = %q, want %q", sig, o.Absence, observation.StateNotCollected)
		}
		if o.Reason != withheldReason {
			t.Errorf("%s: Reason = %q, want the node's own withheld-identity reason %q", sig, o.Reason, withheldReason)
		}
	}
}

// TestPollContentIdentityWithoutCueLeavesCueIDNotCollected proves a
// surface carrying real content (a filename, hash, and catalog revision)
// but no cue id (a direct render.surface.apply with no cue activation
// involved) reports the filename/hash/catalog revision as real values
// while stating cue_id as not applicable, mirroring
// SignalSurfaceOutputIdleMode's identical "only meaningful when a
// condition holds" pattern one signal family over.
func TestPollContentIdentityWithoutCueLeavesCueIDNotCollected(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].FSEQFilename = "test-pattern-01.fseq"
	payload.Surfaces[0].FSEQContentHash = "sha256:cafef00d"
	// CueID and CatalogRevision left "": a direct apply with no cue and no
	// authorization tuple.
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	filename := findObs(t, obs, SignalSurfaceContentFSEQFilename)
	if filename.Absence != "" {
		t.Errorf("surface.content.fseq_filename: Absence = %q, want empty", filename.Absence)
	}

	cueID := findObs(t, obs, SignalSurfaceContentCueID)
	if cueID.Absence != observation.StateNotCollected {
		t.Errorf("surface.content.cue_id without a cue activation: Absence = %q, want %q", cueID.Absence, observation.StateNotCollected)
	}

	revision := findObs(t, obs, SignalSurfaceContentCatalogRevision)
	if revision.Absence != observation.StateNotCollected {
		t.Errorf("surface.content.catalog_revision without an auth tuple: Absence = %q, want %q", revision.Absence, observation.StateNotCollected)
	}
}

// TestPollContentSignalsAgeFromTheirOwnObservedAtNotPipelineState is
// this issue's own regression test. Before the fix, the four surface.content.*
// signals were stamped with sf.ObservedAt — the pipeline-lifecycle
// timestamp, which only moves when PipelineState transitions
// (pipeline.runner.setState). A cue activation swaps the frame writer
// WITHOUT transitioning pipeline state, so the content identity changes
// while sf.ObservedAt sits frozen at whatever it was before the swap: on a
// bench run this reported a content signal with an evidence time 1m41s
// BEFORE the node's own appliedAt for that content, and every healthy
// surface read stale because the 45s validity window was measured from
// that unrelated timestamp.
//
// samplePayload gives sampleObservedAt and sampleContentObservedAt
// deliberately different values, with sampleObservedAt the OLDER of the
// two by more than DefaultValidFor (45s): a surface whose pipeline-state
// evidence is stale but whose content was read fresh must still report its
// four content signals as CURRENT. This must fail against unmodified code,
// where the content signals are judged against sf.ObservedAt (already
// stale by construction here) instead of sf.ContentObservedAt.
func TestPollContentSignalsAgeFromTheirOwnObservedAtNotPipelineState(t *testing.T) {
	st := NewStore()
	payload := samplePayload(mqttproto.RenderPipelineStateRunning)
	payload.Surfaces[0].FSEQFilename = "halloween-01.fseq"
	payload.Surfaces[0].FSEQContentHash = "sha256:deadbeef"
	payload.Surfaces[0].CueID = "cue-42"
	payload.Surfaces[0].CatalogRevision = "rev-7"
	payload.Surfaces[0].ContentObservedAt = sampleContentObservedAt
	st.Put("render-01", payload, false, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	// checkAt is chosen so it is PAST sampleObservedAt+DefaultValidFor (the
	// pipeline-lifecycle evidence has gone stale) but still BEFORE
	// sampleContentObservedAt+DefaultValidFor (the content evidence is
	// still fresh) — exactly the "old ObservedAt, fresh content read"
	// scenario this issue describes.
	checkAt := sampleObservedAt.Add(DefaultValidFor + time.Minute)
	if !checkAt.Before(sampleContentObservedAt.Add(DefaultValidFor)) {
		t.Fatalf("test fixture bug: checkAt %s is not before sampleContentObservedAt+DefaultValidFor %s",
			checkAt, sampleContentObservedAt.Add(DefaultValidFor))
	}

	pipelineState := findObs(t, obs, SignalSurfacePipelineState)
	if pipelineState.StateAt(checkAt) != observation.StateStale {
		t.Fatalf("test fixture bug: surface.pipeline_state at checkAt (%s) = %s, want stale (sampleObservedAt must have aged out)",
			checkAt, pipelineState.StateAt(checkAt))
	}

	for _, sig := range []observation.SignalID{
		SignalSurfaceContentFSEQFilename,
		SignalSurfaceContentFSEQContentHash,
		SignalSurfaceContentCueID,
		SignalSurfaceContentCatalogRevision,
	} {
		o := findObs(t, obs, sig)
		if o.StateAt(checkAt) != observation.StateCurrent {
			t.Errorf("%s at checkAt (%s), with pipeline-state evidence stale but content read fresh: StateAt = %s, want current — this signal must age from its own ContentObservedAt, not sf.ObservedAt",
				sig, checkAt, o.StateAt(checkAt))
		}

		if o.ObservedAt == nil {
			t.Fatalf("%s: ObservedAt is nil, want %s", sig, sampleContentObservedAt)
		}
		if !o.ObservedAt.Equal(sampleContentObservedAt) {
			t.Errorf("%s: ObservedAt = %s, want the node-reported sampleContentObservedAt %s (not sampleObservedAt %s)",
				sig, o.ObservedAt, sampleContentObservedAt, sampleObservedAt)
		}
	}
}

// TestPollContentObservedAtPostDatesAContentChange proves the content
// signal's ObservedAt tracks a genuine content change (a cue activation
// applying a new FSEQ) rather than predating it: this issue's own defect
// was a content signal reporting an evidence time BEFORE the node's
// appliedAt for that content. Two consecutive polls simulate a cue
// activation between them; the second poll's content signals must carry an
// ObservedAt no earlier than the moment the new content was reported.
func TestPollContentObservedAtPostDatesAContentChange(t *testing.T) {
	st := NewStore()

	before := samplePayload(mqttproto.RenderPipelineStateRunning)
	before.Surfaces[0].FSEQFilename = "pre-show-loop.fseq"
	before.Surfaces[0].FSEQContentHash = "sha256:aaaa"
	before.Surfaces[0].ContentObservedAt = time.Unix(1000, 0).UTC()
	st.Put("render-01", before, false, time.Now())

	c := New(st)
	firstObs, _ := c.Poll(context.Background())
	firstFilename := findObs(t, firstObs, SignalSurfaceContentFSEQFilename)
	if firstFilename.ObservedAt == nil || !firstFilename.ObservedAt.Equal(time.Unix(1000, 0).UTC()) {
		t.Fatalf("pre-activation: ObservedAt = %v, want %s", firstFilename.ObservedAt, time.Unix(1000, 0).UTC())
	}

	// The cue activation itself: PipelineState does NOT transition (still
	// running), only the content identity changes — this is exactly the
	// case that produced the defect, since sf.ObservedAt alone would never
	// move here.
	activatedAt := time.Unix(1200, 0).UTC()
	after := samplePayload(mqttproto.RenderPipelineStateRunning)
	after.Surfaces[0].FSEQFilename = "halloween-01.fseq"
	after.Surfaces[0].FSEQContentHash = "sha256:deadbeef"
	after.Surfaces[0].CueID = "cue-42"
	after.Surfaces[0].ContentObservedAt = activatedAt
	st.Put("render-01", after, false, time.Now())

	secondObs, _ := c.Poll(context.Background())
	secondFilename := findObs(t, secondObs, SignalSurfaceContentFSEQFilename)
	if secondFilename.Value != "halloween-01.fseq" {
		t.Fatalf("post-activation: Value = %v, want %q", secondFilename.Value, "halloween-01.fseq")
	}
	if secondFilename.ObservedAt == nil {
		t.Fatalf("post-activation: ObservedAt is nil, want %s", activatedAt)
	}
	if secondFilename.ObservedAt.Before(activatedAt) {
		t.Errorf("post-activation: ObservedAt = %s predates the content change at %s, want it to post-date the change",
			secondFilename.ObservedAt, activatedAt)
	}
	if !secondFilename.ObservedAt.Equal(activatedAt) {
		t.Errorf("post-activation: ObservedAt = %s, want %s (the node's own content-identity read time)", secondFilename.ObservedAt, activatedAt)
	}
}

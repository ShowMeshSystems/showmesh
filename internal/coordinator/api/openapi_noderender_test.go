package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/noderender"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Track B seam B2b's own conformance coverage, following
// openapi_showobjects_test.go's exact pattern one seam over: every real
// response is driven through REAL handlers over a REAL *store.Store and a
// REAL noderender.Store, never a hand-built JSON fixture. It proves this
// seam's own claim — a render report reaches GET /api/v1/observations and
// GET /api/v1/nodes/{nodeId} through the paths those endpoints already
// have. The end-to-end claim against a real MQTT broker and a real agent
// process is proved separately (see this seam's own report: TRACK-B
// verification notes), not by this in-process test.

// storeObservationLister mirrors internal/coordinator/apiwiring.go's own
// adapter of the identical name — duplicated here because this package
// cannot import internal/coordinator (which itself imports this package)
// and cannot import another package's _test.go helpers (see auth_test.go's
// doc comment for that same standing rule).
type storeObservationLister struct{ st *store.Store }

func (l storeObservationLister) ListObservations(ctx context.Context, filter ObservationFilter) ([]observation.Observation, error) {
	var sf store.ObservationFilter
	if filter.ResourceKind != nil {
		sf.ResourceKind = *filter.ResourceKind
	}
	if filter.ResourceID != nil {
		sf.ResourceID = *filter.ResourceID
	}
	if filter.Signal != nil {
		sf.Signal = *filter.Signal
	}
	return l.st.ListObservations(ctx, sf)
}

// TestNodeRenderObservationsReachRealAPI is the real-handlers half of this
// seam's conformance coverage. It exercises exactly the write path
// coordinator.go wires in production — noderender.Store.Put (what
// inventory.Manager.handleRender calls), noderender.Collector.Poll (what
// collector.Runner calls on its own cadence), and
// store.Store.ReplaceObservations (what fppSink.RecordObservations calls)
// — then asserts the result through two REAL handlers: GET
// /api/v1/observations?resourceKind=surface and GET /api/v1/nodes/{id}.
func TestNodeRenderObservationsReachRealAPI(t *testing.T) {
	c := newOpenAPICompiler(t)

	_, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))

	renderStore := noderender.NewStore()
	receivedAt := testNow.Add(-time.Second)
	renderStore.Put("render-01", mqttproto.RenderPayload{
		GstLaunchAvailable: true,
		Surfaces: []mqttproto.RenderSurfaceReport{
			{
				SurfaceID:           "garage",
				PipelineState:       mqttproto.RenderPipelineStateRunning,
				Since:               receivedAt,
				RestartCount:        1,
				ConsecutiveFailures: 0,
				FramesWritten:       500,
				FramesLate:          2,
				FramesDropped:       0,
				Transport:           "ndi",
				ObservedAt:          receivedAt,
			},
		},
	}, false, receivedAt)

	// Exactly what collector.Runner would deliver to fppSink on this
	// collector's next poll — see coordinator.go's
	// fppRunner.Add(noderender.New(renderStore), ...).
	renderCollector := noderender.New(renderStore)
	obs, complete := renderCollector.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll: complete = false, want true")
	}
	if err := st.ReplaceObservations(context.Background(), obs); err != nil {
		t.Fatalf("ReplaceObservations: %v", err)
	}

	nv := inventory.NodeView{
		NodeID:      "render-01",
		FirstSeenAt: receivedAt,
		UpdatedAt:   receivedAt,
		Liveness:    inventory.LivenessOnline,
	}
	deps := Dependencies{
		Nodes:        &fakeNodeLister{views: []inventory.NodeView{nv}},
		Observations: storeObservationLister{st: st},
		Render:       renderStore,
	}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// --- GET /api/v1/observations?resourceKind=surface ---
	_, obsBody := doRequest(t, api.Handler, "GET", "/api/v1/observations?resourceKind=surface", nil)
	assertMatchesSchema(t, c, "ObservationsResponse", obsBody)
	if got := string(obsBody); !strings.Contains(got, `"kind":"surface"`) || !strings.Contains(got, `"id":"garage"`) {
		t.Errorf("GET /observations?resourceKind=surface body does not mention surface/garage: %s", got)
	}
	if !strings.Contains(string(obsBody), `"surface.pipeline.state"`) {
		t.Errorf("GET /observations?resourceKind=surface body does not carry surface.pipeline.state: %s", obsBody)
	}

	// --- GET /api/v1/nodes/{nodeId} carries the same evidence under render ---
	resp, nodeBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-01", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /nodes/render-01: status = %d, want 200; body: %s", resp.StatusCode, nodeBody)
	}
	assertMatchesSchema(t, c, "NodeResponse", nodeBody)
	if !strings.Contains(string(nodeBody), `"surface.pipeline.state"`) {
		t.Errorf("GET /nodes/render-01 body does not carry surface.pipeline.state under render: %s", nodeBody)
	}
	if !strings.Contains(string(nodeBody), `"id":"garage"`) {
		t.Errorf("GET /nodes/render-01 body does not name surface garage: %s", nodeBody)
	}

	// --- a node that never published render evidence renders render: [] ---
	quiet := inventory.NodeView{NodeID: "quiet-01", FirstSeenAt: receivedAt, UpdatedAt: receivedAt, Liveness: inventory.LivenessOnline}
	deps.Nodes = &fakeNodeLister{views: []inventory.NodeView{quiet}}
	api2 := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	_, quietBody := doRequest(t, api2.Handler, "GET", "/api/v1/nodes/quiet-01", nil)
	assertMatchesSchema(t, c, "NodeResponse", quietBody)
	if !strings.Contains(string(quietBody), `"render":[]`) {
		t.Errorf("GET /nodes/quiet-01 (no render ever published) body = %s, want an empty render array, never omitted", quietBody)
	}
}

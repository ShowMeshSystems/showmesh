package noderender

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Collector implements collector.Collector; enforced at compile time so a
// signature drift between the two packages is caught here, matching
// fppmqtt.Collector's identical assertion.
var _ collector.Collector = (*Collector)(nil)

// Collector renders [Store]'s current push cache into observations on a
// collector.Runner's own cadence. The zero value is not usable; construct
// with [New].
type Collector struct {
	store *Store
}

// New builds a Collector reading from store.
func New(store *Store) *Collector {
	return &Collector{store: store}
}

// ID returns [SourceName].
func (c *Collector) ID() string { return SourceName }

// Poll renders every node's currently stored report into observations. It
// never touches the network — see the package doc comment — so it always
// returns complete=true, exactly like fppmqtt.Collector.Poll: every call
// renders the FULL current set of every surface every stored report
// currently names, so a surface dropped from a node's next report (a
// cleared assignment) is correctly pruned by [collector.Sink]'s completeness
// contract rather than left behind as a ghost row.
func (c *Collector) Poll(_ context.Context) ([]observation.Observation, bool) {
	snap := c.store.snapshot()
	var obs []observation.Observation
	for nodeID, rep := range snap {
		obs = append(obs, surfaceObservations(nodeID, rep)...)
	}
	return obs, true
}

// NodeRenderObservations returns every surface.* observation this
// coordinator currently holds for nodeID's most recently reported render
// assignment, or nil if nodeID has never published one. This is the node
// read path's synthesize-at-read-time counterpart to internal/coordinator/
// api/mapping.go's nodeEvidenceObservations: it renders the SAME cache
// [Collector.Poll] does, on demand, for a single node, without waiting for
// or duplicating a poll cycle — the identical relationship [Store] bears to
// both.
func (s *Store) NodeRenderObservations(nodeID string) []observation.Observation {
	rep, ok := s.get(nodeID)
	if !ok {
		return nil
	}
	return surfaceObservations(nodeID, rep)
}

// surfaceObservations renders one node's report into observations, one
// [mqttproto.RenderSurfaceReport] at a time. nodeID is accepted for
// error-message context only — the resource this package observes is the
// surface (ADR-026: a surface, not the node running it, is the thing being
// observed), never the node.
func surfaceObservations(nodeID string, rep report) []observation.Observation {
	obs := make([]observation.Observation, 0, len(rep.payload.Surfaces)*len(AllSignalIDs))
	for _, sf := range rep.payload.Surfaces {
		obs = append(obs, surfaceReportObservations(nodeID, sf, rep)...)
	}
	return obs
}

func surfaceReportObservations(nodeID string, sf mqttproto.RenderSurfaceReport, rep report) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceSurface, ID: sf.SurfaceID}

	obs := []observation.Observation{
		buildValue(nodeID, res, SignalSurfacePipelineState, sf.PipelineState, rep),
		buildValue(nodeID, res, SignalSurfaceReason, sf.Reason, rep),
		buildValue(nodeID, res, SignalSurfaceRestartCount, sf.RestartCount, rep),
		buildValue(nodeID, res, SignalSurfaceConsecutiveFailures, sf.ConsecutiveFailures, rep),
		buildValue(nodeID, res, SignalSurfaceFramesWritten, sf.FramesWritten, rep),
		buildValue(nodeID, res, SignalSurfaceFramesLate, sf.FramesLate, rep),
		buildValue(nodeID, res, SignalSurfaceFramesDropped, sf.FramesDropped, rep),
	}

	// FramesRate is nil whenever the frame writer has not yet completed a
	// full sampling window (see pipeline.FrameWriter.sampleRate's doc
	// comment) — ADR-040's obligation is real achieved-rate evidence, so an
	// unmeasured rate is NotCollected, never a fabricated zero and never
	// the surface's configured frameRate echoed back.
	if sf.FramesRate == nil {
		obs = append(obs, notCollected(res, SignalSurfaceFramesRate,
			"frame rate has not yet been measured for this surface (no completed sampling window)", rep.receivedAt))
	} else {
		obs = append(obs, buildValue(nodeID, res, SignalSurfaceFramesRate, *sf.FramesRate, rep))
	}

	// TransportAvailable is nil whenever this surface's transport has never
	// been probed — an ndi surface only gets a value once render.surface.
	// apply's own pipeline start or a render.transport.probe command has
	// actually run [pipeline.ProbeNDISend] against it (internal/agent/
	// renderops.go), and an hdmi surface has no probe at all yet. An
	// unprobed transport is [observation.StateNotCollected], never rendered
	// as available or unavailable. See [observation.MeasuredUnknownAge]'s
	// ADR-011 rule applied one layer up: nil here means "no attempt", not
	// "unknown value of a known attempt", so this is NotCollected
	// regardless of rep.retained, unlike every other field above.
	if sf.TransportAvailable == nil {
		reason := "transport availability has not been probed for this surface"
		obs = append(obs,
			notCollected(res, SignalSurfaceTransportAvailable, reason, rep.receivedAt),
			notCollected(res, SignalSurfaceTransportReason, reason, rep.receivedAt),
		)
	} else {
		obs = append(obs,
			buildValue(nodeID, res, SignalSurfaceTransportAvailable, *sf.TransportAvailable, rep),
			buildValue(nodeID, res, SignalSurfaceTransportReason, sf.TransportReason, rep),
		)
	}

	return obs
}

// buildValue is where this package's own version of ADR-011's retained/live
// rule is enforced, for every value-bearing signal it produces — the
// identical shape fppmqtt.Collector.buildObservation uses one package over:
//
//   - rep.retained: [observation.MeasuredUnknownAge]. ObservedAt is nil,
//     never rep.receivedAt.
//   - live: [observation.Measured] with rep.receivedAt as ObservedAt — the
//     moment this collector actually recorded the delivery.
//
// CollectedAt is rep.receivedAt in both branches: that is when this
// package's cache actually recorded the evidence (Store.Put), not the later
// moment Poll happens to run — Poll only ever renders a cache, it does not
// itself collect anything.
func buildValue(nodeID string, res observation.ResourceRef, sig observation.SignalID, value any, rep report) observation.Observation {
	opts := []observation.Option{
		observation.WithSource(SourceName),
		observation.WithCollectedAt(rep.receivedAt),
	}

	if rep.retained {
		o, err := observation.MeasuredUnknownAge(res, sig, value, opts...)
		if err != nil {
			return failed(res, sig, internalErrorReason(nodeID, err), rep.receivedAt)
		}
		return o
	}

	opts = append(opts, observation.WithValidFor(DefaultValidFor))
	o, err := observation.Measured(res, sig, value, rep.receivedAt, opts...)
	if err != nil {
		return failed(res, sig, internalErrorReason(nodeID, err), rep.receivedAt)
	}
	return o
}

func failed(res observation.ResourceRef, sig observation.SignalID, reason string, at time.Time) observation.Observation {
	o, err := observation.CollectionFailed(res, sig, reason,
		observation.WithSource(SourceName), observation.WithCollectedAt(at))
	if err != nil {
		// reason is always non-empty and res/sig are always set by every
		// call site in this package; a failure here is a bug in this file,
		// not a runtime condition to degrade from gracefully — matching
		// fppmqtt.Collector.failedAt's identical panic.
		panic(fmt.Sprintf("noderender: CollectionFailed(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

func notCollected(res observation.ResourceRef, sig observation.SignalID, reason string, at time.Time) observation.Observation {
	o, err := observation.NotCollected(res, sig, reason,
		observation.WithSource(SourceName), observation.WithCollectedAt(at))
	if err != nil {
		panic(fmt.Sprintf("noderender: NotCollected(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

func internalErrorReason(nodeID string, err error) string {
	return fmt.Sprintf("internal error building observation for node %s: %v", nodeID, err)
}

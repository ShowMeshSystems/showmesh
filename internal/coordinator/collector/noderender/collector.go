package noderender

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
// collector.Runner's own cadence. It also remembers, per node, which
// surface ids that node's most recent delivery named (known) — see
// [Collector.Poll] for why. The zero value is not usable; construct with
// [New].
type Collector struct {
	store *Store

	mu    sync.Mutex
	known map[string]map[string]struct{} // nodeID -> surface ids named by its last delivery
}

// Option configures a [Collector] at construction. See [WithKnownSurfaces].
type Option func(*Collector)

// WithKnownSurfaces seeds a fresh Collector's per-node memory of which
// surface ids it has previously reported, so a coordinator restart does not
// forget every surface a still-running node reported before the restart —
// see [Collector.Poll]'s doc comment for why that memory exists. The
// caller (internal/coordinator/coordinator.go) builds known from the
// store's own persisted rows, keyed by node id via [NodeFromSource]. known
// is copied; the caller's map is not retained.
func WithKnownSurfaces(known map[string]map[string]struct{}) Option {
	return func(c *Collector) {
		for nodeID, ids := range known {
			cp := make(map[string]struct{}, len(ids))
			for id := range ids {
				cp[id] = struct{}{}
			}
			c.known[nodeID] = cp
		}
	}
}

// New builds a Collector reading from store, applying opts in order.
func New(store *Store, opts ...Option) *Collector {
	c := &Collector{store: store, known: make(map[string]map[string]struct{})}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ID returns [SourceName] — this collector's identity for the shared
// collector.Runner it registers on, unaffected by [SourceFor]'s per-node
// disambiguation of individual observations (see that function's doc
// comment).
func (c *Collector) ID() string { return SourceName }

// Poll renders every node's currently stored report into observations, plus
// an explicit absence for every surface a node reported LAST call but not
// THIS one. It never touches the network — see the package doc comment —
// so it always returns complete=true, exactly like fppmqtt.Collector.Poll.
//
// The absence step exists because [collector.Sink]'s ordinary completeness
// contract (store.Store.ReplaceObservations) prunes stale signals only
// within a (resource, source) pair actually present in a delivery; a
// surface named in no observation at all this call is, by that method's own
// doc comment, "left completely untouched" and would survive as a ghost row
// forever. This package earns the fix a different way than deleting: for a
// surface a node's last report named but this one doesn't, it emits ONE
// explicit [observation.StateNotCollected] observation on
// SignalSurfacePipelineState for that (surface, node) pair — which makes
// the resource present in this delivery, so ReplaceObservations' own
// per-signal pruning removes the other now-undelivered surface.* signals
// for it. The persisted row states absence with a reason (ADR-011,
// ADR-020), rather than vanishing.
//
// known is scoped per node (see [SourceFor]): two nodes reporting the same
// surface id never overwrite each other's memory or each other's absence
// claim.
func (c *Collector) Poll(_ context.Context) ([]observation.Observation, bool) {
	snap := c.store.snapshot()
	var obs []observation.Observation

	c.mu.Lock()
	defer c.mu.Unlock()

	for nodeID, rep := range snap {
		obs = append(obs, surfaceObservations(nodeID, rep)...)
		obs = append(obs, nodeMultiSyncObservations(nodeID, rep)...)

		cur := make(map[string]struct{}, len(rep.payload.Surfaces))
		for _, sf := range rep.payload.Surfaces {
			cur[sf.SurfaceID] = struct{}{}
		}
		for id := range c.known[nodeID] {
			if _, stillPresent := cur[id]; stillPresent {
				continue
			}
			res := observation.ResourceRef{Kind: observation.ResourceSurface, ID: id}
			reason := fmt.Sprintf("node %s no longer reports this surface", nodeID)
			obs = append(obs, notCollected(res, SignalSurfacePipelineState, SourceFor(nodeID), reason, rep.receivedAt))
		}
		c.known[nodeID] = cur
	}

	return obs, true
}

// NodeRenderObservations returns every surface.* AND node.multisync.*
// observation this coordinator currently holds for nodeID's most recently
// reported render assignment, or nil if nodeID has never published one.
// This is the node read path's synthesize-at-read-time counterpart to
// internal/coordinator/api/mapping.go's nodeEvidenceObservations: it
// renders the SAME cache [Collector.Poll] does, on demand, for a single
// node, without waiting for or duplicating a poll cycle — the identical
// relationship [Store] bears to both. It never renders the dropped-surface
// absence [Collector.Poll] synthesizes, since that is inherently a
// cross-poll diff this per-node on-demand read has no memory of.
func (s *Store) NodeRenderObservations(nodeID string) []observation.Observation {
	rep, ok := s.get(nodeID)
	if !ok {
		return nil
	}
	obs := surfaceObservations(nodeID, rep)
	obs = append(obs, nodeMultiSyncObservations(nodeID, rep)...)
	return obs
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

	// sf.ObservedAt is THIS surface's own evidence timestamp — the node's
	// clock at the moment the supervisor actually sampled this report
	// (runner.setState/setFrameCounts/setDrawState — internal/agent/
	// pipeline/supervisor.go), distinct from rep.receivedAt (this
	// coordinator's own receipt/bookkeeping time, which stays CollectedAt
	// via buildValue). Using it as ObservedAt is what makes "a fresh
	// ObservedAt means the state actually moved" true all the way to the
	// observation layer, not just inside the agent's own Supervisor —
	// review fix, finding 2/finding 7.
	observedAt := sf.ObservedAt

	obs := []observation.Observation{
		buildValue(nodeID, res, SignalSurfacePipelineState, sf.PipelineState, observedAt, rep),
		buildValue(nodeID, res, SignalSurfaceReason, sf.Reason, observedAt, rep),
		buildValue(nodeID, res, SignalSurfaceRestartCount, sf.RestartCount, observedAt, rep),
		buildValue(nodeID, res, SignalSurfaceConsecutiveFailures, sf.ConsecutiveFailures, observedAt, rep),
		buildValue(nodeID, res, SignalSurfaceFramesWritten, sf.FramesWritten, observedAt, rep),
		buildValue(nodeID, res, SignalSurfaceFramesLate, sf.FramesLate, observedAt, rep),
		buildValue(nodeID, res, SignalSurfaceFramesDropped, sf.FramesDropped, observedAt, rep),
	}

	// FramesRate is nil whenever the frame writer has not yet completed a
	// full sampling window (see pipeline.FrameWriter.sampleRate's doc
	// comment) — ADR-040's obligation is real achieved-rate evidence, so an
	// unmeasured rate is NotCollected, never a fabricated zero and never
	// the surface's configured frameRate echoed back.
	if sf.FramesRate == nil {
		obs = append(obs, notCollected(res, SignalSurfaceFramesRate, SourceFor(nodeID),
			"frame rate has not yet been measured for this surface (no completed sampling window)", rep.receivedAt))
	} else {
		obs = append(obs, buildValue(nodeID, res, SignalSurfaceFramesRate, *sf.FramesRate, observedAt, rep))
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
			notCollected(res, SignalSurfaceTransportAvailable, SourceFor(nodeID), reason, rep.receivedAt),
			notCollected(res, SignalSurfaceTransportReason, SourceFor(nodeID), reason, rep.receivedAt),
		)
	} else {
		obs = append(obs,
			buildValue(nodeID, res, SignalSurfaceTransportAvailable, *sf.TransportAvailable, observedAt, rep),
			buildValue(nodeID, res, SignalSurfaceTransportReason, sf.TransportReason, observedAt, rep),
		)
	}

	obs = append(obs, surfaceDrawStateObservations(nodeID, res, sf, observedAt, rep)...)

	return obs
}

// surfaceDrawStateObservations renders finding 7's four new fields: what
// this surface is actually drawing, not just whether its pipeline reports
// "running" (build contract: "the process is up" is not "frames are
// arriving somewhere"). sf.Drawing == "" means this surface has no active
// FrameWriter at all — a Track B seam B2a test-pattern-only pipeline with
// no FSEQ assigned — so all four signals are NotCollected together with
// one reason, rather than a fabricated empty string or zero position.
func surfaceDrawStateObservations(nodeID string, res observation.ResourceRef, sf mqttproto.RenderSurfaceReport, observedAt time.Time, rep report) []observation.Observation {
	if sf.Drawing == "" {
		reason := "this surface has no active frame writer (e.g. a test-pattern-only pipeline with no FSEQ assigned)"
		return []observation.Observation{
			notCollected(res, SignalSurfaceTimelineState, SourceFor(nodeID), reason, rep.receivedAt),
			notCollected(res, SignalSurfaceTimelinePositionMS, SourceFor(nodeID), reason, rep.receivedAt),
			notCollected(res, SignalSurfaceOutputMode, SourceFor(nodeID), reason, rep.receivedAt),
			notCollected(res, SignalSurfaceOutputIdleMode, SourceFor(nodeID), reason, rep.receivedAt),
		}
	}

	obs := []observation.Observation{
		buildValue(nodeID, res, SignalSurfaceTimelineState, sf.TimelineState, observedAt, rep),
		buildValue(nodeID, res, SignalSurfaceOutputMode, sf.Drawing, observedAt, rep),
	}

	// TimelinePositionMS is nil whenever the writer is drawing idle output
	// instead of content — a position is not meaningful there (see
	// pipeline.Snapshot.TimelinePositionMS's own doc comment) — never a
	// fabricated zero or the last content position echoed back.
	if sf.TimelinePositionMS == nil {
		obs = append(obs, notCollected(res, SignalSurfaceTimelinePositionMS, SourceFor(nodeID),
			"no timeline position while drawing idle output", rep.receivedAt))
	} else {
		obs = append(obs, buildValue(nodeID, res, SignalSurfaceTimelinePositionMS, *sf.TimelinePositionMS, observedAt, rep))
	}

	// IdleMode is only meaningful while Drawing=="idle" — absent (stated,
	// never a fabricated "") while drawing content, mirroring
	// SignalSurfaceTransportReason's identical required-whenever pattern.
	if sf.Drawing == mqttproto.RenderDrawingIdle {
		obs = append(obs, buildValue(nodeID, res, SignalSurfaceOutputIdleMode, sf.IdleMode, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSurfaceOutputIdleMode, SourceFor(nodeID),
			"not applicable while drawing content", rep.receivedAt))
	}

	return obs
}

// nodeMultiSyncObservations renders the two node-level signals from
// [mqttproto.RenderPayload.MultiSyncListening]/MultiSyncReason. Resource
// kind is [observation.ResourceNode], deliberately not surface: one
// MultiSync listener serves every surface a node supervises, so attributing
// its failure to a surface would report one fact N times and imply N
// independent faults.
//
// rep.payload.MultiSyncObservedAt.IsZero() means this node has never
// determined a real outcome yet (either it predates this field, or its
// listener goroutine has not made its first bind attempt) — NotCollected,
// never a fabricated "not listening" reported as though it were evidence.
func nodeMultiSyncObservations(nodeID string, rep report) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}

	if rep.payload.MultiSyncObservedAt.IsZero() {
		reason := "this node has not reported multisync listener status yet"
		return []observation.Observation{
			notCollected(res, SignalNodeMultiSyncListening, SourceFor(nodeID), reason, rep.receivedAt),
			notCollected(res, SignalNodeMultiSyncReason, SourceFor(nodeID), reason, rep.receivedAt),
		}
	}

	observedAt := rep.payload.MultiSyncObservedAt
	return []observation.Observation{
		buildValue(nodeID, res, SignalNodeMultiSyncListening, rep.payload.MultiSyncListening, observedAt, rep),
		buildValue(nodeID, res, SignalNodeMultiSyncReason, rep.payload.MultiSyncReason, observedAt, rep),
	}
}

// buildValue is where this package's own version of ADR-011's retained/live
// rule is enforced, for every value-bearing signal it produces — the
// identical shape fppmqtt.Collector.buildObservation uses one package over,
// EXTENDED (review fix, finding 2/finding 7): observedAt is now the caller's
// own per-field, NODE-REPORTED evidence timestamp (sf.ObservedAt for a
// surface field, rep.payload.MultiSyncObservedAt for a node field) rather
// than always defaulting to this coordinator's own receipt time. The render
// pipeline is unlike a raw FPP MQTT topic: every field this package renders
// already carries a real "when did this become true" timestamp, stamped by
// [runner.setState]/[runner.setFrameCounts]/[runner.setDrawState] at the
// moment of a genuine transition or sample — using it as ObservedAt is what
// keeps "a fresh ObservedAt means the state actually moved" true all the
// way to the observation layer, the exact invariant finding 2 established
// inside the agent's own Supervisor.
//
//   - rep.retained: [observation.MeasuredUnknownAge]. ObservedAt is nil,
//     never observedAt and never rep.receivedAt — a retained MQTT delivery's
//     own age is unknown regardless of what timestamp the payload claims,
//     unchanged from before this parameter existed.
//   - live: [observation.Measured] with observedAt — the node's own clock
//     at the moment IT recorded this evidence, not this coordinator's.
//
// CollectedAt is rep.receivedAt in both branches, UNCHANGED: that is when
// this package's cache actually recorded the evidence (Store.Put), not the
// later moment Poll happens to run, and never the node's own clock — a
// receiving side's own bookkeeping timestamp per pkg/observation's doc
// comment. Source is [SourceFor](nodeID), not [SourceName] directly — see
// that function's doc comment for why.
func buildValue(nodeID string, res observation.ResourceRef, sig observation.SignalID, value any, observedAt time.Time, rep report) observation.Observation {
	source := SourceFor(nodeID)
	opts := []observation.Option{
		observation.WithSource(source),
		observation.WithCollectedAt(rep.receivedAt),
	}

	if rep.retained {
		o, err := observation.MeasuredUnknownAge(res, sig, value, opts...)
		if err != nil {
			return failed(res, sig, source, internalErrorReason(nodeID, err), rep.receivedAt)
		}
		return o
	}

	opts = append(opts, observation.WithValidFor(DefaultValidFor))
	o, err := observation.Measured(res, sig, value, observedAt, opts...)
	if err != nil {
		return failed(res, sig, source, internalErrorReason(nodeID, err), rep.receivedAt)
	}
	return o
}

func failed(res observation.ResourceRef, sig observation.SignalID, source, reason string, at time.Time) observation.Observation {
	o, err := observation.CollectionFailed(res, sig, reason,
		observation.WithSource(source), observation.WithCollectedAt(at))
	if err != nil {
		// reason is always non-empty and res/sig are always set by every
		// call site in this package; a failure here is a bug in this file,
		// not a runtime condition to degrade from gracefully — matching
		// fppmqtt.Collector.failedAt's identical panic.
		panic(fmt.Sprintf("noderender: CollectionFailed(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

func notCollected(res observation.ResourceRef, sig observation.SignalID, source, reason string, at time.Time) observation.Observation {
	o, err := observation.NotCollected(res, sig, reason,
		observation.WithSource(source), observation.WithCollectedAt(at))
	if err != nil {
		panic(fmt.Sprintf("noderender: NotCollected(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

func internalErrorReason(nodeID string, err error) string {
	return fmt.Sprintf("internal error building observation for node %s: %v", nodeID, err)
}

// SourceFor returns the [observation.Observation.Source] this package
// stamps on every observation it builds for nodeID: SourceName plus that
// node's own id. [Collector.ID] (the Runner registration / snapshot
// collectors[] identity) stays the bare [SourceName] — this is a distinct,
// finer-grained identity used only on individual observations.
//
// This exists because the observations table's primary key is (resource
// kind, resource id, signal, source): two DIFFERENT nodes reporting the
// SAME surface id (a surface reassigned from one node to another, both
// still reporting during the transition) would otherwise collide on one
// row, and internal/coordinator/collector.Runner polls this package's
// Collector over a Go map with randomized iteration order, making whichever
// node's observation happened to be built last within a Poll call win —
// nondeterministically, on every call. Scoping the source per node instead
// gives both nodes their own row, exactly the mechanism schemaV4 built for
// two independent collector SOURCES (fpp-rest vs fpp-mqtt) — see
// internal/coordinator/api/precedence.go — reused here for two independent
// PRODUCERS within one source. A reader that wants a single answer resolves
// them via that same file's ResolveObservations, deterministically, by
// evidence recency rather than by which node happened to poll last.
func SourceFor(nodeID string) string {
	return SourceName + sourceNodeSeparator + nodeID
}

// sourceNodeSeparator joins [SourceName] and a node id inside a [SourceFor]
// value. Colon: node ids are validated by mqttproto.ValidateNodeID, which
// (per that function) forbids ':', so this can never itself be ambiguous
// with a node id containing the separator.
const sourceNodeSeparator = ":"

// NodeFromSource extracts the node id from a source built by [SourceFor],
// or ("", false) if source does not carry this package's node-render
// prefix. Used at coordinator startup to rebuild [Collector]'s per-node
// memory from the store's own persisted rows — see [WithKnownSurfaces].
func NodeFromSource(source string) (string, bool) {
	prefix := SourceName + sourceNodeSeparator
	if !strings.HasPrefix(source, prefix) {
		return "", false
	}
	return strings.TrimPrefix(source, prefix), true
}

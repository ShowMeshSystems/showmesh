package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// renderReportPublishTimeout bounds a single render report publish attempt,
// matching heartbeat.go's heartbeatPublishTimeout and assetinventory.go's
// assetInventoryPublishTimeout: a hung publish must not delay the next
// tick or trigger.
const renderReportPublishTimeout = 5 * time.Second

// runRenderReport publishes this node's render pipeline health to nodeID's
// observed/render topic on every tick received from ticks, and immediately
// (out of cadence) on every signal received from triggered — mirroring
// runAssetInventory's exact shape (ticker plus a trigger channel), so a
// pipeline state transition is visible without waiting out the interval.
// sup is read via [pipeline.Supervisor.SnapshotAll], which is safe to call
// concurrently with every runner's own supervision goroutine.
//
// runRenderReport returns only when ctx is done; a publish failure never
// causes it to return early, matching runHeartbeat's and
// runAssetInventory's identical contract.
func runRenderReport(ctx context.Context, pub Publisher, nodeID string, sup *pipeline.Supervisor, store *pipeline.AssignmentStore, msStatus *multiSyncStatus, now func() time.Time, ticks <-chan time.Time, triggered <-chan struct{}, logger *slog.Logger) {
	topic, err := mqttproto.ObservedTopic(nodeID, "render")
	if err != nil {
		// nodeID is validated at config load, matching runHeartbeat's and
		// runAssetInventory's identical topic-build guard; should be
		// unreachable in production.
		logger.Error("bug: could not build render report topic for a validated node ID", "node_id", nodeID, "error", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			publishOneRenderReport(ctx, pub, topic, nodeID, sup, store, msStatus, now, logger)
		case _, ok := <-triggered:
			if !ok {
				triggered = nil
				continue
			}
			publishOneRenderReport(ctx, pub, topic, nodeID, sup, store, msStatus, now, logger)
		}
	}
}

// publishOneRenderReport snapshots every surface sup currently supervises,
// plus msStatus's current MultiSync bind evidence (finding 7), and publishes
// a single render report.
func publishOneRenderReport(ctx context.Context, pub Publisher, topic, nodeID string, sup *pipeline.Supervisor, store *pipeline.AssignmentStore, msStatus *multiSyncStatus, now func() time.Time, logger *slog.Logger) {
	pubCtx, cancel := context.WithTimeout(ctx, renderReportPublishTimeout)
	defer cancel()

	gstPath, gstOK, _ := pipeline.ResolveGstLaunch()
	msListening, msReason, msObservedAt := msStatus.get()

	// assignments is re-read fresh from disk on every report tick, keyed by
	// surface id, so the report carries what this node actually PERSISTED
	// for that surface rather than anything the coordinator most
	// recently asked for. A read failure is logged and treated as "no
	// assignment known" for this tick: a report with a stale or fabricated
	// filename would be worse than one that states absence and recovers on
	// the next tick.
	assignments := map[string]pipeline.Assignment{}
	if loaded, err := store.Load(); err != nil {
		logger.Warn("failed to load persisted render assignments for report", "error", err)
	} else {
		for _, a := range loaded {
			assignments[a.SurfaceID] = a
		}
	}

	snapshots := sup.SnapshotAll()
	// Surfaces is built as a non-nil, possibly-empty slice regardless of
	// snapshots' own length: a node holding no surface assignment reports
	// "surfaces": [], never omits the key — matching AssetInventoryPayload.
	// Assets's identical no-omitempty rule.
	surfaces := make([]mqttproto.RenderSurfaceReport, 0, len(snapshots))
	for _, s := range snapshots {
		rep := toRenderSurfaceReport(s)
		if a, ok := assignments[s.SurfaceID]; ok {
			applyContentIdentity(&rep, a, now())
		}
		surfaces = append(surfaces, rep)
	}

	env, err := mqttproto.NewRenderEnvelope(now, nodeID, mqttproto.RenderPayload{
		GstLaunchPath:       gstPath,
		GstLaunchAvailable:  gstOK,
		Surfaces:            surfaces,
		MultiSyncListening:  msListening,
		MultiSyncReason:     msReason,
		MultiSyncObservedAt: msObservedAt,
	})
	if err != nil {
		logger.Error("failed to build render report envelope", "error", err)
		return
	}
	payload, err := json.Marshal(env)
	if err != nil {
		logger.Error("failed to marshal render report envelope", "error", err)
		return
	}

	if err := pub.Publish(pubCtx, topic, mqttproto.ObservedDeliveryPolicy.QoS, mqttproto.ObservedDeliveryPolicy.Retain, payload); err != nil {
		logger.Warn("render report publish failed; will retry next tick", "error", err)
		return
	}

	logger.Debug("published render report", "surface_count", len(surfaces), "gst_launch_available", gstOK)
}

// toRenderSurfaceReport converts a pipeline.Snapshot (this package's
// internal supervision state) to the wire type. Reason is passed through
// as-is: pipeline.setState is required to stamp a real, non-empty reason
// for every state other than Running (mqttproto.RenderPayload.Validate
// enforces exactly that on the way out), so no synthesis happens here.
func toRenderSurfaceReport(s pipeline.Snapshot) mqttproto.RenderSurfaceReport {
	lastStderr := truncateForWire(s.LastStderr, mqttproto.RenderStderrTruncatedSuffix)

	return mqttproto.RenderSurfaceReport{
		SurfaceID:           s.SurfaceID,
		PipelineState:       string(s.State),
		Reason:              s.Reason,
		Since:               s.Since,
		RestartCount:        s.RestartCount,
		ConsecutiveFailures: s.ConsecutiveFailures,
		LastExitCode:        s.LastExitCode,
		LastStderr:          lastStderr,
		FramesWritten:       s.FramesWritten,
		FramesLate:          s.FramesLate,
		FramesDropped:       s.FramesDropped,
		FramesRate:          s.FramesRate,
		FramesObservedAt:    s.FramesObservedAt,
		Transport:           s.Transport,
		TransportAvailable:  s.TransportAvailable,
		TransportReason:     s.TransportReason,
		ObservedAt:          s.ObservedAt,
		TimelineState:       s.TimelineState,
		TimelinePositionMS:  s.TimelinePositionMS,
		Drawing:             s.Drawing,
		IdleMode:            s.IdleMode,
		FailureOutput:       s.FailureOutput,
	}
}

// applyContentIdentity stamps rep's six content-identity fields (the
// original four, plus Show/Generation) from a, the assignment this
// node actually PERSISTED for this surface, the same record
// cueactivationrender.go and renderops.go read back at boot to resume
// rendering, never from anything the coordinator most recently requested.
// Leaves every field "" or 0 (already rep's zero value) when a's params
// carry no fseqFilename, so an undecodable or content-less assignment
// reports absence rather than propagating a decode failure into a
// fabricated identity.
//
// observedAt is this node's own read time for a — the caller's now() at
// the moment it re-read the persisted assignment store, stamped onto
// rep.ContentObservedAt only when a genuine identity is actually applied.
// The store is re-read fresh on every report tick (publishOneRenderReport's
// doc comment), so "when I read this" is a real, continuously refreshed
// observation: a cue activation swaps the frame writer without
// transitioning PipelineState, and a surface rendering the same content
// steadily must not read stale merely because ObservedAt never moves.
func applyContentIdentity(rep *mqttproto.RenderSurfaceReport, a pipeline.Assignment, observedAt time.Time) {
	var params map[string]any
	if err := json.Unmarshal(a.RawParams, &params); err != nil {
		return
	}
	filename, _ := params["fseqFilename"].(string)
	if filename == "" {
		return
	}
	hash, _ := params["fseqContentHash"].(string)

	rep.FSEQFilename = filename
	rep.FSEQContentHash = hash
	rep.CueID = a.CueID
	if a.Auth != nil {
		rep.CatalogRevision = a.Auth.CatalogRevision
		rep.Show = a.Auth.Show
		rep.Generation = a.Auth.Generation
	}
	rep.ContentObservedAt = observedAt
}

// renderWireStderrCap mirrors mqttproto's own maxRenderStderrBytes (an
// unexported constant in that package): this defensive second truncation
// exists so a change to pipeline's own ring-buffer cap can never, by
// itself, make a publish fail RenderPayload.Validate.
const renderWireStderrCap = 4 * 1024

// truncateForWire bounds s to renderWireStderrCap, appending suffix
// visibly when truncation actually happens — see mqttproto.
// RenderStderrTruncatedSuffix's doc comment on why silent truncation is
// forbidden on this wire boundary.
func truncateForWire(s, suffix string) string {
	if len(s) <= renderWireStderrCap {
		return s
	}
	cut := renderWireStderrCap - len(suffix)
	if cut < 0 {
		cut = 0
	}
	return s[len(s)-cut:] + suffix
}

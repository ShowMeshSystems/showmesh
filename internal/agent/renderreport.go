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
// concurrently with every runner's own supervision goroutine. fcHeld is
// FC2's upload/binding store (fppconnectheld.go); its Held() and Events()
// are this render report's only path for ADR-044 decision 8's unbound-
// held-file evidence and ADR-044 decision 4's refused-upload evidence to
// reach an operator, since xLights never inspects any of those calls'
// response status.
//
// runRenderReport returns only when ctx is done; a publish failure never
// causes it to return early, matching runHeartbeat's and
// runAssetInventory's identical contract.
func runRenderReport(ctx context.Context, pub Publisher, nodeID string, sup *pipeline.Supervisor, msStatus *multiSyncStatus, fcStatus *fppConnectHTTPStatus, fcHeld *fppConnectHeldStore, now func() time.Time, ticks <-chan time.Time, triggered <-chan struct{}, logger *slog.Logger) {
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
			publishOneRenderReport(ctx, pub, topic, nodeID, sup, msStatus, fcStatus, fcHeld, now, logger)
		case _, ok := <-triggered:
			if !ok {
				triggered = nil
				continue
			}
			publishOneRenderReport(ctx, pub, topic, nodeID, sup, msStatus, fcStatus, fcHeld, now, logger)
		}
	}
}

// publishOneRenderReport snapshots every surface sup currently supervises,
// plus msStatus's current MultiSync bind evidence (finding 7), fcStatus's
// current FPP Connect HTTP listener evidence (ADR-044), and fcHeld's
// currently held files and bounded evidence log, and publishes a single
// render report.
func publishOneRenderReport(ctx context.Context, pub Publisher, topic, nodeID string, sup *pipeline.Supervisor, msStatus *multiSyncStatus, fcStatus *fppConnectHTTPStatus, fcHeld *fppConnectHeldStore, now func() time.Time, logger *slog.Logger) {
	pubCtx, cancel := context.WithTimeout(ctx, renderReportPublishTimeout)
	defer cancel()

	gstPath, gstOK, _ := pipeline.ResolveGstLaunch()
	msListening, msReason, msObservedAt := msStatus.get()
	fcListening, fcReason, fcObservedAt := fcStatus.get()

	snapshots := sup.SnapshotAll()
	// Surfaces is built as a non-nil, possibly-empty slice regardless of
	// snapshots' own length: a node holding no surface assignment reports
	// "surfaces": [], never omits the key — matching AssetInventoryPayload.
	// Assets's identical no-omitempty rule.
	surfaces := make([]mqttproto.RenderSurfaceReport, 0, len(snapshots))
	for _, s := range snapshots {
		surfaces = append(surfaces, toRenderSurfaceReport(s))
	}

	// heldRecords is the true total; held is what actually rides the wire,
	// truncated separately (review round 3 finding 2: an unbounded list
	// here could otherwise carry every render report past the envelope
	// limit once enough files accumulate). Truncating BEFORE calling
	// NewRenderEnvelope, not after, matters: that constructor calls
	// RenderPayload.Validate itself and refuses to build an over-cap
	// payload at all, so an untruncated list would not degrade the field,
	// it would silently cancel the whole publish.
	heldRecords := fcHeld.Held()
	heldTotal := len(heldRecords)
	heldRecords = truncateHeldFilesForWire(heldRecords, renderWireHeldFilesCap)
	held := make([]mqttproto.RenderFPPConnectHeldFile, 0, len(heldRecords))
	for _, rec := range heldRecords {
		held = append(held, toRenderFPPConnectHeldFile(rec))
	}

	heldEvents := fcHeld.Events()
	if len(heldEvents) > renderWireHeldEventsCap {
		// Keep the most recent entries: the oldest-first log's newest
		// tail is the evidence most likely to still be actionable.
		heldEvents = heldEvents[len(heldEvents)-renderWireHeldEventsCap:]
	}
	events := make([]mqttproto.RenderFPPConnectHeldEvent, 0, len(heldEvents))
	for _, ev := range heldEvents {
		events = append(events, toRenderFPPConnectHeldEvent(ev))
	}

	renderPayload := mqttproto.RenderPayload{
		GstLaunchPath:        gstPath,
		GstLaunchAvailable:   gstOK,
		Surfaces:             surfaces,
		MultiSyncListening:   msListening,
		MultiSyncReason:      msReason,
		MultiSyncObservedAt:  msObservedAt,
		FPPConnectListening:  fcListening,
		FPPConnectReason:     fcReason,
		FPPConnectObservedAt: fcObservedAt,
		FPPConnectHeldCount:  heldTotal,
		FPPConnectHeld:       held,
		FPPConnectHeldEvents: events,
	}
	renderPayload = shrinkRenderPayloadToFitEnvelope(renderPayload, logger)

	env, err := mqttproto.NewRenderEnvelope(now, nodeID, renderPayload)
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

// toRenderFPPConnectHeldFile converts one fppConnectHeldRecord
// (fppconnectheld.go) to its wire type, field for field. Name alone is
// bounded (review round 5 finding 6), reusing fppConnectBoundEventString's
// same cap and truncation marker: it is copied straight from the
// Upload-Name header, itself bounded only by fppConnectMaxHeaderBytes (16
// KiB), with no bound of its own on fppConnectHeldRecord. Up to
// renderWireHeldFilesCap (256) of those, each up to 16 KiB, would make
// shrinkRenderPayloadToFitEnvelope's one-record-at-a-time drop loop
// re-marshal a multi-megabyte payload on every iteration it took to shrink
// back under budget: quadratic in the number of entries dropped. Bounding
// Name here keeps the whole payload within a small multiple of the size
// budget even at the count cap, so the shrink loop never has more than a
// few records left to drop.
func toRenderFPPConnectHeldFile(rec fppConnectHeldRecord) mqttproto.RenderFPPConnectHeldFile {
	return mqttproto.RenderFPPConnectHeldFile{
		Dir:                     rec.Dir,
		Name:                    fppConnectBoundEventString(rec.Name),
		SizeBytes:               rec.SizeBytes,
		ContentHash:             rec.ContentHash,
		ReceivedAt:              rec.ReceivedAt,
		Bound:                   rec.Bound,
		Show:                    rec.Show,
		ShowID:                  rec.ShowID,
		LogicalSequence:         rec.LogicalSequence,
		UnboundReason:           rec.UnboundReason,
		RegistrationState:       rec.RegistrationState,
		RegistrationAssetID:     rec.RegistrationAssetID,
		RegistrationRolledBack:  rec.RegistrationRolledBack,
		RegistrationReason:      rec.RegistrationReason,
		RegistrationProblemType: rec.RegistrationProblemType,
		RegistrationNextRetryAt: rec.RegistrationNextRetryAt,
	}
}

// toRenderFPPConnectHeldEvent converts one fppConnectEvent
// (fppconnectheld.go) to its wire type, field for field. Entries is
// already capped at fppConnectMaxEventEntries by the store itself
// (RecordUnknownPlaylist/RecordAmbiguousPlaylist), so no further
// truncation happens here.
func toRenderFPPConnectHeldEvent(ev fppConnectEvent) mqttproto.RenderFPPConnectHeldEvent {
	return mqttproto.RenderFPPConnectHeldEvent{
		Kind:             ev.Kind,
		Name:             ev.Name,
		Dir:              ev.Dir,
		Reason:           ev.Reason,
		Entries:          ev.Entries,
		EntriesTruncated: ev.EntriesTruncated,
		MatchCount:       ev.MatchCount,
		At:               ev.At,
	}
}

// renderWireHeldFilesCap and renderWireHeldEventsCap mirror mqttproto's
// own maxRenderHeldFiles/maxRenderHeldEvents (private constants in that
// package): this defensive truncation exists so a change to either cap
// can never, by itself, make a publish fail RenderPayload.Validate,
// matching renderWireStderrCap's identical role one field over (review
// round 3 finding 2).
const (
	renderWireHeldFilesCap  = 256
	renderWireHeldEventsCap = 64
)

// truncateHeldFilesForWire always reorders records unbound-first, cutting
// down to at most cap entries when it must (review round 5 finding 4:
// partitioning only inside the len(records) > maxEntries branch left the
// dir-then-name order untouched whenever the count cap did not fire, so
// shrinkRenderPayloadToFitEnvelope's own tail-drop, which assumes
// unbound-first ordering to drop bound records before unbound ones, could
// instead drop an unbound record first whenever a report was under the
// count cap but still over the byte budget). Unbound records sort first:
// those are the operator-actionable evidence ADR-044 decision 8 exists to
// surface, so whether cut here or later by the byte-budget shrink, the
// files still awaiting a claim are worth more than the ones already
// resolved. records is already sorted by dir then name
// (fppConnectHeldStore.Held); ordering within each of the two groups is
// preserved.
func truncateHeldFilesForWire(records []fppConnectHeldRecord, maxEntries int) []fppConnectHeldRecord {
	var unbound, bound []fppConnectHeldRecord
	for _, r := range records {
		if r.Bound {
			bound = append(bound, r)
		} else {
			unbound = append(unbound, r)
		}
	}
	combined := append(unbound, bound...)
	if len(combined) <= maxEntries {
		return combined
	}
	return combined[:maxEntries]
}

// renderReportEnvelopeSizeBudget mirrors mqttproto's own maxEnvelopeSize
// (256 KiB, an unexported constant in that package), with headroom kept
// under it for the envelope wrapper's own fields (schema, messageId,
// nodeId, sentAt) and this function's own json.Marshal call not being
// byte-for-byte identical to the one NewRenderEnvelope performs on the
// final Envelope: the last-resort backstop shrinkRenderPayloadToFitEnvelope
// applies BEFORE calling NewRenderEnvelope (review round 4 finding 1).
// Every cap above (renderWireHeldFilesCap, renderWireHeldEventsCap,
// fppConnectMaxEventEntries, fppConnectMaxEventStringBytes) bounds one
// field's own size, never the combined report, so a report carrying many
// events or many held records, each individually within its own per-field
// caps, could still exceed the envelope once everything else this report
// carries is added in.
const renderReportEnvelopeSizeBudget = 240 * 1024

// shrinkRenderPayloadToFitEnvelope drops the oldest event (FPPConnectHeldEvents
// is oldest-first), then the lowest-priority held record
// (truncateHeldFilesForWire's own tail, so the unbound-first priority it
// already established stays intact here too), until p's own serialized
// size fits under renderReportEnvelopeSizeBudget, or until there is
// nothing left to drop. mqttproto.NewRenderEnvelope's own Validate call
// never checks the payload's total serialized size, only the per-field
// counts and lengths already satisfied by the caps applied before this
// runs; the actual size limit, mqttproto.DecodeEnvelope's maxEnvelopeSize,
// is enforced only on the receiving end, once the coordinator reads the
// published bytes off the wire. Without this shrink, an over-budget
// payload would build and publish successfully here, then be rejected
// there instead, silently and per message, for as long as the offending
// content keeps riding every report that carries it, so degrading this
// field before publish is strictly better than that.
func shrinkRenderPayloadToFitEnvelope(p mqttproto.RenderPayload, logger *slog.Logger) mqttproto.RenderPayload {
	dropped := 0
	for {
		raw, err := json.Marshal(p)
		if err == nil && len(raw) <= renderReportEnvelopeSizeBudget {
			break
		}
		switch {
		case len(p.FPPConnectHeldEvents) > 0:
			p.FPPConnectHeldEvents = p.FPPConnectHeldEvents[1:]
		case len(p.FPPConnectHeld) > 0:
			p.FPPConnectHeld = p.FPPConnectHeld[:len(p.FPPConnectHeld)-1]
		default:
			if err != nil {
				logger.Error("failed to measure render report payload size while shrinking it to fit the envelope", "error", err)
			} else {
				logger.Warn("render report payload still exceeds its size budget with no more events or held records left to drop", "size_bytes", len(raw), "budget_bytes", renderReportEnvelopeSizeBudget)
			}
			return p
		}
		dropped++
	}
	if dropped > 0 {
		logger.Warn("shrank render report payload to fit its size budget", "entries_dropped", dropped)
	}
	return p
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

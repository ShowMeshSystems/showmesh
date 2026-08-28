package noderender

import (
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// SourceName is this collector's identity: the API's collectors[] list and
// [Collector.ID]'s Runner-registration identity, matching [Store]'s own
// reservation (docs/build/IDENTIFIER-REGISTER.md). Individual observations
// this package produces stamp [SourceFor](nodeID) instead, not this
// constant bare — see that function's doc comment.
const SourceName = "node-render"

// Signal vocabulary under the "surface" resource kind. One
// [mqttproto.RenderSurfaceReport] field per signal, chosen from the
// contract's list: pipeline state, restart count, consecutive failures,
// frames written/late/dropped, transport availability, and the pipeline's
// stated reason (which doubles as the last-exit reason: it is required
// whenever pipelineState is not "running" — see RenderSurfaceReport.Reason).
const (
	SignalSurfacePipelineState       observation.SignalID = "surface.pipeline.state"
	SignalSurfaceReason              observation.SignalID = "surface.pipeline.reason"
	SignalSurfaceRestartCount        observation.SignalID = "surface.pipeline.restart_count"
	SignalSurfaceConsecutiveFailures observation.SignalID = "surface.pipeline.consecutive_failures"
	SignalSurfaceFramesWritten       observation.SignalID = "surface.frames.written"
	SignalSurfaceFramesLate          observation.SignalID = "surface.frames.late"
	SignalSurfaceFramesDropped       observation.SignalID = "surface.frames.dropped"
	// SignalSurfaceFramesRate is ADR-040's obligation: the achieved frame
	// rate at the surface's configured geometry, so an operator who
	// authors a matrix the hardware cannot sustain finds out from the
	// dashboard instead of the wall. Only ever [observation.StateNotCollected]
	// or a real measurement (see [mqttproto.RenderSurfaceReport.FramesRate])
	// — never a plausible-looking zero and never the configured target rate
	// echoed back.
	SignalSurfaceFramesRate         observation.SignalID = "surface.frames.rate"
	SignalSurfaceTransportAvailable observation.SignalID = "surface.transport.available"
	// SignalSurfaceTransportReason is only ever emitted alongside
	// SignalSurfaceTransportAvailable=false (Track B seam B4,
	// [mqttproto.RenderSurfaceReport.TransportReason]'s identical
	// required-whenever-false rule) — an actionable reason, never a bare
	// "unavailable."
	SignalSurfaceTransportReason observation.SignalID = "surface.transport.reason"

	// Four signals minted 2026-08-17 for the review's finding 7
	// (docs/build/IDENTIFIER-REGISTER.md): what a surface is actually
	// drawing, not just whether its pipeline reports "running". One
	// [mqttproto.RenderSurfaceReport] field each, mirroring
	// TimelineState/TimelinePositionMS/Drawing/IdleMode.
	SignalSurfaceTimelineState      observation.SignalID = "surface.timeline.state"
	SignalSurfaceTimelinePositionMS observation.SignalID = "surface.timeline.position_ms"
	SignalSurfaceOutputMode         observation.SignalID = "surface.output.mode"
	// SignalSurfaceOutputIdleMode is only ever emitted alongside
	// SignalSurfaceOutputMode="idle" — absent (NotCollected, stated) while
	// drawing content, mirroring SignalSurfaceTransportReason's identical
	// required-whenever-the-flag-says-so pattern.
	SignalSurfaceOutputIdleMode observation.SignalID = "surface.output.idle_mode"

	// SignalSurfaceOutputFailure is only ever emitted alongside
	// SignalSurfaceOutputMode="failure": which fallback the writer put on
	// the wire when it could not extract the frame it was asked for
	// ("alert" or "black", the node's operating mode deciding which). The
	// mode is why the same failure looks different on two nights, so the
	// report has to say which one the operator is looking at.
	SignalSurfaceOutputFailure observation.SignalID = "surface.output.failure"

	// Four signals minted for the node content-identity build item
	// (docs/build/IDENTIFIER-REGISTER.md):
	// the content identity this surface's frame writer actually applied:
	// the node's own evidence for which FSEQ it is rendering, so a content
	// swap can be proven from the node's own report rather than inferred
	// from pipelineState/frame counters alone (which read identically
	// whether the surface is rendering the right sequence or the wrong
	// one). One [mqttproto.RenderSurfaceReport] field each.
	SignalSurfaceContentFSEQFilename    observation.SignalID = "surface.content.fseq_filename"
	SignalSurfaceContentFSEQContentHash observation.SignalID = "surface.content.fseq_content_hash"
	// SignalSurfaceContentCueID is only ever emitted (never
	// [observation.StateNotCollected] alongside a real filename) when the
	// current assignment was applied by a cue activation; a direct
	// render.surface.apply with no cue involved reports this one signal as
	// not collected while still reporting the filename/hash/catalog
	// revision, mirroring SignalSurfaceOutputIdleMode's identical
	// conditional pattern.
	SignalSurfaceContentCueID observation.SignalID = "surface.content.cue_id"
	// SignalSurfaceContentCatalogRevision is only ever emitted alongside a
	// real filename when the persisted assignment carries an authorization
	// tuple (TRACK-H-H3-SPEC.md section 5); absent for a legacy assignment
	// or one applied before the tuple existed.
	SignalSurfaceContentCatalogRevision observation.SignalID = "surface.content.catalog_revision"

	// SignalSurfaceContentShow and SignalSurfaceContentGeneration name
	// the Show and generation that authorized this
	// surface's held assignment (mqttproto.RenderSurfaceReport.Show/
	// Generation, [pipeline.AssignmentAuth.Show]/Generation), rendered as
	// their own signals rather than folded into SignalSurfacePipelineState
	// itself — IDENTIFIER-REGISTER.md's own ruling: superseded is a new
	// MEMBER of that signal's existing state vocabulary, not a reason to
	// mint a second parallel signal. This package never derives the
	// superseded verdict itself (these two signals, plus
	// SignalSurfaceContentCatalogRevision and SignalSurfaceContentCueID
	// above, are ALL a node ever states about what it holds); the
	// coordinator's API/readiness layer is the only place that compares
	// them against its own active-show resolution — see that layer's own
	// doc comment for why this package must not make that comparison.
	// Present/absent together, mirroring SignalSurfaceContentCatalogRevision's
	// identical "no authorization tuple" absence rule one signal up.
	SignalSurfaceContentShow       observation.SignalID = "surface.content.show"
	SignalSurfaceContentGeneration observation.SignalID = "surface.content.generation"
)

// Two more signals minted alongside the four above, but under the "node"
// resource kind, not "surface": one MultiSync listener serves every
// surface on a node, so attributing its failure to a surface would report
// one fact N times and imply N independent faults (the review's own
// correction). See [nodeMultiSyncObservations].
const (
	SignalNodeMultiSyncListening observation.SignalID = "node.multisync.listening"
	SignalNodeMultiSyncReason    observation.SignalID = "node.multisync.reason"
)

// AllSignalIDs is every SURFACE-resource signal this package ever emits, in
// the order [Collector.Poll] builds them for one surface — used by tests
// that need to enumerate the full per-surface vocabulary without
// hand-maintaining a second list. Does NOT include the node.multisync.*
// signals: those are node-resource, not surface-resource — see
// [AllNodeSignalIDs].
var AllSignalIDs = []observation.SignalID{
	SignalSurfacePipelineState,
	SignalSurfaceReason,
	SignalSurfaceRestartCount,
	SignalSurfaceConsecutiveFailures,
	SignalSurfaceFramesWritten,
	SignalSurfaceFramesLate,
	SignalSurfaceFramesDropped,
	SignalSurfaceFramesRate,
	SignalSurfaceTransportAvailable,
	SignalSurfaceTransportReason,
	SignalSurfaceTimelineState,
	SignalSurfaceTimelinePositionMS,
	SignalSurfaceOutputMode,
	SignalSurfaceOutputIdleMode,
	SignalSurfaceOutputFailure,
	SignalSurfaceContentFSEQFilename,
	SignalSurfaceContentFSEQContentHash,
	SignalSurfaceContentCueID,
	SignalSurfaceContentCatalogRevision,
	SignalSurfaceContentShow,
	SignalSurfaceContentGeneration,
}

// AllNodeSignalIDs is every NODE-resource signal this package emits, in the
// order [nodeMultiSyncObservations] builds them — [AllSignalIDs]'s
// counterpart for the two signals that are about the node itself, not one
// of its surfaces.
var AllNodeSignalIDs = []observation.SignalID{
	SignalNodeMultiSyncListening,
	SignalNodeMultiSyncReason,
}

// DefaultPollInterval is this collector's recommended [collector.Runner]
// cadence: how often the push cache in [Store] is re-rendered into
// observations, not how often a node publishes (that is the agent's own
// SHOWMESH_RENDER_REPORT_INTERVAL, defaulting to 15s — internal/agent/
// config). SHOWMESH HYPOTHESIS, NOT MEASURED, matching every other
// collector's own default in this codebase (fpp.DefaultPollInterval,
// resolume.DefaultPollInterval): short enough that a fresh report reaches
// the observations table quickly, long enough not to spin needlessly on a
// pure cache render with no I/O of its own.
const DefaultPollInterval = 5 * time.Second

// DefaultValidFor bounds how long a LIVE (non-retained) report stays
// [observation.StateCurrent] before ageing to stale. Three times the
// agent's default publish interval, the same "3x the producer's own
// cadence" convention internal/coordinator/collector/fpp.DefaultValidFor
// and internal/coordinator/inventory's heartbeat staleness window both use
// — SHOWMESH HYPOTHESIS, NOT MEASURED: this project has not run a render
// node long enough to know a real number.
const DefaultValidFor = 45 * time.Second

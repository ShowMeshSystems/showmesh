package api

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// renderSurfaceEditIssuerPrincipalID attributes the re-apply a surface edit dispatches.
const renderSurfaceEditIssuerPrincipalID = "system-render-surface-edit"

// surfaceRenderGeometryChanged reports whether an edit changed anything the running pipeline draws with.
func surfaceRenderGeometryChanged(before, after config.ShowSurfacePayload) bool {
	return before.ChannelRange != after.ChannelRange || before.Geometry != after.Geometry ||
		before.FrameRate != after.FrameRate || !sameSurfaceOutput(before.Output, after.Output)
}

func sameSurfaceOutput(a, b config.ShowSurfaceOutput) bool {
	if a.Transport != b.Transport || (a.NDI == nil) != (b.NDI == nil) || (a.HDMI == nil) != (b.HDMI == nil) {
		return false
	}
	return (a.NDI == nil || *a.NDI == *b.NDI) && (a.HDMI == nil || *a.HDMI == *b.HDMI)
}

// reapplyEditedSurface re-dispatches render.surface.apply with the saved surface's new geometry
// to a node already running that surface, keeping the sequence it is drawing.
func (h *handlers) reapplyEditedSurface(ctx context.Context, now time.Time, nodeID, surfaceID string, configRevision int64) {
	if h.deps.Commands == nil || h.deps.RenderPublisher == nil || h.deps.AssetManifests == nil {
		return
	}
	running, filename := h.liveSurfaceRender(nodeID, surfaceID)
	if !running {
		return
	}
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil || !active.Configured {
		return
	}
	catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
	if err != nil {
		h.logWarn("render surface edit: resolve cue catalog failed", "node", nodeID, "surface", surfaceID, "error", err)
		return
	}
	params, problem, err := h.resolveRenderEstablishParams(ctx, nodeID, surfaceID, catalog.Generation, catalog.Revision)
	if err != nil || problem != nil {
		h.logWarn("render surface edit: resolve params failed", "node", nodeID, "surface", surfaceID, "error", err, "problem", problem)
		return
	}
	if filename != "" {
		hash, err := h.expectedFSEQHash(ctx, catalog.Show, nodeID, filename)
		if err != nil || hash == "" {
			h.logWarn("render surface edit: the running sequence has no current asset, not re-applying", "node", nodeID, "surface", surfaceID, "fseqFilename", filename, "error", err)
			return
		}
		params["fseqFilename"] = filename
		params["fseqContentHash"] = hash
	}
	in := renderDispatchInput{
		Action: "render.surface.apply", NodeID: nodeID, SurfaceID: surfaceID,
		IdempotencyKey: fmt.Sprintf("render-surface-edit-%s-%s-%d", nodeID, surfaceID, configRevision),
		DesiredState:   "running",
		IssuerID:       renderSurfaceEditIssuerPrincipalID, IssuerName: "show.surface edit",
		Params: params,
	}
	if _, problem, err := h.executeRenderDispatch(ctx, now, in); err != nil || problem != nil {
		h.logWarn("render surface edit: re-apply failed", "node", nodeID, "surface", surfaceID, "error", err, "problem", problem)
	}
}

// liveSurfaceRender reads nodeID's latest render report: whether surfaceID is running, and the sequence it draws.
func (h *handlers) liveSurfaceRender(nodeID, surfaceID string) (running bool, fseqFilename string) {
	now := h.now()
	for _, o := range h.deps.Render.NodeRenderObservations(nodeID) {
		if o.Resource.Kind != observation.ResourceSurface || o.Resource.ID != surfaceID || o.StateAt(now) != observation.StateCurrent {
			continue
		}
		value, _ := o.Value.(string)
		switch string(o.Signal) {
		case renderSignalPipelineState:
			running = value == mqttproto.RenderPipelineStateRunning
		case renderSignalContentFSEQFilename:
			fseqFilename = value
		}
	}
	return running, fseqFilename
}

func (h *handlers) expectedFSEQHash(ctx context.Context, showID, nodeID, filename string) (string, error) {
	expected, err := assetsync.ExpectedAssetsForNode(ctx, h.deps.AssetManifests, showID, nodeID)
	if err != nil {
		return "", err
	}
	for _, a := range expected.Assets {
		if a.MediaType == "fseq" && a.Filename == filename {
			return a.ContentHash, nil
		}
	}
	return "", nil
}

// storedShowSurfacePayload returns surface id's current stored payload, if any.
func (h *handlers) storedShowSurfacePayload(ctx context.Context, id string) (config.ShowSurfacePayload, bool) {
	obj, err := h.deps.Config.GetConfigObject(ctx, config.ShowSurfaceConfigKind, id)
	if err != nil || obj.CurrentRevision == 0 {
		return config.ShowSurfacePayload{}, false
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowSurfaceConfigKind, id, obj.CurrentRevision)
	if err != nil {
		return config.ShowSurfacePayload{}, false
	}
	payload, verr := config.DecodeShowSurfacePayload(rev.PayloadJSON, alwaysTrue, alwaysTrue)
	if verr != nil {
		return config.ShowSurfacePayload{}, false
	}
	return payload, true
}

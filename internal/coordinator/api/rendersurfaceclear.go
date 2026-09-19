package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is ADR-053 decision 6's own "every render surface is cleared,
// and emergency stop gains the render surface clear as well" shared
// dispatch: one enumeration of every declared (nodeId, surfaceId) pair,
// and one concurrent fan-out of render.surface.clear across all of them,
// used by both emergencystop.go (its fourth target kind) and
// weatherdelay.go (start's own render clear).

// renderSurfaceRef names one declared show.surface object: its own config
// object id (the surface id) and the node it is assigned to.
type renderSurfaceRef struct {
	NodeID    string
	SurfaceID string
}

// renderSurfaceListUnavailableID is the synthetic identity used for the
// ONE outcome entry [handlers.allRenderSurfaces]'s caller reports when the
// declared show.surface list itself could not be read, mirroring
// emergencyStopInstanceListUnavailableID's identical siblings one file
// over.
const renderSurfaceListUnavailableID = "(render-surface-list)"

// declaredRenderSurfaces lists every show.surface object with an active
// revision as a (node, surface) pair, mirroring
// [handlers.declaredAudioNodeIDs]'s identical "declared, independent of
// whether it is currently reachable" property one config kind over.
func (h *handlers) declaredRenderSurfaces(ctx context.Context) ([]renderSurfaceRef, error) {
	objs, err := h.deps.Config.ListConfigObjects(ctx, config.ShowSurfaceConfigKind)
	if err != nil {
		return nil, fmt.Errorf("list show.surface config objects: %w", err)
	}
	out := make([]renderSurfaceRef, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowSurfaceConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("get active show.surface config revision for %q: %w", obj.ID, err)
		}
		var head struct {
			Node string `json:"node"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return nil, fmt.Errorf("decode show.surface config payload head for %q: %w", obj.ID, err)
		}
		out = append(out, renderSurfaceRef{NodeID: head.Node, SurfaceID: obj.ID})
	}
	return out, nil
}

// clearAllRenderSurfaces dispatches render.surface.clear to every declared
// render surface CONCURRENTLY, mirroring
// [handlers.emergencyStopAllAudioNodes]'s identical "one target must never
// wait on another" reasoning and its identical read-failure-vs-genuinely-
// empty distinction. issuerID/issuerName/clientAddr identify who asked;
// idempotencyKey derives a stable per-surface key the same way
// emergencyStopNodeIdempotencyKey does, so a retried top-level request
// reproduces the same per-surface dispatch identity.
func (h *handlers) clearAllRenderSurfaces(ctx context.Context, now time.Time, idempotencyKey, issuerID, issuerName string, form identity.CredentialForm, credentialID, clientAddr string) []v1.EmergencyStopInstanceOutcome {
	surfaces, err := h.declaredRenderSurfaces(ctx)
	if err != nil {
		h.logWarn("render surface clear: failed to list declared show.surface objects; no clear could be dispatched", "error", err)
		return []v1.EmergencyStopInstanceOutcome{{
			InstanceID:    renderSurfaceListUnavailableID,
			TargetKind:    v1.EmergencyStopTargetKindRender,
			Outcome:       "failed",
			OutcomeReason: fmt.Sprintf("could not list declared show.surface objects, so no clear could be dispatched to any of them: %v", err),
		}}
	}
	if len(surfaces) == 0 {
		return []v1.EmergencyStopInstanceOutcome{}
	}

	out := make([]v1.EmergencyStopInstanceOutcome, len(surfaces))
	var wg sync.WaitGroup
	for i, s := range surfaces {
		wg.Add(1)
		go func(i int, s renderSurfaceRef) {
			defer wg.Done()
			instanceID := s.NodeID + "/" + s.SurfaceID
			in := renderDispatchInput{
				Action: "render.surface.clear", NodeID: s.NodeID, SurfaceID: s.SurfaceID,
				Params:         map[string]any{"surfaceId": s.SurfaceID},
				IdempotencyKey: renderSurfaceClearIdempotencyKey(idempotencyKey, s.NodeID, s.SurfaceID),
				DesiredState:   "stopped",
				IssuerID:       issuerID, IssuerName: issuerName,
				ClientAddr: clientAddr, Form: form, CredentialID: credentialID,
			}
			outcome, problem, err := h.executeRenderDispatch(ctx, now, in)
			switch {
			case err != nil:
				out[i] = v1.EmergencyStopInstanceOutcome{InstanceID: instanceID, TargetKind: v1.EmergencyStopTargetKindRender, Outcome: "failed", OutcomeReason: "this clear could not be dispatched because of an internal coordinator error"}
			case problem != nil:
				out[i] = v1.EmergencyStopInstanceOutcome{InstanceID: instanceID, TargetKind: v1.EmergencyStopTargetKindRender, Outcome: "refused", OutcomeReason: problem.Detail}
			default:
				out[i] = v1.EmergencyStopInstanceOutcome{InstanceID: instanceID, TargetKind: v1.EmergencyStopTargetKindRender, Outcome: outcome.Outcome, OutcomeReason: outcome.OutcomeReason, Replay: outcome.Replay}
			}
		}(i, s)
	}
	wg.Wait()
	return out
}

func renderSurfaceClearIdempotencyKey(idempotencyKey, nodeID, surfaceID string) string {
	return "rendersurfaceclear:" + idempotencyKey + ":" + nodeID + ":" + surfaceID
}

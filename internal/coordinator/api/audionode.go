package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/audioconfigpush"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the HTTP surface for the "audio.node" collection
// (ADR-039): list/get/put/revisions over
// /api/v1/config/audio.node[/{id}[/revisions]], reusing showconfig.go's
// shared helpers rather than a second hand-copy of that plumbing.
//
// handlePutAudioNode refuses placement against probe evidence, never
// against the operator's claim alone: [config.ValidateAudioNodePlacement]
// is the decision, fed the node's advertised audio.output.local/
// audio.output.ltc capability attributes, read live from
// [Dependencies.Nodes] on every write.

const maxAudioNodeConfigRequestBodyBytes = 4 * 1024

// audioOutputLocalCapabilityID and audioOutputLTCCapabilityID mirror the
// literal capability IDs internal/agent/audiocapabilities.go advertises
// (detectAudioCapabilities) — pkg/capability's vocabulary is untyped
// strings by design (an unknown ID is syntactically valid), so there is no
// shared constant to import; these are this file's own copy of the same
// two literals.
const (
	audioOutputLocalCapabilityID = "audio.output.local"
	audioOutputLTCCapabilityID   = "audio.output.ltc"
)

// audioNodeRouteEvidence reads nodeID's advertised program-capable and
// LTC-capable route names from its current Hello capability advertisement
// — [config.ValidateAudioNodePlacement]'s own probe evidence. Both
// returned slices are nil when the node is not in inventory at all, has
// never advertised a Hello, or advertised one with neither capability;
// [config.ValidateAudioNodePlacement] reports that case with
// [config.ErrAudioNodeNoEvidence] rather than this function distinguishing
// it further, matching that function's own doc comment.
func audioNodeRouteEvidence(ctx context.Context, nodes NodeLister, now time.Time, nodeID string) (programRoutes, ltcRoutes []string, err error) {
	views, err := nodes.Snapshot(ctx, now)
	if err != nil {
		return nil, nil, fmt.Errorf("api: snapshot nodes for audio.node placement evidence: %w", err)
	}
	for _, nv := range views {
		if nv.NodeID != nodeID || nv.Hello == nil {
			continue
		}
		for _, c := range nv.Hello.Capabilities {
			switch string(c.ID) {
			case audioOutputLocalCapabilityID:
				programRoutes = capabilityRoutesAttribute(c.Attributes)
			case audioOutputLTCCapabilityID:
				ltcRoutes = capabilityRoutesAttribute(c.Attributes)
			}
		}
		break
	}
	return programRoutes, ltcRoutes, nil
}

// capabilityRoutesAttribute reads a capability's "routes" attribute as a
// []string. [capability.Capability.Attributes] is map[string]any populated
// by decoding the node's own retained MQTT Hello JSON, so a "routes" value
// arrives as []any holding strings, never as a Go []string directly; a
// missing key, a wrong-typed value, or a non-string element all report as
// "this route list is not usable evidence" (nil) rather than panicking or
// silently dropping only the bad element, because a partially-decoded
// route list is worse than none: it would let a real route silently
// disappear from the checked set.
func capabilityRoutesAttribute(attrs map[string]any) []string {
	raw, ok := attrs["routes"]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

// listAudioNodeSummaries lists every "audio.node" config object with an
// active revision. Label carries the configured programRoute — the one
// field of the payload most useful to skim in a list without fetching each
// object's full body. Show is always "" (audio.node carries no show
// reference; [v1.ConfigObjectSummary] is shared with kinds that do).
func (h *handlers) listAudioNodeSummaries(ctx context.Context) ([]v1.ConfigObjectSummary, error) {
	objs, err := h.deps.Config.ListConfigObjects(ctx, config.AudioNodeConfigKind)
	if err != nil {
		return nil, fmt.Errorf("list audio.node config objects: %w", err)
	}
	out := make([]v1.ConfigObjectSummary, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.AudioNodeConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("get active audio.node config revision for %q: %w", obj.ID, err)
		}
		var head struct {
			ProgramRoute string `json:"programRoute"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return nil, fmt.Errorf("decode audio.node config payload head for %q: %w", obj.ID, err)
		}
		out = append(out, v1.ConfigObjectSummary{
			ID: obj.ID, Label: head.ProgramRoute, Show: "",
			CurrentRevision: obj.CurrentRevision, UpdatedAt: formatTime(obj.UpdatedAt),
		})
	}
	return out, nil
}

func (h *handlers) handleListAudioNodes(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	objs, err := h.listAudioNodeSummaries(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "list audio.node config objects", err)
		return
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.AudioNodeConfigKind, Objects: objs})
}

func (h *handlers) handleGetAudioNode(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.AudioNodeConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active audio.node config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.AudioNodePayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode audio.node config payload", err)
		return
	}
	jsonWrite(w, mapAudioNodeConfigResponse(now, rev, obj, payload))
}

func (h *handlers) handlePutAudioNode(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")
	if verr := config.ValidateAudioNodeObjectID(id); verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAudioNodeConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read audio.node request body", err)
		return
	}
	if len(raw) > maxAudioNodeConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	payload, verr := config.DecodeAudioNodePayload(string(raw))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	// Ruling 3: refused against probe evidence, never against the
	// operator's claim alone. Read live on every write (not cached from
	// this request's auth context or anywhere else), matching the
	// fpp.endpoints/resolume.instances live-collision-check convention one
	// file over.
	programRoutes, ltcRoutes, err := audioNodeRouteEvidence(r.Context(), h.deps.Nodes, now, id)
	if err != nil {
		h.writeInternalError(w, now, "get audio.node placement evidence", err)
		return
	}
	if err := config.ValidateAudioNodePlacement(payload, programRoutes, ltcRoutes); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	payloadJSON, err := config.EncodeAudioNodePayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode audio.node config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.AudioNodeConfigKind, id, payloadJSON,
		map[string]any{"programRoute": payload.ProgramRoute, "ltcRoute": payload.LTCRoute})
	if writeErr != nil {
		h.writeInternalError(w, now, "write audio.node config revision", writeErr)
		return
	}

	// ADR-039/ADR-036: push the newly-written binding to id without
	// waiting for its next hello. Best-effort: a node unreachable right
	// now converges on its next successful push instead of failing this
	// write, which already committed.
	audioconfigpush.BestEffort(r.Context(), h.deps.Config, h.deps.RenderPublisher, h.now, id, h.logger)

	jsonWrite(w, mapAudioNodeConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.AudioNodeConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func (h *handlers) handleGetAudioNodeRevisions(w http.ResponseWriter, r *http.Request) {
	h.handleGetShowConfigRevisions(w, r, config.AudioNodeConfigKind)
}

func mapAudioNodeConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.AudioNodePayload) v1.AudioNodeConfigResponse {
	return v1.AudioNodeConfigResponse{
		ServerTime: formatTime(now), Kind: config.AudioNodeConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload: v1.ConfigAudioNode{
			ProgramRoute: p.ProgramRoute, LTCRoute: p.LTCRoute,
			ProgramChannels: p.ProgramChannels, LTCChannel: p.LTCChannel,
			ClockDomain: p.ClockDomain, ClockDomainProvenance: p.ClockDomainProvenance,
		},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is TRACK-H-H3-SPEC.md section 4's own HTTP surface: GET
// /nodes/{nodeId}/cue-catalog (one node's resolved Cue catalog) and POST
// /nodes/{nodeId}/cue-catalog/acknowledge (that node reporting which
// catalog revision it holds). internal/coordinator/assetsync.ResolveCueCatalog
// — reached here and nowhere else — is the ONLY function in this codebase
// permitted to resolve a Cue catalog (see that function's own doc
// comment); this file fetches the coordinator's current time, calls it,
// and maps its result onto the wire. It adds no second resolution rule.

// auditActionCueCatalogAcknowledge is IDENTIFIER-REGISTER.md's own
// reservation for this route's audit action string.
const auditActionCueCatalogAcknowledge = "cuecatalog.acknowledge"

// scopeNodeObserve exists only so api.go's route registration can take its
// address, mirroring [scopeFPPObserve]'s identical reason
// (fppobservations.go): [handlers.writeGuard] takes *identity.Scope, and
// identity.ScopeNodeObserve is a typed string CONSTANT, whose address Go
// does not allow taking directly.
var scopeNodeObserve = identity.ScopeNodeObserve

// maxCueCatalogAcknowledgeRequestBodyBytes bounds the acknowledgement
// request body — it carries only a revision string, a show id, and an
// integer, so this is generous headroom, not a tuned limit.
const maxCueCatalogAcknowledgeRequestBodyBytes = 16 * 1024

// errCueCatalogStoreNotWired is [handlers.writeInternalError]'s cause for
// an acknowledgement received while [Dependencies.AssetManifests] is nil —
// there is no safe no-op store to fall back to for a WRITE the way the GET
// route above falls back to "configured: false" for a read.
var errCueCatalogStoreNotWired = errors.New("no cue catalog data source (AssetManifests) is wired into this API's Dependencies")

// --- GET /nodes/{nodeId}/cue-catalog ---

func (h *handlers) handleGetNodeCueCatalog(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}
	if !h.nodeDeclared(ctx)(nodeID) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("no declared node with id "+strconv.Quote(nodeID)))
		return
	}

	if h.deps.AssetManifests == nil {
		jsonWrite(w, v1.CueCatalogResponse{
			ServerTime: formatTime(now), Node: nodeID, Configured: false, Entries: []v1.CueCatalogEntry{},
		})
		return
	}

	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		h.writeInternalError(w, now, "resolve active show", err)
		return
	}
	if !active.Configured {
		jsonWrite(w, v1.CueCatalogResponse{
			ServerTime: formatTime(now), Node: nodeID, Configured: false, Entries: []v1.CueCatalogEntry{},
		})
		return
	}

	catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
	if err != nil {
		h.writeInternalError(w, now, "resolve cue catalog", err)
		return
	}
	generation := catalog.Generation
	jsonWrite(w, v1.CueCatalogResponse{
		ServerTime: formatTime(now), Node: catalog.Node, Configured: true,
		Show: catalog.Show, Generation: &generation, Revision: catalog.Revision,
		Entries: mapCueCatalogEntries(catalog.Entries),
	})
}

// --- POST /nodes/{nodeId}/cue-catalog/acknowledge ---

// decodeCueCatalogAcknowledgeBody decodes and shallow-validates req's
// body, strict on unknown fields for the identical reason
// decodeNightCommandBody is (nightsessioncontrol.go): a misspelled field
// must never decode silently as "absent".
func decodeCueCatalogAcknowledgeBody(r *http.Request) (v1.CueCatalogAcknowledgeRequest, *v1.Problem) {
	var req v1.CueCatalogAcknowledgeRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxCueCatalogAcknowledgeRequestBodyBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		p := invalidParameterProblem("malformed request body: " + err.Error())
		return v1.CueCatalogAcknowledgeRequest{}, &p
	}
	if req.Revision == "" {
		p := invalidParameterProblem("revision is required")
		return v1.CueCatalogAcknowledgeRequest{}, &p
	}
	if req.Show == "" {
		p := invalidParameterProblem("show is required")
		return v1.CueCatalogAcknowledgeRequest{}, &p
	}
	if req.Generation <= 0 {
		p := invalidParameterProblem("generation must be a positive integer")
		return v1.CueCatalogAcknowledgeRequest{}, &p
	}
	return req, nil
}

func (h *handlers) handlePostNodeCueCatalogAcknowledge(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)
	nodeID := r.PathValue("nodeId")

	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}
	if !h.nodeDeclared(ctx)(nodeID) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("no declared node with id "+strconv.Quote(nodeID)))
		return
	}

	req, problem := decodeCueCatalogAcknowledgeBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	if h.deps.AssetManifests == nil {
		h.writeInternalError(w, now, "acknowledge cue catalog", errCueCatalogStoreNotWired)
		return
	}

	if err := h.deps.AssetManifests.PutNodeCueCatalogAck(ctx, store.NodeCueCatalogAckRecord{
		NodeID: nodeID, Revision: req.Revision, ShowID: req.Show, Generation: req.Generation, AcknowledgedAt: now,
	}); err != nil {
		h.writeInternalError(w, now, "store cue catalog acknowledgement", err)
		return
	}

	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionCueCatalogAcknowledge, Target: nodeID, Kind: identity.AuditOutcome,
		Params:        map[string]any{"revision": req.Revision, "show": req.Show, "generation": req.Generation},
		OutcomeReason: "acknowledged",
	}
	if !h.writeAuditOrFail(ctx, w, now, entry) {
		return
	}

	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		h.writeInternalError(w, now, "resolve active show", err)
		return
	}

	resp := v1.CueCatalogAcknowledgeResponse{
		ServerTime: formatTime(now), Node: nodeID, AcknowledgedRevision: req.Revision,
		AcknowledgedAt: formatTime(now),
	}
	if !active.Configured {
		// No active show to resolve a current revision against: there is
		// no "current" this acknowledgement could match, so it is stale by
		// construction (H3 spec section 4: "no partial state").
		resp.Configured = false
		resp.Status = v1.CueCatalogStatusStale
		jsonWrite(w, resp)
		return
	}

	catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
	if err != nil {
		h.writeInternalError(w, now, "resolve cue catalog", err)
		return
	}
	resp.Configured = true
	resp.CurrentRevision = catalog.Revision
	if req.Revision == catalog.Revision {
		resp.Status = v1.CueCatalogStatusCurrent
	} else {
		resp.Status = v1.CueCatalogStatusStale
	}
	jsonWrite(w, resp)
}

// --- mapping: assetsync.Catalog / pkg/cuecatalog -> wire ---

func mapCueCatalogEntries(entries []cuecatalog.Entry) []v1.CueCatalogEntry {
	out := make([]v1.CueCatalogEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, v1.CueCatalogEntry{
			CueID: e.CueID, CueRevision: e.CueRevision, Outputs: mapCueCatalogOutputs(e.Outputs),
		})
	}
	return out
}

func mapCueCatalogOutputs(o cuecatalog.Outputs) v1.CueCatalogOutputs {
	var out v1.CueCatalogOutputs
	if o.Render != nil {
		out.Render = &v1.CueCatalogRenderOutput{
			Sequence: o.Render.Sequence, AssetHashes: emptyIfNil(o.Render.AssetHashes),
		}
	}
	if o.Audio != nil {
		out.Audio = &v1.CueCatalogAudioOutput{
			Asset: o.Audio.Asset, StartOffsetMillis: o.Audio.StartOffsetMillis,
			AssetHashes: emptyIfNil(o.Audio.AssetHashes),
		}
	}
	if o.LTC != nil {
		out.LTC = &v1.CueCatalogLTCOutput{StartOffsetMillis: o.LTC.StartOffsetMillis}
	}
	if o.Announcement != nil {
		out.Announcement = &v1.CueCatalogAnnouncementOutput{
			Policy: o.Announcement.Policy, DuckGainDb: o.Announcement.DuckGainDb, FadeMillis: o.Announcement.FadeMillis,
		}
	}
	return out
}

// emptyIfNil returns hashes, or an empty (never nil) slice — a JSON array
// field must never render as null for a Cue whose output simply has no
// matching asset uploaded yet.
func emptyIfNil(hashes []string) []string {
	if hashes == nil {
		return []string{}
	}
	return hashes
}

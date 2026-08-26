package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"

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
			AcknowledgedStatus: v1.CueCatalogStatusNeverAcknowledged,
		})
		return
	}

	ackStatus, ackRevision, ackAt, err := h.resolveCueCatalogAcknowledgedFields(ctx, nodeID, "")
	if err != nil {
		h.writeInternalError(w, now, "get node cue catalog acknowledgement", err)
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
			AcknowledgedStatus: ackStatus, AcknowledgedRevision: ackRevision, AcknowledgedAt: ackAt,
		})
		return
	}

	catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
	if err != nil {
		h.writeInternalError(w, now, "resolve cue catalog", err)
		return
	}
	generation := catalog.Generation

	// Recomputed against catalog.Revision now that it is known. The
	// no-active-show lookup above deliberately never treats a stored
	// acknowledgement as "current" (mirrors
	// handlePostNodeCueCatalogAcknowledge's own "no current to match"
	// rule), so a real active show's revision must be resolved first.
	ackStatus, ackRevision, ackAt, err = h.resolveCueCatalogAcknowledgedFields(ctx, nodeID, catalog.Revision)
	if err != nil {
		h.writeInternalError(w, now, "get node cue catalog acknowledgement", err)
		return
	}

	jsonWrite(w, v1.CueCatalogResponse{
		ServerTime: formatTime(now), Node: catalog.Node, Configured: true,
		Show: catalog.Show, Generation: &generation, Revision: catalog.Revision,
		Entries:              mapCueCatalogEntries(catalog.Entries),
		AcknowledgedStatus:   ackStatus,
		AcknowledgedRevision: ackRevision,
		AcknowledgedAt:       ackAt,
	})
}

// resolveCueCatalogAcknowledgedFields reads nodeID's persisted cue-catalog
// acknowledgement ([store.GetNodeCueCatalogAck]) and turns it into
// CueCatalogResponse's three-way verdict without ever performing a write:
// [v1.CueCatalogStatusNeverAcknowledged] when nodeID has never
// acknowledged anything ([store.ErrNodeCueCatalogAckNotFound]),
// [v1.CueCatalogStatusCurrent] when the acknowledged revision equals
// currentRevision, and [v1.CueCatalogStatusStale] otherwise, including
// when currentRevision is "" (no active show resolved), matching
// handlePostNodeCueCatalogAcknowledge's own rule that there is no
// "current" for an unconfigured active show to match. The returned
// revision/timestamp pointers are both nil exactly when the status is
// never-acknowledged. A non-nil error means a genuine store failure, which
// the caller must turn into a 500 rather than reporting
// never-acknowledged.
func (h *handlers) resolveCueCatalogAcknowledgedFields(ctx context.Context, nodeID, currentRevision string) (status string, revision, acknowledgedAt *string, err error) {
	ack, err := h.deps.AssetManifests.GetNodeCueCatalogAck(ctx, nodeID)
	if errors.Is(err, store.ErrNodeCueCatalogAckNotFound) {
		return v1.CueCatalogStatusNeverAcknowledged, nil, nil, nil
	}
	if err != nil {
		return "", nil, nil, err
	}
	status = v1.CueCatalogStatusStale
	if currentRevision != "" && ack.Revision == currentRevision {
		status = v1.CueCatalogStatusCurrent
	}
	rev := ack.Revision
	at := formatTime(ack.AcknowledgedAt)
	return status, &rev, &at, nil
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

	// Resolved BEFORE anything is validated or stored: req.Show and
	// req.Generation are a CALLER's claim, and storing or auditing them
	// verbatim without checking them against this coordinator's own
	// resolution would let a caller record (and have audited) a show and
	// generation that were never true — a fresh-reviewer finding fixed
	// here. The active show is also what the response's own resolution
	// below needs, so it is only ever resolved once.
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		h.writeInternalError(w, now, "resolve active show", err)
		return
	}
	if !active.Configured || req.Show != active.ShowID || req.Generation != active.Generation {
		detail := fmt.Sprintf("acknowledgement names show %q generation %d, which does not match this coordinator's currently resolved active show", req.Show, req.Generation)
		if active.Configured {
			detail = fmt.Sprintf("acknowledgement names show %q generation %d, but the active show is %q generation %d", req.Show, req.Generation, active.ShowID, active.Generation)
		}
		writeProblem(w, h.logger, now, invalidParameterProblem(detail))
		return
	}

	// ADR-024 decision 11's dispatch/outcome split, matching session.go's
	// handleDeleteSession precedent: the Dispatch entry is written and
	// must succeed BEFORE the store write runs (a write that cannot be
	// attributed does not proceed); the Outcome entry below is best-effort
	// and never gates the response, since by then the store write has
	// already happened and refusing to answer would only hide it from the
	// caller. This used to run the store write BEFORE any audit entry at
	// all, so an audit failure returned an error with the acknowledgement
	// already written — a second fresh-reviewer finding fixed here.
	commandID := uuid.NewString()
	if !h.writeAuditOrFail(ctx, w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionCueCatalogAcknowledge, Target: nodeID,
		Kind: identity.AuditDispatch, CommandID: commandID,
		Params: map[string]any{"revision": req.Revision, "show": req.Show, "generation": req.Generation},
	}) {
		return
	}

	if err := h.deps.AssetManifests.PutNodeCueCatalogAck(ctx, store.NodeCueCatalogAckRecord{
		NodeID: nodeID, Revision: req.Revision, ShowID: req.Show, Generation: req.Generation, AcknowledgedAt: now,
	}); err != nil {
		h.writeInternalError(w, now, "store cue catalog acknowledgement", err)
		return
	}

	outcomeNow := h.now()
	outcome := identity.AuditEntry{
		Timestamp: outcomeNow, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionCueCatalogAcknowledge, Target: nodeID,
		Kind: identity.AuditOutcome, CommandID: commandID,
		Params:        map[string]any{"revision": req.Revision, "show": req.Show, "generation": req.Generation},
		OutcomeReason: "acknowledged",
	}
	if h.deps.Identity != nil {
		if err := h.deps.Identity.WriteAudit(ctx, outcome); err != nil {
			h.logWarn("cue catalog acknowledge outcome audit write failed", "node", nodeID, "error", err)
		}
	}

	resp := v1.CueCatalogAcknowledgeResponse{
		ServerTime: formatTime(now), Node: nodeID, AcknowledgedRevision: req.Revision,
		AcknowledgedAt: formatTime(now),
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
			Sequence: o.Render.Sequence, Filename: o.Render.Filename, AssetHashes: emptyIfNil(o.Render.AssetHashes),
		}
	}
	if o.Audio != nil {
		out.Audio = &v1.CueCatalogAudioOutput{
			Asset: o.Audio.Asset, Filename: o.Audio.Filename, StartOffsetMillis: o.Audio.StartOffsetMillis,
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

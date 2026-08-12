package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file implements BUILD-PLAN Step 7 seam B: node discovery and
// declaration (RES-008 D2/D6). The idea, stated once here because getting
// it backwards crosses ADR-003: a discovery run PROPOSES what it observes,
// and an operator action PROMOTES a proposal to a declaration.
// RecordNodeDiscoverySeen (seam 0, store/nodes_declared.go) can only ever
// touch a node ALREADY declared; nothing in this file creates or deletes a
// declaration except handlePromoteNode and handleDeleteNodeDeclaration,
// both direct operator actions behind config:write.
//
// Discovery here performs NO active probing — no mDNS, no subnet sweep, no
// MultiSync discover ping. It reads what this coordinator already
// observes: agent hellos already in inventory (h.deps.Nodes.Snapshot), and
// configured FPP instances (h.deps.FPP.ListInstances). That narrowing is
// deliberate: active probing puts new traffic on the show LAN, touches
// ADR-013's rules about MultiSync sockets, and would need its own research
// record and bench verification before anyone could trust it — a later
// step's work, not a missing piece of this one. The honest consequence:
// discovery cannot find equipment that has never talked to ShowMesh.

// discoverySourceNode and discoverySourceFPP name where a
// [v1.DiscoveryProposal] came from — the two, and only two, sources a
// discovery run reads per this file's own doc comment.
const (
	discoverySourceNode = "node"
	discoverySourceFPP  = "fpp"
)

// maxDeclarationRequestBodyBytes bounds the request body of every
// discovery/declaration write in this file. SHOWMESH HYPOTHESIS, matching
// [maxSessionRequestBodyBytes]'s own posture: generous for
// {"label":string,"notes":string} or {"confirm":bool} with headroom, small
// enough to cost nothing to enforce against an authenticated (so already
// somewhat trusted, but still not unbounded) caller.
const maxDeclarationRequestBodyBytes = 8 * 1024

// --- read-time declaration state (RES-008 D6, B3) ---

// fetchDeclarationContext loads every current node declaration, keyed by
// node id, and the single most recent [store.DiscoveryRunRecord] (nil if
// none has ever been recorded, or none is currently retained — see that
// type's own doc comment: discovery_runs is pruned by retention,
// node_declarations is not, so this is an expected, not exceptional,
// outcome). Every caller that renders one or more [v1.Node] values fetches
// this exactly once per response/render pass, never once per node — see
// [mapNode]'s doc comment for why decl/latestRun are threaded in as
// parameters rather than looked up inside it.
func fetchDeclarationContext(ctx context.Context, ds DeclarationStore) (map[string]store.NodeDeclarationRecord, *store.DiscoveryRunRecord, error) {
	decls, err := ds.ListNodeDeclarations(ctx)
	if err != nil {
		return nil, nil, err
	}
	byNodeID := make(map[string]store.NodeDeclarationRecord, len(decls))
	for _, d := range decls {
		byNodeID[d.NodeID] = d
	}

	runs, err := ds.ListDiscoveryRuns(ctx, 1)
	if err != nil {
		return nil, nil, err
	}
	var latest *store.DiscoveryRunRecord
	if len(runs) > 0 {
		latest = &runs[0]
	}
	return byNodeID, latest, nil
}

// latestDiscoveryRun is [fetchDeclarationContext]'s narrower sibling for a
// caller (handlePromoteNode) that already has the one declaration it needs
// in hand and has no use for the full declared-node map.
func latestDiscoveryRun(ctx context.Context, ds DeclarationStore) (*store.DiscoveryRunRecord, error) {
	runs, err := ds.ListDiscoveryRuns(ctx, 1)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return &runs[0], nil
}

// declPtr looks up nodeID in byNodeID and returns a pointer to a copy of
// the match, or nil — the shape [mapNode] wants for "no declaration
// exists". A pointer into a range variable would alias the loop variable
// across iterations pre-Go-1.22-semantics; taking the address of a fresh
// local copy here sidesteps that regardless of which Go version builds
// this package.
func declPtr(byNodeID map[string]store.NodeDeclarationRecord, nodeID string) *store.NodeDeclarationRecord {
	d, ok := byNodeID[nodeID]
	if !ok {
		return nil
	}
	return &d
}

// mapNodeDeclaration computes B3's four-state discovery verdict. See
// [v1.NodeDeclaration]'s doc comment for what each state means; this
// function is the one place that vocabulary is decided, matching every
// other derived-verdict function in this package (e.g. mapControlPlane,
// deriveInstanceHealth) in being called fresh on every render rather than
// reading a stored verdict — ADR-011: "a verdict is computed on read,
// never stored."
func mapNodeDeclaration(decl *store.NodeDeclarationRecord, latestRun *store.DiscoveryRunRecord) v1.NodeDeclaration {
	if decl == nil {
		return v1.NodeDeclaration{Declared: false, DiscoveryState: "not_applicable"}
	}

	out := v1.NodeDeclaration{
		Declared:                true,
		Label:                   nonEmptyStrPtr(decl.Label),
		Notes:                   nonEmptyStrPtr(decl.Notes),
		DeclaredAt:              strPtr(formatTime(decl.DeclaredAt)),
		DeclaredByPrincipalID:   nonEmptyStrPtr(decl.DeclaredByPrincipalID),
		DeclaredByPrincipalName: nonEmptyStrPtr(decl.DeclaredByPrincipalName),
	}

	// In BOTH "unknown" branches below, this declaration's own last-known
	// positive sighting (if it has ever had one) is still reported rather
	// than left null — B3/ADR-020's "never blank" rule applies to this
	// evidence exactly as it does everywhere else: a run id/time that no
	// longer resolves, or that predates a currently-incomplete run, is
	// still real, non-fabricated history, and reporting it is not the
	// same claim as DiscoveryState itself, which is what actually says
	// whether that history is trustworthy right now.
	if decl.LastDiscoveryRunID != "" {
		out.LastDiscoveryRunID = strPtr(decl.LastDiscoveryRunID)
		out.LastDiscoveredAt = formatTimePtr(decl.LastDiscoveredAt)
	}

	switch {
	case latestRun == nil:
		// "No run has happened, or the run this declaration points at has
		// been pruned" — B3's fourth bullet, deliberately ONE outcome for
		// both root causes: this API cannot and must not guess which,
		// only that no run history is currently available to say anything
		// about absence.
		out.DiscoveryState = "unknown"
		out.DiscoveryReason = strPtr("no discovery run history is available (either no discovery run has ever been performed, or discovery run history has been pruned)")
	case !latestRun.Complete:
		// "The most recent run was incomplete: unknown with the reason,
		// never not_seen." Deliberately does NOT fall back to an older
		// complete run — see this function's own report for why looking
		// further back would be the wrong direction to fail in.
		out.DiscoveryState = "unknown"
		reason := latestRun.Reason
		if reason == "" {
			reason = "the most recent discovery run has not completed"
		}
		out.DiscoveryReason = strPtr(reason)
	case decl.LastDiscoveryRunID == latestRun.ID:
		// LastDiscoveryRunID/LastDiscoveredAt are already set above from
		// decl, and decl.LastDiscoveryRunID == latestRun.ID here by
		// construction, so nothing further to assign.
		out.DiscoveryState = "present"
	default:
		// Not seen BY THE MOST RECENT COMPLETE RUN specifically — the run
		// id/time reported here name THAT run, not this declaration's own
		// (possibly older, possibly now-pruned) last-actually-seen
		// bookkeeping, because "not seen by the most recent complete run"
		// is a statement about that run, not about history in general.
		out.DiscoveryState = "not_seen"
		out.DiscoveryReason = strPtr("not seen by the most recent complete discovery run (" + latestRun.ID + ")")
		out.LastDiscoveryRunID = strPtr(latestRun.ID)
		out.LastDiscoveredAt = formatTimePtr(latestRun.FinishedAt)
	}

	return out
}

func mapDiscoveryRun(rec store.DiscoveryRunRecord) v1.DiscoveryRun {
	return v1.DiscoveryRun{
		ID:                       rec.ID,
		StartedAt:                formatTime(rec.StartedAt),
		FinishedAt:               formatTimePtr(rec.FinishedAt),
		Complete:                 rec.Complete,
		Reason:                   nonEmptyStrPtr(rec.Reason),
		FoundCount:               rec.FoundCount,
		InitiatedByPrincipalID:   rec.InitiatedByPrincipalID,
		InitiatedByPrincipalName: rec.InitiatedByPrincipalName,
	}
}

// --- POST /api/v1/discovery/runs ---

// handleStartDiscoveryRun serves POST /api/v1/discovery/runs, behind
// [handlers.writeGuard](&identity.ScopeConfigWrite, ...). Declaring what
// hardware exists is configuration — ADR-024 decision 4 defines no
// narrower scope for it, so this is a deliberate choice of the closest
// fit rather than a default: see this file's report.
//
// A run never creates, modifies, or deletes a declaration's own identity
// (RES-008 D6) — it only stamps last-seen evidence on declarations ALREADY
// present (via [DeclarationStore.RecordNodeDiscoverySeen], which the store
// layer itself refuses to use to create a row) and reports proposals for
// what it saw that is not declared. complete/reason implement Step 5's
// rule for the fourth time in this codebase: only a complete run may
// support a claim about absence. A run that fails partway is a row with
// complete=0 and a stated reason — never a missing row, never a silent
// partial success — which is why every failure branch below calls
// [handlers.finishDiscoveryRunFailed] rather than simply returning.
func (h *handlers) handleStartDiscoveryRun(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx) // writeGuard has already required ac.ok

	runID := uuid.NewString()

	// Dispatch-style audit entry, written BEFORE anything is recorded,
	// fail-closed per ADR-024 decision 11's default rule for config:write:
	// if this cannot be attributed, nothing happens at all — not even the
	// discovery_runs row itself.
	if !h.writeAuditOrFail(ctx, w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: "discovery.run.start", Target: runID,
		Kind: identity.AuditDispatch, CommandID: runID,
	}) {
		return
	}

	rec, err := h.deps.Discovery.StartDiscoveryRun(ctx, store.DiscoveryRunRecord{
		ID:                       runID,
		InitiatedByPrincipalID:   ac.result.Principal.ID,
		InitiatedByPrincipalName: ac.result.Principal.Name,
	})
	if err != nil {
		h.auditDiscoveryOutcome(r, h.now(), ac, runID, "failed", "failed to start discovery run")
		h.writeInternalError(w, now, "start discovery run", err)
		return
	}

	// Read what this coordinator already observes. No active probing — see
	// this file's doc comment.
	nodeViews, err := h.deps.Nodes.Snapshot(ctx, now)
	if err != nil {
		h.finishDiscoveryRunFailed(r, ac, runID, "failed to list node inventory: "+err.Error())
		h.writeInternalError(w, now, "list nodes for discovery", err)
		return
	}
	fppViews, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		h.finishDiscoveryRunFailed(r, ac, runID, "failed to list FPP instances: "+err.Error())
		h.writeInternalError(w, now, "list fpp instances for discovery", err)
		return
	}
	declared, err := h.deps.Discovery.ListNodeDeclarations(ctx)
	if err != nil {
		h.finishDiscoveryRunFailed(r, ac, runID, "failed to list declared nodes: "+err.Error())
		h.writeInternalError(w, now, "list node declarations for discovery", err)
		return
	}
	declaredSet := make(map[string]struct{}, len(declared))
	for _, d := range declared {
		declaredSet[d.NodeID] = struct{}{}
	}

	// De-duplicate by id (a node id and an FPP instance id share one
	// syntax and, in principle, one namespace — see handleFPPInstance's
	// own comment on that shared syntax) before diffing against declared,
	// so an id observed from both sources is never proposed twice.
	type observedEntity struct {
		id     string
		source string
	}
	var observed []observedEntity
	seen := make(map[string]struct{}, len(nodeViews)+len(fppViews))
	for _, nv := range nodeViews {
		// Liveness must be online, not merely "this node id has a
		// persisted nodes row": inventory.Manager never deletes a nodes
		// row (Step 2), so every node that has ever said hello even once
		// would otherwise count as "observed" forever, and RES-008 D6's
		// not_seen state — the entire reason this seam exists — would be
		// unreachable by any real power-off, only by a row that has
		// literally never existed. A node currently LivenessOffline or
		// LivenessUnknown is exactly RES-008 D2's "powered-off equipment
		// is normal outside display hours" case: this run does not count
		// it as observed, so an already-declared instance of it is left
		// alone and flagged not_seen on read (mapNodeDeclaration), and an
		// undeclared one is not proposed as new hardware to declare while
		// it cannot currently be confirmed.
		if nv.Liveness != inventory.LivenessOnline {
			continue
		}
		if _, dup := seen[nv.NodeID]; dup {
			continue
		}
		seen[nv.NodeID] = struct{}{}
		observed = append(observed, observedEntity{id: nv.NodeID, source: discoverySourceNode})
	}
	for _, fv := range fppViews {
		if _, dup := seen[fv.InstanceID]; dup {
			continue
		}
		seen[fv.InstanceID] = struct{}{}
		observed = append(observed, observedEntity{id: fv.InstanceID, source: discoverySourceFPP})
	}

	var proposals []v1.DiscoveryProposal
	for _, e := range observed {
		if _, isDeclared := declaredSet[e.id]; isDeclared {
			// RecordNodeDiscoverySeen only ever stamps evidence on an
			// ALREADY-declared node — never creates one (RES-008 D6). This
			// is the ONLY write this handler performs against an existing
			// declaration.
			if err := h.deps.Discovery.RecordNodeDiscoverySeen(ctx, e.id, runID, now); err != nil {
				h.finishDiscoveryRunFailed(r, ac, runID, "failed to record discovery evidence for "+strconv.Quote(e.id)+": "+err.Error())
				h.writeInternalError(w, now, "record node discovery seen", err)
				return
			}
			continue
		}
		proposals = append(proposals, v1.DiscoveryProposal{NodeID: e.id, Source: e.source})
	}

	if err := h.deps.Discovery.FinishDiscoveryRun(ctx, runID, true, "", int64(len(observed))); err != nil {
		// The run's declarations-seen bookkeeping above already committed;
		// only the terminal row update failed. Still refuse to claim
		// success to the caller — the row is left complete=false (its
		// StartDiscoveryRun-time state), which correctly renders as
		// "unknown", never as a false "complete" claim this handler cannot
		// actually back up.
		h.auditDiscoveryOutcome(r, h.now(), ac, runID, "failed", "failed to finish discovery run")
		h.writeInternalError(w, now, "finish discovery run", err)
		return
	}

	h.auditDiscoveryOutcome(r, h.now(), ac, runID, "succeeded", "")

	finishedAt := h.now()
	rec.Complete = true
	rec.FinishedAt = &finishedAt
	rec.FoundCount = int64(len(observed))

	jsonWrite(w, v1.DiscoveryRunResponse{
		ServerTime: formatTime(h.now()),
		Run:        mapDiscoveryRun(rec),
		Proposals:  nonNilProposalSlice(proposals),
	})
}

// finishDiscoveryRunFailed marks runID complete=false with reason
// (best-effort — a failure here is logged, never escalated: the caller is
// already reporting the original failure to the client) and writes a
// best-effort failed-outcome audit entry. Called from every failure branch
// in [handlers.handleStartDiscoveryRun] after the row has been started, so
// a run that fails partway is always a row with complete=0 and a stated
// reason — never left dangling at "started, never finished" forever.
func (h *handlers) finishDiscoveryRunFailed(r *http.Request, ac authContext, runID, reason string) {
	if err := h.deps.Discovery.FinishDiscoveryRun(r.Context(), runID, false, reason, 0); err != nil && h.logger != nil {
		h.logger.Warn("api: failed to mark discovery run as incomplete after an earlier failure", "run_id", runID, "error", err)
	}
	h.auditDiscoveryOutcome(r, h.now(), ac, runID, "failed", reason)
}

// auditDiscoveryOutcome writes a best-effort Outcome audit entry
// correlated to runID's earlier Dispatch entry (ADR-024 decision 11's
// dispatch/outcome split). Best-effort, never gating the response, exactly
// like [handlers.handleDeleteSession]'s own outcome write: the effect (or
// its absence) has already happened by the time this is called, so
// refusing to answer the caller over a SECOND audit-write failure would
// only hide what already occurred, not undo it.
func (h *handlers) auditDiscoveryOutcome(r *http.Request, now time.Time, ac authContext, runID, outcome, reason string) {
	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: "discovery.run.start", Target: runID,
		Kind: identity.AuditOutcome, CommandID: runID,
		Outcome:      outcome,
		OutcomeState: string(observation.StateCurrent),
	}
	if reason != "" {
		entry.OutcomeReason = reason
	}
	if err := h.deps.Identity.WriteAudit(r.Context(), entry); err != nil && h.logger != nil {
		h.logger.Warn("api: failed to write discovery.run.start outcome audit entry", "error", err, "command_id", runID)
	}
}

// nonNilProposalSlice matches this API's standing "absent evidence is
// stated, never omitted" rule applied to a collection: a run that found
// nothing to propose still renders "proposals": [], never null.
func nonNilProposalSlice(v []v1.DiscoveryProposal) []v1.DiscoveryProposal {
	if v == nil {
		return []v1.DiscoveryProposal{}
	}
	return v
}

// --- POST /api/v1/nodes/{nodeId}/declaration ---

// handlePromoteNode serves POST /api/v1/nodes/{nodeId}/declaration, behind
// [handlers.writeGuard](&identity.ScopeConfigWrite, ...). Promotes nodeId
// to declared, or — idempotently, per [store.Store.DeclareNode] — updates
// an already-declared node's label and notes. This is a coordinator-local
// state change, so ADR-024 decision 11's same-transaction rule applies in
// full: the declaration write and its audit entry land in one transaction
// via [identity.Service.AuditedWrite], or neither does.
func (h *handlers) handlePromoteNode(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}

	var req v1.DeclareNodeRequest
	if r.ContentLength != 0 {
		dec := json.NewDecoder(io.LimitReader(r.Body, maxDeclarationRequestBodyBytes+1))
		if err := dec.Decode(&req); err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(
				"request body, if present, must be JSON matching {\"label\":string,\"notes\":string}"))
			return
		}
	}

	var declared store.NodeDeclarationRecord
	err := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		rec, err := tx.DeclareNode(ctx, store.NodeDeclarationRecord{
			NodeID:                  nodeID,
			Label:                   req.Label,
			Notes:                   req.Notes,
			DeclaredByPrincipalID:   ac.result.Principal.ID,
			DeclaredByPrincipalName: ac.result.Principal.Name,
		})
		if err != nil {
			return identity.AuditEntry{}, err
		}
		declared = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "node.declare", Target: nodeID,
			Params: map[string]any{"label": req.Label, "notes": req.Notes},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if err != nil {
		// Whether this is [identity.ErrAuditWrite] (the audit append
		// itself failed) or fn's own error (e.g. a store failure inside
		// DeclareNode), ADR-024 decision 11's same-transaction rule has
		// already done its job by the time control reaches here: on ANY
		// error, [identity.Service.AuditedWrite] has rolled the whole
		// transaction back, so the declaration this handler was about to
		// create/update is absent afterwards exactly as if this request
		// never happened. Both cases are reported identically as an
		// internal error — matching handleClaimBootstrap/
		// handleCreateSession's identical posture for the same call.
		h.writeInternalError(w, now, "declare node", err)
		return
	}

	latestRun, err := latestDiscoveryRun(ctx, h.deps.Discovery)
	if err != nil {
		h.writeInternalError(w, now, "fetch latest discovery run after declare", err)
		return
	}
	jsonWrite(w, v1.NodeDeclarationResponse{
		ServerTime:  formatTime(h.now()),
		Declaration: mapNodeDeclaration(&declared, latestRun),
	})
}

// --- DELETE /api/v1/nodes/{nodeId}/declaration ---

// handleDeleteNodeDeclaration serves DELETE /api/v1/nodes/{nodeId}/declaration,
// behind [handlers.writeGuard](&identity.ScopeConfigWrite, ...). Requires
// an explicit {"confirm":true} body — BUILD-PLAN Step 7 seam B B2: "so a
// mis-issued call cannot quietly remove inventory." The UI's own
// confirmation dialog is in addition to this, never instead of it: a
// script or a curl call gets no shortcut around this check just because it
// is not a browser. Audited the same way as promote, via
// [identity.Service.AuditedWrite] — ADR-024 decision 11's same-transaction
// rule in full.
//
// Discovery itself never calls this — see discovery.go's own doc comment
// and [store.Store.DeleteNodeDeclaration]'s: this is the one and only path
// that removes a declaration, and it is always a direct, confirmed
// operator action.
func (h *handlers) handleDeleteNodeDeclaration(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}

	var req v1.DeleteNodeDeclarationRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxDeclarationRequestBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"request body must be JSON matching {\"confirm\":true}"))
		return
	}
	if !req.Confirm {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"deleting a node declaration requires an explicit {\"confirm\":true} body, so a mis-issued call cannot quietly remove inventory"))
		return
	}

	err := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if err := tx.DeleteNodeDeclaration(ctx, nodeID); err != nil {
			return identity.AuditEntry{}, err
		}
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "node.declaration.delete", Target: nodeID,
			Kind: identity.AuditAdmin,
		}, nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNodeDeclarationNotFound) {
			writeProblem(w, h.logger, now, resourceNotFoundProblem("no declared node with id "+strconv.Quote(nodeID)))
			return
		}
		h.writeInternalError(w, now, "delete node declaration", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is this seam's own HTTP surface: GET /nodes/{nodeId}/assets/unused
// (which of a node's held assets no Cue in its resolved catalog references)
// and POST /nodes/{nodeId}/assets/remove (dispatching asset.remove for one).
// The GET route adds no second readiness or usage computation - it reuses
// [assetsync.BuildNodeManifest]'s own Extra field and the SAME
// [assetsync.ResolveCueCatalog] resolution the cue-catalog route already
// serves (see internal/coordinator/assetsync/unused.go's own doc comment).
// The POST route is shaped like cuecatalogdeploy.go's dispatch-and-await
// core (resolve THIS coordinator's own params, refuse before ever
// dispatching, record durably before acting, confirm by evidence), narrowed
// to one asset's identity instead of a whole catalog.

// auditActionAssetRemove is IDENTIFIER-REGISTER.md's own reservation for
// this route's audit action string, matching auditActionCueCatalogDeploy's
// identical role one file over.
const auditActionAssetRemove = "asset.remove"

// assetRemoveConfirmDeadline bounds how long a removal dispatch waits for
// the node's own result-topic reply, mirroring cueCatalogDeployConfirmDeadline's
// identical reasoning and magnitude: a package var, not a const, only so a
// test can shrink it deterministically.
var assetRemoveConfirmDeadline = 15 * time.Second

// assetRemoveWireDeadline bounds how stale a removal dispatch may be before
// the agent refuses it, mirroring cueCatalogDeployWireDeadline.
const assetRemoveWireDeadline = 60 * time.Second

// maxRemoveNodeAssetRequestBodyBytes bounds this endpoint's request body  -
// it carries only a content hash and an optional idempotency key.
const maxRemoveNodeAssetRequestBodyBytes = 4 << 10

// --- GET /nodes/{nodeId}/assets/unused ---

func (h *handlers) handleGetNodeUnusedAssets(w http.ResponseWriter, r *http.Request) {
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
		reason := unwiredAssetManifestReason
		jsonWrite(w, v1.NodeUnusedAssetsResponse{
			ServerTime: formatTime(now), Node: nodeID, State: string(assetsync.ManifestUnknown), Reason: &reason,
			Unused: []v1.UnusedAsset{},
		})
		return
	}

	m, err := assetsync.BuildNodeManifest(ctx, h.deps.AssetManifests, now, h.deps.AssetSettings.InventoryInterval(), nodeID)
	if err != nil {
		h.writeInternalError(w, now, "build node asset manifest", err)
		return
	}

	resp := v1.NodeUnusedAssetsResponse{ServerTime: formatTime(now), Node: nodeID, State: string(m.State), Unused: []v1.UnusedAsset{}}
	if m.State == assetsync.ManifestUnknown {
		// Missing evidence is withheld, never rendered as "zero unused
		// assets" - see assetsync.UnusedAssetsForNode's own doc comment and
		// mapNodeAssetManifest's identical rule for the sibling route.
		reason := m.Reason
		resp.Reason = &reason
		jsonWrite(w, resp)
		return
	}
	observedAt := formatTime(m.ObservedAt)
	resp.ObservedAt = &observedAt

	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		h.writeInternalError(w, now, "resolve active show", err)
		return
	}
	if !active.Configured {
		// ComputeNodeManifest never reaches Ready/NotReady without an
		// active show configured (see its own doc comment); a defensive
		// re-check only for the vanishingly rare race where show.active
		// changed between BuildNodeManifest's own resolution and this one  -
		// rendered the same honest-unknown way as a genuinely unconfigured
		// show, never a guess at what m.Extra meant against no show at all.
		reason := "no active show is configured"
		resp.State = string(assetsync.ManifestUnknown)
		resp.Reason = &reason
		resp.ObservedAt = nil
		jsonWrite(w, resp)
		return
	}

	unused, err := assetsync.UnusedAssetsForNode(ctx, h.deps.AssetManifests, active.ShowID, m)
	if err != nil {
		h.writeInternalError(w, now, "compute unused assets", err)
		return
	}
	resp.Unused = mapUnusedAssets(unused)
	jsonWrite(w, resp)
}

func mapUnusedAssets(in []assetsync.UnusedAsset) []v1.UnusedAsset {
	out := make([]v1.UnusedAsset, 0, len(in))
	for _, u := range in {
		out = append(out, v1.UnusedAsset{ContentHash: u.ContentHash, Filename: u.Filename, SizeBytes: u.SizeBytes, Sequence: u.SequenceID})
	}
	return out
}

// --- POST /nodes/{nodeId}/assets/remove ---

// assetRemoveWireParams is "asset.remove"'s params, mirroring
// internal/agent/assets.go's parseAssetRemoveParams field for field  -
// independently reproduced, not shared, matching cueCatalogDeployWireParams'
// identical each-side-of-a-wire-boundary-decodes-independently convention.
type assetRemoveWireParams struct {
	ContentHash string `json:"contentHash"`
	Filename    string `json:"filename"`
}

// removeNodeAssetRequestIdentity is the caller's own unresolved request
// shape, stored in commands.caller_intent tagged
// store.CallerIntentAssetRemove - mirrors cueCatalogDeployRequestIdentity's
// identical role, narrowed to this action's one identifying field.
type removeNodeAssetRequestIdentity struct {
	NodeID      string `json:"node"`
	ContentHash string `json:"contentHash"`
}

// removeNodeAssetResultPayload is the JSON this file persists into
// store.CommandRecord.ResultJSON, mirroring cueCatalogDeployResultPayload's
// identical role.
type removeNodeAssetResultPayload struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

func (h *handlers) handlePostRemoveNodeAsset(w http.ResponseWriter, r *http.Request) {
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

	var req v1.RemoveNodeAssetRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRemoveNodeAssetRequestBodyBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(`request body must be JSON matching {"contentHash":string,"idempotencyKey":string?}`))
		return
	}
	if req.ContentHash == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("contentHash is required"))
		return
	}

	if h.deps.AssetManifests == nil || h.deps.Commands == nil {
		h.writeInternalError(w, now, "remove node asset", errors.New("no asset manifest store or command store is configured on this coordinator"))
		return
	}

	ac := authFromContext(ctx)
	issuerID := ac.result.Principal.ID
	issuerName := ac.result.Principal.Name
	if issuerID == "" {
		issuerID = renderIssuerPrincipalIDMissing
	}

	idempotencyKey := req.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = uuid.NewString()
	}

	if req.IdempotencyKey != "" {
		existing, lookupErr := h.deps.Commands.GetCommandByIdempotencyKey(ctx, idempotencyKey)
		switch {
		case lookupErr == nil:
			result, problem := resolveRemoveNodeAssetReplay(existing, nodeID)
			if problem != nil {
				writeProblem(w, h.logger, now, *problem)
				return
			}
			jsonWrite(w, v1.RemoveNodeAssetResponse{ServerTime: formatTime(h.now()), Command: result})
			return
		case errors.Is(lookupErr, store.ErrCommandNotFound):
			// Genuinely new key - fall through to resolution and dispatch.
		default:
			h.writeInternalError(w, now, "look up asset remove command by idempotency key", lookupErr)
			return
		}
	}

	// The filename dispatched to the agent is THIS coordinator's own
	// evidence of what nodeID currently holds under contentHash, never
	// accepted from the caller: a caller only ever learns a content hash
	// worth removing from GET .../assets/unused, which is this exact
	// inventory row's own Filename.
	inventory, err := h.deps.AssetManifests.GetNodeAssetInventory(ctx, nodeID)
	if err != nil {
		h.writeInternalError(w, now, "get node asset inventory", err)
		return
	}
	filename := ""
	for _, item := range inventory {
		if item.ContentHash == req.ContentHash {
			filename = item.RuntimeFilename
			break
		}
	}
	if filename == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			"node %q's inventory holds no asset with content hash %q; nothing to remove", nodeID, req.ContentHash)))
		return
	}

	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		h.writeInternalError(w, now, "resolve active show", err)
		return
	}
	if !active.Configured {
		writeProblem(w, h.logger, now, invalidParameterProblem("no active show is configured; there is no Cue catalog to check this asset against"))
		return
	}
	catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
	if err != nil {
		h.writeInternalError(w, now, "resolve cue catalog", err)
		return
	}
	if cues := assetsync.CuesReferencingAsset(catalog, req.ContentHash); len(cues) > 0 {
		writeProblem(w, h.logger, now, assetInUseByCueProblem(nodeID, req.ContentHash, cues))
		return
	}

	raw, err := json.Marshal(assetRemoveWireParams{ContentHash: req.ContentHash, Filename: filename})
	if err != nil {
		h.writeInternalError(w, now, "encode asset.remove params", err)
		return
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		h.writeInternalError(w, now, "decode asset.remove params back into a map", err)
		return
	}
	paramsJSON, err := canonicalParamsJSON(params)
	if err != nil {
		h.writeInternalError(w, now, "encode params", err)
		return
	}
	identityJSON, _ := json.Marshal(removeNodeAssetRequestIdentity{NodeID: nodeID, ContentHash: req.ContentHash})

	commandID := uuid.NewString()
	rec := store.CommandRecord{
		ID: commandID, IdempotencyKey: idempotencyKey, Action: auditActionAssetRemove,
		TargetKind: "node", TargetID: nodeID, ParamsJSON: paramsJSON,
		IssuerPrincipalID: issuerID, IssuerPrincipalName: issuerName,
		CallerIntent:       store.FormatCallerIntent(store.CallerIntentAssetRemove, string(identityJSON)),
		ConfirmationMethod: "evidence", State: "pending",
	}
	if _, err := h.deps.Commands.InsertCommand(ctx, rec); err != nil {
		var dup *store.DuplicateCommandError
		if errors.As(err, &dup) {
			result, problem := resolveRemoveNodeAssetReplay(dup.Existing, nodeID)
			if problem != nil {
				writeProblem(w, h.logger, now, *problem)
				return
			}
			jsonWrite(w, v1.RemoveNodeAssetResponse{ServerTime: formatTime(h.now()), Command: result})
			return
		}
		h.writeInternalError(w, now, "insert asset remove command", err)
		return
	}

	cmdTopic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		h.writeInternalError(w, now, "build cmd topic", err)
		return
	}
	resultTopic, err := mqttproto.ResultTopic(nodeID, commandID)
	if err != nil {
		h.writeInternalError(w, now, "build result topic", err)
		return
	}
	deadline := now.Add(assetRemoveWireDeadline)
	payload := mqttproto.CmdPayload{
		CommandID: commandID, IdempotencyKey: idempotencyKey, Action: auditActionAssetRemove,
		Target: mqttproto.CmdTarget{Kind: "node", ID: nodeID}, Params: params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: issuerID, PrincipalName: issuerName},
		ConfirmationMethod: "evidence",
		Deadline:           &deadline,
	}
	env, err := mqttproto.NewCmdEnvelope(func() time.Time { return now }, nodeID, payload)
	if err != nil {
		h.writeInternalError(w, now, "build cmd envelope", err)
		return
	}
	rawEnv, err := json.Marshal(env)
	if err != nil {
		h.writeInternalError(w, now, "marshal cmd envelope", err)
		return
	}

	h.writeAssetRemoveAudit(ctx, now, identity.AuditDispatch, ac, nodeID, commandID, idempotencyKey, req.ContentHash, "")

	// From here on every write is on bgCtx, matching cuecatalogdeploy.go's
	// identical bgCtx cutover: the command is already durably recorded and
	// about to be dispatched, and a caller walking away must not abort the
	// dispatch or its post-dispatch bookkeeping.
	bgCtx := context.WithoutCancel(ctx)

	dispatchedAt := now
	if h.deps.AudioPublisher == nil {
		h.writeInternalError(w, now, "remove node asset", errors.New("no command publish-and-await capability is configured on this coordinator"))
		return
	}
	msg, err := h.deps.AudioPublisher.AwaitResponse(bgCtx, broker.ResponseRequest{
		PublishTopic: cmdTopic, PublishPayload: rawEnv,
		PublishQoS: mqttproto.CmdDeliveryPolicy.QoS, PublishRetain: mqttproto.CmdDeliveryPolicy.Retain,
		ResponseTopic: resultTopic, ResponseQoS: mqttproto.ResultDeliveryPolicy.QoS,
		Deadline: assetRemoveConfirmDeadline,
		Match: func(m broker.Message) bool {
			return assetRemoveResultCorrelates(m.Payload, nodeID, commandID, idempotencyKey)
		},
	})
	if err != nil {
		if errors.Is(err, broker.ErrResponseFailedBeforePublish) {
			resolvedAt := h.now()
			resultJSON, _ := json.Marshal(removeNodeAssetResultPayload{Outcome: mqttproto.OutcomeFailed, Reason: err.Error()})
			_ = h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{
				ResolvedAt: &resolvedAt, State: strPtr("failed"), ResultJSON: strPtr(string(resultJSON)),
				OutcomeState: strPtr("collection_failed"), OutcomeReason: strPtr(err.Error()),
			})
			h.writeAssetRemoveAudit(bgCtx, now, identity.AuditOutcome, ac, nodeID, commandID, idempotencyKey, req.ContentHash, "failed: "+err.Error())
			h.writeInternalError(w, now, "dispatch asset.remove", err)
			return
		}
		_ = h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{DispatchedAt: &dispatchedAt})
		resolvedAt := h.now()
		reason := err.Error()
		resultJSON, _ := json.Marshal(removeNodeAssetResultPayload{Outcome: mqttproto.OutcomeUnconfirmed, Reason: reason})
		_ = h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{
			ResolvedAt: &resolvedAt, State: strPtr("resolved"), ResultJSON: strPtr(string(resultJSON)),
			OutcomeState: strPtr("not_collected"), OutcomeReason: strPtr(reason),
		})
		h.writeAssetRemoveAudit(bgCtx, now, identity.AuditOutcome, ac, nodeID, commandID, idempotencyKey, req.ContentHash, "unconfirmed: "+reason)
		jsonWrite(w, v1.RemoveNodeAssetResponse{
			ServerTime: formatTime(h.now()),
			Command: v1.RemoveNodeAssetResult{
				CommandID: commandID, IdempotencyKey: idempotencyKey, Node: nodeID, ContentHash: req.ContentHash,
				Outcome: mqttproto.OutcomeUnconfirmed, Reason: reason,
				DispatchedAt: strPtr(formatTime(dispatchedAt)),
			},
		})
		return
	}

	env2, err := mqttproto.DecodeEnvelope(msg.Payload)
	var res mqttproto.ResultPayload
	if err == nil {
		res, err = mqttproto.DecodeResultPayload(env2)
	}
	if err != nil {
		h.writeInternalError(w, now, "decode asset.remove result", err)
		return
	}

	resolvedAt := h.now()
	resultJSON, _ := json.Marshal(removeNodeAssetResultPayload{Outcome: res.Outcome, Reason: res.Reason})
	_ = h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, ResolvedAt: &resolvedAt, State: strPtr("resolved"),
		ResultJSON: strPtr(string(resultJSON)), OutcomeState: strPtr(res.Outcome), OutcomeReason: strPtr(res.Reason),
	})
	h.writeAssetRemoveAudit(bgCtx, now, identity.AuditOutcome, ac, nodeID, commandID, idempotencyKey, req.ContentHash, res.Outcome+": "+res.Reason)

	resolvedFmt := formatTime(resolvedAt)
	jsonWrite(w, v1.RemoveNodeAssetResponse{
		ServerTime: formatTime(h.now()),
		Command: v1.RemoveNodeAssetResult{
			CommandID: commandID, IdempotencyKey: idempotencyKey, Node: nodeID, ContentHash: req.ContentHash,
			Outcome: res.Outcome, Reason: res.Reason,
			DispatchedAt: strPtr(formatTime(dispatchedAt)), ResolvedAt: &resolvedFmt,
		},
	})
}

// assetRemoveResultCorrelates mirrors cueCatalogDeployResultCorrelates
// exactly, narrowed to this action's own name.
func assetRemoveResultCorrelates(payload []byte, nodeID, commandID, idempotencyKey string) bool {
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil || env.NodeID != nodeID {
		return false
	}
	res, err := mqttproto.DecodeResultPayload(env)
	if err != nil {
		return false
	}
	return res.CommandID == commandID && res.IdempotencyKey == idempotencyKey && res.Action == auditActionAssetRemove
}

// resolveRemoveNodeAssetReplay answers a replayed idempotency key against
// existing's own stored row - mirrors resolveCueCatalogDeployReplay's
// identical shape, narrowed to this action's single-field identity.
func resolveRemoveNodeAssetReplay(existing store.CommandRecord, nodeID string) (v1.RemoveNodeAssetResult, *v1.Problem) {
	if existing.Action != auditActionAssetRemove || existing.TargetID != nodeID {
		p := renderCommandReplayConflictProblem(existing.ID, existing.Action, existing.TargetID, auditActionAssetRemove, nodeID)
		return v1.RemoveNodeAssetResult{}, &p
	}
	var res removeNodeAssetResultPayload
	if err := json.Unmarshal([]byte(existing.ResultJSON), &res); err != nil {
		p := assetRemoveReplayUndecodableProblem(existing.ID, nodeID)
		return v1.RemoveNodeAssetResult{}, &p
	}
	var resolvedAt *string
	if existing.ResolvedAt != nil {
		resolvedAt = strPtr(formatTime(*existing.ResolvedAt))
	}
	var dispatchedAt *string
	if existing.DispatchedAt != nil {
		dispatchedAt = strPtr(formatTime(*existing.DispatchedAt))
	}
	var reqID removeNodeAssetRequestIdentity
	payload, _ := store.CallerIntentPayload(store.CallerIntentAssetRemove, existing.CallerIntent)
	if err := json.Unmarshal([]byte(payload), &reqID); err != nil || (payload != "" && reqID.ContentHash == "") {
		p := assetRemoveReplayUndecodableProblem(existing.ID, nodeID)
		return v1.RemoveNodeAssetResult{}, &p
	}
	return v1.RemoveNodeAssetResult{
		CommandID: existing.ID, IdempotencyKey: existing.IdempotencyKey, Node: nodeID, Replay: true,
		ContentHash: reqID.ContentHash, Outcome: res.Outcome, Reason: res.Reason,
		DispatchedAt: dispatchedAt, ResolvedAt: resolvedAt,
	}, nil
}

// assetRemoveReplayUndecodableProblem mirrors
// cueCatalogDeployReplayUndecodableProblem's identical reasoning: an
// idempotency key already used for a row this route cannot trust as its own
// family is reported as a conflict, never answered as a confident, wrong
// replay.
func assetRemoveReplayUndecodableProblem(existingID, nodeID string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used for a record this route cannot replay",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (node %q), but its stored record does not decode "+
				"as an asset-remove result or identity; it cannot be answered as a replay. Mint a fresh "+
				"idempotencyKey for a genuinely new request.",
			existingID, nodeID),
	}
}

// writeAssetRemoveAudit writes one best-effort audit entry for this route  -
// never blocks or refuses the dispatch on a write failure, mirroring
// writeCueCatalogDeployAudit's identical reasoning.
func (h *handlers) writeAssetRemoveAudit(ctx context.Context, now time.Time, kind identity.AuditKind, ac authContext, nodeID, commandID, idempotencyKey, contentHash, note string) {
	if h.deps.Identity == nil {
		return
	}
	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID,
		Action: auditActionAssetRemove, Target: nodeID,
		IdempotencyKey: idempotencyKey, Kind: kind, CommandID: commandID,
		Params: map[string]any{"contentHash": contentHash},
	}
	if note != "" {
		entry.Params["note"] = note
	}
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.logWarn("asset remove audit write failed", "commandId", commandID, "error", err)
	}
}

// assetInUseByCueProblem is this route's own 409: contentHash is referenced
// by at least one Cue in nodeID's currently resolved catalog. Every
// referencing Cue is named, not only the first, matching
// cueCatalogClaimConflictProblem's identical "name every conflict" rule,
// so an operator sees the whole reason a removal is refused in one
// response, which is the entire point of this route existing: a refusal
// that does not name the cue is not usable, because the operator cannot
// act on it.
func assetInUseByCueProblem(nodeID, contentHash string, cueIDs []string) v1.Problem {
	quoted := make([]string, 0, len(cueIDs))
	for _, id := range cueIDs {
		quoted = append(quoted, strconv.Quote(id))
	}
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Asset removal refused: still referenced by a Cue",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"content hash %q on node %q is referenced by the following Cue(s) in this node's current Cue catalog and cannot be removed: %s",
			contentHash, nodeID, strings.Join(quoted, ", ")),
	}
}

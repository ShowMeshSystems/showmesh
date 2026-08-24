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
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file closes the gap a fresh reviewer found in TRACK-H-H3-SPEC.md
// section 4/8's own build item 2: cuecatalog.deploy was registered on the
// agent (internal/agent/cuecatalogops.go) and reserved in
// IDENTIFIER-REGISTER.md, but nothing on the coordinator side ever
// dispatched it — GET /nodes/{nodeId}/cue-catalog could always resolve
// what a node SHOULD hold, and POST .../acknowledge could always record
// what a node CLAIMED to hold, but nothing ever pushed a resolved catalog
// TO a node or recorded the revision a real deployment produced. Without
// this, [decideBootResume] (internal/agent/bootresume.go) discards every
// persisted render assignment at every boot, forever, because catalogStore.
// Load() never had anything to load — which breaks the rule a node
// restart must not turn into every surface failed.
//
// Shaped like renderdispatch.go's own dispatch core (resolve THIS
// coordinator's own params, never trust a caller's; authorize; record
// durably before acting; confirm by evidence; never claim success on a
// bare publish) for the identical reason that file gives — this is the
// second HTTP-triggered MQTT command this coordinator sends to a node.
// Confirmation, though, follows audiodispatch.go's shape rather than
// renderdispatch.go's: cuecatalog.deploy's own OperationResult (internal/
// agent/cuecatalogops.go's deploy) is a synchronous read-back with no
// asynchronous pipeline startup to poll a collector for, so this file
// waits on the dispatched command's own result topic
// ([mqttproto.ResultTopic]) via [Dependencies.AudioPublisher.AwaitResponse]
// — the SAME already-wired MQTT publish-and-await capability
// audiodispatch.go uses (both are satisfied by the one real
// *broker.BrokerManager instance; there is no second thing to wire up).

// scopeCueCatalogDeploy exists only so api.go's route registration can
// take its address, mirroring scopeAssetWrite's identical reason
// (assets.go): [handlers.writeGuard] takes *identity.Scope, and a Go
// constant's address cannot be taken directly. Deliberately its own scope
// rather than identity.ScopeAssetWrite: see ScopeCueCatalogDeploy's own
// doc comment for why granting execution authority is not an asset write.
var scopeCueCatalogDeploy = identity.ScopeCueCatalogDeploy

// auditActionCueCatalogDeploy is IDENTIFIER-REGISTER.md's own reservation
// for this route's audit action string.
const auditActionCueCatalogDeploy = "cuecatalog.deploy"

// cueCatalogDeployConfirmDeadline bounds how long a deploy dispatch waits
// for the node's own result-topic reply. A package var, not a const, only
// so a test can shrink it deterministically — matching
// renderCommandConfirmDeadline's identical test-only-override rule
// (renderdispatch.go); no runtime configuration ever reassigns it.
var cueCatalogDeployConfirmDeadline = 15 * time.Second

// maxCueCatalogDeployRequestBodyBytes bounds this endpoint's request body
// — it carries only an optional idempotencyKey, mirroring
// maxRenderCommandRequestBodyBytes's identical reasoning.
const maxCueCatalogDeployRequestBodyBytes = 4 << 10

// cueCatalogDeployWireParams is "cuecatalog.deploy"'s params, mirroring
// internal/agent/cuecatalogops.go's own catalogDeployWireParams field for
// field and tag for tag — the two are independently reproduced, not
// shared, per this codebase's standing each-side-of-a-wire-boundary-
// decodes-independently convention (renderApplyParamsPayload's own doc
// comment states the identical rule one file over).
type cueCatalogDeployWireParams struct {
	Show       string             `json:"show"`
	Generation int64              `json:"generation"`
	Revision   string             `json:"revision"`
	Entries    []cuecatalog.Entry `json:"entries"`
}

// cueCatalogDeployRequestBody is POST .../cue-catalog/deploy's optional
// body: only an idempotency key, the same shape render.surface.clear/
// restart's own RenderSurfaceRequest carries.
type cueCatalogDeployRequestBody struct {
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

func (h *handlers) handlePostNodeCueCatalogDeploy(w http.ResponseWriter, r *http.Request) {
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

	var body cueCatalogDeployRequestBody
	dec := json.NewDecoder(io.LimitReader(r.Body, maxCueCatalogDeployRequestBodyBytes+1))
	// Strict on unknown fields for the identical reason
	// decodeCueCatalogAcknowledgeBody is (cuecatalog.go): a misspelled
	// field must never decode silently as "absent".
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeProblem(w, h.logger, now, invalidParameterProblem(`request body must be a JSON object matching {"idempotencyKey":string?}`))
		return
	}

	if h.deps.AssetManifests == nil || h.deps.Commands == nil {
		h.writeInternalError(w, now, "deploy cue catalog", errors.New("no asset manifest store or command store is configured on this coordinator"))
		return
	}

	ac := authFromContext(ctx)
	issuerID := ac.result.Principal.ID
	issuerName := ac.result.Principal.Name
	if issuerID == "" {
		issuerID = renderIssuerPrincipalIDMissing
	}

	idempotencyKey := body.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = uuid.NewString()
	}

	// Idempotency-first, against the caller's unresolved request identity
	// — before resolution runs, matching dispatchRenderCommand's identical
	// reasoning (renderdispatch.go): resolution reads mutable state, and a
	// replay must never depend on it.
	if body.IdempotencyKey != "" {
		existing, lookupErr := h.deps.Commands.GetCommandByIdempotencyKey(ctx, idempotencyKey)
		switch {
		case lookupErr == nil:
			result, problem := resolveCueCatalogDeployReplay(existing, nodeID)
			if problem != nil {
				writeProblem(w, h.logger, now, *problem)
				return
			}
			jsonWrite(w, v1.CueCatalogDeployResponse{ServerTime: formatTime(h.now()), Command: result})
			return
		case errors.Is(lookupErr, store.ErrCommandNotFound):
			// Genuinely new key — fall through to resolution and dispatch.
		default:
			h.writeInternalError(w, now, "look up cue catalog deploy command by idempotency key", lookupErr)
			return
		}
	}

	// The catalog is resolved by THIS coordinator, never accepted from the
	// caller — the identical "a caller's claim is not evidence" rule build
	// item 5 fixes on the acknowledge route one file over.
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		h.writeInternalError(w, now, "resolve active show", err)
		return
	}
	if !active.Configured {
		writeProblem(w, h.logger, now, invalidParameterProblem("no active show is configured; there is no catalog to deploy"))
		return
	}
	catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
	if err != nil {
		h.writeInternalError(w, now, "resolve cue catalog", err)
		return
	}

	raw, err := json.Marshal(cueCatalogDeployWireParams{
		Show: catalog.Show, Generation: catalog.Generation, Revision: catalog.Revision, Entries: catalog.Entries,
	})
	if err != nil {
		h.writeInternalError(w, now, "encode cuecatalog.deploy params", err)
		return
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		h.writeInternalError(w, now, "decode cuecatalog.deploy params back into a map", err)
		return
	}

	paramsJSON, err := canonicalParamsJSON(params)
	if err != nil {
		h.writeInternalError(w, now, "encode params", err)
		return
	}
	identityJSON, _ := json.Marshal(cueCatalogDeployRequestIdentity{NodeID: nodeID, Show: catalog.Show, Generation: catalog.Generation, Revision: catalog.Revision})

	commandID := uuid.NewString()
	rec := store.CommandRecord{
		ID: commandID, IdempotencyKey: idempotencyKey, Action: auditActionCueCatalogDeploy,
		TargetKind: "node", TargetID: nodeID, ParamsJSON: paramsJSON,
		IssuerPrincipalID: issuerID, IssuerPrincipalName: issuerName,
		RequestedRevision:  string(identityJSON),
		ConfirmationMethod: "evidence", State: "pending",
	}
	_, err = h.deps.Commands.InsertCommand(ctx, rec)
	if err != nil {
		var dup *store.DuplicateCommandError
		if errors.As(err, &dup) {
			result, problem := resolveCueCatalogDeployReplay(dup.Existing, nodeID)
			if problem != nil {
				writeProblem(w, h.logger, now, *problem)
				return
			}
			jsonWrite(w, v1.CueCatalogDeployResponse{ServerTime: formatTime(h.now()), Command: result})
			return
		}
		h.writeInternalError(w, now, "insert cue catalog deploy command", err)
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
	payload := mqttproto.CmdPayload{
		CommandID: commandID, IdempotencyKey: idempotencyKey, Action: auditActionCueCatalogDeploy,
		Target: mqttproto.CmdTarget{Kind: "node", ID: nodeID}, Params: params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: issuerID, PrincipalName: issuerName},
		ConfirmationMethod: "evidence",
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

	h.writeCueCatalogDeployAudit(ctx, now, identity.AuditDispatch, ac, nodeID, commandID, idempotencyKey, catalog, "")

	// From here on, every write is on bgCtx: the command is already
	// durably recorded and about to be dispatched, and a caller walking
	// away (an abandoned HTTP client) must not be able to abort the
	// dispatch or its post-dispatch bookkeeping — matching audiodispatch.
	// go's identical bgCtx cutover.
	bgCtx := context.WithoutCancel(ctx)

	dispatchedAt := now
	if h.deps.AudioPublisher == nil {
		h.writeInternalError(w, now, "deploy cue catalog", errors.New("no command publish-and-await capability is configured on this coordinator"))
		return
	}
	msg, err := h.deps.AudioPublisher.AwaitResponse(bgCtx, broker.ResponseRequest{
		PublishTopic: cmdTopic, PublishPayload: rawEnv,
		PublishQoS: mqttproto.CmdDeliveryPolicy.QoS, PublishRetain: mqttproto.CmdDeliveryPolicy.Retain,
		ResponseTopic: resultTopic, ResponseQoS: mqttproto.ResultDeliveryPolicy.QoS,
		Deadline: cueCatalogDeployConfirmDeadline,
		Match: func(m broker.Message) bool {
			return cueCatalogDeployResultCorrelates(m.Payload, nodeID, commandID, idempotencyKey)
		},
	})
	if err != nil {
		if errors.Is(err, broker.ErrResponseFailedBeforePublish) {
			// Nothing reached the wire — the commands row must not claim a
			// dispatch that never happened.
			resolvedAt := h.now()
			resultJSON, _ := json.Marshal(cueCatalogDeployResultPayload{Outcome: "failed", Reason: err.Error()})
			_ = h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{
				ResolvedAt: &resolvedAt, State: strPtr("failed"), ResultJSON: strPtr(string(resultJSON)),
				OutcomeState: strPtr("collection_failed"), OutcomeReason: strPtr(err.Error()),
			})
			h.writeCueCatalogDeployAudit(bgCtx, now, identity.AuditOutcome, ac, nodeID, commandID, idempotencyKey, catalog, "failed: "+err.Error())
			h.writeInternalError(w, now, "dispatch cuecatalog.deploy", err)
			return
		}
		// Published, but no reply arrived (deadline, or the await itself
		// failed) — an honest "unconfirmed" outcome, not a caller error.
		_ = h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{DispatchedAt: &dispatchedAt})
		resolvedAt := h.now()
		reason := err.Error()
		resultJSON, _ := json.Marshal(cueCatalogDeployResultPayload{Outcome: mqttproto.OutcomeUnconfirmed, Reason: reason})
		_ = h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{
			ResolvedAt: &resolvedAt, State: strPtr("resolved"), ResultJSON: strPtr(string(resultJSON)),
			OutcomeState: strPtr("not_collected"), OutcomeReason: strPtr(reason),
		})
		h.writeCueCatalogDeployAudit(bgCtx, now, identity.AuditOutcome, ac, nodeID, commandID, idempotencyKey, catalog, "unconfirmed: "+reason)
		jsonWrite(w, v1.CueCatalogDeployResponse{
			ServerTime: formatTime(h.now()),
			Command: v1.CueCatalogDeployResult{
				CommandID: commandID, IdempotencyKey: idempotencyKey, Node: nodeID,
				Show: catalog.Show, Generation: catalog.Generation, Revision: catalog.Revision,
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
		h.writeInternalError(w, now, "decode cuecatalog.deploy result", err)
		return
	}

	acknowledgedRevision := ""
	if res.Evidence != nil {
		if v, ok := res.Evidence.Value.(string); ok {
			acknowledgedRevision = v
		}
	}

	resolvedAt := h.now()
	resultJSON, _ := json.Marshal(cueCatalogDeployResultPayload{Outcome: res.Outcome, Reason: res.Reason, AcknowledgedRevision: acknowledgedRevision})
	_ = h.updateCommandOutcomeBounded(bgCtx, commandID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, ResolvedAt: &resolvedAt, State: strPtr("resolved"),
		ResultJSON: strPtr(string(resultJSON)), OutcomeState: strPtr(res.Outcome), OutcomeReason: strPtr(res.Reason),
	})
	h.writeCueCatalogDeployAudit(bgCtx, now, identity.AuditOutcome, ac, nodeID, commandID, idempotencyKey, catalog, res.Outcome+": "+res.Reason)

	// The node reports which revision it now holds via THIS command's own
	// result — see internal/agent/cuecatalogops.go's deploy, whose
	// Evidence.Value is exactly the revision it read back after persisting.
	// Recorded through the SAME [store.PutNodeCueCatalogAck] path POST
	// .../cue-catalog/acknowledge uses, so GET /nodes/{nodeId}/cue-catalog
	// can report catalog-current from a REAL deployment, not only from a
	// showmeshctl-issued acknowledgement. Best-effort: the deploy itself
	// already succeeded on the node regardless of whether this coordinator
	// manages to also record it.
	if res.Outcome == mqttproto.OutcomeConfirmed && acknowledgedRevision != "" {
		if err := h.deps.AssetManifests.PutNodeCueCatalogAck(bgCtx, store.NodeCueCatalogAckRecord{
			NodeID: nodeID, Revision: acknowledgedRevision, ShowID: catalog.Show, Generation: catalog.Generation, AcknowledgedAt: resolvedAt,
		}); err != nil {
			h.logWarn("failed to record cue catalog acknowledgement after a confirmed deploy", "node", nodeID, "error", err)
		}
	}

	resolvedFmt := formatTime(resolvedAt)
	jsonWrite(w, v1.CueCatalogDeployResponse{
		ServerTime: formatTime(h.now()),
		Command: v1.CueCatalogDeployResult{
			CommandID: commandID, IdempotencyKey: idempotencyKey, Node: nodeID,
			Show: catalog.Show, Generation: catalog.Generation, Revision: catalog.Revision,
			Outcome: res.Outcome, Reason: res.Reason, AcknowledgedRevision: acknowledgedRevision,
			DispatchedAt: strPtr(formatTime(dispatchedAt)), ResolvedAt: &resolvedFmt,
		},
	})
}

// cueCatalogDeployRequestIdentity is the caller's own unresolved request
// shape, stored in commands.requested_revision — mirrors
// renderRequestIdentity's identical role and reasoning one file over,
// narrowed to what a replay of THIS action needs to compare.
type cueCatalogDeployRequestIdentity struct {
	NodeID     string `json:"node"`
	Show       string `json:"show"`
	Generation int64  `json:"generation"`
	Revision   string `json:"revision"`
}

// cueCatalogDeployResultPayload is the JSON this file persists into
// store.CommandRecord.ResultJSON, mirroring commandResultPayload's
// identical role for render commands.
type cueCatalogDeployResultPayload struct {
	Outcome              string `json:"outcome"`
	Reason               string `json:"reason"`
	AcknowledgedRevision string `json:"acknowledgedRevision,omitempty"`
}

// cueCatalogDeployResultCorrelates mirrors audioResultCorrelates
// (audiodispatch.go) exactly, narrowed to this action's own identity
// fields (no session id to compare).
func cueCatalogDeployResultCorrelates(payload []byte, nodeID, commandID, idempotencyKey string) bool {
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil || env.NodeID != nodeID {
		return false
	}
	res, err := mqttproto.DecodeResultPayload(env)
	if err != nil {
		return false
	}
	return res.CommandID == commandID && res.IdempotencyKey == idempotencyKey && res.Action == auditActionCueCatalogDeploy
}

// resolveCueCatalogDeployReplay answers a replayed idempotency key against
// existing's own stored row — mirrors resolveRenderCommandReplay's
// identical shape, narrowed to this action's own single-action, single-
// target-kind identity (there is no per-request field beyond node to
// compare, since this route always resolves its own params rather than
// accepting them from the caller).
func resolveCueCatalogDeployReplay(existing store.CommandRecord, nodeID string) (v1.CueCatalogDeployResult, *v1.Problem) {
	if existing.Action != auditActionCueCatalogDeploy || existing.TargetID != nodeID {
		p := renderCommandReplayConflictProblem(existing.ID, existing.Action, existing.TargetID, auditActionCueCatalogDeploy, nodeID)
		return v1.CueCatalogDeployResult{}, &p
	}
	var res cueCatalogDeployResultPayload
	_ = json.Unmarshal([]byte(existing.ResultJSON), &res)
	// A blank Outcome and a nil DispatchedAt are the honest state of a
	// command still in flight, or one whose publish failed before the
	// wire: reported as-is, never substituted (ADR-020), matching
	// renderCommandResultFromRecord's identical accepted-empty case.
	var resolvedAt *string
	if existing.ResolvedAt != nil {
		resolvedAt = strPtr(formatTime(*existing.ResolvedAt))
	}
	var dispatchedAt *string
	if existing.DispatchedAt != nil {
		dispatchedAt = strPtr(formatTime(*existing.DispatchedAt))
	}
	var reqID cueCatalogDeployRequestIdentity
	_ = json.Unmarshal([]byte(existing.RequestedRevision), &reqID)
	return v1.CueCatalogDeployResult{
		CommandID: existing.ID, IdempotencyKey: existing.IdempotencyKey, Node: nodeID, Replay: true,
		Show: reqID.Show, Generation: reqID.Generation, Revision: reqID.Revision,
		Outcome: res.Outcome, Reason: res.Reason, AcknowledgedRevision: res.AcknowledgedRevision,
		DispatchedAt: dispatchedAt, ResolvedAt: resolvedAt,
	}, nil
}

// writeCueCatalogDeployAudit writes one best-effort audit entry for this
// route — never blocks or refuses the dispatch on a write failure, per the
// identical reasoning writeRenderAudit's own doc comment gives
// (renderdispatch.go): refusing to push a catalog because the audit log
// could not be written would make an unwritable audit log a way to keep a
// node's rendering permanently discarded at every boot.
func (h *handlers) writeCueCatalogDeployAudit(ctx context.Context, now time.Time, kind identity.AuditKind, ac authContext, nodeID, commandID, idempotencyKey string, catalog assetsync.Catalog, note string) {
	if h.deps.Identity == nil {
		return
	}
	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID,
		Action: auditActionCueCatalogDeploy, Target: nodeID,
		IdempotencyKey: idempotencyKey, Kind: kind, CommandID: commandID,
		Params: map[string]any{"show": catalog.Show, "generation": catalog.Generation, "revision": catalog.Revision},
	}
	if note != "" {
		if entry.Params == nil {
			entry.Params = map[string]any{}
		}
		entry.Params["note"] = note
	}
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.logWarn("cue catalog deploy audit write failed", "commandId", commandID, "error", err)
	}
}

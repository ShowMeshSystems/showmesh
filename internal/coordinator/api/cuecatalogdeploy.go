package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// TRACK-H-cues-and-playlists.md section H5 build item 2's own ruling: a
	// claim conflict is DATA on the resolved catalog (assetsync.Catalog.
	// Conflicts), never an error out of ResolveCueCatalog — but deployment
	// itself still refuses outright rather than pushing a catalog it knows
	// two Cues cannot both safely execute. Named, operator-visible, and
	// reachable through this API (and showmeshctl, which prints a Problem's
	// Detail verbatim), not only a log line.
	if len(catalog.Conflicts) > 0 {
		writeProblem(w, h.logger, now, cueCatalogClaimConflictProblem(nodeID, catalog.Conflicts))
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
		// TRACK-H-cues-and-playlists.md section H5 build item 1: a node that just proved it holds the
		// active Show's authorized Cue catalog is exactly the node this
		// coordinator applies that Show's showmesh-audio background
		// Playlist to (if any, and if nodeID has an audio.node object at
		// all) — see showmeshaudiodispatch.go. Best-effort, matching the
		// cue-catalog-ack write immediately above: the deploy itself
		// already succeeded regardless of whether this follow-on apply
		// does.
		h.applyShowmeshAudioPlaylistIfAny(ctx, h.now(), nodeID, active)

		// SM-281 half-two: a node that just proved it holds the active
		// Show's authorized Cue catalog is exactly the node whose
		// show.surface objects should hold SOME render assignment — see
		// establishRenderAssignments' own doc comment for the defect this
		// closes. Best-effort, matching the two calls above it: the
		// deploy itself already succeeded regardless of whether this
		// follow-on establishment does.
		h.establishRenderAssignments(ctx, h.now(), nodeID, catalog)
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

// renderEstablishIssuerPrincipalID attributes an establishment dispatch to
// the deploy that triggered it, not to whichever human or automation
// happened to POST the deploy — mirrors showmeshAudioIssuerPrincipalID's
// identical reasoning one file over (showmeshaudiodispatch.go): this is an
// autonomous coordinator action, one HTTP request can fan out into several
// of these across several surfaces, and none of them is itself something
// an operator directly asked for.
const renderEstablishIssuerPrincipalID = "system:cuecatalog-deploy"

// establishRenderAssignments is SM-281 half-two's own fix for the gap its
// build task describes: nothing ever created a node's persisted render
// assignment except an operator dispatching render.surface.apply by hand,
// and ADR-043's H0.7 clears assignments at boot — together, a render node
// that reboots mid-show never renders again on its own, forever, until
// somebody remembers to run a manual apply. A confirmed cuecatalog.deploy
// is exactly the moment this coordinator already knows the node holds
// (asset store, own manifest evidence) the active Show's authorized
// catalog and every asset it targets, so it is also the moment to
// establish every one of that Show's show.surface objects on this node
// with NO sequence selected — resolveRenderEstablishParams
// (renderdispatch.go) — so the FIRST cue activation has something for
// activateSurfaceRender (internal/agent/cueactivationrender.go) to swap an
// FSEQ onto, rather than refusing outright because the node holds no
// assignment at all.
//
// Mirrors applyShowmeshAudioPlaylistIfAny's identical shape and posture
// one call site above: best-effort per surface. A refused or unconfirmed
// establishment on ONE surface must never prevent the deploy's own
// already-succeeded outcome from being reported, and must never stop this
// loop from still establishing every OTHER surface on this node.
func (h *handlers) establishRenderAssignments(ctx context.Context, now time.Time, nodeID string, catalog assetsync.Catalog) {
	if h.deps.Config == nil || h.deps.Commands == nil || h.deps.RenderPublisher == nil {
		return
	}
	surfaces, err := h.listShowSurfaceSummaries(ctx, catalog.Show, nodeID)
	if err != nil {
		h.logWarn("render assignment establishment: list show.surface objects failed", "node", nodeID, "show", catalog.Show, "error", err)
		return
	}
	for _, surface := range surfaces {
		h.establishRenderAssignment(ctx, now, nodeID, surface.ID, catalog)
	}
}

// establishRenderAssignment establishes exactly one surface, skipping it
// outright when the node already holds SOME current render assignment for
// it — see [handlers.renderSurfaceCurrentlyAssigned]'s own doc comment for
// why that guard exists: this call must only ever fill the gap
// [ReadinessNodeRenderUnassigned] reports, never disturb a surface that is
// already rendering real content.
func (h *handlers) establishRenderAssignment(ctx context.Context, now time.Time, nodeID, surfaceID string, catalog assetsync.Catalog) {
	assigned, err := h.renderSurfaceCurrentlyAssigned(ctx, nodeID, surfaceID)
	if err != nil {
		h.logWarn("render assignment establishment: check current assignment failed", "node", nodeID, "surface", surfaceID, "error", err)
		return
	}
	if assigned {
		return
	}

	params, problem, err := h.resolveRenderEstablishParams(ctx, nodeID, surfaceID, catalog.Generation, catalog.Revision)
	if err != nil {
		h.logWarn("render assignment establishment: resolve failed", "node", nodeID, "surface", surfaceID, "error", err)
		return
	}
	if problem != nil {
		h.logWarn("render assignment establishment: refused", "node", nodeID, "surface", surfaceID, "detail", problem.Detail)
		return
	}

	in := renderDispatchInput{
		Action: "render.surface.apply", NodeID: nodeID, SurfaceID: surfaceID,
		IdempotencyKey: renderEstablishIdempotencyKey(nodeID, surfaceID, catalog.Show, catalog.Generation, catalog.Revision),
		DesiredState:   "running",
		IssuerID:       renderEstablishIssuerPrincipalID, IssuerName: "cuecatalog.deploy",
		Params: params,
	}
	_, problem, err = h.executeRenderDispatch(ctx, now, in)
	if err != nil {
		h.logWarn("render assignment establishment: dispatch failed", "node", nodeID, "surface", surfaceID, "error", err)
		return
	}
	if problem != nil {
		h.logWarn("render assignment establishment: refused by dispatch", "node", nodeID, "surface", surfaceID, "detail", problem.Detail)
	}
}

// renderEstablishIdempotencyKey derives one surface's establishment
// idempotency key from nodeID, surfaceID, and the deployed catalog's own
// (show, generation, revision) identity — mirrors
// showmeshAudioIdempotencyKey's identical "keyed on content, not a bare
// counter" reasoning one file over (showmeshaudiodispatch.go): a repeated
// deploy of the SAME catalog must replay the same establishment (no
// second, redundant MQTT publish), but a genuinely new generation or
// revision must be free to establish again.
func renderEstablishIdempotencyKey(nodeID, surfaceID, show string, generation int64, revision string) string {
	return fmt.Sprintf("render-establish-%s-%s-%s-%d-%s", nodeID, surfaceID, show, generation, revision)
}

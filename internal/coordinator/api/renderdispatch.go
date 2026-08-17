package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Track B seam B2b-front: the coordinator-side dispatch for
// the three agent operations B2a shipped (internal/agent/renderops.go) —
// render.surface.apply, render.surface.clear, render.pipeline.restart.
// Shaped like fppcommand_handler.go/fppcommand_dispatch.go's own dispatch
// core (authenticate, authorize by scope, record durably before acting,
// confirm by evidence, never claim success on a bare publish) and
// assetsync/sync.go's dispatchFetch for the MQTT envelope itself, since
// this is the first HTTP-triggered command this coordinator sends to a
// node over MQTT rather than over FPP's own REST API.
//
// Build contract ruling 4 ("the node is told its surface, it does not
// discover it") is this file's entire reason to exist beyond a thin
// dispatch: [handlers.resolveRenderApplyParams] assembles the COMPLETE
// self-contained assignment — the surface's own show.surface config plus
// the coordinator-resolved runtime filename and content hash of its
// current FSEQ asset — and refuses the dispatch outright, naming exactly
// what could not be resolved, rather than ever sending a partial one.

// scopeRenderCommand exists only so api.go's route registration can take
// its address, matching scopeFPPCommand/scopeResolumeAction's identical
// need.
var scopeRenderCommand = identity.ScopeRenderCommand

// renderSurfaceIDPattern mirrors internal/agent/renderops.go's own
// surfaceIDPattern exactly. Independently reproduced, not shared: this
// package does not import internal/agent, matching this project's
// standing "each side validates independently" convention for a wire
// boundary (fppcommand_copy_guard_test.go's own reasoning applies
// identically here — a bug that makes both sides silently accept an unsafe
// id needs two independent decoders to disagree, not one shared regex).
var renderSurfaceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// Three timeouts nest around one dispatch, each with real margin so no
// layer times out while the layer below it might still legitimately
// answer:
//
//	agent renderConfirmDeadline (internal/agent/renderops.go)   5s
//	< renderCommandConfirmDeadline (this file)                 15s
//	< renderHandlerWriteDeadline = confirm + margin             25s
//	< CLI HTTP client timeout (cmd/showmeshctl/cmd_render.go)   35s
//
// See renderdispatch_timeouts_test.go for the enforced strict ordering.
// renderCommandConfirmDeadline bounds how long a dispatch waits for
// surface.pipeline.state evidence dated at or after dispatch before
// reporting "unconfirmed". Longer than the agent's own 5s
// renderConfirmDeadline: the agent's own wait already covers pipeline
// startup; this deadline additionally has to cover one MQTT round trip for
// the render report and one collector poll of noderender.
// DefaultPollInterval (5s) rendering it into an observation. SHOWMESH
// HYPOTHESIS, NOT MEASURED — no bench data exists yet for the full
// apply-to-observed-evidence path. A package var, not a const, only so a
// test can shrink it deterministically (renderdispatch_test.go); no
// runtime configuration ever reassigns it.
var renderCommandConfirmDeadline = 15 * time.Second

// renderCommandPollInterval is how often the confirmation wait re-checks
// observations while renderCommandConfirmDeadline runs out. Same
// test-only-override rule as renderCommandConfirmDeadline.
var renderCommandPollInterval = 250 * time.Millisecond

const (
	// renderHandlerWriteDeadlineMargin is added to
	// renderCommandConfirmDeadline for the HTTP response write deadline,
	// matching fppcommand_handler.go's identical reasoning: this handler
	// can legitimately hold the connection open for the whole
	// confirmation wait, so the server's own write deadline must exceed
	// it with margin rather than severing the connection out from under a
	// still-working wait.
	renderHandlerWriteDeadlineMargin = 10 * time.Second

	// maxRenderCommandRequestBodyBytes bounds this endpoint's request
	// body, mirroring maxFPPCommandRequestBodyBytes's identical
	// reasoning.
	maxRenderCommandRequestBodyBytes = 4 << 10
)

// renderHandlerWriteDeadline is the absolute per-request deadline set on
// the response writer — strictly greater than renderCommandConfirmDeadline
// by renderHandlerWriteDeadlineMargin.
func renderHandlerWriteDeadline() time.Duration {
	return renderCommandConfirmDeadline + renderHandlerWriteDeadlineMargin
}

// RenderPublisher is the coordinator's MQTT publish capability this file
// depends on, declared here at the consumer exactly as
// assetsync.Publisher is declared at its own consumer — *broker.
// BrokerManager already satisfies this with no adapter.
type RenderPublisher interface {
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
}

// renderIssuerPrincipalIDMissing is never a real value — every dispatch
// through this file is authenticated (writeGuard(&scopeRenderCommand, ...)
// runs first), so authFromContext always yields a real principal. This
// constant exists only as a defensive label if that invariant is ever
// violated by a future refactor.
const renderIssuerPrincipalIDMissing = "unknown"

// alwaysTrue is this file's own copy of assetsync's unexported helper of
// the same name (config.DecodeShowSurfacePayload's showExists/nodeDeclared
// callbacks are irrelevant here — the surface object already exists by
// construction, since it was read from the store by id — and are not the
// concern of THIS decode, which only re-parses a stored, already-valid
// payload).
func alwaysTrue(string) bool { return true }

// dispatchRenderCommand is the one core this file's three thin HTTP
// handlers below share: resolve params (apply's own refusal-capable
// resolution, or the trivial surfaceId-only params for clear/restart),
// authorize, record, publish, and confirm by evidence.
func (h *handlers) dispatchRenderCommand(w http.ResponseWriter, r *http.Request, action, desiredState string) {
	now := h.now()
	ctx := r.Context()

	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(renderHandlerWriteDeadline()))

	nodeID := r.PathValue("nodeId")
	surfaceID := r.PathValue("surfaceId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}
	if !renderSurfaceIDPattern.MatchString(surfaceID) {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("surfaceId %q is not a safe identifier (must match %s)", surfaceID, renderSurfaceIDPattern.String())))
		return
	}

	var body struct {
		SequenceID     string `json:"sequenceId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRenderCommandRequestBodyBytes+1))
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body must be a JSON object matching {\"sequenceId\":string?,\"idempotencyKey\":string?}"))
		return
	}

	var params map[string]any
	if action == "render.surface.apply" {
		if body.SequenceID == "" {
			writeProblem(w, h.logger, now, invalidParameterProblem("sequenceId is required"))
			return
		}
		var problem *v1.Problem
		params, problem = h.resolveRenderApplyParams(ctx, nodeID, surfaceID, body.SequenceID)
		if problem != nil {
			writeProblem(w, h.logger, now, *problem)
			return
		}
	} else {
		params = map[string]any{"surfaceId": surfaceID}
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

	outcome, problem, err := h.executeRenderDispatch(ctx, now, renderDispatchInput{
		Action:         action,
		NodeID:         nodeID,
		SurfaceID:      surfaceID,
		Params:         params,
		IdempotencyKey: idempotencyKey,
		DesiredState:   desiredState,
		IssuerID:       issuerID,
		IssuerName:     issuerName,
		ClientAddr:     h.clientAddr(r),
		Form:           ac.result.Form,
		CredentialID:   ac.result.CredentialID,
	})
	if err != nil {
		h.writeInternalError(w, now, "dispatch render command", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	jsonWrite(w, v1.RenderCommandResponse{
		ServerTime: formatTime(h.now()),
		Command:    outcome,
	})
}

func (h *handlers) handleRenderSurfaceApply(w http.ResponseWriter, r *http.Request) {
	h.dispatchRenderCommand(w, r, "render.surface.apply", "running")
}

func (h *handlers) handleRenderSurfaceClear(w http.ResponseWriter, r *http.Request) {
	h.dispatchRenderCommand(w, r, "render.surface.clear", "stopped")
}

func (h *handlers) handleRenderPipelineRestart(w http.ResponseWriter, r *http.Request) {
	h.dispatchRenderCommand(w, r, "render.pipeline.restart", "running")
}

// handleRenderTransportProbe dispatches render.transport.probe — a COMMAND
// (it starts a real gst-launch-1.0 subprocess on the node to attempt a
// state transition; ADR-026 decision 6's "element presence is not runtime
// presence" rule is what this whole operation exists to answer for real),
// never reachable by GET. desiredState is passed as "" because this
// operation has no desired STATE to match — see confirmRenderTransportProbe.
func (h *handlers) handleRenderTransportProbe(w http.ResponseWriter, r *http.Request) {
	h.dispatchRenderCommand(w, r, "render.transport.probe", "")
}

// resolveRenderApplyParams builds render.surface.apply's complete,
// self-contained params object (build contract ruling 4) or refuses
// outright, naming exactly what could not be resolved. It never returns a
// partial params map alongside a non-nil problem.
func (h *handlers) resolveRenderApplyParams(ctx context.Context, nodeID, surfaceID, sequenceID string) (map[string]any, *v1.Problem) {
	if h.deps.AssetManifests == nil || h.deps.Config == nil {
		p := invalidParameterProblem("this coordinator has no asset store or config store wired in; render.surface.apply cannot resolve an assignment")
		return nil, &p
	}

	obj, err := h.deps.Config.GetConfigObject(ctx, config.ShowSurfaceConfigKind, surfaceID)
	if err != nil {
		if errors.Is(err, store.ErrConfigObjectNotFound) {
			p := resourceNotFoundProblem(fmt.Sprintf("surface %q is not a configured show.surface object", surfaceID))
			return nil, &p
		}
		return nil, nil
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowSurfaceConfigKind, surfaceID, obj.CurrentRevision)
	if err != nil {
		return nil, nil
	}
	payload, verr := config.DecodeShowSurfacePayload(rev.PayloadJSON, alwaysTrue, alwaysTrue)
	if verr != nil {
		p := invalidParameterProblem(fmt.Sprintf("surface %q has a stored payload that no longer decodes: %s", surfaceID, verr.Detail))
		return nil, &p
	}
	if payload.Node != nodeID {
		p := invalidParameterProblem(fmt.Sprintf("surface %q is assigned to node %q, not %q", surfaceID, payload.Node, nodeID))
		return nil, &p
	}

	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		return nil, nil
	}
	if !active.Configured {
		p := invalidParameterProblem("no active show is configured; render.surface.apply has no show to resolve an asset against")
		return nil, &p
	}
	if payload.Show != active.ShowID {
		p := invalidParameterProblem(fmt.Sprintf("surface %q belongs to show %q, which is not the active show %q", surfaceID, payload.Show, active.ShowID))
		return nil, &p
	}

	expected, err := assetsync.ExpectedAssetsForNode(ctx, h.deps.AssetManifests, active.ShowID, nodeID)
	if err != nil {
		return nil, nil
	}
	var matches []assetsync.ExpectedAsset
	for _, a := range expected.Assets {
		if a.SequenceID == sequenceID {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		p := invalidParameterProblem(fmt.Sprintf("no asset found for surface %q (sequence %q) in show %q", surfaceID, sequenceID, active.ShowID))
		return nil, &p
	default:
		if len(matches) > 1 {
			p := invalidParameterProblem(fmt.Sprintf("ambiguous: %d current assets match sequence %q for node %q in show %q; cannot resolve one FSEQ to assign", len(matches), sequenceID, nodeID, active.ShowID))
			return nil, &p
		}
	}
	asset := matches[0]

	raw, err := json.Marshal(renderApplyParamsPayload{
		SurfaceID: surfaceID, Show: payload.Show, Name: payload.Name, Node: payload.Node,
		ChannelRange: payload.ChannelRange, Geometry: payload.Geometry, FrameRate: payload.FrameRate, Output: payload.Output,
		FSEQFilename: asset.Filename, FSEQContentHash: asset.ContentHash,
	})
	if err != nil {
		return nil, nil
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, nil
	}
	return params, nil
}

// renderApplyParamsPayload's JSON key set matches
// internal/agent/renderops.go's renderApplyKnownKeys exactly (ten keys,
// surfaceId plus the nine show.surface/asset fields) — the same
// independently-reproduced-on-purpose relationship
// renderSurfaceIDPattern's own doc comment describes.
type renderApplyParamsPayload struct {
	SurfaceID       string                         `json:"surfaceId"`
	Show            string                         `json:"show"`
	Name            string                         `json:"name"`
	Node            string                         `json:"node"`
	ChannelRange    config.ShowSurfaceChannelRange `json:"channelRange"`
	Geometry        config.ShowSurfaceGeometry     `json:"geometry"`
	FrameRate       int                            `json:"frameRate"`
	Output          config.ShowSurfaceOutput       `json:"output"`
	FSEQFilename    string                         `json:"fseqFilename"`
	FSEQContentHash string                         `json:"fseqContentHash"`
}

// renderDispatchInput is [handlers.executeRenderDispatch]'s input, kept as
// its own struct so the HTTP wire adapter above and this core stay
// cleanly separated, matching FPPCommandInput's identical role one file
// over.
type renderDispatchInput struct {
	Action         string
	NodeID         string
	SurfaceID      string
	Params         map[string]any
	IdempotencyKey string
	DesiredState   string
	IssuerID       string
	IssuerName     string
	ClientAddr     string
	Form           identity.CredentialForm
	CredentialID   string
}

// executeRenderDispatch records the command (idempotency-first: a replayed
// key returns the existing row's own outcome rather than dispatching
// again), publishes it to the node's cmd topic, and polls for
// surface.pipeline.state evidence dated at or after dispatch that matches
// in.DesiredState before returning. A nil error with a non-nil problem
// means "the request was refused"; a non-nil error means "something this
// coordinator cannot attribute to the caller went wrong" (rendered as a
// 500 by the caller).
func (h *handlers) executeRenderDispatch(ctx context.Context, now time.Time, in renderDispatchInput) (v1.RenderCommandResult, *v1.Problem, error) {
	if h.deps.Commands == nil {
		return v1.RenderCommandResult{}, nil, errors.New("no command store is configured")
	}
	if h.deps.RenderPublisher == nil {
		return v1.RenderCommandResult{}, nil, errors.New("no render command publisher is configured")
	}

	paramsJSON, err := json.Marshal(in.Params)
	if err != nil {
		return v1.RenderCommandResult{}, nil, fmt.Errorf("encode params: %w", err)
	}

	commandID := uuid.NewString()
	rec := store.CommandRecord{
		ID: commandID, IdempotencyKey: in.IdempotencyKey, Action: in.Action,
		TargetKind: "node", TargetID: in.NodeID, ParamsJSON: string(paramsJSON),
		IssuerPrincipalID: in.IssuerID, IssuerPrincipalName: in.IssuerName,
		ConfirmationMethod: "evidence", State: "pending",
	}
	inserted, err := h.deps.Commands.InsertCommand(ctx, rec)
	if err != nil {
		var dup *store.DuplicateCommandError
		if errors.As(err, &dup) {
			// A replayed idempotency key: render the ALREADY-RESOLVED
			// outcome rather than dispatching a second time (ADR-008's
			// "executes exactly once"), matching fppcommand's own
			// idempotency-first rule.
			return renderCommandResultFromRecord(dup.Existing, true), nil, nil
		}
		return v1.RenderCommandResult{}, nil, fmt.Errorf("insert command: %w", err)
	}

	topic, err := mqttproto.CmdTopic(in.NodeID)
	if err != nil {
		return v1.RenderCommandResult{}, nil, fmt.Errorf("build cmd topic: %w", err)
	}
	payload := mqttproto.CmdPayload{
		CommandID: commandID, IdempotencyKey: in.IdempotencyKey, Action: in.Action,
		Target: mqttproto.CmdTarget{Kind: "node", ID: in.NodeID}, Params: in.Params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: in.IssuerID, PrincipalName: in.IssuerName},
		ConfirmationMethod: "evidence",
	}
	env, err := mqttproto.NewCmdEnvelope(func() time.Time { return now }, in.NodeID, payload)
	if err != nil {
		return v1.RenderCommandResult{}, nil, fmt.Errorf("build cmd envelope: %w", err)
	}
	rawEnv, err := json.Marshal(env)
	if err != nil {
		return v1.RenderCommandResult{}, nil, fmt.Errorf("marshal cmd envelope: %w", err)
	}

	dispatchedAt := now
	if err := h.deps.RenderPublisher.Publish(ctx, topic, mqttproto.CmdDeliveryPolicy.QoS, mqttproto.CmdDeliveryPolicy.Retain, rawEnv); err != nil {
		// The command row already exists (state "pending", never
		// dispatched) — that is an honest record of an attempted dispatch
		// that could not reach the broker, not something to unwind.
		h.writeRenderAudit(ctx, now, identity.AuditDispatch, in, inserted, "publish failed: "+err.Error())
		return v1.RenderCommandResult{}, nil, fmt.Errorf("publish command: %w", err)
	}

	_ = h.updateCommandOutcomeBounded(ctx, commandID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, State: strPtr("dispatched"),
	})
	h.writeRenderAudit(ctx, now, identity.AuditDispatch, in, inserted, "")

	var confirmed bool
	var outcomeState, outcomeReason string
	if in.Action == "render.transport.probe" {
		// No desired STATE to match (unlike apply/clear/restart): a probe
		// that correctly reports the runtime absent is just as confirmed
		// as one that reports it present — see confirmRenderTransportProbe.
		confirmed, outcomeState, outcomeReason = h.confirmRenderTransportProbe(ctx, in.NodeID, in.SurfaceID, dispatchedAt)
	} else {
		confirmed, outcomeState, outcomeReason = h.confirmRenderCommand(ctx, in.NodeID, in.SurfaceID, in.DesiredState, dispatchedAt)
	}
	resolvedAt := h.now()
	outcome := "unconfirmed"
	if confirmed {
		outcome = "confirmed"
	}
	resultJSON, _ := json.Marshal(commandResultPayload{Outcome: outcome})
	_ = h.updateCommandOutcomeBounded(ctx, commandID, store.CommandOutcomeUpdate{
		ResolvedAt: &resolvedAt, State: strPtr("resolved"),
		ResultJSON: strPtr(string(resultJSON)), OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	})

	entry := identity.AuditEntry{
		Timestamp: h.now(), PrincipalID: in.IssuerID, PrincipalName: in.IssuerName,
		Form: in.Form, CredentialID: in.CredentialID, ClientAddr: in.ClientAddr,
		Action: in.Action, Target: "node:" + in.NodeID + "/surface:" + in.SurfaceID,
		IdempotencyKey: in.IdempotencyKey, Kind: identity.AuditOutcome, CommandID: commandID,
		Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
	}
	if h.deps.Identity != nil {
		if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
			// The event this entry records already happened and cannot be
			// un-recorded; refusing the response here would only deny the
			// operator the record of it, never protect them from
			// anything — the same reasoning
			// degradedAttributionReasonPostDispatch names for FPP
			// commands (fppcommand_handler.go).
			h.logWarn("render command outcome audit write failed", "commandId", commandID, "error", err)
		}
	}

	dispatchedFmt := formatTime(dispatchedAt)
	resolvedFmt := formatTime(resolvedAt)
	return v1.RenderCommandResult{
		CommandID: commandID, IdempotencyKey: in.IdempotencyKey, Action: in.Action,
		NodeID: in.NodeID, SurfaceID: in.SurfaceID, Replay: false,
		Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
		DispatchedAt: dispatchedFmt, ResolvedAt: &resolvedFmt,
	}, nil, nil
}

// writeRenderAudit is the dispatch-side (pre-outcome) best-effort audit
// entry. Never blocks the caller — see executeRenderDispatch's own outcome
// audit comment for why an audit-write failure never withholds a render
// command: refusing to dispatch would make an unwritable audit log a way
// to stop a surface from rendering, which this project's reliability goal
// forbids for anything short of the ADR-024 decision-11 fail-closed
// class (config:write, principal:write) render commands are not part of.
func (h *handlers) writeRenderAudit(ctx context.Context, now time.Time, kind identity.AuditKind, in renderDispatchInput, cmd store.CommandRecord, failureNote string) {
	if h.deps.Identity == nil {
		return
	}
	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: in.IssuerID, PrincipalName: in.IssuerName,
		Form: in.Form, CredentialID: in.CredentialID, ClientAddr: in.ClientAddr,
		Action: in.Action, Target: "node:" + in.NodeID + "/surface:" + in.SurfaceID,
		IdempotencyKey: in.IdempotencyKey, Kind: kind, CommandID: cmd.ID,
	}
	if failureNote != "" {
		entry.Params = map[string]any{"dispatchFailure": failureNote}
	}
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.logWarn("render command dispatch audit write failed", "commandId", cmd.ID, "error", err)
	}
}

// confirmRenderCommand polls surface.pipeline.state for evidence dated at
// or after dispatchedAt reporting wantState, bounded by
// renderCommandConfirmDeadline — the same never-a-pre-existing-reading
// fence resolveConfirmationEvidence applies for FPP commands
// (fppcommand_evidence.go), reimplemented here for observation.
// ResourceSurface rather than observation.ResourceFPP. nodeID is the node
// THIS dispatch was sent to — see evaluateRenderSurfaceState for why that
// matters.
func (h *handlers) confirmRenderCommand(ctx context.Context, nodeID, surfaceID, wantState string, dispatchedAt time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	if h.deps.Observations == nil {
		return false, string(observation.StateNotCollected), "no observation source is configured"
	}
	absDeadline := time.Now().Add(renderCommandConfirmDeadline)
	ticker := time.NewTicker(renderCommandPollInterval)
	defer ticker.Stop()

	for {
		confirmed, outcomeState, outcomeReason = h.evaluateRenderSurfaceState(ctx, nodeID, surfaceID, wantState, dispatchedAt)
		if confirmed {
			return true, outcomeState, outcomeReason
		}
		if !time.Now().Before(absDeadline) {
			return false, outcomeState, outcomeReason
		}
		select {
		case <-ctx.Done():
			return false, string(observation.StateUnknownAge), "confirmation aborted before the deadline: " + ctx.Err().Error()
		case <-ticker.C:
		}
	}
}

const renderSignalPipelineState = "surface.pipeline.state"

// renderNodeSourceFor mirrors internal/coordinator/collector/noderender.
// SourceFor's exact wire format (that package's SourceName constant plus a
// ':' plus the node id) without importing that collector package — this
// package's own TestPackageNeverImportsACollector (resolumeinstances_test.go)
// forbids importing any internal/coordinator/collector/... package at all,
// so GET /resolume/instances and this API stay servable from stored
// evidence with no client capable of reaching a live device. Both sides of
// this format are pinned by TestRenderNodeSourceForMatchesNodeRenderPackage.
func renderNodeSourceFor(nodeID string) string {
	return "node-render:" + nodeID
}

// evaluateRenderSurfaceState reads the surface.pipeline.state evidence
// belonging specifically to the node this command was dispatched to
// (renderNodeSourceFor(nodeID)), never a value ResolveObservations picked
// among every node that has ever reported surfaceID. Two nodes CAN both
// hold a row for the same surfaceID — a surface reassigned mid-transition,
// or a cleared runner (see internal/agent/pipeline.Supervisor.Clear) whose
// old node kept reporting it — and reading the resolved (i.e. most-recent-
// across-every-node) winner would let a stale reading from a DIFFERENT node
// confirm or unconfirm a command this dispatch never touched. This is the
// same schemaV4 (resource, signal, source) row noderender.Collector wrote,
// so the source's own identity already disambiguates it, with no
// resolution needed for a caller that knows exactly which node it dispatched to.
func (h *handlers) evaluateRenderSurfaceState(ctx context.Context, nodeID, surfaceID, wantState string, notBefore time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	kind := observation.ResourceSurface
	sig := observation.SignalID(renderSignalPipelineState)
	wantSource := renderNodeSourceFor(nodeID)
	obs, err := h.deps.Observations.ListObservations(ctx, ObservationFilter{ResourceKind: &kind, ResourceID: &surfaceID, Signal: &sig})
	if err != nil {
		return false, string(observation.StateCollectionFailed), "reading surface.pipeline.state for confirmation: " + err.Error()
	}
	var o observation.Observation
	var found bool
	for _, cand := range obs {
		if cand.Resource.Kind == kind && cand.Resource.ID == surfaceID && cand.Signal == sig && cand.Source == wantSource {
			o = cand
			found = true
			break
		}
	}
	if !found {
		return false, string(observation.StateNotCollected), fmt.Sprintf(
			"no surface.pipeline.state observation is recorded for this surface from node %s yet", nodeID)
	}
	src := o.Source
	if src == "" {
		src = "unknown source"
	}
	if o.CollectedAt.Before(notBefore) {
		return false, string(observation.StateNotCollected), fmt.Sprintf(
			"no surface.pipeline.state reading has arrived since this command was dispatched at %s; the most recent evidence is from %s, via %s, and predates dispatch",
			notBefore.Format(time.RFC3339), o.CollectedAt.Format(time.RFC3339), src)
	}

	// A surface this node explicitly stops reporting, with a reason
	// (noderender.Collector.Poll's dropped-surface absence — see that
	// package), IS evidence the pipeline is gone: ADR-003's "evidence that
	// observed state moved", exactly as much as an explicit "stopped"
	// value would be. The CollectedAt-vs-notBefore fence immediately above
	// already applies to this row too, so this branch can only fire on
	// absence evidence that post-dates dispatch — the fence this project
	// has paid for once already (a command confirmed 179 microseconds
	// after its own dispatch off a pre-dispatch reading) is not
	// bypassed here. Only wantState=="stopped" (render.surface.clear)
	// accepts this: render.surface.apply/render.pipeline.restart want
	// "running", which an absence can never satisfy.
	if wantState == mqttproto.RenderPipelineStateStopped && o.Absence == observation.StateNotCollected {
		reason := o.Reason
		if reason == "" {
			reason = "surface.pipeline.state was explicitly reported absent"
		}
		// Formatted with the same `surface.pipeline.state = %q` prefix the
		// value-observation branch below uses, so a caller reading the
		// outcome reason sees the desired state was reached either way —
		// only the parenthetical names how it was confirmed.
		return true, string(observation.StateCurrent), fmt.Sprintf("surface.pipeline.state = %q (absent: %s, via %s)", wantState, reason, src)
	}

	state := o.StateAt(h.now())
	if state != observation.StateCurrent {
		reason := o.Reason
		if reason == "" {
			reason = fmt.Sprintf("surface.pipeline.state evidence state is %s", state)
		}
		return false, string(state), fmt.Sprintf("%s (via %s)", reason, src)
	}
	if v, ok := o.Value.(string); ok && v == wantState {
		return true, string(state), fmt.Sprintf("surface.pipeline.state = %q (via %s)", v, src)
	}
	return false, string(state), fmt.Sprintf("surface.pipeline.state = %v, wanted %q (via %s)", o.Value, wantState, src)
}

// renderSignalTransportAvailable is the signal
// internal/coordinator/collector/noderender renders from
// [mqttproto.RenderSurfaceReport.TransportAvailable].
const renderSignalTransportAvailable = "surface.transport.available"

// confirmRenderTransportProbe polls surface.transport.available for
// evidence dated at or after dispatchedAt, the same poll shape
// confirmRenderCommand uses for surface.pipeline.state. nodeID is the node
// THIS probe was dispatched to — see evaluateRenderTransportProbe for why
// that matters, the same reason evaluateRenderSurfaceState takes it.
func (h *handlers) confirmRenderTransportProbe(ctx context.Context, nodeID, surfaceID string, dispatchedAt time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	if h.deps.Observations == nil {
		return false, string(observation.StateNotCollected), "no observation source is configured"
	}
	absDeadline := time.Now().Add(renderCommandConfirmDeadline)
	ticker := time.NewTicker(renderCommandPollInterval)
	defer ticker.Stop()

	for {
		confirmed, outcomeState, outcomeReason = h.evaluateRenderTransportProbe(ctx, nodeID, surfaceID, dispatchedAt)
		if confirmed {
			return true, outcomeState, outcomeReason
		}
		if !time.Now().Before(absDeadline) {
			return false, outcomeState, outcomeReason
		}
		select {
		case <-ctx.Done():
			return false, string(observation.StateUnknownAge), "confirmation aborted before the deadline: " + ctx.Err().Error()
		case <-ticker.C:
		}
	}
}

// evaluateRenderTransportProbe reports confirmed=true once a fresh
// surface.transport.available reading (dated at or after notBefore) exists
// for surfaceID, from the node this probe was dispatched to — deliberately
// regardless of its VALUE. Unlike evaluateRenderSurfaceState, which confirms
// only a specific desired pipeline state, a probe has no desired transport
// value: an operator asking "can this node send NDI now?" is equally well
// answered by true and by false. Refusing to confirm a correctly-reported
// false would make "the runtime genuinely is not installed" indistinguishable
// from "the probe never ran," which is exactly the false-claim direction
// ADR-026 decision 6 forbids. Filtered to renderNodeSourceFor(nodeID) rather
// than resolved across every node that has ever reported surfaceID, for the
// identical reason evaluateRenderSurfaceState is: a stale reading from a
// DIFFERENT node must never confirm or unconfirm a probe this dispatch never
// touched.
func (h *handlers) evaluateRenderTransportProbe(ctx context.Context, nodeID, surfaceID string, notBefore time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	kind := observation.ResourceSurface
	sig := observation.SignalID(renderSignalTransportAvailable)
	wantSource := renderNodeSourceFor(nodeID)
	obs, err := h.deps.Observations.ListObservations(ctx, ObservationFilter{ResourceKind: &kind, ResourceID: &surfaceID, Signal: &sig})
	if err != nil {
		return false, string(observation.StateCollectionFailed), "reading surface.transport.available for confirmation: " + err.Error()
	}
	var o observation.Observation
	var found bool
	for _, cand := range obs {
		if cand.Resource.Kind == kind && cand.Resource.ID == surfaceID && cand.Signal == sig && cand.Source == wantSource {
			o = cand
			found = true
			break
		}
	}
	if !found {
		return false, string(observation.StateNotCollected), fmt.Sprintf(
			"no surface.transport.available observation is recorded for this surface from node %s yet", nodeID)
	}
	src := o.Source
	if src == "" {
		src = "unknown source"
	}
	if o.CollectedAt.Before(notBefore) {
		return false, string(observation.StateNotCollected), fmt.Sprintf(
			"no surface.transport.available reading has arrived since this probe was dispatched at %s; the most recent evidence is from %s, via %s, and predates dispatch",
			notBefore.Format(time.RFC3339), o.CollectedAt.Format(time.RFC3339), src)
	}
	state := o.StateAt(h.now())
	if state != observation.StateCurrent {
		reason := o.Reason
		if reason == "" {
			reason = fmt.Sprintf("surface.transport.available evidence state is %s", state)
		}
		return false, string(state), fmt.Sprintf("%s (via %s)", reason, src)
	}
	v, _ := o.Value.(bool)
	return true, string(state), fmt.Sprintf("surface.transport.available = %v (via %s)", v, src)
}

// renderCommandResultFromRecord renders a replayed command's already-
// resolved outcome from its stored row, decoding ResultJSON's outcome
// field (mirroring commandResultPayload's shape).
func renderCommandResultFromRecord(rec store.CommandRecord, replay bool) v1.RenderCommandResult {
	var res commandResultPayload
	_ = json.Unmarshal([]byte(rec.ResultJSON), &res)
	var resolvedAt *string
	if rec.ResolvedAt != nil {
		resolvedAt = strPtr(formatTime(*rec.ResolvedAt))
	}
	dispatchedAt := ""
	if rec.DispatchedAt != nil {
		dispatchedAt = formatTime(*rec.DispatchedAt)
	}
	nodeID := rec.TargetID
	surfaceID := ""
	if params := decodeSurfaceIDFromParamsJSON(rec.ParamsJSON); params != "" {
		surfaceID = params
	}
	return v1.RenderCommandResult{
		CommandID: rec.ID, IdempotencyKey: rec.IdempotencyKey, Action: rec.Action,
		NodeID: nodeID, SurfaceID: surfaceID, Replay: replay,
		Outcome: res.Outcome, OutcomeState: rec.OutcomeState, OutcomeReason: rec.OutcomeReason,
		DispatchedAt: dispatchedAt, ResolvedAt: resolvedAt,
	}
}

func decodeSurfaceIDFromParamsJSON(raw string) string {
	var p struct {
		SurfaceID string `json:"surfaceId"`
	}
	_ = json.Unmarshal([]byte(raw), &p)
	return p.SurfaceID
}

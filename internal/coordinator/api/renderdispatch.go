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

	if action == "render.surface.apply" && body.SequenceID == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("sequenceId is required"))
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

	in := renderDispatchInput{
		Action: action, NodeID: nodeID, SurfaceID: surfaceID, SequenceID: body.SequenceID,
		IdempotencyKey: idempotencyKey, DesiredState: desiredState,
		IssuerID: issuerID, IssuerName: issuerName,
		ClientAddr: h.clientAddr(r), Form: ac.result.Form, CredentialID: ac.result.CredentialID,
	}

	// Idempotency-first, against the caller's own unresolved request
	// identity — before apply's resolution runs, since resolution reads
	// mutable state and a replay must never depend on it. A freshly minted
	// key can never already be bound to a row, so it skips straight through.
	if body.IdempotencyKey != "" {
		existing, lookupErr := h.deps.Commands.GetCommandByIdempotencyKey(ctx, idempotencyKey)
		switch {
		case lookupErr == nil:
			result, problem, err := h.resolveRenderCommandReplay(ctx, now, in, existing)
			if err != nil {
				h.writeInternalError(w, now, "resolve render command replay", err)
				return
			}
			if problem != nil {
				writeProblem(w, h.logger, now, *problem)
				return
			}
			jsonWrite(w, v1.RenderCommandResponse{ServerTime: formatTime(h.now()), Command: result})
			return
		case errors.Is(lookupErr, store.ErrCommandNotFound):
			// Genuinely new key — fall through to resolution and dispatch.
		default:
			h.writeInternalError(w, now, "look up render command by idempotency key", lookupErr)
			return
		}
	}

	var params map[string]any
	if action == "render.surface.apply" {
		var problem *v1.Problem
		var resolveErr error
		params, problem, resolveErr = h.resolveRenderApplyParams(ctx, nodeID, surfaceID, body.SequenceID)
		if resolveErr != nil {
			h.writeInternalError(w, now, "resolve render.surface.apply params", resolveErr)
			return
		}
		if problem != nil {
			writeProblem(w, h.logger, now, *problem)
			return
		}
		if params == nil {
			// Defensive: resolveRenderApplyParams's contract is "never a
			// nil params map without a problem or an error" — see that
			// function's doc comment (Finding 5). A future call site that
			// slips past both checks above must still never dispatch a
			// command with "params": null on the wire.
			h.writeInternalError(w, now, "resolve render.surface.apply params",
				errors.New("resolveRenderApplyParams returned nil params with no problem and no error"))
			return
		}
	} else {
		params = map[string]any{"surfaceId": surfaceID}
	}
	in.Params = params

	outcome, problem, err := h.executeRenderDispatch(ctx, now, in)
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
//
// Its result is one of exactly three shapes (Finding 5): (params, nil, nil)
// on success; (nil, problem, nil) when the CALLER's request is what cannot
// be resolved (bad sequence id, no active show, and the like) — a v1.Problem
// the caller reports to the operator; or (nil, nil, err) when THIS
// coordinator failed to read its own store (a transient SQLite error and
// the like) — an error the caller must report as its own 500, never as a
// node-side rejection. A coordinator-side failure disguised as "no params,
// no problem" used to reach dispatch as a real MQTT publish carrying
// "params": null, which a node then correctly refused — reported to the
// operator as the NODE having rejected the command, when the coordinator
// never actually resolved one.
func (h *handlers) resolveRenderApplyParams(ctx context.Context, nodeID, surfaceID, sequenceID string) (map[string]any, *v1.Problem, error) {
	if h.deps.AssetManifests == nil || h.deps.Config == nil {
		p := invalidParameterProblem("this coordinator has no asset store or config store wired in; render.surface.apply cannot resolve an assignment")
		return nil, &p, nil
	}

	obj, err := h.deps.Config.GetConfigObject(ctx, config.ShowSurfaceConfigKind, surfaceID)
	if err != nil {
		if errors.Is(err, store.ErrConfigObjectNotFound) {
			p := resourceNotFoundProblem(fmt.Sprintf("surface %q is not a configured show.surface object", surfaceID))
			return nil, &p, nil
		}
		return nil, nil, fmt.Errorf("read show.surface config object %q: %w", surfaceID, err)
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowSurfaceConfigKind, surfaceID, obj.CurrentRevision)
	if err != nil {
		return nil, nil, fmt.Errorf("read show.surface config revision for %q: %w", surfaceID, err)
	}
	payload, verr := config.DecodeShowSurfacePayload(rev.PayloadJSON, alwaysTrue, alwaysTrue)
	if verr != nil {
		p := invalidParameterProblem(fmt.Sprintf("surface %q has a stored payload that no longer decodes: %s", surfaceID, verr.Detail))
		return nil, &p, nil
	}
	if payload.Node != nodeID {
		p := invalidParameterProblem(fmt.Sprintf("surface %q is assigned to node %q, not %q", surfaceID, payload.Node, nodeID))
		return nil, &p, nil
	}

	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve active show: %w", err)
	}
	if !active.Configured {
		p := invalidParameterProblem("no active show is configured; render.surface.apply has no show to resolve an asset against")
		return nil, &p, nil
	}
	if payload.Show != active.ShowID {
		p := invalidParameterProblem(fmt.Sprintf("surface %q belongs to show %q, which is not the active show %q", surfaceID, payload.Show, active.ShowID))
		return nil, &p, nil
	}

	expected, err := assetsync.ExpectedAssetsForNode(ctx, h.deps.AssetManifests, active.ShowID, nodeID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve expected assets for node %q in show %q: %w", nodeID, active.ShowID, err)
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
		return nil, &p, nil
	default:
		if len(matches) > 1 {
			p := invalidParameterProblem(fmt.Sprintf("ambiguous: %d current assets match sequence %q for node %q in show %q; cannot resolve one FSEQ to assign", len(matches), sequenceID, nodeID, active.ShowID))
			return nil, &p, nil
		}
	}
	asset := matches[0]

	// render.settings.idleOutput is resolved to a CONCRETE value here, at
	// dispatch time, through the same [resolveRenderSettings] every other
	// reader uses (rendersettings.go) — the node is told a resolved value
	// and never has to know the coordinator's own default (build contract
	// ruling 4: the assignment is complete and self-contained). A
	// resolution error is treated the same as this function's other
	// internal-error paths (return nil, nil, rendered as a 500 by the
	// caller): render.settings' own store being unavailable is not a bad
	// request.
	settings, _, err := resolveRenderSettings(ctx, h.deps.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve render.settings: %w", err)
	}

	raw, err := json.Marshal(renderApplyParamsPayload{
		SurfaceID: surfaceID, Show: payload.Show, Name: payload.Name, Node: payload.Node,
		ChannelRange: payload.ChannelRange, Geometry: payload.Geometry, FrameRate: payload.FrameRate, Output: payload.Output,
		FSEQFilename: asset.Filename, FSEQContentHash: asset.ContentHash, IdleOutput: settings.IdleOutput,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("encode render.surface.apply params: %w", err)
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, nil, fmt.Errorf("decode render.surface.apply params back into a map: %w", err)
	}
	return params, nil, nil
}

// renderApplyParamsPayload's JSON key set matches
// internal/agent/renderops.go's renderApplyKnownKeys exactly (eleven keys,
// surfaceId plus the ten show.surface/asset/render.settings fields) — the
// same independently-reproduced-on-purpose relationship
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
	// IdleOutput is render.settings.idleOutput RESOLVED at dispatch time
	// (see resolveRenderApplyParams) — always one of black/hold/diagnostic,
	// never absent, because this function always resolves a concrete value
	// before building this payload.
	IdleOutput string `json:"idleOutput"`
}

// renderDispatchInput is [handlers.executeRenderDispatch]'s input, kept as
// its own struct so the HTTP wire adapter above and this core stay
// cleanly separated, matching FPPCommandInput's identical role one file
// over.
type renderDispatchInput struct {
	Action    string
	NodeID    string
	SurfaceID string
	// SequenceID is the caller's own apply request field, part of the
	// replay identity below. Empty for clear/restart/probe.
	SequenceID     string
	Params         map[string]any
	IdempotencyKey string
	DesiredState   string
	IssuerID       string
	IssuerName     string
	ClientAddr     string
	Form           identity.CredentialForm
	CredentialID   string
}

// renderRequestIdentity is the caller's own unresolved request shape,
// stored in commands.requested_revision — never in the mutable params_json
// a resolution produces.
type renderRequestIdentity struct {
	Action     string `json:"action"`
	NodeID     string `json:"node"`
	SurfaceID  string `json:"surface"`
	SequenceID string `json:"sequenceId"`
}

func renderRequestIdentityFor(in renderDispatchInput) renderRequestIdentity {
	return renderRequestIdentity{Action: in.Action, NodeID: in.NodeID, SurfaceID: in.SurfaceID, SequenceID: in.SequenceID}
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

	// canonicalParamsJSON (fppcommand_primitives.go): sorted keys, so a
	// stored row and a fresh call are byte-comparable.
	paramsJSON, err := canonicalParamsJSON(in.Params)
	if err != nil {
		return v1.RenderCommandResult{}, nil, fmt.Errorf("encode params: %w", err)
	}
	identityJSON, err := json.Marshal(renderRequestIdentityFor(in))
	if err != nil {
		return v1.RenderCommandResult{}, nil, fmt.Errorf("encode render request identity: %w", err)
	}

	commandID := uuid.NewString()
	rec := store.CommandRecord{
		ID: commandID, IdempotencyKey: in.IdempotencyKey, Action: in.Action,
		TargetKind: "node", TargetID: in.NodeID, ParamsJSON: paramsJSON,
		IssuerPrincipalID: in.IssuerID, IssuerPrincipalName: in.IssuerName,
		RequestedRevision:  string(identityJSON),
		ConfirmationMethod: "evidence", State: "pending",
	}
	inserted, err := h.deps.Commands.InsertCommand(ctx, rec)
	if err != nil {
		var dup *store.DuplicateCommandError
		if errors.As(err, &dup) {
			result, problem, rerr := h.resolveRenderCommandReplay(ctx, now, in, dup.Existing)
			return result, problem, rerr
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
	// pipelineFailed is Finding 15: only confirmRenderCommand's own
	// evaluateRenderSurfaceState path can ever set it — a transport probe
	// has no desired pipeline STATE to match and never reports one failed.
	var pipelineFailed bool
	if in.Action == "render.transport.probe" {
		// No desired STATE to match (unlike apply/clear/restart): a probe
		// that correctly reports the runtime absent is just as confirmed
		// as one that reports it present — see confirmRenderTransportProbe.
		confirmed, outcomeState, outcomeReason = h.confirmRenderTransportProbe(ctx, in.NodeID, in.SurfaceID, dispatchedAt)
	} else {
		// render.pipeline.restart's wantState "running" is what the surface
		// already was before this command (that is the whole point of a
		// restart), so a naive receipt-time fence could trivially confirm
		// off a pre-existing reading — Finding 4. evaluateRenderSurfaceState's
		// ObservedAt fence already closes this: internal/agent/pipeline.
		// runner.setState only stamps a fresh ObservedAt on a REAL state
		// transition, and both cmdApply and cmdRestart unconditionally run
		// stopCurrent+attemptStart (Starting, then Running), so a stale
		// pre-dispatch "running" reading can never satisfy the fence — a
		// restart-count movement check was tried here and reverted: the
		// real agent's RestartCount increments only on a crash-driven
		// restart (the exit branch in pipeline.runner.loop), never on an
		// operator-issued one, so requiring it to move would make every
		// render.pipeline.restart command time out. Confirmed against a
		// real agent (test/integration/render_dispatch_test.go).
		confirmed, outcomeState, outcomeReason, pipelineFailed = h.confirmRenderCommand(ctx, in.NodeID, in.SurfaceID, in.DesiredState, dispatchedAt)
	}
	resolvedAt := h.now()
	outcome := "unconfirmed"
	if confirmed {
		outcome = "confirmed"
	}
	resultJSON, _ := json.Marshal(commandResultPayload{Outcome: outcome, PipelineFailed: pipelineFailed})
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
		PipelineFailed: pipelineFailed,
		DispatchedAt:   dispatchedFmt, ResolvedAt: &resolvedFmt,
		IdleOutput: idleOutputFromParams(in.Params),
	}, nil, nil
}

// idleOutputFromParams reads params["idleOutput"] back out of the very map
// resolveRenderApplyParams built — empty (never a default) for
// clear/restart/probe, whose params never carry it.
func idleOutputFromParams(params map[string]any) string {
	v, _ := params["idleOutput"].(string)
	return v
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
func (h *handlers) confirmRenderCommand(ctx context.Context, nodeID, surfaceID, wantState string, dispatchedAt time.Time) (confirmed bool, outcomeState, outcomeReason string, pipelineFailed bool) {
	if h.deps.Observations == nil {
		return false, string(observation.StateNotCollected), "no observation source is configured", false
	}
	absDeadline := time.Now().Add(renderCommandConfirmDeadline)
	ticker := time.NewTicker(renderCommandPollInterval)
	defer ticker.Stop()

	for {
		confirmed, outcomeState, outcomeReason, pipelineFailed = h.evaluateRenderSurfaceState(ctx, nodeID, surfaceID, wantState, dispatchedAt)
		if confirmed {
			return true, outcomeState, outcomeReason, false
		}
		if !time.Now().Before(absDeadline) {
			return false, outcomeState, outcomeReason, pipelineFailed
		}
		select {
		case <-ctx.Done():
			return false, string(observation.StateUnknownAge), "confirmation aborted before the deadline: " + ctx.Err().Error(), false
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
func (h *handlers) evaluateRenderSurfaceState(ctx context.Context, nodeID, surfaceID, wantState string, notBefore time.Time) (confirmed bool, outcomeState, outcomeReason string, pipelineFailed bool) {
	kind := observation.ResourceSurface
	sig := observation.SignalID(renderSignalPipelineState)
	wantSource := renderNodeSourceFor(nodeID)
	obs, err := h.deps.Observations.ListObservations(ctx, ObservationFilter{ResourceKind: &kind, ResourceID: &surfaceID, Signal: &sig})
	if err != nil {
		return false, string(observation.StateCollectionFailed), "reading surface.pipeline.state for confirmation: " + err.Error(), false
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
			"no surface.pipeline.state observation is recorded for this surface from node %s yet", nodeID), false
	}
	src := o.Source
	if src == "" {
		src = "unknown source"
	}

	// A surface this node explicitly stops reporting, with a reason
	// (noderender.Collector.Poll's dropped-surface absence — see that
	// package), IS evidence the pipeline is gone: ADR-003's "evidence that
	// observed state moved", exactly as much as an explicit "stopped"
	// value would be. This absence carries no node-clock ObservedAt (there
	// is no sample to date), so it is fenced on CollectedAt — the moment
	// THIS coordinator's own poll actually noticed the drop, which is real
	// evidence of a transition for an absence row, unlike CollectedAt on a
	// value-bearing row (see below). Only wantState=="stopped"
	// (render.surface.clear) accepts this: render.surface.apply/
	// render.pipeline.restart want "running", which an absence can never
	// satisfy.
	if o.Absence == observation.StateNotCollected {
		if o.CollectedAt.Before(notBefore) {
			return false, string(observation.StateNotCollected), fmt.Sprintf(
				"no surface.pipeline.state reading has arrived since this command was dispatched at %s; the most recent evidence is from %s, via %s, and predates dispatch",
				notBefore.Format(time.RFC3339), o.CollectedAt.Format(time.RFC3339), src), false
		}
		if wantState == mqttproto.RenderPipelineStateStopped {
			reason := o.Reason
			if reason == "" {
				reason = "surface.pipeline.state was explicitly reported absent"
			}
			// Formatted with the same `surface.pipeline.state = %q` prefix
			// the value-observation branch below uses, so a caller reading
			// the outcome reason sees the desired state was reached either
			// way — only the parenthetical names how it was confirmed.
			return true, string(observation.StateCurrent), fmt.Sprintf("surface.pipeline.state = %q (absent: %s, via %s)", wantState, reason, src), false
		}
		return false, string(o.Absence), fmt.Sprintf("surface.pipeline.state is absent (%s), wanted %q (via %s)", o.Reason, wantState, src), false
	}

	// Value-bearing evidence is fenced on ObservedAt — the node's own
	// clock reading of when the condition was true (Finding 4) — never on
	// CollectedAt, the coordinator's receipt time. A report snapshotted
	// before dispatch and merely RECEIVED after it must not confirm; that
	// is the 179-microsecond defect this project has already paid for
	// once. ObservedAt==nil (genuinely unknown age) can never satisfy this
	// fence either, since there is then no evidence the reading post-dates
	// dispatch at all.
	if o.ObservedAt == nil {
		return false, string(observation.StateUnknownAge), fmt.Sprintf(
			"surface.pipeline.state evidence from node %s carries no observation timestamp (unknown age); cannot confirm it post-dates dispatch", nodeID), false
	}
	if o.ObservedAt.Before(notBefore) {
		return false, string(observation.StateNotCollected), fmt.Sprintf(
			"no surface.pipeline.state reading has arrived since this command was dispatched at %s; the most recent evidence was observed at %s, via %s, and predates dispatch",
			notBefore.Format(time.RFC3339), o.ObservedAt.Format(time.RFC3339), src), false
	}

	state := o.StateAt(h.now())
	if state != observation.StateCurrent {
		reason := o.Reason
		if reason == "" {
			reason = fmt.Sprintf("surface.pipeline.state evidence state is %s", state)
		}
		return false, string(state), fmt.Sprintf("%s (via %s)", reason, src), false
	}
	v, _ := o.Value.(string)
	if v == wantState {
		return true, string(state), fmt.Sprintf("surface.pipeline.state = %q (via %s)", v, src), false
	}
	// Finding 15: the pipeline's own reported value is distinct,
	// structured evidence — not merely "some other state" — exactly when
	// it equals mqttproto.RenderPipelineStateFailed. This is the ONLY
	// place PipelineFailed is ever set true; everywhere else in this
	// function it is false because the branch that fired is either
	// confirmation itself or a state that genuinely isn't "failed".
	pipelineFailed = v == mqttproto.RenderPipelineStateFailed
	return false, string(state), fmt.Sprintf("surface.pipeline.state = %v, wanted %q (via %s)", o.Value, wantState, src), pipelineFailed
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
	// Fenced on ObservedAt, not CollectedAt — the identical fix
	// evaluateRenderSurfaceState applies for Finding 4: the node's own
	// clock reading of when the probe result was true, never the
	// coordinator's receipt time.
	if o.ObservedAt == nil {
		return false, string(observation.StateUnknownAge), fmt.Sprintf(
			"surface.transport.available evidence from node %s carries no observation timestamp (unknown age); cannot confirm it post-dates dispatch", nodeID)
	}
	if o.ObservedAt.Before(notBefore) {
		return false, string(observation.StateNotCollected), fmt.Sprintf(
			"no surface.transport.available reading has arrived since this probe was dispatched at %s; the most recent evidence was observed at %s, via %s, and predates dispatch",
			notBefore.Format(time.RFC3339), o.ObservedAt.Format(time.RFC3339), src)
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

// renderCommandReplayConflictProblem mirrors fppCommandReplayConflictProblem
// (problem.go): a key reused against a different action or node.
func renderCommandReplayConflictProblem(existingID, existingAction, existingNodeID, requestedAction, requestedNodeID string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used for a different command",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (action %q, node %q); this request names a "+
				"different action %q or node %q. Mint a fresh idempotencyKey for a genuinely new request.",
			existingID, existingAction, existingNodeID, requestedAction, requestedNodeID),
	}
}

// renderCommandReplayIdentityConflictProblem mirrors
// renderCommandReplayConflictProblem one level down: same action and node,
// but a different surfaceId or (for apply) sequenceId.
func renderCommandReplayIdentityConflictProblem(existingID, action, nodeID string, existingSurfaceID, existingSequenceID, requestedSurfaceID, requestedSequenceID string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used with different parameters",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (action %q, node %q, surface %q, sequenceId %q); this request "+
				"has the SAME action and node but names a DIFFERENT surface %q or sequenceId %q. Mint a fresh idempotencyKey "+
				"for a genuinely new request.",
			existingID, action, nodeID, existingSurfaceID, existingSequenceID, requestedSurfaceID, requestedSequenceID),
	}
}

// resolveRenderCommandReplay answers a replayed idempotency key against
// existing's own stored row, judged against the caller's request identity
// rather than any resolution: an identical replay returns existing's own
// result verbatim; anything else is a conflict, dispatching nothing. Called
// both pre-resolution and from executeRenderDispatch's post-insert race —
// the idempotency_key UNIQUE constraint remains the sole authority on
// whether a duplicate exists; this only decides how to answer one. Every
// exit writes an AuditReplay entry, matching resolveFPPCommandReplay's
// entry shape (fppcommand_dispatch.go).
func (h *handlers) resolveRenderCommandReplay(ctx context.Context, now time.Time, in renderDispatchInput, existing store.CommandRecord) (v1.RenderCommandResult, *v1.Problem, error) {
	if existing.Action != in.Action || existing.TargetID != in.NodeID {
		p := renderCommandReplayConflictProblem(existing.ID, existing.Action, existing.TargetID, in.Action, in.NodeID)
		h.writeRenderAudit(ctx, now, identity.AuditReplay, in, store.CommandRecord{ID: existing.ID}, "")
		return v1.RenderCommandResult{}, &p, nil
	}

	want := renderRequestIdentityFor(in)
	var got renderRequestIdentity
	matched := false
	if existing.RequestedRevision != "" {
		if err := json.Unmarshal([]byte(existing.RequestedRevision), &got); err == nil {
			matched = got == want
		}
	}
	if !matched {
		p := renderCommandReplayIdentityConflictProblem(existing.ID, in.Action, in.NodeID, got.SurfaceID, got.SequenceID, want.SurfaceID, want.SequenceID)
		h.writeRenderAudit(ctx, now, identity.AuditReplay, in, store.CommandRecord{ID: existing.ID}, "")
		return v1.RenderCommandResult{}, &p, nil
	}

	h.writeRenderAudit(ctx, now, identity.AuditReplay, in, store.CommandRecord{ID: existing.ID}, "")
	return renderCommandResultFromRecord(existing, true), nil, nil
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
		PipelineFailed: res.PipelineFailed,
		DispatchedAt:   dispatchedAt, ResolvedAt: resolvedAt,
		IdleOutput: decodeIdleOutputFromParamsJSON(rec.ParamsJSON),
	}
}

// decodeIdleOutputFromParamsJSON mirrors decodeSurfaceIDFromParamsJSON one
// field over, for a replayed idempotency key's already-resolved idleOutput.
func decodeIdleOutputFromParamsJSON(raw string) string {
	var p struct {
		IdleOutput string `json:"idleOutput"`
	}
	_ = json.Unmarshal([]byte(raw), &p)
	return p.IdleOutput
}

func decodeSurfaceIDFromParamsJSON(raw string) string {
	var p struct {
		SurfaceID string `json:"surfaceId"`
	}
	_ = json.Unmarshal([]byte(raw), &p)
	return p.SurfaceID
}

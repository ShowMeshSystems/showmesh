package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is Track H seam H4's coordinator-side dispatch of the
// runner-neutral cue.activate operation (TRACK-H-cues-and-playlists.md
// section H4, docs/build/IDENTIFIER-REGISTER.md's own "cue.activate"
// reservation). It is shaped like cuecatalogdeploy.go's own dispatch core,
// NOT renderdispatch.go's: record the command durably (idempotency-first),
// publish it, and await the NODE'S OWN result on its result topic before
// recording anything as accepted — a bare successful publish is never
// conflated with acceptance (ADR-003). A node that refuses an activation
// (cross-show, stale-catalog, or any other TRACK-H-H3-SPEC.md section 6
// outcome) reports that refusal in its own result, and this file records
// it, with its outcome and evidence, exactly as it would a confirmed
// activation — never silently as "dispatched" the way a bare publish-and-
// forget would.
//
// [cueactivate.Decide] resolves WHAT to dispatch; [cueactivate.Authorize]
// is called here, immediately before every single publish, as this
// coordinator's own independent refusal check (TRACK-H-H3-SPEC.md section
// 6) — never trusted from whatever [cueactivate.Decide] itself computed,
// which may be stale by the time a queued or retried dispatch actually
// reaches the wire. Authorize's refusal and the node's OWN refusal are
// deliberately two distinct things this file records distinctly
// (AuthorizeOutcome vs. NodeOutcome below): one never reached the wire,
// the other did and was refused there — collapsing them would lose
// exactly the "did this node actually see and refuse it" evidence H3
// spec section 6 exists to preserve.

// cueActivationConfirmDeadline bounds how long a cue.activate dispatch
// waits for the node's own result-topic reply — mirrors
// cueCatalogDeployConfirmDeadline's identical role and reasoning one file
// over (cuecatalogdeploy.go). A package var, not a const, only so a test
// can shrink it deterministically; no runtime configuration ever
// reassigns it.
var cueActivationConfirmDeadline = 15 * time.Second

// cueActivationNodeOutcomeAuthorized is the exact string
// internal/agent/cueactivationops.go's activate reports (in its
// node.cue_activation.outcome evidence Value's own "outcome" field) when
// its own [cueauth.CheckLazy] passed and every declared output applied
// without error. It is independently reproduced here, not imported —
// this package must never import internal/agent (pkg/cuecatalog's own
// doc comment states the identical boundary) — matching this codebase's
// standing each-side-of-a-wire-boundary convention.
const cueActivationNodeOutcomeAuthorized = "authorized"

// cueActivationIssuer identifies who a cue.activate (or a blackAndSilence
// mismatch effect's render.surface.clear) dispatch is attributed to, in
// its audit entry and command envelope — mirrors FPPCommandIssuer's own
// doc comment (fppcommand_dispatch.go) one seam over: never a degraded
// "system" identity, always the real principal whose OWN authenticated
// request (the FPP plugin's observation POST, or an operator's manual
// re-trigger) is what set this dispatch in motion.
type cueActivationIssuer struct {
	PrincipalID   string
	PrincipalName string
	Form          identity.CredentialForm
	CredentialID  string
}

// cueActivationDispatchOutcome is one node's own dispatch result.
type cueActivationDispatchOutcome struct {
	NodeID string

	// Dispatched reports whether this activation reached the node's own
	// cmd topic (a successful publish) — never, by itself, evidence the
	// node accepted it. See Confirmed.
	Dispatched bool

	// Confirmed reports whether the NODE'S OWN result reported this
	// activation authorized and applied — TRACK-H-cues-and-playlists.md
	// section H4/ADR-003's "confirmation is the node's own result", never
	// inferred from Dispatched alone.
	Confirmed bool

	// AuthorizeOutcome is set only when THIS coordinator's own
	// [cueactivate.Authorize] refused — nothing was ever published for
	// this node.
	AuthorizeOutcome cueauth.Outcome

	// AuthorizeReason is [cueactivate.Authorize]'s own detail text,
	// populated only when AuthorizeOutcome is [cueauth.OutcomeAssetMissing]:
	// names the sequence and the asset the refusal is actually about,
	// never just the bare outcome string.
	AuthorizeReason string

	// RefusedCueOutputs is [cueactivate.Authorize]'s own resolved
	// [cuecatalog.Outputs] for the Activation's CueID on this node —
	// populated whenever Authorize found the Cue in nodeID's catalog at
	// all, regardless of whether it authorized or refused. A scoped
	// fail-to-black (see assetMissingFailToBlackTargets) reads this to
	// clear only the outputs this specific Cue actually declares, never
	// every surface or audio session on the node: an audio-only Cue's
	// refusal must never touch a render surface, and vice versa. Set for
	// BOTH kinds of asset-missing refusal this seam can see — this
	// coordinator's own pre-dispatch AuthorizeOutcome refusal, and the
	// node's own post-dispatch NodeOutcome refusal, since Authorize runs
	// (and resolves this) on every dispatch attempt regardless of which
	// side ultimately refuses.
	RefusedCueOutputs cuecatalog.Outputs

	// NodeOutcome is set once a result was actually received from the
	// node (or replayed from one previously received): either
	// [cueActivationNodeOutcomeAuthorized] or one of [cueauth.Outcome]'s
	// refusal strings, or "apply-failed" (internal/agent/
	// cueactivationops.go's own vocabulary for a Cue that authorized but
	// whose declared output failed to apply). Empty when no node result
	// was ever received (a coordinator-side dispatch error, or an
	// unconfirmed/timed-out await).
	NodeOutcome string

	Err error
}

// dispatchCueActivations authorizes and dispatches one cue.activate per
// (nodeID, Activation) in activations — see this file's own doc comment
// for why [cueactivate.Authorize] runs again here, per node, rather than
// trusting activations as already-authorized. A node cueauth refuses is
// simply skipped (recorded in its own outcome); it never blocks dispatch
// to the other nodes — H4's envelope is per-node, so a refusal for one
// node is not evidence about any other.
func (h *handlers) dispatchCueActivations(ctx context.Context, now time.Time, activations map[string]cueactivation.Activation, issuer cueActivationIssuer, pin *cueactivate.ShowPin) []cueActivationDispatchOutcome {
	out := make([]cueActivationDispatchOutcome, 0, len(activations))
	for nodeID, act := range activations {
		outcome := h.dispatchOneCueActivation(ctx, now, nodeID, act, issuer, pin)
		out = append(out, outcome)
		// Best-effort, independent of cue N's own outcome above — see
		// dispatchPrepareAheadAudio's own doc comment (cueactivationloop.go)
		// for why a wrong or stale guess here costs nothing. Cue N's own
		// activation, above, has already been dispatched (or refused) by
		// the time this runs, so nothing past this point may affect it —
		// including a panic: this whole method runs on runTick's own
		// detached goroutine (cueactivationloop.go's Run), so an unrecovered
		// panic here would not just skip a prepare-ahead cycle, it would
		// crash this entire coordinator process. h.safeDispatchPrepareAheadAudio
		// makes best-effort mean genuinely total.
		h.safeDispatchPrepareAheadAudio(ctx, now, nodeID, act, issuer)
	}
	return out
}

func (h *handlers) dispatchOneCueActivation(ctx context.Context, now time.Time, nodeID string, act cueactivation.Activation, issuer cueActivationIssuer, pin *cueactivate.ShowPin) cueActivationDispatchOutcome {
	inventoryInterval := h.deps.AssetSettings.InventoryInterval()
	refusalOutcome, refusalReason, cueOutputs, ok, err := cueactivate.Authorize(ctx, h.deps.AssetManifests, now, inventoryInterval, nodeID, act, pin)
	if err != nil {
		return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("authorize cue activation for node %q: %w", nodeID, err)}
	}
	if !ok {
		h.writeCueActivationRefusalAudit(ctx, now, nodeID, act, issuer, refusalOutcome, refusalReason)
		return cueActivationDispatchOutcome{NodeID: nodeID, AuthorizeOutcome: refusalOutcome, AuthorizeReason: refusalReason, RefusedCueOutputs: cueOutputs}
	}

	raw, err := json.Marshal(act)
	if err != nil {
		return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("encode activation for node %q: %w", nodeID, err)}
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("decode activation params for node %q: %w", nodeID, err)}
	}
	paramsJSON, err := canonicalParamsJSON(params)
	if err != nil {
		return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("canonicalize activation params for node %q: %w", nodeID, err)}
	}

	// act.ActivationID IS this command's own idempotency key: [cueactivate.
	// Decide]'s own doc comment guarantees it is stable for the identical
	// logical activation, so a repeated tick over an unchanged entry
	// (this seam's "an entry change dispatches exactly one activation"
	// requirement) replays the SAME command row rather than inserting a
	// second one and publishing again.
	commandID := uuid.NewString()
	rec := store.CommandRecord{
		ID: commandID, IdempotencyKey: act.ActivationID, Action: "cue.activate",
		TargetKind: "node", TargetID: nodeID, ParamsJSON: paramsJSON,
		IssuerPrincipalID: issuer.PrincipalID, IssuerPrincipalName: issuer.PrincipalName,
		ConfirmationMethod: "evidence", State: "pending",
	}
	inserted, err := h.deps.Commands.InsertCommand(ctx, rec)
	if err != nil {
		var dup *store.DuplicateCommandError
		if errors.As(err, &dup) {
			// Already dispatched for this exact activation: a replay, not
			// a second activation. Nothing new is published — answer from
			// the ALREADY-RECORDED outcome, which may itself be a node
			// refusal, never a bare "Dispatched: true". cueOutputs is
			// still this FRESH Authorize call's own resolution (run above,
			// unconditionally, before this duplicate-command branch was
			// ever reached) — attached here so a continuing node-side
			// refusal keeps re-scoping fail-to-black correctly on every
			// replay tick, not just the first.
			replayed := cueActivationOutcomeFromRecord(nodeID, dup.Existing)
			replayed.RefusedCueOutputs = cueOutputs
			return replayed
		}
		return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("insert cue.activate command for node %q: %w", nodeID, err)}
	}

	cmdTopic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("build cmd topic for node %q: %w", nodeID, err)}
	}
	resultTopic, err := mqttproto.ResultTopic(nodeID, commandID)
	if err != nil {
		return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("build result topic for node %q: %w", nodeID, err)}
	}
	payload := mqttproto.CmdPayload{
		CommandID: commandID, IdempotencyKey: act.ActivationID, Action: "cue.activate",
		Target: mqttproto.CmdTarget{Kind: "node", ID: nodeID}, Params: params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName},
		ConfirmationMethod: "evidence",
	}
	env, err := mqttproto.NewCmdEnvelope(func() time.Time { return now }, nodeID, payload)
	if err != nil {
		return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("build cmd envelope for node %q: %w", nodeID, err)}
	}
	rawEnv, err := json.Marshal(env)
	if err != nil {
		return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("marshal cmd envelope for node %q: %w", nodeID, err)}
	}

	h.writeCueActivationDispatchAudit(ctx, now, nodeID, act, issuer, inserted.ID)

	dispatchedAt := now
	if h.deps.AudioPublisher == nil {
		return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("no command publish-and-await capability is configured on this coordinator")}
	}
	msg, err := h.deps.AudioPublisher.AwaitResponse(ctx, broker.ResponseRequest{
		PublishTopic: cmdTopic, PublishPayload: rawEnv,
		PublishQoS: mqttproto.CmdDeliveryPolicy.QoS, PublishRetain: mqttproto.CmdDeliveryPolicy.Retain,
		ResponseTopic: resultTopic, ResponseQoS: mqttproto.ResultDeliveryPolicy.QoS,
		Deadline: cueActivationConfirmDeadline,
		Match: func(m broker.Message) bool {
			return cueActivationResultCorrelates(m.Payload, nodeID, commandID, act.ActivationID)
		},
	})
	if err != nil {
		if errors.Is(err, broker.ErrResponseFailedBeforePublish) {
			// Nothing reached the wire — the commands row must not claim a
			// dispatch that never happened.
			resolvedAt := h.now()
			resultJSON, _ := json.Marshal(cueActivationResultPayload{Outcome: "failed", Reason: err.Error()})
			_ = h.updateCommandOutcomeBounded(ctx, commandID, store.CommandOutcomeUpdate{
				ResolvedAt: &resolvedAt, State: strPtr("failed"), ResultJSON: strPtr(string(resultJSON)),
				OutcomeState: strPtr("collection_failed"), OutcomeReason: strPtr(err.Error()),
			})
			h.writeCueActivationOutcomeAudit(ctx, now, nodeID, act, issuer, "failed", err.Error())
			return cueActivationDispatchOutcome{NodeID: nodeID, Err: fmt.Errorf("publish cue.activate to node %q: %w", nodeID, err)}
		}
		// Published, but no reply arrived before the deadline (or the
		// await itself failed) — an honest "unconfirmed" outcome, never
		// conflated with acceptance (ADR-003): Confirmed stays false and
		// NodeOutcome stays empty, because no node result was ever
		// actually received.
		_ = h.deps.Commands.UpdateCommandOutcome(ctx, commandID, store.CommandOutcomeUpdate{
			DispatchedAt: &dispatchedAt, State: strPtr("dispatched"),
		})
		resolvedAt := h.now()
		reason := err.Error()
		resultJSON, _ := json.Marshal(cueActivationResultPayload{Outcome: mqttproto.OutcomeUnconfirmed, Reason: reason})
		_ = h.updateCommandOutcomeBounded(ctx, commandID, store.CommandOutcomeUpdate{
			ResolvedAt: &resolvedAt, State: strPtr("resolved"), ResultJSON: strPtr(string(resultJSON)),
			OutcomeState: strPtr("not_collected"), OutcomeReason: strPtr(reason),
		})
		h.writeCueActivationOutcomeAudit(ctx, now, nodeID, act, issuer, mqttproto.OutcomeUnconfirmed, reason)
		return cueActivationDispatchOutcome{NodeID: nodeID, Dispatched: true}
	}

	env2, err := mqttproto.DecodeEnvelope(msg.Payload)
	var res mqttproto.ResultPayload
	if err == nil {
		res, err = mqttproto.DecodeResultPayload(env2)
	}
	if err != nil {
		_ = h.deps.Commands.UpdateCommandOutcome(ctx, commandID, store.CommandOutcomeUpdate{
			DispatchedAt: &dispatchedAt, State: strPtr("dispatched"),
		})
		return cueActivationDispatchOutcome{NodeID: nodeID, Dispatched: true, Err: fmt.Errorf("decode cue.activate result from node %q: %w", nodeID, err)}
	}

	nodeOutcome := cueActivationNodeOutcomeFromResult(res)
	resolvedAt := h.now()
	resultJSON, _ := json.Marshal(cueActivationResultPayload{Outcome: res.Outcome, Reason: res.Reason, NodeOutcome: nodeOutcome})
	_ = h.updateCommandOutcomeBounded(ctx, commandID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, ResolvedAt: &resolvedAt, State: strPtr("resolved"),
		ResultJSON: strPtr(string(resultJSON)), OutcomeState: strPtr(res.Outcome), OutcomeReason: strPtr(res.Reason),
	})

	confirmed := res.Outcome == mqttproto.OutcomeConfirmed && nodeOutcome == cueActivationNodeOutcomeAuthorized
	if confirmed {
		h.writeCueActivationOutcomeAudit(ctx, now, nodeID, act, issuer, "confirmed", "")
	} else {
		reason := nodeOutcome
		if reason == "" {
			reason = res.Reason
		}
		h.writeCueActivationOutcomeAudit(ctx, now, nodeID, act, issuer, "refused", reason)
	}

	return cueActivationDispatchOutcome{NodeID: nodeID, Dispatched: true, Confirmed: confirmed, NodeOutcome: nodeOutcome, RefusedCueOutputs: cueOutputs}
}

// cueActivationResultPayload is the JSON this file persists into
// store.CommandRecord.ResultJSON — mirrors cueCatalogDeployResultPayload's
// identical role one file over (cuecatalogdeploy.go), narrowed to this
// action's own fields.
type cueActivationResultPayload struct {
	Outcome     string `json:"outcome"`
	Reason      string `json:"reason,omitempty"`
	NodeOutcome string `json:"nodeOutcome,omitempty"`
}

// cueActivationResultCorrelates mirrors cueCatalogDeployResultCorrelates
// exactly (cuecatalogdeploy.go), narrowed to the "cue.activate" action.
func cueActivationResultCorrelates(payload []byte, nodeID, commandID, idempotencyKey string) bool {
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil || env.NodeID != nodeID {
		return false
	}
	res, err := mqttproto.DecodeResultPayload(env)
	if err != nil {
		return false
	}
	return res.CommandID == commandID && res.IdempotencyKey == idempotencyKey && res.Action == "cue.activate"
}

// cueActivationNodeOutcomeFromResult extracts the node's own reported
// "outcome" field from res.Evidence.Value — internal/agent/
// cueactivationops.go's activate always populates Evidence.Value as a
// map carrying "outcome" (one of [cueActivationNodeOutcomeAuthorized], a
// [cueauth.Outcome] refusal string, or "apply-failed"), regardless of
// whether Confirmed was true or false: this is what lets a caller
// distinguish "the node refused this for reason X" from "no result was
// ever received", rather than collapsing every non-confirmed result into
// one opaque "unconfirmed". Returns "" when no such evidence is present
// (a malformed or missing result, or an outcome this coordinator's own
// vocabulary does not recognize — never guessed at).
func cueActivationNodeOutcomeFromResult(res mqttproto.ResultPayload) string {
	if res.Evidence == nil {
		return ""
	}
	m, ok := res.Evidence.Value.(map[string]any)
	if !ok {
		return ""
	}
	outcome, _ := m["outcome"].(string)
	return outcome
}

// cueActivationOutcomeFromRecord answers a replayed idempotency key from
// existing's own stored row — mirrors resolveCueCatalogDeployReplay's
// identical shape one file over (cuecatalogdeploy.go): a replay must
// report whatever was ACTUALLY recorded for this activation, including a
// node refusal, never a bare "Dispatched: true" that would silently
// upgrade a previously-refused activation to a successful-looking one on
// a later tick's redelivery.
func cueActivationOutcomeFromRecord(nodeID string, existing store.CommandRecord) cueActivationDispatchOutcome {
	var res cueActivationResultPayload
	_ = json.Unmarshal([]byte(existing.ResultJSON), &res)
	confirmed := res.Outcome == mqttproto.OutcomeConfirmed && res.NodeOutcome == cueActivationNodeOutcomeAuthorized
	return cueActivationDispatchOutcome{
		NodeID: nodeID, Dispatched: existing.DispatchedAt != nil, Confirmed: confirmed, NodeOutcome: res.NodeOutcome,
	}
}

// cueActivationAuditParams is the pinned identities every cue.activate
// audit entry (dispatch or refusal) carries — never a secret — so an
// operator reading the audit log sees exactly which Cue, Playlist, and
// generation this node was told, or refused, without cross-referencing
// the command row.
func cueActivationAuditParams(act cueactivation.Activation) map[string]any {
	return map[string]any{
		"show": act.Show, "generation": act.Generation, "catalogRevision": act.CatalogRevision,
		"playlist": act.Playlist, "entryId": act.EntryID, "cueId": act.CueID, "cueRevision": act.CueRevision,
	}
}

// writeCueActivationDispatchAudit records one cue.activate publish
// attempt (before the node's own result is known — see
// writeCueActivationOutcomeAudit for that).
func (h *handlers) writeCueActivationDispatchAudit(ctx context.Context, now time.Time, nodeID string, act cueactivation.Activation, issuer cueActivationIssuer, commandID string) {
	if h.deps.Identity == nil {
		return
	}
	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName,
		Form: issuer.Form, CredentialID: issuer.CredentialID,
		Action: "cue.activate", Target: "node:" + nodeID,
		Params: cueActivationAuditParams(act), IdempotencyKey: act.ActivationID,
		Kind: identity.AuditDispatch, CommandID: commandID,
	}
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.logWarn("cue activation dispatch audit write failed", "nodeId", nodeID, "error", err)
	}
}

// writeCueActivationOutcomeAudit records the NODE'S OWN result for a
// dispatched cue.activate — confirmed, refused, or failed/unconfirmed —
// distinct from writeCueActivationRefusalAudit, which records THIS
// coordinator's own pre-dispatch [cueactivate.Authorize] refusal (nothing
// ever reached the wire in that case). A refusal here is a state with
// evidence, never a silent no-op, per TRACK-H-H3-SPEC.md section 6.
func (h *handlers) writeCueActivationOutcomeAudit(ctx context.Context, now time.Time, nodeID string, act cueactivation.Activation, issuer cueActivationIssuer, outcome, reason string) {
	if h.deps.Identity == nil {
		return
	}
	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName,
		Form: issuer.Form, CredentialID: issuer.CredentialID,
		Action: "cue.activate", Target: "node:" + nodeID,
		Params: cueActivationAuditParams(act), IdempotencyKey: act.ActivationID,
		Kind: identity.AuditOutcome, Outcome: outcome, OutcomeReason: reason,
	}
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.logWarn("cue activation outcome audit write failed", "nodeId", nodeID, "error", err)
	}
}

// writeCueActivationRefusalAudit records this coordinator's own
// [cueactivate.Authorize] refusal — a stated result with evidence, never a
// silent skip, per H3 spec section 6. Nothing was ever published for this
// node when this is called; see writeCueActivationOutcomeAudit for the
// node's own post-dispatch refusal.
//
// reason is [cueactivate.Authorize]'s own detail text, non-empty only for
// an asset-missing refusal: the audit's OutcomeReason carries it in place
// of the bare outcome string, so an operator reading the audit log sees
// which sequence and asset are actually missing, never just
// "asset-missing" naming nothing. Every other refusal outcome carries no
// extra detail yet, so OutcomeReason falls back to the outcome string
// itself, exactly as before this fix.
func (h *handlers) writeCueActivationRefusalAudit(ctx context.Context, now time.Time, nodeID string, act cueactivation.Activation, issuer cueActivationIssuer, outcome cueauth.Outcome, reason string) {
	if h.deps.Identity == nil {
		return
	}
	outcomeReason := reason
	if outcomeReason == "" {
		outcomeReason = string(outcome)
	}
	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName,
		Form: issuer.Form, CredentialID: issuer.CredentialID,
		Action: "cue.activate", Target: "node:" + nodeID,
		Params: cueActivationAuditParams(act), IdempotencyKey: act.ActivationID,
		Kind: identity.AuditOutcome, Outcome: "refused", OutcomeReason: outcomeReason,
	}
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.logWarn("cue activation refusal audit write failed", "nodeId", nodeID, "error", err)
	}
}

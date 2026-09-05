package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the operator-fired cue-activation HTTP surface: POST /api/v1/cues/{id}/activate,
// an operator hand-firing one Cue directly from Live Control's
// Announcements control. It is the one path onto
// [handlers.dispatchOneCueActivation] (cueactivationdispatch.go) that does
// not run through the automatic FPP-observation-driven [CueActivationLoop]
// (cueactivationloop.go) at all - there is no FPP playlist entry behind
// this call, only an operator's own click.
//
// This handler calls dispatchOneCueActivation ALONE, once per
// participating node, never dispatchCueActivations (cueactivationdispatch.go):
// that wrapper also fires safeDispatchPrepareAheadAudio, which guesses the
// NEXT PLAYLIST entry - meaningless, and wrong, for a hand-fired
// announcement that has no playlist behind it at all.
//
// It always resolves live (pin == nil, ADR-033 program mode's own
// behavior): see [cueactivate.ResolveDirectCueActivations]'s own doc
// comment for why a hand-fired announcement has no frozen show-mode pin
// to reuse.

// scopeCueActivate exists only so api.go's route registration can take
// its address - see scopeActionInvoke's identical pattern (actioninvoke.go).
var scopeCueActivate = identity.ScopeCueActivate

// cueDirectActivationRunnerInstance is [cueactivation.Activation.
// RunnerInstance]'s fixed value for every directly-fired activation this
// route builds: there is no FPP instance behind a manual Fire click, so
// this names the origin plainly (distinct from any real FPP instance
// UUID) rather than leaving the field looking like one. WHO fired it is
// still recorded precisely, on the dispatch's own audit entry
// (cueActivationIssuer, cueactivationdispatch.go) - this constant only
// answers "what kind of thing reported this activation".
const cueDirectActivationRunnerInstance = "live-control"

// cueDirectActivationNonce is [cueactivate.ResolveDirectCueActivations]'s
// own occurrenceNonce source: an atomic, process-lifetime counter, added
// to the dispatch-time clock reading, so two operator clicks - however
// close together - never resolve to the same ActivationID and have the
// second mistaken for a replay of the first.
var cueDirectActivationNonce atomic.Int64

func nextCueDirectActivationNonce(now time.Time) int64 {
	return now.UnixNano() + cueDirectActivationNonce.Add(1)
}

// handleActivateCue serves POST /api/v1/cues/{id}/activate, behind
// writeGuard(&scopeCueActivate, ...). It takes no request body: a manual
// Fire is inherently a fresh operator action every time it is clicked,
// never a request this route should treat as a retry-replay of an
// earlier one, so there is no client-supplied idempotency key to accept -
// dispatchOneCueActivation's own act.ActivationID (minted fresh per call
// via [nextCueDirectActivationNonce]) is this route's only idempotency
// identity, exactly as it is for the automatic activation loop.
//
// 202 Accepted, never 200: the response's own per-node outcomes are this
// coordinator's own evidence, gathered synchronously (dispatchOneCueActivation
// awaits each node's own result before returning) before this response is
// written - never a claim of success, and never collapsed into one
// verdict across nodes.
//
// A request that would dispatch to ZERO nodes is refused outright (400),
// never answered 202 with an empty nodes array: an operator's explicit
// Fire click that reaches nothing is a refusal to act, not "nothing to
// report" - see the two invalidParameterProblem calls below, one per
// distinct reason (no active show at all, versus an active show whose
// Cue catalog resolves this Cue on no node).
func (h *handlers) handleActivateCue(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	cueID := r.PathValue("id")

	if h.deps.AssetManifests == nil {
		h.writeInternalError(w, now, "activate cue", errors.New("no asset manifest store is configured on this coordinator"))
		return
	}

	if _, err := h.deps.AssetManifests.GetConfigObject(ctx, config.ShowCueConfigKind, cueID); err != nil {
		if errors.Is(err, store.ErrConfigObjectNotFound) {
			writeProblem(w, h.logger, now, resourceNotFoundProblem(fmt.Sprintf("no show.cue with id %q exists", cueID)))
			return
		}
		h.writeInternalError(w, now, "get show.cue for activation", err)
		return
	}

	// Checked here, before resolution, so the two empty-activations causes
	// below stay distinguishable: resolveActivationsForCue's own doc
	// comment already treats "no active show" as an unconditional empty
	// map, which would otherwise be indistinguishable from "an active
	// show whose catalog resolves this Cue on no node".
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		h.writeInternalError(w, now, "resolve active show", err)
		return
	}
	if !active.Configured {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			fmt.Sprintf("no active show is configured; there is nothing to fire %q against", cueID)))
		return
	}

	nonce := nextCueDirectActivationNonce(now)
	activations, err := cueactivate.ResolveDirectCueActivations(ctx, h.deps.AssetManifests, now, cueID, cueDirectActivationRunnerInstance, nonce)
	if err != nil {
		h.writeInternalError(w, now, "resolve direct cue activations", err)
		return
	}
	if len(activations) == 0 {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			fmt.Sprintf("%q resolves on no node: no node's own Cue catalog currently declares any output for it", cueID)))
		return
	}

	ac := authFromContext(ctx)
	issuer := cueActivationIssuer{
		PrincipalID:   ac.result.Principal.ID,
		PrincipalName: ac.result.Principal.Name,
		Form:          ac.result.Form,
		CredentialID:  ac.result.CredentialID,
	}

	// Never dispatchCueActivations - see this file's own doc comment for
	// why: that wrapper's own safeDispatchPrepareAheadAudio call guesses a
	// playlist entry that does not exist here.
	nodes := make([]v1.CueActivationNodeOutcome, 0, len(activations))
	for nodeID, act := range activations {
		outcome := h.dispatchOneCueActivation(ctx, now, nodeID, act, issuer, nil)
		nodes = append(nodes, cueActivateWireOutcome(outcome))
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(v1.CueActivateResponse{ServerTime: formatTime(now), CueID: cueID, Nodes: nodes})
}

// cueActivateWireOutcome renders one node's own [cueActivationDispatchOutcome]
// (cueactivationdispatch.go) onto the wire, in the shared "confirmed" |
// "unconfirmed" | "refused" | "failed" vocabulary (ADR-020,
// [outcomeWordConfirmed] and its siblings, actioninvoke.go) every other
// command route already reports outcomes in:
//
//   - Err != nil: this coordinator's own dispatch attempt failed before
//     any node result could ever exist - "failed".
//   - AuthorizeOutcome != "": THIS coordinator's own pre-dispatch refusal
//     (cueactivate.Authorize) - nothing was ever published - "refused".
//   - Dispatched && Confirmed: the node's own result reported the
//     activation authorized and applied - "confirmed".
//   - Dispatched && a NodeOutcome was received: the node itself refused
//     or failed to apply it - "refused", carrying the node's own outcome
//     word as the reason.
//   - Dispatched with no NodeOutcome: published, but no confirmed result
//     arrived before the deadline - "unconfirmed", never conflated with
//     acceptance (ADR-003).
func cueActivateWireOutcome(o cueActivationDispatchOutcome) v1.CueActivationNodeOutcome {
	switch {
	case o.Err != nil:
		return v1.CueActivationNodeOutcome{NodeID: o.NodeID, Outcome: outcomeWordFailed, OutcomeReason: o.Err.Error()}
	case o.AuthorizeOutcome != "":
		reason := o.AuthorizeReason
		if reason == "" {
			reason = string(o.AuthorizeOutcome)
		}
		return v1.CueActivationNodeOutcome{NodeID: o.NodeID, Outcome: outcomeWordRefused, OutcomeReason: reason}
	case o.Dispatched && o.Confirmed:
		return v1.CueActivationNodeOutcome{NodeID: o.NodeID, Dispatched: true, Confirmed: true, Outcome: outcomeWordConfirmed}
	case o.Dispatched && o.NodeOutcome != "":
		return v1.CueActivationNodeOutcome{NodeID: o.NodeID, Dispatched: true, Outcome: outcomeWordRefused, OutcomeReason: o.NodeOutcome}
	case o.Dispatched:
		return v1.CueActivationNodeOutcome{
			NodeID: o.NodeID, Dispatched: true, Outcome: outcomeWordUnconfirmed,
			OutcomeReason: "no confirmed result arrived from this node before the deadline",
		}
	default:
		return v1.CueActivationNodeOutcome{NodeID: o.NodeID, Outcome: outcomeWordFailed, OutcomeReason: "no outcome was recorded for this node"}
	}
}

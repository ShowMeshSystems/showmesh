package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

// cueActionFireResultPayload is the ResultJSON of one fired cue action's
// command row, read back when a repeat tick replays the same activation.
type cueActionFireResultPayload struct {
	ActivationID string `json:"activationId"`
	CueID        string `json:"cueId"`
	Label        string `json:"label,omitempty"`
	Outcome      string `json:"outcome"`
}

// cueActionsActivation picks the one Activation that stands for the whole
// multi-node activation when firing its actions: the smallest ActivationID,
// which is stable across repeat ticks over the same entry.
func cueActionsActivation(activations map[string]cueactivation.Activation) (cueactivation.Activation, bool) {
	ids := make([]string, 0, len(activations))
	byID := make(map[string]cueactivation.Activation, len(activations))
	for _, act := range activations {
		ids = append(ids, act.ActivationID)
		byID[act.ActivationID] = act
	}
	if len(ids) == 0 {
		return cueactivation.Activation{}, false
	}
	sort.Strings(ids)
	return byID[ids[0]], true
}

// cueActionIdempotencyKey is one fired action's command-row key: the same
// activation, position, and action always produce the same key.
func cueActionIdempotencyKey(activationID string, index int, actionID string) string {
	sum := sha256.Sum256([]byte(activationID + "|" + strconv.Itoa(index) + "|" + actionID))
	return "cueaction-" + hex.EncodeToString(sum[:])
}

// cueActionIDs reads outputs.actions from the exact show.cue revision act
// was resolved against.
func (h *handlers) cueActionIDs(ctx context.Context, act cueactivation.Activation) ([]string, error) {
	if h.deps.Config == nil {
		return nil, nil
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowCueConfigKind, act.CueID, act.CueRevision)
	if err != nil {
		return nil, fmt.Errorf("read show.cue %q revision %d: %w", act.CueID, act.CueRevision, err)
	}
	var payload config.ShowCuePayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return nil, fmt.Errorf("decode show.cue %q revision %d: %w", act.CueID, act.CueRevision, err)
	}
	return payload.Outputs.Actions, nil
}

// fireCueActions fires act's Cue's show actions, in order, each at most
// once per activation. A failed action is reported in its own outcome and
// never stops the next one or the node dispatch. fresh[i] is false when
// outcome i was replayed from an earlier fire of the same activation.
func (h *handlers) fireCueActions(ctx context.Context, act cueactivation.Activation, actionIDs []string, issuer cueActivationIssuer) (outcomes []v1.CueActionOutcome, fresh []bool) {
	outcomes = make([]v1.CueActionOutcome, 0, len(actionIDs))
	fresh = make([]bool, 0, len(actionIDs))
	for i, actionID := range actionIDs {
		o, f := h.fireOneCueAction(ctx, act, i, actionID, issuer)
		outcomes = append(outcomes, o)
		fresh = append(fresh, f)
	}
	return outcomes, fresh
}

// startCueActivationActions fires the actions of the Cue behind activations
// on its own goroutine, owned by cueActivationFailToBlackWG so the loop's
// shutdown waits for it. It returns before any action is dispatched.
func (h *handlers) startCueActivationActions(ctx context.Context, activations map[string]cueactivation.Activation, issuer cueActivationIssuer) {
	act, ok := cueActionsActivation(activations)
	if !ok {
		return
	}
	actionIDs, err := h.cueActionIDs(ctx, act)
	if err != nil {
		h.logWarn("cue actions: could not read the cue's actions", "cueId", act.CueID, "activationId", act.ActivationID, "error", err)
		return
	}
	if len(actionIDs) == 0 {
		return
	}
	h.cueActivationFailToBlackWG.Add(1)
	go func() {
		defer h.cueActivationFailToBlackWG.Done()
		outcomes, fresh := h.fireCueActions(ctx, act, actionIDs, issuer)
		for i, o := range outcomes {
			if fresh[i] && o.Outcome != outcomeWordConfirmed {
				h.logWarn("cue actions: a show action did not confirm; the cue's other outputs were not affected",
					"cueId", act.CueID, "activationId", act.ActivationID, "actionId", o.ActionID, "outcome", o.Outcome, "reason", o.OutcomeReason)
			}
		}
	}()
}

func (h *handlers) fireOneCueAction(ctx context.Context, act cueactivation.Activation, index int, actionID string, issuer cueActivationIssuer) (v1.CueActionOutcome, bool) {
	key := cueActionIdempotencyKey(act.ActivationID, index, actionID)
	if existing, err := h.deps.Commands.GetCommandByIdempotencyKey(ctx, key); err == nil {
		return cueActionOutcomeFromRecord(actionID, existing), false
	} else if !errors.Is(err, store.ErrCommandNotFound) {
		return v1.CueActionOutcome{ActionID: actionID, Outcome: outcomeWordFailed, OutcomeReason: "this action could not be checked because of an internal coordinator error"}, firstUnrecordedCueAction(key)
	}

	rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.ShowActionConfigKind, actionID)
	if err != nil {
		return v1.CueActionOutcome{ActionID: actionID, Outcome: outcomeWordFailed, OutcomeReason: "this action could not be read because of an internal coordinator error"}, firstUnrecordedCueAction(key)
	}
	if problem != nil {
		return v1.CueActionOutcome{ActionID: actionID, Outcome: outcomeWordRefused, OutcomeReason: "this action no longer exists. Edit the cue to remove it."}, firstUnrecordedCueAction(key)
	}
	payload, err := decodeShowActionPayloadForRead(rev.PayloadJSON)
	if err != nil {
		return v1.CueActionOutcome{ActionID: actionID, Outcome: outcomeWordFailed, OutcomeReason: "this action's stored settings could not be read. Save the action again."}, firstUnrecordedCueAction(key)
	}
	if payload.Show != act.Show {
		return v1.CueActionOutcome{ActionID: actionID, Label: payload.Label, Outcome: outcomeWordRefused,
			OutcomeReason: fmt.Sprintf("this action belongs to show %q, not the active show %q. Edit the cue to use an action from its own show.", payload.Show, act.Show)}, firstUnrecordedCueAction(key)
	}

	auditAction := "action.invoke:" + payload.Target.Integration
	cmdID := uuid.NewString()
	callerIntent := store.FormatCallerIntent(store.CallerIntentRevision, strconv.FormatInt(rev.Revision, 10))
	auditParams := map[string]any{"actionId": actionID, "label": payload.Label, "revision": rev.Revision, "cueId": act.CueID, "activationId": act.ActivationID}
	pending, _ := json.Marshal(cueActionFireResultPayload{ActivationID: act.ActivationID, CueID: act.CueID, Label: payload.Label})
	rec := store.CommandRecord{
		ID: cmdID, IdempotencyKey: key, Action: auditAction,
		TargetKind: actionInvokeTargetKind, TargetID: actionID,
		IssuerPrincipalID: issuer.PrincipalID, IssuerPrincipalName: issuer.PrincipalName,
		CallerIntent: callerIntent, ConfirmationMethod: string(command.ConfirmationEvidence), State: "pending",
		ResultJSON: string(pending),
	}
	if _, err := h.deps.Commands.InsertCommand(ctx, rec); err != nil {
		var dup *store.DuplicateCommandError
		if errors.As(err, &dup) {
			return cueActionOutcomeFromRecord(actionID, dup.Existing), false
		}
		return v1.CueActionOutcome{ActionID: actionID, Label: payload.Label, Outcome: outcomeWordFailed, OutcomeReason: "this action could not be recorded because of an internal coordinator error"}, firstUnrecordedCueAction(key)
	}

	now := h.now()
	h.writeCueActionAudit(ctx, identity.AuditEntry{
		Timestamp: now, PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName,
		Form: issuer.Form, CredentialID: issuer.CredentialID,
		Action: auditAction, Target: actionID, IdempotencyKey: key,
		Kind: identity.AuditDispatch, CommandID: cmdID, Params: auditParams,
	})

	ac := authContext{result: identity.Authenticated{
		Principal: identity.Principal{ID: issuer.PrincipalID, Name: issuer.PrincipalName},
		Form:      issuer.Form, CredentialID: issuer.CredentialID,
	}, ok: true}
	dispatchCtx, cancel := context.WithTimeout(ctx, actionInvokeHTTPWriteDeadline-actionInvokeBookkeepingBudget)
	outcome, outcomeState, outcomeReason, dispatchedAt, resolvedAt := h.dispatchActionTarget(dispatchCtx, payload, cmdID, callerIntent, rev.Revision, ac, "", payload.SafetyClass != config.ShowSafetyClassNone)
	cancel()
	if outcomeState == "" && outcome == outcomeWordConfirmed {
		outcomeState = "current"
	}

	h.writeCueActionAudit(ctx, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName,
		Form: issuer.Form, CredentialID: issuer.CredentialID,
		Action: auditAction, Target: actionID, IdempotencyKey: key,
		Kind: identity.AuditOutcome, CommandID: cmdID, Params: auditParams,
		Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
	})
	final, _ := json.Marshal(cueActionFireResultPayload{ActivationID: act.ActivationID, CueID: act.CueID, Label: payload.Label, Outcome: outcome})
	if err := h.updateCommandOutcomeBounded(ctx, cmdID, store.CommandOutcomeUpdate{
		DispatchedAt: dispatchedAt, ResolvedAt: &resolvedAt, State: strPtr("resolved"),
		ResultJSON: strPtr(string(final)), OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	}); err != nil {
		h.logWarn("cue actions: failed to record action outcome", "commandId", cmdID, "error", err)
	}
	return v1.CueActionOutcome{ActionID: actionID, Label: payload.Label, Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason}, true
}

// cueActionUnrecorded remembers keys whose action was refused before a
// command row existed, so a repeat tick reports it without logging again.
var cueActionUnrecorded sync.Map

func firstUnrecordedCueAction(key string) bool {
	_, seen := cueActionUnrecorded.LoadOrStore(key, struct{}{})
	return !seen
}

// cueActionOutcomeFromRecord answers a repeat of an already-fired action
// from its stored row, so a replay reports what actually happened.
func cueActionOutcomeFromRecord(actionID string, existing store.CommandRecord) v1.CueActionOutcome {
	var res cueActionFireResultPayload
	_ = json.Unmarshal([]byte(existing.ResultJSON), &res)
	outcome := res.Outcome
	reason := existing.OutcomeReason
	if outcome == "" {
		outcome = outcomeWordUnconfirmed
		if reason == "" {
			reason = "this action was sent for this activation but its outcome was never recorded"
		}
	}
	return v1.CueActionOutcome{ActionID: actionID, Label: res.Label, Outcome: outcome, OutcomeState: existing.OutcomeState, OutcomeReason: reason}
}

func (h *handlers) writeCueActionAudit(ctx context.Context, entry identity.AuditEntry) {
	if h.deps.Identity == nil {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.deps.Identity.WriteAudit(wctx, entry); err != nil {
		h.logWarn("cue actions: audit write failed", "action", entry.Action, "target", entry.Target, "error", err)
	}
}

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
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/sendsignal"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

// cueActionsSendBudget caps how long a cue's node dispatch waits for its
// show actions to be sent. Actions still unsent past it are sent late.
var cueActionsSendBudget = 2 * time.Second

// cueActionUnresolvedCommandAction names the command row of an action that
// could not be resolved to an integration, so it was never sent.
const cueActionUnresolvedCommandAction = "action.invoke"

const cueActionLateReason = "Sent late, after the cue's other outputs started. "

// cueActionFireResultPayload is the ResultJSON of one fired cue action's
// command row, read back when a repeat tick replays the same activation.
type cueActionFireResultPayload struct {
	CueID   string `json:"cueId"`
	Label   string `json:"label,omitempty"`
	Outcome string `json:"outcome"`
}

// cueActionsRun is one activation's show actions in flight.
type cueActionsRun struct {
	sent chan struct{}
	done chan struct{}

	mu       sync.Mutex
	outcomes []v1.CueActionOutcome
	fresh    []bool
}

func (r *cueActionsRun) set(i int, o v1.CueActionOutcome, fresh bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes[i] = o
	r.fresh[i] = fresh
}

// snapshot copies the outcomes known now. An action still waiting for its
// confirmation reads as unconfirmed.
func (r *cueActionsRun) snapshot() []v1.CueActionOutcome {
	if r == nil {
		return []v1.CueActionOutcome{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]v1.CueActionOutcome{}, r.outcomes...)
}

func (r *cueActionsRun) anyFresh() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.fresh {
		if f {
			return true
		}
	}
	return false
}

// waitSent blocks until every action is sent or the send budget runs out.
func (r *cueActionsRun) waitSent() {
	if r == nil {
		return
	}
	t := time.NewTimer(cueActionsSendBudget)
	defer t.Stop()
	select {
	case <-r.sent:
	case <-t.C:
	}
}

func (r *cueActionsRun) waitDone() {
	if r != nil {
		<-r.done
	}
}

type cueActionsRunKey struct{}

func withCueActionsRun(ctx context.Context, r *cueActionsRun) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, cueActionsRunKey{}, r)
}

func cueActionsRunFrom(ctx context.Context) *cueActionsRun {
	r, _ := ctx.Value(cueActionsRunKey{}).(*cueActionsRun)
	return r
}

// cueActionsActivation picks the node-independent fields every per-node
// Activation of one activation shares, from the smallest node id.
func cueActionsActivation(activations map[string]cueactivation.Activation) (cueactivation.Activation, bool) {
	nodeIDs := make([]string, 0, len(activations))
	for nodeID := range activations {
		nodeIDs = append(nodeIDs, nodeID)
	}
	if len(nodeIDs) == 0 {
		return cueactivation.Activation{}, false
	}
	sort.Strings(nodeIDs)
	return activations[nodeIDs[0]], true
}

// cueActionsActivationKey identifies one activation without naming any
// node, so a node joining mid-entry cannot fire the actions again.
func cueActionsActivationKey(act cueactivation.Activation, occurrence int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s|%d|%s|%s|%d|%d",
		act.RunnerInstance, act.Show, act.Generation, act.Playlist, act.PlaylistRevision,
		act.EntryID, act.CueID, act.CueRevision, occurrence)))
	return hex.EncodeToString(sum[:])
}

// cueActionIdempotencyKey is one fired action's command-row key: the same
// activation, position, and action always produce the same key.
func cueActionIdempotencyKey(activationKey string, index int, actionID string) string {
	sum := sha256.Sum256([]byte(activationKey + "|" + strconv.Itoa(index) + "|" + actionID))
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

// startCueActions fires the show actions of the cue behind activations, in
// order: each starts once the one before it is sent, never waiting for its
// confirmation. It returns nil when the cue has no actions. The goroutine
// is owned by cueActivationFailToBlackWG so the loop's shutdown waits for it.
func (h *handlers) startCueActions(ctx context.Context, activations map[string]cueactivation.Activation, occurrence int64, issuer cueActivationIssuer) *cueActionsRun {
	act, ok := cueActionsActivation(activations)
	if !ok {
		return nil
	}
	actionIDs, err := h.cueActionIDs(ctx, act)
	if err != nil {
		h.logWarn("cue actions: could not read the cue's actions; its other outputs still start", "cueId", act.CueID, "error", err)
		return nil
	}
	if len(actionIDs) == 0 {
		return nil
	}
	run := &cueActionsRun{
		sent: make(chan struct{}), done: make(chan struct{}),
		outcomes: make([]v1.CueActionOutcome, len(actionIDs)), fresh: make([]bool, len(actionIDs)),
	}
	for i, id := range actionIDs {
		run.outcomes[i] = v1.CueActionOutcome{ActionID: id, Outcome: outcomeWordUnconfirmed, OutcomeReason: "This action has not been sent yet."}
	}
	activationKey := cueActionsActivationKey(act, occurrence)
	budgetEnds := time.Now().Add(cueActionsSendBudget)

	h.cueActivationFailToBlackWG.Add(1)
	go func() {
		defer h.cueActivationFailToBlackWG.Done()
		var wg sync.WaitGroup
		for i, actionID := range actionIDs {
			late := time.Now().After(budgetEnds)
			sent := make(chan struct{})
			var once sync.Once
			markSent := func() { once.Do(func() { close(sent) }) }
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer markSent()
				defer func() {
					if r := recover(); r != nil {
						run.set(i, v1.CueActionOutcome{ActionID: actionID, Outcome: outcomeWordFailed, OutcomeReason: "this action stopped because of an internal coordinator error"}, true)
					}
				}()
				o, fresh := h.fireOneCueAction(sendsignal.WithHook(ctx, markSent), act, activationKey, i, actionID, issuer, late)
				run.set(i, o, fresh)
				if fresh && o.Outcome != outcomeWordConfirmed {
					h.logWarn("cue actions: a show action did not confirm; the cue's other outputs were not affected",
						"cueId", act.CueID, "actionId", actionID, "outcome", o.Outcome, "reason", o.OutcomeReason)
				}
			}()
			<-sent
		}
		close(run.sent)
		wg.Wait()
		close(run.done)
	}()
	return run
}

// dispatchCueActivationsWithActions is the playlist loop's dispatch: the
// cue's show actions are sent first, up to cueActionsSendBudget, then the
// node dispatch runs, and the action outcomes land on the activation later.
func (h *handlers) dispatchCueActivationsWithActions(ctx context.Context, now time.Time, activations map[string]cueactivation.Activation, occurrence int64, issuer cueActivationIssuer, pin *cueactivate.ShowPin) []cueActivationDispatchOutcome {
	run := h.startCueActions(ctx, activations, occurrence, issuer)
	run.waitSent()
	outcomes := h.dispatchCueActivations(withCueActionsRun(ctx, run), now, activations, issuer, pin)
	if run != nil {
		h.cueActivationFailToBlackWG.Add(1)
		go func() {
			defer h.cueActivationFailToBlackWG.Done()
			h.finishCueActions(ctx, run, activations)
		}()
	}
	return outcomes
}

// finishCueActions waits for every action's outcome and then writes the
// outcomes onto each node's cue.activate command row.
func (h *handlers) finishCueActions(ctx context.Context, run *cueActionsRun, activations map[string]cueactivation.Activation) {
	if run == nil {
		return
	}
	run.waitDone()
	if !run.anyFresh() {
		return
	}
	actions := run.snapshot()
	for nodeID, act := range activations {
		rec, err := h.deps.Commands.GetCommandByIdempotencyKey(ctx, act.ActivationID)
		if err != nil {
			continue
		}
		var res cueActivationResultPayload
		_ = json.Unmarshal([]byte(rec.ResultJSON), &res)
		res.Actions = actions
		raw, err := json.Marshal(res)
		if err != nil {
			continue
		}
		if err := h.updateCommandOutcomeBounded(ctx, rec.ID, store.CommandOutcomeUpdate{ResultJSON: strPtr(string(raw))}); err != nil {
			h.logWarn("cue actions: failed to record action outcomes on the activation", "nodeId", nodeID, "error", err)
		}
	}
}

// fireOneCueAction fires one action at most once per activation. fresh is
// false when the outcome was replayed from an earlier fire.
func (h *handlers) fireOneCueAction(ctx context.Context, act cueactivation.Activation, activationKey string, index int, actionID string, issuer cueActivationIssuer, late bool) (v1.CueActionOutcome, bool) {
	key := cueActionIdempotencyKey(activationKey, index, actionID)
	if existing, err := h.deps.Commands.GetCommandByIdempotencyKey(ctx, key); err == nil {
		return cueActionOutcomeFromRecord(actionID, existing), false
	} else if !errors.Is(err, store.ErrCommandNotFound) {
		return v1.CueActionOutcome{ActionID: actionID, Outcome: outcomeWordFailed, OutcomeReason: "this action could not be checked because of an internal coordinator error"}, true
	}

	rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.ShowActionConfigKind, actionID)
	if err != nil {
		return h.recordUnsentCueAction(ctx, act, key, actionID, cueActionUnresolvedCommandAction, "", issuer, outcomeWordFailed, "this action could not be read because of an internal coordinator error")
	}
	if problem != nil {
		return h.recordUnsentCueAction(ctx, act, key, actionID, cueActionUnresolvedCommandAction, "", issuer, outcomeWordRefused, "this action no longer exists. Edit the cue to remove it.")
	}
	payload, err := decodeShowActionPayloadForRead(rev.PayloadJSON)
	if err != nil {
		return h.recordUnsentCueAction(ctx, act, key, actionID, cueActionUnresolvedCommandAction, "", issuer, outcomeWordFailed, "this action's stored settings could not be read. Save the action again.")
	}
	auditAction := "action.invoke:" + payload.Target.Integration
	if payload.Show != act.Show {
		return h.recordUnsentCueAction(ctx, act, key, actionID, auditAction, payload.Label, issuer, outcomeWordRefused,
			fmt.Sprintf("this action belongs to show %q, not the active show %q. Edit the cue to use an action from its own show.", payload.Show, act.Show))
	}

	cmdID := uuid.NewString()
	callerIntent := store.FormatCallerIntent(store.CallerIntentRevision, strconv.FormatInt(rev.Revision, 10))
	auditParams := map[string]any{"actionId": actionID, "label": payload.Label, "revision": rev.Revision, "cueId": act.CueID, "activationKey": activationKey}
	pending, _ := json.Marshal(cueActionFireResultPayload{CueID: act.CueID, Label: payload.Label})
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
		return v1.CueActionOutcome{ActionID: actionID, Label: payload.Label, Outcome: outcomeWordFailed, OutcomeReason: "this action could not be recorded because of an internal coordinator error"}, true
	}

	h.writeCueActionAudit(ctx, identity.AuditEntry{
		Timestamp: h.now(), PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName,
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
	if late {
		outcomeReason = cueActionLateReason + outcomeReason
	}

	h.writeCueActionAudit(ctx, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName,
		Form: issuer.Form, CredentialID: issuer.CredentialID,
		Action: auditAction, Target: actionID, IdempotencyKey: key,
		Kind: identity.AuditOutcome, CommandID: cmdID, Params: auditParams,
		Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
	})
	final, _ := json.Marshal(cueActionFireResultPayload{CueID: act.CueID, Label: payload.Label, Outcome: outcome})
	if err := h.updateCommandOutcomeBounded(ctx, cmdID, store.CommandOutcomeUpdate{
		DispatchedAt: dispatchedAt, ResolvedAt: &resolvedAt, State: strPtr("resolved"),
		ResultJSON: strPtr(string(final)), OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	}); err != nil {
		h.logWarn("cue actions: failed to record action outcome", "commandId", cmdID, "error", err)
	}
	return v1.CueActionOutcome{ActionID: actionID, Label: payload.Label, Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason}, true
}

// recordUnsentCueAction records an action refused before it was sent, under
// its activation key, so a repeat tick replays the refusal.
func (h *handlers) recordUnsentCueAction(ctx context.Context, act cueactivation.Activation, key, actionID, commandAction, label string, issuer cueActivationIssuer, outcome, reason string) (v1.CueActionOutcome, bool) {
	now := h.now()
	result, _ := json.Marshal(cueActionFireResultPayload{CueID: act.CueID, Label: label, Outcome: outcome})
	rec := store.CommandRecord{
		ID: uuid.NewString(), IdempotencyKey: key, Action: commandAction,
		TargetKind: actionInvokeTargetKind, TargetID: actionID,
		IssuerPrincipalID: issuer.PrincipalID, IssuerPrincipalName: issuer.PrincipalName,
		ConfirmationMethod: string(command.ConfirmationEvidence), State: "resolved",
		ResolvedAt: &now, ResultJSON: string(result), OutcomeState: outcome, OutcomeReason: reason,
	}
	if _, err := h.deps.Commands.InsertCommand(ctx, rec); err != nil {
		var dup *store.DuplicateCommandError
		if errors.As(err, &dup) {
			return cueActionOutcomeFromRecord(actionID, dup.Existing), false
		}
		h.logWarn("cue actions: failed to record an unsent action", "actionId", actionID, "error", err)
	}
	return v1.CueActionOutcome{ActionID: actionID, Label: label, Outcome: outcome, OutcomeReason: reason}, true
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
			reason = "This action was sent for this activation and has no recorded outcome yet."
		}
	}
	return v1.CueActionOutcome{ActionID: actionID, Label: res.Label, Outcome: outcome, OutcomeState: existing.OutcomeState, OutcomeReason: reason}
}

func (h *handlers) writeCueActionAudit(ctx context.Context, entry identity.AuditEntry) {
	if h.deps.Identity == nil {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbWriteTimeout)
	defer cancel()
	if err := h.deps.Identity.WriteAudit(wctx, entry); err != nil {
		h.logWarn("cue actions: audit write failed", "action", entry.Action, "target", entry.Target, "error", err)
	}
}

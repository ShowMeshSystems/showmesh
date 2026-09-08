package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// GET /api/v1/actions/{id}/binding and GET /api/v1/actions/bindings
// re-resolve a stored show.action's target against current integration
// state. A read: dispatches nothing, requires no credential. The result is
// three-valued: "ok", "broken", or "unknown" — never "ok" for a check that
// did not run, never "broken" for one that could not.
//
// handleListActionBindings' result also carries a second, narrower
// relation: one entry per show.macro step whose own action reference no
// longer resolves to a live show.action (a tombstoned action a macro
// step still names). That is a different question from an action's own
// binding to ITS target, reusing the same [v1.ActionBinding] shape
// (ok/broken/unknown, ActionID/Show/Reason) because the answer is the
// same kind of fact, not because the two
// relations are the same thing; see [handlers.macroStepActionBindings]'s
// own doc comment for the exact rule. handleGetActionBinding (the
// single-id form) is NOT extended: a dangling id there already 404s,
// which is the correct, separate answer for "this id names nothing" and
// is not this file's concern to change.

func (h *handlers) handleGetActionBinding(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")

	binding, problem, err := h.checkActionBindingByID(r.Context(), id)
	if err != nil {
		h.writeInternalError(w, now, "check show.action binding", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	jsonWrite(w, v1.ActionBindingResponse{ServerTime: formatTime(now), Binding: binding})
}

func (h *handlers) handleListActionBindings(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	showFilter := r.URL.Query().Get("show")

	objs, err := h.deps.Config.ListConfigObjects(r.Context(), config.ShowActionConfigKind)
	if err != nil {
		h.writeInternalError(w, now, "list show.action config objects for binding check", err)
		return
	}

	fetchFPPEndpoints := h.memoizedFPPEndpoints(r.Context())

	bindings := make([]v1.ActionBinding, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(r.Context(), config.ShowActionConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			h.writeInternalError(w, now, "get active show.action config revision for binding check", err)
			return
		}
		payload, decodeErr := decodeShowActionPayloadForRead(rev.PayloadJSON)
		if decodeErr != nil {
			bindings = append(bindings, v1.ActionBinding{
				ActionID: obj.ID, State: v1.ActionBindingStateUnknown,
				Reason: fmt.Sprintf("this coordinator could not decode the stored payload: %v", decodeErr),
			})
			continue
		}
		if showFilter != "" && payload.Show != showFilter {
			continue
		}
		bindings = append(bindings, h.checkActionBindingTarget(r.Context(), obj.ID, payload, fetchFPPEndpoints))
	}

	macroBindings, err := h.macroStepActionBindings(r.Context(), showFilter)
	if err != nil {
		h.writeInternalError(w, now, "check show.macro step action bindings", err)
		return
	}
	bindings = append(bindings, macroBindings...)

	jsonWrite(w, v1.ActionBindingsResponse{ServerTime: formatTime(now), Bindings: bindings})
}

// macroStepActionBindings is GET /api/v1/actions/bindings' macro edge:
// for every show.macro, for every step, one [v1.ActionBinding] entry when
// step.Action does not resolve to a live show.action via
// [ConfigStore.GetConfigObject], the SAME tombstone-filtering existence
// check [nodeHasAudioNodeObject] already uses for the audio-node edge,
// applied one reference hop over. A step whose action DOES resolve emits
// NOTHING: this must never report a healthy macro's steps as a list of
// "ok" bindings, both because that is not the question this function
// answers (it never re-resolves the action's own target) and because a
// check that always emits something can never be told apart from one that
// reports everything broken.
//
// Multiplicity is deliberately per (macro, step), never deduplicated by
// action id: two steps (in the same or different macros) naming the same
// missing action id each produce their own entry, because each has its
// own macro id and step id to report and a reader must be able to tell
// which macro and step an entry came from: Reason always names both.
//
// showFilter narrows by the MACRO's own Show, matching the action loop
// above it exactly (a macro belongs to one show, same as an action does).
//
// A macro whose stored payload cannot be decoded reports ONE
// [v1.ActionBindingStateUnknown] entry naming the macro itself (ActionID
// carries the macro's own object id here, not an action id, since there
// is no step to read an action id from) rather than being silently skipped:
// "this macro's steps could not be checked" must never look the same as
// "this macro's steps are all fine". A [ConfigStore.GetConfigObject]
// failure other than "not found" while resolving one step is reported the
// same way, scoped to that one step.
func (h *handlers) macroStepActionBindings(ctx context.Context, showFilter string) ([]v1.ActionBinding, error) {
	objs, err := h.deps.Config.ListConfigObjects(ctx, config.ShowMacroConfigKind)
	if err != nil {
		return nil, fmt.Errorf("list show.macro config objects for binding check: %w", err)
	}

	var out []v1.ActionBinding
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowMacroConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("get active show.macro config revision %q for binding check: %w", obj.ID, err)
		}
		var payload config.ShowMacroPayload
		if decodeErr := jsonUnmarshalStrict(rev.PayloadJSON, &payload); decodeErr != nil {
			out = append(out, v1.ActionBinding{
				ActionID: obj.ID, State: v1.ActionBindingStateUnknown,
				Reason: fmt.Sprintf("show.macro %q could not be decoded to check its steps' action references: %v", obj.ID, decodeErr),
			})
			continue
		}
		if showFilter != "" && payload.Show != showFilter {
			continue
		}
		for _, step := range payload.Steps {
			_, err := h.deps.Config.GetConfigObject(ctx, config.ShowActionConfigKind, step.Action)
			switch {
			case errors.Is(err, store.ErrConfigObjectNotFound):
				out = append(out, v1.ActionBinding{
					ActionID: step.Action, Show: payload.Show, State: v1.ActionBindingStateBroken,
					Reason: fmt.Sprintf("show.macro %q step %q names action %q which does not exist", obj.ID, step.ID, step.Action),
				})
			case err != nil:
				out = append(out, v1.ActionBinding{
					ActionID: step.Action, Show: payload.Show, State: v1.ActionBindingStateUnknown,
					Reason: fmt.Sprintf("show.macro %q step %q: this coordinator could not check whether action %q exists: %v", obj.ID, step.ID, step.Action, err),
				})
			}
		}
	}
	return out, nil
}

// checkActionBindingByID is the GET-one path. problem is non-nil only for
// "no such action" (404); a decode failure or unresolved reference is
// never a 4xx, since the object itself is real.
func (h *handlers) checkActionBindingByID(ctx context.Context, id string) (v1.ActionBinding, *v1.Problem, error) {
	rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.ShowActionConfigKind, id)
	if err != nil {
		return v1.ActionBinding{}, nil, err
	}
	if problem != nil {
		return v1.ActionBinding{}, problem, nil
	}
	payload, decodeErr := decodeShowActionPayloadForRead(rev.PayloadJSON)
	if decodeErr != nil {
		return v1.ActionBinding{
			ActionID: id, State: v1.ActionBindingStateUnknown,
			Reason: fmt.Sprintf("this coordinator could not decode the stored payload: %v", decodeErr),
		}, nil, nil
	}
	return h.checkActionBindingTarget(ctx, id, payload, h.memoizedFPPEndpoints(ctx)), nil, nil
}

// memoizedFPPEndpoints fetches the FPP endpoint list at most once per
// call, whether or not any binding actually consults it, and caches a
// fetch failure alongside a successful result so callers see it too.
func (h *handlers) memoizedFPPEndpoints(ctx context.Context) func() ([]config.FPPEndpoint, error) {
	var (
		fetched   bool
		endpoints []config.FPPEndpoint
		err       error
	)
	return func() ([]config.FPPEndpoint, error) {
		if !fetched {
			endpoints, err = currentFPPEndpoints(ctx, h.deps.FPP)
			fetched = true
		}
		return endpoints, err
	}
}

// checkActionBindingTarget re-resolves payload.Target against current
// integration state. fetchFPPEndpoints is only invoked for an FPP-target
// binding, so a failure loading FPP inventory never affects a binding
// that does not consult it.
func (h *handlers) checkActionBindingTarget(ctx context.Context, id string, payload config.ShowActionPayload, fetchFPPEndpoints func() ([]config.FPPEndpoint, error)) v1.ActionBinding {
	binding := v1.ActionBinding{ActionID: id, Label: payload.Label, Show: payload.Show}

	switch payload.Target.Integration {
	case config.ShowActionIntegrationFPP:
		endpoints, err := fetchFPPEndpoints()
		if err != nil {
			binding.State = v1.ActionBindingStateUnknown
			binding.Reason = fmt.Sprintf("this coordinator could not list fpp endpoints to check the binding: %v", err)
			break
		}
		binding.State, binding.Reason = checkFPPActionBinding(payload.Target, endpoints)
	case config.ShowActionIntegrationMQTT:
		binding.State, binding.Reason = checkMQTTActionBinding(payload.Target, h.deps.IntegrationBrokers)
	case config.ShowActionIntegrationResolume:
		binding.State, binding.Reason = h.checkResolumeActionBinding(payload.Target)
	case config.ShowActionIntegrationAudio:
		binding.State, binding.Reason = h.checkAudioActionBinding(ctx, payload.Target)
	default:
		// Reached whenever payload.Target.Integration is a value this
		// switch does not name: NOT unreachable — see dispatchActionTarget's
		// (actioninvoke.go) identical default branch, corrected the same
		// way, for the full explanation of why write-time validation does
		// not make this switch itself exhaustive.
		binding.State = v1.ActionBindingStateUnknown
		binding.Reason = fmt.Sprintf("this action names an unrecognized integration %q", payload.Target.Integration)
	}
	return binding
}

// checkAudioActionBinding checks that audioNodeId still names a declared
// audio.node config object and that audioAction is still a supported
// operation, mirroring checkFPPActionBinding's identical two-part shape
// one section up (a configured target and a still-registered operation).
// It deliberately does NOT check audioSessionId against a live
// audio.session object: decodeAudioTarget's own doc comment
// (internal/coordinator/config/showaction.go) states why — a session id is
// minted by the caller (the night-session driver or an operator), never
// looked up, so a session that does not exist yet is not a broken binding,
// exactly as an unresolvable one is not checked at write time either.
func (h *handlers) checkAudioActionBinding(ctx context.Context, target config.ShowActionTarget) (state, reason string) {
	for _, nodeID := range target.AudioNodeIDs {
		hasNode, err := nodeHasAudioNodeObject(ctx, h.deps.Config, nodeID)
		if err != nil {
			return v1.ActionBindingStateUnknown, fmt.Sprintf("this coordinator could not check whether audio node %q is still declared: %v", nodeID, err)
		}
		if !hasNode {
			return v1.ActionBindingStateBroken, fmt.Sprintf("audio node %q is not a declared audio.node", nodeID)
		}
	}
	if !config.IsSupportedAudioAction(target.AudioAction) {
		return v1.ActionBindingStateBroken, fmt.Sprintf("audioAction %q is no longer a supported audio operation (supported: %s)",
			target.AudioAction, strings.Join(config.ShowActionAudioActions(), ", "))
	}
	return v1.ActionBindingStateOK, fmt.Sprintf("audio node(s) %v are declared and operation %q is still supported", []string(target.AudioNodeIDs), target.AudioAction)
}

// checkFPPActionBinding is a configuration check only — no network call.
func checkFPPActionBinding(target config.ShowActionTarget, endpoints []config.FPPEndpoint) (state, reason string) {
	found := false
	for _, ep := range endpoints {
		if ep.ID == target.InstanceID {
			found = true
			break
		}
	}
	if !found {
		return v1.ActionBindingStateBroken, fmt.Sprintf("fpp instance %q is not a configured FPP endpoint", target.InstanceID)
	}

	registered := false
	for _, a := range FPPPrimitiveRegistry.WireActions() {
		if a == target.Primitive {
			registered = true
			break
		}
	}
	if !registered {
		return v1.ActionBindingStateBroken, fmt.Sprintf("primitive %q is no longer a supported FPP action", target.Primitive)
	}
	return v1.ActionBindingStateOK, fmt.Sprintf("fpp instance %q and primitive %q both still resolve", target.InstanceID, target.Primitive)
}

// checkMQTTActionBinding checks only that the declared broker is still
// declared — never whether it is reachable right now, which is a
// transport condition, not a binding fault.
func checkMQTTActionBinding(target config.ShowActionTarget, brokers []config.IntegrationBroker) (state, reason string) {
	for _, b := range brokers {
		if b.ID == target.Broker {
			return v1.ActionBindingStateOK, fmt.Sprintf("broker %q is still a declared integration broker", target.Broker)
		}
	}
	return v1.ActionBindingStateBroken, fmt.Sprintf("broker %q is not a declared integration broker", target.Broker)
}

// checkResolumeActionBinding runs target's stored reference through
// [config.ResolveResolumeRef], the same resolver dispatch write-time
// validation uses, rather than a second hand-copied switch. blackout
// resolves nothing and reports "ok" without claiming a resolution
// happened. [config.ErrResolumeCompositionNotUploaded] and
// [config.ErrResolumeActionResolutionUnrecognized] are "unknown", never
// "broken": neither means the reference is bad, only that it cannot be
// checked. Every other resolver error already names the reference or
// every candidate; its Error() text is reported unchanged.
func (h *handlers) checkResolumeActionBinding(target config.ShowActionTarget) (state, reason string) {
	err := config.ResolveResolumeRef(target.Action, target.Ref, h.deps.ResolumeReferences)
	switch {
	case err == nil && target.Action == config.ShowActionResolumeBlackout:
		return v1.ActionBindingStateOK, "this resolume action carries no reference to resolve"
	case err == nil:
		return v1.ActionBindingStateOK, "the resolume reference resolves unambiguously against the currently stored composition"
	case errors.Is(err, config.ErrResolumeCompositionNotUploaded):
		return v1.ActionBindingStateUnknown, "no resolume composition has ever been uploaded to this coordinator; the binding cannot be checked"
	case errors.Is(err, config.ErrResolumeActionResolutionUnrecognized):
		return v1.ActionBindingStateUnknown, err.Error()
	default:
		return v1.ActionBindingStateBroken, err.Error()
	}
}

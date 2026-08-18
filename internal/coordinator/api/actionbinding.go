package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// GET /api/v1/actions/{id}/binding and GET /api/v1/actions/bindings
// re-resolve a stored show.action's target against current integration
// state, through the same resolver/registry/broker-list write-time
// validation already uses. A read: dispatches nothing, requires no
// credential. The result is three-valued: "ok", "broken", or "unknown"
// (the check could not be performed) — never "ok" for a check that did
// not run, never "broken" for one that could not.

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

	endpoints, err := currentFPPEndpoints(r.Context(), h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "list fpp instances for binding check", err)
		return
	}

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
		bindings = append(bindings, h.checkActionBindingTarget(r.Context(), obj.ID, payload, endpoints))
	}

	jsonWrite(w, v1.ActionBindingsResponse{ServerTime: formatTime(now), Bindings: bindings})
}

// checkActionBindingByID reads id's active show.action revision and
// checks its binding — the GET-one path. problem is non-nil only for
// "no such action" (404); a decode failure or an unresolved reference is
// never a 4xx here, since the object itself is real — see this file's own
// top comment.
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
	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		return v1.ActionBinding{}, nil, err
	}
	return h.checkActionBindingTarget(ctx, id, payload, endpoints), nil, nil
}

// checkActionBindingTarget re-resolves payload.Target against current
// integration state. endpoints is the caller's already-fetched FPP
// endpoint list, so a list of N actions costs one FPP lookup, not N.
func (h *handlers) checkActionBindingTarget(ctx context.Context, id string, payload config.ShowActionPayload, endpoints []config.FPPEndpoint) v1.ActionBinding {
	binding := v1.ActionBinding{ActionID: id, Label: payload.Label, Show: payload.Show}

	switch payload.Target.Integration {
	case config.ShowActionIntegrationFPP:
		binding.State, binding.Reason = checkFPPActionBinding(payload.Target, endpoints)
	case config.ShowActionIntegrationMQTT:
		binding.State, binding.Reason = checkMQTTActionBinding(payload.Target, h.deps.IntegrationBrokers)
	case config.ShowActionIntegrationResolume:
		binding.State, binding.Reason = h.checkResolumeActionBinding(payload.Target)
	default:
		// Unreachable given write-time validation of target.integration's
		// closed enum; answered rather than silently reported "ok".
		binding.State = v1.ActionBindingStateUnknown
		binding.Reason = fmt.Sprintf("this action names an unrecognized integration %q", payload.Target.Integration)
	}
	return binding
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

// checkResolumeActionBinding runs target's stored reference through the
// same [config.ResolumeReferenceResolver] write-time validation uses.
// [config.ErrResolumeCompositionNotUploaded] is "unknown", never
// "broken" (ADR-011): this coordinator cannot tell whether the reference
// resolves with nothing to resolve it against. Every other resolver
// error already names the reference or every candidate (ADR-037
// decisions 5/6); its Error() text is reported unchanged.
func (h *handlers) checkResolumeActionBinding(target config.ShowActionTarget) (state, reason string) {
	err := resolveStoredResolumeRef(target.Action, target.Ref, h.deps.ResolumeReferences)
	switch {
	case err == nil:
		return v1.ActionBindingStateOK, "the resolume reference resolves unambiguously against the currently stored composition"
	case errors.Is(err, config.ErrResolumeCompositionNotUploaded):
		return v1.ActionBindingStateUnknown, "no resolume composition has ever been uploaded to this coordinator; the binding cannot be checked"
	default:
		return v1.ActionBindingStateBroken, err.Error()
	}
}

// resolveStoredResolumeRef re-dispatches an already-decoded stored ref
// onto the same four [config.ResolumeReferenceResolver] methods
// config.resolveResolumeRef dispatches at write time.
func resolveStoredResolumeRef(action string, ref map[string]any, resolver config.ResolumeReferenceResolver) error {
	switch action {
	case config.ShowActionResolumeBlackout:
		return nil
	case config.ShowActionResolumeLaunchClip:
		clip, _ := ref["clip"].(string)
		deck, _ := ref["deck"].(string)
		layer, _ := ref["layer"].(string)
		persistent, _ := ref["persistent"].(bool)
		return resolver.ResolveClip(config.ResolumeClipReference{Clip: clip, Deck: deck, Persistent: persistent, Layer: layer})
	case config.ShowActionResolumeClearLayer, config.ShowActionResolumeSetLayerBypass, config.ShowActionResolumeSetLayerMaster:
		layer, _ := ref["layer"].(string)
		return resolver.ResolveLayer(layer)
	case config.ShowActionResolumeLaunchColumn:
		deck, _ := ref["deck"].(string)
		column, _ := ref["column"].(string)
		return resolver.ResolveColumn(deck, column)
	case config.ShowActionResolumeSelectDeck:
		deck, _ := ref["deck"].(string)
		return resolver.ResolveDeck(deck)
	default:
		return fmt.Errorf("no resolution rule for resolume action %q", action)
	}
}

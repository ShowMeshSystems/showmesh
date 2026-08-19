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
// state. A read: dispatches nothing, requires no credential. The result is
// three-valued: "ok", "broken", or "unknown" — never "ok" for a check that
// did not run, never "broken" for one that could not.

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

	jsonWrite(w, v1.ActionBindingsResponse{ServerTime: formatTime(now), Bindings: bindings})
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

package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/oscfade"
)

// resolumeOSCActionDispatcher sends every Resolume action over OSC for an
// instance set to OSC control (ADR-065). It never contacts Arena's web
// server: positions come from the uploaded composition, and nothing is read back.
type resolumeOSCActionDispatcher struct {
	compositions *resolume.CompositionStore
	osc          *oscfade.Fader
	now          func() time.Time
}

var _ api.ResolumeActionDispatcher = (*resolumeOSCActionDispatcher)(nil)

func newResolumeOSCActionDispatcher(compositions *resolume.CompositionStore, osc *oscfade.Fader) *resolumeOSCActionDispatcher {
	return &resolumeOSCActionDispatcher{compositions: compositions, osc: osc, now: time.Now}
}

func (d *resolumeOSCActionDispatcher) Actions() []api.ResolumeActionDescriptor {
	return translateResolumeActionDescriptors(resolume.ActionDescriptors())
}

const resolumeOSCSentReason = "Sent to Resolume over OSC. Resolume does not answer OSC, so ShowMesh cannot check it. Check Arena on its own screen."

func (d *resolumeOSCActionDispatcher) Dispatch(_ context.Context, action string, params map[string]any, _ time.Time) (api.ResolumeActionResult, error) {
	name, ok := resolumeActionNameFromWire(action)
	if !ok {
		return api.ResolumeActionResult{}, fmt.Errorf("coordinator: resolume osc dispatcher: dispatch called with unrecognized action %q", action)
	}
	if d.osc == nil {
		return oscRefused("No OSC address is set up for this Resolume instance, so nothing was sent. Check the instance URL and OSC port."), nil
	}

	tc, err := d.compositions.Current()
	if err != nil {
		if name == resolume.ActionBlackout {
			return d.send("", oscfade.IntMessage(oscfade.DisconnectAllAddress, 1)), nil
		}
		return oscRefused("No composition has been uploaded, so ShowMesh cannot tell where this is in Arena and sent nothing. Upload the composition."), nil
	}

	if name == resolume.ActionFadeLayer {
		return d.fadeLayer(params, tc), nil
	}
	if name == resolume.ActionBlackout {
		layers := tc.Layers()
		if len(layers) == 0 {
			return d.send("", oscfade.IntMessage(oscfade.DisconnectAllAddress, 1)), nil
		}
		msgs := make([]oscfade.Message, 0, len(layers))
		for _, l := range layers {
			msgs = append(msgs, oscfade.IntMessage(oscfade.LayerClearAddress(l.Index), 1))
		}
		return d.send("", msgs...), nil
	}

	ap, refusal, err := buildResolumeActionParams(name, params, tc)
	if err != nil {
		return api.ResolumeActionResult{}, fmt.Errorf("coordinator: resolume osc dispatcher: building params for %q: %w", action, err)
	}
	if refusal != "" {
		return oscRefused(refusal), nil
	}

	switch name {
	case resolume.ActionLaunchClip:
		return d.launchClip(tc, ap.ClipID), nil
	case resolume.ActionLaunchColumn:
		column, ok := tc.ColumnByID(ap.ColumnID)
		deck, deckOK := deckIndex(tc, column.DeckID)
		if !ok || !deckOK {
			return oscRefused("This column is not in the uploaded composition, so nothing was sent. Upload the composition again."), nil
		}
		return d.send(ap.ColumnID.String(),
			oscfade.IntMessage(oscfade.DeckSelectAddress(deck), 1),
			oscfade.IntMessage(oscfade.ColumnConnectAddress(column.Index), 1)), nil
	case resolume.ActionSelectDeck:
		deck, ok := deckIndex(tc, ap.DeckID)
		if !ok {
			return oscRefused("This deck is not in the uploaded composition, so nothing was sent. Upload the composition again."), nil
		}
		return d.send(ap.DeckID.String(), oscfade.IntMessage(oscfade.DeckSelectAddress(deck), 1)), nil
	}

	layer, ok := tc.LayerByID(ap.LayerID)
	if !ok {
		return oscRefused("This layer is not in the uploaded composition, so nothing was sent. Upload the composition again."), nil
	}
	switch name {
	case resolume.ActionClearLayer:
		return d.send(ap.LayerID.String(), oscfade.IntMessage(oscfade.LayerClearAddress(layer.Index), 1)), nil
	case resolume.ActionSetLayerBypass:
		var v int32
		if ap.Bypassed {
			v = 1
		}
		return d.send(ap.LayerID.String(), oscfade.IntMessage(oscfade.LayerBypassedAddress(layer.Index), v)), nil
	case resolume.ActionSetLayerMaster:
		if ap.Master < 0 || ap.Master > 1 {
			return oscRefused(fmt.Sprintf("The level %g is outside 0 to 1, so nothing was sent. Enter a level from 0 to 1.", ap.Master)), nil
		}
		dispatchedAt := d.now()
		if err := d.osc.SetLevel(layer.Index, ap.Master); err != nil {
			return d.sendFailed(ap.LayerID.String()), nil
		}
		return d.sent(dispatchedAt, ap.LayerID.String(), resolumeOSCSentReason), nil
	}
	return api.ResolumeActionResult{}, fmt.Errorf("coordinator: resolume osc dispatcher: no OSC rule for action %q", name)
}

// launchClip selects the clip's own deck first, because Arena's clip address
// counts columns within whichever deck is selected.
func (d *resolumeOSCActionDispatcher) launchClip(tc *resolume.TrackedComposition, id resolume.ObjectID) api.ResolumeActionResult {
	clip, ok := tc.ClipByID(id)
	if !ok {
		if _, persistent := tc.PersistentClipByID(id); persistent {
			return oscRefused("This is a persistent clip, which ShowMesh cannot launch over OSC, so nothing was sent. Launch it in Arena.")
		}
		return oscRefused("This clip is not in the uploaded composition, so nothing was sent. Upload the composition again.")
	}
	deck, ok := deckIndex(tc, clip.DeckID)
	if !ok {
		return oscRefused("This clip's deck is not in the uploaded composition, so nothing was sent. Upload the composition again.")
	}
	return d.send(id.String(),
		oscfade.IntMessage(oscfade.DeckSelectAddress(deck), 1),
		oscfade.IntMessage(oscfade.ClipConnectAddress(clip.LayerIndex, clip.ColumnIndex), 1))
}

// fadeLayer ramps from the last level ShowMesh sent this layer, else from
// fromPercent, else from full, since Arena's live level is never read.
func (d *resolumeOSCActionDispatcher) fadeLayer(params map[string]any, tc *resolume.TrackedComposition) api.ResolumeActionResult {
	layerName, _ := params["layer"].(string)
	id, rerr := resolume.ResolveLayer(tc, layerName)
	if rerr != nil {
		return oscRefused(rerr.Error())
	}
	layer, ok := tc.LayerByID(id)
	if !ok {
		return oscRefused(fmt.Sprintf("Layer %q is not in the uploaded composition, so the fade was not sent. Upload the composition again.", layerName))
	}
	target, _ := params["targetPercent"].(float64)
	if target < 0 || target > 100 {
		return oscRefused(fmt.Sprintf("The fade target %g is outside 0 to 100, so the fade was not sent. Enter a target from 0 to 100.", target))
	}
	durationMs, _ := params["durationMs"].(float64)
	if durationMs < 0 {
		return oscRefused("The fade time is negative, so the fade was not sent. Enter a fade time of zero or more.")
	}
	start := 1.0
	if last, ok := d.osc.LastSent(layer.Index); ok {
		start = last
	} else if from, ok := params["fromPercent"].(float64); ok {
		if from < 0 || from > 100 {
			return oscRefused(fmt.Sprintf("The fade start %g is outside 0 to 100, so the fade was not sent. Enter a start from 0 to 100.", from))
		}
		start = from / 100
	}
	duration := time.Duration(durationMs) * time.Millisecond
	dispatchedAt := d.now()
	if err := d.osc.Fade(layer.Index, start, target/100, duration, nil); err != nil {
		return d.sendFailed(id.String())
	}
	return d.sent(dispatchedAt, id.String(),
		fmt.Sprintf("The fade to %g%% over %s was sent. Resolume does not answer OSC, so it cannot be confirmed.", target, duration))
}

func (d *resolumeOSCActionDispatcher) send(resolvedID string, msgs ...oscfade.Message) api.ResolumeActionResult {
	dispatchedAt := d.now()
	if err := d.osc.Send(msgs...); err != nil {
		return d.sendFailed(resolvedID)
	}
	return d.sent(dispatchedAt, resolvedID, resolumeOSCSentReason)
}

func (d *resolumeOSCActionDispatcher) sent(dispatchedAt time.Time, resolvedID, reason string) api.ResolumeActionResult {
	resolvedAt := d.now()
	return api.ResolumeActionResult{
		Outcome: api.ResolumeOutcomeUnconfirmable, Dispatched: true, DispatchedAt: &dispatchedAt,
		ResolvedAt: &resolvedAt, ResolvedID: resolvedID, Reason: reason,
	}
}

func (d *resolumeOSCActionDispatcher) sendFailed(resolvedID string) api.ResolumeActionResult {
	resolvedAt := d.now()
	return api.ResolumeActionResult{
		Outcome: api.ResolumeOutcomeFailed, ResolvedAt: &resolvedAt, ResolvedID: resolvedID,
		Reason: "The command could not be sent to Resolume over OSC. Check the OSC port and that Arena's OSC input is on.",
	}
}

func oscRefused(reason string) api.ResolumeActionResult {
	return api.ResolumeActionResult{Outcome: api.ResolumeOutcomeRefused, Reason: reason}
}

// deckIndex is the deck's 0-based position in the composition file, which
// is the order Arena numbers decks in.
func deckIndex(tc *resolume.TrackedComposition, id resolume.ObjectID) (int, bool) {
	for i, d := range tc.Decks() {
		if d.ID == id {
			return i, true
		}
	}
	return 0, false
}

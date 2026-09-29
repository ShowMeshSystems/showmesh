package api

import (
	"context"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// audioRoutingEvidence reads nodeID's reported outputs from its current
// hello. A malformed "outputs" attribute is reported as such, never
// partially trusted.
func audioRoutingEvidence(ctx context.Context, nodes NodeLister, now time.Time, nodeID string) (config.AudioRoutingEvidence, error) {
	caps, err := audioNodeCapabilitySet(ctx, nodes, now, nodeID)
	if err != nil {
		return config.AudioRoutingEvidence{}, err
	}
	local, ok := caps.Capabilities.Lookup(audioOutputLocalCapabilityID)
	if caps.NeverPublished || !ok {
		return config.AudioRoutingEvidence{}, nil
	}
	ev := config.AudioRoutingEvidence{Advertised: true, Live: caps.Live}
	raw, reported := local.Attributes["outputs"]
	if !reported {
		return ev, nil
	}
	ev.Reported = true
	outputs, okOutputs := parseAudioOutputsAttribute(raw)
	complete, okComplete := local.Attributes["discoveryComplete"].(bool)
	if !okOutputs || !okComplete {
		ev.Malformed = true
		return ev, nil
	}
	ev.Outputs, ev.Complete = outputs, complete
	ev.IncompleteReason, _ = local.Attributes["discoveryIncompleteReason"].(string)
	return ev, nil
}

func parseAudioOutputsAttribute(raw any) ([]config.AudioOutputEvidence, bool) {
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]config.AudioOutputEvidence, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		var o config.AudioOutputEvidence
		var okRoute, okIface, okSource, okBasis, okLTC bool
		o.Route, okRoute = m["route"].(string)
		o.Interface, okIface = m["interface"].(string)
		o.Source, okSource = m["source"].(string)
		o.ChannelBasis, okBasis = m["channelBasis"].(string)
		o.LTCCapable, okLTC = m["ltcCapable"].(bool)
		channels, okChannels := m["channels"].(float64)
		if !okRoute || !okIface || !okSource || !okBasis || !okLTC || !okChannels ||
			o.Route == "" || channels < 1 || channels != float64(int(channels)) ||
			(o.ChannelBasis != config.AudioChannelBasisInventory && o.ChannelBasis != config.AudioChannelBasisAtLeast) {
			return nil, false
		}
		o.Channels = int(channels)
		out = append(out, o)
	}
	return out, true
}

// handleGetAudioRoutingChoices serves GET
// /api/v1/nodes/{nodeId}/audio/routing-choices. It reads live and writes
// nothing: a stored audio.node is reported against the choices, never
// changed by them.
func (h *handlers) handleGetAudioRoutingChoices(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("nodeId")
	if verr := config.ValidateAudioNodeObjectID(id); verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}
	ev, err := audioRoutingEvidence(r.Context(), h.deps.Nodes, now, id)
	if err != nil {
		h.writeInternalError(w, now, "get audio routing evidence", err)
		return
	}
	current, err := h.storedAudioNodePayload(r.Context(), id)
	if err != nil {
		h.writeInternalError(w, now, "get stored audio.node", err)
		return
	}
	roles, err := h.audioNodeRoles(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "get audio.node roles", err)
		return
	}
	jsonWrite(w, mapAudioRoutingChoices(now, id, config.ResolveAudioRoutingChoices(id, ev, current, roles)))
}

// storedAudioNodePayload is id's active audio.node payload, or nil when
// the node has none.
func (h *handlers) storedAudioNodePayload(ctx context.Context, id string) (*config.AudioNodePayload, error) {
	rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.AudioNodeConfigKind, id)
	if err != nil || problem != nil {
		return nil, err
	}
	var p config.AudioNodePayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func mapAudioRoutingChoices(now time.Time, id string, c config.AudioRoutingChoices) v1.AudioRoutingChoicesResponse {
	resp := v1.AudioRoutingChoicesResponse{
		ServerTime: formatTime(now), NodeID: id, Discovery: c.Discovery, Reason: c.Reason,
		ManualEntry: v1.AudioRoutingManualEntry{Allowed: c.ManualEntry.Allowed, Reason: c.ManualEntry.Reason},
		LTC:         v1.AudioRoutingLTC{Available: c.LTC.Available, Reason: c.LTC.Reason},
		Routes:      make([]v1.AudioRoutingRouteChoice, 0, len(c.Routes)),
	}
	for _, r := range c.Routes {
		rc := v1.AudioRoutingRouteChoice{
			Route: r.Route, Interface: r.Interface, Source: r.Source,
			Channels: r.Channels, ChannelBasis: r.ChannelBasis,
			LTCCapable: r.LTCCapable, LTCReason: r.LTCReason,
			ProgramGroups: make([]v1.AudioProgramGroup, 0, len(r.ProgramGroups)),
		}
		for _, g := range r.ProgramGroups {
			pg := v1.AudioProgramGroup{Channels: g.Channels, LTCChannels: g.LTCChannels, Conflicts: make([]v1.AudioChannelConflict, 0, len(g.Conflicts))}
			for _, cf := range g.Conflicts {
				pg.Conflicts = append(pg.Conflicts, v1.AudioChannelConflict{Channel: cf.Channel, Reason: cf.Reason})
			}
			rc.ProgramGroups = append(rc.ProgramGroups, pg)
		}
		resp.Routes = append(resp.Routes, rc)
	}
	if c.Current != nil {
		resp.Current = &v1.AudioRoutingCurrent{
			ProgramRoute: c.Current.ProgramRoute, ProgramChannels: c.Current.ProgramChannels,
			LTCChannel: c.Current.LTCChannel, Offered: c.Current.Offered, Reason: c.Current.Reason,
		}
	}
	if c.Clock != nil {
		resp.Clock = &v1.AudioRoutingClock{LocalClock: c.Clock.LocalClock, Source: c.Clock.Source, Verification: c.Clock.Verification}
	}
	return resp
}

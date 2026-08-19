package agent

import (
	"context"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// audioDiscoverer runs this node's audio discovery sequence, a
// package-level var (matching capabilityDetector's own injection
// convention) so advertise_test.go and audioreport_test.go can prove
// wiring without shelling out to a real gst-launch-1.0/aplay.
var audioDiscoverer = audio.Discover

// audioEnumerator is the real [audio.Enumerator] detectAudioCapabilities
// and runAudioReport both probe against.
var audioEnumerator audio.Enumerator = audio.AlsaEnumerator{}

// minLTCChannels mirrors [audio.MinLTCChannels]: the channel count both
// detectAudioCapabilities and buildAudioPayload use to decide whether a
// route is LTC-capable. Kept as this package's own name (matching every
// other package-local mirror of a shared threshold in this codebase)
// rather than a re-export, so a caller reading this file never has to
// chase an import to find the number that gates its own output.
const minLTCChannels = audio.MinLTCChannels

// detectAudioCapabilities probes this node's real ALSA/GStreamer state and
// returns exactly the capability set that evidence supports — audio.engine,
// audio.output.local, and audio.output.ltc, each independently and each
// only from a real PLAYING transition. A node with no audio hardware —
// every render node and the development laptop — returns an empty set,
// not a fault.
//
// This node never claims its own clock domain: no software call here
// proves two outputs share a hardware clock, so ClockDomain/
// ClockDomainProvenance are the coordinator's own operator-declared
// audio.node configuration (ADR-039), not anything this agent reports.
func detectAudioCapabilities(ctx context.Context) capability.Set {
	d := audioDiscoverer(ctx, audioEnumerator)
	if !d.EngineUsable {
		return nil
	}

	set := capability.Set{{ID: "audio.engine", Version: 1}}

	usable, ltc := splitUsableRoutes(d.Routes)

	if len(usable) > 0 {
		set = append(set, capability.Capability{
			ID: "audio.output.local", Version: 1,
			Attributes: routeAttributes(usable),
		})
	}
	if len(ltc) > 0 {
		set = append(set, capability.Capability{
			ID: "audio.output.ltc", Version: 1,
			Attributes: ltcRouteAttributes(ltc),
		})
	}

	return set
}

// splitUsableRoutes partitions d.Routes into routes that achieved at least
// one channel (program-capable) and, of those, routes whose SEPARATE
// [audio.MinLTCChannels]-constrained probe also succeeded
// ([audio.RouteEvidence.LTCChannels], LTC-capable) — shared by capability
// advertisement and the wire report so the two can never disagree about
// what counts as usable.
func splitUsableRoutes(routes []audio.RouteEvidence) (usable, ltc []audio.RouteEvidence) {
	for _, r := range routes {
		if !r.Available || r.Channels < 1 {
			continue
		}
		usable = append(usable, r)
		if r.LTCChannels >= minLTCChannels {
			ltc = append(ltc, r)
		}
	}
	return usable, ltc
}

func routeAttributes(routes []audio.RouteEvidence) map[string]any {
	names := make([]string, 0, len(routes))
	for _, r := range routes {
		names = append(names, r.Device)
	}
	return map[string]any{
		"outputCount": len(routes),
		"routes":      names,
	}
}

// ltcRouteAttributes is [routeAttributes] plus the one fact an achieved
// channel count cannot itself prove: that the extra channel is a
// physically discrete output rather than a mirror of the program pair.
// That check is C0b (commissioning), never discovery.
func ltcRouteAttributes(routes []audio.RouteEvidence) map[string]any {
	attrs := routeAttributes(routes)
	attrs["physicalDiscretenessVerified"] = false
	return attrs
}

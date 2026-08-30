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

// audioEngineAvailable reports whether this node's actual playback
// engine (the real backend behind internal/agent/audio.Manager, bound to
// a delivered audio.node configuration) can currently play something —
// see [detectAudioCapabilities]'s gating of "audio.engine" on it. A
// package-level var, matching audioDiscoverer's own injection
// convention: the default reports unavailable, matching every Engine
// this repository ships before agent.go overwrites it with the real
// engine's own Available method.
var audioEngineAvailable = func() (bool, string) { return false, "no playback engine is wired on this node" }

// minLTCChannels mirrors [audio.MinLTCChannels]: the channel count both
// detectAudioCapabilities and buildAudioPayload use to decide whether a
// route is LTC-capable. Kept as this package's own name (matching every
// other package-local mirror of a shared threshold in this codebase)
// rather than a re-export, so a caller reading this file never has to
// chase an import to find the number that gates its own output.
const minLTCChannels = audio.MinLTCChannels

// audioSessionCapabilityIDs are the reserved capability.IDs that
// this node's session Manager (internal/agent/audio) provides IDENTICALLY
// for every Engine implementation this repository binds, once that
// engine reports itself available, see [detectAudioCapabilities]'s own
// gating of these (and "audio.engine") on [audioEngineAvailable].
// Playlist advance (background/announcement source roles, item repeat),
// gain/fade application, duck/interrupt priority resolution, and
// position readback are all Manager-level behavior implemented once
// against the [audio.Engine] interface (internal/agent/audio/mix.go,
// session.go, engine.go), never varying by which engine backend is
// actually bound underneath it, so there is nothing further to probe
// per-ID: an available engine provides all of them.
//
// "audio.mix.concurrent" (more than one session audible on this output
// at once) is real for the one production engine this repository binds
// (internal/agent/audio/gstengine, cgo-built): its own doc comment states
// concurrent sessions are branches mixed by a single audiomixer onto one
// physical sink. It is included here rather than probed separately
// because no other Engine implementation in this repository is ever
// wired as "the real engine" behind [audioEngineAvailable] (see
// TestFakeAudioEngineNeverAdvertisesPlaybackCapability).
//
// "audio.transition.gapless" and "audio.transition.crossfade" are
// deliberately NOT in this list, and ship nowhere in this build:
// [audio.Session.advanceLocked] always stops the completed item and
// starts its successor in sequence, only MEASURING the resulting gap
// (docs/build/IDENTIFIER-REGISTER.md's inter-item gap), never eliminating
// it, true for every engine this repository ships, gstengine included -
// proven directly by
// internal/agent/audio's own
// TestAdvanceReleasesThePredecessorBeforeTheSuccessorEverLoadsRegardlessOfRequestedTransition,
// which asserts the actual engine call order for both requested
// transitions, not merely their absence from this list. There is no
// evidence any node built from this repository could ever honestly
// confirm either ability, so only "audio.transition.sequential"
// ships until an engine actually implements one of the other two.
var audioSessionCapabilityIDs = []capability.ID{
	"audio.playback.background",
	"audio.playback.announcement",
	"audio.playback.playlist",
	"audio.playback.loop",
	"audio.playback.gain",
	"audio.playback.fade",
	"audio.playback.seek",
	"audio.playback.position",
	"audio.mix.concurrent",
	"audio.mix.duck",
	"audio.mix.interrupt",
	"audio.transition.sequential",
}

// detectAudioCapabilities probes this node's real ALSA/GStreamer state and
// returns exactly the capability set that evidence supports —
// audio.output.local and audio.output.ltc from route probe evidence
// (unconditional on the playback engine: the coordinator's own
// audio.node placement validation reads these BEFORE a binding can ever
// be delivered, so gating them on [audioEngineAvailable] would make a
// node's own output routes unreachable to configure), plus audio.engine
// and every ID in [audioSessionCapabilityIDs] ONLY when
// [audioEngineAvailable] also reports true, the actual session engine,
// never merely "gst-launch-1.0 works on this box". A node with no audio
// hardware (every render node and the development laptop) returns an
// empty set, not a fault.
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

	set := capability.Set{}
	if ok, _ := audioEngineAvailable(); ok {
		set = append(set, capability.Capability{ID: "audio.engine", Version: 1})
		for _, id := range audioSessionCapabilityIDs {
			set = append(set, capability.Capability{ID: id, Version: 1})
		}
	}

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

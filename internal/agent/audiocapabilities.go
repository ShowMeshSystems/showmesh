package agent

import (
	"context"
	"sync"

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

// audioEngineHeldNode reports the audioNodeConfig the currently bound
// real engine was built from, and true only when that engine genuinely
// reports itself available right now, wired to the same
// audioEngineRebuilder instance audioEngineAvailable is (agent.go). See
// [detectAudioCapabilities]'s own use of this: a post-bind capability
// detection must never re-probe a route the engine it just bound
// actively holds open (real ALSA hardware reports EBUSY for a second,
// concurrent open of the same device), so it substitutes this
// already-genuine evidence (the accepted binding an actively playing
// engine was built from) for that route instead of a fresh probe. The
// default always reports (audioNodeConfig{}, false), matching
// audioEngineAvailable's own "no engine wired yet" default, so a node
// with no audioEngineRebuilder ever wired (impossible in production,
// agent.go always wires both together, but still the honest zero value)
// never claims to hold anything.
var audioEngineHeldNode = func() (audioNodeConfig, bool) { return audioNodeConfig{}, false }

// routeEvidenceCache remembers each device's last genuinely-successful
// probe: a held device probes EBUSY (zero on every field), so only an
// earlier pass has real evidence to substitute.
type routeEvidenceCache struct {
	mu     sync.Mutex
	routes map[string]audio.RouteEvidence
}

// update stores routes' successful entries. When complete is true
// (routes is the full, untruncated candidate list from a successful
// enumeration), any cached device missing from routes entirely is
// evicted: gone from ALSA, not merely busy (busy still appears in routes).
func (c *routeEvidenceCache) update(routes []audio.RouteEvidence, complete bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := make(map[string]bool, len(routes))
	for _, r := range routes {
		seen[r.Device] = true
		if r.Available {
			c.routes[r.Device] = r
		}
	}
	if !complete {
		return
	}
	for device := range c.routes {
		if !seen[device] {
			delete(c.routes, device)
		}
	}
}

func (c *routeEvidenceCache) get(device string) (audio.RouteEvidence, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.routes[device]
	return r, ok
}

// reset returns the cache to empty. Test-only, matching
// capabilityGate/detectedCapabilityCache's own convention: this is
// process-lifetime state shared across every test in this package.
func (c *routeEvidenceCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.routes = map[string]audio.RouteEvidence{}
}

// lastKnownGoodRoutes is this node's single route-evidence cache,
// process-lifetime state shared across every detectAudioCapabilities
// call, matching detectedCapabilityCache's own package-level convention.
var lastKnownGoodRoutes = &routeEvidenceCache{routes: map[string]audio.RouteEvidence{}}

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
// gain/fade application, ceiling enforcement, duck/interrupt priority
// resolution, and position readback are all Manager-level behavior
// implemented once against the [audio.Engine] interface
// (internal/agent/audio/mix.go, session.go, engine.go), never varying by
// which engine backend is actually bound underneath it, so there is
// nothing further to probe per-ID: an available engine provides all of
// them.
//
// "audio.mix.concurrent" (more than one session audible on this output
// at once) is DIFFERENT from the other twelve, and this is worth
// stating plainly rather than overclaiming a guarantee that does not
// exist: it is real for internal/agent/audio/gstengine, the one
// production Engine agent.go's own real wiring ever binds newGstEngine
// to (its own doc comment states concurrent sessions are branches mixed
// by a single audiomixer onto one physical sink), but NOTHING IN THIS
// CODEBASE ENFORCES THAT. audioEngineAvailable and this whole
// audioSessionCapabilityIDs list are wired against whatever the
// [audio.Engine] interface's own Available() reports, with no type-level
// or runtime check tying "available" specifically to gstengine, and
// TestFakeAudioEngineNeverAdvertisesPlaybackCapability does NOT rule
// this out either: it withholds the whole list only because
// audio.FakeEngine.Available() is hardcoded false, so its assertion
// would pass identically whether "audio.mix.concurrent" were removed,
// renamed, or replaced by any other string. This PR's own
// TestInstallAudioCapabilityRepublishRepublishesOnRebuild wires an
// AVAILABLE non-gstengine engine double and gets "audio.mix.concurrent"
// advertised for it, proving the point directly. This is a build-time
// assumption (agent.go's own newGstEngine wiring, readable but not
// asserted anywhere) that would need re-examining the day a second real
// Engine implementation is ever wired in.
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
	"audio.playback.ceiling",
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
	lastKnownGoodRoutes.update(d.Routes, d.HardwareEnumerated && !d.Truncated)
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

	heldNode, heldOK := audioEngineHeldNode()
	usable, ltc := splitUsableRoutes(withHeldRouteTrusted(d.Routes, heldNode, heldOK))

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

// withHeldRouteTrusted returns routes with any entry matching heldNode's
// ProgramRoute/LTCRoute (when heldOK) REPLACED by evidence built from
// heldNode itself, regardless of what this run's own probe against that
// device found (including a busy failure, or no entry at all if the
// route was dropped or never enumerated this pass). detectAudioCapabilities
// runs on every real availability transition (this repository's own
// installAudioCapabilityRepublish, agent.go), including immediately
// after a successful bind, at which point gstengine has already opened
// heldNode.ProgramRoute (and heldNode.LTCRoute, if distinct) and is
// actively playing through it; a second, concurrent probe of that SAME
// device observes ALSA EBUSY on real hardware
// (internal/agent/audio/probe.go's own ProbeResult.Busy), which
// splitUsableRoutes would otherwise read as "this route stopped
// working," dropping audio.output.local/audio.output.ltc for the one
// route that is demonstrably fine, on every single successful bind.
//
// Channels is the higher of probed and declared. LTCChannels keeps
// whatever real value this pass or [lastKnownGoodRoutes] has, regardless
// of the current binding's own LTC role: a hardware fact, not config.
func withHeldRouteTrusted(routes []audio.RouteEvidence, heldNode audioNodeConfig, heldOK bool) []audio.RouteEvidence {
	if !heldOK || (heldNode.ProgramRoute == "" && heldNode.LTCRoute == "") {
		return routes
	}
	declared := audioNodeChannelCount(heldNode)
	trusted := func(r audio.RouteEvidence, device string) audio.RouteEvidence {
		ev := r
		ev.Device = device
		ev.Available = true
		if ev.Channels < declared {
			ev.Channels = declared
		}
		if ev.LTCChannels == 0 {
			if good, ok := lastKnownGoodRoutes.get(device); ok {
				ev.LTCChannels = good.LTCChannels
			}
		}
		return ev
	}

	out := make([]audio.RouteEvidence, 0, len(routes)+1)
	seen := map[string]bool{}
	for _, r := range routes {
		if r.Device == heldNode.ProgramRoute || (heldNode.LTCRoute != "" && r.Device == heldNode.LTCRoute) {
			out = append(out, trusted(r, r.Device))
			seen[r.Device] = true
			continue
		}
		out = append(out, r)
	}
	for _, dev := range []string{heldNode.ProgramRoute, heldNode.LTCRoute} {
		if dev != "" && !seen[dev] {
			out = append(out, trusted(audio.RouteEvidence{}, dev))
			seen[dev] = true
		}
	}
	return out
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

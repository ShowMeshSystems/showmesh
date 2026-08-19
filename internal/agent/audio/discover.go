package audio

import (
	"context"
	"fmt"
)

// maxProbedDevices bounds how many enumerated candidate devices [Discover]
// probes and how many routes enter an advertisement or report, so a node
// with an unusually large device list cannot consume this node's entire
// advertisement budget.
const maxProbedDevices = 4

// MinLTCChannels is the channel count ADR-018 requires to carry a discrete
// LTC output alongside 1-2 program channels. [Discover] probes every
// working candidate a second time, explicitly REQUESTING this many
// channels, and only [RouteEvidence.LTCChannels] records a value when that
// probe both ran and achieved it — an unconstrained probe's own achieved
// channel count is never evidence of LTC capability on its own, because
// GStreamer/ALSA may negotiate fewer channels than a device actually
// supports unless asked.
const MinLTCChannels = 3

// alwaysPresentProbeDevice is the PCM name ALSA reports even with no real
// interface attached (r7_capability_discovery.json). Probing it answers a
// different question than probing a candidate route: not "does this node
// have usable audio hardware" but "does this node's GStreamer/ALSA plugin
// chain itself work at all" — see [Discovery.EngineUsable].
const alwaysPresentProbeDevice = "null"

// RouteEvidence is one candidate device's real probe outcome.
type RouteEvidence struct {
	Device string
	ProbeResult

	// LTCChannels is 0 unless a SEPARATE probe of Device, explicitly
	// requesting at least [MinLTCChannels], both ran and achieved at
	// least that many — never inferred from ProbeResult.Channels alone.
	// A non-zero value is still only a channel-count claim: it is not
	// evidence the extra channel is a physically discrete output, which
	// is a commissioning check (C0b), not a discovery one.
	LTCChannels int
}

// Discovery is this node's complete audio discovery evidence: no engine,
// an engine with no usable route, or an engine with one or more probed
// routes — applied to what gets advertised, not only reported.
type Discovery struct {
	// EngineUsable is real evidence (a PLAYING transition against
	// [alwaysPresentProbeDevice]) that this node's GStreamer alsasink
	// element chain works, independent of whether real hardware exists.
	// False on a host with no gstreamer1.0-alsa install, no gst-launch-1.0
	// at all, or no alsasink element.
	EngineUsable bool
	EngineReason string

	// HardwareEnumerated is true only when this node's own device and
	// hardware-card enumeration BOTH completed without error. False makes
	// HasHardwareCards and Routes mean "we do not know yet", never
	// "confirmed absent" — a shell-out failure (permissions, a missing
	// aplay binary, a transient error) must never be reported the same
	// way as a clean enumeration that genuinely found no card.
	HardwareEnumerated bool

	// HardwareEnumeratedReason is required whenever HardwareEnumerated is
	// false, carrying the actual enumeration error text.
	HardwareEnumeratedReason string

	// HasHardwareCards is [Enumerator.HasHardwareCards]'s own answer, only
	// meaningful when HardwareEnumerated is true.
	HasHardwareCards bool

	// EnumeratedCount is how many PCM device names [Enumerator.Devices]
	// returned, before virtual-name filtering or the maxProbedDevices cap
	// — kept so a truncated report can still state how much was omitted.
	EnumeratedCount int

	// Truncated is true when more real candidate devices were found than
	// [maxProbedDevices] allows probing.
	Truncated bool

	// Routes is every probed real-hardware candidate's outcome. A route
	// reporting Channels>=1 here is a graph-level property only: this
	// package cannot detect an interface that mirrors one physical pair
	// from another downstream of anything ALSA exposes — the physical
	// check is C0b, outstanding on the punch list.
	Routes []RouteEvidence
}

// Discover runs this node's full discovery sequence: probe the always-
// present virtual device for engine evidence, enumerate real candidates,
// then probe up to [maxProbedDevices] of them. It never returns an error;
// an enumeration failure is folded into a Discovery with
// HardwareEnumerated=false and no routes — "we do not know yet", never
// "no hardware".
func Discover(ctx context.Context, enum Enumerator) Discovery {
	engine := ProbeOutput(ctx, alwaysPresentProbeDevice, 0, 0)
	d := Discovery{EngineUsable: engine.Available, EngineReason: engine.Reason}

	devices, err := enum.Devices(ctx)
	if err != nil {
		d.HardwareEnumeratedReason = fmt.Sprintf("device enumeration failed: %v", err)
		return d
	}
	d.EnumeratedCount = len(devices)

	hasCards, err := enum.HasHardwareCards(ctx)
	if err != nil {
		d.HardwareEnumeratedReason = fmt.Sprintf("hardware card enumeration failed: %v", err)
		return d
	}
	d.HasHardwareCards = hasCards
	d.HardwareEnumerated = true

	candidates := CandidateDevices(devices, hasCards)
	if len(candidates) > maxProbedDevices {
		d.Truncated = true
		candidates = candidates[:maxProbedDevices]
	}

	d.Routes = make([]RouteEvidence, 0, len(candidates))
	for _, dev := range candidates {
		route := RouteEvidence{Device: dev, ProbeResult: ProbeOutput(ctx, dev, 0, 0)}
		if route.Available && route.Channels >= 1 {
			if ltc := ProbeOutput(ctx, dev, MinLTCChannels, 0); ltc.Available && ltc.Channels >= MinLTCChannels {
				route.LTCChannels = ltc.Channels
			}
		}
		d.Routes = append(d.Routes, route)
	}

	return d
}

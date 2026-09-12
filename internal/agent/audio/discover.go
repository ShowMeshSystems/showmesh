package audio

import (
	"context"
	"fmt"
)

// maxProbedDevices bounds how many candidate devices [Discover] and
// [DiscoverPipeWire] each probe on their own enumeration source, so an
// unusually large device list on either source alone cannot consume this
// node's whole advertisement budget. It no longer bounds the COMBINED
// route count once [Discovery.WithPipeWireRoutes] merges both sources: a
// node with both a large ALSA list and a large PipeWire graph can
// advertise up to twice this many routes.
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
	//
	// For a route [DiscoverPipeWire] produced (FromGraph true), there is
	// no second constrained probe to run: LTCChannels is simply Channels
	// when Channels >= [MinLTCChannels], because the graph already
	// reports the device's real negotiated channel count.
	LTCChannels int

	// FromGraph is true for a route [DiscoverPipeWire] produced from a
	// PipeWire graph node rather than an ALSA device probe. Channels and
	// Rate are still real evidence in that case (PipeWire's own
	// negotiated values for the node), but callers that word evidence
	// for an operator must not describe it as a probe result; see
	// resolveNodeSampleRate/resolveNodeChannelCount in
	// internal/agent/audionodeops.go.
	FromGraph bool
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

	// Routes is every probed real-hardware candidate's outcome, plus any
	// [DiscoverPipeWire] route a caller has merged in via
	// [Discovery.WithPipeWireRoutes] (FromGraph true). A route reporting
	// Channels>=1 here is a graph-level property only: this package
	// cannot detect an interface that mirrors one physical pair from
	// another downstream of anything ALSA or PipeWire exposes; the
	// physical check is C0b, outstanding on the punch list.
	Routes []RouteEvidence

	// PipeWireEnumerated and PipeWireEnumeratedReason mirror
	// HardwareEnumerated/HardwareEnumeratedReason for this node's
	// PipeWire graph. [Discover] never sets these: it enumerates ALSA
	// only, and a caller sets them via [Discovery.WithPipeWireRoutes]. False
	// with an empty reason means no PipeWire is present on this host at
	// all (a clean absence, not a failure); false with a reason means
	// pw-dump ran but its output could not be parsed.
	PipeWireEnumerated       bool
	PipeWireEnumeratedReason string
}

// WithPipeWireRoutes returns a copy of d with pw's routes appended and its
// enumeration outcome recorded, so a caller can advertise and probe the
// union of this node's ALSA and PipeWire discovery as one Discovery. A
// PipeWire route's Device is the PipeWire node name (e.g.
// "alsa_output.usb-MOTU_M4_..."), distinguishable from an ALSA "hw:..."
// name by shape, so it needs no separate list or discriminator field.
func (d Discovery) WithPipeWireRoutes(pw PipeWireDiscovery) Discovery {
	out := d
	out.PipeWireEnumerated = pw.Enumerated
	out.PipeWireEnumeratedReason = pw.EnumeratedReason
	out.Routes = append(append([]RouteEvidence{}, d.Routes...), pw.Routes...)
	return out
}

// PipeWireDiscovery is [DiscoverPipeWire]'s own result: this node's
// PipeWire graph evidence, kept separate from the ALSA [Discovery] it
// merges into via [Discovery.WithPipeWireRoutes] so the two enumeration
// outcomes can be reported and tested independently.
type PipeWireDiscovery struct {
	// Enumerated is true only when pw-dump ran and its output parsed
	// cleanly (whether or not it named any Audio/Sink node): the same
	// true-only-when-clean contract as [Discovery.HardwareEnumerated].
	Enumerated bool
	// EnumeratedReason is set only when pw-dump genuinely ran but its
	// output could not be parsed. Left empty for the ordinary case of no
	// PipeWire on this host at all: that is ok, not a failure to report.
	EnumeratedReason string
	// Routes is every Audio/Sink node pw-dump reported, each with
	// FromGraph true, Available true when the graph reported at least
	// one channel for it, and LTCChannels set directly from Channels
	// when it meets [MinLTCChannels]: there is no second, constrained
	// probe to run against a graph node the way [Discover] runs one
	// against an ALSA device.
	Routes []RouteEvidence
}

// DiscoverPipeWire runs this node's PipeWire graph discovery: enumerate
// Audio/Sink nodes via enum, up to [maxProbedDevices] of them (the same
// budget [Discover] applies to ALSA candidates, so neither enumeration
// method can alone consume this node's whole advertisement). It never
// returns an error: a pw-dump that failed to run at all (no PipeWire on
// this host) is a clean absence (Enumerated=false, no reason, no routes);
// a pw-dump that ran but could not be parsed is reported as a failure
// (Enumerated=false, reason set).
func DiscoverPipeWire(ctx context.Context, enum PipeWireEnumerator) PipeWireDiscovery {
	nodes, present, err := enum.Nodes(ctx)
	if !present {
		return PipeWireDiscovery{}
	}
	if err != nil {
		return PipeWireDiscovery{EnumeratedReason: fmt.Sprintf("PipeWire graph enumeration failed: %v", err)}
	}
	if len(nodes) > maxProbedDevices {
		nodes = nodes[:maxProbedDevices]
	}
	routes := make([]RouteEvidence, 0, len(nodes))
	for _, n := range nodes {
		ev := RouteEvidence{
			Device:      n.Name,
			ProbeResult: ProbeResult{Available: n.Channels > 0, Channels: n.Channels, Rate: n.Rate},
			FromGraph:   true,
		}
		if !ev.Available {
			ev.Reason = "PipeWire graph reported no audio.channels for this node"
		}
		if n.Channels >= MinLTCChannels {
			ev.LTCChannels = n.Channels
		}
		routes = append(routes, ev)
	}
	return PipeWireDiscovery{Enumerated: true, Routes: routes}
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

package config

import (
	"fmt"
	"slices"
)

// This file resolves one audio node's routing choices from the channel
// evidence its audio.output.local capability reports. It is pure: the API
// layer gathers the evidence, the stored payload and the other nodes'
// roles, and this package decides what is offered and what is refused.

// The discovery states a routing-choices answer can be in. Only
// [AudioRoutingDiscoveryAvailable] ever carries choices.
const (
	AudioRoutingDiscoveryAvailable   = "available"
	AudioRoutingDiscoveryPartial     = "partial"
	AudioRoutingDiscoveryStale       = "stale"
	AudioRoutingDiscoveryFailed      = "failed"
	AudioRoutingDiscoveryNotReported = "not_reported"
	AudioRoutingDiscoveryAbsent      = "absent"
)

// The two channel bases a node reports: a full inventory from its
// PipeWire graph, or a floor an ALSA probe actually achieved.
const (
	AudioChannelBasisInventory = "inventory"
	AudioChannelBasisAtLeast   = "atLeast"
)

// The ways a configured node's local clock is known to carry LTC safely.
const (
	AudioClockVerificationSameInterface     = "same_interface"
	AudioClockVerificationOperatorConfirmed = "operator_confirmed"
	AudioClockVerificationNoLTC             = "no_ltc"
)

// AudioOutputEvidence is one entry of a node's reported "outputs".
type AudioOutputEvidence struct {
	Route        string
	Interface    string
	Source       string
	Channels     int
	ChannelBasis string
	LTCCapable   bool
}

// AudioRoutingEvidence is everything a node currently says about its
// outputs, as the API layer read it from inventory.
type AudioRoutingEvidence struct {
	// Advertised is true when the node's current hello carries
	// audio.output.local at all.
	Advertised bool
	// Live is false when the node cannot be confirmed online, so its
	// retained report may no longer describe the hardware.
	Live bool
	// Reported is true when audio.output.local carries "outputs"; an
	// agent built before that attribute leaves it false.
	Reported bool
	// Malformed is true when "outputs" was present but unreadable.
	Malformed        bool
	Complete         bool
	IncompleteReason string
	Outputs          []AudioOutputEvidence
}

// AudioRoutingChoices is one node's resolved routing choices.
type AudioRoutingChoices struct {
	Discovery string
	Reason    string
	// DiscoveryDetail is the node's own reason a partial discovery was
	// partial: diagnostic text, never operator copy.
	DiscoveryDetail string
	ManualEntry     AudioRoutingManualEntry
	LTC             AudioRoutingLTC
	Routes          []AudioRoutingRouteChoice
	Current         *AudioRoutingCurrent
	Clock           *AudioRoutingClock
}

// AudioRoutingManualEntry says whether hand-typed channels are accepted.
type AudioRoutingManualEntry struct {
	Allowed bool
	Reason  string
}

// AudioRoutingLTC says whether any choice on this node can carry LTC.
type AudioRoutingLTC struct {
	Available bool
	Reason    string
}

// AudioRoutingRouteChoice is one reported output and the program groups
// it offers.
type AudioRoutingRouteChoice struct {
	AudioOutputEvidence
	LTCReason     string
	ProgramGroups []AudioProgramGroup
}

// AudioProgramGroup is one offered set of program channels, the LTC
// channels still free beside it, and why the rest are not offered.
type AudioProgramGroup struct {
	Channels    []int
	LTCChannels []int
	Conflicts   []AudioChannelConflict
}

// AudioChannelConflict names a channel not offered for LTC and why.
type AudioChannelConflict struct {
	Channel int
	Reason  string
}

// AudioRoutingCurrent is the stored placement and whether today's
// choices still offer it. A stored placement is never changed here.
type AudioRoutingCurrent struct {
	ProgramRoute    string
	ProgramChannels []int
	LTCChannel      int
	Offered         bool
	Reason          string
}

// AudioRoutingClock is the configured node's local clock and how LTC is
// known to share it.
type AudioRoutingClock struct {
	LocalClock   string
	Source       string
	Verification string
}

// ResolveAudioRoutingChoices resolves nodeID's choices from ev. current
// is its stored payload, or nil when unconfigured. otherRoles maps every
// other configured audio node to its role, for the one-LTC-node rule.
func ResolveAudioRoutingChoices(nodeID string, ev AudioRoutingEvidence, current *AudioNodePayload, otherRoles map[string]string) AudioRoutingChoices {
	out := AudioRoutingChoices{Discovery: AudioRoutingDiscoveryAvailable, Routes: []AudioRoutingRouteChoice{}}
	out.ManualEntry = AudioRoutingManualEntry{Allowed: true}
	if current != nil {
		out.Clock = resolveAudioRoutingClock(*current)
	}

	switch {
	case !ev.Advertised:
		out.Discovery = AudioRoutingDiscoveryAbsent
		out.Reason = "This node reports no usable audio outputs. Connect the audio interface, then restart ShowMesh on the node."
		out.ManualEntry = AudioRoutingManualEntry{Reason: "This node reports no usable audio outputs, so no channels can be saved for it yet."}
	case !ev.Live:
		out.Discovery = AudioRoutingDiscoveryStale
		out.Reason = "This node is offline, so its outputs may have changed. Bring the node online to choose from its outputs."
	case !ev.Reported:
		out.Discovery = AudioRoutingDiscoveryNotReported
		out.Reason = "This node's ShowMesh version does not list its channels. Enter channels by hand, or update ShowMesh on the node."
	case ev.Malformed:
		out.Discovery = AudioRoutingDiscoveryFailed
		out.Reason = "This node's output list could not be read. Enter channels by hand, or restart ShowMesh on the node."
	case !ev.Complete:
		out.Discovery = AudioRoutingDiscoveryPartial
		out.Reason = "This node could not check all of its audio outputs. Enter channels by hand, or restart ShowMesh on the node to check again."
		out.DiscoveryDetail = ev.IncompleteReason
	}

	ltcHolder := programLTCHolder(nodeID, otherRoles)
	if out.Discovery == AudioRoutingDiscoveryAvailable {
		for _, o := range ev.Outputs {
			out.Routes = append(out.Routes, resolveAudioRouteChoice(o, ltcHolder))
		}
	}
	out.LTC = resolveAudioRoutingLTC(out, ltcHolder)
	if current != nil {
		out.Current = resolveAudioRoutingCurrent(*current, out)
	}
	return out
}

func programLTCHolder(nodeID string, otherRoles map[string]string) string {
	holders := []string{}
	for id, role := range otherRoles {
		if id != nodeID && role == AudioNodeRoleProgramLTC {
			holders = append(holders, id)
		}
	}
	slices.Sort(holders)
	if len(holders) == 0 {
		return ""
	}
	return holders[0]
}

func resolveAudioRoutingLTC(out AudioRoutingChoices, ltcHolder string) AudioRoutingLTC {
	if out.Discovery != AudioRoutingDiscoveryAvailable {
		return AudioRoutingLTC{Reason: out.Reason}
	}
	if ltcHolder != "" {
		return AudioRoutingLTC{Reason: ltcHeldReason(ltcHolder)}
	}
	for _, r := range out.Routes {
		for _, g := range r.ProgramGroups {
			if len(g.LTCChannels) > 0 {
				return AudioRoutingLTC{Available: true}
			}
		}
	}
	return AudioRoutingLTC{Reason: "No output on this node has a spare channel for timecode. Choose program only."}
}

func ltcHeldReason(holder string) string {
	return fmt.Sprintf("Node %s already sends timecode. Choose program only here, or change %s first.", holder, holder)
}

// resolveAudioRouteChoice offers adjacent stereo pairs (or channel 1 on a
// one-channel output) and, beside each, every other proven channel on the
// same output for LTC, since LTC never leaves a different interface.
func resolveAudioRouteChoice(o AudioOutputEvidence, ltcHolder string) AudioRoutingRouteChoice {
	rc := AudioRoutingRouteChoice{AudioOutputEvidence: o, ProgramGroups: []AudioProgramGroup{}}
	ltcOK := o.LTCCapable && ltcHolder == ""
	switch {
	case ltcHolder != "":
		rc.LTCReason = ltcHeldReason(ltcHolder)
	case !o.LTCCapable:
		rc.LTCReason = "This output has no spare channel for timecode. Choose program only, or pick an output with three or more channels."
	}

	var groups [][]int
	if o.Channels == 1 {
		groups = [][]int{{1}}
	}
	for ch := 1; ch+1 <= o.Channels; ch += 2 {
		groups = append(groups, []int{ch, ch + 1})
	}
	for _, g := range groups {
		pg := AudioProgramGroup{Channels: g, LTCChannels: []int{}, Conflicts: []AudioChannelConflict{}}
		for ch := 1; ch <= o.Channels; ch++ {
			if slices.Contains(g, ch) {
				pg.Conflicts = append(pg.Conflicts, AudioChannelConflict{
					Channel: ch, Reason: fmt.Sprintf("Channel %d carries program audio. Timecode needs a channel of its own.", ch),
				})
				continue
			}
			if ltcOK {
				pg.LTCChannels = append(pg.LTCChannels, ch)
			}
		}
		rc.ProgramGroups = append(rc.ProgramGroups, pg)
	}
	return rc
}

func resolveAudioRoutingCurrent(p AudioNodePayload, out AudioRoutingChoices) *AudioRoutingCurrent {
	cur := &AudioRoutingCurrent{ProgramRoute: p.ProgramRoute, ProgramChannels: p.ProgramChannels, LTCChannel: p.LTCChannel}
	if out.Discovery != AudioRoutingDiscoveryAvailable {
		cur.Reason = "The saved channels stay in use. They cannot be checked against this node's outputs right now."
		return cur
	}
	for _, r := range out.Routes {
		if r.Route != p.ProgramRoute {
			continue
		}
		for _, g := range r.ProgramGroups {
			if slices.Equal(g.Channels, p.ProgramChannels) && (p.LTCChannel == 0 || slices.Contains(g.LTCChannels, p.LTCChannel)) {
				cur.Offered = true
				return cur
			}
		}
		cur.Reason = "The saved channels are not among this output's choices and stay in use as saved. Pick a choice, or keep them through manual entry."
		return cur
	}
	cur.Reason = fmt.Sprintf("This node no longer reports %s. The saved channels stay in use; pick an output this node reports.", p.ProgramRoute)
	return cur
}

func resolveAudioRoutingClock(p AudioNodePayload) *AudioRoutingClock {
	local, source := AudioNodeLocalClock(p)
	c := &AudioRoutingClock{LocalClock: local, Source: source}
	switch {
	case p.LTCRoute == "":
		c.Verification = AudioClockVerificationNoLTC
	case source == AudioNodeLocalClockOverride:
		c.Verification = AudioClockVerificationOperatorConfirmed
	default:
		c.Verification = AudioClockVerificationSameInterface
	}
	return c
}

// ValidateAudioNodeChannelInventory refuses a channel beyond an output's
// full reported inventory. A floor ("atLeast") never refuses: channels
// above it are unproven, not absent, and manual entry stays allowed.
func ValidateAudioNodeChannelInventory(p AudioNodePayload, ev AudioRoutingEvidence) *ValidationError {
	if !ev.Live || !ev.Reported || ev.Malformed {
		return nil
	}
	for _, o := range ev.Outputs {
		if o.Route != p.ProgramRoute || o.ChannelBasis != AudioChannelBasisInventory {
			continue
		}
		for i, ch := range p.ProgramChannels {
			if ch > o.Channels {
				return channelBeyondInventory(fmt.Sprintf("programChannels[%d]", i), ch, o)
			}
		}
		if p.LTCChannel > o.Channels {
			return channelBeyondInventory("ltcChannel", p.LTCChannel, o)
		}
	}
	return nil
}

func channelBeyondInventory(field string, ch int, o AudioOutputEvidence) *ValidationError {
	return &ValidationError{
		Code: ValidationCodeFieldInvalid, Field: field,
		Detail: fmt.Sprintf("Channel %d does not exist on %s, which has %d channels. Choose a channel from 1 to %d.",
			ch, o.Route, o.Channels, o.Channels),
	}
}

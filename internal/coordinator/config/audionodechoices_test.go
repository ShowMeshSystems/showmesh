package config

import (
	"slices"
	"strings"
	"testing"
)

func liveEvidence(outputs ...AudioOutputEvidence) AudioRoutingEvidence {
	return AudioRoutingEvidence{Advertised: true, Live: true, Reported: true, Complete: true, Outputs: outputs}
}

var m4Inventory = AudioOutputEvidence{Route: "alsa_output.m4", Interface: "alsa_output.m4", Source: "pipewire", Channels: 4, ChannelBasis: AudioChannelBasisInventory, LTCCapable: true}
var pchStereo = AudioOutputEvidence{Route: "hw:CARD=PCH,DEV=0", Interface: "hw:CARD=PCH", Source: "alsa", Channels: 2, ChannelBasis: AudioChannelBasisAtLeast}

func TestResolveAudioRoutingChoicesOffersPairsAndLTCOffProgramChannels(t *testing.T) {
	c := ResolveAudioRoutingChoices("node-a", liveEvidence(m4Inventory), nil, nil)
	if c.Discovery != AudioRoutingDiscoveryAvailable || !c.LTC.Available || !c.ManualEntry.Allowed {
		t.Fatalf("discovery=%q ltc=%+v manual=%+v, want available, LTC available, manual allowed", c.Discovery, c.LTC, c.ManualEntry)
	}
	groups := c.Routes[0].ProgramGroups
	if len(groups) != 2 || !slices.Equal(groups[0].Channels, []int{1, 2}) || !slices.Equal(groups[1].Channels, []int{3, 4}) {
		t.Fatalf("program groups = %+v, want [1 2] and [3 4]", groups)
	}
	if !slices.Equal(groups[0].LTCChannels, []int{3, 4}) || !slices.Equal(groups[1].LTCChannels, []int{1, 2}) {
		t.Errorf("LTC channels = %v / %v, want every channel outside the program group", groups[0].LTCChannels, groups[1].LTCChannels)
	}
	for _, g := range groups {
		for _, cf := range g.Conflicts {
			if slices.Contains(g.LTCChannels, cf.Channel) || !slices.Contains(g.Channels, cf.Channel) || cf.Reason == "" {
				t.Errorf("conflict %+v in group %v: want each program channel excluded from LTC with a reason", cf, g.Channels)
			}
		}
	}
}

func TestResolveAudioRoutingChoicesProgramOnlyOutput(t *testing.T) {
	c := ResolveAudioRoutingChoices("node-a", liveEvidence(pchStereo), nil, nil)
	r := c.Routes[0]
	if len(r.ProgramGroups) != 1 || len(r.ProgramGroups[0].LTCChannels) != 0 || r.LTCReason == "" {
		t.Errorf("route = %+v, want one program-only group and a reason no LTC is offered", r)
	}
	if c.LTC.Available || c.LTC.Reason == "" {
		t.Errorf("ltc = %+v, want unavailable with a reason", c.LTC)
	}
}

func TestResolveAudioRoutingChoicesOneChannelOutput(t *testing.T) {
	mono := AudioOutputEvidence{Route: "hw:1,0", Interface: "hw:1", Source: "alsa", Channels: 1, ChannelBasis: AudioChannelBasisAtLeast}
	c := ResolveAudioRoutingChoices("node-a", liveEvidence(mono), nil, nil)
	if g := c.Routes[0].ProgramGroups; len(g) != 1 || !slices.Equal(g[0].Channels, []int{1}) {
		t.Errorf("groups = %+v, want [[1]]", g)
	}
}

func TestResolveAudioRoutingChoicesNeverOffersUntrustedDiscovery(t *testing.T) {
	cases := map[string]AudioRoutingEvidence{
		AudioRoutingDiscoveryAbsent:      {},
		AudioRoutingDiscoveryStale:       {Advertised: true, Reported: true, Complete: true, Outputs: []AudioOutputEvidence{m4Inventory}},
		AudioRoutingDiscoveryNotReported: {Advertised: true, Live: true},
		AudioRoutingDiscoveryFailed:      {Advertised: true, Live: true, Reported: true, Malformed: true},
		AudioRoutingDiscoveryPartial:     {Advertised: true, Live: true, Reported: true, IncompleteReason: "device enumeration failed", Outputs: []AudioOutputEvidence{m4Inventory}},
	}
	for want, ev := range cases {
		c := ResolveAudioRoutingChoices("node-a", ev, nil, nil)
		if c.Discovery != want || len(c.Routes) != 0 || c.Reason == "" || c.LTC.Available {
			t.Errorf("%s: discovery=%q routes=%d reason=%q ltc=%v, want no choices and a reason", want, c.Discovery, len(c.Routes), c.Reason, c.LTC.Available)
		}
		if gotManual := c.ManualEntry.Allowed; gotManual != (want != AudioRoutingDiscoveryAbsent) {
			t.Errorf("%s: manual entry allowed = %v", want, gotManual)
		}
	}
}

func TestResolveAudioRoutingChoicesLTCHeldByAnotherNode(t *testing.T) {
	roles := map[string]string{"node-b": AudioNodeRoleProgramLTC, "node-a": AudioNodeRoleProgramLTC}
	c := ResolveAudioRoutingChoices("node-a", liveEvidence(m4Inventory), nil, roles)
	if c.LTC.Available || !strings.Contains(c.LTC.Reason, "node-b") {
		t.Errorf("ltc = %+v, want unavailable naming node-b", c.LTC)
	}
	for _, g := range c.Routes[0].ProgramGroups {
		if len(g.LTCChannels) != 0 {
			t.Errorf("group %v offers LTC %v while node-b holds it", g.Channels, g.LTCChannels)
		}
	}
}

func TestResolveAudioRoutingChoicesReportsCurrentWithoutChangingIt(t *testing.T) {
	offered := &AudioNodePayload{ProgramRoute: "alsa_output.m4", LTCRoute: "alsa_output.m4", ProgramChannels: []int{1, 2}, LTCChannel: 3}
	if cur := ResolveAudioRoutingChoices("node-a", liveEvidence(m4Inventory), offered, nil).Current; !cur.Offered {
		t.Errorf("current = %+v, want offered", cur)
	}
	manual := &AudioNodePayload{ProgramRoute: "alsa_output.m4", ProgramChannels: []int{2, 3}}
	cur := ResolveAudioRoutingChoices("node-a", liveEvidence(m4Inventory), manual, nil).Current
	if cur.Offered || cur.Reason == "" || !slices.Equal(cur.ProgramChannels, []int{2, 3}) {
		t.Errorf("current = %+v, want the manual placement kept, not offered, with a reason", cur)
	}
	old := ResolveAudioRoutingChoices("node-a", AudioRoutingEvidence{Advertised: true, Live: true}, manual, nil).Current
	if old.Offered || old.ProgramRoute != "alsa_output.m4" || old.Reason == "" {
		t.Errorf("current against an old agent = %+v, want kept as saved with a reason", old)
	}
}

func TestResolveAudioRoutingChoicesClockVerification(t *testing.T) {
	cases := []struct {
		p    AudioNodePayload
		want string
	}{
		{AudioNodePayload{ProgramRoute: "hw:CARD=M4,DEV=0", ProgramChannels: []int{1, 2}}, AudioClockVerificationNoLTC},
		{AudioNodePayload{ProgramRoute: "hw:CARD=M4,DEV=0", LTCRoute: "hw:CARD=M4,DEV=0", ProgramChannels: []int{1, 2}, LTCChannel: 3}, AudioClockVerificationSameInterface},
		{AudioNodePayload{ProgramRoute: "hw:CARD=M4,DEV=0", LTCRoute: "hw:CARD=M4,DEV=0", ProgramChannels: []int{1, 2}, LTCChannel: 3, LocalClockOverride: "wordclock"}, AudioClockVerificationOperatorConfirmed},
	}
	for _, tc := range cases {
		clock := ResolveAudioRoutingChoices("node-a", liveEvidence(), &tc.p, nil).Clock
		if clock.Verification != tc.want {
			t.Errorf("payload %+v: verification = %q, want %q", tc.p, clock.Verification, tc.want)
		}
	}
}

func TestValidateAudioNodeChannelInventory(t *testing.T) {
	beyond := AudioNodePayload{ProgramRoute: "alsa_output.m4", ProgramChannels: []int{1, 2}, LTCRoute: "alsa_output.m4", LTCChannel: 5}
	if verr := ValidateAudioNodeChannelInventory(beyond, liveEvidence(m4Inventory)); verr == nil || verr.Field != "ltcChannel" || !strings.HasPrefix(verr.Detail, "Channel 5 does not exist") {
		t.Errorf("channel 5 of 4 = %+v, want refused on ltcChannel", verr)
	}
	floor := AudioOutputEvidence{Route: "hw:CARD=M4,DEV=0", Channels: 3, ChannelBasis: AudioChannelBasisAtLeast}
	manual := AudioNodePayload{ProgramRoute: "hw:CARD=M4,DEV=0", ProgramChannels: []int{3, 4}}
	if verr := ValidateAudioNodeChannelInventory(manual, liveEvidence(floor)); verr != nil {
		t.Errorf("channel 4 above an atLeast floor of 3 refused: %v, want accepted as manual entry", verr)
	}
	stale := liveEvidence(m4Inventory)
	stale.Live = false
	if verr := ValidateAudioNodeChannelInventory(beyond, stale); verr != nil {
		t.Errorf("offline node's inventory refused a write: %v, want accepted", verr)
	}
	if verr := ValidateAudioNodeChannelInventory(beyond, AudioRoutingEvidence{Advertised: true, Live: true}); verr != nil {
		t.Errorf("old agent refused a write: %v, want accepted", verr)
	}
}

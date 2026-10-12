package audio

import (
	"context"
	"testing"
)

func TestShortInterfaceName(t *testing.T) {
	cases := []struct {
		name, nick, description, want string
	}{
		{"maker and connection detail removed", "", "MOTU M4 Pro Audio (USB-1234)", "M4"},
		{"maker and trailing USB removed", "", "Focusrite Scarlett Solo USB", "Scarlett Solo"},
		{"no nick and no maker", "", "Built-in Audio Analog Stereo", "Built-in Audio"},
		{"nick wins over description", "M4", "MOTU M4 Pro Audio (USB-1234)", "M4"},
		{"text after a comma is dropped", "", "Scarlett 18i20, rear outputs", "Scarlett 18i20"},
		{"a description that is only a maker is kept", "", "MOTU", "MOTU"},
		{"a description that is only connection words is kept", "", "USB Audio", "USB Audio"},
		{"nothing reported", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShortInterfaceName(tc.nick, tc.description); got != tc.want {
				t.Errorf("ShortInterfaceName(%q, %q) = %q, want %q", tc.nick, tc.description, got, tc.want)
			}
		})
	}
}

func TestALSAShortNameIsTheCard(t *testing.T) {
	cases := map[string]string{
		"hw:CARD=M4,DEV=0":      "M4",
		"plughw:CARD=PCH,DEV=3": "PCH",
		"hw:0,0":                "",
	}
	for device, want := range cases {
		if got := alsaShortName(device); got != want {
			t.Errorf("alsaShortName(%q) = %q, want %q", device, got, want)
		}
	}
}

func TestDiscoverPipeWireCarriesShortName(t *testing.T) {
	out := `[{"type":"PipeWire:Interface:Node","info":{"props":{"media.class":"Audio/Sink",
		"node.name":"alsa_output.usb-MOTU_M4_M4MA0-00.pro-output-0","node.nick":"M4",
		"node.description":"MOTU M4 Pro Audio (USB-1234)","audio.channels":4,"audio.rate":48000}}},
	{"type":"PipeWire:Interface:Node","info":{"props":{"media.class":"Audio/Sink",
		"node.name":"alsa_output.usb-Focusrite_Scarlett_Solo-00.analog-stereo",
		"node.description":"Focusrite Scarlett Solo USB","audio.channels":2,"audio.rate":48000}}}]`
	nodes, err := parsePwDumpSinkNodes(out)
	if err != nil {
		t.Fatalf("parsePwDumpSinkNodes: %v", err)
	}
	pw := DiscoverPipeWire(t.Context(), fixedPipeWireNodes(nodes))
	if len(pw.Routes) != 2 {
		t.Fatalf("got %d routes, want 2", len(pw.Routes))
	}
	if pw.Routes[0].ShortName != "M4" || pw.Routes[1].ShortName != "Scarlett Solo" {
		t.Errorf("short names = %q, %q; want M4, Scarlett Solo", pw.Routes[0].ShortName, pw.Routes[1].ShortName)
	}
	if pw.Routes[0].Device != "alsa_output.usb-MOTU_M4_M4MA0-00.pro-output-0" {
		t.Errorf("full device string changed: %q", pw.Routes[0].Device)
	}
}

type fixedPipeWireNodes []PipeWireNode

func (f fixedPipeWireNodes) Nodes(context.Context) ([]PipeWireNode, bool, error) {
	return f, true, nil
}

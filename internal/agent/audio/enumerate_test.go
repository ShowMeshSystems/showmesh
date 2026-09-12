package audio

import (
	"context"
	"errors"
	"testing"
)

// aplayLSample is a MEASURED aplay -L shape (r7_capability_discovery.log)
// with one real card's routes appended, the way a real interface would
// present alongside the always-present virtual devices.
const aplayLSample = `null
    Discard all samples (playback) or generate zero samples (capture)
default
    Playback/recording through the PulseAudio sound server
hw:CARD=PCH,DEV=0
    HDA Intel PCH, ALC3234 Analog
    Direct hardware device without any conversions
sysdefault:CARD=PCH
    HDA Intel PCH, ALC3234 Analog
    Default Audio Device
`

func TestParseAplayLExtractsDeviceNamesOnly(t *testing.T) {
	got := parseAplayL(aplayLSample)
	want := []string{"null", "default", "hw:CARD=PCH,DEV=0", "sysdefault:CARD=PCH"}
	if len(got) != len(want) {
		t.Fatalf("parseAplayL: got %d devices %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseAplayL[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseAplayLNoCardsStillListsVirtualDevices(t *testing.T) {
	// MEASURED, r7_capability_discovery.log: a container with no /dev/snd
	// still reports exactly null and default.
	got := parseAplayL("null\n    Discard all samples (playback) or generate zero samples (capture)\ndefault\n")
	if len(got) != 2 || got[0] != "null" || got[1] != "default" {
		t.Errorf("parseAplayL(no-hardware sample) = %v, want [null default]", got)
	}
}

func TestParseAplayLHasCardsFalseOnNoSoundcardsFound(t *testing.T) {
	// MEASURED, r7_capability_discovery.log verbatim message.
	if parseAplayLHasCards("aplay: device_list:279: no soundcards found...\n") {
		t.Error("parseAplayLHasCards(no soundcards found) = true, want false")
	}
}

func TestParseAplayLHasCardsTrueWithRealCard(t *testing.T) {
	sample := "**** List of PLAYBACK Hardware Devices ****\ncard 0: PCH [HDA Intel PCH], device 0: ALC3234 Analog [ALC3234 Analog]\n"
	if !parseAplayLHasCards(sample) {
		t.Error("parseAplayLHasCards(real card listing) = false, want true")
	}
}

// TestHasHardwareCardsReturnsErrorOnGenuineFailure verifies that a
// transport failure running `aplay -l` — permission denied, missing
// binary, timeout, anything whose output does not carry the expected
// "no soundcards found" marker — must be reported as an error, not
// silently folded into "confirmed no hardware". [Discover] already
// treats this error as "we do not know yet" via HardwareEnumerated; this
// proves AlsaEnumerator actually produces it instead of discarding it.
func TestHasHardwareCardsReturnsErrorOnGenuineFailure(t *testing.T) {
	prev := runCommand
	defer func() { runCommand = prev }()
	runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		return "", errors.New("permission denied")
	}

	_, err := AlsaEnumerator{}.HasHardwareCards(context.Background())
	if err == nil {
		t.Fatal("HasHardwareCards() returned no error for a genuine transport failure, want one")
	}
}

// TestHasHardwareCardsNoErrorOnLegitimateNoCards proves the other half:
// the documented, expected "no soundcards found" exit (which aplay -l
// signals with a non-zero exit code even though it is not a failure)
// must still report (false, nil), never an error.
func TestHasHardwareCardsNoErrorOnLegitimateNoCards(t *testing.T) {
	prev := runCommand
	defer func() { runCommand = prev }()
	runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		return "aplay: device_list:279: no soundcards found...\n", errors.New("exit status 1")
	}

	has, err := AlsaEnumerator{}.HasHardwareCards(context.Background())
	if err != nil {
		t.Fatalf("HasHardwareCards() = %v, want no error for the legitimate no-cards exit", err)
	}
	if has {
		t.Fatal("HasHardwareCards() = true, want false")
	}
}

// pwDumpSample is a trimmed real `pw-dump` shape: a 4-channel MOTU M4
// Audio/Sink node alongside a non-audio device node and a monitor source
// (media.class "Audio/Source", never a route this package advertises),
// modeled on pw-dump's actual object/info/props structure.
const pwDumpSample = `[
  {
    "id": 45,
    "type": "PipeWire:Interface:Node",
    "info": {
      "props": {
        "node.name": "alsa_output.usb-MOTU_M4_000000000000-00.analog-surround-4",
        "node.description": "MOTU M4 Analog Surround 4.0",
        "media.class": "Audio/Sink",
        "audio.channels": 4,
        "audio.rate": 48000
      }
    }
  },
  {
    "id": 46,
    "type": "PipeWire:Interface:Node",
    "info": {
      "props": {
        "node.name": "alsa_output.usb-MOTU_M4_000000000000-00.analog-surround-4.monitor",
        "media.class": "Audio/Source",
        "audio.channels": 4,
        "audio.rate": 48000
      }
    }
  },
  {
    "id": 47,
    "type": "PipeWire:Interface:Device",
    "info": {
      "props": {
        "device.name": "alsa_card.usb-MOTU_M4_000000000000-00"
      }
    }
  }
]`

func TestParsePwDumpSinkNodesExtractsOnlyAudioSinkNodes(t *testing.T) {
	nodes, err := parsePwDumpSinkNodes(pwDumpSample)
	if err != nil {
		t.Fatalf("parsePwDumpSinkNodes() error = %v, want nil", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("parsePwDumpSinkNodes() = %d nodes %+v, want exactly 1 (Audio/Source and Device objects excluded)", len(nodes), nodes)
	}
	got := nodes[0]
	want := PipeWireNode{
		Name:        "alsa_output.usb-MOTU_M4_000000000000-00.analog-surround-4",
		Description: "MOTU M4 Analog Surround 4.0",
		Channels:    4,
		Rate:        48000,
	}
	if got != want {
		t.Errorf("parsePwDumpSinkNodes()[0] = %+v, want %+v", got, want)
	}
}

func TestParsePwDumpSinkNodesGarbageIsAnError(t *testing.T) {
	if _, err := parsePwDumpSinkNodes("not json at all"); err == nil {
		t.Error("parsePwDumpSinkNodes(garbage) returned no error, want one: unparseable pw-dump output is a real failure, not a clean absence")
	}
}

// TestPwDumpEnumeratorNoBinaryIsCleanAbsence proves a host with no
// PipeWire at all (pw-dump itself fails to run) reports present=false
// with a nil error, never an error a caller would have to explain.
func TestPwDumpEnumeratorNoBinaryIsCleanAbsence(t *testing.T) {
	prev := runCommand
	defer func() { runCommand = prev }()
	runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		return "", errors.New("exec: \"pw-dump\": executable file not found in $PATH")
	}

	nodes, present, err := PwDumpEnumerator{}.Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes() error = %v, want nil for a clean absence", err)
	}
	if present {
		t.Error("Nodes() present = true, want false: pw-dump did not run")
	}
	if len(nodes) != 0 {
		t.Errorf("Nodes() = %v, want none", nodes)
	}
}

// TestPwDumpEnumeratorGarbageOutputIsAFailure proves pw-dump running but
// returning unparseable output is a genuine failure this node must report,
// not silently read as "no PipeWire".
func TestPwDumpEnumeratorGarbageOutputIsAFailure(t *testing.T) {
	prev := runCommand
	defer func() { runCommand = prev }()
	runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		return "{not valid json", nil
	}

	nodes, present, err := PwDumpEnumerator{}.Nodes(context.Background())
	if err == nil {
		t.Fatal("Nodes() returned no error for unparseable pw-dump output, want one")
	}
	if !present {
		t.Error("Nodes() present = false, want true: pw-dump genuinely ran")
	}
	if len(nodes) != 0 {
		t.Errorf("Nodes() = %v, want none", nodes)
	}
}

// TestPwDumpEnumeratorRealNodes proves the happy path end to end through
// PwDumpEnumerator.Nodes, not just the parser it delegates to.
func TestPwDumpEnumeratorRealNodes(t *testing.T) {
	prev := runCommand
	defer func() { runCommand = prev }()
	runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if name != "pw-dump" {
			t.Errorf("runCommand called with %q, want pw-dump", name)
		}
		return pwDumpSample, nil
	}

	nodes, present, err := PwDumpEnumerator{}.Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes() error = %v, want nil", err)
	}
	if !present {
		t.Error("Nodes() present = false, want true")
	}
	if len(nodes) != 1 || nodes[0].Channels != 4 || nodes[0].Rate != 48000 {
		t.Errorf("Nodes() = %+v, want one 4-channel/48kHz node", nodes)
	}
}

// TestCandidateDevicesExcludesVirtualNames proves null/default are never
// presented as a candidate route even when real hardware is also present,
// and that a second alias of the SAME card ("sysdefault:CARD=PCH") is
// collapsed into its direct "hw:" entry rather than counted separately.
func TestCandidateDevicesExcludesVirtualNames(t *testing.T) {
	got := CandidateDevices([]string{"null", "default", "hw:CARD=PCH,DEV=0", "sysdefault:CARD=PCH"}, true)
	want := []string{"hw:CARD=PCH,DEV=0"}
	if len(got) != len(want) {
		t.Fatalf("CandidateDevices = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("CandidateDevices[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestCandidateDevicesDeduplicatesAliasesPreferringDirect proves many ALSA
// aliases for ONE card never fill every probe slot — a second, genuinely
// different card must still get a slot.
func TestCandidateDevicesDeduplicatesAliasesPreferringDirect(t *testing.T) {
	got := CandidateDevices([]string{
		"sysdefault:CARD=PCH", "front:CARD=PCH,DEV=0", "surround51:CARD=PCH,DEV=0",
		"hdmi:CARD=PCH,DEV=3", "dmix:CARD=PCH,DEV=0", "hw:CARD=PCH,DEV=0", "plughw:CARD=PCH,DEV=0",
		"hw:CARD=USB,DEV=0",
	}, true)
	want := []string{"hw:CARD=PCH,DEV=0", "hw:CARD=USB,DEV=0"}
	if len(got) != len(want) {
		t.Fatalf("CandidateDevices = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("CandidateDevices[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestCandidateDevicesFallsBackToAliasWhenNoDirectName proves a card whose
// only reported name is not hw:/plughw: still gets a candidate — a missing
// direct name must never mean a missing card.
func TestCandidateDevicesFallsBackToAliasWhenNoDirectName(t *testing.T) {
	got := CandidateDevices([]string{"sysdefault:CARD=PCH"}, true)
	if len(got) != 1 || got[0] != "sysdefault:CARD=PCH" {
		t.Errorf("CandidateDevices = %v, want [sysdefault:CARD=PCH]", got)
	}
}

// TestCandidateDevicesEmptyWhenNoHardwareCards proves the decisive case: a
// node with only null/default (no real card) advertises no candidate
// route at all, regardless of what device names happen to be enumerated.
func TestCandidateDevicesEmptyWhenNoHardwareCards(t *testing.T) {
	got := CandidateDevices([]string{"null", "default", "hw:CARD=GHOST,DEV=0"}, false)
	if got != nil {
		t.Errorf("CandidateDevices(hasHardwareCards=false) = %v, want nil", got)
	}
}

func TestCandidateDevicesEmptyDeviceListStaysEmpty(t *testing.T) {
	if got := CandidateDevices(nil, true); got != nil {
		t.Errorf("CandidateDevices(nil, true) = %v, want nil", got)
	}
}

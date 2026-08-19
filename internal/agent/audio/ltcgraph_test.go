package audio

import (
	"strings"
	"testing"
)

func sampleGraph() LTCMixGraph {
	return LTCMixGraph{
		Route:             "hw:CARD=PCH,DEV=0",
		ProgramSourceArgv: []string{"audiotestsrc", "is-live=true"},
		LTCSourceArgv:     []string{"fdsrc", "fd=3"},
		ProgramChannels:   2,
	}
}

// TestLTCNeverSharesAChannelIndexWithProgram is the graph-level test for
// ADR-018: the channel index LTC is assigned is never one of program's own
// indexes, for every program channel count this package would ever be
// asked to build. This is a property of the graph's own structure — see
// [LTCMixGraph.LTCChannelIndex]'s doc comment — never inferred from
// anything about a physical interface, which stays a commissioning check.
func TestLTCNeverSharesAChannelIndexWithProgram(t *testing.T) {
	for _, programChannels := range []int{1, 2, 4} {
		g := sampleGraph()
		g.ProgramChannels = programChannels

		programIdx := g.ProgramChannelIndexes()
		ltcIdx := g.LTCChannelIndex()

		for _, idx := range programIdx {
			if idx == ltcIdx {
				t.Fatalf("programChannels=%d: LTC channel index %d collides with a program channel index", programChannels, ltcIdx)
			}
		}
	}
}

// TestLTCChannelIndexIsChannelThreeForStereoProgram proves the shipped
// ADR-018 case exactly: program on 1-2 (indexes 0,1), LTC on channel 3
// (index 2).
func TestLTCChannelIndexIsChannelThreeForStereoProgram(t *testing.T) {
	g := sampleGraph()
	want := []int{0, 1}
	got := g.ProgramChannelIndexes()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ProgramChannelIndexes() = %v, want %v", got, want)
	}
	if g.LTCChannelIndex() != 2 {
		t.Errorf("LTCChannelIndex() = %d, want 2 (channel 3)", g.LTCChannelIndex())
	}
}

// TestBuildArgvLinksLTCSourceOnlyToItsOwnSinkPad proves the property at
// the argv level too: the built pipeline never links the LTC source
// chain's output to a "mix.sink_0" or "mix.sink_1" token (program's own
// pads), only to "mix.sink_2".
func TestBuildArgvLinksLTCSourceOnlyToItsOwnSinkPad(t *testing.T) {
	g := sampleGraph()
	argv, err := g.BuildArgv()
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	line := strings.Join(argv, " ")

	ltcSourceIdx := strings.Index(line, "fdsrc fd=3")
	if ltcSourceIdx < 0 {
		t.Fatalf("built argv does not contain the ltc source chain: %s", line)
	}
	tail := line[ltcSourceIdx:]
	if strings.Contains(tail, "mix.sink_0") || strings.Contains(tail, "mix.sink_1") {
		t.Errorf("ltc source chain's own tail links to a program sink pad: %s", tail)
	}
	if !strings.Contains(tail, "mix.sink_2") {
		t.Errorf("ltc source chain never links to mix.sink_2: %s", tail)
	}

	// And the inverse: program's deinterleaved pads only ever link to
	// sink_0/sink_1, never sink_2.
	progIdx := strings.Index(line, "name=prog")
	progTail := line[progIdx:ltcSourceIdx]
	if strings.Contains(progTail, "mix.sink_2") {
		t.Errorf("program's own linking region reaches mix.sink_2 (the LTC pad): %s", progTail)
	}
}

func TestBuildArgvRejectsMissingRoute(t *testing.T) {
	g := sampleGraph()
	g.Route = ""
	if _, err := g.BuildArgv(); err == nil {
		t.Error("BuildArgv with no route: got nil error, want one")
	}
}

func TestBuildArgvRejectsMissingProgramSource(t *testing.T) {
	g := sampleGraph()
	g.ProgramSourceArgv = nil
	if _, err := g.BuildArgv(); err == nil {
		t.Error("BuildArgv with no program source: got nil error, want one")
	}
}

func TestBuildArgvRejectsMissingLTCSource(t *testing.T) {
	g := sampleGraph()
	g.LTCSourceArgv = nil
	if _, err := g.BuildArgv(); err == nil {
		t.Error("BuildArgv with no ltc source: got nil error, want one")
	}
}

func TestBuildArgvRejectsZeroProgramChannels(t *testing.T) {
	g := sampleGraph()
	g.ProgramChannels = 0
	if _, err := g.BuildArgv(); err == nil {
		t.Error("BuildArgv with 0 program channels: got nil error, want one")
	}
}

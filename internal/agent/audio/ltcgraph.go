package audio

import "fmt"

// This file builds the graph shape ADR-018/AUDIO-ENGINE §6 require:
// program on channels 1-2 and LTC on a DISCRETE channel 3 of one
// interleaved stream on the node's declared ltcRoute (config.AudioNodePayload,
// ADR-039) — never mixed into program. It is a structural description only:
// [LTCMixGraph.BuildArgv] renders gst-launch-1.0 syntax for inspection and
// tests, but nothing in this package or its callers executes gst-launch-1.0
// against it (the standing constraint — Linear SM-68 — still holds: there
// is no pipeline backend, so this graph never reaches a real interface).
// [LTCMixGraph.ProgramChannelIndexes]/[LTCMixGraph.LTCChannelIndex] are
// this file's own graph-level test surface: they answer "which channel
// index carries which bus" from the SAME structure BuildArgv renders, so a
// test asserting no overlap is asserting about the actual built graph, not
// a hand-maintained parallel claim about it.
//
// A graph-level property proves the CHANNEL ASSIGNMENT never crosses; it
// proves nothing about a physical interface actually keeping those
// channels electrically discrete — that stays a commissioning check.

// LTCMixGraph describes one node's program+LTC interleave: program's own
// (already-interleaved, e.g. stereo) source deinterleaved onto the first
// ProgramChannels indexes, LTC's mono source placed at the next index.
// ProgramChannels is a field, not a hardcoded 2, because AUDIO-ENGINE §6
// stops short of promising every program bus is exactly stereo; ADR-018
// itself only requires "1-2 program, 3 LTC" for the shipped, 2-channel
// case this seam actually builds.
type LTCMixGraph struct {
	// Route is the ALSA device name (config.AudioNodePayload.LTCRoute)
	// this interleaved stream is sent to — ADR-018's "one interface"
	// requirement means this is also where program is expected to land;
	// a caller supplying a ProgramRoute that disagrees with LTCRoute is a
	// configuration error this package does not itself detect (that
	// belongs to config.ValidateAudioNodePlacement, one layer up).
	Route string

	// ProgramSourceArgv is the gst-launch element chain (as already-built
	// argv tokens, "!"-free) producing this node's interleaved program
	// audio, ending in a pad producer named "prog". Left to the caller
	// because this package does not itself define where program audio
	// comes from — that is the audio.Engine/pipeline backend Linear SM-68
	// has not decided yet.
	ProgramSourceArgv []string

	// LTCSourceArgv is the gst-launch element chain producing this node's
	// generated LTC as a single mono channel, ending in a pad producer
	// named "ltc" — normally "fdsrc fd=<n> ! ..." reading the supervised
	// generator's stdout, never a filesrc reading a pre-rendered file.
	LTCSourceArgv []string

	// ProgramChannels is how many channels ProgramSourceArgv's output
	// carries — 2 for the shipped stereo case.
	ProgramChannels int
}

// ProgramChannelIndexes returns the 0-based interleave channel indexes
// program occupies: {0, ..., ProgramChannels-1}.
func (g LTCMixGraph) ProgramChannelIndexes() []int {
	out := make([]int, g.ProgramChannels)
	for i := range out {
		out[i] = i
	}
	return out
}

// LTCChannelIndex returns the 0-based interleave channel index LTC
// occupies: always the first index past program's own channels, never
// one of program's own indexes — this IS the "never enters the program
// bus" property, expressed as a single index assignment rather than
// asserted separately from it.
func (g LTCMixGraph) LTCChannelIndex() int {
	return g.ProgramChannels
}

// BuildArgv renders g as gst-launch-1.0 argv: program's source chain
// deinterleaved onto interleave's first ProgramChannels sink pads by
// index, LTC's source chain onto the next sink pad, into a capsfilter
// pinning the resulting channel count and an alsasink on Route. Every
// sink pad index comes from [LTCMixGraph.ProgramChannelIndexes]/
// [LTCMixGraph.LTCChannelIndex] — the SAME two methods a test calls — so
// the rendered graph and the queried channel assignment can never
// disagree with each other.
func (g LTCMixGraph) BuildArgv() ([]string, error) {
	if g.Route == "" {
		return nil, fmt.Errorf("audio: ltc mix graph has no output route")
	}
	if g.ProgramChannels < 1 {
		return nil, fmt.Errorf("audio: ltc mix graph programChannels must be at least 1, got %d", g.ProgramChannels)
	}
	if len(g.ProgramSourceArgv) == 0 {
		return nil, fmt.Errorf("audio: ltc mix graph has no program source")
	}
	if len(g.LTCSourceArgv) == 0 {
		return nil, fmt.Errorf("audio: ltc mix graph has no ltc source")
	}

	totalChannels := g.ProgramChannels + 1

	var argv []string

	// interleave, declared once, up front: every source stage below links
	// INTO one of its named sink pads by request-pad name, and the sink
	// stage links FROM its single output pad.
	argv = append(argv, "interleave", "name=mix")

	argv = append(argv, g.ProgramSourceArgv...)
	argv = append(argv, "!", "deinterleave", "name=prog")
	for _, idx := range g.ProgramChannelIndexes() {
		argv = append(argv, fmt.Sprintf("prog.src_%d", idx), "!", fmt.Sprintf("mix.sink_%d", idx))
	}

	argv = append(argv, g.LTCSourceArgv...)
	argv = append(argv, "!", "audioconvert", "!", fmt.Sprintf("mix.sink_%d", g.LTCChannelIndex()))

	argv = append(argv, "mix.",
		"!", "capsfilter", fmt.Sprintf("caps=audio/x-raw,channels=%d", totalChannels),
		"!", "alsasink", "device="+g.Route, "sync=false",
	)

	return argv, nil
}

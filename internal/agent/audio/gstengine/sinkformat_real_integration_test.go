//go:build cgo

package gstengine

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

// restrictiveSinkConfig builds a Config whose sink only accepts one fixed
// caps value, standing in for a class-compliant USB interface's raw hw:
// route: capsfilter is a real GStreamer element (not a test double), and
// fixed caps on it reject anything the upstream chain does not already
// match -- exactly what alsasink does against a device whose own
// negotiated format differs from the F32LE this package's output
// topology is pinned to internally.
func restrictiveSinkConfig(caps string, programChannels []int, channelCount int) Config {
	return Config{
		SinkFactory:     "capsfilter",
		SinkProperties:  map[string]any{"caps": gst.CapsFromString(caps)},
		ProgramChannels: programChannels,
		ChannelCount:    channelCount,
		SampleRate:      48000,
		Resolve:         resolveByRuntimeFilename,
	}
}

// waitForAvailable polls e.Available() until it reports true or timeout
// elapses, returning the last observed pair. A sink-side not-negotiated
// failure can surface asynchronously off watchBus, after
// New/buildPipeline has already returned successfully -- see
// engine_cgo.go's own doc comments on Available -- so a caller cannot
// assume the final state is visible the instant New returns.
func waitForAvailable(e *Engine, timeout time.Duration) (bool, string) {
	deadline := time.Now().Add(timeout)
	for {
		ok, reason := e.Available()
		if ok || time.Now().After(deadline) {
			return ok, reason
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestEngineOpensAnIntegerOnlySink proves a route discovery blessed can
// actually be played: interleave's internal mixing stays fixed at F32LE
// (interleaveSampleFormat), but the output pipeline must still reach a
// sink that only accepts an integer PCM format -- exactly what a
// class-compliant USB interface's raw hw: route negotiates, and exactly
// what [ProbeOutput]'s own audioconvert!audioresample-fronted probe
// pipeline already proved that route accepts.
func TestEngineOpensAnIntegerOnlySink(t *testing.T) {
	gst.Init() // CapsFromString below needs GStreamer initialized before New would otherwise do it
	cfg := restrictiveSinkConfig("audio/x-raw,format=S32LE,rate=48000,channels=1", []int{1}, 1)
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	if ok, reason := waitForAvailable(e, 3*time.Second); !ok {
		t.Fatalf("Available() = false (%s), want true: the sink only accepts S32LE and the pipeline must convert to it, matching what discovery's own probe proved the device accepts", reason)
	}
}

// TestEngineOpensAMultiChannelIntegerSink is
// TestEngineOpensAnIntegerOnlySink's multi-channel, second-format sibling
// -- S24LE across two program channels, no LTC channel -- so the fix is
// not just proven for the single-channel/single-format case.
func TestEngineOpensAMultiChannelIntegerSink(t *testing.T) {
	gst.Init()
	cfg := restrictiveSinkConfig("audio/x-raw,format=S24LE,rate=48000,channels=2", []int{1, 2}, 2)
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	if ok, reason := waitForAvailable(e, 3*time.Second); !ok {
		t.Fatalf("Available() = false (%s), want true: the sink only accepts S24LE and the pipeline must convert to it", reason)
	}
}

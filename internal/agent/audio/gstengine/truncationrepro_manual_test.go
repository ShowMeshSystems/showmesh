//go:build cgo && showmesh_truncation_repro

// Manual, hardware-gated experiment. It confirms or refutes, against real
// rendered content on a real ALSA device, the prediction that
// Engine.Start's seek callback (resyncMixerPads, see branch.go)
// anchors a non-live pipeline's branch so far into GstAudioAggregator's
// past that the aggregator's leading-buffer-discard rule truncates the
// start of playback -- a cue starting mid-content, not merely late --
// while a live pipeline (the production shape, since addMixerKeepAlive
// always sets is-live=true on every channel's keep-alive source) does
// not.
//
// This file constructs its own non-live pipeline entirely at runtime, by
// flipping the "is-live" property this package's own production code
// (engine_cgo.go's addMixerKeepAlive) sets on the per-channel keep-alive
// audiotestsrc AFTER New has already built and started the real
// production pipeline, then asking GStreamer to recompute liveness (see
// forceNonLive below). No committed file's is-live literal is touched --
// production always ships live, exactly as it does today.
//
// The output sink is a real, unmodified alsasink talking to a real ALSA
// route, wrapped in a tee that also feeds queue!wavenc!filesink so the
// actual rendered content can be captured and analyzed offline. The tee
// is substituted for the plain sink newSinkFactoryElement would otherwise
// build, via this package's own test-injection seam (see engine_cgo.go's
// doc comment on newSinkFactoryElement and sinkformat_real_integration_
// test.go's useSinkElement, which this deliberately mirrors) -- the tee
// does not sit downstream of the sink and does not change which element
// the pipeline selects its clock from; see the pipeline.IsLive() and
// pipeline.GetPipelineClock() logging below, and the run instructions
// (bench/audio-node/resync-truncation-repro/README.md) for the
// GST_DEBUG-based confirmation of exactly which element provided that
// clock.
//
// Opt in explicitly -- excluded from every ordinary run because it opens
// a physical device:
//
//	go test -c -tags showmesh_truncation_repro -o truncation_repro.test \
//	    ./internal/agent/audio/gstengine
//
// Then on the target machine (a real card 1, never node-01 -- see the run
// instructions for the exact per-arm invocations and expected output):
//
//	SHOWMESH_TRUNC_DEVICE=plughw:CARD=PCH \
//	SHOWMESH_TRUNC_SWEEP=/path/to/sweep_100_2000_2s_48k_s16le_mono.wav \
//	SHOWMESH_TRUNC_CAPTURE_LIVE=/path/to/live_capture.wav \
//	    ./truncation_repro.test -test.run TestTruncationRepro_LiveArm -test.v
//
//	SHOWMESH_TRUNC_DEVICE=plughw:CARD=PCH \
//	SHOWMESH_TRUNC_SWEEP=/path/to/sweep_100_2000_2s_48k_s16le_mono.wav \
//	SHOWMESH_TRUNC_CAPTURE_NONLIVE=/path/to/nonlive_capture.wav \
//	    ./truncation_repro.test -test.run TestTruncationRepro_NonLiveArm -test.v
package gstengine

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// truncationReproRate is the pipeline's interior sample rate for this
// experiment. Card 1 accepts 44100 or 48000 per its own spec; 48000 is
// picked here and must match whatever rate the sweep WAV was generated
// at (gensweep.go's -rate flag), since the branch's own capsfilter pins
// this rate on the decoded content (see branch.go's build).
const truncationReproRate = 48000

// truncationReproSettle is how long this test waits after Start before
// tearing down: the 2-second sweep plus margin for decode, mix, and
// output-side latency to actually reach the filesink and finalize.
const truncationReproSettle = 5 * time.Second

// buildTruncationCaptureSink parses a tee that duplicates the pipeline's
// real output to both a genuine alsasink on device (exactly as production
// wires it: sync=true, the GstBaseSink default, which is what makes it
// the entity actually pacing playback to the real card's clock) and a
// queue!wavenc!filesink recording branch. Each tee branch gets its own
// queue so neither branch's downstream can stall the other's thread.
// ghost_unlinked_pads=true (the second ParseBinFromDescription argument)
// is what turns tee's own dangling sink pad into this bin's ghost pad, so
// the returned element satisfies newSinkFactoryElement's gst.Element
// contract exactly as a plain alsasink would.
func buildTruncationCaptureSink(device, capturePath string) (gst.Element, error) {
	gst.Init() // ParseBinFromDescription below needs GStreamer initialized before New would otherwise do it
	desc := fmt.Sprintf(
		`tee name=t ! queue ! alsasink device=%q `+
			`t. ! queue ! wavenc ! filesink location=%q sync=false async=false`,
		device, capturePath)
	bin, err := gst.ParseBinFromDescription(desc, true)
	if err != nil {
		return nil, fmt.Errorf("could not build tee/capture sink bin from %q: %w", desc, err)
	}
	return bin, nil
}

// forceNonLive flips is-live to false on this engine's sole channel-1
// keep-alive source (the only is-live=true element the single-program-
// channel, no-LTC topology built by truncationReproConfig contains) and
// asks the pipeline bin to recompute its cached liveness (gst_bin_
// recalculate_latency runs a fresh GST_QUERY_LATENCY, which is what
// actually re-derives whether any upstream source still reports live).
// It is test-local: it runs against an already-built, already-PLAYING
// production pipeline from outside, and touches no committed file's
// is-live literal.
func forceNonLive(t *testing.T, e *Engine) {
	t.Helper()
	bin, ok := e.pipeline.(gst.Bin)
	if !ok {
		t.Fatalf("engine pipeline does not implement gst.Bin")
	}
	keepAlive := bin.GetByName("keepalive-ch1")
	if keepAlive == nil {
		t.Fatalf("could not find keepalive-ch1 -- this test's topology assumption (single program channel, no LTC) no longer matches buildPipeline's naming; stop rather than guess at a different name")
	}
	keepAlive.SetObjectProperty("is-live", false)
	if !bin.RecalculateLatency() {
		t.Fatalf("RecalculateLatency refused")
	}
	// gst_bin_recalculate_latency dispatches the recalculation onto the
	// pipeline's own streaming/messaging machinery rather than blocking
	// until it lands; this settle window is the same order of magnitude
	// as buildSettleWindow (engine_cgo.go), which this package's own code
	// already treats as enough time for a latency-adjacent async effect
	// to surface.
	time.Sleep(300 * time.Millisecond)
}

// truncationReproConfig is the fixed, minimal topology both arms share:
// exactly one program channel, no LTC, so exactly one keep-alive source
// exists for forceNonLive to control and the produced audio is trivial to
// analyze (interleave's single sink pad output is the whole file). Using
// one shared config for both arms, differing only in whether forceNonLive
// runs afterward, isolates is-live/liveness as the only variable between
// them -- anything else that differed between the two arms would confound
// the measurement this experiment exists to make.
func truncationReproConfig() Config {
	return Config{
		SinkFactory:     "alsasink", // checkPrerequisites probes this name independently of the sink actually used; see newSinkFactoryElement's doc comment
		ProgramChannels: []int{1},
		ChannelCount:    1,
		SampleRate:      truncationReproRate,
		Resolve:         resolveByRuntimeFilename,
	}
}

// runTruncationArm is the shared body for both arms: build the tee/
// capture sink against device, build the engine, optionally force the
// pipeline non-live, confirm via pipeline.IsLive() (never assume the
// forcing took effect), load and start the sweep at position 0 exactly as
// Engine.Start's own production callers would, and let it render.
func runTruncationArm(t *testing.T, forceNonLiveArm bool, captureEnvVar string) {
	t.Helper()
	device := os.Getenv("SHOWMESH_TRUNC_DEVICE")
	sweep := os.Getenv("SHOWMESH_TRUNC_SWEEP")
	capture := os.Getenv(captureEnvVar)
	if device == "" || sweep == "" || capture == "" {
		t.Skipf("set SHOWMESH_TRUNC_DEVICE, SHOWMESH_TRUNC_SWEEP and %s to run this gate", captureEnvVar)
	}

	sink, err := buildTruncationCaptureSink(device, capture)
	if err != nil {
		t.Fatalf("buildTruncationCaptureSink: %v", err)
	}

	origSinkFactory := newSinkFactoryElement
	newSinkFactoryElement = func(Config) (gst.Element, error) { return sink, nil }
	cfg := truncationReproConfig()
	e, err := New(cfg)
	newSinkFactoryElement = origSinkFactory
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = boundedCall(ctx, func() error { _ = e.Close(); return nil })
	})

	if ok, reason := e.Available(); !ok {
		t.Fatalf("engine unavailable against %s: %s", device, reason)
	}

	if forceNonLiveArm {
		forceNonLive(t, e)
	}

	gotLive := e.pipeline.IsLive()
	t.Logf("CONTROL: pipeline.IsLive() = %v (requested forceNonLive=%v)", gotLive, forceNonLiveArm)
	if gotLive == forceNonLiveArm {
		t.Fatalf("CONTROL FAILURE: pipeline.IsLive()=%v while forceNonLive=%v -- the live/non-live toggle did not produce the intended pipeline classification. STOP: any truncation reading from this run is void, not a result.", gotLive, forceNonLiveArm)
	}

	if clk := e.pipeline.GetPipelineClock(); clk != nil {
		t.Logf("pipeline selected clock = %q", clk.GetName())
	} else {
		t.Fatalf("CONTROL FAILURE: pipeline has no selected clock at all -- cannot be the real card's clock either. STOP.")
	}
	t.Logf("NOTE: confirming this named clock is specifically the alsasink instance's own ALSA clock (not a system-clock fallback because the real device could not provide one) requires GST_DEBUG=GST_PIPELINE:5 on this run -- see the run instructions for the exact grep target (\"selected clock\") and treat its absence there as a control failure too, not as silent success.")

	handle := agentaudio.EngineHandle(fmt.Sprintf("truncation-repro-%s", captureEnvVar))
	media := pkgaudio.MediaRef{AssetID: "truncation-repro-sweep", ContentHash: "sweep-v1", RuntimeFilename: sweep}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := e.Load(ctx, handle, media, 2*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}

	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}
	preStartRunningTime := b.pipelineRunningTime()

	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	appliedOffset := int64(0)
	if len(b.channelMixerPads) > 0 && b.channelMixerPads[0] != nil {
		appliedOffset = b.channelMixerPads[0].GetOffset()
	}
	t.Logf("DIAGNOSTIC: pipelineRunningTime just before Start = %s; resyncMixerPads applied pad offset = %s (%d ns)",
		preStartRunningTime, time.Duration(appliedOffset), appliedOffset)

	time.Sleep(truncationReproSettle)

	t.Logf("capture written to %s -- analyze with analyze.go against the same sweep passed as SHOWMESH_TRUNC_SWEEP", capture)
}

// TestTruncationRepro_LiveArm is the comparison arm (control #1): the
// unmodified production pipeline shape, which addMixerKeepAlive always
// makes live. Expectation: pipeline.IsLive() == true and no truncation
// measured in the capture (the prediction places the anchor ~18ms in the
// mixer's future here, not its past).
func TestTruncationRepro_LiveArm(t *testing.T) {
	runTruncationArm(t, false, "SHOWMESH_TRUNC_CAPTURE_LIVE")
}

// TestTruncationRepro_NonLiveArm is the experimental arm: the same
// topology with the channel-1 keep-alive source's is-live forced to
// false, test-locally, after production has already built the pipeline.
// Expectation under the prediction this file tests: pipeline.IsLive() == false and
// the capture is missing roughly the first 395ms of the sweep's content
// (not merely delayed by that long).
func TestTruncationRepro_NonLiveArm(t *testing.T) {
	runTruncationArm(t, true, "SHOWMESH_TRUNC_CAPTURE_NONLIVE")
}

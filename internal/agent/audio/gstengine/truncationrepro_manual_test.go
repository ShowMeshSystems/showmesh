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
// The non-live arm cannot be reached through production configuration at
// all today: every is-live site in this package (engine_cgo.go's
// addMixerKeepAlive and its silence-channel chain, ltc.go's LTC appsrc
// chain) is a bare literal `true`, addMixerKeepAlive runs unconditionally
// once per program channel, and config.go's Config.Validate rejects an
// empty ProgramChannels -- so every structurally valid Config yields at
// least one live source. That is a real, separate finding: production as
// shipped cannot build a non-live pipeline, only ever a live one, which
// is also why the live arm below is production's own real behavior, not
// a constructed comparison case.
//
// A first version of this file tried to reach non-live anyway by
// flipping "is-live" on the keep-alive source AFTER New had already
// built and started the real pipeline, then calling
// gst_bin_recalculate_latency. That measurably does not work: run
// against a real device, pipeline.IsLive() kept reading true, and this
// file's own CONTROL FAILURE check caught it and stopped rather than
// reporting a number. GStreamer decides a pipeline's liveness once,
// during its own READY->PAUSED preroll (the LATENCY query every element
// answers at that point); recalculating latency afterward reconfigures
// timing within that existing classification, it does not reopen the
// classification itself -- the same reason a pipeline's clock, once
// selected, cannot be changed by calling UseClock again after PLAYING
// (a separate, independently confirmed finding on this same hardware).
//
// buildTestPipeline below is what actually reaches non-live: it
// constructs its own pipeline object, test-locally, with the keep-alive
// source's is-live set to false from the moment it is created --
// before the pipeline's first state change, not after. It is not a
// modified copy of Engine.buildPipeline and does not replace it:
// production's own buildPipeline is untouched, is still what the live
// arm calls, and still hardcodes is-live=true exactly as shipped. This
// file's construction-time builder exists solely so that pipeline.
// IsLive() == false becomes reachable to measure at all, following the
// same test-injection precedent as newSinkFactoryElement. No production
// file gained a config field, a flag, or an environment variable that
// changes what it builds.
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

// buildTestPipeline constructs a pipeline for exactly the single-
// program-channel, no-LTC topology truncationReproConfig describes, with
// live controlling the channel's keep-alive source's is-live from the
// moment it is created -- see this file's own top doc comment for why
// that timing, not the property's final value alone, is what actually
// determines pipeline.IsLive().
//
// It reuses production's own private linking and caps helpers verbatim
// -- probeSinkChannelPositions (channelpositions.go) and
// linkInterleaveToSink (engine_cgo.go) -- rather than reimplementing
// them, so every part of the topology this test does not need to differ
// on is guaranteed identical to what Engine.buildPipeline itself builds.
// The one deliberate divergence is testKeepAlive below, a copy of
// engine_cgo.go's addMixerKeepAlive with is-live turned into a
// parameter -- the only property this experiment needs to vary, and the
// one addMixerKeepAlive itself has no way to vary since it hardcodes
// true unconditionally. Everything else -- interleave, its
// channel-positions-from-input decision, the mixer, its output
// capsfilter, the sink-side audioconvert/audioresample/capsfilter chain
// -- comes from calling production's own code, not from duplicating it.
//
// This function is never called by anything in engine_cgo.go and adds
// no seam production reaches: it is this test file's own construction
// path, used only by runTruncationArm below.
func buildTestPipeline(cfg Config, sink gst.Element, live bool) (*Engine, error) {
	if len(cfg.ProgramChannels) != 1 || cfg.ChannelCount != 1 || cfg.LTCChannel != 0 {
		return nil, fmt.Errorf("buildTestPipeline only supports a single program channel with no LTC; got %+v", cfg)
	}
	ch := cfg.ProgramChannels[0]

	pipelineElem := gst.ElementFactoryMake("pipeline", "")
	pipeline, ok := pipelineElem.(gst.Pipeline)
	if !ok {
		return nil, fmt.Errorf("could not create a gst.Pipeline")
	}
	bin, ok := pipeline.(gst.Bin)
	if !ok {
		return nil, fmt.Errorf("pipeline does not implement gst.Bin")
	}

	interleave := gst.ElementFactoryMake("interleave", "interleave")
	if interleave == nil {
		return nil, fmt.Errorf("could not create interleave")
	}
	if _, isBin := sink.(gst.Bin); !isBin {
		sink.SetObjectProperty("qos", true)
	}
	positionBits := probeSinkChannelPositions(sink, cfg.ChannelCount, cfg.SampleRate)
	interleave.SetObjectProperty("channel-positions-from-input", len(positionBits) > 0)
	if !bin.Add(interleave) {
		return nil, fmt.Errorf("could not add interleave to pipeline")
	}
	if !bin.Add(sink) {
		return nil, fmt.Errorf("could not add sink to pipeline")
	}
	if err := linkInterleaveToSink(bin, interleave, sink, cfg.ChannelCount, positionBits); err != nil {
		return nil, err
	}

	sinkPad := interleave.RequestPadSimple("sink_%u")
	if sinkPad == nil {
		return nil, fmt.Errorf("interleave refused a sink pad request for channel %d", ch)
	}
	var maskBit uint64
	if len(positionBits) > 0 {
		maskBit = positionBits[ch-1]
	}

	mixer := gst.ElementFactoryMake("audiomixer", fmt.Sprintf("mixer-ch%d", ch))
	if mixer == nil {
		return nil, fmt.Errorf("could not create audiomixer for channel %d", ch)
	}
	mixer.SetObjectProperty("ignore-inactive-pads", true)
	if !bin.Add(mixer) {
		return nil, fmt.Errorf("could not add audiomixer for channel %d", ch)
	}
	mixerCaps := gst.ElementFactoryMake("capsfilter", fmt.Sprintf("mixer-caps-ch%d", ch))
	if mixerCaps == nil {
		return nil, fmt.Errorf("could not create mixer output capsfilter for channel %d", ch)
	}
	mixerCaps.SetObjectProperty("caps", gst.CapsFromString(channelCapsString(cfg.SampleRate, maskBit)))
	if !bin.Add(mixerCaps) {
		return nil, fmt.Errorf("could not add mixer output capsfilter for channel %d", ch)
	}
	if !mixer.Link(mixerCaps) {
		return nil, fmt.Errorf("could not link channel %d mixer to its output capsfilter", ch)
	}
	if mixerCaps.GetStaticPad("src").Link(sinkPad) != gst.PadLinkOK {
		return nil, fmt.Errorf("could not link channel %d mixer to interleave", ch)
	}
	if err := testKeepAlive(bin, mixer, ch, cfg.SampleRate, live); err != nil {
		return nil, err
	}

	e := &Engine{
		cfg:           cfg,
		handles:       make(map[agentaudio.EngineHandle]*branch),
		elementIndex:  make(map[string]*branch),
		done:          make(chan struct{}),
		startedAt:     time.Now(),
		pipeline:      pipeline,
		channelMixers: []gst.Element{mixer},
	}

	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		return nil, fmt.Errorf("test pipeline refused to reach PLAYING")
	}
	if err := e.awaitSustainedPlaying(); err != nil {
		return nil, err
	}
	e.availOK = true
	go e.watchBus()
	return e, nil
}

// testKeepAlive mirrors engine_cgo.go's addMixerKeepAlive exactly except
// is-live is a parameter instead of a hardcoded true -- see
// buildTestPipeline's own doc comment for why this is the one property
// this experiment needs a construction-time seam for at all.
func testKeepAlive(bin gst.Bin, mixer gst.Element, ch int, sampleRate int, live bool) error {
	src := gst.ElementFactoryMake("audiotestsrc", fmt.Sprintf("keepalive-ch%d", ch))
	conv := gst.ElementFactoryMake("audioconvert", fmt.Sprintf("keepalive-conv-ch%d", ch))
	resample := gst.ElementFactoryMake("audioresample", fmt.Sprintf("keepalive-resample-ch%d", ch))
	caps := gst.ElementFactoryMake("capsfilter", fmt.Sprintf("keepalive-caps-ch%d", ch))
	if src == nil || conv == nil || resample == nil || caps == nil {
		return fmt.Errorf("could not create mixer keep-alive chain for channel %d", ch)
	}
	src.SetObjectProperty("is-live", live)
	src.SetObjectProperty("wave", int32(4)) // GST_AUDIO_TEST_SRC_WAVE_SILENCE
	caps.SetObjectProperty("caps", gst.CapsFromString(fmt.Sprintf("audio/x-raw,format=%s,rate=%d,channels=1", interleaveSampleFormat, sampleRate)))
	for _, el := range []gst.Element{src, conv, resample, caps} {
		if !bin.Add(el) {
			return fmt.Errorf("could not add mixer keep-alive element for channel %d", ch)
		}
	}
	if !src.Link(conv) || !conv.Link(resample) || !resample.Link(caps) {
		return fmt.Errorf("could not link mixer keep-alive chain for channel %d", ch)
	}
	pad := mixer.RequestPadSimple("sink_%u")
	if pad == nil {
		return fmt.Errorf("mixer for channel %d refused a keep-alive sink pad", ch)
	}
	if caps.GetStaticPad("src").Link(pad) != gst.PadLinkOK {
		return fmt.Errorf("could not link mixer keep-alive source for channel %d", ch)
	}
	return nil
}

// truncationReproConfig is the fixed, minimal topology both arms share:
// exactly one program channel, no LTC, so exactly one keep-alive source
// exists for buildTestPipeline to control and the produced audio is
// trivial to analyze (interleave's single sink pad output is the whole
// file). One shared config for both arms, built by New for the live arm
// and by buildTestPipeline for the non-live arm, differing only in the
// keep-alive source's is-live at construction, isolates liveness as the
// only variable between them -- anything else that differed would
// confound the measurement this experiment exists to make.
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
// capture sink against device, build the engine (production's own New
// for the live arm, buildTestPipeline for the non-live arm -- see its
// doc comment for why the non-live arm cannot also go through New),
// confirm liveness via pipeline.IsLive() (never assume construction
// achieved what it intended), load and start the sweep at position 0
// exactly as Engine.Start's own production callers would, and let it
// render.
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

	var e *Engine
	if forceNonLiveArm {
		e, err = buildTestPipeline(truncationReproConfig(), sink, false)
		if err != nil {
			t.Fatalf("buildTestPipeline (non-live): %v", err)
		}
	} else {
		origSinkFactory := newSinkFactoryElement
		newSinkFactoryElement = func(Config) (gst.Element, error) { return sink, nil }
		e, err = New(truncationReproConfig())
		newSinkFactoryElement = origSinkFactory
		if err != nil {
			t.Fatalf("New: %v", err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = boundedCall(ctx, func() error { _ = e.Close(); return nil })
	})

	if ok, reason := e.Available(); !ok {
		t.Fatalf("engine unavailable against %s: %s", device, reason)
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

// TestTruncationRepro_LiveArm is the comparison arm (control #1):
// production's own New/buildPipeline, entirely unmodified, which
// addMixerKeepAlive always makes live -- this is also the only pipeline
// shape production can build today (see this file's top doc comment).
// Expectation: pipeline.IsLive() == true and no truncation measured in
// the capture (the prediction places the anchor ~18ms in the mixer's
// future here, not its past).
func TestTruncationRepro_LiveArm(t *testing.T) {
	runTruncationArm(t, false, "SHOWMESH_TRUNC_CAPTURE_LIVE")
}

// TestTruncationRepro_NonLiveArm is the experimental arm: the same
// topology built by this file's own buildTestPipeline, with the
// channel-1 keep-alive source's is-live set to false from construction
// -- not reachable through production configuration at all today (see
// this file's top doc comment), and the state a future fix to make the
// engine clock-before-playback would put it in. Expectation under the
// prediction this file tests: pipeline.IsLive() == false and the capture
// is missing roughly the first 395ms of the sweep's content (not merely
// delayed by that long).
func TestTruncationRepro_NonLiveArm(t *testing.T) {
	runTruncationArm(t, true, "SHOWMESH_TRUNC_CAPTURE_NONLIVE")
}

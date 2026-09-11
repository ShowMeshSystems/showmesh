//go:build cgo

package gstengine

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

// This suite drives a real GStreamer pipeline through go-gst with
// "fakesink" — no physical audio device, no claim about ALSA or real
// hardware output, and fakesink offers no audio clock of its own. It
// proves the pipeline this package builds is not live and still reaches
// PLAYING, with and without an LTC channel configured. Whether a real
// audio sink's clock is actually selected, and whether the sink slaves to
// it, is real-hardware evidence this suite cannot produce.

// pipelineReachesPlaying blocks up to timeout for e's shared output
// pipeline to leave its async PLAYING transition and reports the state it
// settled in.
func pipelineReachesPlaying(t *testing.T, e *Engine, timeout time.Duration) gst.State {
	t.Helper()
	current, _, ret := e.pipeline.GetState(gst.ClockTime(timeout))
	if ret == gst.StateChangeFailure {
		t.Fatalf("pipeline.GetState: state change failed while waiting for PLAYING")
	}
	return current
}

// TestPipelineReachesPlayingWithoutLTC is this task's risk #2 acceptance
// check for the ordinary case: a non-live pipeline's PAUSED transition
// returns ASYNC rather than the NO_PREROLL a live pipeline returned
// before this change, and New already only treats StateChangeFailure as
// an error (see buildPipeline and awaitSustainedPlaying). This proves
// that tolerance actually carries a non-live pipeline through to PLAYING
// within a bound, not merely that the code compiles against ASYNC.
func TestPipelineReachesPlayingWithoutLTC(t *testing.T) {
	e := newTestEngine(t)
	if got := pipelineReachesPlaying(t, e, 5*time.Second); got != gst.StatePlaying {
		t.Fatalf("pipeline state after New = %v, want PLAYING", got)
	}
}

// TestPipelineReachesPlayingWithLTC is this task's risk #1 acceptance
// check: the LTC appsrc is also non-live now, and a non-live aggregator
// (interleave, downstream of every channel including the LTC chain) will
// not complete its own PAUSED preroll until every sink pad it collects
// from has offered a buffer. runLTCFeeder starts pushing silence before
// any LTC run is requested (see New's own comment on why it starts before
// awaitSustainedPlaying), so the LTC pad should still have data to offer
// during preroll. This proves the pipeline still reaches PLAYING within a
// bound with an LTC channel configured, which no prior run of this
// codebase has exercised: the LTC appsrc was live until this change, so
// its own pad never blocked the aggregator's preroll on data.
func TestPipelineReachesPlayingWithLTC(t *testing.T) {
	e := newLTCTestEngine(t)
	if got := pipelineReachesPlaying(t, e, 5*time.Second); got != gst.StatePlaying {
		t.Fatalf("pipeline state after New with an LTC channel = %v, want PLAYING", got)
	}
}

// TestPipelineIsNotLiveWithoutLTC is this task's risk #3 acceptance check
// for the ordinary case: gst_pipeline_is_live reports true only when the
// pipeline contains at least one live source. With is-live false on every
// silence and keep-alive source, the built pipeline must report false.
func TestPipelineIsNotLiveWithoutLTC(t *testing.T) {
	e := newTestEngine(t)
	if e.pipeline.IsLive() {
		t.Fatal("pipeline.IsLive() = true, want false: no source in this pipeline should report itself live")
	}
}

// TestPipelineIsNotLiveWithLTC is TestPipelineIsNotLiveWithoutLTC's
// counterpart with an LTC channel configured, whose appsrc is the third
// is-live site this task changes.
func TestPipelineIsNotLiveWithLTC(t *testing.T) {
	e := newLTCTestEngine(t)
	if e.pipeline.IsLive() {
		t.Fatal("pipeline.IsLive() = true with an LTC channel, want false: the LTC appsrc must not report itself live either")
	}
}

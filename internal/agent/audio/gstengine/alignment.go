//go:build cgo

package gstengine

import (
	"context"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

var _ agentaudio.AlignmentObserver = (*Engine)(nil)

// Alignment implements [agentaudio.AlignmentObserver]: it samples handle's
// branch position and this engine's one LTC channel's timecode against
// the SAME shared pipeline running time, read once and used for both
// sides, so a caller never differences readings taken at two different
// instants.
func (e *Engine) Alignment(_ context.Context, handle agentaudio.EngineHandle) (agentaudio.AlignmentSample, bool, string) {
	if ok, reason := e.Available(); !ok {
		return agentaudio.AlignmentSample{}, false, reason
	}
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.AlignmentSample{}, false, err.Error()
	}
	if err := b.checkAnchorKnown(); err != nil {
		return agentaudio.AlignmentSample{}, false, "this branch's position anchoring is no longer trustworthy: " + err.Error()
	}
	if e.ltc == nil {
		return agentaudio.AlignmentSample{}, false, ltcNoChannelReason
	}

	rt := e.pipeline.GetCurrentRunningTime()
	if rt == gst.ClockTimeNone {
		return agentaudio.AlignmentSample{}, false, "this node's output pipeline has no running time yet; it has not reached PLAYING"
	}
	runningTime := time.Duration(rt)

	b.mu.Lock()
	segmentStart := b.segmentStart
	renderedPos := b.renderedPos
	pads := b.deinterleaveSrcPads
	b.mu.Unlock()

	if len(pads) == 0 || pads[0] == nil {
		return agentaudio.AlignmentSample{}, false, "this branch has not joined the shared mixer yet"
	}
	// A pad offset is only ever set on every one of a branch's src pads
	// together (resyncMixerPads), so the first is as good evidence as any.
	offset := time.Duration(pads[0].GetOffset())
	programPos := segmentStart + (runningTime - offset)
	if programPos < 0 {
		programPos = 0
	}

	// The branch has not decoded up to the position the pipeline is
	// presenting: what plays is the mixer's own silent keep-alive pad, not
	// this branch's program audio, so a model-derived alignment here would
	// describe a session that is not actually being heard.
	if renderedPos < programPos-queueMaxSizeTime {
		return agentaudio.AlignmentSample{}, false, "program branch has not rendered up to its presented position; underrun suspected"
	}

	e.ltc.mu.Lock()
	active := e.ltc.active
	generation := e.ltc.generation
	rate := e.ltc.rate
	startTC := e.ltc.generationStartTimecode
	e.ltc.mu.Unlock()
	emitted := e.ltc.emittedGeneration.Load()

	if !active || emitted != generation {
		return agentaudio.AlignmentSample{}, false, "LTC generation is not confirmed active for this sample"
	}

	anchorGen := e.ltc.genAnchorGeneration.Load()
	if anchorGen != generation {
		return agentaudio.AlignmentSample{}, false, "this generation's LTC alignment anchor is not yet established"
	}
	genAnchor := time.Duration(e.ltc.genAnchorNs.Load())

	tc, err := startTC.Advance(runningTime-genAnchor, rate)
	if err != nil {
		return agentaudio.AlignmentSample{}, false, "could not resolve the LTC timecode audible at this running time: " + err.Error()
	}

	return agentaudio.AlignmentSample{
		ProgramPosition: programPos,
		LTCTimecode:     tc,
		LTCFrameRate:    rate,
		RunningTime:     runningTime,
		SampledAt:       e.cfg.now(),
	}, true, ""
}

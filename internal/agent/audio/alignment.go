package audio

import (
	"context"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// AlignmentSample is one handle's raw program-to-LTC alignment quantities,
// read at one shared pipeline running time. [Manager.AlignmentSnapshot]
// turns this into the signed millisecond offset the wire reports; this
// package's own arithmetic stays out of the engine, since only the
// manager knows the session's LTC start offset.
type AlignmentSample struct {
	// ProgramPosition is the program position the shared pipeline's mixer
	// mapping places at RunningTime for this handle's branch.
	ProgramPosition time.Duration

	// LTCTimecode/LTCFrameRate is the timecode, and the rate it is
	// expressed at, that the LTC feeder placed at RunningTime.
	LTCTimecode  pkgaudio.LTCTimecode
	LTCFrameRate pkgaudio.LTCFrameRate

	// RunningTime is the shared pipeline running time both quantities
	// above were read against; a caller differences two engine readings
	// taken at the SAME RunningTime, never at two different ones.
	RunningTime time.Duration

	// SampledAt is the wall-clock instant this sample was taken, this
	// engine's own clock, never the caller's, and never a later report
	// tick's own time. A stale sample must be identifiable as stale, which
	// only the sample's own time can do.
	SampledAt time.Time
}

// AlignmentObserver is implemented by an [Engine] that can report one
// handle's raw program-to-LTC alignment quantities. known is false, with a
// reason, whenever the pipeline, the branch, or the LTC generation is not
// in a state honest to measure from, never a fabricated sample.
type AlignmentObserver interface {
	Alignment(ctx context.Context, handle EngineHandle) (sample AlignmentSample, known bool, reason string)
}

// noAlignmentObserverReason is what [ObserveEngineAlignment] reports for
// an engine that does not implement [AlignmentObserver] at all.
const noAlignmentObserverReason = "this node's audio engine does not report program-to-LTC alignment"

// ObserveEngineAlignment returns engine's fresh alignment evidence for
// handle, or (zero, false, reason) when engine does not implement
// [AlignmentObserver].
func ObserveEngineAlignment(ctx context.Context, engine Engine, handle EngineHandle) (AlignmentSample, bool, string) {
	o, ok := engine.(AlignmentObserver)
	if !ok {
		return AlignmentSample{}, false, noAlignmentObserverReason
	}
	return o.Alignment(ctx, handle)
}

package audio

import (
	"context"
	"time"
)

// SinkClockObserver is implemented by an [Engine] that can report how
// much audio its OUTPUT has actually presented, measured on the sink's
// own clock rather than at the decode frontier. The distinction is the
// whole point: a decoder can be seconds ahead of the speaker, so a
// position read anywhere upstream of the sink answers a different
// question from the one a scheduled timeline asks.
//
// known distinguishes a genuine zero from "not collected", exactly as
// [GlitchObserver] does; reason is required whenever known is false.
type SinkClockObserver interface {
	// PresentedElapsed reports the output's presented running time: the
	// presented sample count over the nominal rate, expressed as a
	// duration since this engine's pipeline began running. It is a
	// free-running counter for the engine as a whole, not a position
	// within any one session's media, so a caller measures an interval
	// by differencing two readings rather than reading one absolutely.
	PresentedElapsed(ctx context.Context) (elapsed time.Duration, known bool, reason string)
}

// ObserveEngineSinkClock returns engine's presented running time, or
// (0, false, reason) when engine does not implement [SinkClockObserver].
func ObserveEngineSinkClock(ctx context.Context, engine Engine) (time.Duration, bool, string) {
	o, ok := engine.(SinkClockObserver)
	if !ok {
		return 0, false, "this node's audio engine does not report a sink clock"
	}
	return o.PresentedElapsed(ctx)
}

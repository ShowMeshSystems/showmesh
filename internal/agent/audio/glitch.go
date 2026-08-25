package audio

import "time"

// GlitchCounts is one engine's cumulative bus-level glitch counts since
// Since. Stream/Resource/OtherWarnings classify WARNING-class bus
// messages by GError domain, not confirmed to correlate with an ALSA
// xrun/underrun -- see gstengine's watchBus. QosEvents counts QOS-class
// bus messages.
type GlitchCounts struct {
	// Since is when the reporting engine started counting; changes on
	// every rebind, so a caller can tell a reset from a quiet period.
	Since time.Time

	StreamWarnings   uint64
	ResourceWarnings uint64
	OtherWarnings    uint64
	QosEvents        uint64
}

// GlitchObserver is implemented by an [Engine] that counts glitch-class
// bus conditions. known distinguishes a genuine zero from "not collected".
type GlitchObserver interface {
	GlitchCounts() (counts GlitchCounts, known bool)
}

// ObserveEngineGlitches returns engine's glitch counts, or (zero, false)
// when engine does not implement [GlitchObserver].
func ObserveEngineGlitches(engine Engine) (GlitchCounts, bool) {
	g, ok := engine.(GlitchObserver)
	if !ok {
		return GlitchCounts{}, false
	}
	return g.GlitchCounts()
}

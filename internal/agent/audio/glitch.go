package audio

// GlitchCounts is the cumulative count of glitch-class bus conditions an
// engine has observed since it started: Warnings covers GStreamer
// WARNING-class bus messages (ALSA xrun/underrun and clock-drift sample
// drop/insert are reported this way), and QosEvents covers QoS-class bus
// messages (a downstream element dropping or skipping buffers to keep
// pace with the clock).
type GlitchCounts struct {
	Warnings  uint64
	QosEvents uint64
}

// GlitchObserver is implemented by an [Engine] that counts glitch-class
// bus conditions. Not every Engine backend can: [ObserveEngineGlitches]'s
// known return distinguishes "counted, here is the number" from "this
// backend never collects this evidence", so a genuine zero is never
// confused with an absent count.
type GlitchObserver interface {
	GlitchCounts() (counts GlitchCounts, known bool)
}

// ObserveEngineGlitches returns engine's cumulative glitch counts, or
// (zero value, false) when engine is nil or does not implement
// [GlitchObserver] — never a fabricated zero for a backend that never
// collects this evidence, matching [ObserveEngineLTC]'s identical
// absent-evidence convention one field over.
func ObserveEngineGlitches(engine Engine) (GlitchCounts, bool) {
	g, ok := engine.(GlitchObserver)
	if engine == nil || !ok {
		return GlitchCounts{}, false
	}
	return g.GlitchCounts()
}

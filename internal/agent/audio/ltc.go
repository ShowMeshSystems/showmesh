package audio

import (
	"context"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// LTCState is the node's LTC output vocabulary as reported on
// node.audio.ltc.generator.state.
type LTCState string

const (
	// LTCStopped means no LTC is being generated: never started, or
	// stopped along with the session that drove it.
	LTCStopped LTCState = "stopped"
	// LTCRunning means the engine confirmed it is emitting LTC samples.
	LTCRunning LTCState = "running"
	// LTCFailed means generation was requested and the engine could not
	// deliver it.
	LTCFailed LTCState = "failed"
	// LTCUnsupported means this build or this node cannot generate LTC at
	// all, for the stated reason.
	LTCUnsupported LTCState = "unsupported"
)

// LTCSpec is one LTC run's configuration: the frame rate to encode at and
// the timecode the first emitted sample carries.
type LTCSpec struct {
	FrameRate     pkgaudio.LTCFrameRate
	StartTimecode pkgaudio.LTCTimecode
}

// LTCObservation is evidence about this node's LTC output collected at
// ObservedAt. FrameRateKnown and TimecodeKnown are false when the value is
// genuinely unknown; neither field ever carries a plausible-looking
// default, and Timecode is present only while the engine is confirmed
// emitting.
type LTCObservation struct {
	State  LTCState
	Reason string

	FrameRateKnown bool
	FrameRate      pkgaudio.LTCFrameRate

	TimecodeKnown bool
	Timecode      pkgaudio.LTCTimecode

	ObservedAt time.Time
}

// LTCGenerator is implemented by an [Engine] that can also place generated
// LTC on the node's discrete LTC channel, in the same pipeline and clock
// domain as program audio. An Engine that does not implement it cannot
// generate LTC, which callers report as [LTCUnsupported] with a reason
// rather than as silence.
//
// StartLTC is also the realignment verb: calling it on an already-running
// generator restarts generation at spec.StartTimecode, which is what a
// seek does.
type LTCGenerator interface {
	StartLTC(ctx context.Context, spec LTCSpec) (LTCObservation, error)
	StopLTC(ctx context.Context) (LTCObservation, error)
	ObserveLTC(ctx context.Context) LTCObservation
}

// noLTCGeneratorReason is what [ObserveEngineLTC] reports for an engine
// that cannot generate LTC at all.
const noLTCGeneratorReason = "this node's audio engine cannot generate LTC"

// ObserveEngineLTC returns engine's fresh LTC evidence, or an
// [LTCUnsupported] observation stamped at now when engine is nil or does
// not implement [LTCGenerator]. Absent evidence is always stated with a
// state and a reason, never omitted.
func ObserveEngineLTC(ctx context.Context, engine Engine, now time.Time) LTCObservation {
	gen, ok := engine.(LTCGenerator)
	if engine == nil || !ok {
		return LTCObservation{State: LTCUnsupported, Reason: noLTCGeneratorReason, ObservedAt: now}
	}
	return gen.ObserveLTC(ctx)
}

// ResolveLTCStartOffset implements this project's start-offset precedence
// exactly: a session's own override, when present, always wins; otherwise
// the coordinator's audio.settings default applies.
func ResolveLTCStartOffset(sessionOverride *pkgaudio.LTCTimecode, settingsDefault pkgaudio.LTCTimecode) pkgaudio.LTCTimecode {
	if sessionOverride != nil {
		return *sessionOverride
	}
	return settingsDefault
}

package gstengine

import (
	"errors"
	"fmt"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// AssetResolver maps a [pkgaudio.MediaRef] to a local file path this node
// can open — typically Track E's node-local asset store. The vertical-slice
// wiring seam supplies the real implementation; tests inject one over
// fixture files.
type AssetResolver func(media pkgaudio.MediaRef) (string, error)

// ClockReader is the pipeline clock source [Config.Clock] wraps —
// internal/agent/clock.PHCReader in production, opened once by the
// caller and handed here already open, so the GStreamer clock callback
// never itself opens or closes a device file (RES-019 section 7.2
// candidate A: the callback runs on GStreamer's own scheduling and must
// be cheap and never block). A test may inject any implementation.
type ClockReader interface {
	// Now reads the current time off this clock source.
	Now() (time.Time, error)

	// Close releases whatever this reader holds open. Called exactly
	// once, by [Engine.Close], regardless of whether the reader was ever
	// actually used as the pipeline clock.
	Close() error
}

// Config is the output pipeline's fixed shape: one physical sink, its
// negotiated channel layout, where the mixed program bus lands on that
// layout, and which channel (if any) carries generated LTC. Every index
// claimed by neither ProgramChannels nor LTCChannel is wired to silence.
type Config struct {
	// SinkFactory is the GStreamer element factory name for the output
	// sink — "alsasink" in production, a non-hardware sink (e.g.
	// "fakesink") in tests and on hosts with no ALSA.
	SinkFactory string

	// SinkProperties are set on the constructed sink element, e.g.
	// {"device": "hw:1,0"}.
	SinkProperties map[string]any

	// ProgramChannels are the 1-based output channel indices carrying
	// the mixed program bus, in left-to-right order.
	ProgramChannels []int

	// ChannelCount is the total number of channels the device output
	// carries. Must be at least the highest index in ProgramChannels.
	ChannelCount int

	// LTCChannel is the 1-based output channel index carrying generated
	// LTC, or 0 if this node generates no LTC at all. Never a member of
	// ProgramChannels.
	LTCChannel int

	// SampleRate is the output pipeline's sample rate in Hz.
	SampleRate int

	// Resolve maps a session's MediaRef to a local file path. Required.
	Resolve AssetResolver

	// Now returns the current time for observation timestamps. Defaults
	// to time.Now; overridden in tests for deterministic ObservedAt
	// comparisons. It never affects a reported Position, which always
	// comes from a live GStreamer query.
	Now func() time.Time

	// Clock, when non-nil, is this pipeline's own running clock — the
	// node's PTP hardware clock (PHC) or, for a node with no PHC
	// hardware, its CLOCK_REALTIME (see [ClockKind]) — already open, per
	// RES-019 section 7.2 candidate A (ADR-046): the output pipeline runs
	// on the same timebase as the card it plays through, so alsasink
	// never needs to step the playout pointer to correct for a
	// system/card clock drift, and pipewiresink never waits forever for a
	// graph timebase its own pipeline clock does not share (proven on a
	// Raspberry Pi with no PHC: GStreamer's own default clock never
	// presented a sample; CLOCK_REALTIME played correctly). [Engine]
	// reads it once at construction, before the pipeline's first state
	// change (a clock installed afterward is proven not to reach an
	// already-playing sink), to confirm it is actually readable; a
	// failed read there is not fatal — the engine falls back to
	// GStreamer's own default clock exactly as it did before this field
	// existed. nil means no clock at all was configured for this node,
	// which is likewise not a failure.
	Clock ClockReader

	// ClockKind is which kind of clock Clock actually is — [ClockKindPHC]
	// or [ClockKindRealtime] — reported verbatim by [Engine.ClockSource]
	// once Clock is confirmed readable and installed. Meaningless when
	// Clock is nil. Defaults to [ClockKindPHC] when left empty, matching
	// this field's only value before [ClockKindRealtime] existed.
	ClockKind string

	// ClockUnavailableReason is set by the caller instead of Clock when
	// a pipeline clock was configured for this node but could not even
	// be opened (a named interface with a PHC that could not be opened)
	// — carried through so [Engine.ClockSource] reports the same class
	// of reason whether the failure happened before or after this
	// package ever saw a reader. Left empty when Clock is nil because
	// nothing was configured at all; ignored when Clock is non-nil.
	ClockUnavailableReason string
}

// ClockKindPHC and ClockKindRealtime are [Config.ClockKind]'s closed
// vocabulary, matching [Engine.ClockSource]'s own reserved
// node.audio.engine.clock_source values.
const (
	// ClockKindPHC is Clock's kind when it reads the node's PTP hardware
	// clock — RES-019 section 7.2 candidate A.
	ClockKindPHC = "phc"

	// ClockKindRealtime is Clock's kind when it reads CLOCK_REALTIME as
	// this node's media clock: selected for a node with no PHC hardware,
	// whose ptp4l instance (in software timestamping mode) disciplines
	// CLOCK_REALTIME itself, so it IS that node's media clock.
	ClockKindRealtime = "realtime"
)

// clockKind reports c.ClockKind, defaulting to [ClockKindPHC] when unset
// — this field's only value before [ClockKindRealtime] existed.
func (c Config) clockKind() string {
	if c.ClockKind == "" {
		return ClockKindPHC
	}
	return c.ClockKind
}

// ErrConfigInvalid is returned by [Config.Validate] and wraps the reason.
var ErrConfigInvalid = errors.New("gstengine: invalid output configuration")

// Validate reports whether c is well-formed enough to build a pipeline
// from: a sink factory name, a positive sample rate, a positive channel
// count, at least one program channel, every program channel index within
// [1, ChannelCount] and non-repeating, and a non-nil asset resolver.
func (c Config) Validate() error {
	if c.SinkFactory == "" {
		return fmt.Errorf("%w: SinkFactory is empty", ErrConfigInvalid)
	}
	if c.SampleRate <= 0 {
		return fmt.Errorf("%w: SampleRate must be positive, got %d", ErrConfigInvalid, c.SampleRate)
	}
	if c.ChannelCount <= 0 {
		return fmt.Errorf("%w: ChannelCount must be positive, got %d", ErrConfigInvalid, c.ChannelCount)
	}
	if len(c.ProgramChannels) == 0 {
		return fmt.Errorf("%w: ProgramChannels is empty", ErrConfigInvalid)
	}
	seen := make(map[int]struct{}, len(c.ProgramChannels))
	for _, idx := range c.ProgramChannels {
		if idx < 1 || idx > c.ChannelCount {
			return fmt.Errorf("%w: program channel %d is outside [1, %d]", ErrConfigInvalid, idx, c.ChannelCount)
		}
		if _, dup := seen[idx]; dup {
			return fmt.Errorf("%w: program channel %d is repeated", ErrConfigInvalid, idx)
		}
		seen[idx] = struct{}{}
	}
	if c.Resolve == nil {
		return fmt.Errorf("%w: Resolve is nil", ErrConfigInvalid)
	}
	if c.LTCChannel != 0 {
		if c.LTCChannel < 1 || c.LTCChannel > c.ChannelCount {
			return fmt.Errorf("%w: LTC channel %d is outside [1, %d]", ErrConfigInvalid, c.LTCChannel, c.ChannelCount)
		}
		if _, isProgram := seen[c.LTCChannel]; isProgram {
			return fmt.Errorf("%w: LTC channel %d is also a program channel", ErrConfigInvalid, c.LTCChannel)
		}
	}
	return nil
}

// now returns c.Now, or time.Now if c.Now is nil.
func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

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

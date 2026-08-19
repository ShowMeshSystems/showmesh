package audio

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Gain is a linear amplitude multiplier: 1.0 is unity gain, 0.0 is
// silence, values above 1.0 amplify.
type Gain float64

// ErrGainInvalid is returned by [Gain.Validate] for a negative, NaN, or
// infinite value.
var ErrGainInvalid = errors.New("audio: gain must be a non-negative, finite number")

// Validate reports whether g is a usable gain value.
func (g Gain) Validate() error {
	f := float64(g)
	if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("%w: got %v", ErrGainInvalid, f)
	}
	return nil
}

// Ceiling is the maximum [Gain] a session may not exceed, in the same
// linear unit as Gain.
type Ceiling float64

// ErrCeilingInvalid is returned by [Ceiling.Validate] for a value that is
// zero, negative, NaN, or infinite.
var ErrCeilingInvalid = errors.New("audio: ceiling must be a positive, finite number")

// Validate reports whether c is a usable ceiling value. Zero and
// negative are rejected: a ceiling that clamps everything to silence is
// expressed by a session's Gain, never by a Ceiling that makes clamping
// indistinguishable from a deliberate mute.
func (c Ceiling) Validate() error {
	f := float64(c)
	if f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("%w: got %v", ErrCeilingInvalid, f)
	}
	return nil
}

// CeilingResult is what [ApplyCeiling] resolves to: the gain a caller
// asked for, the gain that is actually in effect, and whether the
// ceiling changed it.
type CeilingResult struct {
	Requested Gain
	Effective Gain
	Clamped   bool
}

// ApplyCeiling validates requested and ceiling, then clamps requested to
// ceiling when it exceeds it, always reporting the clamp rather than
// silently altering the value.
func ApplyCeiling(requested Gain, ceiling Ceiling) (CeilingResult, error) {
	if err := requested.Validate(); err != nil {
		return CeilingResult{}, err
	}
	if err := ceiling.Validate(); err != nil {
		return CeilingResult{}, err
	}
	if requested > Gain(ceiling) {
		return CeilingResult{Requested: requested, Effective: Gain(ceiling), Clamped: true}, nil
	}
	return CeilingResult{Requested: requested, Effective: requested, Clamped: false}, nil
}

// Fade carries a gain fade's curve, duration, and target gain. Its
// completion is an observable [OutcomeFadeComplete], never inferred from
// elapsed time.
type Fade struct {
	Curve      FadeCurve
	Duration   time.Duration
	TargetGain Gain
}

// ErrFadeDurationNonPositive is returned by [Fade.Validate] when
// Duration is zero or negative.
var ErrFadeDurationNonPositive = errors.New("audio: fade duration must be positive")

// Validate reports whether f is well-formed: Curve is a member of its
// closed vocabulary, Duration is positive, and TargetGain validates.
func (f Fade) Validate() error {
	if err := f.Curve.Validate(); err != nil {
		return err
	}
	if f.Duration <= 0 {
		return ErrFadeDurationNonPositive
	}
	return f.TargetGain.Validate()
}

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

// SilenceFloorDb is the decibel value at or below which an
// operator-entered gain means silence rather than an inaudibly small
// multiplier. It is the same floor show.cue's outputs.announcement
// duckGainDb already bounds against, reused here so one operator meets
// one floor on every surface.
const SilenceFloorDb = -60.0

// UnityDb is the decibel value that leaves a signal unchanged: 0 dB is a
// linear multiplier of 1.0.
const UnityDb = 0.0

// MaxOperatorGainDb is the ceiling every operator-facing gain shares: a
// typo guard, not a tuned headroom figure. Above +12 dB a number is far
// more likely to be a mistake (a millisecond count, a percentage) than an
// intended level. Defined once here so the API boundary, the
// audio.settings validator, and show.action authoring all refuse at the
// same bound rather than at three drifting copies of the number.
const MaxOperatorGainDb = 12.0

// linearFromDb is THIS PROJECT'S ONLY decibel-to-amplitude conversion.
// Amplitude (not power) decibels, so the exponent divides by 20: -6.02 dB
// halves the amplitude, +6.02 dB doubles it. Every operator-facing gain
// arrives in dB and is converted here exactly once, at the coordinator's
// own boundary; the coordinator-to-agent wire, [Gain], [Ceiling], and the
// engine below them stay linear.
func linearFromDb(db float64) float64 {
	return math.Pow(10, db/20)
}

// GainFromDb converts an operator-entered decibel value to the linear
// [Gain] the engine uses. At or below [SilenceFloorDb] the result is
// exactly 0: an operator who asks for silence gets silence, not a
// multiplier small enough to be inaudible but not zero. A NaN input
// converts to silence for the same reason a NaN gain is refused
// elsewhere: it is never a level anyone meant.
func GainFromDb(db float64) Gain {
	if math.IsNaN(db) || db <= SilenceFloorDb {
		return 0
	}
	return Gain(linearFromDb(db))
}

// GainToDb is [GainFromDb]'s inverse for reporting a stored linear gain
// back to an operator. Zero (and any non-positive value, which Validate
// refuses anyway) reports [SilenceFloorDb], so a round trip through
// GainFromDb returns silence rather than negative infinity.
func GainToDb(g Gain) float64 {
	f := float64(g)
	if math.IsNaN(f) || f <= 0 {
		return SilenceFloorDb
	}
	db := 20 * math.Log10(f)
	if db <= SilenceFloorDb {
		return SilenceFloorDb
	}
	return db
}

// CeilingFromDb converts an operator-entered decibel value to a linear
// [Ceiling]. Unlike [GainFromDb] it applies no silence floor: a Ceiling
// of zero is refused by [Ceiling.Validate] on purpose, because a ceiling
// that clamps everything to silence is indistinguishable from a
// deliberate mute. A ceiling at or below the silence floor stays a very
// small positive number and the session's own Gain is what expresses
// silence.
func CeilingFromDb(db float64) Ceiling {
	return Ceiling(linearFromDb(db))
}

// CeilingToDb reports a stored linear [Ceiling] back to an operator in
// decibels, [CeilingFromDb]'s inverse.
func CeilingToDb(c Ceiling) float64 {
	f := float64(c)
	if math.IsNaN(f) || f <= 0 {
		return SilenceFloorDb
	}
	return 20 * math.Log10(f)
}

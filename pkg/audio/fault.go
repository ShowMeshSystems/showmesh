package audio

import "errors"

// SessionFault is one session's currently reported engine-level fault
// (AUDIO-ENGINE section 11.4). FaultNone means no fault is in effect. The
// other six are permanently distinct and none may collapse into another,
// or into [StateStopped]: a session that stops on its own, a session an
// operator stopped, and a session a fault silenced are three different
// facts.
type SessionFault string

const (
	FaultNone                SessionFault = "none"
	FaultPipelineCrash       SessionFault = "pipeline_crash"
	FaultFreeze              SessionFault = "freeze"
	FaultDecodeFailure       SessionFault = "decode_failure"
	FaultMediaDisappeared    SessionFault = "media_disappeared"
	FaultRouteChanged        SessionFault = "route_changed"
	FaultTimingAuthorityLost SessionFault = "timing_authority_lost"

	// FaultOther is an engine error that occurred but does not match any
	// of the six named classes above. It exists so an unrecognized error
	// still reports as a distinct fault rather than silently falling back
	// to a generic Failed outcome with no fault classification at all —
	// see [ClassifyFault].
	FaultOther SessionFault = "other"
)

var sessionFaults = map[string]struct{}{
	string(FaultNone): {}, string(FaultPipelineCrash): {}, string(FaultFreeze): {},
	string(FaultDecodeFailure): {}, string(FaultMediaDisappeared): {},
	string(FaultRouteChanged): {}, string(FaultTimingAuthorityLost): {}, string(FaultOther): {},
}

// Validate reports whether f is one of the eight reserved fault values.
func (f SessionFault) Validate() error {
	return closedSet("audio.SessionFault", string(f), sessionFaults)
}

// Sentinel errors an [Engine] implementation wraps its own errors with, so
// [ClassifyFault] can recover which of the six named fault classes
// produced a given error without the [Engine] interface itself carrying
// fault classification. [FakeEngine] has no real backend to produce these
// from, so tests inject them directly via its fault-injection hooks.
var (
	ErrEnginePipelineCrash       = errors.New("audio: pipeline crashed")
	ErrEngineFreeze              = errors.New("audio: pipeline stopped producing progress")
	ErrEngineDecodeFailure       = errors.New("audio: decode failed")
	ErrEngineMediaDisappeared    = errors.New("audio: media asset disappeared or was replaced")
	ErrEngineRouteChanged        = errors.New("audio: output sample rate or route changed under a running session")
	ErrEngineTimingAuthorityLost = errors.New("audio: timing authority lost")
)

// ClassifyFault maps err onto one of the six named [SessionFault] values
// via errors.Is against this package's sentinel errors, [FaultOther] for
// any other non-nil error, and [FaultNone] for nil. This is the one place
// an engine error becomes a fault classification; every caller in this
// package goes through it rather than inspecting err text itself.
func ClassifyFault(err error) SessionFault {
	switch {
	case err == nil:
		return FaultNone
	case errors.Is(err, ErrEnginePipelineCrash):
		return FaultPipelineCrash
	case errors.Is(err, ErrEngineFreeze):
		return FaultFreeze
	case errors.Is(err, ErrEngineDecodeFailure):
		return FaultDecodeFailure
	case errors.Is(err, ErrEngineMediaDisappeared):
		return FaultMediaDisappeared
	case errors.Is(err, ErrEngineRouteChanged):
		return FaultRouteChanged
	case errors.Is(err, ErrEngineTimingAuthorityLost):
		return FaultTimingAuthorityLost
	default:
		return FaultOther
	}
}

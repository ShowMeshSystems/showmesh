package audio

import (
	"fmt"
	"regexp"
)

// LTCFrameRate is the closed vocabulary this project authorizes: exactly
// the four rates Resolume's Preferences > Audio timecode input supports.
// A rate outside this set is refused at configuration time (audio.settings
// and any per-node override), never discovered by a receiver failing to
// lock.
type LTCFrameRate string

const (
	LTCFrameRate24   LTCFrameRate = "24"
	LTCFrameRate25   LTCFrameRate = "25"
	LTCFrameRate2997 LTCFrameRate = "29.97"
	LTCFrameRate30   LTCFrameRate = "30"
)

var ltcFrameRates = map[string]struct{}{
	string(LTCFrameRate24): {}, string(LTCFrameRate25): {},
	string(LTCFrameRate2997): {}, string(LTCFrameRate30): {},
}

// Validate reports whether r is one of the four reserved rates.
func (r LTCFrameRate) Validate() error {
	return closedSet("audio.LTCFrameRate", string(r), ltcFrameRates)
}

// ltcTimecodePattern is HH:MM:SS:FF, the SMPTETimecode shape the owner's
// own examples use (song 1 at 00:00:00:00, song 2 at 01:00:00:00). Frame
// count validity against a given rate is not checked here — the caller
// that actually starts a generator against a resolved rate is where that
// bound is meaningful; this type only enforces the wire shape.
var ltcTimecodePattern = regexp.MustCompile(`^([0-9]{2}):([0-9]{2}):([0-9]{2}):([0-9]{2})$`)

// LTCTimecode is an HH:MM:SS:FF SMPTE timecode string: a session's LTC
// start offset, or a generator's currently-reported position.
//
// This project ships NON-DROP-FRAME timecode at every rate, including
// 29.97: RES-001 §9 leaves Resolume's drop-frame expectation at 29.97
// explicitly unresearched ("still open"), and this answers the question
// rather than leaving it ambiguous — ship non-drop and record the
// limitation here rather than silently picking one. A receiver expecting
// drop-frame at 29.97 will drift against this timecode over a
// long-running session; that is the recorded limitation, not a defect
// discovered later.
type LTCTimecode string

// Validate reports whether t is a well-formed HH:MM:SS:FF string with
// each field in its natural range (hours 0-23, minutes/seconds 0-59,
// frames 0-99 — the frame ceiling for a given rate is a generator-time
// concern, not this type's).
func (t LTCTimecode) Validate() error {
	m := ltcTimecodePattern.FindStringSubmatch(string(t))
	if m == nil {
		return fmt.Errorf("%w: audio.LTCTimecode %q is not HH:MM:SS:FF", ErrUnknownVocabularyMember, t)
	}
	hh, mm, ss := atoi2(m[1]), atoi2(m[2]), atoi2(m[3])
	if hh > 23 || mm > 59 || ss > 59 {
		return fmt.Errorf("%w: audio.LTCTimecode %q has a field out of range", ErrUnknownVocabularyMember, t)
	}
	return nil
}

// atoi2 parses a two-digit numeric field already matched by
// [ltcTimecodePattern] — never called on anything else, so the error
// return of strconv.Atoi is unreachable and deliberately discarded.
func atoi2(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

package capability

import (
	"errors"
	"fmt"
	"regexp"
)

// ID is a namespaced, dot-separated capability identifier, such as
// "matrix.render" or "transport.ndi.send". See the package doc comment for
// why validity here means syntax only, never vocabulary membership.
type ID string

// idPattern is the syntax ADR-002 and ARCHITECTURE section 6's YAML example
// require: two or more lowercase, dot-separated segments, each starting
// with a letter and containing only lowercase letters and digits after
// that. This single pattern is what rejects every case the spec calls out
// by name: an empty string (no segment to match), uppercase (outside the
// character class), a leading or trailing dot (a dot cannot start or end a
// segment), a doubled dot (an empty segment between two dots does not
// match), and any MQTT topic metacharacter such as '+', '#', or '/' (simply
// not in the character class).
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)+$`)

// ErrInvalidID is wrapped by every error [ID.Validate] returns.
var ErrInvalidID = errors.New("capability: invalid capability ID syntax")

// Validate reports whether id is syntactically well-formed: two or more
// lowercase, dot-separated segments, each starting with a letter. It does
// NOT check whether id is part of the known vocabulary; an unknown ID is
// syntactically valid and must be accepted. See the package doc comment.
func (id ID) Validate() error {
	if !idPattern.MatchString(string(id)) {
		return fmt.Errorf("%w: %q: must be two or more lowercase dot-separated segments, each starting with a letter (e.g. \"matrix.render\")",
			ErrInvalidID, string(id))
	}
	return nil
}

// knownIDs is the initial vocabulary from ARCHITECTURE section 6, verbatim,
// as it stands 2026-08-10. Membership here is informational only; see
// [ID.IsKnown].
var knownIDs = map[ID]struct{}{
	"matrix.render":         {},
	"video.playback":        {},
	"media.cache":           {},
	"display.hdmi":          {},
	"transport.ndi.send":    {},
	"transport.ndi.receive": {},
	"audio.engine":          {},
	"audio.output.local":    {},
	"audio.output.fm":       {},
	"audio.output.ltc":      {},
	"audio.output.dante":    {},
	"timecode.ltc.observe":  {},
	"process.supervise":     {},

	// SM-201: an audio output's granular playback abilities, reserved
	// 2026-08-30 so ValidateItemTransitionSupport and the
	// resting:background-audio-output-capabilities readiness check have
	// real identifiers to ask a node's Hello advertisement about instead
	// of none at all. See internal/agent/audiocapabilities.go for which
	// of these a node actually advertises and why; membership here is
	// informational only, per this map's own doc comment.
	"audio.playback.background":   {},
	"audio.playback.announcement": {},
	"audio.playback.playlist":     {},
	"audio.playback.loop":         {},
	"audio.playback.gain":         {},
	"audio.playback.fade":         {},
	"audio.playback.seek":         {},
	"audio.playback.position":     {},
	"audio.mix.concurrent":        {},
	"audio.mix.duck":              {},
	"audio.mix.interrupt":         {},
	"audio.transition.sequential": {},
	"audio.transition.gapless":    {},
	"audio.transition.crossfade":  {},
}

// withdrawnIDs are identifiers ARCHITECTURE section 6 records as replaced:
// audio.output.* superseded audio.playback/audio.multichannel/audio.dante,
// and timecode.ltc.observe superseded timecode.ltc.generate. Nothing
// advertises these as of 2026-08-10. They remain syntactically valid ([ID.
// Validate] does not reject them) so that an agent that still advertises
// one fails informatively (see [ID.IsWithdrawn]) rather than being silently
// dropped by the model layer.
var withdrawnIDs = map[ID]struct{}{
	"audio.playback":        {},
	"audio.multichannel":    {},
	"audio.dante":           {},
	"timecode.ltc.generate": {},
}

// IsKnown reports whether id is in the initial vocabulary ARCHITECTURE
// section 6 documents, as an aid for logging, UI grouping, and the like.
// This is informational only: it MUST NOT be used to reject a capability.
// An unknown ID is valid per ADR-002 (hardware support must expand without
// changing the core object model) and OPERATOR-UI requires an unrecognized
// capability to render as a generic panel, not fail the view.
func (id ID) IsKnown() bool {
	_, ok := knownIDs[id]
	return ok
}

// IsWithdrawn reports whether id is one of the identifiers ARCHITECTURE
// section 6 records as withdrawn (replaced by a newer identifier; see the
// withdrawnIDs doc comment). A withdrawn ID is still syntactically valid
// and [ID.Validate] does not reject it; this method exists so a caller
// wiring up an agent's advertised capabilities (Task C) can produce a clear
// diagnostic for a node still advertising a withdrawn ID, instead of a
// silent mystery about why it does not behave like the current vocabulary.
func (id ID) IsWithdrawn() bool {
	_, ok := withdrawnIDs[id]
	return ok
}

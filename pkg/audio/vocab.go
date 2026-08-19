package audio

import (
	"errors"
	"fmt"
)

// closedSet validates that v is a member of valid, returning an error
// naming kind and v otherwise.
func closedSet(kind, v string, valid map[string]struct{}) error {
	if _, ok := valid[v]; !ok {
		return fmt.Errorf("%w: %s %q", ErrUnknownVocabularyMember, kind, v)
	}
	return nil
}

// ErrUnknownVocabularyMember is wrapped by every closed vocabulary's
// Validate method in this package.
var ErrUnknownVocabularyMember = errors.New("audio: value is not a member of this closed vocabulary")

// SourceRole is a session's source type (AUDIO-ENGINE section 3).
type SourceRole string

const (
	SourceRoleShow         SourceRole = "show"
	SourceRoleBackground   SourceRole = "background"
	SourceRoleAnnouncement SourceRole = "announcement"
	SourceRoleManual       SourceRole = "manual"
)

var sourceRoles = map[string]struct{}{
	string(SourceRoleShow):         {},
	string(SourceRoleBackground):   {},
	string(SourceRoleAnnouncement): {},
	string(SourceRoleManual):       {},
}

// Validate reports whether r is one of the four reserved source roles.
func (r SourceRole) Validate() error {
	return closedSet("audio.SourceRole", string(r), sourceRoles)
}

// State is a session's semantic playback state (AUDIO-ENGINE section 3).
// Unknown means observation is absent or stale and must never be treated
// as Stopped, Completed, or Ready. Completed (natural end) and Stopped
// (commanded) are permanently distinct — Track F anchors transitions on
// Completed.
type State string

const (
	StatePreparing State = "preparing"
	StateReady     State = "ready"
	StatePlaying   State = "playing"
	StatePaused    State = "paused"
	StateStopping  State = "stopping"
	StateStopped   State = "stopped"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateUnknown   State = "unknown"
)

var states = map[string]struct{}{
	string(StatePreparing): {}, string(StateReady): {}, string(StatePlaying): {},
	string(StatePaused): {}, string(StateStopping): {}, string(StateStopped): {},
	string(StateCompleted): {}, string(StateFailed): {}, string(StateUnknown): {},
}

// Validate reports whether s is one of the nine reserved session states.
func (s State) Validate() error {
	return closedSet("audio.State", string(s), states)
}

// RepeatMode is a playlist's repeat behavior (AUDIO-ENGINE section 3).
type RepeatMode string

const (
	RepeatNone     RepeatMode = "none"
	RepeatItem     RepeatMode = "item"
	RepeatPlaylist RepeatMode = "playlist"
)

var repeatModes = map[string]struct{}{
	string(RepeatNone): {}, string(RepeatItem): {}, string(RepeatPlaylist): {},
}

// Validate reports whether m is one of the three reserved repeat modes.
func (m RepeatMode) Validate() error {
	return closedSet("audio.RepeatMode", string(m), repeatModes)
}

// ItemTransition is the requested transition between playlist items
// (AUDIO-ENGINE section 3). Sequential promises no overlap and no gapless
// seam; Gapless and Crossfade require the selected output to confirm
// support — see [ValidateItemTransitionSupport].
type ItemTransition string

const (
	ItemTransitionSequential ItemTransition = "sequential"
	ItemTransitionGapless    ItemTransition = "gapless"
	ItemTransitionCrossfade  ItemTransition = "crossfade"
)

var itemTransitions = map[string]struct{}{
	string(ItemTransitionSequential): {}, string(ItemTransitionGapless): {}, string(ItemTransitionCrossfade): {},
}

// Validate reports whether t is one of the three reserved item
// transitions.
func (t ItemTransition) Validate() error {
	return closedSet("audio.ItemTransition", string(t), itemTransitions)
}

// ErrItemTransitionUnconfirmed is returned by
// [ValidateItemTransitionSupport] when a required Gapless or Crossfade
// transition is requested against an output that cannot confirm it.
var ErrItemTransitionUnconfirmed = errors.New("audio: requested item transition is not confirmed by the selected output")

// ValidateItemTransitionSupport refuses a required Gapless or Crossfade
// transition that the selected output cannot confirm, rather than
// silently approximating it as Sequential. Sequential always passes.
func ValidateItemTransitionSupport(t ItemTransition, outputConfirms bool) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if t == ItemTransitionSequential {
		return nil
	}
	if !outputConfirms {
		return fmt.Errorf("%w: %s", ErrItemTransitionUnconfirmed, t)
	}
	return nil
}

// MixPolicy is how a session's audio combines with lower- or
// higher-priority sessions (AUDIO-ENGINE section 9).
type MixPolicy string

const (
	MixPolicyMix         MixPolicy = "mix"
	MixPolicyDuck        MixPolicy = "duck"
	MixPolicyInterrupt   MixPolicy = "interrupt"
	MixPolicyUnsupported MixPolicy = "unsupported"
)

var mixPolicies = map[string]struct{}{
	string(MixPolicyMix): {}, string(MixPolicyDuck): {}, string(MixPolicyInterrupt): {}, string(MixPolicyUnsupported): {},
}

// Validate reports whether p is one of the four reserved mix policies.
func (p MixPolicy) Validate() error {
	return closedSet("audio.MixPolicy", string(p), mixPolicies)
}

// ResumePolicy is a session's behavior after a discontinuity (AUDIO-ENGINE
// section 3).
type ResumePolicy string

const (
	ResumePolicyResume  ResumePolicy = "resume"
	ResumePolicyRestart ResumePolicy = "restart"
)

var resumePolicies = map[string]struct{}{
	string(ResumePolicyResume): {}, string(ResumePolicyRestart): {},
}

// Validate reports whether p is one of the two reserved resume policies.
func (p ResumePolicy) Validate() error {
	return closedSet("audio.ResumePolicy", string(p), resumePolicies)
}

// FadeCurve is a gain fade's shape. Only Linear ships until the C0a-1
// bench establishes what curves the engine actually produces; adding a
// member later is additive.
type FadeCurve string

const (
	FadeCurveLinear FadeCurve = "linear"
)

var fadeCurves = map[string]struct{}{
	string(FadeCurveLinear): {},
}

// Validate reports whether c is the one reserved fade curve.
func (c FadeCurve) Validate() error {
	return closedSet("audio.FadeCurve", string(c), fadeCurves)
}

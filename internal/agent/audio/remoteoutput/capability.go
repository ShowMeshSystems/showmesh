package remoteoutput

import (
	"errors"
	"fmt"
)

// Support is a destination's confirmed relationship to one capability.
// Unsupported and Unknown are deliberately distinct: Unknown means the
// adapter has no evidence either way and must never be resolved into an
// assumption that the capability works.
type Support string

const (
	SupportSupported   Support = "supported"
	SupportUnsupported Support = "unsupported"
	SupportUnknown     Support = "unknown"
)

var supportValues = map[Support]struct{}{
	SupportSupported: {}, SupportUnsupported: {}, SupportUnknown: {},
}

// ErrUnknownSupportValue is returned by [Support.Validate] for a value
// outside the three-member vocabulary.
var ErrUnknownSupportValue = errors.New("remoteoutput: support value is not a member of this closed vocabulary")

// Validate reports whether s is one of the three reserved support values.
func (s Support) Validate() error {
	if _, ok := supportValues[s]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSupportValue, s)
	}
	return nil
}

// ErrCapabilityNotConfirmed is returned by [Support.RequireSupported]
// when a capability is Unsupported or Unknown: a caller may not proceed
// as though an unconfirmed capability works.
var ErrCapabilityNotConfirmed = errors.New("remoteoutput: capability is not confirmed supported")

// RequireSupported returns nil only for [SupportSupported]; both
// Unsupported and Unknown are refused identically, because acting on an
// unconfirmed capability is exactly the assumption AUDIO-ENGINE section
// 8.1 forbids.
func (s Support) RequireSupported(capability string) error {
	if s == SupportSupported {
		return nil
	}
	return fmt.Errorf("%w: %s is %s", ErrCapabilityNotConfirmed, capability, s)
}

// Capabilities is one destination's honestly-declared capability
// profile, matching AUDIO-ENGINE section 8's example fields. A field
// left at its zero value is [SupportUnknown], never a silent false.
type Capabilities struct {
	SynchronizedMedia           Support
	PCMStream                   Support
	AdvanceProvisioning         Support
	ProvisioningAcknowledgement Support
	ReadinessObservation        Support
	Mixing                      Support
	Announcements               Support
	Ducking                     Support
	GainFades                   Support
	Looping                     Support
	Playlists                   Support
	Sequential                  Support
	Gapless                     Support
	Crossfade                   Support
	Seeking                     Support
	PositionReporting           Support

	// AcceptedFormats reports Support per format identifier (the
	// FPP-recognized-format L0 corpus). A format absent from this map is
	// [SupportUnknown] — see [Capabilities.Format].
	AcceptedFormats map[string]Support
}

// Format reports s's declared support for format, defaulting to
// [SupportUnknown] when the destination has never expressed an opinion
// rather than defaulting to Unsupported, which would claim evidence that
// does not exist.
func (c Capabilities) Format(format string) Support {
	if s, ok := c.AcceptedFormats[format]; ok {
		return s
	}
	return SupportUnknown
}

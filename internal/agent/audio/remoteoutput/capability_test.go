package remoteoutput

import (
	"errors"
	"testing"
)

func TestSupportValidate(t *testing.T) {
	for _, s := range []Support{SupportSupported, SupportUnsupported, SupportUnknown} {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%s): got %v, want nil", s, err)
		}
	}
	if err := Support("ready").Validate(); !errors.Is(err, ErrUnknownSupportValue) {
		t.Errorf("Validate(ready): got %v, want ErrUnknownSupportValue", err)
	}
}

func TestSupportRequireSupportedTreatsUnsupportedAndUnknownIdentically(t *testing.T) {
	if err := SupportSupported.RequireSupported("looping"); err != nil {
		t.Errorf("RequireSupported(Supported): got %v, want nil", err)
	}
	if err := SupportUnsupported.RequireSupported("looping"); !errors.Is(err, ErrCapabilityNotConfirmed) {
		t.Errorf("RequireSupported(Unsupported): got %v, want ErrCapabilityNotConfirmed", err)
	}
	if err := SupportUnknown.RequireSupported("looping"); !errors.Is(err, ErrCapabilityNotConfirmed) {
		t.Errorf("RequireSupported(Unknown): got %v, want ErrCapabilityNotConfirmed", err)
	}
}

func TestCapabilitiesFormatDefaultsUnknown(t *testing.T) {
	c := Capabilities{AcceptedFormats: map[string]Support{"mp3": SupportSupported}}
	if got := c.Format("mp3"); got != SupportSupported {
		t.Errorf("Format(mp3): got %s, want Supported", got)
	}
	if got := c.Format("flac"); got != SupportUnknown {
		t.Errorf("Format(flac) with no entry: got %s, want Unknown (never Unsupported by inference)", got)
	}
}

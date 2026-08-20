package gstengine

import (
	"errors"
	"testing"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

func baseValidConfig() Config {
	return Config{
		SinkFactory:     "fakesink",
		ProgramChannels: []int{1, 2},
		ChannelCount:    3,
		SampleRate:      44100,
		Resolve:         func(pkgaudio.MediaRef) (string, error) { return "", nil },
	}
}

func TestConfigValidateLTCChannelZeroIsFine(t *testing.T) {
	c := baseValidConfig()
	c.LTCChannel = 0
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestConfigValidateLTCChannelWithinRange(t *testing.T) {
	c := baseValidConfig()
	c.LTCChannel = 3
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestConfigValidateLTCChannelOutsideRange(t *testing.T) {
	c := baseValidConfig()
	c.LTCChannel = 4
	if err := c.Validate(); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("Validate: got %v, want ErrConfigInvalid", err)
	}
}

func TestConfigValidateLTCChannelCannotBeAProgramChannel(t *testing.T) {
	c := baseValidConfig()
	c.LTCChannel = 1
	if err := c.Validate(); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("Validate: got %v, want ErrConfigInvalid", err)
	}
}

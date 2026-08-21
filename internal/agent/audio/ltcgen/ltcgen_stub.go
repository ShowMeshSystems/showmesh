//go:build !cgo

package ltcgen

import (
	"fmt"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// SampleFormat mirrors the cgo build's PCM format so a caller can build
// caps without a build-tag switch, even though this build never produces
// samples.
const SampleFormat = "S16LE"

const unsupportedReason = "built without cgo: libltc is not linked into this binary"

// Encoder is a same-shaped, non-functional stand-in so this package
// always compiles under CGO_ENABLED=0.
type Encoder struct{}

// NewEncoder always fails: this build has no libltc linked in.
func NewEncoder(pkgaudio.LTCFrameRate, pkgaudio.LTCTimecode, int) (*Encoder, error) {
	return nil, fmt.Errorf("ltcgen: %s", unsupportedReason)
}

// NextFrame always fails: this build has no libltc linked in.
func (e *Encoder) NextFrame() ([]byte, pkgaudio.LTCTimecode, error) {
	return nil, "", fmt.Errorf("ltcgen: %s", unsupportedReason)
}

// Close is a no-op: this build never held a libltc encoder.
func (e *Encoder) Close() {}

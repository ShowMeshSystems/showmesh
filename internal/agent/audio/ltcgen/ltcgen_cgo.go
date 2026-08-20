//go:build cgo

// Package ltcgen wraps libltc's encoder over cgo, producing SMPTE Linear
// Timecode as PCM this project's GStreamer pipeline can carry on a
// discrete output channel.
package ltcgen

/*
#cgo pkg-config: ltc
#include <ltc.h>

// showmesh_ltc_force_nondrop clears the encoder's drop-frame bit and
// recomputes parity to match. ltc_encoder_create sets dfbit whenever fps
// is ~29.97, and this project ships non-drop timecode at every rate.
static void showmesh_ltc_force_nondrop(LTCEncoder *e, enum LTC_TV_STANDARD standard) {
	LTCFrame f;
	ltc_encoder_get_frame(e, &f);
	f.dfbit = 0;
	ltc_frame_set_parity(&f, standard);
	ltc_encoder_set_frame(e, &f);
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// SampleFormat is the PCM format every [Encoder.NextFrame] sample belongs
// to: signed 16-bit little-endian, mono. Exposed so a caller building
// GStreamer caps never duplicates this literal.
const SampleFormat = "S16LE"

// Encoder produces one LTC frame's worth of [SampleFormat] PCM per call to
// [Encoder.NextFrame], advancing its own timecode each time. Not safe for
// concurrent use.
type Encoder struct {
	enc *C.LTCEncoder
}

// NewEncoder creates an [Encoder] emitting rate at sampleHz, starting at
// start. It refuses a rate outside [pkgaudio.LTCFrameRate]'s closed set, a
// malformed start timecode, or a start frame not valid at rate; a libltc
// allocation failure is reported rather than left to panic.
func NewEncoder(rate pkgaudio.LTCFrameRate, start pkgaudio.LTCTimecode, sampleHz int) (*Encoder, error) {
	if err := rate.Validate(); err != nil {
		return nil, err
	}
	hh, mm, ss, ff, err := start.Fields()
	if err != nil {
		return nil, err
	}
	if _, err := start.FrameCount(rate); err != nil {
		return nil, err
	}
	if sampleHz <= 0 {
		return nil, fmt.Errorf("ltcgen: sample rate must be positive, got %d", sampleHz)
	}

	standard := tvStandard(rate)
	enc := C.ltc_encoder_create(C.double(sampleHz), C.double(rate.Rate()), standard, 0)
	if enc == nil {
		return nil, fmt.Errorf("ltcgen: libltc could not allocate an encoder")
	}
	C.showmesh_ltc_force_nondrop(enc, standard)

	e := &Encoder{enc: enc}
	e.setTimecode(hh, mm, ss, ff)
	return e, nil
}

func tvStandard(rate pkgaudio.LTCFrameRate) C.enum_LTC_TV_STANDARD {
	switch rate {
	case pkgaudio.LTCFrameRate24:
		return C.LTC_TV_FILM_24
	case pkgaudio.LTCFrameRate25:
		return C.LTC_TV_625_50
	default:
		return C.LTC_TV_525_60
	}
}

func (e *Encoder) setTimecode(hh, mm, ss, ff int) {
	var t C.SMPTETimecode
	t.hours = C.uchar(hh)
	t.mins = C.uchar(mm)
	t.secs = C.uchar(ss)
	t.frame = C.uchar(ff)
	C.ltc_encoder_set_timecode(e.enc, &t)
}

// NextFrame encodes the encoder's current timecode to PCM, returns that
// timecode alongside the samples it produced, and advances the encoder to
// the following frame. The sample count is not constant: at 29.97 fps
// libltc distributes a fractional sample count across frames, so callers
// must read it from the returned slice rather than assume a fixed size.
func (e *Encoder) NextFrame() ([]int16, pkgaudio.LTCTimecode, error) {
	if e.enc == nil {
		return nil, "", fmt.Errorf("ltcgen: encoder is closed")
	}

	var t C.SMPTETimecode
	C.ltc_encoder_get_timecode(e.enc, &t)
	tc := pkgaudio.LTCTimecode(fmt.Sprintf("%02d:%02d:%02d:%02d", int(t.hours), int(t.mins), int(t.secs), int(t.frame)))

	C.ltc_encoder_encode_frame(e.enc)

	var bufPtr *C.ltcsnd_sample_t
	n := C.ltc_encoder_get_bufferptr(e.enc, &bufPtr, 1)
	if n <= 0 {
		return nil, "", fmt.Errorf("ltcgen: libltc produced an empty frame buffer")
	}

	// libltc's encoded samples are unsigned 8-bit, 128 at silence; centre
	// and widen to signed 16-bit by shifting into the top byte.
	raw := unsafe.Slice((*byte)(unsafe.Pointer(bufPtr)), int(n))
	samples := make([]int16, len(raw))
	for i, b := range raw {
		samples[i] = int16(int(b)-128) << 8
	}

	C.ltc_encoder_inc_timecode(e.enc)
	return samples, tc, nil
}

// Close releases the underlying libltc encoder. Safe to call more than
// once.
func (e *Encoder) Close() {
	if e.enc != nil {
		C.ltc_encoder_free(e.enc)
		e.enc = nil
	}
}

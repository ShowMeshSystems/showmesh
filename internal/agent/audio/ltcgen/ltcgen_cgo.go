//go:build cgo

// Package ltcgen wraps libltc's encoder over cgo, producing SMPTE Linear
// Timecode as PCM this project's GStreamer pipeline can carry on a
// discrete output channel.
package ltcgen

/*
#cgo pkg-config: ltc
#include <ltc.h>
#include <stdint.h>

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

// showmesh_ltc_frame_s16le encodes e's current frame and widens libltc's
// unsigned 8-bit samples to signed 16-bit little-endian PCM directly into
// out, returning the sample count written (never more than out_cap).
static size_t showmesh_ltc_frame_s16le(LTCEncoder *e, int16_t *out, size_t out_cap) {
	ltc_encoder_encode_frame(e);
	ltcsnd_sample_t *buf;
	int n = ltc_encoder_get_bufferptr(e, &buf, 1);
	if (n <= 0) {
		return 0;
	}
	size_t count = (size_t)n;
	if (count > out_cap) {
		count = out_cap;
	}
	for (size_t i = 0; i < count; i++) {
		out[i] = (int16_t)(((int)buf[i] - 128) << 8);
	}
	return count;
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
	// buf is scratch space [Encoder.NextFrame] hands to C to fill, sized
	// once for the slowest supported frame rate (the most samples per
	// frame) at construction.
	buf []int16
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

	// 24 fps is the slowest rate this project authorizes and so carries
	// the most samples per frame; the margin covers libltc's rounding at
	// non-integer sample-rate/fps ratios.
	maxSamples := sampleHz/24 + 64
	e := &Encoder{enc: enc, buf: make([]int16, maxSamples)}
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

// NextFrame encodes the encoder's current timecode to [SampleFormat] PCM,
// returns that timecode alongside the bytes produced, and advances the
// encoder to the following frame. libltc does the amplitude conversion in
// C (ADR-042 decision 2); this only copies the result out of scratch space
// it owns, so the returned slice is invalidated by the next call. The byte
// count is not constant: at 29.97 fps libltc distributes a fractional
// sample count across frames, so callers must read it from the returned
// slice rather than assume a fixed size.
func (e *Encoder) NextFrame() ([]byte, pkgaudio.LTCTimecode, error) {
	if e.enc == nil {
		return nil, "", fmt.Errorf("ltcgen: encoder is closed")
	}

	var t C.SMPTETimecode
	C.ltc_encoder_get_timecode(e.enc, &t)
	tc := pkgaudio.LTCTimecode(fmt.Sprintf("%02d:%02d:%02d:%02d", int(t.hours), int(t.mins), int(t.secs), int(t.frame)))

	n := C.showmesh_ltc_frame_s16le(e.enc, (*C.int16_t)(unsafe.Pointer(&e.buf[0])), C.size_t(len(e.buf)))
	if n == 0 {
		return nil, "", fmt.Errorf("ltcgen: libltc produced an empty frame buffer")
	}
	C.ltc_encoder_inc_timecode(e.enc)

	raw := unsafe.Slice((*byte)(unsafe.Pointer(&e.buf[0])), int(n)*2)
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, tc, nil
}

// Close releases the underlying libltc encoder. Safe to call more than
// once.
func (e *Encoder) Close() {
	if e.enc != nil {
		C.ltc_encoder_free(e.enc)
		e.enc = nil
	}
}

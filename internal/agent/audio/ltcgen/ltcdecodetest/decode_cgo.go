//go:build cgo

// Package ltcdecodetest wraps libltc's decoder for [ltcgen]'s own round-trip
// tests. It exists as a separate package, rather than living in
// ltcgen_test.go, because this toolchain refuses cgo directly in a
// _test.go file.
package ltcdecodetest

/*
#cgo pkg-config: ltc
#include <ltc.h>

// cgo cannot address an individual C bitfield member, so dfbit is read
// through this helper instead.
static int showmesh_ltc_dfbit(LTCFrame *f) {
	return f->dfbit;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Frame is one LTC frame libltc's decoder recovered from an audio signal.
// OffStart is the sample offset, within the buffer passed to [Decode], of
// this frame's first transition -- what a caller needs to place the frame
// on a timeline rather than just knowing its decoded value.
type Frame struct {
	Hours, Mins, Secs, Frame int
	DropFrame                bool
	OffStart                 int64
}

// Decode feeds samples (S16LE mono) to a fresh libltc decoder and returns
// every frame it recovered, in order. framesPerVideoFrame is libltc's
// initial-lock hint (sample rate / fps).
func Decode(samples []int16, framesPerVideoFrame int) ([]Frame, error) {
	if len(samples) == 0 {
		return nil, fmt.Errorf("ltcdecodetest: no samples to decode")
	}
	dec := C.ltc_decoder_create(C.int(framesPerVideoFrame), 1000)
	if dec == nil {
		return nil, fmt.Errorf("ltcdecodetest: libltc could not allocate a decoder")
	}
	defer C.ltc_decoder_free(dec)

	buf := (*C.short)(unsafe.Pointer(&samples[0]))
	C.ltc_decoder_write_s16(dec, buf, C.size_t(len(samples)), 0)

	var frames []Frame
	var ext C.LTCFrameExt
	for C.ltc_decoder_read(dec, &ext) != 0 {
		var stime C.SMPTETimecode
		C.ltc_frame_to_time(&stime, &ext.ltc, 0)
		frames = append(frames, Frame{
			Hours:     int(stime.hours),
			Mins:      int(stime.mins),
			Secs:      int(stime.secs),
			Frame:     int(stime.frame),
			DropFrame: C.showmesh_ltc_dfbit(&ext.ltc) != 0,
			OffStart:  int64(ext.off_start),
		})
	}
	return frames, nil
}

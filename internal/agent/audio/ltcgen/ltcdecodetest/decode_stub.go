//go:build !cgo

package ltcdecodetest

import "fmt"

// Frame is one LTC frame libltc's decoder recovered from an audio signal.
type Frame struct {
	Hours, Mins, Secs, Frame int
	DropFrame                bool
	OffStart                 int64
}

// Decode always fails: this build has no libltc linked in.
func Decode(samples []int16, framesPerVideoFrame int) ([]Frame, error) {
	return nil, fmt.Errorf("ltcdecodetest: built without cgo: libltc is not linked into this binary")
}

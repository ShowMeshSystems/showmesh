// Generates small uncompressed FSEQ v2 files whose every frame is one
// solid RGB colour, so a rendered frame identifies its source file.
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"

	"github.com/showmeshsystems/showmesh/pkg/fseq"
)

const headerSize = 32

func build(channelCount, frameCount uint32, stepTimeMS byte, r, g, b byte) []byte {
	fixedHeaderLen := headerSize
	chanDataOffset := fixedHeaderLen
	pad := (4 - chanDataOffset%4) % 4
	chanDataOffset += pad

	hdr := make([]byte, headerSize)
	copy(hdr[0:4], "PSEQ")
	binary.LittleEndian.PutUint16(hdr[4:6], uint16(chanDataOffset))
	hdr[6] = 2
	hdr[7] = 2
	binary.LittleEndian.PutUint16(hdr[8:10], uint16(fixedHeaderLen))
	binary.LittleEndian.PutUint32(hdr[10:14], channelCount)
	binary.LittleEndian.PutUint32(hdr[14:18], frameCount)
	hdr[18] = stepTimeMS
	hdr[19] = 0
	hdr[20] = 0
	hdr[21] = 0
	hdr[22] = 0
	hdr[23] = 0
	binary.LittleEndian.PutUint64(hdr[24:32], 1760000000000000)

	out := append([]byte{}, hdr...)
	out = append(out, make([]byte, pad)...)

	frame := make([]byte, channelCount)
	for i := uint32(0); i < channelCount; i++ {
		switch i % 3 {
		case 0:
			frame[i] = r
		case 1:
			frame[i] = g
		default:
			frame[i] = b
		}
	}
	for f := uint32(0); f < frameCount; f++ {
		out = append(out, frame...)
	}
	return out
}

func main() {
	if len(os.Args) != 8 {
		fmt.Fprintln(os.Stderr, "usage: genfseq <path> <channels> <frames> <stepMS> <r> <g> <b>")
		os.Exit(2)
	}
	atoi := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			panic(err)
		}
		return n
	}
	path := os.Args[1]
	data := build(uint32(atoi(os.Args[2])), uint32(atoi(os.Args[3])), byte(atoi(os.Args[4])),
		byte(atoi(os.Args[5])), byte(atoi(os.Args[6])), byte(atoi(os.Args[7])))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		panic(err)
	}

	f, err := fseq.Open(path)
	if err != nil {
		panic(fmt.Sprintf("verify %s: %v", path, err))
	}
	defer f.Close()
	dst := make([]byte, 6)
	if err := f.ChannelRange(f.FrameCount()-1, 0, 6, dst); err != nil {
		panic(err)
	}
	fmt.Printf("%s frames=%d channels=%d stepMS=%d lastFrameFirst6=%v\n",
		path, f.FrameCount(), f.ChannelCount(), f.StepTimeMS(), dst)
}

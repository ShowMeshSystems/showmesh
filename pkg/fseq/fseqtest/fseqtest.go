// Package fseqtest builds valid FSEQ v2 bytes for tests outside package
// fseq, which cannot reach that package's own fixtures_test.go builders.
//
// This exists because the repository's .gitignore refuses every *.fseq
// (real show content is someone's authored work and describes a physical
// installation channel by channel, and the backstop is deliberately
// blanket), so there is no committed fixture file any test can open. Bytes
// have to be generated in memory.
//
// Deliberately narrow: dense, uncompressed, no sparse ranges and no
// variable headers. Package fseq's own fixtures cover the compressed,
// sparse and padded-variable-header shapes; anything needing those belongs
// in a test inside that package rather than here.
package fseqtest

import "encoding/binary"

// headerSize is the FSEQ v2 fixed header length in bytes.
const headerSize = 32

// FrameByte is the value Build writes at the given frame and 0-based
// channel: (frame*7 + channel) & 0xFF. Exported so a caller can assert
// extracted content byte for byte without restating the rule.
func FrameByte(frame, channel int) byte {
	return byte(frame*7 + channel)
}

// Build returns a dense, uncompressed FSEQ v2 file carrying channelCount
// channels across frameCount frames at stepTimeMS milliseconds per frame,
// filled by [FrameByte].
func Build(channelCount, frameCount uint32, stepTimeMS byte) []byte {
	// Uncompressed files carry no block table, and with no sparse ranges
	// and no variable headers the channel data starts immediately after
	// the fixed header, which is already a multiple of 4 (no padding).
	const numBlocks, numSparse = 0, 0
	fixedHeaderLen := headerSize
	chanDataOffset := fixedHeaderLen

	hdr := make([]byte, headerSize)
	copy(hdr[0:4], "PSEQ")
	binary.LittleEndian.PutUint16(hdr[4:6], uint16(chanDataOffset))
	hdr[6] = 2 // minor version
	hdr[7] = 2 // major version
	binary.LittleEndian.PutUint16(hdr[8:10], uint16(fixedHeaderLen))
	binary.LittleEndian.PutUint32(hdr[10:14], channelCount)
	binary.LittleEndian.PutUint32(hdr[14:18], frameCount)
	hdr[18] = stepTimeMS
	hdr[19] = 0
	// High nibble carries numBlocks' upper bits; the low nibble is the
	// compression type, 0 for none.
	hdr[20] = byte((numBlocks >> 4) & 0xF0)
	hdr[21] = byte(numBlocks & 0xFF)
	hdr[22] = byte(numSparse)
	hdr[23] = 0
	binary.LittleEndian.PutUint64(hdr[24:32], 1760000000000000)

	out := make([]byte, 0, headerSize+int(channelCount)*int(frameCount))
	out = append(out, hdr...)
	for f := uint32(0); f < frameCount; f++ {
		for c := uint32(0); c < channelCount; c++ {
			out = append(out, FrameByte(int(f), int(c)))
		}
	}
	return out
}

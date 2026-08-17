package fseq

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// maliciousZstdFrame builds the minimal legal zstd frame header (magic,
// descriptor, a 1 KiB window descriptor, 8-byte frame content size)
// declaring declaredSize, with no actual compressed block data. The window
// is deliberately tiny and independent of declaredSize so this exercises
// klauspost/compress's Frame_Content_Size-vs-WithDecoderMaxMemory check
// specifically (the one that gates the pre-allocation) rather than its
// separate, always-on window-size check, which a naive single-segment
// frame would trip first and mask the bug this test targets.
func maliciousZstdFrame(declaredSize uint64) []byte {
	const zstdMagic = 0xFD2FB528
	frame := make([]byte, 4+1+1+8)
	binary.LittleEndian.PutUint32(frame[0:4], zstdMagic)
	frame[4] = 0xC0 // Single_Segment=0, FCS_Field_Size selector=3 (8 bytes)
	frame[5] = 0x00 // Window_Descriptor: windowLog=10 => 1 KiB window
	binary.LittleEndian.PutUint64(frame[6:14], declaredSize)
	return frame
}

// TestOpenZstdBoundsDecoderToDeclaredBlockSize is the regression test for
// F4: a block whose zstd frame header declares a FrameContentSize far
// larger than this file's own block table implies must be refused with a
// typed error, never attempted as an allocation. Before the fix,
// zstd.NewReader(nil) used klauspost/compress's 64 GiB default and
// DecodeAll pre-allocated the declared size before this package's own
// post-decode size check ever ran.
func TestOpenZstdBoundsDecoderToDeclaredBlockSize(t *testing.T) {
	const channelCount = 4
	const frameCount = 1

	malicious := maliciousZstdFrame(8 << 30) // declares 8 GiB

	hdr := make([]byte, headerSize)
	copy(hdr[0:4], "PSEQ")
	numBlocks := 1
	fixedHeaderLen := headerSize + numBlocks*8
	chanDataOffset := fixedHeaderLen
	binary.LittleEndian.PutUint16(hdr[4:6], uint16(chanDataOffset))
	hdr[6] = 2 // minor
	hdr[7] = 2 // major
	binary.LittleEndian.PutUint16(hdr[8:10], uint16(fixedHeaderLen))
	binary.LittleEndian.PutUint32(hdr[10:14], channelCount)
	binary.LittleEndian.PutUint32(hdr[14:18], frameCount)
	hdr[18] = 25 // stepTimeMS
	hdr[19] = 0
	hdr[20] = byte((numBlocks>>4)&0xF0) | 1 // compression = zstd
	hdr[21] = byte(numBlocks & 0xFF)
	hdr[22] = 0 // numSparse
	hdr[23] = 0
	binary.LittleEndian.PutUint64(hdr[24:32], 1760000000000000)

	blockEntry := make([]byte, 8)
	binary.LittleEndian.PutUint32(blockEntry[0:4], 0) // firstFrame
	binary.LittleEndian.PutUint32(blockEntry[4:8], uint32(len(malicious)))

	data := append([]byte{}, hdr...)
	data = append(data, blockEntry...)
	data = append(data, malicious...)

	path := filepath.Join(t.TempDir(), "malicious.fseq")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v (a crafted block should not fail Open itself, only a later decode)", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	dst := make([]byte, channelCount)
	err = f.ChannelRange(0, 0, channelCount, dst)
	if err == nil {
		t.Fatalf("ChannelRange succeeded against a block declaring an 8 GiB frame content size, want a typed refusal")
	}
	// This is the specific mechanism the fix relies on: decodeBlock peeks
	// the frame's declared FrameContentSize (via zstd.Header) and compares
	// it against the exact size this block's own frame span implies,
	// before ever calling DecodeAll. Without that check, this frame's 8 GiB
	// declaration clears zstd's own 64 GiB default decoder limit, so
	// DecodeAll instead proceeds to allocate an 8 GiB buffer and only then
	// fails on the frame's missing block data — a different error, raised
	// after the allocation already happened.
	var malformed *ErrMalformed
	if !errors.As(err, &malformed) {
		t.Fatalf("ChannelRange error = %v (%T), want *ErrMalformed from the pre-decode FrameContentSize check", err, err)
	}
	if !strings.Contains(malformed.Reason, "declares a decompressed size") {
		t.Fatalf("ErrMalformed.Reason = %q, want it to name the declared-vs-expected size mismatch caught before decoding", malformed.Reason)
	}
}

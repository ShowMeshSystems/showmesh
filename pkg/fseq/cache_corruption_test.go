package fseq

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// TestChannelRangeDoesNotReturnCorruptedCacheAfterFailedDecode is the
// regression test for F5. readCompressedFrame only updates f.cachedBlock on
// a SUCCESSFUL decodeBlock, but zstd.Decoder.DecodeAll appends into
// f.cachedData's existing backing array in place when capacity allows (the
// common case: consecutive blocks of a real file decode to the same size).
// So a failed decode of a NEW block can overwrite the bytes behind the OLD,
// still-"cached" block while f.cachedBlock keeps naming that old block —
// and a subsequent seek back to it skips decodeBlock entirely and returns
// the half-overwritten bytes as if nothing had gone wrong.
//
// This builds its own two-block file (rather than reusing fixtures_test.go's
// buildFixture) because that helper's synthetic content is a simple linear
// ramp, which zstd compresses to a couple hundred bytes and decodes back in
// one shot — a corruption there is rejected before anything is written, and
// never reproduces the bug. Real block content (and this test's) has enough
// entropy that a 200,000-byte block spans multiple internal zstd blocks
// (the format's 128 KiB block cap), so corrupting only the back half of
// block 1's compressed bytes lets DecodeAll write a real 128 KiB of output
// before failing on the next internal block — confirmed empirically before
// writing this test (klauspost/compress v1.19.2, scratch probe): the first
// internal block decodes and is appended, the second is rejected, and the
// shared destination buffer's earlier (block 0) bytes are left changed.
func TestChannelRangeDoesNotReturnCorruptedCacheAfterFailedDecode(t *testing.T) {
	const channelCount = 2000
	const framesPerBlock = 100 // 2000 * 100 = 200,000 bytes/block, > 128 KiB
	const frameCount = 2 * framesPerBlock

	rng := rand.New(rand.NewSource(20260817))
	block0Data := make([]byte, channelCount*framesPerBlock)
	rng.Read(block0Data)
	block1Data := make([]byte, channelCount*framesPerBlock)
	rng.Read(block1Data)

	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	c0 := enc.EncodeAll(block0Data, nil)
	c1 := enc.EncodeAll(block1Data, nil)
	_ = enc.Close()

	// Corrupt only the back half of block 1's compressed bytes, so its
	// frame header (and FrameContentSize) stays intact and the first
	// internal zstd block still decodes cleanly.
	corrupt := append([]byte(nil), c1...)
	for i := len(corrupt) / 2; i < len(corrupt); i++ {
		corrupt[i] ^= 0xFF
	}

	numBlocks := 2
	fixedHeaderLen := headerSize + numBlocks*blockEntrySize
	chanDataOffset := fixedHeaderLen // no padding needed, no sparse/var headers

	hdr := make([]byte, headerSize)
	copy(hdr[0:4], "PSEQ")
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

	block0Entry := make([]byte, 8)
	binary.LittleEndian.PutUint32(block0Entry[0:4], 0)
	binary.LittleEndian.PutUint32(block0Entry[4:8], uint32(len(c0)))
	block1Entry := make([]byte, 8)
	binary.LittleEndian.PutUint32(block1Entry[0:4], framesPerBlock)
	binary.LittleEndian.PutUint32(block1Entry[4:8], uint32(len(corrupt)))

	data := append([]byte{}, hdr...)
	data = append(data, block0Entry...)
	data = append(data, block1Entry...)
	data = append(data, c0...)
	data = append(data, corrupt...)

	path := filepath.Join(t.TempDir(), "corrupt.fseq")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()

	dst := make([]byte, channelCount)

	// Prime the cache with block 0's correct bytes.
	if err := f.ChannelRange(0, 0, channelCount, dst); err != nil {
		t.Fatalf("ChannelRange(frame 0) (priming read): %v", err)
	}
	want0 := append([]byte(nil), dst...)
	if !bytes.Equal(want0, block0Data[:channelCount]) {
		t.Fatalf("priming read did not return the expected block 0 content; test fixture is wrong")
	}

	// Force a decode of block 1, which must fail — this is the read that
	// can corrupt the shared cache buffer underneath block 0.
	if err := f.ChannelRange(framesPerBlock, 0, channelCount, dst); err == nil {
		t.Fatalf("ChannelRange(frame %d) into a corrupted block succeeded, want an error", framesPerBlock)
	}

	// Seek back to block 0. Before the fix this skips decodeBlock (bi ==
	// f.cachedBlock) and returns whatever is left in the shared buffer,
	// silently, with no error — exactly the "wrong pixels, no error"
	// failure mode this fix exists to close.
	if err := f.ChannelRange(0, 0, channelCount, dst); err != nil {
		t.Fatalf("ChannelRange(frame 0) (re-read after failed block 1 decode): %v", err)
	}
	if !bytes.Equal(dst, want0) {
		t.Fatalf("frame 0 bytes changed after an unrelated failed decode of block 1 (stale cachedBlock returned corrupted data)")
	}
}

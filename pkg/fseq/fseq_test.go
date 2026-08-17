package fseq

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// expectedByte is the deterministic fill formula fixtures_test.go's
// synthFrameBytes uses for every fixture: byte value at absolute channel
// c, frame f, is (f*7 + c) & 0xFF.
func expectedByte(frame int, channel uint32) byte {
	return byte(frame*7 + int(channel))
}

func TestSparseOffsetUncompressed(t *testing.T) {
	f := fixtureSparseOffsetUncompressed(t)

	if got, want := f.ChannelCount(), 7; got != want {
		t.Fatalf("ChannelCount() = %d, want %d", got, want)
	}
	if got, want := f.FrameCount(), 3; got != want {
		t.Fatalf("FrameCount() = %d, want %d", got, want)
	}
	if got, want := f.StepTimeMS(), byte(25); got != want {
		t.Fatalf("StepTimeMS() = %d, want %d", got, want)
	}
	if got, want := f.Compression(), CompressionNone; got != want {
		t.Fatalf("Compression() = %s, want %s", got, want)
	}
	ranges := f.SparseRanges()
	if len(ranges) != 1 || ranges[0] != (SparseRange{Start: 100, Length: 7}) {
		t.Fatalf("SparseRanges() = %v, want [{100 7}]", ranges)
	}
	if got, want := f.MaxChannel(), uint32(107); got != want {
		t.Fatalf("MaxChannel() = %d, want %d", got, want)
	}

	dst := make([]byte, 7)
	if err := f.ChannelRange(1, 100, 7, dst); err != nil {
		t.Fatalf("ChannelRange: %v", err)
	}
	for i, b := range dst {
		if want := expectedByte(1, uint32(100+i)); b != want {
			t.Fatalf("byte %d = %d, want %d", i, b, want)
		}
	}
}

// TestSparseOffsetUncompressed_PartialCoverageRefused claims that a
// request straddling a covered range and uncovered channels is refused
// rather than partially filled.
func TestSparseOffsetUncompressed_PartialCoverageRefused(t *testing.T) {
	f := fixtureSparseOffsetUncompressed(t)

	cases := []struct {
		name  string
		start int
		count int
	}{
		{"starts one channel before the covered range", 99, 7},
		{"ends one channel past the covered range", 105, 5},
		{"entirely outside the covered range", 0, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := make([]byte, tc.count)
			err := f.ChannelRange(0, tc.start, tc.count, dst)
			var notCovered *ErrChannelRangeNotCovered
			if !errors.As(err, &notCovered) {
				t.Fatalf("ChannelRange(%d,%d) = %v, want *ErrChannelRangeNotCovered", tc.start, tc.count, err)
			}
			for _, b := range dst {
				if b != 0 {
					t.Fatalf("dst was written to on a refused read: %v", dst)
				}
			}
		})
	}
}

// TestSparseMultiRange_TableOrderNotSorted uses a fixture whose sparse
// table lists a higher-numbered range before a lower-numbered one, which
// RES-017 §6 says the format does not forbid. A reader that assumes
// ascending order would mis-walk this file.
func TestSparseMultiRange_TableOrderNotSorted(t *testing.T) {
	f := fixtureSparseMultiRange(t)

	ranges := f.SparseRanges()
	want := []SparseRange{{Start: 50, Length: 5}, {Start: 10, Length: 3}}
	if len(ranges) != 2 || ranges[0] != want[0] || ranges[1] != want[1] {
		t.Fatalf("SparseRanges() = %v, want %v (table order preserved)", ranges, want)
	}

	// Both individual ranges are readable.
	dst := make([]byte, 3)
	if err := f.ChannelRange(2, 10, 3, dst); err != nil {
		t.Fatalf("ChannelRange(range 2): %v", err)
	}
	for i, b := range dst {
		if want := expectedByte(2, uint32(10+i)); b != want {
			t.Fatalf("range 2 byte %d = %d, want %d", i, b, want)
		}
	}
	dst2 := make([]byte, 5)
	if err := f.ChannelRange(2, 50, 5, dst2); err != nil {
		t.Fatalf("ChannelRange(range 1): %v", err)
	}
	for i, b := range dst2 {
		if want := expectedByte(2, uint32(50+i)); b != want {
			t.Fatalf("range 1 byte %d = %d, want %d", i, b, want)
		}
	}

	// A request spanning the gap between the two ranges (13..50) must be
	// refused, not zero-filled.
	dst3 := make([]byte, 40)
	err := f.ChannelRange(0, 12, 40, dst3)
	var notCovered *ErrChannelRangeNotCovered
	if !errors.As(err, &notCovered) {
		t.Fatalf("ChannelRange spanning the gap = %v, want *ErrChannelRangeNotCovered", err)
	}
}

// TestSparseOverlapBoundary claims that adjacent sparse ranges sharing a
// boundary channel are readable, not refused, AND that the shared channel
// resolves to range 1's (table order) byte rather than range 0's. This
// shape was found in a real file pulled from an FPP host's own
// sequences/ directory (not a hypothesised edge case): two ranges {10,20}
// and {29,15} both claim absolute channel 29. RES-017 §6's own inference
// that files from xLights' export path are sorted and disjoint does not
// hold for this file.
//
// The fixture packs range 0 as all 'A' and range 1 as all 'B', so the two
// candidate values for channel 29 disagree and this test can actually
// tell first-wins from last-wins apart — a fixture whose two ranges share
// one per-channel source value (as a real xLights file's do) cannot, and
// previously let this test pass under either resolution order.
func TestSparseOverlapBoundary(t *testing.T) {
	f := fixtureSparseOverlapBoundary(t)
	ranges := f.SparseRanges()
	if len(ranges) != 2 || ranges[0].Start+ranges[0].Length-1 != ranges[1].Start {
		t.Fatalf("fixture ranges = %v, want overlapping at one boundary channel", ranges)
	}

	// The full union [10, 44) must be readable as one request, spanning
	// both ranges and their shared channel (range 0 covers [10,30),
	// range 1 covers [29,44), overlapping at channel 29): 19 'A's for
	// channels 10..28, then 15 'B's for channels 29..43 — channel 29
	// resolves to range 1's byte, not range 0's.
	want := append(bytesOf('A', 19), bytesOf('B', 15)...)
	dst := make([]byte, 34)
	if err := f.ChannelRange(1, 10, 34, dst); err != nil {
		t.Fatalf("ChannelRange across the overlap: %v", err)
	}
	if string(dst) != string(want) {
		t.Fatalf("ChannelRange(10,34) = %q, want %q (channel 29 must resolve to range 1's 'B', last in table order)", dst, want)
	}

	// The shared boundary channel alone, requested on its own, must also
	// resolve to range 1's byte rather than being treated as ambiguous.
	one := make([]byte, 1)
	if err := f.ChannelRange(1, 29, 1, one); err != nil {
		t.Fatalf("ChannelRange(boundary channel alone): %v", err)
	}
	if one[0] != 'B' {
		t.Fatalf("boundary channel = %q, want 'B' (range 1, last in table order)", one[0])
	}
}

// TestMultiblock_SequentialAndRandomAccess claims that block-cache reuse
// on sequential frame access, and cache invalidation on random access,
// both produce correct data — not just fast data.
func TestMultiblock_SequentialAndRandomAccess(t *testing.T) {
	f := fixtureMultiblock(t)
	if got, want := f.FrameCount(), 6; got != want {
		t.Fatalf("FrameCount() = %d, want %d", got, want)
	}
	dst := make([]byte, f.ChannelCount())

	checkFrame := func(frame int) {
		t.Helper()
		if err := f.ChannelRange(frame, 0, f.ChannelCount(), dst); err != nil {
			t.Fatalf("ChannelRange(frame %d): %v", frame, err)
		}
		for i, b := range dst {
			if want := expectedByte(frame, uint32(i)); b != want {
				t.Fatalf("frame %d byte %d = %d, want %d", frame, i, b, want)
			}
		}
	}

	// Sequential, exercising the block cache across a block boundary
	// (framesPerBlock=3, 6 frames => 2 blocks).
	for frame := 0; frame < 6; frame++ {
		checkFrame(frame)
	}
	// Random access forcing repeated cache invalidation.
	for _, frame := range []int{5, 0, 3, 1, 5, 2} {
		checkFrame(frame)
	}
}

func TestShortFinalBlock(t *testing.T) {
	f := fixtureShortFinalBlock(t)
	if got, want := f.FrameCount(), 5; got != want {
		t.Fatalf("FrameCount() = %d, want %d", got, want)
	}
	dst := make([]byte, f.ChannelCount())

	// framesPerBlock=4 with 5 frames: block 0 holds frames 0-3, block 1
	// holds only frame 4. Reading frame 4 must not read past the block's
	// real (short) length.
	if err := f.ChannelRange(4, 0, f.ChannelCount(), dst); err != nil {
		t.Fatalf("ChannelRange(frame 4): %v", err)
	}
	for i, b := range dst {
		if want := expectedByte(4, uint32(i)); b != want {
			t.Fatalf("frame 4 byte %d = %d, want %d", i, b, want)
		}
	}

	if err := f.ChannelRange(5, 0, 1, make([]byte, 1)); err == nil {
		t.Fatalf("ChannelRange(frame 5) on a 5-frame file: want error, got nil")
	} else {
		var oor *ErrFrameOutOfRange
		if !errors.As(err, &oor) {
			t.Fatalf("ChannelRange(frame 5) = %v, want *ErrFrameOutOfRange", err)
		}
	}
}

func TestWithVariableHeader(t *testing.T) {
	f := fixtureWithVariableHeader(t)
	if got, want := f.SequenceProducer(), "gen-fixture 1.0"; got != want {
		t.Fatalf("SequenceProducer() = %q, want %q", got, want)
	}
	dst := make([]byte, f.ChannelCount())
	if err := f.ChannelRange(2, 0, f.ChannelCount(), dst); err != nil {
		t.Fatalf("ChannelRange: %v", err)
	}
	for i, b := range dst {
		if want := expectedByte(2, uint32(i)); b != want {
			t.Fatalf("byte %d = %d, want %d", i, b, want)
		}
	}
}

func TestChannelRange_Validation(t *testing.T) {
	f := fixtureMultiblock(t)

	t.Run("frame negative", func(t *testing.T) {
		var oor *ErrFrameOutOfRange
		if err := f.ChannelRange(-1, 0, 1, make([]byte, 1)); !errors.As(err, &oor) {
			t.Fatalf("got %v, want *ErrFrameOutOfRange", err)
		}
	})
	t.Run("frame past end", func(t *testing.T) {
		var oor *ErrFrameOutOfRange
		if err := f.ChannelRange(6, 0, 1, make([]byte, 1)); !errors.As(err, &oor) {
			t.Fatalf("got %v, want *ErrFrameOutOfRange", err)
		}
	})
	t.Run("dst too small", func(t *testing.T) {
		if err := f.ChannelRange(0, 0, 5, make([]byte, 4)); err == nil {
			t.Fatalf("want error for undersized dst, got nil")
		}
	})
	t.Run("zero count is a no-op", func(t *testing.T) {
		if err := f.ChannelRange(0, 0, 0, nil); err != nil {
			t.Fatalf("zero-count ChannelRange: %v", err)
		}
	})
	t.Run("negative start", func(t *testing.T) {
		if err := f.ChannelRange(0, -1, 1, make([]byte, 1)); err == nil {
			t.Fatalf("want error for negative start, got nil")
		}
	})
}

// --- Malformed-header rejection tests, built by mutating a fixture's own
// bytes rather than the format's own writer, so they exercise Open's own
// validation independent of the builder.

func multiblockFixtureBytes(t *testing.T) []byte {
	t.Helper()
	return buildFixture(fixtureSpec{
		compression: 1, channelCount: 12, frameCount: 6, stepTimeMS: 25,
		framesPerBlock: 3, declareExtraZeroBlocks: 2,
	})
}

func sparseOffsetUncompressedFixtureBytes(t *testing.T) []byte {
	t.Helper()
	ranges := []SparseRange{{Start: 100, Length: 7}}
	return buildFixture(fixtureSpec{
		compression: 0, channelCount: sparseSum(ranges), frameCount: 3,
		stepTimeMS: 25, sparse: ranges,
	})
}

func openBytes(t *testing.T, data []byte) (*File, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mutated.fseq")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing mutated fixture: %v", err)
	}
	return Open(path)
}

func TestOpen_RejectsBadMagic(t *testing.T) {
	b := multiblockFixtureBytes(t)
	copy(b[0:4], "XXXX")
	if _, err := openBytes(t, b); !errors.Is(err, ErrNotFSEQ) {
		t.Fatalf("got %v, want ErrNotFSEQ", err)
	}
}

func TestOpen_RejectsESEQMagic(t *testing.T) {
	b := multiblockFixtureBytes(t)
	copy(b[0:4], "ESEQ")
	if _, err := openBytes(t, b); !errors.Is(err, ErrESEQUnsupported) {
		t.Fatalf("got %v, want ErrESEQUnsupported", err)
	}
}

func TestOpen_RejectsVersion1(t *testing.T) {
	b := multiblockFixtureBytes(t)
	b[7] = 1 // major version
	_, err := openBytes(t, b)
	var uv *ErrUnsupportedVersion
	if !errors.As(err, &uv) {
		t.Fatalf("got %v, want *ErrUnsupportedVersion", err)
	}
}

func TestOpen_RejectsZlib(t *testing.T) {
	b := sparseOffsetUncompressedFixtureBytes(t)
	// This fixture is uncompressed (no block table), so flipping the
	// compression nibble is a pure header mutation with no table to fix up.
	b[20] = (b[20] & 0xF0) | 0x02
	_, err := openBytes(t, b)
	var uc *ErrUnsupportedCompression
	if !errors.As(err, &uc) || uc.Type != CompressionZlib {
		t.Fatalf("got %v, want *ErrUnsupportedCompression{zlib}", err)
	}
}

func TestOpen_RejectsUnknownCompression(t *testing.T) {
	b := sparseOffsetUncompressedFixtureBytes(t)
	b[20] = (b[20] & 0xF0) | 0x07
	_, err := openBytes(t, b)
	var uc *ErrUnsupportedCompression
	if !errors.As(err, &uc) {
		t.Fatalf("got %v, want *ErrUnsupportedCompression", err)
	}
}

func TestOpen_RejectsHeaderLengthMismatch(t *testing.T) {
	b := multiblockFixtureBytes(t)
	declared := binary.LittleEndian.Uint16(b[8:10])
	binary.LittleEndian.PutUint16(b[8:10], declared+8)
	_, err := openBytes(t, b)
	var m *ErrMalformed
	if !errors.As(err, &m) {
		t.Fatalf("got %v, want *ErrMalformed", err)
	}
}

func TestOpen_RejectsBlockRunningPastEOF(t *testing.T) {
	b := multiblockFixtureBytes(t)
	// The first block-table entry's length field is at byte 32+4.
	binary.LittleEndian.PutUint32(b[36:40], 0xFFFFFF)
	_, err := openBytes(t, b)
	var m *ErrMalformed
	if !errors.As(err, &m) {
		t.Fatalf("got %v, want *ErrMalformed", err)
	}
}

func TestOpen_RejectsSparseSumMismatch(t *testing.T) {
	b := sparseOffsetUncompressedFixtureBytes(t)
	// Header channel count is at offset 10; the fixture has one sparse
	// range of length 7. Corrupt the declared channel count so it
	// disagrees with the sparse table's own sum.
	binary.LittleEndian.PutUint32(b[10:14], 999)
	_, err := openBytes(t, b)
	var m *ErrMalformed
	if !errors.As(err, &m) {
		t.Fatalf("got %v, want *ErrMalformed", err)
	}
}

func TestOpen_RejectsTruncatedFile(t *testing.T) {
	b := multiblockFixtureBytes(t)
	_, err := openBytes(t, b[:16])
	var m *ErrMalformed
	if !errors.As(err, &m) {
		t.Fatalf("got %v, want *ErrMalformed", err)
	}
}

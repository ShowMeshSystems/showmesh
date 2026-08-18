package fseq

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// Fixtures are built in memory rather than as committed testdata files:
// the repository's .gitignore refuses every *.fseq (real show content is
// someone's authored work and describes a physical installation channel
// by channel, and the backstop is deliberately blanket), so even a tiny
// synthetic fixture has nowhere safe to live on disk in this repo. These
// builders are the single source of truth for the byte layout every
// fixture test in this package exercises.

// fixtureSpec describes one synthetic FSEQ v2 file to build.
type fixtureSpec struct {
	compression            byte // 0 none, 1 zstd
	channelCount           uint32
	frameCount             uint32
	stepTimeMS             byte
	sparse                 []SparseRange
	framesPerBlock         uint32 // 0 => single block covering everything (or no block table for none)
	declareExtraZeroBlocks int
	varHeaderCode          string // optional, exercises "trust offset 4, don't walk headers"
	varHeaderData          string
	literalFrames          [][]byte // when set, used verbatim as each frame's already-sparse-packed bytes instead of synthFrameBytes/packFrame
}

func write3(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
}

// synthFrameBytes deterministically fills one frame's channel bytes at
// full (pre-sparse) width so a decoded frame can be checked byte-for-byte
// in tests: byte at absolute channel c, frame f, is (f*7 + c) & 0xFF.
func synthFrameBytes(frame int, n uint32) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(frame*7 + i)
	}
	return b
}

func packFrame(full []byte, ranges []SparseRange) []byte {
	var out []byte
	for _, r := range ranges {
		out = append(out, full[r.Start:r.Start+r.Length]...)
	}
	return out
}

// buildFixture renders spec into FSEQ v2 bytes: header, block table,
// sparse table, optional padded variable header, then channel data
// (compressed per-block if spec.compression is zstd). It panics on a
// zstd writer failure, which only a broken build environment would ever
// trigger; both *testing.T and *testing.F fixture helpers call this, and
// neither testing type implements a common error-reporting interface.
func buildFixture(spec fixtureSpec) []byte {
	fpb := spec.framesPerBlock
	if fpb == 0 {
		fpb = spec.frameCount
	}
	var blocks [][]byte
	for start := uint32(0); start < spec.frameCount; start += fpb {
		end := start + fpb
		if end > spec.frameCount {
			end = spec.frameCount
		}
		var buf []byte
		for fr := start; fr < end; fr++ {
			switch {
			case spec.literalFrames != nil:
				buf = append(buf, spec.literalFrames[fr]...)
			case len(spec.sparse) > 0:
				full := synthFrameBytes(int(fr), maxChannelSpan(spec.sparse))
				buf = append(buf, packFrame(full, spec.sparse)...)
			default:
				buf = append(buf, synthFrameBytes(int(fr), spec.channelCount)...)
			}
		}
		blocks = append(blocks, buf)
	}

	var compressedBlocks [][]byte
	var blockFirstFrames []uint32
	if spec.compression == 1 {
		enc, err := zstd.NewWriter(nil)
		if err != nil {
			panic(err)
		}
		defer func() { _ = enc.Close() }()
		fr := uint32(0)
		for _, b := range blocks {
			compressedBlocks = append(compressedBlocks, enc.EncodeAll(b, nil))
			blockFirstFrames = append(blockFirstFrames, fr)
			fr += fpb
		}
	}

	numBlocks := len(compressedBlocks) + spec.declareExtraZeroBlocks
	if spec.compression == 0 {
		numBlocks = 0 // uncompressed files carry no block table
	}
	numSparse := len(spec.sparse)

	var varHdr []byte
	if spec.varHeaderCode != "" {
		data := append([]byte(spec.varHeaderData), 0)
		entryLen := 4 + len(data)
		varHdr = make([]byte, entryLen)
		binary.LittleEndian.PutUint16(varHdr[0:2], uint16(entryLen))
		copy(varHdr[2:4], spec.varHeaderCode)
		copy(varHdr[4:], data)
	}

	fixedHeaderLen := headerSize + numBlocks*8 + numSparse*6
	chanDataOffset := fixedHeaderLen + len(varHdr)
	// Round up to a multiple of 4, matching the real writer's padding, to
	// exercise "trust offset 4, don't derive it from variable headers."
	pad := (4 - chanDataOffset%4) % 4
	chanDataOffset += pad

	hdr := make([]byte, headerSize)
	copy(hdr[0:4], "PSEQ")
	binary.LittleEndian.PutUint16(hdr[4:6], uint16(chanDataOffset))
	hdr[6] = 2 // minor
	hdr[7] = 2 // major
	binary.LittleEndian.PutUint16(hdr[8:10], uint16(fixedHeaderLen))
	binary.LittleEndian.PutUint32(hdr[10:14], spec.channelCount)
	binary.LittleEndian.PutUint32(hdr[14:18], spec.frameCount)
	hdr[18] = spec.stepTimeMS
	hdr[19] = 0
	hdr[20] = byte((numBlocks>>4)&0xF0) | spec.compression
	hdr[21] = byte(numBlocks & 0xFF)
	hdr[22] = byte(numSparse)
	hdr[23] = 0
	binary.LittleEndian.PutUint64(hdr[24:32], 1760000000000000)

	var blockTable []byte
	if spec.compression == 1 {
		for i, cb := range compressedBlocks {
			e := make([]byte, 8)
			binary.LittleEndian.PutUint32(e[0:4], blockFirstFrames[i])
			binary.LittleEndian.PutUint32(e[4:8], uint32(len(cb)))
			blockTable = append(blockTable, e...)
		}
		for i := 0; i < spec.declareExtraZeroBlocks; i++ {
			blockTable = append(blockTable, make([]byte, 8)...)
		}
	}

	var sparseTable []byte
	for _, r := range spec.sparse {
		e := make([]byte, 6)
		write3(e[0:3], r.Start)
		write3(e[3:6], r.Length)
		sparseTable = append(sparseTable, e...)
	}

	out := append([]byte{}, hdr...)
	out = append(out, blockTable...)
	out = append(out, sparseTable...)
	out = append(out, varHdr...)
	out = append(out, make([]byte, pad)...)
	if spec.compression == 0 {
		for _, b := range blocks {
			out = append(out, b...)
		}
	} else {
		for _, cb := range compressedBlocks {
			out = append(out, cb...)
		}
	}
	return out
}

func maxChannelSpan(ranges []SparseRange) uint32 {
	var max uint32
	for _, r := range ranges {
		if end := r.Start + r.Length; end > max {
			max = end
		}
	}
	return max
}

// openFixture builds spec's bytes, writes them to a temp file, and opens
// them through the real package entry point.
func openFixture(t *testing.T, spec fixtureSpec) *File {
	t.Helper()
	data := buildFixture(spec)
	path := filepath.Join(t.TempDir(), "fixture.fseq")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open(fixture): %v\n(spec: %+v)", err, spec)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func sparseSum(ranges []SparseRange) uint32 {
	var sum uint32
	for _, r := range ranges {
		sum += r.Length
	}
	return sum
}

// --- Named fixtures used across fseq_test.go.

func fixtureSparseOffsetUncompressed(t *testing.T) *File {
	t.Helper()
	ranges := []SparseRange{{Start: 100, Length: 7}}
	return openFixture(t, fixtureSpec{
		compression: 0, channelCount: sparseSum(ranges), frameCount: 3,
		stepTimeMS: 25, sparse: ranges,
	})
}

func fixtureSparseMultiRange(t *testing.T) *File {
	t.Helper()
	// Out of ascending order deliberately, to prove the reader doesn't
	// assume sorted ranges.
	ranges := []SparseRange{{Start: 50, Length: 5}, {Start: 10, Length: 3}}
	return openFixture(t, fixtureSpec{
		compression: 1, channelCount: sparseSum(ranges), frameCount: 4,
		stepTimeMS: 25, sparse: ranges,
	})
}

func fixtureSparseOverlapBoundary(t *testing.T) *File {
	t.Helper()
	// Two ranges sharing one boundary channel — the shape measured in a
	// real file pulled from an FPP host's own sequences/ directory
	// (RES-017 addendum): {10,20} and {29,15} both claim channel 29.
	//
	// The two ranges are packed here from DISTINCT literal bytes ("A"s for
	// range 0, "B"s for range 1) rather than derived from one shared
	// per-channel source buffer. A real xLights-written file always copies
	// both a range's channel and its overlap partner's from the same
	// source value (RES-017 addendum §13.1), so first-wins and last-wins
	// resolution are indistinguishable on real data — which is exactly why
	// a fixture built that way cannot tell the two resolution orders
	// apart. This fixture deliberately makes them disagree at the shared
	// channel so the test can assert which one this package implements:
	// range 1 (table order) must win, matching FPP's own
	// overwrite-per-range reassembly (UncompressedFrameData::readFrame).
	ranges := []SparseRange{{Start: 10, Length: 20}, {Start: 29, Length: 15}}
	frame1 := append(append([]byte{}, bytesOf('A', 20)...), bytesOf('B', 15)...)
	return openFixture(t, fixtureSpec{
		compression: 1, channelCount: sparseSum(ranges), frameCount: 3,
		stepTimeMS: 25, sparse: ranges,
		literalFrames: [][]byte{bytesOf('A', 35), frame1, bytesOf('A', 35)},
	})
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func fixtureMultiblock(t *testing.T) *File {
	t.Helper()
	// Multiple blocks, no sparse (full width), declares extra
	// zero-length block-table slots the reader must skip.
	return openFixture(t, fixtureSpec{
		compression: 1, channelCount: 12, frameCount: 6, stepTimeMS: 25,
		framesPerBlock: 3, declareExtraZeroBlocks: 2,
	})
}

func fixtureShortFinalBlock(t *testing.T) *File {
	t.Helper()
	// framesPerBlock=4 with 5 frames total, so the last block holds
	// exactly one frame.
	return openFixture(t, fixtureSpec{
		compression: 1, channelCount: 9, frameCount: 5, stepTimeMS: 25,
		framesPerBlock: 4,
	})
}

func fixtureWithVariableHeader(t *testing.T) *File {
	t.Helper()
	// Single block, with a padded variable header ('sp') between the
	// tables and the channel data, so the reader must use the
	// channel-data-offset field rather than walking headers.
	return openFixture(t, fixtureSpec{
		compression: 1, channelCount: 15, frameCount: 3, stepTimeMS: 25,
		varHeaderCode: "sp", varHeaderData: "gen-fixture 1.0",
	})
}

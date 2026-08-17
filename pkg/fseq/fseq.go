// Package fseq reads FSEQ v2 sequence files (the format xLights and FPP
// use for lighting playback data) so a render node can pull one surface's
// absolute channel range out of one frame, repeatedly, during playback.
//
// Only FSEQ v2 is supported, with compression none or zstd. zlib
// (compression type 2) and ESEQ are refused explicitly rather than
// guessed at, because their framing was not read from source (see
// docs/research/RES-017-fseq-format.md §8, §11.2). The format facts this
// package is built against — the block table storing lengths rather than
// offsets, the 12-bit split block count, the 0-based sparse ranges, the
// single-byte step time — are RES-017's, sourced from FPP and xLights,
// not re-derived here.
//
// Every channel number this package accepts or returns — SparseRange.Start,
// ChannelRange's start argument, MaxChannel — is 0-based, matching the
// file itself. xLights and FPP show operators a 1-based channel number
// (a model's "Start Channel"); converting that to this package's 0-based
// space is the caller's job, done once, at the one place a surface's
// configured start channel is read. Do not assume this package's numbers
// are already 1-based converted, and do not scatter the "-1" across
// unrelated call sites.
package fseq

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/klauspost/compress/zstd"
)

const (
	headerSize       = 32
	blockEntrySize   = 8
	sparseEntrySize  = 6
	varHeaderMinSize = 4
	maxHeaderBudget  = 65535 // channel data offset is a uint16 (RES-017 §10.8)
)

// SparseRange is one entry of the file's sparse channel range table: the
// absolute, 0-based starting channel and the number of channels it
// carries. In a sparse file, frame data holds only these channels,
// concatenated in table order with no gaps.
type SparseRange struct {
	Start  uint32
	Length uint32
}

// block is a resolved compression block: its position in the frame
// timeline and its byte position in the file. The header only ever
// stores (firstFrame, length) pairs; offset is computed once at Open by
// accumulating lengths from the channel data offset (RES-017 §5).
type block struct {
	firstFrame uint32
	offset     uint64
	length     uint64
}

// File is an open FSEQ v2 file positioned for repeated, efficient
// ChannelRange reads. It is not safe for concurrent use by multiple
// goroutines: it holds one decompressed-block cache, which a concurrent
// caller would thrash.
type File struct {
	f    *os.File
	size uint64

	versionMajor, versionMinor byte
	chanDataOffset             uint64
	channelCount               uint32 // per-frame channel count in this file (sum of sparse lengths, or full width with no sparse ranges)
	frameCount                 uint32
	stepTimeMS                 byte // raw byte as FPP stores and reads it — see StepTimeMS doc
	compression                CompressionType
	uniqueID                   uint64
	sparseRanges               []SparseRange
	sparseOffsets              []uint64 // sparseOffsets[i] = running byte offset within a frame where sparseRanges[i]'s data starts
	mediaFilename              string
	sequenceProducer           string

	blocks []block // used only when compression != none

	dec *zstd.Decoder

	cachedBlock int // index into blocks; -1 means nothing cached
	cachedData  []byte
	rawBuf      []byte

	segBuf        []segment    // reused scratch for resolveSegments' coverage pass
	rawOverlapBuf []rawOverlap // reused scratch for resolveSegments' overlap collection
	claimedBuf    []ivl        // reused scratch for resolveSegments' last-wins resolution

	// segCache holds the resolved segments for the most recent (start,
	// count) request. A render surface requests the same absolute
	// channel range on every frame, so this is what keeps the sparse
	// table walk from repeating at 40fps; it is a distinct, independently
	// owned buffer from segBuf, never aliased to it, since segBuf is
	// scratch that the next call overwrites.
	segCache      []segment
	segCacheStart uint64
	segCacheEnd   uint64
	segCacheValid bool
}

// rawOverlap is one sparse range's overlap with a requested channel
// window, before overlap resolution.
type rawOverlap struct {
	ovStart, ovEnd uint64
	srcBase        uint64 // src offset within the frame corresponding to ovStart
}

// Open parses an FSEQ v2 file's header and tables and returns a File
// ready for repeated ChannelRange calls. It does not read any frame data.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	file, err := open(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return file, nil
}

func open(f *os.File) (*File, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := uint64(fi.Size())
	if size < headerSize {
		return nil, &ErrMalformed{Reason: fmt.Sprintf("file is %d bytes, smaller than the %d-byte fixed header", size, headerSize)}
	}

	hdr := make([]byte, headerSize)
	if _, err := io.ReadFull(io.NewSectionReader(f, 0, headerSize), hdr); err != nil {
		return nil, err
	}

	switch string(hdr[0:4]) {
	case "PSEQ", "FSEQ":
		// ok
	case "ESEQ":
		return nil, ErrESEQUnsupported
	default:
		return nil, ErrNotFSEQ
	}

	chanDataOffset := uint64(binary.LittleEndian.Uint16(hdr[4:6]))
	versionMinor := hdr[6]
	versionMajor := hdr[7]
	if versionMajor != 2 {
		return nil, &ErrUnsupportedVersion{Major: versionMajor, Minor: versionMinor}
	}

	declaredHeaderLen := uint64(binary.LittleEndian.Uint16(hdr[8:10]))
	channelCount := binary.LittleEndian.Uint32(hdr[10:14])
	frameCount := binary.LittleEndian.Uint32(hdr[14:18])
	stepTimeMS := hdr[18]
	compression := CompressionType(hdr[20] & 0x0F)
	numBlocks := (uint32(hdr[20]&0xF0) << 4) | uint32(hdr[21])
	numSparse := uint32(hdr[22])
	uniqueID := binary.LittleEndian.Uint64(hdr[24:32])

	if compression == CompressionZlib {
		return nil, &ErrUnsupportedCompression{Type: compression}
	}
	if compression != CompressionNone && compression != CompressionZstd {
		return nil, &ErrUnsupportedCompression{Type: compression}
	}

	// Bound the header budget before trusting the counts for anything.
	// numBlocks is capped at 4095 (12 bits) and numSparse at 255 (1 byte),
	// so blockTableBytes+sparseTableBytes can never itself overflow, but
	// the result must still fit inside the file and inside the uint16
	// channel-data-offset budget (RES-017 §10.8) before it is used to
	// carve up the file.
	blockTableBytes := uint64(numBlocks) * blockEntrySize
	sparseTableBytes := uint64(numSparse) * sparseEntrySize
	computedHeaderLen := headerSize + blockTableBytes + sparseTableBytes
	if computedHeaderLen > maxHeaderBudget {
		return nil, &ErrMalformed{Reason: fmt.Sprintf("block+sparse table (%d bytes) exceeds the 65535-byte header budget", computedHeaderLen)}
	}
	if computedHeaderLen != declaredHeaderLen {
		// FPP itself only logs this mismatch and keeps going. RES-017 §4
		// and §10.11: a ShowMesh parser treats it as fatal, because a
		// mismatch means one of the two counts is wrong and everything
		// downstream is misaligned.
		return nil, &ErrMalformed{Reason: fmt.Sprintf("declared header length %d does not match computed length %d (32 + %d blocks*8 + %d sparse*6)", declaredHeaderLen, computedHeaderLen, numBlocks, numSparse)}
	}
	if chanDataOffset < computedHeaderLen || chanDataOffset > size {
		return nil, &ErrMalformed{Reason: fmt.Sprintf("channel data offset %d is inconsistent with header length %d and file size %d", chanDataOffset, computedHeaderLen, size)}
	}

	tables := make([]byte, blockTableBytes+sparseTableBytes)
	if blockTableBytes+sparseTableBytes > 0 {
		if _, err := io.ReadFull(io.NewSectionReader(f, headerSize, int64(len(tables))), tables); err != nil {
			return nil, err
		}
	}

	rawBlocks := make([]struct {
		firstFrame uint32
		length     uint32
	}, numBlocks)
	for i := range rawBlocks {
		e := tables[i*blockEntrySize : (i+1)*blockEntrySize]
		rawBlocks[i].firstFrame = binary.LittleEndian.Uint32(e[0:4])
		rawBlocks[i].length = binary.LittleEndian.Uint32(e[4:8])
	}

	sparseRaw := tables[blockTableBytes:]
	sparseRanges := make([]SparseRange, numSparse)
	for i := range sparseRanges {
		e := sparseRaw[i*sparseEntrySize : (i+1)*sparseEntrySize]
		sparseRanges[i] = SparseRange{
			Start:  read3ByteUint(e[0:3]),
			Length: read3ByteUint(e[3:6]),
		}
	}
	if numSparse > 0 {
		var sum uint64
		for _, r := range sparseRanges {
			sum += uint64(r.Length)
		}
		if sum != uint64(channelCount) {
			return nil, &ErrMalformed{Reason: fmt.Sprintf("sparse range lengths sum to %d but header channel count is %d", sum, channelCount)}
		}
	}

	// Resolve block offsets: the table stores lengths, not offsets. A
	// zero-length entry is a reserved-but-unused slot and is skipped
	// without advancing the cursor (RES-017 §5) — the declared block
	// count routinely exceeds the used count.
	var blocks []block
	if compression != CompressionNone {
		off := chanDataOffset
		var prevFirstFrame uint32
		blocks = make([]block, 0, numBlocks)
		for _, rb := range rawBlocks {
			if rb.length == 0 {
				continue
			}
			if len(blocks) > 0 && rb.firstFrame <= prevFirstFrame {
				return nil, &ErrMalformed{Reason: fmt.Sprintf("block table firstFrame is not strictly increasing at frame %d after %d", rb.firstFrame, prevFirstFrame)}
			}
			if off+uint64(rb.length) > size {
				return nil, &ErrMalformed{Reason: fmt.Sprintf("block at offset %d length %d runs past end of file (%d bytes)", off, rb.length, size)}
			}
			blocks = append(blocks, block{firstFrame: rb.firstFrame, offset: off, length: uint64(rb.length)})
			off += uint64(rb.length)
			prevFirstFrame = rb.firstFrame
		}
		if len(blocks) == 0 {
			return nil, &ErrMalformed{Reason: "compressed file has no non-empty blocks"}
		}
		if blocks[0].firstFrame != 0 {
			return nil, &ErrMalformed{Reason: fmt.Sprintf("first block starts at frame %d, not 0", blocks[0].firstFrame)}
		}
	}

	mediaFilename, sequenceProducer := parseVariableHeaders(f, declaredHeaderLen, chanDataOffset)

	sparseOffsets := make([]uint64, len(sparseRanges))
	var running uint64
	for i, r := range sparseRanges {
		sparseOffsets[i] = running
		running += uint64(r.Length)
	}

	file := &File{
		f:                f,
		size:             size,
		versionMajor:     versionMajor,
		versionMinor:     versionMinor,
		chanDataOffset:   chanDataOffset,
		channelCount:     channelCount,
		frameCount:       frameCount,
		stepTimeMS:       stepTimeMS,
		compression:      compression,
		uniqueID:         uniqueID,
		sparseRanges:     sparseRanges,
		sparseOffsets:    sparseOffsets,
		mediaFilename:    mediaFilename,
		sequenceProducer: sequenceProducer,
		blocks:           blocks,
		cachedBlock:      -1,
	}
	if compression == CompressionZstd {
		dec, err := zstd.NewReader(nil)
		if err != nil {
			return nil, err
		}
		file.dec = dec
	}
	return file, nil
}

// read3ByteUint assembles a little-endian 24-bit unsigned integer, the
// width FSEQ uses for sparse range start/length.
func read3ByteUint(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}

// parseVariableHeaders best-effort extracts the 'mf' (media filename) and
// 'sp' (sequence producer) entries. RES-017 §7: variable headers do not
// have to be parsed to find channel data, so any failure here is
// swallowed rather than failing Open — they are provenance, not structure.
func parseVariableHeaders(f *os.File, headerLen, chanDataOffset uint64) (mediaFilename, sequenceProducer string) {
	if chanDataOffset <= headerLen {
		return "", ""
	}
	n := chanDataOffset - headerLen
	if n > maxHeaderBudget {
		return "", ""
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(io.NewSectionReader(f, int64(headerLen), int64(n)), buf); err != nil {
		return "", ""
	}
	var idx uint64
	for idx+varHeaderMinSize < n {
		entryLen := uint64(binary.LittleEndian.Uint16(buf[idx : idx+2]))
		code := string(buf[idx+2 : idx+4])
		if entryLen < varHeaderMinSize || idx+entryLen > n {
			break
		}
		data := buf[idx+4 : idx+entryLen]
		s := nulTerminated(data)
		switch code {
		case "mf":
			mediaFilename = s
		case "sp":
			sequenceProducer = s
		}
		idx += entryLen
	}
	return mediaFilename, sequenceProducer
}

func nulTerminated(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// Close releases the underlying file handle.
func (f *File) Close() error {
	if f.dec != nil {
		f.dec.Close()
	}
	return f.f.Close()
}

// FrameCount is the number of frames in the file.
func (f *File) FrameCount() int { return int(f.frameCount) }

// ChannelCount is the per-frame channel count carried by this file — the
// sum of the sparse range lengths when sparse ranges are present, or the
// full channel width otherwise. It is not the show's total channel count
// and, per RES-017 §10.7, it is not guaranteed to be a multiple of 3:
// this package deals in channels and bytes only, never pixels.
func (f *File) ChannelCount() int { return int(f.channelCount) }

// StepTimeMS is the raw single-byte step time exactly as the file and FPP
// store it, e.g. 33 for a nominally-30fps sequence. Do not "correct" this
// to a true frame duration (33.333...): FPP reads this same byte to drive
// the lighting timeline, and matching FPP — not the arithmetically true
// rate — is what keeps a render in step with the show.
func (f *File) StepTimeMS() byte { return f.stepTimeMS }

// Compression reports the file's declared compression type.
func (f *File) Compression() CompressionType { return f.compression }

// UniqueID is the file's write-time id (FPP writes microseconds since the
// Unix epoch). It is a write timestamp, not a content identity — it is
// not stable across two renders of identical content.
func (f *File) UniqueID() uint64 { return f.uniqueID }

// SparseRanges returns a copy of the file's sparse range table, in file
// (table) order. An empty slice means the file carries no sparse ranges
// and every channel in [0, ChannelCount) is present.
func (f *File) SparseRanges() []SparseRange {
	out := make([]SparseRange, len(f.sparseRanges))
	copy(out, f.sparseRanges)
	return out
}

// MediaFilename is the 'mf' variable header, when present. It is the
// authoring machine's path and is not resolvable on a node.
func (f *File) MediaFilename() string { return f.mediaFilename }

// SequenceProducer is the 'sp' variable header, when present — the
// xLights build that wrote the file.
func (f *File) SequenceProducer() string { return f.sequenceProducer }

// MaxChannel is the highest absolute channel this file describes:
// max(ChannelCount, max over sparse ranges of start+length). With no
// sparse ranges this equals ChannelCount.
func (f *File) MaxChannel() uint32 {
	max := f.channelCount
	for _, r := range f.sparseRanges {
		if end := r.Start + r.Length; end > max {
			max = end
		}
	}
	return max
}

// segment is one contiguous run of source bytes (within a frame's packed
// data) that lands at a contiguous run of destination positions (within
// the caller's requested [start, start+count) window).
type segment struct {
	dstOffset uint32
	srcOffset uint64
	length    uint32
}

// ChannelRange writes the bytes for absolute channel range
// [start, start+count) at the given frame into dst, which must have
// length >= count. If any channel in the requested range is not covered
// by the file's sparse ranges, it returns *ErrChannelRangeNotCovered and
// writes nothing — a partially covered request is refused whole, never
// partially filled or zero-filled, because an absent channel decoded as
// zero is indistinguishable from real content.
func (f *File) ChannelRange(frame, start, count int, dst []byte) error {
	if frame < 0 || frame >= int(f.frameCount) {
		return &ErrFrameOutOfRange{Frame: frame, FrameCount: int(f.frameCount)}
	}
	if start < 0 || count < 0 {
		return &ErrMalformed{Reason: fmt.Sprintf("negative channel range requested: start=%d count=%d", start, count)}
	}
	if count == 0 {
		return nil
	}
	if len(dst) < count {
		return fmt.Errorf("fseq: dst has length %d, need at least %d", len(dst), count)
	}
	reqStart := uint64(start)
	reqEnd := reqStart + uint64(count)

	segs, ok := f.resolveSegments(reqStart, reqEnd)
	if !ok {
		return &ErrChannelRangeNotCovered{
			RequestedStart: uint32(start),
			RequestedCount: uint32(count),
			Covered:        f.SparseRanges(),
		}
	}

	frameData, err := f.frameData(frame)
	if err != nil {
		return err
	}
	for _, seg := range segs {
		copy(dst[seg.dstOffset:seg.dstOffset+seg.length], frameData[seg.srcOffset:seg.srcOffset+uint64(seg.length)])
	}
	return nil
}

// ivl is a half-open [start, end) interval of absolute channel numbers,
// used only to resolve overlapping sparse ranges (see resolveSegments).
type ivl struct{ start, end uint64 }

// subtractClaimed returns the parts of iv not covered by any interval in
// claimed.
func subtractClaimed(iv ivl, claimed []ivl) []ivl {
	remaining := []ivl{iv}
	for _, c := range claimed {
		var next []ivl
		for _, r := range remaining {
			if c.end <= r.start || c.start >= r.end {
				next = append(next, r)
				continue
			}
			if c.start > r.start {
				next = append(next, ivl{r.start, c.start})
			}
			if c.end < r.end {
				next = append(next, ivl{c.end, r.end})
			}
		}
		remaining = next
	}
	return remaining
}

// resolveSegments maps the absolute channel window [reqStart, reqEnd)
// onto byte segments within one frame's packed data. It walks the sparse
// range table in table order (RES-017 §6: order is not guaranteed sorted
// or non-overlapping, so this must not be optimised into a sorted binary
// search) and collects every overlap.
//
// Measured on two independent copies of a real per-target render pulled
// from an FPP host's own sequences/ directory: adjacent sparse ranges can
// share a boundary channel (range N ending at the same absolute channel
// range N+1 starts at), so the packed frame data carries that channel's
// byte twice. Checked byte-for-byte across all 10,023 frames of that
// file: the two copies never disagreed. FPP's own reassembly
// (FSEQFile.cpp's UncompressedFrameData::readFrame) is an unconditional
// memcpy per range in table order, so a later range's copy silently
// overwrites an earlier range's for any channel both claim. This function
// matches that: overlaps are resolved last-table-order-wins, not
// rejected. A genuine gap — a channel no range claims at all — is still a
// hard refusal.
func (f *File) resolveSegments(reqStart, reqEnd uint64) ([]segment, bool) {
	if len(f.sparseRanges) == 0 {
		// No sparse table: the file is the full channel width, identity
		// mapped.
		if reqEnd > uint64(f.channelCount) {
			return nil, false
		}
		return []segment{{dstOffset: 0, srcOffset: reqStart, length: uint32(reqEnd - reqStart)}}, true
	}

	if f.segCacheStart == reqStart && f.segCacheEnd == reqEnd && f.segCacheValid {
		return f.segCache, true
	}

	raws := f.rawOverlapBuf[:0]
	for i, r := range f.sparseRanges {
		rStart := uint64(r.Start)
		rEnd := rStart + uint64(r.Length)
		ovStart := reqStart
		if rStart > ovStart {
			ovStart = rStart
		}
		ovEnd := reqEnd
		if rEnd < ovEnd {
			ovEnd = rEnd
		}
		if ovStart >= ovEnd {
			continue
		}
		raws = append(raws, rawOverlap{ovStart: ovStart, ovEnd: ovEnd, srcBase: f.sparseOffsets[i] + (ovStart - rStart)})
	}
	f.rawOverlapBuf = raws
	if len(raws) == 0 {
		f.segCacheValid = false
		return nil, false
	}

	// Walk in reverse table order so a later range's claim is resolved
	// first and an earlier range only fills what's left unclaimed —
	// last-table-order-wins, matching FPP's overwrite-per-range loop.
	segs := f.segBuf[:0]
	claimed := f.claimedBuf[:0]
	for i := len(raws) - 1; i >= 0; i-- {
		ro := raws[i]
		for _, p := range subtractClaimed(ivl{ro.ovStart, ro.ovEnd}, claimed) {
			segs = append(segs, segment{
				dstOffset: uint32(p.start - reqStart),
				srcOffset: ro.srcBase + (p.start - ro.ovStart),
				length:    uint32(p.end - p.start),
			})
		}
		claimed = append(claimed, ivl{ro.ovStart, ro.ovEnd})
	}
	f.segBuf = segs
	f.claimedBuf = claimed

	sort.Slice(segs, func(i, j int) bool { return segs[i].dstOffset < segs[j].dstOffset })
	var covered uint64
	for _, s := range segs {
		if uint64(s.dstOffset) != covered {
			f.segCacheValid = false
			return nil, false // gap: no range claims this channel
		}
		covered += uint64(s.length)
	}
	if covered != reqEnd-reqStart {
		f.segCacheValid = false
		return nil, false
	}

	// Cache: the caller's (start, count) is the same on every frame for
	// the life of a surface assignment, so this is what keeps the 40fps
	// hot path from re-walking the sparse table every frame.
	if cap(f.segCache) < len(segs) {
		f.segCache = make([]segment, len(segs))
	}
	f.segCache = f.segCache[:len(segs)]
	copy(f.segCache, segs)
	f.segCacheStart, f.segCacheEnd, f.segCacheValid = reqStart, reqEnd, true
	return f.segCache, true
}

// frameData returns the raw per-frame packed bytes for frame, from the
// decompressed-block cache (compressed files) or directly from the file
// (uncompressed files, which are randomly addressable with no
// decompression — RES-017 §5.1).
func (f *File) frameData(frame int) ([]byte, error) {
	if f.compression == CompressionNone {
		return f.readUncompressedFrame(frame)
	}
	return f.readCompressedFrame(frame)
}

func (f *File) readUncompressedFrame(frame int) ([]byte, error) {
	off := f.chanDataOffset + uint64(frame)*uint64(f.channelCount)
	end := off + uint64(f.channelCount)
	if end > f.size {
		return nil, &ErrMalformed{Reason: fmt.Sprintf("frame %d at offset %d..%d runs past end of file (%d bytes)", frame, off, end, f.size)}
	}
	if uint64(cap(f.rawBuf)) < uint64(f.channelCount) {
		f.rawBuf = make([]byte, f.channelCount)
	}
	f.rawBuf = f.rawBuf[:f.channelCount]
	if _, err := io.ReadFull(io.NewSectionReader(f.f, int64(off), int64(f.channelCount)), f.rawBuf); err != nil {
		return nil, err
	}
	return f.rawBuf, nil
}

func (f *File) readCompressedFrame(frame int) ([]byte, error) {
	bi := f.blockIndexForFrame(uint32(frame))
	if bi < 0 {
		return nil, &ErrFrameOutOfRange{Frame: frame, FrameCount: int(f.frameCount)}
	}
	if bi != f.cachedBlock {
		if err := f.decodeBlock(bi); err != nil {
			return nil, err
		}
		f.cachedBlock = bi
	}
	b := f.blocks[bi]
	frameInBlock := uint64(frame) - uint64(b.firstFrame)
	off := frameInBlock * uint64(f.channelCount)
	end := off + uint64(f.channelCount)
	if end > uint64(len(f.cachedData)) {
		return nil, &ErrMalformed{Reason: fmt.Sprintf("frame %d needs bytes %d..%d but decoded block %d is only %d bytes", frame, off, end, bi, len(f.cachedData))}
	}
	return f.cachedData[off:end], nil
}

// blockIndexForFrame finds the block whose [firstFrame, nextFirstFrame)
// span contains frame, via binary search over the ascending firstFrame
// values validated at Open.
func (f *File) blockIndexForFrame(frame uint32) int {
	n := len(f.blocks)
	i := sort.Search(n, func(i int) bool { return f.blocks[i].firstFrame > frame })
	if i == 0 {
		return -1
	}
	return i - 1
}

// framesInBlock is the clamped frame span of block bi: the gap to the
// next block's firstFrame, or to the file's frame count for the last
// block, whichever is smaller. RES-017 §5 measured this producing a
// last block that holds one frame in a file whose other blocks hold four.
func (f *File) framesInBlock(bi int) uint32 {
	next := f.frameCount
	if bi+1 < len(f.blocks) {
		if f.blocks[bi+1].firstFrame < next {
			next = f.blocks[bi+1].firstFrame
		}
	}
	return next - f.blocks[bi].firstFrame
}

func (f *File) decodeBlock(bi int) error {
	b := f.blocks[bi]
	if uint64(cap(f.rawBuf)) < b.length {
		f.rawBuf = make([]byte, b.length)
	}
	f.rawBuf = f.rawBuf[:b.length]
	if _, err := io.ReadFull(io.NewSectionReader(f.f, int64(b.offset), int64(b.length)), f.rawBuf); err != nil {
		return err
	}

	switch f.compression {
	case CompressionZstd:
		decoded, err := f.dec.DecodeAll(f.rawBuf, f.cachedData[:0])
		if err != nil {
			return fmt.Errorf("fseq: block %d failed to decompress: %w", bi, err)
		}
		f.cachedData = decoded
	default:
		return &ErrUnsupportedCompression{Type: f.compression}
	}

	want := uint64(f.framesInBlock(bi)) * uint64(f.channelCount)
	if uint64(len(f.cachedData)) != want {
		return &ErrMalformed{Reason: fmt.Sprintf("block %d decompressed to %d bytes, expected %d (%d frames * %d channels)", bi, len(f.cachedData), want, f.framesInBlock(bi), f.channelCount)}
	}
	return nil
}

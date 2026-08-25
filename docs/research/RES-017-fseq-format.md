# RES-017: The FSEQ v2 File Format, Precisely Enough to Write a Go Parser

[ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md) · [ADR-027](../decisions/ADR-027-show-and-surface-model.md) · [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md) · [Track B](../build/TRACK-B-nodes-and-projection.md) · [RES-003](RES-003-xlights-fpp-connect-compatibility.md) · [Tracker](README.md)

Status: planned · Risk: medium · Verification: **L1 — source-verified** against FPP `8.4.2` and FPP `master`, and against xLights `master`, plus **direct inspection of 198 real xLights-written `.fseq` artifacts** on the owner's machine. **No ShowMesh code was written or run. Nothing here is a bench verification of any ShowMesh component.**

Researched 2026-08-17. Everything marked **Fact** below was read in source or measured from a real file with a hex reader; everything marked **Inference (L0)** was reasoned from those facts and has not been confirmed by running anything.

## 1. Decision to make

Track B needs to read a node-local FSEQ file and extract a specific **absolute channel range** at a specific **frame index**, on the node, in Go, with no CGo (ADR-012 forbids CGo in the coordinator image and the agent inherits the same pure-Go discipline for anything the coordinator also links). This record establishes the byte-level contract that parser is written against.

It does **not** decide the surface-extraction model, the caching strategy, or the frame pacing. Those are Track B design questions; this is the wire format underneath them.

## 2. Questions

- What is the exact v2 header layout — offsets, widths, endianness?
- Which compression types exist, how is the block table laid out, and how do you get from a frame index to bytes?
- What is the sparse-range layout, and how does a sparse range map an absolute channel to an offset inside a frame?
- Is channel numbering 0-based or 1-based, in the file and in the tools?
- What do the variable headers look like, and must they be parsed to find the channel data?
- What does xLights actually write for an FPP Connect per-target render?
- Is a pure-Go zstd decoder sufficient?

## 3. Provenance

| Source | Pin | How read |
|---|---|---|
| FPP `8.4.2` — `src/fseq/FSEQFile.cpp`, `src/fseq/FSEQFile.h` | local source tree at `~/Downloads/fpp-8.4.2/` | read directly, in full for the V2 paths |
| FPP `8.4.2` — `docs/FSEQ_Sequence_File_Format.txt` | same tree | read in full; it is the project's own normative prose spec |
| FPP `master` — `src/fseq/FSEQFile.cpp` | commit `ac141d68a7a23482594ede116acdc72d64dbe915` | fetched via `gh api`, diffed line-for-line against 8.4.2 |
| FPP `master` — `src/MultiSync.cpp` | same commit | fetched, read for `channelRanges` generation |
| xLights `master` — `src-core/controllers/FPP.cpp` | default branch HEAD, fetched 2026-08-17 | fetched via `gh api`, read for `PrepareUploadSequence` |
| 198 real `.fseq` files | `~/Documents/**`, `~/Downloads/**`, written by xLights 2025.08 / 2025.10.1 / 2025.11 | parsed with a throwaway Python reader written from the spec above |

Web/API access date for every remote fetch: **2026-08-17**. The FPP 8.4.2 tree is the local copy; the deployed fleet runs FPP 9.4, which is **not** the tree read here — see §11.1 for why that gap does not move any conclusion in this record.

`docs/FSEQ_Sequence_File_Format.txt` was also requested from `raw.githubusercontent.com` on `master`; the fetch returned a paraphrase rather than the file, so **every quoted offset below comes from the local 8.4.2 copy and from the code**, not from that fetch. The code and the local doc agree on every field.

## 4. The v2 header

**Fact.** All multi-byte integers are **little-endian**, unsigned. The reader helpers are `read2ByteUInt`, `read3ByteUInt`, `read4ByteUInt` in `FSEQFile.cpp`, each of which assembles bytes low-to-high (`data[0] + (data[1] << 8) + ...`).

**Fact.** The fixed v2 header is **32 bytes** (`static const int V2FSEQ_HEADER_SIZE = 32;`, `FSEQFile.cpp` — identical `constexpr` value on `master`).

| Offset | Size | Field | Notes |
|---:|---:|---|---|
| 0 | 4 | Magic | `PSEQ` for FSEQ. `openFSEQFile` also accepts `FSEQ` and `ESEQ` at byte 0; **`ESEQ` is a different, 20-byte-header format** and Track B should reject it explicitly rather than fall through. |
| 4 | 2 | Channel data offset | Absolute byte offset where frame data begins. **This is a `uint16`, so the entire header including variable headers, block table and sparse table is capped at 65535 bytes.** |
| 6 | 1 | Version minor | Observed `2` on every real file (§9). |
| 7 | 1 | Version major | `2` |
| 8 | 2 | Fixed header length | `32 + numBlocks*8 + numSparse*6`. Index of the first variable header. |
| 10 | 4 | Channel count **per frame in this file** | Not the show's channel count. With sparse ranges this is the **sum of the sparse range lengths**. |
| 14 | 4 | Number of frames | |
| 18 | 1 | Step time, milliseconds | **One byte.** Max representable step time is 255 ms. |
| 19 | 1 | Flags / reserved | Written as `0`; observed `0` on all 198 files. |
| 20 | 1 | Compression type (low nibble) + block count bits 8–11 (high nibble) | See §5. |
| 21 | 1 | Block count, bits 0–7 | |
| 22 | 1 | Number of sparse ranges | **One byte, so at most 255 sparse ranges.** |
| 23 | 1 | Flags / reserved | Written as `0`. |
| 24 | 8 | Unique id | `uint64`. FPP writes `GetTime()` — microseconds since the Unix epoch. Observed values are consistent with that (e.g. `1760763046447617` → 2025-10-18). |
| 32 | `numBlocks*8` | Compression block table | §5 |
| 32 + `numBlocks*8` | `numSparse*6` | Sparse range table | §6 |
| = header length (offset 8) | … | Variable headers | §7 |
| = channel data offset (offset 4) | … | Frame data | |

**Fact.** The writer pads the channel data offset up to a multiple of 4 (`roundTo4Internal`), so there is normally 0–3 bytes of zero padding between the last variable header and the channel data. A parser must trust the offset at byte 4 and never compute the data start by walking the variable headers.

**Fact, and it is the trap in this header.** The **unique id is at a fixed index and is not on the sequential read path.** FPP reads the block table and sparse table forward from offset 32, then reads the id with `m_uniqueId = *((uint64_t*)&header[24])`, explicitly noting that this "does not advance readPos". A parser that reads fields in file order will read the id, then the block table, and the block table starts at 32 regardless.

**Fact.** `header[8]` (fixed header length) is a **cross-check, not a navigation aid**: FPP validates `readPos == headerSize` after the sparse table and only logs an error on mismatch. It keeps going. A ShowMesh parser should treat a mismatch as a hard reject, because a mismatch means one of the two counts is wrong and everything downstream is misaligned.

**Inference (L0).** Nothing in the format identifies the *show*, the *target*, or the content. ADR-028's identity rule (show + sequence + target + content hash) gets no help from the file itself: the unique id is a write timestamp, not a content identity, and two per-target renders of one sequence carry different unique ids with the same filename. This confirms ADR-028's premise from the format side.

## 5. Compression and the block table

**Fact.** The compression type is `header[20] & 0x0F`:

| Value | Type |
|---:|---|
| 0 | none |
| 1 | zstd |
| 2 | zlib |

Any other value is logged as unknown by FPP and left at `none`, which will misparse. ShowMesh should reject.

**Fact.** The block count is **12 bits split across two bytes**:

```
numBlocks = ((header[20] & 0xF0) << 4) | header[21]
```

Verified against the writer, which does `header[20] = ((maxBlocks >> 4) & 0xF0) | compressionType; header[21] = maxBlocks & 0xFF;`. The upper 4 bits are an FSEQ **2.1** addition; a 2.0 file has them zero and the expression degrades correctly. Maximum 4095 blocks with extended blocks enabled, 255 without.

**Fact.** Each block table entry is **8 bytes**: `uint32 firstFrame`, `uint32 compressedLength`. `V2FSEQ_COMPRESSION_BLOCK_SIZE = 8`.

**Fact, and this is the single most important structural rule in the format.** **The table stores lengths, not offsets.** A block's byte offset is the running sum of all preceding non-zero lengths, starting at the channel data offset:

```
off = chanDataOffset
for each (firstFrame, length) in table:
    if length > 0:
        block{firstFrame -> off, length}
        off += length
```

Entries with `length == 0` are **skipped entirely and do not advance the cursor**. This matters because the writer reserves `maxBlocks` slots in the header before it knows how many it will use and zero-fills them, so real files routinely carry trailing zero entries — measured 603 declared / 600 used, 4003 declared / 4000 used, 3466 declared / 3464 used (§9).

**Fact, measured.** On `Halloween Rainbow Test v1.fseq` (600 used blocks) the cumulative sum of block lengths from the channel data offset lands **exactly** on the file size (1,287,870 bytes), and **all 600** computed block offsets begin with the zstd frame magic `28 B5 2F FD`. That is a 600-of-600 confirmation of the accumulation rule and of the "one standalone zstd frame per block" property in the same measurement.

**Fact.** A block holds `nextBlockFirstFrame - thisBlockFirstFrame` frames, **clamped to the file's frame count**. FPP:

```cpp
m_framesPerBlock = (m_file->m_frameOffsets[m_curBlock + 1].first > m_file->getNumFrames()
                      ? m_file->getNumFrames()
                      : m_file->m_frameOffsets[m_curBlock + 1].first)
                   - m_file->m_frameOffsets[m_curBlock].first;
```

For the **last** block there is no next entry; FPP synthesises a sentinel `(numFrames + 2, fileSize)` so the arithmetic works out, and the clamp then trims it. Measured on `Kpop Demon Hunters v3.fseq`: blocks are 4 frames each, last block `firstFrame = 13852` with `numFrames = 13853`, and the block decompresses to exactly **one** frame (45,241 bytes), not four. **A parser that assumes a uniform frames-per-block reads past the end of the last block.**

**Fact.** The **first block is deliberately short.** The writer closes block 0 at frame 10 unconditionally (`if ((m_curBlock == 0 && m_curFrameInBlock == 10) || ...)`) so a remote can get its first frames fast, and compresses it at a negative zstd level when zstd is new enough. In the real files this shows up as block 0 having the same frame span as the others but a larger compressed size than its neighbours. Do not infer frames-per-block from block 0.

### 5.1 Locating and reading frame N

**Fact**, from `V2ZSTDCompressionHandler::getFrame` plus the block-offset rule:

1. Find the block `b` such that `blocks[b].firstFrame <= N < blocks[b+1].firstFrame` (using the synthesised sentinel for the last block).
2. Read `blocks[b].length` bytes at `blocks[b].offset`.
3. Decompress. The result is **exactly** `framesInBlock * channelCountPerFrame` bytes, where `framesInBlock` is the clamped value above.
4. Frame `N`'s data is the contiguous slice at `(N - blocks[b].firstFrame) * channelCountPerFrame`, length `channelCountPerFrame`.

**Fact, measured.** Decompressed lengths matched `framesInBlock * channelCount` exactly on all eight blocks sampled across two files, including block 0, a mid-file block, and the short final block.

**Fact.** For `compressionType == none` there is no block table (FPP pushes a single synthetic entry at the channel data offset) and frame `N` is simply at `chanDataOffset + N * channelCount`. This is the only case where the file is randomly addressable without decompression.

**Inference (L0).** For Track B's access pattern — one surface's channel range, frame by frame, in order — the natural implementation is: decode a block, hold it, serve every frame in it, then move on. Decoding a block per frame would decompress the same 2–6 frames 2–6 times. Nothing in the format prevents either; this is a Track B design note, not a format fact.

## 6. Sparse ranges

**Fact.** Each entry is **6 bytes**: `uint24 startChannel`, `uint24 length`, both little-endian. `V2FSEQ_SPARSE_RANGE_SIZE = 6`. Confirmed on both the read side (`read3ByteUInt(&header[readPos])` / `read3ByteUInt(&header[readPos + 3])`) and the write side (`write3ByteUInt`). **Maximum addressable channel from a sparse range is therefore 16,777,215.**

**Fact.** When sparse ranges are present, `channelCount` at offset 10 is the **sum of the range lengths**, and each frame in the file contains only those channels, **concatenated in table order with no gaps and no padding**. The writer is explicit:

```cpp
for (auto& a : m_file->m_sparseRanges) {
    write(&data[a.first], a.second);   // uncompressed handler
}
```

and the same loop feeds the zstd compressor range by range.

**Fact — the absolute-channel mapping.** The inverse is `UncompressedFrameData::readFrame`, which scatters a frame back into a full-width buffer:

```cpp
uint32_t offset = 0;
for (auto& rng : m_ranges) {
    memcpy(&data[rng.first], &m_data[offset], toCopy);
    offset += rng.second;
}
```

`data` is indexed by `rng.first` directly. So the rule Track B needs is:

> For absolute channel `C`, walk the sparse ranges in table order accumulating `runningOffset`. The first range with `start <= C < start + length` puts channel `C` at byte `runningOffset + (C - start)` within the frame. If no range contains `C`, **the channel is not in this file** — and that is a real, expected state, not an error.

**Inference (L0), and it is load-bearing.** "Channel not present" must be a first-class outcome in ShowMesh's API, not a zero. A per-target render legitimately omits every channel belonging to another controller, and returning 0 for an absent channel is this project's recurring `"ma": null` defect in a fifth disguise: an absent value decoded as a plausible-looking measurement. A surface whose channel range falls outside the file's sparse ranges must report *absent, with a reason*, per ADR-021's absent-evidence rule.

**Inference (L0).** The format does not require sparse ranges to be sorted or non-overlapping. FPP's `SetNewRanges` in xLights merges and coalesces before writing, and `V2FSEQFile::writeHeader` clips ranges to the channel count, so files from that path will be sorted and disjoint — but a parser should not depend on it. Walking in table order accumulating the offset is correct **regardless** of ordering, and is what FPP itself does. Do not "optimise" it into a sorted binary search without first proving ordering.

### 6.1 0-based or 1-based

**Fact.** In the file, sparse range start channels are **0-based**. Four independent confirmations:

1. `readFrame` uses `rng.first` as a direct index into a 0-based full-channel buffer (above).
2. `FSEQFile.cpp` converts explicitly when reading the *other* format: `// ESEQ files use 1 based start channels, offset to start at 0` followed by `modelStart ? modelStart - 1 : modelStart`. The conversion exists precisely because FSEQ is 0-based and ESEQ is not.
3. xLights converts the same direction when synthesising a range from a controller: `uint32_t sc = controller->GetStartChannel() - 1;` (`src-core/controllers/FPP.cpp`). `GetStartChannel()` is the 1-based number the xLights UI shows.
4. Measured: every sparse file inspected whose target is the whole controller carries `(0, N)`, never `(1, N)`.

**Fact.** xLights and FPP **user interfaces** are 1-based. The xLights model start channel, the FPP channel-output start channel, and the "Start Channel" column an operator reads are all 1-based.

**Inference (L0), stated as a warning rather than a finding.** The `channelRanges` string that xLights parses from FPP is a **string of unclear base on at least one path**. `PrepareUploadSequence` parses it with `strtol` and uses the value *verbatim* as the sparse start — no `-1`. On the path where xLights synthesises the string itself it subtracts 1 first, so that path is 0-based. But FPP's own `GetConfiguredOutputRanges` (`src/MultiSync.cpp`, `master`) builds ranges from `co-universes.json`'s `startChannel`, which is 1-based in that file, and `createRanges` formats it unchanged. **Whether every FPP-reported `channelRanges` is 0-based has not been established here, and a one-channel offset would be invisible in an RGB render.** This does not change how ShowMesh *parses* a file — the file's own ranges are 0-based and unambiguous — but it means ShowMesh must never reconstruct a target's expected range from FPP's `channelRanges` and compare it to the file's ranges as an equality check without first settling this. It is an open item (§12).

**Derived rule for ShowMesh.** Store and compute in 0-based absolute channels internally. Convert at the operator surface only, and label the surface as 1-based so it matches what xLights shows. ADR-027's "manual channel ranges are a permanent first-class path" means an operator will type a start channel; that number will be 1-based.

## 7. Variable headers

**Fact.** Variable headers live between the fixed header length (offset 8) and the channel data offset (offset 4). Layout per entry:

| Offset | Size | Field |
|---:|---:|---|
| 0 | 2 | Total entry length, **including these 4 bytes** |
| 2 | 2 | Two-character code |
| 4 | len−4 | Data |

**Fact.** `FSEQ_VARIABLE_HEADER_SIZE = 4`. An entry with `length <= 4` carries no data and advances the cursor by exactly 4. The parse loop condition is `while (readIndex + 4 < header.size())`, which is what stops it walking into the 0–3 bytes of alignment padding.

**Fact.** Known codes: `mf` media filename (NUL-terminated string), `sp` sequence producer (NUL-terminated string, e.g. `xLights Macintosh 2025.10.1`), `FC` FPP commands, `FE` FPP effects, `ED` extended data. FPP `master` adds `XS`, `XN`, `XR` — zstd-compressed copies of the xLights `.xsq`, `xlights_networks.xml`, and `xlights_rgbeffects.xml`. **These were not in 8.4.2 and they can be large**; xLights strips them for older FPPs and for non-FPP targets.

**Fact — the `ED` indirection, FSEQ 2.2.** Because the channel data offset is a `uint16`, the whole header cannot exceed 65535 bytes. From 2.2, an oversized header is stored out of line:

| Offset | Size | Field |
|---:|---:|---|
| 0 | 2 | `18` |
| 2 | 2 | `ED` |
| 4 | 2 | the real two-character code |
| 6 | 8 | `uint64` absolute file offset of the data |
| 14 | 4 | `uint32` data length |

The data itself normally sits **after the channel data**. So an `ED` entry is 18 bytes in the header and points somewhere past the end of the frame data.

**Fact.** Answering the question directly: **variable headers do not have to be parsed to find the channel data.** Byte 4 gives the channel data offset outright, and the block table and sparse table are located from the fixed 32-byte header and the two counts. Track B can skip variable-header parsing entirely and still read frames correctly.

**Inference (L0).** ShowMesh should nonetheless parse `mf` and `sp`, because they are the only provenance the file carries — `sp` names the exact xLights build that wrote it, which is the first thing anyone will want when a file misbehaves. `ED` handling can be deferred: an unparsed `ED` entry costs nothing because it is fixed-length and self-skipping. What must **not** happen is treating an `ED` file-offset as if it were inline data.

## 8. Pure-Go feasibility

**Fact.** `github.com/klauspost/compress/zstd` is a **pure-Go** zstd implementation. Its own documentation states "This package is pure Go", with optional amd64 assembly disabled by the `noasm` build tag. Version v1.19.2, published 2026-08-05 (pkg.go.dev, accessed 2026-08-17). It exposes `Decoder.DecodeAll(input, dst []byte) ([]byte, error)` for stateless decoding of a complete frame from a byte slice, safe for concurrent use.

**Fact.** FSEQ zstd blocks are **standard, complete, self-contained zstd frames**. Two independent confirmations: the writer terminates every block with `ZSTD_compressStream2(..., ZSTD_e_end)` before starting the next, and all 600 computed block offsets in a real file begin with the zstd frame magic `28 B5 2F FD`. `DecodeAll` per block is therefore the correct API — no streaming decoder state carries across blocks, no dictionary, no skippable-frame handling.

**Fact.** `DecodeAll` caps a single decode at 1 GiB. FSEQ blocks are sized around a 64 KiB target (`V2FSEQ_OUT_COMPRESSION_BLOCK_SIZE`); the largest decompressed block measured here was 180,964 bytes. The cap is not reachable in practice.

**Fact.** zlib (compression type 2) is covered by the standard library's `compress/zlib` — also pure Go. FPP writes zlib blocks with `deflateInit2(..., -MAX_WBITS, ...)`-style raw handling in its own handler; **this record did not read the zlib handler's framing in detail** and cannot state whether each zlib block is a self-contained zlib stream with a header or a raw deflate stream. Since xLights does not write zlib by default (§9), this is deferred rather than answered. **Do not guess.**

**Fact.** Nothing in FSEQ parsing requires CGo. There is no vendor library, no proprietary runtime, and no format feature outside what `encoding/binary` plus `klauspost/compress/zstd` provides. This is unlike the NDI question (RES-006), which genuinely does turn on a native runtime.

**Inference (L0).** `klauspost/compress` is a widely used pure-Go module and adding it does not violate ADR-012's constraint, which is specifically about CGo-free static builds and clean arm64 cross-compilation. It should still be added deliberately, since the agent runs on the media nodes and the coordinator image is distroless.

## 9. What xLights actually writes

**Fact, from `src-core/controllers/FPP.cpp`.** The upload type enumeration is stated in a comment in the source:

```
// 0 - V1
// 1 - V2 zstd
// 2 - V2 sparse zstd
// 3 - V2 sparse uncompressed
// 4 - V2 uncompressed
// 5 - V2 zlib
// 6 - V2 sparse zlib
```

`PrepareUploadSequence(file, seq, media, FSEQ_Version, ctype, sparse)` takes the version, compression type and sparseness as parameters chosen by the FPP Connect dialog.

**Fact.** For a **sparse** upload, xLights builds the sparse ranges from the target's `ranges` string (`start-end` pairs, comma separated), converting each to `(start, end - start + 1)`, and sets them on the output `V2FSEQFile` before `writeHeader()`. Where the source is an effect sequence (marked with an `eS` variable header) it intersects the controller's ranges with the source's own sparse ranges, and **skips the upload entirely** if the intersection is empty.

**Fact.** For a **non-sparse zstd** v2 upload to an FPP target, xLights does not re-render at all — it uploads the original file byte for byte (`outputFile = file; outputFileIsOriginal = true;`).

**Fact.** `enableMinorVersionFeatures(2)` is called for FPP 7.0 and later, which is what produces the **2.2** minor version and enables the `ED` extended-header path and the 4095-block ceiling.

**Fact.** Default compression level is **2**, dropped to a fast level (`-5` on modern zstd) for Pi Zero / Pi Model A / Pi Model B targets and for Beagle targets above 20,000 or 50,000 channels. Level does not affect parsing.

**Fact, measured across 198 real files** written by xLights 2025.08, 2025.10.1 and 2025.11:

- **Every single file** is `PSEQ`, version **2.2**, compression type **1 (zstd)**. Zero v1 files, zero uncompressed, zero zlib.
- Step time is **25 ms** on 197 files and 50 ms on one.
- Both flag bytes (19 and 23) are `0` everywhere.
- Sparse-range counts observed: 0 (the xLights working file), 1 (the overwhelmingly common per-target render), 2, and 13.
- The files sitting in an actual FPP host's `sequences/` directory are the sparse ones. A representative pair, both targeting the same controller: `(0, 45241)` — a single range, starting at absolute channel 0, `channelCount` in the header equal to `45241`.

**Conclusion for Track B.** The file ShowMesh will receive is **v2.2, zstd, one or a small number of sparse ranges**. A parser that supports exactly that is sufficient for the show. It should still *detect* and cleanly reject v1, ESEQ, zlib and unknown compression rather than misread them, but implementing them is not day-0 work.

**Inference (L0).** The `mf` header in these files carries the **authoring machine's absolute path** (e.g. `/Users/…/Sequences/kpop/…`). It is not a path any node can resolve and must never be treated as one. It is provenance.

## 10. Gotchas

Each of these was either read in source or measured; none is speculative.

1. **The block table stores lengths, not offsets.** Offsets are a running sum from the channel data offset. Zero-length entries are skipped without advancing.
2. **The declared block count exceeds the used block count.** Measured 603/600, 4003/4000, 3466/3464. The writer reserves slots before it knows how many it needs.
3. **The last block is short.** Measured: a 4-frames-per-block file whose final block contains 1 frame, because `firstFrame + 4` exceeds the frame count and FPP clamps. Never assume a uniform block size.
4. **Block 0 is deliberately different** — closed at frame 10 by an unconditional rule and compressed at a different level. Do not derive frames-per-block from it.
5. **Step time is one byte.** 255 ms is the ceiling. There is no sub-millisecond field and no fractional part, so a nominally 33.33 ms (30 fps) sequence is stored as `33` and drifts 0.33 ms per frame against wall clock — 1.2 seconds per hour. ShowMesh must not reconstruct absolute times by multiplying step time by frame index and expect it to match a media file. The lighting timeline's authority is MultiSync, not arithmetic on this byte (ADR-001, constraint 9).
6. **Channel count is per frame in this file, not the show's channel count.** With sparse ranges it is the sum of range lengths. `getMaxChannel()` — the highest absolute channel the file describes — is `max(channelCount, max(start + length))`, a different number.
7. **Channel count need not be a multiple of 3.** Nothing in the format knows about RGB. Measured `45241`, `22876`, `19884`, `693120`, `720404` — several not divisible by 3. A sparse range boundary can land mid-pixel, and DMX/servo/relay channels are single-channel by nature. **A surface extractor that assumes triplets will silently shear its colours on a range that starts off a pixel boundary.** Channel-to-pixel mapping is ShowMesh's configuration problem (ADR-027 manual ranges), not something the file answers.
8. **A `uint16` channel data offset means a hard 65535-byte header budget** shared by the block table, the sparse table and the variable headers. This is why `ED` exists. A 4095-block table alone is 32,760 bytes.
9. **An absent channel is not a zero channel.** §6.
10. **`ESEQ` shares the entry point.** `openFSEQFile` accepts `E`, `P` and `F` as the first byte. An ESEQ file has a 20-byte header, a hardcoded 50 ms step time, a frame count *derived from the file size*, and a 1-based start channel. Anything that treats it as FSEQ will read garbage. Reject by magic, explicitly.
11. **The header-length cross-check at offset 8 is advisory in FPP and should be fatal in ShowMesh.** FPP logs and continues.
12. **`m_uniqueId` is a write timestamp, not a content identity** (§4). ADR-028's hash is not optional.

## 11. What this record does not establish

### 11.1 Version coverage

The local tree read in full is FPP **8.4.2**. The deployed fleet runs FPP **9.4**. The FSEQ header layout, all three size constants, the block-count bit split, the sparse-range widths and the writer's byte assignments were diffed against FPP **`master`** (`ac141d68`) and are **byte-for-byte identical**. The differences on `master` are file locking, an ESEQ divide-by-zero guard, lazy loading of `ED` data, and the new `XS`/`XN`/`XR` codes. **None touches an offset, a width, or an endianness.** 9.4 sits between the two versions read, so the layout is bracketed rather than assumed — but 9.4 itself was not read.

### 11.2 Not verified at all

- The zlib block framing (§8). Deferred, not answered.
- Whether every FPP-reported `channelRanges` string is 0-based (§6.1). This is a real open question with a one-channel failure mode.
- Everything about performance: no timing, no memory figure, no answer to whether decoding a 180 KB block per 25 ms tick on OptiPlex 7040-class hardware is comfortable. **That belongs to RES-004 and only a running renderer can answer it.**
- Any behaviour of a file written by anything other than xLights 2025.08 / 2025.10.1 / 2025.11.
- Files with more than 255 sparse ranges, or with any sparse range above channel 16,777,215. Neither was observed; both are format-representable edge cases the reader should reject explicitly rather than wrap around silently.

## 12. Open items

- **OI-1. Closed at L1 on 2026-08-25**, by source reading rather than by the deployed-host inspection this item proposed. The `channelRanges` string is `start-end` with an **inclusive, 0-based** end on the path that matters here: xLights parses the second number as an end and computes `end - start + 1`, pushes the pair straight into the file's 0-based sparse table, and converts down from its own 1-based start channel when it synthesises the string itself. Evidence, permalinks and the consequence for `show.surface`'s 1-based range are in [RES-003](RES-003-xlights-fpp-connect-compatibility.md) §10.1, pinned to xLights `ae379c0408ab39f3de265aea13c326bf48ab84b7`.

  **What stays open, and it is narrower than this item was.** §13.3's warning that no *FPP-reported* string was ever compared against the file it produced is untouched: FPP builds its own ranges from `co-universes.json`, whose `startChannel` is 1-based, and formats it unchanged. So a range **ShowMesh advertises** is now settled and a formatter can be written against it, while an equality check against a range **an FPP host reports** must still be built as containment with the offset stated. Nothing here rose above L1 and nothing ran against a live xLights or a deployed FPP.
- **OI-2.** Establish the zlib block framing if ShowMesh is ever to accept a zlib FSEQ; until then, reject compression type 2 with a clear message rather than a misparse.
- **OI-3.** Decide whether the parser exposes "channel absent from this file" as a distinct state through to the API surface. §6 argues it must.
- **OI-4.** RES-004 owns the decode-cost measurement. Nothing in this record licenses a frame-rate claim.

## 13. Addendum: findings from building `pkg/fseq` (Track B seam B3, 2026-08-17)

Everything in this section was measured while implementing and testing the parser this record specifies. It corrects one inference in §6 and independently confirms this record's own numbers against the owner's known-good decode of a second real file. The record's own status line is left as the orchestrator's call; this section only states what was found.

### 13.1 Adjacent sparse ranges can overlap by one channel in a real, deployed file — §6's "sorted and disjoint" inference does not hold

Measured on two on-disk copies of `We Three Kings by Tommee Profitt & We The Kingdom [Lyric Video] [cwBBSL14UTs] v3.fseq`, pulled from two different FPP hosts' own `sequences/` directories (`.../Haloween Lighting/FPP-01/sequences/` and `.../Lightingn Base /FPP-01/sequences/`): the sparse table is `[{start:1, length:2187}, {start:2187, length:10503}]`. Range 0 covers absolute channels 1..2187 inclusive; range 1 starts at 2187. Channel 2187 is claimed by both ranges.

Checked byte-for-byte across all 10,023 frames of the file: the two copies of channel 2187 (one reached via range 0's offset, one via range 1's) never disagreed — 2,400 non-zero frames, 0 mismatches. This is expected: xLights' writer copies each range from one shared source buffer (`write(&data[a.first], a.second)` per range, §9), so an overlapping channel is the same source value written twice, not conflicting data.

This means §6's inference ("files from that path will be sorted and disjoint... FPP's `SetNewRanges` in xLights merges and coalesces before writing") is **not correct for this file**. Whatever xLights path wrote this particular render did not coalesce the boundary.

**A correction to §6's stated resolution rule.** §6 paraphrases the read side as "the *first* range with `start <= C < start+length`" claims a channel. Re-reading the quoted `UncompressedFrameData::readFrame` loop: it is an unconditional `memcpy(&data[rng.first], ...)` per range **in table order**, so for two ranges that both claim one absolute channel, the **later** range in table order overwrites the earlier one's byte in the reassembled buffer — last-table-order-wins, not first. `pkg/fseq`'s `ChannelRange` was initially built the way §6 describes (accumulate every range's overlap, then reject if the union doesn't tile the request with no double-coverage), and this exact file caused it to wrongly refuse a request that should have succeeded — a real per-target render, the shape a render node will actually receive, being rejected by an over-strict reading of an L0 inference. It now resolves overlaps last-table-order-wins, matching the C++, and still refuses a genuine gap (a channel no range claims at all) exactly as before.

### 13.2 Cross-check against a second real file, decoded independently by the owner (not with this package)

The owner rendered and independently hand-decoded the header and sparse table of `~/showmesh-fseq-samples/kpop 2026 MH Test.fseq` (303 MB, a full/non-sparse render that nonetheless carries 13 sparse ranges) before `pkg/fseq` was pointed at it. Every reported value matched what `pkg/fseq.Open` computes: `channelDataOffset=27992`, `stdHeaderLen=27838` (closing exactly as `32 + 3466*8 + 13*6`, independently confirming the 8-byte block entry, 6-byte sparse entry, and 12-bit block-count split on a real file), `channelsPerFrame=698120` (matching the sum of the 13 sparse-range lengths exactly), `frames=13853`, `stepTime=25`, zstd, and all 13 `(start, length)` pairs and their derived frame-data offsets. The declared block count (3466) versus this package's used (non-zero-length) count (3464) reproduces the exact 3466/3464 pair §5 and §10.2 already cite as a measured example — on this same file.

Two real xLights UI (1-based) surface start channels supplied by the owner — matrix 1 at 25410, matrix 2 at 505410 — converted to this package's 0-based space (25409, 505409) and resolved through `resolveSegments`, land at frame-data offsets 24923 and 504923 respectively, matching the owner's independently computed values exactly. This is also the file that exercises real, non-hypothetical gaps: channels 735..740, 855..866 and 891..896 are genuinely absent from this render (between its sparse ranges) and `pkg/fseq.ChannelRange` refuses each with `*ErrChannelRangeNotCovered` rather than returning zeros.

## 14. Citations

Every remote source accessed **2026-08-17**.

- FPP `8.4.2`, `src/fseq/FSEQFile.cpp` and `src/fseq/FSEQFile.h` — local tree, `~/Downloads/fpp-8.4.2/`. Primary source for §4, §5, §6, §7.
- FPP `8.4.2`, `docs/FSEQ_Sequence_File_Format.txt` — local tree. FPP's own prose spec; corroborates §4 field-for-field.
- FPP `master`, `src/fseq/FSEQFile.cpp` — <https://github.com/FalconChristmas/fpp/blob/master/src/fseq/FSEQFile.cpp>, commit `ac141d68a7a23482594ede116acdc72d64dbe915`, fetched via `gh api`. §11.1.
- FPP `master`, `src/MultiSync.cpp` — <https://github.com/FalconChristmas/fpp/blob/master/src/MultiSync.cpp>, same commit. §6.1.
- FPP `master`, `docs/FSEQ_Sequence_File_Format.txt` — <https://github.com/FalconChristmas/fpp/blob/master/docs/FSEQ_Sequence_File_Format.txt>. Requested; the fetch returned a paraphrase, so **nothing in this record rests on it**.
- xLights `master`, `src-core/controllers/FPP.cpp` — <https://github.com/smeighan/xLights/blob/master/src-core/controllers/FPP.cpp>, default-branch HEAD, fetched via `gh api`. §9 and §6.1.
- `github.com/klauspost/compress/zstd` v1.19.2 — <https://pkg.go.dev/github.com/klauspost/compress/zstd>. §8.
- 198 `.fseq` artifacts under `~/Documents` and `~/Downloads` on the owner's machine, written by xLights 2025.08 / 2025.10.1 / 2025.11, parsed 2026-08-17 with a throwaway reader. §5, §9, §10. **These are the owner's real show files; they were read and never written.**

### 13.3 What the real-file work did and did not settle about OI-1

**Settled:** the surface-to-file boundary. `show.surface.channelRange.startChannel`
is validated `>= 1` and is the operator's xLights UI number, so it is **1-based**,
while the file's range table is **0-based**. The conversion lives in exactly one
place at the caller's boundary; `pkg/fseq` is 0-based throughout, matching the
file. The owner's two real start channels resolving to the expected frame-data
offsets is the evidence.

**Not settled: OI-1 itself.** That question is whether every *FPP-reported*
`channelRanges` string is 0-based, and it is untouched by any of this, because
no FPP-reported string was compared against the file it produced. It does not
affect the parser. It blocks any equality check between an operator-typed
surface range and a file's coverage, and a one-channel error there would render
as a slight colour cast rather than a broken image. Until it is closed, build
that check as **containment with the offset stated**, never as equality.

package audio

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"time"
)

const (
	wavRIFFID = "RIFF"
	wavWAVEID = "WAVE"
	wavFmtID  = "fmt "
	wavDataID = "data"

	wavFormatPCM = 1

	// wavMaxFmtChunk bounds the fmt chunk read so a corrupt size cannot force a large allocation.
	wavMaxFmtChunk = 4096
)

// wavHeaderDuration walks the RIFF/WAVE chunks read from r and computes the
// PCM duration from the fmt chunk's byte rate and the data chunk's size. It
// returns ok=false for anything that is not a well-formed PCM WAV file, so
// callers fall through to their existing behavior rather than trusting a
// guess. A data chunk declaring size 0 or 0xFFFFFFFF (a streamed writer that
// never rewrote its header) is measured as fileSize minus the data chunk's
// offset instead.
func wavHeaderDuration(r io.Reader, fileSize int64) (time.Duration, bool) {
	br := bufio.NewReader(r)

	var riff [12]byte
	if _, err := io.ReadFull(br, riff[:]); err != nil {
		return 0, false
	}
	if string(riff[0:4]) != wavRIFFID || string(riff[8:12]) != wavWAVEID {
		return 0, false
	}

	offset := int64(len(riff))
	var byteRate uint32
	haveFmt := false
	var dataSize uint32
	var dataOffset int64
	haveData := false

	for !haveData {
		var chunkHeader [8]byte
		if _, err := io.ReadFull(br, chunkHeader[:]); err != nil {
			break
		}
		offset += int64(len(chunkHeader))
		chunkID := string(chunkHeader[0:4])
		chunkSize := binary.LittleEndian.Uint32(chunkHeader[4:8])

		switch chunkID {
		case wavFmtID:
			if chunkSize > wavMaxFmtChunk {
				return 0, false
			}
			body := make([]byte, chunkSize)
			if _, err := io.ReadFull(br, body); err != nil {
				return 0, false
			}
			offset += int64(chunkSize)
			if len(body) < 16 {
				return 0, false
			}
			audioFormat := binary.LittleEndian.Uint16(body[0:2])
			channels := binary.LittleEndian.Uint16(body[2:4])
			sampleRate := binary.LittleEndian.Uint32(body[4:8])
			byteRate = binary.LittleEndian.Uint32(body[8:12])
			bitsPerSample := binary.LittleEndian.Uint16(body[14:16])
			if audioFormat != wavFormatPCM {
				return 0, false
			}
			if byteRate == 0 && sampleRate > 0 && channels > 0 && bitsPerSample > 0 {
				byteRate = sampleRate * uint32(channels) * uint32(bitsPerSample) / 8
			}
			haveFmt = true
			if chunkSize%2 == 1 {
				if _, err := br.Discard(1); err != nil {
					return 0, false
				}
				offset++
			}
		case wavDataID:
			dataSize = chunkSize
			dataOffset = offset
			haveData = true
		default:
			skip := int64(chunkSize)
			if chunkSize%2 == 1 {
				skip++
			}
			if _, err := io.CopyN(io.Discard, br, skip); err != nil {
				return 0, false
			}
			offset += skip
		}
	}

	if !haveFmt || !haveData || byteRate == 0 {
		return 0, false
	}

	dataBytes := uint64(dataSize)
	if dataSize == 0 || dataSize == 0xFFFFFFFF {
		if fileSize <= dataOffset {
			return 0, false
		}
		dataBytes = uint64(fileSize - dataOffset)
	}

	seconds := float64(dataBytes) / float64(byteRate)
	return time.Duration(seconds * float64(time.Second)), true
}

// wavFileDuration opens path and reports its PCM duration from its RIFF/WAVE
// header. See [wavHeaderDuration].
func wavFileDuration(path string) (time.Duration, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return 0, false
	}
	return wavHeaderDuration(f, info.Size())
}

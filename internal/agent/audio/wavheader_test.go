package audio

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// buildWAV assembles a minimal RIFF/WAVE file: a fmt chunk (PCM, the given
// format) followed by extraChunks (each already including its own 8-byte
// chunk header), then a data chunk of dataBytes bytes of silence. dataSize
// overrides the size field written into the data chunk header (used to
// fabricate the streamed-writer 0/0xFFFFFFFF case); pass -1 to use
// len(dataBytes).
func buildWAV(t *testing.T, sampleRate, channels, bitsPerSample int, extraChunks []byte, dataBytes []byte, dataSizeOverride int64) []byte {
	t.Helper()

	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	fmtBody := &bytes.Buffer{}
	_ = binary.Write(fmtBody, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(fmtBody, binary.LittleEndian, uint16(channels))
	_ = binary.Write(fmtBody, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(fmtBody, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(fmtBody, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(fmtBody, binary.LittleEndian, uint16(bitsPerSample))

	body := &bytes.Buffer{}
	body.WriteString("WAVE")
	body.WriteString("fmt ")
	_ = binary.Write(body, binary.LittleEndian, uint32(fmtBody.Len()))
	body.Write(fmtBody.Bytes())
	body.Write(extraChunks)

	dataSize := dataSizeOverride
	if dataSize < 0 {
		dataSize = int64(len(dataBytes))
	}
	body.WriteString("data")
	_ = binary.Write(body, binary.LittleEndian, uint32(dataSize))
	body.Write(dataBytes)

	out := &bytes.Buffer{}
	out.WriteString("RIFF")
	_ = binary.Write(out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// listChunk builds a LIST chunk of the given payload, for tests proving a
// chunk before data is skipped rather than misread as data.
func listChunk(payload string) []byte {
	buf := &bytes.Buffer{}
	buf.WriteString("LIST")
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(payload)))
	buf.WriteString(payload)
	if len(payload)%2 == 1 {
		buf.WriteByte(0)
	}
	return buf.Bytes()
}

func TestWAVHeaderDuration(t *testing.T) {
	silence := func(n int) []byte { return make([]byte, n) }

	cases := []struct {
		name       string
		data       []byte
		fileSize   int64 // 0 means len(data)
		wantOK     bool
		wantAround time.Duration // checked within 1ms when wantOK
	}{
		{
			name:   "48k 16-bit stereo",
			data:   buildWAV(t, 48000, 2, 16, nil, silence(48000*2*2), -1), // 1 second of audio
			wantOK: true, wantAround: time.Second,
		},
		{
			name:   "extra LIST chunk before data",
			data:   buildWAV(t, 48000, 2, 16, listChunk("INFOICRD2026-09-18"), silence(48000*2*2/2), -1), // 0.5s
			wantOK: true, wantAround: 500 * time.Millisecond,
		},
		{
			name:   "truncated header",
			data:   buildWAV(t, 48000, 2, 16, nil, silence(1000), -1)[:20],
			wantOK: false,
		},
		{
			name:   "not RIFF",
			data:   []byte("this is not a wav file at all, just plain text"),
			wantOK: false,
		},
		{
			name:   "streamed writer, data size 0",
			data:   buildWAV(t, 48000, 2, 16, nil, silence(48000*2*2), 0), // header claims 0 bytes, 1s actually present
			wantOK: true, wantAround: time.Second,
		},
		{
			name:   "streamed writer, data size 0xFFFFFFFF",
			data:   buildWAV(t, 48000, 2, 16, nil, silence(48000*2*2), 0xFFFFFFFF),
			wantOK: true, wantAround: time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fileSize := tc.fileSize
			if fileSize == 0 {
				fileSize = int64(len(tc.data))
			}
			got, ok := wavHeaderDuration(bytes.NewReader(tc.data), fileSize)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got duration %v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			diff := got - tc.wantAround
			if diff < 0 {
				diff = -diff
			}
			if diff > time.Millisecond {
				t.Errorf("duration = %v, want around %v", got, tc.wantAround)
			}
		})
	}
}

func TestWAVHeaderDurationNonPCMFallsThrough(t *testing.T) {
	fmtBody := &bytes.Buffer{}
	_ = binary.Write(fmtBody, binary.LittleEndian, uint16(3)) // IEEE float, not PCM
	_ = binary.Write(fmtBody, binary.LittleEndian, uint16(2))
	_ = binary.Write(fmtBody, binary.LittleEndian, uint32(48000))
	_ = binary.Write(fmtBody, binary.LittleEndian, uint32(48000*2*4))
	_ = binary.Write(fmtBody, binary.LittleEndian, uint16(8))
	_ = binary.Write(fmtBody, binary.LittleEndian, uint16(32))

	body := &bytes.Buffer{}
	body.WriteString("WAVE")
	body.WriteString("fmt ")
	_ = binary.Write(body, binary.LittleEndian, uint32(fmtBody.Len()))
	body.Write(fmtBody.Bytes())
	body.WriteString("data")
	_ = binary.Write(body, binary.LittleEndian, uint32(400))
	body.Write(make([]byte, 400))

	out := &bytes.Buffer{}
	out.WriteString("RIFF")
	_ = binary.Write(out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())

	_, ok := wavHeaderDuration(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if ok {
		t.Fatal("ok = true, want false for a non-PCM format tag")
	}
}

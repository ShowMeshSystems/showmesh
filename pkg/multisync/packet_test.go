package multisync

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
)

// All packet bytes in this file are hand-built from the byte offset table
// in doc.go (itself extracted from FPP's own docs/ControlProtocol.txt and
// src/MultiSync.h / MultiSync.cpp, accessed 2026-08-10), not produced by
// calling this package's own encoders. That is deliberate: a decode test
// whose expected input comes from the package's own EncodeSync/EncodePing
// would only prove the two are self-consistent, not that either matches
// the real wire format.
//
// Where encoders are exercised (TestEncodeSyncMatchesGolden,
// TestEncodePingMatchesGolden, and the round-trip tests), the golden bytes
// for the "matches golden" cases are the very same hand-built bytes used
// for decode tests elsewhere in this file.

// --- Header ---

func TestDecodeHeader(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		wantErr error // checked with errors.Is; nil means success
	}{
		{
			name: "valid blank header, no body",
			in:   []byte{'F', 'P', 'P', 'D', 0x03, 0x00, 0x00},
		},
		{
			name:    "empty input",
			in:      nil,
			wantErr: ErrNotFPPD,
		},
		{
			name:    "too short for header",
			in:      []byte{'F', 'P'},
			wantErr: ErrTooShort,
		},
		{
			name:    "one byte short of header",
			in:      []byte{'F', 'P', 'P', 'D', 0x03, 0x00},
			wantErr: ErrTooShort,
		},
		{
			name:    "bad magic",
			in:      []byte{'X', 'P', 'P', 'D', 0x03, 0x00, 0x00},
			wantErr: ErrNotFPPD,
		},
		{
			name:    "declared length mismatch, header says 5, body has 2",
			in:      []byte{'F', 'P', 'P', 'D', 0x03, 0x05, 0x00, 0x01, 0x02},
			wantErr: ErrLengthMismatch,
		},
		{
			name:    "declared length mismatch wraps ErrMalformed",
			in:      []byte{'F', 'P', 'P', 'D', 0x03, 0x05, 0x00, 0x01, 0x02},
			wantErr: ErrMalformed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DecodeHeader(tt.in)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("DecodeHeader() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DecodeHeader() error = %v, want errors.Is(err, %v)", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeHeaderBadMagicIsNotMalformed(t *testing.T) {
	// A bad-magic packet is "not ours", not "ours but broken": it must not
	// also satisfy errors.Is(err, ErrMalformed), or a listener trying to
	// distinguish the two cases could not.
	_, _, err := DecodeHeader([]byte{'X', 'P', 'P', 'D', 0x03, 0x00, 0x00})
	if errors.Is(err, ErrMalformed) {
		t.Fatalf("bad magic error unexpectedly wraps ErrMalformed: %v", err)
	}
}

// --- Sync packet (type 0x01) ---

func TestDecodeSync(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		want    SyncPacket
		wantErr error
	}{
		{
			name: "start sequence frame0 seconds0",
			// action=Start(0) fileType=Sequence(0) frame=0 seconds=0.0 "test.fseq\x00"
			in: []byte{
				'F', 'P', 'P', 'D', 0x01, 0x14, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x74, 0x65, 0x73, 0x74, 0x2e, 0x66, 0x73,
				0x65, 0x71, 0x00,
			},
			want: SyncPacket{Action: SyncActionStart, FileType: SyncFileTypeSequence, FrameNumber: 0, SecondsElapsed: 0, Filename: "test.fseq"},
		},
		{
			name: "sync media frame150 seconds5.25, non-zero secondsElapsed",
			// action=Sync(2) fileType=Media(1) frame=150 seconds=5.25 "show.mp4\x00"
			in: []byte{
				'F', 'P', 'P', 'D', 0x01, 0x13, 0x00, 0x02, 0x01, 0x96, 0x00, 0x00,
				0x00, 0x00, 0x00, 0xa8, 0x40, 0x73, 0x68, 0x6f, 0x77, 0x2e, 0x6d, 0x70,
				0x34, 0x00,
			},
			want: SyncPacket{Action: SyncActionSync, FileType: SyncFileTypeMedia, FrameNumber: 150, SecondsElapsed: 5.25, Filename: "show.mp4"},
		},
		{
			name: "stop sequence, empty filename",
			// action=Stop(1) fileType=Sequence(0) frame=0 seconds=0.0 "\x00"
			in: []byte{
				'F', 'P', 'P', 'D', 0x01, 0x0b, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
			want: SyncPacket{Action: SyncActionStop, FileType: SyncFileTypeSequence, FrameNumber: 0, SecondsElapsed: 0, Filename: ""},
		},
		{
			name: "open sequence, filename fills remaining bytes with no null terminator",
			// action=Open(3) fileType=Sequence(0) frame=0 seconds=0.0 "abc" (no trailing 0x00)
			in: []byte{
				'F', 'P', 'P', 'D', 0x01, 0x0d, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x61, 0x62, 0x63,
			},
			want: SyncPacket{Action: SyncActionOpen, FileType: SyncFileTypeSequence, FrameNumber: 0, SecondsElapsed: 0, Filename: "abc"},
		},
		{
			name: "start media, secondsElapsed zero (pre-8.x master mid-show join per RES-002)",
			// action=Start(0) fileType=Media(1) frame=0 seconds=0.0 "holiday.mp4\x00"
			in: []byte{
				'F', 'P', 'P', 'D', 0x01, 0x16, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x68, 0x6f, 0x6c, 0x69, 0x64, 0x61, 0x79,
				0x2e, 0x6d, 0x70, 0x34, 0x00,
			},
			want: SyncPacket{Action: SyncActionStart, FileType: SyncFileTypeMedia, FrameNumber: 0, SecondsElapsed: 0, Filename: "holiday.mp4"},
		},
		{
			name: "truncated, shorter than the fixed 10-byte fields",
			in: []byte{
				'F', 'P', 'P', 'D', 0x01, 0x05, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03,
			},
			wantErr: ErrTruncated,
		},
		{
			name: "invalid UTF-8 filename",
			// action=Start(0) fileType=Sequence(0) frame=0 seconds=0.0, filename bytes 0xFF 0xFE (invalid UTF-8) then null
			in: []byte{
				'F', 'P', 'P', 'D', 0x01, 0x0d, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0xfe, 0x00,
			},
			wantErr: ErrInvalidUTF8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, extra, err := DecodeHeader(tt.in)
			if err != nil {
				t.Fatalf("DecodeHeader() unexpected error: %v", err)
			}
			if h.Type != PacketTypeMultiSync {
				t.Fatalf("Header.Type = %v, want PacketTypeMultiSync", h.Type)
			}
			got, err := DecodeSync(extra)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("DecodeSync() error = %v, want errors.Is(err, %v)", err, tt.wantErr)
				}
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("DecodeSync() error = %v, want it to also wrap ErrMalformed", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeSync() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("DecodeSync() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeSyncZeroLengthBody(t *testing.T) {
	// A Sync packet with a header-declared extraDataLen of 0 has no room
	// for even the fixed fields; it must error, not panic or return a
	// zero-value SyncPacket silently.
	in := []byte{'F', 'P', 'P', 'D', 0x01, 0x00, 0x00}
	h, extra, err := DecodeHeader(in)
	if err != nil {
		t.Fatalf("DecodeHeader() unexpected error: %v", err)
	}
	if h.Type != PacketTypeMultiSync {
		t.Fatalf("Header.Type = %v, want PacketTypeMultiSync", h.Type)
	}
	_, err = DecodeSync(extra)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("DecodeSync() error = %v, want errors.Is(err, ErrTruncated)", err)
	}
}

func TestDecodeSyncFilenameTooLong(t *testing.T) {
	longName := strings.Repeat("a", MaxFilenameLength+1)
	body := append([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, []byte(longName)...)
	body = append(body, 0x00)
	_, err := DecodeSync(body)
	if !errors.Is(err, ErrFieldTooLong) {
		t.Fatalf("DecodeSync() error = %v, want errors.Is(err, ErrFieldTooLong)", err)
	}
}

// syncBodyWithSecondsElapsed hand-builds a minimal Sync packet body (the
// fixed fields plus an empty, null-terminated filename) with the given
// SecondsElapsed bit pattern, using encoding/binary directly rather than
// EncodeSync: this file's convention is that DecodeSync test input is built
// independently of this package's own encoder (see the file's top comment).
func syncBodyWithSecondsElapsed(se float32) []byte {
	body := make([]byte, syncFixedLen+1) // fixed fields + one null terminator byte
	body[0] = byte(SyncActionSync)
	body[1] = byte(SyncFileTypeSequence)
	binary.LittleEndian.PutUint32(body[2:6], 0)
	binary.LittleEndian.PutUint32(body[6:10], math.Float32bits(se))
	return body
}

// TestPingV1MinBodyLenFPPEnforces_MatchesDocumentedValue guards the named
// constant against silent drift from the doc/source contradiction note in
// doc.go: if this ever needs to change, that note (and its byte-offset
// arithmetic) needs to change with it.
func TestPingV1MinBodyLenFPPEnforces_MatchesDocumentedValue(t *testing.T) {
	if pingV1MinBodyLenFPPEnforces != 169 {
		t.Fatalf("pingV1MinBodyLenFPPEnforces = %d, want 169 (see the doc/source contradiction note in doc.go)", pingV1MinBodyLenFPPEnforces)
	}
}

// --- BLOCKER 3: a malformed SecondsElapsed must be rejected at decode,
// before it can ever reach Timeline's float-to-Duration conversion ---

func TestDecodeSync_RejectsNonFiniteOrOutOfRangeSecondsElapsed(t *testing.T) {
	tests := []struct {
		name    string
		se      float32
		wantErr bool
	}{
		{"NaN", float32(math.NaN()), true},
		{"+Inf", float32(math.Inf(1)), true},
		{"-Inf", float32(math.Inf(-1)), true},
		{"far too large (1e30)", 1e30, true},
		{"negative", -1.0, true},
		{"just above the largest accepted value", float32(maxSecondsElapsed) + 1, true},
		{"largest accepted value", float32(maxSecondsElapsed), false},
		{"zero", 0, false},
		{"ordinary mid-show value", 123.456, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := syncBodyWithSecondsElapsed(tt.se)
			_, err := DecodeSync(body)
			if tt.wantErr {
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("DecodeSync(secondsElapsed=%v) error = %v, want errors.Is(err, ErrMalformed)", tt.se, err)
				}
				if !errors.Is(err, ErrSecondsElapsedOutOfRange) {
					t.Fatalf("DecodeSync(secondsElapsed=%v) error = %v, want errors.Is(err, ErrSecondsElapsedOutOfRange)", tt.se, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeSync(secondsElapsed=%v) unexpected error: %v", tt.se, err)
			}
		})
	}
}

func TestEncodeSyncMatchesGolden(t *testing.T) {
	want := []byte{
		'F', 'P', 'P', 'D', 0x01, 0x13, 0x00, 0x02, 0x01, 0x96, 0x00, 0x00,
		0x00, 0x00, 0x00, 0xa8, 0x40, 0x73, 0x68, 0x6f, 0x77, 0x2e, 0x6d, 0x70,
		0x34, 0x00,
	}
	p := SyncPacket{Action: SyncActionSync, FileType: SyncFileTypeMedia, FrameNumber: 150, SecondsElapsed: 5.25, Filename: "show.mp4"}

	got, err := EncodeSync(p)
	if err != nil {
		t.Fatalf("EncodeSync() unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("EncodeSync() = % x, want % x", got, want)
	}
}

func TestSyncRoundTrip(t *testing.T) {
	cases := []SyncPacket{
		{Action: SyncActionStart, FileType: SyncFileTypeSequence, FrameNumber: 0, SecondsElapsed: 0, Filename: "test.fseq"},
		{Action: SyncActionSync, FileType: SyncFileTypeMedia, FrameNumber: 150, SecondsElapsed: 5.25, Filename: "show.mp4"},
		{Action: SyncActionStop, FileType: SyncFileTypeSequence, FrameNumber: 0, SecondsElapsed: 0, Filename: ""},
		{Action: SyncActionOpen, FileType: SyncFileTypeMedia, FrameNumber: 987654, SecondsElapsed: 12.375, Filename: "a/b/c holiday show.mp4"},
	}

	for _, p := range cases {
		encoded, err := EncodeSync(p)
		if err != nil {
			t.Fatalf("EncodeSync(%+v) unexpected error: %v", p, err)
		}
		h, decoded, err := Decode(encoded)
		if err != nil {
			t.Fatalf("Decode(EncodeSync(%+v)) unexpected error: %v", p, err)
		}
		if h.Type != PacketTypeMultiSync {
			t.Fatalf("Header.Type = %v, want PacketTypeMultiSync", h.Type)
		}
		got, ok := decoded.(SyncPacket)
		if !ok {
			t.Fatalf("Decode() payload type = %T, want SyncPacket", decoded)
		}
		if got != p {
			t.Fatalf("round trip mismatch: got %+v, want %+v", got, p)
		}
	}
}

// --- Blank packet (type 0x03) ---

func TestDecodeBlank(t *testing.T) {
	in := []byte{'F', 'P', 'P', 'D', 0x03, 0x00, 0x00}
	h, extra, err := DecodeHeader(in)
	if err != nil {
		t.Fatalf("DecodeHeader() unexpected error: %v", err)
	}
	if h.Type != PacketTypeBlank {
		t.Fatalf("Header.Type = %v, want PacketTypeBlank", h.Type)
	}
	if _, err := DecodeBlank(extra); err != nil {
		t.Fatalf("DecodeBlank() unexpected error: %v", err)
	}
}

// --- Ping / Discover packet (type 0x04) ---

func TestDecodePing(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		want    PingPacket
		wantErr error
	}{
		{
			name: "v3 ping response from an FPP master",
			in: []byte{
				'F', 'P', 'P', 'D', 0x04, 0x26, 0x01, 0x03, 0x00, 0x01, 0x00, 0x09,
				0x00, 0x05, 0x06, 0xc0, 0xa8, 0x01, 0x32, 0x66, 0x70, 0x70, 0x2d, 0x6d,
				0x61, 0x73, 0x74, 0x65, 0x72, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x39, 0x2e, 0x35, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x52, 0x61, 0x73, 0x70, 0x62, 0x65, 0x72,
				0x72, 0x79, 0x20, 0x50, 0x69, 0x20, 0x34, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x30, 0x2d,
				0x34, 0x35, 0x35, 0x2c, 0x35, 0x31, 0x32, 0x2d, 0x31, 0x30, 0x32, 0x34,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00,
			},
			want: PingPacket{
				Version:       3,
				SubType:       PingSubTypePing,
				SystemType:    SystemTypeFPP,
				VersionMajor:  9,
				VersionMinor:  5,
				Mode:          PingModePlayer | PingModeSendingMultiSync,
				IP:            [4]byte{192, 168, 1, 50},
				Hostname:      "fpp-master",
				VersionString: "9.5",
				HardwareType:  "Raspberry Pi 4",
				Ranges:        "0-455,512-1024",
			},
		},
		{
			name: "v3 discover ping from a non-FPP device, IP 0.0.0.0 per etiquette",
			in: []byte{
				'F', 'P', 'P', 'D', 0x04, 0x26, 0x01, 0x03, 0x01, 0xc3, 0x00, 0x04,
				0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x73, 0x68, 0x6f, 0x77, 0x6d,
				0x65, 0x73, 0x68, 0x2d, 0x70, 0x72, 0x6f, 0x62, 0x65, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x30, 0x2e, 0x31, 0x2e, 0x30, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00,
			},
			want: PingPacket{
				Version:       3,
				SubType:       PingSubTypeDiscover,
				SystemType:    SystemTypeESPixelStickESP32,
				VersionMajor:  4,
				VersionMinor:  2,
				Mode:          0,
				IP:            [4]byte{0, 0, 0, 0},
				Hostname:      "showmesh-probe",
				VersionString: "0.1.0",
				HardwareType:  "",
				Ranges:        "",
			},
		},
		{
			name: "short legacy-shaped ping, only the fixed fields and a truncated hostname",
			// version=1 subtype=Ping(0) systemType=FPP(1) major=1 minor=10 mode=Player(0x02) ip=10.0.0.5, hostname bytes "abc" with no null and no further fields at all
			in: []byte{
				'F', 'P', 'P', 'D', 0x04, 0x0f, 0x00, 0x01, 0x00, 0x01, 0x00, 0x01,
				0x00, 0x0a, 0x02, 0x0a, 0x00, 0x00, 0x05, 0x61, 0x62, 0x63,
			},
			want: PingPacket{
				Version:      1,
				SubType:      PingSubTypePing,
				SystemType:   SystemTypeFPP,
				VersionMajor: 1,
				VersionMinor: 10,
				Mode:         PingModePlayer,
				IP:           [4]byte{10, 0, 0, 5},
				Hostname:     "abc",
				// VersionString, HardwareType, Ranges are absent from this
				// short packet and must decode as empty rather than error.
			},
		},
		{
			name: "truncated below the 12-byte minimum (Version through IP)",
			in: []byte{
				'F', 'P', 'P', 'D', 0x04, 0x07, 0x00, 0x03, 0x00, 0x01, 0x00, 0x09,
				0x00, 0x00,
			},
			wantErr: ErrTruncated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, extra, err := DecodeHeader(tt.in)
			if err != nil {
				t.Fatalf("DecodeHeader() unexpected error: %v", err)
			}
			if h.Type != PacketTypePing {
				t.Fatalf("Header.Type = %v, want PacketTypePing", h.Type)
			}
			got, err := DecodePing(extra)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("DecodePing() error = %v, want errors.Is(err, %v)", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodePing() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("DecodePing() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEncodePingMatchesGolden(t *testing.T) {
	want := []byte{
		'F', 'P', 'P', 'D', 0x04, 0x26, 0x01, 0x03, 0x00, 0x01, 0x00, 0x09,
		0x00, 0x05, 0x06, 0xc0, 0xa8, 0x01, 0x32, 0x66, 0x70, 0x70, 0x2d, 0x6d,
		0x61, 0x73, 0x74, 0x65, 0x72, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x39, 0x2e, 0x35, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x52, 0x61, 0x73, 0x70, 0x62, 0x65, 0x72,
		0x72, 0x79, 0x20, 0x50, 0x69, 0x20, 0x34, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x30, 0x2d,
		0x34, 0x35, 0x35, 0x2c, 0x35, 0x31, 0x32, 0x2d, 0x31, 0x30, 0x32, 0x34,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00,
	}
	p := PingPacket{
		Version:       3, // ignored on input, always written as 3
		SubType:       PingSubTypePing,
		SystemType:    SystemTypeFPP,
		VersionMajor:  9,
		VersionMinor:  5,
		Mode:          PingModePlayer | PingModeSendingMultiSync,
		IP:            [4]byte{192, 168, 1, 50},
		Hostname:      "fpp-master",
		VersionString: "9.5",
		HardwareType:  "Raspberry Pi 4",
		Ranges:        "0-455,512-1024",
	}

	got, err := EncodePing(p)
	if err != nil {
		t.Fatalf("EncodePing() unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("EncodePing() = % x\n\nwant % x", got, want)
	}
}

func TestPingRoundTrip(t *testing.T) {
	cases := []PingPacket{
		{
			Version: 3, SubType: PingSubTypePing, SystemType: SystemTypeFPP,
			VersionMajor: 9, VersionMinor: 5, Mode: PingModePlayer | PingModeSendingMultiSync,
			IP: [4]byte{192, 168, 1, 50}, Hostname: "fpp-master", VersionString: "9.5",
			HardwareType: "Raspberry Pi 4", Ranges: "0-455,512-1024",
		},
		{
			Version: 3, SubType: PingSubTypeDiscover, SystemType: SystemTypeShowMesh,
			VersionMajor: 0, VersionMinor: 1, Mode: 0,
			IP: [4]byte{0, 0, 0, 0}, Hostname: "showmesh-probe", VersionString: "0.1.0",
		},
		{
			// Empty fields throughout, still round trips.
			Version: 3, SubType: PingSubTypePing, SystemType: SystemTypeUnknown,
		},
	}

	for _, p := range cases {
		encoded, err := EncodePing(p)
		if err != nil {
			t.Fatalf("EncodePing(%+v) unexpected error: %v", p, err)
		}
		h, decoded, err := Decode(encoded)
		if err != nil {
			t.Fatalf("Decode(EncodePing(%+v)) unexpected error: %v", p, err)
		}
		if h.Type != PacketTypePing {
			t.Fatalf("Header.Type = %v, want PacketTypePing", h.Type)
		}
		got, ok := decoded.(PingPacket)
		if !ok {
			t.Fatalf("Decode() payload type = %T, want PingPacket", decoded)
		}
		want := p
		want.Version = 3 // EncodePing always writes version 3, regardless of input
		if got != want {
			t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
		}
	}
}

func TestEncodePingFieldTooLong(t *testing.T) {
	p := PingPacket{Hostname: strings.Repeat("h", 65)} // field width is 65 including the terminator, so 64 is the max content
	if _, err := EncodePing(p); !errors.Is(err, ErrFieldTooLong) {
		t.Fatalf("EncodePing() error = %v, want errors.Is(err, ErrFieldTooLong)", err)
	}
}

// --- FPP Command packet (type 0x06) ---

func TestDecodeCommand(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		want    CommandPacket
		wantErr error
	}{
		{
			name: "two args, empty host",
			// numArgs=2 host="" command="Stop All Now" args=["a","bee"]
			in: []byte{
				'F', 'P', 'P', 'D', 0x06, 0x15, 0x00, 0x02, 0x00, 0x53, 0x74, 0x6f,
				0x70, 0x20, 0x41, 0x6c, 0x6c, 0x20, 0x4e, 0x6f, 0x77, 0x00, 0x61, 0x00,
				0x62, 0x65, 0x65, 0x00,
			},
			want: CommandPacket{Host: "", Command: "Stop All Now", Args: []string{"a", "bee"}},
		},
		{
			name: "zero args",
			// numArgs=0 host="" command="Ping\x00"
			in: []byte{
				'F', 'P', 'P', 'D', 0x06, 0x07, 0x00, 0x00, 0x00, 0x50, 0x69, 0x6e,
				0x67, 0x00,
			},
			want: CommandPacket{Host: "", Command: "Ping", Args: []string{}},
		},
		{
			name: "missing null terminator on command name",
			// numArgs=0 host="" command="NoNullHere" with no terminating 0x00
			in: []byte{
				'F', 'P', 'P', 'D', 0x06, 0x0c, 0x00, 0x00, 0x00, 0x4e, 0x6f, 0x4e,
				0x75, 0x6c, 0x6c, 0x48, 0x65, 0x72, 0x65,
			},
			wantErr: ErrNoNullTerminator,
		},
		{
			name:    "truncated, empty body",
			in:      []byte{'F', 'P', 'P', 'D', 0x06, 0x00, 0x00},
			wantErr: ErrTruncated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, extra, err := DecodeHeader(tt.in)
			if err != nil {
				t.Fatalf("DecodeHeader() unexpected error: %v", err)
			}
			if h.Type != PacketTypeCommand {
				t.Fatalf("Header.Type = %v, want PacketTypeCommand", h.Type)
			}
			got, err := DecodeCommand(extra)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("DecodeCommand() error = %v, want errors.Is(err, %v)", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeCommand() unexpected error: %v", err)
			}
			if got.Host != tt.want.Host || got.Command != tt.want.Command || len(got.Args) != len(tt.want.Args) {
				t.Fatalf("DecodeCommand() = %+v, want %+v", got, tt.want)
			}
			for i := range got.Args {
				if got.Args[i] != tt.want.Args[i] {
					t.Fatalf("DecodeCommand() Args = %v, want %v", got.Args, tt.want.Args)
				}
			}
		})
	}
}

// --- Plugin packet (type 0x05) ---

func TestDecodePlugin(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want []byte
	}{
		{
			name: "plugin name plus data",
			in: []byte{
				'F', 'P', 'P', 'D', 0x05, 0x0c, 0x00, 0x6d, 0x79, 0x70, 0x6c, 0x75,
				0x67, 0x69, 0x6e, 0x00, 0x01, 0x02, 0x03,
			},
			want: []byte{0x6d, 0x79, 0x70, 0x6c, 0x75, 0x67, 0x69, 0x6e, 0x00, 0x01, 0x02, 0x03},
		},
		{
			name: "zero-length payload",
			in:   []byte{'F', 'P', 'P', 'D', 0x05, 0x00, 0x00},
			want: []byte{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, extra, err := DecodeHeader(tt.in)
			if err != nil {
				t.Fatalf("DecodeHeader() unexpected error: %v", err)
			}
			if h.Type != PacketTypePlugin {
				t.Fatalf("Header.Type = %v, want PacketTypePlugin", h.Type)
			}
			got, err := DecodePlugin(extra)
			if err != nil {
				t.Fatalf("DecodePlugin() unexpected error: %v", err)
			}
			if string(got.Raw) != string(tt.want) {
				t.Fatalf("DecodePlugin().Raw = % x, want % x", got.Raw, tt.want)
			}
		})
	}
}

// --- Top-level Decode dispatch ---

func TestDecodeDispatch(t *testing.T) {
	blank := []byte{'F', 'P', 'P', 'D', 0x03, 0x00, 0x00}
	h, payload, err := Decode(blank)
	if err != nil {
		t.Fatalf("Decode() unexpected error: %v", err)
	}
	if h.Type != PacketTypeBlank {
		t.Fatalf("Header.Type = %v, want PacketTypeBlank", h.Type)
	}
	if _, ok := payload.(BlankPacket); !ok {
		t.Fatalf("Decode() payload type = %T, want BlankPacket", payload)
	}
}

func TestDecodeUnknownPacketType(t *testing.T) {
	// 0x02 is FPP's deprecated Event packet type: a header FPPD would
	// happily produce, but not one this package decodes a payload for.
	in := []byte{'F', 'P', 'P', 'D', 0x02, 0x00, 0x00}
	_, _, err := Decode(in)
	if err == nil {
		t.Fatalf("Decode() error = nil, want an error for an unrecognized packet type")
	}
	var upte *UnknownPacketTypeError
	if !errors.As(err, &upte) {
		t.Fatalf("Decode() error = %v, want errors.As to find an *UnknownPacketTypeError", err)
	}
	if upte.Type != PacketType(0x02) {
		t.Fatalf("UnknownPacketTypeError.Type = 0x%02x, want 0x02", uint8(upte.Type))
	}
	// It must not be mistaken for either "not ours" or "malformed": it is
	// a valid FPPD packet of a type this package simply does not decode.
	if errors.Is(err, ErrNotFPPD) {
		t.Fatalf("unknown packet type error unexpectedly wraps ErrNotFPPD: %v", err)
	}
	if errors.Is(err, ErrMalformed) {
		t.Fatalf("unknown packet type error unexpectedly wraps ErrMalformed: %v", err)
	}

	arbitraryUnhandled := []byte{'F', 'P', 'P', 'D', 0x07, 0x00, 0x00}
	_, _, err = Decode(arbitraryUnhandled)
	if !errors.As(err, &upte) {
		t.Fatalf("Decode() error = %v, want errors.As to find an *UnknownPacketTypeError", err)
	}
	if upte.Type != PacketType(0x07) {
		t.Fatalf("UnknownPacketTypeError.Type = 0x%02x, want 0x07", uint8(upte.Type))
	}
}

// --- String() rendering of unknown enum values ---

func TestUnknownValueStringers(t *testing.T) {
	if got, want := SyncAction(7).String(), "SyncAction(7)"; got != want {
		t.Errorf("SyncAction(7).String() = %q, want %q", got, want)
	}
	if got, want := SyncFileType(7).String(), "SyncFileType(7)"; got != want {
		t.Errorf("SyncFileType(7).String() = %q, want %q", got, want)
	}
	if got, want := PacketType(0x99).String(), "PacketType(0x99)"; got != want {
		t.Errorf("PacketType(0x99).String() = %q, want %q", got, want)
	}
	if got, want := SystemType(0x50).String(), "SystemType(0x50)"; got != want {
		t.Errorf("SystemType(0x50).String() = %q, want %q", got, want)
	}
	if got, want := PingSubType(9).String(), "PingSubType(9)"; got != want {
		t.Errorf("PingSubType(9).String() = %q, want %q", got, want)
	}
	if got, want := PingMode(0x10).String(), "PingMode(0x10)"; got != want {
		t.Errorf("PingMode(0x10).String() = %q, want %q", got, want)
	}

	// Known values render by name, and combined PingMode flags join.
	if got, want := SyncAction(0).String(), "Start"; got != want {
		t.Errorf("SyncAction(0).String() = %q, want %q", got, want)
	}
	if got, want := PacketTypeMultiSync.String(), "MultiSync"; got != want {
		t.Errorf("PacketTypeMultiSync.String() = %q, want %q", got, want)
	}
	if got, want := (PingModePlayer | PingModeSendingMultiSync).String(), "Player|SendingMultiSync"; got != want {
		t.Errorf("combined PingMode.String() = %q, want %q", got, want)
	}
}

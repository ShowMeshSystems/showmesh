package agent

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// These offsets are taken directly from the byte offset table in
// pkg/multisync/doc.go, not from the encoder's own field-width constants,
// so this test catches a layout change in either place rather than only
// confirming the encoder agrees with itself.
const (
	wireHeaderLen        = 7
	wireOffSystemType    = 2
	wireOffVersionMajor  = 3
	wireOffVersionMinor  = 5
	wireOffMode          = 7
	wireOffVersionString = 77
	wireOffRanges        = 159
)

// readWireCString reads a null-terminated string from body starting at
// offset, without assuming a null byte is present.
func readWireCString(t *testing.T, body []byte, offset int) string {
	t.Helper()
	if offset >= len(body) {
		t.Fatalf("offset %d is past the end of the body (%d bytes)", offset, len(body))
	}
	rest := body[offset:]
	if i := bytes.IndexByte(rest, 0); i >= 0 {
		return string(rest[:i])
	}
	return string(rest)
}

// TestDiscoverResponseWireValues pins the values ADR-044 and RES-003
// section 10 settled, at the exact v3 body offsets pkg/multisync/doc.go
// documents. This is what makes a ShowMesh render node's discover-ping
// reply eligible as an xLights FPP Connect upload target; see FC0.
func TestDiscoverResponseWireValues(t *testing.T) {
	const wantRanges = "0-455,512-1024"

	p := discoverResponse("render-node-1", func() string { return wantRanges })
	packet, err := multisync.EncodePing(p)
	if err != nil {
		t.Fatalf("EncodePing() unexpected error: %v", err)
	}
	body := packet[wireHeaderLen:]

	if got := body[wireOffSystemType]; got != 0x7F {
		t.Errorf("body[%d] (SystemType) = 0x%02x, want 0x7f", wireOffSystemType, got)
	}
	if got := binary.BigEndian.Uint16(body[wireOffVersionMajor : wireOffVersionMajor+2]); got != 9 {
		t.Errorf("body[%d:%d] (VersionMajor) = %d, want 9", wireOffVersionMajor, wireOffVersionMajor+2, got)
	}
	if got := binary.BigEndian.Uint16(body[wireOffVersionMinor : wireOffVersionMinor+2]); got != 5 {
		t.Errorf("body[%d:%d] (VersionMinor) = %d, want 5", wireOffVersionMinor, wireOffVersionMinor+2, got)
	}
	if got := body[wireOffMode]; got != 0x08 {
		t.Errorf("body[%d] (Mode) = 0x%02x, want 0x08 (PingModeRemote) — this byte must stay Remote, per ADR-044 owner ruling 2026-08-25", wireOffMode, got)
	}
	if got := readWireCString(t, body, wireOffVersionString); got != "9.5.0" {
		t.Errorf("VersionString at body[%d] = %q, want %q", wireOffVersionString, got, "9.5.0")
	}
	if got := readWireCString(t, body, wireOffRanges); got != wantRanges {
		t.Errorf("Ranges at body[%d] = %q, want %q", wireOffRanges, got, wantRanges)
	}

	if bytes.Contains(packet, []byte("Falcon")) || bytes.Contains(packet, []byte("Player")) {
		t.Fatalf("encoded packet contains a Falcon Player identity claim: % x", packet)
	}
}

// TestDiscoverResponseEmptyRanges confirms an empty holder produces an
// empty (null-first-byte) Ranges field rather than any placeholder text,
// per RES-003 section 10.1: an empty string is xLights' correct-but-full
// FSEQ fallback, not a defect this seam should paper over.
func TestDiscoverResponseEmptyRanges(t *testing.T) {
	p := discoverResponse("render-node-1", func() string { return "" })
	packet, err := multisync.EncodePing(p)
	if err != nil {
		t.Fatalf("EncodePing() unexpected error: %v", err)
	}
	body := packet[wireHeaderLen:]

	if got := body[wireOffRanges]; got != 0 {
		t.Errorf("body[%d] (Ranges first byte) = 0x%02x, want 0x00 for an empty holder", wireOffRanges, got)
	}
}

// TestDiscoverResponseNilRanges confirms a nil ranges func (the shape
// runMultiSyncListener never actually passes, but discoverResponse's
// contract allows) behaves the same as one returning "".
func TestDiscoverResponseNilRanges(t *testing.T) {
	p := discoverResponse("render-node-1", nil)
	if p.Ranges != "" {
		t.Fatalf("Ranges = %q, want \"\" for a nil ranges func", p.Ranges)
	}
}

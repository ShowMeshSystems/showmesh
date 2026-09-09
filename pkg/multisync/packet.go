package multisync

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// magic is the fixed 4-byte prefix every FPPD control packet starts with.
const magic = "FPPD"

// headerLen is the size in bytes of the common header: magic, packet type,
// and the extra data length. See the byte offset table in doc.go.
const headerLen = 7

// MaxFilenameLength bounds how long a decoded or encoded Sync filename may
// be. This is not a limit FPP itself documents; ExtraDataLen is a uint16, so
// the wire format alone permits a filename tens of kilobytes long. This
// package caps it well below that so a crafted or corrupted packet cannot
// make a caller hold or log an unbounded string just because the length
// byte says so.
const MaxFilenameLength = 4096

// PacketType is the packet type byte at header offset 4.
type PacketType uint8

const (
	PacketTypeMultiSync PacketType = 0x01
	PacketTypeBlank     PacketType = 0x03
	PacketTypePing      PacketType = 0x04
	PacketTypePlugin    PacketType = 0x05
	PacketTypeCommand   PacketType = 0x06
)

// String renders known packet types by name and unknown ones as
// PacketType(0xNN) so a value can always be logged usefully.
func (t PacketType) String() string {
	switch t {
	case PacketTypeMultiSync:
		return "MultiSync"
	case PacketTypeBlank:
		return "Blank"
	case PacketTypePing:
		return "Ping"
	case PacketTypePlugin:
		return "Plugin"
	case PacketTypeCommand:
		return "Command"
	default:
		return fmt.Sprintf("PacketType(0x%02x)", uint8(t))
	}
}

// SyncAction is the sync packet's action byte (Sync packet body offset 0).
type SyncAction uint8

const (
	SyncActionStart SyncAction = 0
	SyncActionStop  SyncAction = 1
	SyncActionSync  SyncAction = 2
	SyncActionOpen  SyncAction = 3
)

// String renders known actions by name and unknown ones as SyncAction(N).
func (a SyncAction) String() string {
	switch a {
	case SyncActionStart:
		return "Start"
	case SyncActionStop:
		return "Stop"
	case SyncActionSync:
		return "Sync"
	case SyncActionOpen:
		return "Open"
	default:
		return fmt.Sprintf("SyncAction(%d)", uint8(a))
	}
}

// SyncFileType is the sync packet's file type byte (Sync packet body
// offset 1).
type SyncFileType uint8

const (
	SyncFileTypeSequence SyncFileType = 0
	SyncFileTypeMedia    SyncFileType = 1
)

// String renders known file types by name and unknown ones as
// SyncFileType(N).
func (t SyncFileType) String() string {
	switch t {
	case SyncFileTypeSequence:
		return "Sequence"
	case SyncFileTypeMedia:
		return "Media"
	default:
		return fmt.Sprintf("SyncFileType(%d)", uint8(t))
	}
}

// SyncPacket is the decoded body of a type 0x01 MultiSync packet: a
// start, stop, sync, or open action against a sequence or media file. See
// the byte offset table in doc.go for the wire layout.
type SyncPacket struct {
	Action         SyncAction
	FileType       SyncFileType
	FrameNumber    uint32
	SecondsElapsed float32
	Filename       string
}

// PingSubType is the ping packet's subtype byte (Ping packet body offset 1),
// distinguishing an unsolicited or answering ping from a discover request
// that asks every listener to ping back.
type PingSubType uint8

const (
	PingSubTypePing     PingSubType = 0x00
	PingSubTypeDiscover PingSubType = 0x01
)

// String renders known subtypes by name and unknown ones as
// PingSubType(N).
func (s PingSubType) String() string {
	switch s {
	case PingSubTypePing:
		return "Ping"
	case PingSubTypeDiscover:
		return "Discover"
	default:
		return fmt.Sprintf("PingSubType(%d)", uint8(s))
	}
}

// PingMode is the ping packet's operating mode bitmask (Ping packet body
// offset 7). Multiple bits may be set at once (for example a full FPP
// instance that is both Player and Sending Multisync), so unlike the other
// enums here it does not have a single canonical name for every value.
type PingMode uint8

const (
	PingModeBridge           PingMode = 0x01
	PingModePlayer           PingMode = 0x02
	PingModeSendingMultiSync PingMode = 0x04
	PingModeRemote           PingMode = 0x08
)

// String renders the set bits joined by "|", or PingMode(0xNN) if none of
// the known bits are set (the value is still 0 or has bits this package
// does not name).
func (m PingMode) String() string {
	var parts []string
	if m&PingModeBridge != 0 {
		parts = append(parts, "Bridge")
	}
	if m&PingModePlayer != 0 {
		parts = append(parts, "Player")
	}
	if m&PingModeSendingMultiSync != 0 {
		parts = append(parts, "SendingMultiSync")
	}
	if m&PingModeRemote != 0 {
		parts = append(parts, "Remote")
	}
	if len(parts) == 0 {
		return fmt.Sprintf("PingMode(0x%02x)", uint8(m))
	}
	return strings.Join(parts, "|")
}

// SystemType identifies the sending device's hardware or software class in
// a Ping packet (body offset 2, "App/Hardware Type" in ControlProtocol.txt).
// FPP's own enum in src/MultiSync.h is much larger than what is named here
// (every Raspberry Pi model, BeagleBone variant, Falcon and Experience
// Lights hardware revision, and so on); this package only names the values
// relevant to non-FPP MultiSync interoperability, per RES-002 and
// src/MultiSync.h (both accessed 2026-08-10). Unnamed values still decode
// and round-trip correctly; String just falls back to SystemType(0xNN) for
// them.
type SystemType uint8

const (
	SystemTypeUnknown SystemType = 0x00
	SystemTypeFPP     SystemType = 0x01 // undetermined FPP hardware

	// SystemTypeOther is FPP's own generic "other systems" bucket
	// (kSysTypeOtherSystem in src/MultiSync.h), the base of the 0xC0-0xFF
	// range ControlProtocol.txt labels "Other systems".
	SystemTypeOther               SystemType = 0xC0
	SystemTypeXSchedule           SystemType = 0xC1
	SystemTypeESPixelStickESP8266 SystemType = 0xC2
	SystemTypeESPixelStickESP32   SystemType = 0xC3
	SystemTypeNonMultiSyncCapable SystemType = 0xF0
	SystemTypeWLED                SystemType = 0xFB
	SystemTypeDIYLEDExpress       SystemType = 0xFC
	SystemTypeHinksPix            SystemType = 0xFD
	SystemTypeAlphaPix            SystemType = 0xFE
	SystemTypeSanDevices          SystemType = 0xFF

	// SystemTypeShowMesh is the value ShowMesh reports in its own outgoing
	// Ping packets, PROVISIONAL (ADR-044 decision 6, RES-003 section 10.2).
	// xLights only offers a device as an FPP Connect upload target when its
	// typeId is below 0x80, which xLights classifies as FPP_TYPE::FPP; every
	// value at or above 0x80, including SystemTypeOther (0xC0), falls
	// through xLights' own type map and is rejected outright, so ShowMesh
	// cannot use FPP's real "other systems" bucket for this purpose. There is
	// no value in 0x01-0x7F that is both eligible and honestly unclaimed,
	// since FPP reserves that whole range for its own platform enumeration
	// (src/MultiSync.h), so 0x7F is chosen as the value furthest from every
	// growing FPP platform family base, minimizing collision risk. 0x01 is
	// never used: it is FPP's live fallback for unrecognized hardware, so a
	// real FPP on a new board reports it too. Getting a value allocated
	// properly needs pull requests to both FPP and xLights; that is a
	// tracked follow-up, not a blocker here.
	SystemTypeShowMesh SystemType = 0x7F
)

// String renders known system types by name and unknown ones as
// SystemType(0xNN).
func (t SystemType) String() string {
	switch t {
	case SystemTypeUnknown:
		return "Unknown"
	case SystemTypeFPP:
		return "FPP"
	case SystemTypeOther:
		return "Other"
	case SystemTypeShowMesh:
		return "ShowMesh"
	case SystemTypeXSchedule:
		return "xSchedule"
	case SystemTypeESPixelStickESP8266:
		return "ESPixelStick-ESP8266"
	case SystemTypeESPixelStickESP32:
		return "ESPixelStick-ESP32"
	case SystemTypeNonMultiSyncCapable:
		return "NonMultiSyncCapable"
	case SystemTypeWLED:
		return "WLED"
	case SystemTypeDIYLEDExpress:
		return "DIYLEDExpress"
	case SystemTypeHinksPix:
		return "HinksPix"
	case SystemTypeAlphaPix:
		return "AlphaPix"
	case SystemTypeSanDevices:
		return "SanDevices"
	default:
		return fmt.Sprintf("SystemType(0x%02x)", uint8(t))
	}
}

// PingPacket is the decoded body of a type 0x04 Ping or Discover packet.
// See the byte offset table in doc.go for the wire layout, including the
// fixed field widths DecodePing and EncodePing use for Hostname,
// VersionString, HardwareType, and Ranges.
type PingPacket struct {
	Version       uint8
	SubType       PingSubType
	SystemType    SystemType
	VersionMajor  uint16
	VersionMinor  uint16
	Mode          PingMode
	IP            [4]byte
	Hostname      string
	VersionString string
	HardwareType  string
	Ranges        string
}

// BlankPacket is the decoded body of a type 0x03 Blank packet. It carries
// no fields: the packet's only content is the header saying "blank now".
type BlankPacket struct{}

// PluginPacket is a recognized but unparsed type 0x05 Plugin packet. See
// the package doc comment for why this package does not decode the
// null-terminated plugin name and plugin-defined data inside Raw.
type PluginPacket struct {
	Raw []byte
}

// CommandPacket is the decoded body of a type 0x06 FPP Command packet: a
// command name and its arguments, exposed without interpreting them. Host
// is the target host the command was addressed to, usually empty (meaning
// the receiving instance itself).
type CommandPacket struct {
	Host    string
	Command string
	Args    []string
}

// Header is the common 7-byte header present on every FPPD control packet.
type Header struct {
	Type         PacketType
	ExtraDataLen uint16
}

// Sentinel errors for header-level problems: b does not look like an FPPD
// control packet at all. A listener receiving arbitrary UDP traffic on the
// MultiSync control port should treat these quietly rather than log them as
// a protocol violation from a peer.
var (
	// ErrNotFPPD is wrapped by every error DecodeHeader and Decode return
	// because the input never had a valid FPPD header to begin with.
	ErrNotFPPD = errors.New("multisync: not an FPPD control packet")

	// ErrTooShort means b was shorter than the 7-byte header itself.
	ErrTooShort = errors.New("multisync: packet too short for the FPPD header")

	// ErrBadMagic means b had 7 or more bytes but did not start with "FPPD".
	ErrBadMagic = errors.New("multisync: missing FPPD magic")
)

// Sentinel errors for body-level problems: the header was a valid FPPD
// header, so this is a real MultiSync peer, but the packet body itself is
// malformed. A listener should log these, since they indicate something
// unexpected from a peer that is speaking the protocol.
var (
	// ErrMalformed is wrapped by every decode error below this point, so
	// callers can distinguish "not ours" (ErrNotFPPD) from "ours, but
	// broken" (ErrMalformed) with a single errors.Is check.
	ErrMalformed = errors.New("multisync: malformed MultiSync packet body")

	// ErrLengthMismatch means the header's declared ExtraDataLen does not
	// match the number of bytes actually following the header.
	ErrLengthMismatch = errors.New("multisync: declared extra data length does not match packet size")

	// ErrTruncated means the body is shorter than the fixed-size fields its
	// packet type requires, even though ExtraDataLen matched the packet's
	// actual size.
	ErrTruncated = errors.New("multisync: packet body truncated")

	// ErrNoNullTerminator means a field that must be null-terminated (an
	// FPP Command packet's host, command, or argument strings) ran off the
	// end of the packet before finding one.
	ErrNoNullTerminator = errors.New("multisync: string field is missing its null terminator")

	// ErrInvalidUTF8 means a Sync packet's filename is not valid UTF-8.
	ErrInvalidUTF8 = errors.New("multisync: filename is not valid UTF-8")

	// ErrFieldTooLong means a length-bounded field (a Sync filename on
	// decode, or any fixed-width Ping string field on encode) exceeded its
	// maximum length.
	ErrFieldTooLong = errors.New("multisync: field exceeds its maximum length")

	// ErrSecondsElapsedOutOfRange means a Sync packet's SecondsElapsed field
	// was not a finite value within the sane range this package accepts.
	// See maxSecondsElapsed and isSaneSecondsElapsed.
	ErrSecondsElapsedOutOfRange = errors.New("multisync: secondsElapsed is not a finite value in the accepted range")
)

// UnknownPacketTypeError is returned by Decode when the header's packet
// type byte is not one this package decodes: either a currently valid FPP
// packet type this package has not implemented a payload for, or one of the
// two packet types FPP itself marks deprecated (0x00 legacy Command, 0x02
// Event, both superseded by FPP Command packets). It deliberately does not
// wrap ErrNotFPPD or ErrMalformed: the header parsed cleanly and the packet
// is a well-formed FPPD control packet, just of a type this decoder has no
// payload type for. A caller such as a listener should log Type and move on
// rather than treat this as noise (ErrNotFPPD) or corruption (ErrMalformed).
type UnknownPacketTypeError struct {
	Type PacketType
}

func (e *UnknownPacketTypeError) Error() string {
	return fmt.Sprintf("multisync: unknown packet type %s", e.Type)
}

// DecodeHeader parses the common 7-byte header from b and validates that
// the header's declared ExtraDataLen matches the number of bytes actually
// remaining in b. It returns the parsed Header and the remaining bytes (the
// packet-type-specific body) on success.
//
// b is expected to be exactly one UDP datagram: MultiSync packets are
// always sent as a single, complete datagram, so "remaining bytes" and
// "declared length" must match exactly. A caller that has concatenated or
// otherwise reframed data before calling DecodeHeader will see spurious
// ErrLengthMismatch errors; that is intentional, since accepting a mismatch
// here would mean silently truncating or padding, which this package never
// does.
func DecodeHeader(b []byte) (Header, []byte, error) {
	if len(b) < headerLen {
		return Header{}, nil, fmt.Errorf("%w: %w: need at least %d bytes for the FPPD header, got %d",
			ErrNotFPPD, ErrTooShort, headerLen, len(b))
	}
	if !bytes.Equal(b[0:4], []byte(magic)) {
		return Header{}, nil, fmt.Errorf("%w: %w: got %q", ErrNotFPPD, ErrBadMagic, b[0:4])
	}

	h := Header{
		Type:         PacketType(b[4]),
		ExtraDataLen: binary.LittleEndian.Uint16(b[5:7]),
	}
	extra := b[headerLen:]
	if int(h.ExtraDataLen) != len(extra) {
		return h, nil, fmt.Errorf("%w: %w: header declares %d bytes of extra data, packet has %d",
			ErrMalformed, ErrLengthMismatch, h.ExtraDataLen, len(extra))
	}
	return h, extra, nil
}

// Decode parses a complete FPPD control packet: the header plus its
// type-specific body. On success it returns the header and a typed payload:
// SyncPacket, BlankPacket, PingPacket, PluginPacket, or CommandPacket for
// packet types 0x01, 0x03, 0x04, 0x05, and 0x06 respectively.
//
// If b does not have a valid FPPD header at all, the returned error wraps
// ErrNotFPPD. If the header is valid but the body is malformed, the error
// wraps ErrMalformed. If the header is valid and of a recognized shape but
// the packet type is not one this package decodes, the error is an
// *UnknownPacketTypeError carrying the type byte.
func Decode(b []byte) (Header, any, error) {
	h, extra, err := DecodeHeader(b)
	if err != nil {
		return h, nil, err
	}

	switch h.Type {
	case PacketTypeMultiSync:
		p, err := DecodeSync(extra)
		return h, p, err
	case PacketTypeBlank:
		p, err := DecodeBlank(extra)
		return h, p, err
	case PacketTypePing:
		p, err := DecodePing(extra)
		return h, p, err
	case PacketTypePlugin:
		p, err := DecodePlugin(extra)
		return h, p, err
	case PacketTypeCommand:
		p, err := DecodeCommand(extra)
		return h, p, err
	default:
		return h, nil, &UnknownPacketTypeError{Type: h.Type}
	}
}

// encodeHeader builds a complete packet: the 7-byte FPPD header (with
// ExtraDataLen set to len(body)) followed by body.
func encodeHeader(t PacketType, body []byte) []byte {
	buf := make([]byte, headerLen+len(body))
	copy(buf[0:4], magic)
	buf[4] = byte(t)
	binary.LittleEndian.PutUint16(buf[5:7], uint16(len(body)))
	copy(buf[7:], body)
	return buf
}

// syncFixedLen is the size in bytes of a Sync packet's fixed-width fields
// (Action, FileType, FrameNumber, SecondsElapsed), before the
// variable-length filename.
const syncFixedLen = 10

// maxSecondsElapsed bounds the sane range this package accepts for a Sync
// packet's SecondsElapsed field. SecondsElapsed is a position within
// whatever single sequence or media file is currently playing, not a
// duration since some epoch; a holiday show file, however long, is not
// going to run for anywhere near a day. 24 hours is picked deliberately as
// a bound comfortably above any real show file while still catching the
// class of value that poisons the arithmetic downstream: converting an
// unbounded float32 (NaN, +/-Inf, or something like 1e30) to a
// time.Duration via float64(v) * float64(time.Second) is an out-of-range
// float-to-int conversion, which the Go spec leaves implementation-defined.
// This was measured, on the exact same malformed packet, to produce
// PositionMS = -9223372036854 on linux/amd64 and +9223372036854 on
// darwin/arm64, each then wrapping through int64 on the next free-run tick,
// with nothing in Timeline's state signaling the corruption. See BLOCKER 3
// in the review this responds to.
const maxSecondsElapsed = 24 * 60 * 60 // seconds; see the reasoning above

// isSaneSecondsElapsed reports whether se is a finite value DecodeSync and
// Timeline should trust as a position: not NaN, not +/-Inf, and within
// [0, maxSecondsElapsed]. FPP's own remote only clamps a negative
// secondsElapsed up to 0 (channeloutputthread.cpp); this package is
// stricter on both decode (validateSecondsElapsed) and, defensively, in
// Timeline.positionFromPacketLocked, because it is parsing untrusted UDP
// off a show network rather than trusted output from FPP's own encoder.
func isSaneSecondsElapsed(se float32) bool {
	f := float64(se)
	return !math.IsNaN(f) && !math.IsInf(f, 0) && se >= 0 && se <= maxSecondsElapsed
}

// validateSecondsElapsed returns a wrapped ErrSecondsElapsedOutOfRange (and,
// through it, ErrMalformed) if se is not a value DecodeSync should trust.
func validateSecondsElapsed(se float32) error {
	if !isSaneSecondsElapsed(se) {
		return fmt.Errorf("%w: %w: secondsElapsed %v is not a finite value in [0, %d] seconds",
			ErrMalformed, ErrSecondsElapsedOutOfRange, se, maxSecondsElapsed)
	}
	return nil
}

// DecodeSync decodes the body of a type 0x01 MultiSync (sync) packet. extra
// is the packet body after the 7-byte header, as returned by DecodeHeader.
//
// The filename is expected to be null-terminated, matching what a real FPP
// master always sends (see doc.go). If no null byte is found, DecodeSync
// treats the entire remainder of extra as the filename rather than
// returning an error, since nothing in the protocol strictly requires a
// non-FPP producer to include the terminator.
func DecodeSync(extra []byte) (SyncPacket, error) {
	if len(extra) < syncFixedLen+1 {
		return SyncPacket{}, fmt.Errorf("%w: %w: sync body needs at least %d bytes for its fixed fields and a filename terminator, got %d",
			ErrMalformed, ErrTruncated, syncFixedLen+1, len(extra))
	}

	secondsElapsed := math.Float32frombits(binary.LittleEndian.Uint32(extra[6:10]))
	if err := validateSecondsElapsed(secondsElapsed); err != nil {
		return SyncPacket{}, err
	}

	p := SyncPacket{
		Action:         SyncAction(extra[0]),
		FileType:       SyncFileType(extra[1]),
		FrameNumber:    binary.LittleEndian.Uint32(extra[2:6]),
		SecondsElapsed: secondsElapsed,
	}

	nameBytes := extra[syncFixedLen:]
	if i := bytes.IndexByte(nameBytes, 0); i >= 0 {
		nameBytes = nameBytes[:i]
	}
	if len(nameBytes) > MaxFilenameLength {
		return SyncPacket{}, fmt.Errorf("%w: %w: filename is %d bytes, exceeds the %d byte limit",
			ErrMalformed, ErrFieldTooLong, len(nameBytes), MaxFilenameLength)
	}
	if !utf8.Valid(nameBytes) {
		return SyncPacket{}, fmt.Errorf("%w: %w", ErrMalformed, ErrInvalidUTF8)
	}
	p.Filename = string(nameBytes)

	return p, nil
}

// EncodeSync encodes a complete packet (header plus body) for a type 0x01
// MultiSync (sync) packet, null-terminating the filename the way FPP itself
// does.
func EncodeSync(p SyncPacket) ([]byte, error) {
	if len(p.Filename) > MaxFilenameLength {
		return nil, fmt.Errorf("%w: filename is %d bytes, exceeds the %d byte limit",
			ErrFieldTooLong, len(p.Filename), MaxFilenameLength)
	}
	if !utf8.ValidString(p.Filename) {
		return nil, fmt.Errorf("%w: filename is not valid UTF-8", ErrInvalidUTF8)
	}

	body := make([]byte, syncFixedLen+len(p.Filename)+1) // +1 null terminator, left zero by make
	body[0] = byte(p.Action)
	body[1] = byte(p.FileType)
	binary.LittleEndian.PutUint32(body[2:6], p.FrameNumber)
	binary.LittleEndian.PutUint32(body[6:10], math.Float32bits(p.SecondsElapsed))
	copy(body[syncFixedLen:], p.Filename)

	return encodeHeader(PacketTypeMultiSync, body), nil
}

// DecodeBlank decodes the body of a type 0x03 Blank packet. A real FPP
// sender always sets ExtraDataLen to 0 for this type (see doc.go), but
// DecodeBlank does not enforce that: ControlProtocol.txt phrases it as "may
// be 0x00", not "is always 0x00", so any extra bytes present are simply
// ignored rather than rejected.
func DecodeBlank(_ []byte) (BlankPacket, error) {
	return BlankPacket{}, nil
}

// DecodePlugin recognizes a type 0x05 Plugin packet without parsing it. See
// the package doc comment for why the body (a null-terminated plugin name
// followed by plugin-defined data) is retained as raw bytes instead.
func DecodePlugin(extra []byte) (PluginPacket, error) {
	raw := make([]byte, len(extra))
	copy(raw, extra)
	return PluginPacket{Raw: raw}, nil
}

// pingMinBodyLen is the minimum number of bytes DecodePing requires: the
// fixed fields from Version through IP (offsets 0 through 11). Everything
// after that (Hostname, VersionString, HardwareType, Ranges) is read with
// bounds clamped to whatever bytes are actually present; see the
// doc/source contradiction note in doc.go for why this package does not
// hardcode either of the two conflicting "v1 packet length" numbers FPP's
// own doc and source disagree on.
const pingMinBodyLen = 12

// pingV1MinBodyLenFPPEnforces is the minimum ping body length current FPP
// source (MultiSync.cpp's ProcessPingPacket, accessed 2026-08-10) actually
// enforces before it will process an incoming ping packet at all, despite
// ControlProtocol.txt separately documenting 98 bytes for a "version 0x01"
// ping (see the doc/source contradiction note in doc.go). This package's
// own EncodePing always emits the full 294-byte v3 body (pingV3BodyLen),
// well above this floor, so nothing here changes today's behavior; the
// constant exists so that a future change adding a shorter, v1-style ping
// for backward compatibility does not silently produce a packet a real,
// currently-shipping FPP instance would reject outright.
const pingV1MinBodyLenFPPEnforces = 169

// Field widths for the four variable-content-but-fixed-width string fields
// in a version 3 Ping packet body, each including its null terminator. See
// the byte offset table in doc.go.
const (
	pingHostnameFieldLen = 65
	pingVersionFieldLen  = 41
	pingHardwareFieldLen = 41
	pingRangesFieldLen   = 121
)

// MaxPingRangesLength is the most content bytes (not counting the
// terminator) a Ping packet's Ranges field can hold. EncodePing already
// rejects a longer string with ErrFieldTooLong; this constant lets a caller
// assembling a ranges string check the limit itself before encoding, rather
// than discovering it only when encoding fails.
const MaxPingRangesLength = pingRangesFieldLen - 1

// pingV3BodyLen is the total body length CreatePingPacket in FPP's own
// source allocates for a version 3 ping (280 bytes of defined fields plus
// 14 bytes of trailing zero padding it never fills). EncodePing reproduces
// this exactly so an encoded packet matches a real FPP ping byte for byte.
const pingV3BodyLen = 294

// DecodePing decodes the body of a type 0x04 Ping or Discover packet. extra
// is the packet body after the 7-byte header, as returned by DecodeHeader.
//
// String fields (Hostname, VersionString, HardwareType, Ranges) are read
// with their read window clamped to whatever bytes of extra are actually
// present, the same way FPP's own ProcessPingPacket does with its
// copyField helper. A packet shorter than the full 294-byte version 3 body
// therefore decodes successfully with the missing trailing fields left as
// empty strings, rather than being rejected outright.
func DecodePing(extra []byte) (PingPacket, error) {
	if len(extra) < pingMinBodyLen {
		return PingPacket{}, fmt.Errorf("%w: %w: ping body needs at least %d bytes to reach the end of the IP field, got %d",
			ErrMalformed, ErrTruncated, pingMinBodyLen, len(extra))
	}

	p := PingPacket{
		Version:      extra[0],
		SubType:      PingSubType(extra[1]),
		SystemType:   SystemType(extra[2]),
		VersionMajor: binary.BigEndian.Uint16(extra[3:5]),
		VersionMinor: binary.BigEndian.Uint16(extra[5:7]),
		Mode:         PingMode(extra[7]),
	}
	copy(p.IP[:], extra[8:12])

	p.Hostname = readPingField(extra, 12, pingHostnameFieldLen)
	p.VersionString = readPingField(extra, 12+pingHostnameFieldLen, pingVersionFieldLen)
	p.HardwareType = readPingField(extra, 12+pingHostnameFieldLen+pingVersionFieldLen, pingHardwareFieldLen)
	p.Ranges = readPingField(extra, 12+pingHostnameFieldLen+pingVersionFieldLen+pingHardwareFieldLen, pingRangesFieldLen)

	return p, nil
}

// readPingField reads a null-terminated string field from extra starting at
// offset, never reading past min(offset+width, len(extra)). It returns ""
// if offset is at or past the end of extra, rather than erroring: Ping
// string fields are advisory metadata, not required for a caller to act on
// the packet, and FPP's own ProcessPingPacket treats a short packet the
// same way.
func readPingField(extra []byte, offset, width int) string {
	if offset >= len(extra) {
		return ""
	}
	end := offset + width
	if end > len(extra) {
		end = len(extra)
	}
	field := extra[offset:end]
	if i := bytes.IndexByte(field, 0); i >= 0 {
		field = field[:i]
	}
	return string(field)
}

// EncodePing encodes a complete packet (header plus body) for a type 0x04
// Ping or Discover packet, using the version 3 wire layout: the format
// every currently supported FPP release sends (CreatePingPacket in
// src/MultiSync.cpp, accessed 2026-08-10, hardcodes ping version 3).
// p.Version is ignored on input and always written as 3; EncodePing does
// not support producing the older, shorter v1/v2 layouts, since nothing
// ShowMesh needs to interoperate with still requires them, and DecodePing
// already tolerates receiving them.
func EncodePing(p PingPacket) ([]byte, error) {
	body := make([]byte, pingV3BodyLen)
	body[0] = 3
	body[1] = byte(p.SubType)
	body[2] = byte(p.SystemType)
	binary.BigEndian.PutUint16(body[3:5], p.VersionMajor)
	binary.BigEndian.PutUint16(body[5:7], p.VersionMinor)
	body[7] = byte(p.Mode)
	copy(body[8:12], p.IP[:])

	if err := putPingField(body, 12, pingHostnameFieldLen, p.Hostname); err != nil {
		return nil, fmt.Errorf("hostname: %w", err)
	}
	if err := putPingField(body, 12+pingHostnameFieldLen, pingVersionFieldLen, p.VersionString); err != nil {
		return nil, fmt.Errorf("version string: %w", err)
	}
	if err := putPingField(body, 12+pingHostnameFieldLen+pingVersionFieldLen, pingHardwareFieldLen, p.HardwareType); err != nil {
		return nil, fmt.Errorf("hardware type: %w", err)
	}
	if err := putPingField(body, 12+pingHostnameFieldLen+pingVersionFieldLen+pingHardwareFieldLen, pingRangesFieldLen, p.Ranges); err != nil {
		return nil, fmt.Errorf("ranges: %w", err)
	}
	// body[280:294] stays zero: the 14 bytes of trailing padding FPP itself
	// never fills either (see pingV3BodyLen).

	return encodeHeader(PacketTypePing, body), nil
}

// putPingField writes s, plus at least one trailing null byte, into
// dst[offset : offset+width]. width includes the terminator, so s may be at
// most width-1 bytes.
func putPingField(dst []byte, offset, width int, s string) error {
	maxContent := width - 1
	if len(s) > maxContent {
		return fmt.Errorf("%w: %d bytes, field holds at most %d", ErrFieldTooLong, len(s), maxContent)
	}
	copy(dst[offset:offset+width], s)
	return nil
}

// DecodeCommand decodes the body of a type 0x06 FPP Command packet: the
// argument count, then the null-terminated target host, command name, and
// each argument in turn. It exposes the command and its arguments without
// interpreting them; FPP Command names and argument semantics are FPP's
// concern, not this package's.
func DecodeCommand(extra []byte) (CommandPacket, error) {
	if len(extra) < 1 {
		return CommandPacket{}, fmt.Errorf("%w: %w: command body needs at least 1 byte for the argument count, got 0",
			ErrMalformed, ErrTruncated)
	}
	numArgs := int(extra[0])
	pos := 1

	host, pos, err := readCString(extra, pos)
	if err != nil {
		return CommandPacket{}, fmt.Errorf("host: %w", err)
	}
	cmd, pos, err := readCString(extra, pos)
	if err != nil {
		return CommandPacket{}, fmt.Errorf("command: %w", err)
	}

	args := make([]string, 0, numArgs)
	for i := 0; i < numArgs; i++ {
		var arg string
		arg, pos, err = readCString(extra, pos)
		if err != nil {
			return CommandPacket{}, fmt.Errorf("arg %d: %w", i, err)
		}
		args = append(args, arg)
	}

	return CommandPacket{Host: host, Command: cmd, Args: args}, nil
}

// readCString reads a null-terminated string from b starting at offset. It
// returns the string (without the terminator) and the offset of the byte
// immediately after the terminator. If offset is past the end of b, or no
// null byte is found before the end of b, it returns a wrapped
// ErrNoNullTerminator rather than panicking or reading out of bounds.
func readCString(b []byte, offset int) (string, int, error) {
	if offset > len(b) {
		return "", offset, fmt.Errorf("%w: %w: field starts past the end of the packet",
			ErrMalformed, ErrTruncated)
	}
	i := bytes.IndexByte(b[offset:], 0)
	if i < 0 {
		return "", offset, fmt.Errorf("%w: %w", ErrMalformed, ErrNoNullTerminator)
	}
	return string(b[offset : offset+i]), offset + i + 1, nil
}

// Package multisync implements the FPP MultiSync wire protocol: encoding and
// decoding of the UDP control packets FPP masters and remotes exchange for
// playback synchronization, ping/discover, and remote commands.
//
// This package is wire codec only: header parsing, packet type dispatch, and
// per-type encode/decode. It has no socket and no listener loop, and no
// timeline or state machine; those are separate concerns built on top of
// this package. Per ADR-001, FPP stays the authoritative scheduler; this
// codec only lets ShowMesh speak and observe FPP's own protocol, never
// replace it.
//
// # Verification status
//
// Everything in this package is L1: source-verified against the FPP
// project's own documentation and current source, accessed 2026-08-10 (see
// docs/research/RES-002-fpp-multisync-compatibility.md). It has NOT been
// confirmed against a live FPP player. That bench verification, a packet
// capture against a real FPP 9.x or 10.x master, is explicitly still open:
// it is RES-002's open item 1 and an acceptance criterion of
// docs/build/BUILD-PLAN.md Step 1. Passing unit tests in this package,
// however thorough, does not raise this above L1; only a capture against a
// real FPP instance does that.
//
// # Sources (all accessed 2026-08-10)
//
//   - https://raw.githubusercontent.com/FalconChristmas/fpp/master/docs/ControlProtocol.txt
//     The protocol's own prose description of every packet type and byte
//     offset.
//   - https://raw.githubusercontent.com/FalconChristmas/fpp/master/src/MultiSync.h
//     The packed C structs (ControlPkt, SyncPkt) and the
//     MultiSyncSystemType device-type enum.
//   - https://raw.githubusercontent.com/FalconChristmas/fpp/master/src/MultiSync.cpp
//     The actual encode (CreatePingPacket, SendSeqSyncPacket,
//     SendFPPCommandPacket, and friends) and decode (ProcessPingPacket,
//     ProcessFPPCommandPacket) code. This is what settles endianness: the
//     sync and header fields are written by direct struct field assignment
//     with no htons/htonl call anywhere in the send path, so they carry the
//     host's native byte order, and FPP has only ever shipped on
//     little-endian targets (Raspberry Pi, BeagleBone, x86, Falcon
//     hardware). The ping packet's version fields are the exception:
//     CreatePingPacket builds them by hand with explicit >>8 / &0xFF
//     shifts, independent of host byte order, so those two fields are
//     genuinely wire-endian rather than incidentally little-endian.
//   - https://raw.githubusercontent.com/forkineye/ESPixelStick/main/src/service/FPPDiscovery.cpp
//     A third-party listener, cross-checked for the header and sync
//     fields. Consistent with the above, but since ESP32 is also
//     little-endian this corroborates rather than independently proves
//     wire endianness on its own.
//
// # Common header (7 bytes, every packet type)
//
//	Offset  Size  Field         Notes
//	0-3     4     Magic         ASCII "FPPD"
//	4       1     PacketType    see PacketType consts
//	5-6     2     ExtraDataLen  uint16, little-endian
//
// ControlPkt in src/MultiSync.h is declared __attribute__((packed)), so
// there is no alignment padding anywhere in the header. ExtraDataLen's
// endianness is not spelled out in ControlProtocol.txt or explicitly called
// out in RES-002; it is inferred here the same way as the sync fields
// below, from ControlPkt's packed layout and unswapped assignment. Recorded
// as a refinement of RES-002, not a contradiction.
//
// # Sync packet (type 0x01), body follows the 7-byte header
//
//	Offset  Size  Field           Notes
//	0       1     Action          SyncAction: 0 start, 1 stop, 2 sync, 3 open
//	1       1     FileType        SyncFileType: 0 sequence (fseq), 1 media
//	2-5     4     FrameNumber     uint32, little-endian
//	6-9     4     SecondsElapsed  float32, little-endian
//	10+     var   Filename        null-terminated
//
// SyncPkt is also __attribute__((packed)): no padding before FrameNumber,
// matching ControlProtocol.txt's buf[9-12] sitting immediately after the two
// 1-byte fields at buf[7-8]. This confirms RES-002's "sync fields
// little-endian" finding: SendSeqSyncPacket and friends in MultiSync.cpp
// assign spkt->frameNumber and spkt->secondsElapsed directly with no
// byte-swap, so they carry the host's native order, and FPP only runs on
// little-endian targets. FPP always null-terminates the filename:
// SendSeqOpenPacket and friends use strcpy into SyncPkt.filename and set
// extraDataLen = sizeof(SyncPkt) + filename.length(), where sizeof(SyncPkt)
// already reserves the one byte SyncPkt.filename[1] occupies for that
// terminator, matching ControlProtocol.txt's own "10 + filename length + 1
// (for null)" formula for buf[5-6]. DecodeSync still tolerates a filename
// that fills every remaining byte with no null at all, treating the
// remainder as the name in that case; nothing requires a non-FPP producer to
// follow FPP's own convention, so this is this package's own defensive
// choice, not something the protocol document requires.
//
// # Blank packet (type 0x03)
//
// No body. SendBlankingDataPacket in MultiSync.cpp sets
// cpkt->extraDataLen = 0 and sends nothing further.
//
// # Ping / Discover packet (type 0x04), body follows the 7-byte header
//
//	Offset   Size  Field          Notes
//	0        1     Version        ping wire-format version; FPP currently
//	                              only ever sends 3 (CreatePingPacket
//	                              hardcodes this byte to 3)
//	1        1     SubType        PingSubType: 0 ping, 1 discover
//	2        1     SystemType     see SystemType consts
//	3-4      2     VersionMajor   uint16, big-endian: built with explicit
//	                              shifts in CreatePingPacket and
//	                              ProcessPingPacket, not struct assignment,
//	                              so this field is genuinely wire-endian.
//	                              Confirms RES-002.
//	5-6      2     VersionMinor   uint16, big-endian, same reasoning
//	7        1     Mode           PingMode bitmask: 0x01 bridge, 0x02
//	                              player, 0x04 sending multisync, 0x08
//	                              remote
//	8-11     4     IP             four raw octets in dotted order, not a
//	                              network-order uint32. 0.0.0.0 is the
//	                              documented etiquette for a non-FPP
//	                              device's discover ping (ControlProtocol.txt,
//	                              Ping Packet section), confirmed against
//	                              RES-002 and against ProcessPingPacket's
//	                              explicit "all four IP bytes zero" special
//	                              case.
//	12-76    65    Hostname       null-terminated: 64 bytes of content plus
//	                              1 terminator byte
//	77-117   41    VersionString  null-terminated: 40 + 1
//	118-158  41    HardwareType   null-terminated: 40 + 1 (ControlProtocol.txt's
//	                              "Ping type 2 and 3" field)
//	159-279  121   Ranges         null-terminated comma-separated channel
//	                              ranges, 120 + 1 (ControlProtocol.txt's
//	                              "Ping type 3" field; type 2 used a shorter
//	                              40 + 1 field at the same offset). DecodePing
//	                              clamps every one of these string reads to
//	                              whatever bytes are actually present rather
//	                              than requiring the full 294-byte v3 body,
//	                              mirroring ProcessPingPacket's own
//	                              copyField helper in MultiSync.cpp.
//	280-293  14    (reserved)     zero padding: CreatePingPacket allocates
//	                              294 bytes of extra data but only ever
//	                              fills the first 280
//
// # A doc/source contradiction found while extracting this table
//
// ControlProtocol.txt documents the minimum ping length as "98 for version
// 0x01 ping packet". Current MultiSync.cpp's ProcessPingPacket instead
// rejects anything under 169 bytes (pingV1MinBodyLenFPPEnforces in
// packet.go), with the comment "// v1 packet length" attached to that 169
// check, not to 98. The two numbers do not describe the same layout: a
// 169-byte body reaches through Hostname and VersionString in full, all the
// way through HardwareType (which ends at body offset 158), and 10 bytes
// into the following Ranges field (offsets 159-279; byte 168 is the last
// one a 169-byte body reaches), well past what a genuinely 98-byte v1
// packet (fixed fields plus a much shorter hostname/version pair) could
// hold. This reads as ControlProtocol.txt describing a historical v1 format
// the current source no longer actually accepts, i.e. the prose doc and the
// running code have drifted apart, not a contradiction inside RES-002
// itself (RES-002 did not make a claim about the v1 minimum length; it only
// asserted the little-endian/big-endian split, which both sources agree on).
// Reported here because CLAUDE.md and RES-002 treat this class of
// doc/source mismatch as a finding to surface, not something to quietly
// paper over. Practical effect on this package: DecodePing does not
// hardcode either 98 or 169 as a required minimum. It only requires the 12
// bytes needed to reach the end of the IP field (Version through IP) and
// clamps every field beyond that to whatever bytes are present, so it
// accepts a legitimately short packet and the current 294-byte v3 packet
// without depending on either disputed number.
//
// # FPP Command packet (type 0x06), body follows the 7-byte header
//
//	Offset  Size  Field       Notes
//	0       1     NumArgs     uint8, count of trailing arg strings
//	1+      var   Host        null-terminated, usually empty (means "self")
//	x+      var   Command     null-terminated
//	x+      var   Args[0..N)  NumArgs null-terminated strings, back to back
//
// # Plugin packet (type 0x05)
//
// DecodePlugin recognizes the type but does not parse the body (a
// null-terminated plugin name followed by plugin-defined data, per
// ControlProtocol.txt). ShowMesh has no plugin of its own to interpret that
// data for, so the raw bytes are retained unparsed on PluginPacket.Raw
// rather than decoded.
package multisync

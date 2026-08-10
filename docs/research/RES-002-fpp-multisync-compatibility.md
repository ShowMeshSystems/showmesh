# RES-002: FPP MultiSync Compatibility

[Architecture](../architecture/ARCHITECTURE.md#5-synchronization-model) · [Tracker](README.md) · [FPP Connect research](RES-003-xlights-fpp-connect-compatibility.md)

Status: planned (bench) · Risk: critical · Verification: L1 — source verified 2026-08-10

## Decision to make

Define how a non-FPP media node participates in FPP timing and what compatibility contract is required for start, position, pause, seek, and recovery.

## Questions

- Which documented or source-defined MultiSync messages and transports are available?
- Can a third-party listener interoperate without modifying FPP?
- Which fields identify sequence, media, position, rate, and source priority?
- How often is timing refreshed, and what are the loss and failover rules?
- How do FPP versions, unicast/multicast modes, routed VLANs, and clock differences affect behavior?
- Does an FPP plugin provide a safer compatibility boundary than direct protocol implementation?

## Acceptance criteria

- A prototype listener follows start, stop, pause, seek, restart, and late join.
- Compatibility is measured across the supported FPP versions and network modes.
- Loss of updates becomes `unsynchronized` within a defined interval.
- The implementation avoids pretending to be a complete FPP instance unless that is explicitly required and supportable.

## Test method

Capture authoritative documentation and relevant source behavior, then record packets from representative playlists. Build an isolated listener and compare its timeline with an FPP remote and physical reference. Inject delay, loss, duplication, reordering, source restart, and competing masters.

## Evidence and findings

Desk research 2026-08-10 (documentation and source reading; no packet captures yet). Confidence tags: [doc] officially documented, [src] read in FPP or third-party source, [hist] git history.

### Protocol definition

- MultiSync is officially documented in-repo: [docs/ControlProtocol.txt](https://github.com/FalconChristmas/fpp/blob/master/docs/ControlProtocol.txt); implemented in [src/MultiSync.h](https://github.com/FalconChristmas/fpp/blob/master/src/MultiSync.h) / [MultiSync.cpp](https://github.com/FalconChristmas/fpp/blob/master/src/MultiSync.cpp). [doc][src]
- Transport: UDP port **32320** (`FPP_CTRL_PORT`); multicast group **239.70.80.80**; broadcast and unicast modes selectable by FPP settings, with **multicast as the default** when nothing is configured. A listener should join the multicast group and also accept broadcast/unicast on 32320. [src]
- All packets share a header: `'FPPD'` + packet type byte + uint16 extraDataLen. Types: 0x01 MultiSync, 0x03 Blank, 0x04 Ping/Discover, 0x05 Plugin, 0x06 FPP Command (0x00 Command and 0x02 Event are deprecated). [doc]
- Sync packet (0x01): action byte (0=START, 1=STOP, 2=SYNC, 3=OPEN), file type (0=FSEQ sequence, 1=media), uint32 frameNumber, float32 secondsElapsed, null-terminated filename. Sync fields are little-endian (confirmed against ESPixelStick's explicit `write32`); ping version fields are big-endian. [doc][src]
- **Rate is not carried in packets** — the remote derives it from the step time in its local copy of the .fseq file. The protocol assumes both sides hold the same file. [src]
- Cadence from an FPP master: sequence sync every 4 frames for the first 32 frames, then every 10 frames (≈2–4/sec); media sync every 0.5 s. xSchedule masters use their own similar intervals — do not hard-code FPP cadence. [src]

### Third-party interoperability — confirmed viable without modifying FPP

- FPP's own device-type enum reserves IDs for non-FPP followers: xSchedule (0xC1), ESPixelStick (0xC2/0xC3), WLED, HinksPix, Falcon and Genius controllers. [src]
- Working third-party listeners: [ESPixelStick FPPDiscovery.cpp](https://github.com/forkineye/ESPixelStick/blob/main/src/service/FPPDiscovery.cpp) (sequence sync only, ignores media sync); xSchedule master+remote (e.g., xLights 2024.20 tag, `xSchedule/SyncFPP.cpp`); Falcon controller firmware. [src]
- ControlProtocol.txt defines etiquette for non-FPP devices (discover pings with IP 0.0.0.0). Caveat: FPP's auto-unicast mode only targets FPP instances (`supportsUnicast`); a third-party node should rely on multicast/broadcast or manual listing in `MultiSyncRemotes`, and should answer discover pings to appear in the FPP MultiSync UI. [src]

### Semantics a listener must implement

- Position: prefer `secondsElapsed` when > 0 — FPP's own remote recomputes frame as `round(seconds*1000/stepTimeMs)`. Media START carries secondsElapsed for mid-show joins (possibly 0 from ≤8.x masters). [src]
- Lifecycle: OPEN → START → SYNC… → STOP, but a robust listener must handle START without OPEN and even a bare SYNC for an unstarted sequence (FPP's remote does both). [src]
- Stop: explicit STOP packets per sequence and media; FPP remotes deliberately wait ~5 frames before blanking so back-to-back stop/start doesn't blink ([Sequence.cpp](https://github.com/FalconChristmas/fpp/blob/master/src/Sequence.cpp) ~695). [src]
- Loss policy: FPP's design is to **free-run through sync silence** and only slew/jump when packets resume ([channeloutputthread.cpp](https://github.com/FalconChristmas/fpp/blob/master/src/channeloutput/channeloutputthread.cpp): slew ≤4 frames, skip when moderately behind, jump when >0.5 s behind). Silence is deliberately not a teardown trigger; the only watchdog is `RemoteSyncedMediaIdleTimeout` (default 10 s) after media ends. ESPixelStick re-broadcasts pings after 30 s of no master contact and supports a local fallback file; FPP remotes support `fallback.fseq`. [src]

### Version stability

- Sync packet format unchanged FPP 4→10; changes have been additive (ping v3 in 2019, FPP Command type in 2020) or semantic (FPP 5 removed "master mode" naming; masters are "player + sending multisync", mode flag 0x04). FPP 10's GStreamer/PipeWire media engine changes internal clocking only, not the wire protocol. [hist][src]
- Current versions (Aug 2026): stable **FPP 9.5** (2026-01-08); **10.0-beta3** (2026-08-08). [doc]

### Alternative boundaries

- A C++ FPP plugin can register via `multiSync->addMultiSyncPlugin(...)` and receive parsed sync callbacks (`ReceivedSeqSyncPacket(filename, frames, seconds)` etc.) — no wire parsing, but code must run on an FPP box. [src]
- FPP REST API: `GET /api/fppd/status` returns playlist, sequence, and elapsed-seconds fields; also `/api/fppd/multiSyncSystems`, `/api/fppd/multiSyncStats`, and MQTT status. Suitable for supervision (~1 Hz), not frame-accurate timing. [doc][src]
- Assessment: raw MultiSync parsing is the intended, de-facto-stable boundary for separate-hardware nodes; a hybrid (MultiSync for timing + REST/MQTT for metadata and health) fits the ShowMesh coordinator model.

### Open items for bench (L2) verification

1. Packet capture against a real FPP 9.x/10.x master to confirm endianness/padding and exact cadence/jitter under load.
2. Pause/seek packet behavior, and whether OPEN reliably precedes START across master versions and xSchedule.
3. Packets emitted at playlist end vs manual stop vs fppd shutdown (STOP vs BLANK combinations; orphaned no-STOP cases).
4. Clock-drift accumulation over a 30–60 min show between sync nudges; required slew aggressiveness.
5. Multicast IGMP behavior on the reference switch; discover-ping participation needed for the FPP UI.

## Decision, fallback, and revalidation

Direction (pending L2/L3 confirmation): implement a native MultiSync listener speaking the documented wire protocol, answering discover pings with a reserved/appropriate device type, following FPP's own remote semantics (free-run on silence, slew/jump on resume, STOP+~5-frame blank). Supplement with REST/MQTT for supervision. Fallback options remain a small FPP-side bridge, running FPP on the node, or pre-rendered media with a separate verified clock. Revalidate on supported FPP major releases (next: FPP 10.0 GA).

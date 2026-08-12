# RES-002: FPP MultiSync Compatibility

[Architecture](../architecture/ARCHITECTURE.md#5-synchronization-model) · [Tracker](README.md) · [FPP Connect research](RES-003-xlights-fpp-connect-compatibility.md)

Status: planned (bench) · Risk: critical · Verification: **L2 for protocol semantics** (containerized bench, 2026-08-10) · **L1 for everything hardware- or network-dependent**

The split is deliberate and is the owner's call. Protocol semantics have been reproduced against a real `fppd` with recorded versions, which is what L2 means. Clock drift and switch behavior have not been reproduced at all, because no container can produce them.

**This record does not support show readiness at any level yet.** Per CLAUDE.md, architecture-critical claims need L3 (integrated) before adoption and L4 (resilient) before show readiness, and this record has reached neither. L2 here means the wire protocol is proven enough to keep building against; it does not mean the sync path is trustworthy in a live show. That still requires the physical rig, and it is the gate before release rather than before the next build step.

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

### Two bench tiers, because they answer different questions

**Tier 1, containerized (`bench/fpp-multisync/`).** A real `fppd` in a container drives the probe. This is a real daemon emitting real packets, so it is genuine evidence for everything that is *software* behavior: packet layout, lifecycle ordering, cadence, and the effect of a version change. It is repeatable, it does not touch the show rig, and it can run several FPP versions side by side without reflashing anything. It also satisfies [ADR-013](../decisions/ADR-013-no-fpp-control-port-sharing.md) structurally rather than by discipline, because `fppd` and the probe occupy separate network namespaces and cannot share UDP 32320.

**Tier 2, physical.** The real player on the reference switch, per [the capture procedure](../bench/RES-002-capture-procedure.md). Required for anything that is a property of the hardware clock or the physical network.

The distinction matters because a container result for a hardware question is not weak evidence, it is misleading evidence. Docker networking changes multicast behavior, and an x86 container's clock says nothing about a Raspberry Pi's crystal.

Which tier can close which open item is recorded against each item below. Tier 1 cannot raise items 4 or 5 at all, and no amount of container work will change that.

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

Each item records which bench tier can close it. All five remain open; the tier annotation says where the work should happen, not that it has happened.

1. Packet capture against a real FPP 9.x/10.x master to confirm endianness/padding and exact cadence/jitter under load. **Tier 1**, except jitter under realistic load, which wants tier 2.
2. Pause/seek packet behavior, and whether OPEN reliably precedes START across master versions and xSchedule. **Tier 1** for FPP masters. The xSchedule half needs a real xSchedule and is neither tier as currently built.
3. Packets emitted at playlist end vs manual stop vs fppd shutdown (STOP vs BLANK combinations; orphaned no-STOP cases). **Tier 1.** Killing a containerized `fppd` uncleanly is easier and more repeatable than doing it to a live player.
4. Clock-drift accumulation over a 30–60 min show between sync nudges; required slew aggressiveness. **Tier 2 only.** Drift is a property of the player's clock hardware and OS scheduling; a container on an x86 host produces a number that does not transfer to a Raspberry Pi.
5. Multicast IGMP behavior on the reference switch; discover-ping participation needed for the FPP UI. **Tier 2 only.** Docker networking alters multicast behavior, and even a macvlan container on a hypervisor host is behind a virtual switch doing its own IGMP snooping, so it is still not the reference switch.

### First corroboration against a running daemon (2026-08-10)

Recorded during construction of the tier 1 bench, not as a structured capture run. It is reported here because it is the first evidence in this record that came from a running `fppd` rather than from reading documentation and source, and because one finding is operationally significant.

- A containerized FPP 9.5.3 master, with **no channel outputs configured at all**, emitted `OPEN`, then `START`, then periodic `SYNC` while playing a sequence, and the probe in a separate container received and decoded the sequence with no malformed packets. Observed cadence matched the cadence recorded in this document from source: every 4 frames for the first 32, then every 10.
- This settles a question the design had only reasoned about: **MultiSync output derives from sequence position, not from output hardware**, so a master with nothing physically attached still emits usable sync.
- **`MultiSyncEnabled` defaults to off.** With it off, `fppd` plays sequences entirely normally and never constructs a single MultiSync packet, logging no error at default verbosity. This is worth carrying into the product, not just the bench: an operator whose FPP has never had MultiSync enabled will see a working show and no sync traffic, and ShowMesh must be able to say so rather than reporting an absence of packets as a network fault. It belongs in readiness evidence (OBSERVABILITY §10) and in whatever the FPP collector reports in Step 3.

**Promotion to L2 for protocol semantics, 2026-08-10, owner's decision.** The reasoning is worth recording because it is a judgement about what a bench is *for*: a containerized `fppd` is testing ShowMesh against the real protocol implementation, which is the thing that can actually be wrong in the code. Establishing that the protocol talks before touching the physical network also avoids spending an evening troubleshooting a switch for a problem that was never in the switch.

What that promotion rests on, stated so a later reader can judge it rather than trust it: FPP 9.5.3-14-g422ed1ae8 in a container, no channel outputs configured, sequence started through FPP's own API, cadence observed matching the source-derived record, and a clean decode of the full OPEN/START/SYNC sequence by the probe in a separate container. The observation was made while building the harness rather than as a structured capture run, so **running the three tier-1 captures in the procedure would firm this up considerably and now costs minutes rather than an evening**.

Items 4 and 5 stay where they were. No container promoted them and none will.

### First contact with the real deployed fleet (2026-08-11, read-only REST, still L1)

Read-only `GET /api/system/info` and `GET /api/fppd/status` against the three deployed hosts recorded in the [reference installation](../reference-installation.md). No packets were captured and no probe was run, so **this changes nothing about the wire-protocol evidence level**; it is recorded because the bench tier 2 work will run against exactly these hosts and their state is now known rather than assumed.

- The player (`FPP-Main`, Pi 3 Model B+, FPP 9.4) reports **`multisync: true`** in `/api/fppd/status`. The detection path described below therefore reads a live `true` on the real rig, not only a container-default `false`, which is the case the collector's test could not previously exercise against real hardware.
- The fleet is **not uniform**: 9.4 on the player and one remote, and a master-branch build (`9.x-master-822-g56515e4d`) on the other remote, run deliberately because a bug hit during last season's display was fixed in master. A MultiSync capture is therefore a capture across mixed FPP builds, and the versions of every participant must be recorded with it rather than assumed equal.
- The player's own `warnings` array reported `MQTT Disconnected` and `A Log Level is set to Debug` at probe time. Both are FPP-side conditions unrelated to MultiSync, noted so a future capture is not read against an assumed-clean baseline.
- The operator intends to move the fleet to a 9.x release or trial the FPP 10 beta before the season. That is a material environment change: captures taken before it do not carry forward.

### `warnings` is omitted from the status document when empty (2026-08-11, L1, source-verified)

A second read-only pass over the fleet found the three hosts disagreeing in shape rather than in content: `FPP-remote-04` has **no `warnings` key at all** in `/api/fppd/status`, while its MQTT `warnings` topic publishes `[]`, and the other two hosts carry populated arrays. Absent, empty, and populated are three different claims, and guessing between them is exactly what this project's evidence discipline forbids.

Settled against FPP's own source rather than by inference. In `src/httpAPI.cpp`, the field is built only inside the loop:

```cpp
for (auto& warn : WarningHolder::GetWarnings()) {
    result["warnings"].append(warn.message());
    result["warningInfo"].append(warn);
}
```

`.append()` creates the key on first use, so an empty warning list never creates it. **FPP omits `warnings` and `warningInfo` from `/api/fppd/status` when there are no warnings.** Verified at commit `7e3c6acb02386e65855f420aa21cde518453be38`, which is the `RemoteGitVersion` `FPP-Main` itself reports, so it is the correct source for this fleet rather than for `master` generally. Read at lines 120-124 of that file; a builder independently cited the same construct in the same file. [src: `src/httpAPI.cpp` @ `7e3c6acb0`, accessed 2026-08-11]

Also confirmed live in the same pass, and worth recording next to the note above about not assuming a clean baseline: the player's `MQTT Disconnected` warning is **gone**, and all three hosts now report `MQTT: {configured: true, connected: true}` against the operator's existing broker. The earlier reading was a point-in-time observation, not a standing condition.

**What this does and does not license.** ShowMesh still models a REST-absent `warnings` as `unsupported` with a reason rather than as a measured zero, and lets the MQTT source answer the signal positively. The source verification makes that a deliberate conservatism rather than an unknown: turning a key's absence into a measured value is the failure mode this project keeps catching, and having a second source that states the fact positively costs nothing.

### How ShowMesh detects that MultiSync is off (2026-08-10, tier 1)

The note above says ShowMesh must be able to report "MultiSync is disabled"
rather than reporting an absence of packets as a network fault. Building the
Step 3 collector required knowing *how*, and the obvious answer is wrong in a
way that hides itself. Captured against the same containerized FPP 9.5.3.

- `GET /api/settings/MultiSyncEnabled` returns the setting's **schema**, not its
  value: `name`, `description`, `tip`, `type`, `restart`, and so on. On a daemon
  where the setting has never been explicitly written, which is every fresh
  install, there is no `value` key at all. A decoder expecting one gets a zero
  value and no error, so it reports "disabled" correctly, by accident, for as
  long as MultiSync stays off. Once the setting has been written once, the
  endpoint *does* gain a `value` key, carrying the JSON **string** `"0"` or
  `"1"` rather than a boolean. Two boxes therefore behave differently and are
  indistinguishable without knowing this. `GET /api/setting/...` (singular) is
  404, and `GET /api/settings` returns the group catalogue, also not values.
- The usable signal is the top-level `multisync` boolean in
  `/api/fppd/status` and `/api/system/status`. Verified transition: `false` on a
  fresh container; still `false` immediately after `PUT
  /api/settings/MultiSyncEnabled` succeeds; `true` after an `fppd` restart. The
  setting's own schema explains why, with `"restart": 2`.
- So the status field reports **what the running daemon is actually doing**, and
  the stored setting legitimately disagrees with it until `fppd` restarts.
  Observing the daemon's behaviour is the correct choice for a collector, and it
  means ShowMesh can distinguish "configured but not yet in effect" from "in
  effect" if that ever matters.

Also recorded because it costs an hour to rediscover: several
`/api/fppd/status` fields that look numeric arrive as JSON **strings**
(`seconds_played`, `seconds_remaining`, `repeat_mode`,
`current_playlist.count`, `current_playlist.index`), while `mode`, `status`,
`volume` and `uptimeSeconds` are genuine numbers. A struct declaring an integer
for one of the former fails to unmarshal the whole document, which surfaces as
the FPP appearing unreachable: a decoding bug wearing a network fault's
clothes.

This is REST behaviour rather than MultiSync wire behaviour, so it does not
change this record's L2/L1 split. It is recorded here because it is what makes
the `MultiSyncEnabled` finding above actionable rather than merely known.

## Decision, fallback, and revalidation

Direction (pending L2/L3 confirmation): implement a native MultiSync listener speaking the documented wire protocol, answering discover pings with a reserved/appropriate device type, following FPP's own remote semantics (free-run on silence, slew/jump on resume, STOP+~5-frame blank). Supplement with REST/MQTT for supervision. Fallback options remain a small FPP-side bridge, running FPP on the node, or pre-rendered media with a separate verified clock. Revalidate on supported FPP major releases (next: FPP 10.0 GA).

# Reference Installation

[Documentation index](README.md) · [Architecture specification](architecture/ARCHITECTURE.md) · [Research tracker](research/README.md)

Status: Template — fill in with the actual reference show hardware and topology.

The architecture spec and every research test matrix refer to "the reference installation." This document is its single source of truth. Research results (L2+) are only valid against a recorded topology; when this document changes materially, dependent conclusions become `stale`.

## Show overview

- Location / audience viewing model:
- Show season and nightly schedule:
- Approximate channel count and prop inventory:
- Projection surfaces (count, physical size, surface type):

## Controllers and players

| Role | Hardware | OS / firmware | Software + version | Notes |
|---|---|---|---|---|
| FPP player (authoritative scheduler) | Raspberry Pi **3** Model B+ (`FPP-Main`, 192.168.133.159) | OS v2025-09 | FPP **9.4** (branch v9.4) | Probed live 2026-08-11. `multisync: true`. Was recorded as a Pi 4 B+ and "latest"; both were wrong, see the version note below |
| FPP remotes (if any) | Kulp K16A-B on BeagleBone Black (`FPP-remote-01`, 192.168.133.70); Kulp K16-Max on PocketBeagle2 (`FPP-remote-04`, 192.168.133.61); Kulp K16-Pro (standby, not probed) | OS v2025-11 on both probed remotes | remote-01: **9.x-master-822-g56515e4d** (master branch, not a release); remote-04: 9.4 | K16-Max and K16A-B (eFuse variant, operator-confirmed) both have per-string current monitoring; K16-Pro is standby only, blade-fused, no current telemetry. **remote-01 runs master deliberately**: a bug hit during last year's display was fixed in master, and the operator chose not to move to a release mid-show while master was working |
| Sequencing workstation | MacBook Pro M1 Max | | xLights v - latest| |
| Resolume host | Hackintosh may sawp to windows | | Arena v 7.23.2 | Arena license required for SMPTE input |
| Media node candidates | Dell Micro 7040 i5 | Linux, probably| | |
| Audio node candidate | Dell Micro 7040 i5 | Linux, or windows depending on audio support | | |
| Smart receivers | Older Kulp differential receivers (pre-V5 protocol) | | | No current/fuse telemetry — known blind spots; planned replacement with V5-protocol receivers that report per-output current. Confirmed live 2026-08-11: these appear in `/api/fppd/ports` as entries carrying only `col`, `name`, `row`, and `smartReceivers: true`, with no `ma` key at all |

**Software version state, and the plan for this season.** The versions above are what was installed for last season and left untouched; they are a point-in-time observation from 2026-08-11, not a target. The intent for this season is to move the fleet to a **9.x release**, or to trial the **FPP 10 beta**, rather than stay on this mix. That decision is open. Until it is made, treat the fleet as deliberately non-uniform, and treat any ShowMesh behaviour verified against these versions as verified against *these* versions only. A fleet-wide version move is a material environment change and moves affected research conclusions to `stale` per the research workflow.

## Network

- Switch model(s) and firmware: Unifi Pro Max 16 mostly dedicated to holiday stuff, 10g uplink to full unifi core.
- VLAN layout (control vs media vs house): vlan 150 is curernt “AV LAN” Adding VLAN 160 for holiday stuff
- Multicast/IGMP snooping configuration: Configured with unifi defaults for AES67 audio across the network
- Uplinks and bandwidth budget (NDI aggregate estimate): Not a problem really, resolume machine has 2.5g ethernet, switch will be 10g to my core.
- WiFi present on show network? (NDI defaults to it — should be disabled on Resolume host): Yes, there will be an SSID for either vlan 150 or 160, whatever needs it most, obviously no wifi for NDI stuff

## Audio

- Audio interface(s) (model, channel count, driver): Likely some kind of focusrite (need ot purchase when we know how many channels) OR Dante virtual soundcard (own already)
- LTC output path (dedicated channel? Dante?): Deicated path either way, its own output on a physical or its own output on dante.
- Program audio path (FM transmitter? speakers?): FM transmitter. A third-party listener application is also in use on the FPP host; its integration is being discussed with its developer and is deliberately not specified here.
- Dante devices and clock master, if applicable: Dante master is a BSS BLU-DAN

## Video / projection

- Projector model(s), resolution, refresh: Mixed models all 720 — all support PJLink (purchased for it); per-model PJLink class (`CLSS?`) still to be probed
- Transport plan per surface (NDI / HDMI+capture): Node to resolume should be NDI primary, but also support HDMI/DP out on the video nodes as well, if its NDI i’ll pull directly into resolume then its HDMI > convert to SDI or HDMI over cat6 > long cable > converter > projector
- Capture hardware, if any: 2 generic HDMI dongles
- EDID management approach: not sure

## Timing sources

- NTP/PTP arrangement on show network: I have an axia xNode that can do PTP if we need it.
- LTC frame rate (must match Resolume input config): should be configurable no matter what
- Expected sequence frame timing (25/40 fps, i.e., 40/25 ms step time): should be configurable

## Power

- Circuits, UPS coverage, and power-restore ordering: Sounds like a future problem, but just know i’m working on it
- Per-circuit power monitoring: ESPHome-flashed Emporia Vue — per-circuit AC power over MQTT; usable as coarse cross-check evidence for receiver telemetry blind spots (see RES-011)

## Change log

| Date | Change | Research invalidated |
|---|---|---|
| 2026-08-10 | Confirmed K16A-B is the eFuse variant (per-string current monitoring); all projectors PJLink-capable; deployed smart receivers are pre-V5 (no telemetry, planned upgrade); Kulp boards support V5 receiver protocol; ESPHome Emporia Vue per-circuit power monitoring available | RES-011/RES-012 evidence updated to match |

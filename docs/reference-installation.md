# Reference Installation

> **A note on identifiers, added 2026-08-14 and corrected the same day.** This repository is public. Addresses, account names and board serials specific to the operator's network are **deliberately omitted**, and host names are **placeholders**, because none of them is needed to understand the topology and all of them are permanent once committed. What matters here is the *shape*: hardware classes, firmware versions, protocol behaviour, topic structure and cadences.
>
> `fpp-player`, `fpp-remote-a`, `fpp-remote-b` and `fpp-ghost` are **not the real host names**. They are the placeholder scheme, and the same four names are used across this document, the test fixtures and the test code, so a host is recognisable as one host everywhere. The mapping and what it did and did not change are recorded in [`internal/coordinator/collector/fpp/testdata/README.md`](../internal/coordinator/collector/fpp/testdata/README.md).
>
> The first version of this note claimed host names were omitted while the real ones were printed in the table below, which is the failure mode it was written to prevent. Real addresses live in the deployment's own `.env` and in `docs/private/`, which is gitignored. **Do not reintroduce them**, and do not treat a redacted value as unknown: it is known, and it is simply not written down here.


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
| FPP player (authoritative scheduler) | Raspberry Pi **3** Model B+ (`fpp-player`) | OS v2025-09 | FPP **9.4** (branch v9.4) | Probed live 2026-08-11. `multisync: true`. Was recorded as a Pi 4 B+ and "latest"; both were wrong, see the version note below |
| FPP remotes (if any) | Kulp K16A-B on BeagleBone Black (`fpp-remote-a`); Kulp K16-Max on PocketBeagle2 (`fpp-remote-b`); Kulp K16-Pro (standby, not probed) | OS v2025-11 on both probed remotes | remote-01: **9.x-master-822-g56515e4d** (master branch, not a release); remote-04: 9.4 | K16-Max and K16A-B (eFuse variant, operator-confirmed) both have per-string current monitoring; K16-Pro is standby only, blade-fused, no current telemetry. **remote-01 runs master deliberately**: a bug hit during last year's display was fixed in master, and the operator chose not to move to a release mid-show while master was working |
| Sequencing workstation | MacBook Pro M1 Max | | xLights v - latest| |
| Resolume host | Hackintosh may sawp to windows | | Arena v 7.23.2 | Arena license required for SMPTE input |
| Media node candidates | Dell Micro 7040 i5 | Linux, probably| | |
| Audio node candidate | Dell Micro 7040 i5 | Linux, or windows depending on audio support | | |
| Smart receivers | Older Kulp differential receivers (pre-V5 protocol) | | | No current/fuse telemetry — known blind spots; planned replacement with V5-protocol receivers that report per-output current. Confirmed live 2026-08-11: these appear in `/api/fppd/ports` as entries carrying only `col`, `name`, `row`, and `smartReceivers: true`, with no `ma` key at all |
| Unaccounted FPP (`fpp-ghost`) | unknown | unknown | FPP **9.2** (branch v9.2) | **Not part of the topology above, and its role is an open question for the operator.** It does not appear in the address list and was not probed over REST. It is known only because it publishes to the MQTT broker below, where **every one of its topics arrived retained and it published nothing live during a 60-second capture on 2026-08-11**. Its `port_status` is `[]`. Whether it is powered off, decommissioned, or the standby K16-Pro is unresolved |

**Per-board port element counts, confirmed live 2026-08-11.** `/api/fppd/ports` returns a different number of elements per board and the element set is heterogeneous, so neither the count nor the layout may be hard-coded: fpp-player returns `[]` (a Pi with no output cape — zero ports is a true fact, not a failure), fpp-remote-a returns 32 elements (16 carrying `ma`, 16 smart-receiver positions), fpp-remote-b returns 48 (16 carrying `ma`, 32 smart-receiver positions). `bank` labels are non-contiguous and differ per board (`Ports 1-4`, `Ports 13-16`, `Ports 17-20`, `Ports 21-24`). `pixelCount` was absent on every element of every board.

**Player and remote report structurally different status documents.** On fpp-player (player mode) `/api/fppd/status` carries `current_playlist`, `next_playlist`, `scheduler` and `repeat_mode`. On both remotes those four keys are entirely absent and are replaced by `playlist`, `sequence_filename`, `media_filename` and `seconds_elapsed`. fpp-remote-b additionally omits `warnings` and `warningInfo` while its MQTT `warnings` topic publishes `[]`, so absence over REST and an empty list over MQTT describe the same fact by different means.

**Software version state, and the plan for this season.** The versions above are what was installed for last season and left untouched; they are a point-in-time observation from 2026-08-11, not a target. The ShowMesh FPP plugin will support **FPP 9.4–9.x and FPP 10.x**; FPP 8 is excluded. The fleet's actual move to a 9.x release or an FPP 10 release remains an operational decision. As checked 2026-08-18, FPP 10 is still `10.0-beta` and has no published RC. Until the fleet choice is made, treat it as deliberately non-uniform, and treat any ShowMesh behaviour verified against these versions as verified against *these* versions only. A fleet-wide version move is a material environment change and moves affected research conclusions to `stale` per the research workflow.

## Network

- Switch model(s) and firmware: Unifi Pro Max 16 mostly dedicated to holiday stuff, 10g uplink to full unifi core.
- VLAN layout (control vs media vs house): vlan 150 is curernt “AV LAN” Adding VLAN 160 for holiday stuff
- Multicast/IGMP snooping configuration: Configured with unifi defaults for AES67 audio across the network
- Uplinks and bandwidth budget (NDI aggregate estimate): Not a problem really, resolume machine has 2.5g ethernet, switch will be 10g to my core.
- WiFi present on show network? (NDI defaults to it — should be disabled on Resolume host): Yes, there will be an SSID for either vlan 150 or 160, whatever needs it most, obviously no wifi for NDI stuff

## MQTT

The fleet already publishes to a broker, and it is **not** ShowMesh's. Probed read-only 2026-08-11.

- **Broker:** the operator's existing home-automation broker on the standard MQTT port. Anonymous subscribe is refused; FPP authenticates as a **shared home-automation account that also holds publish rights**, which is the ADR-021 exposure in concrete form. The hostname, address and account name are deliberately not recorded here: see the note on identifiers at the top of this document.
- **All three probed hosts report `MQTT: {configured: true, connected: true}`.** An earlier note recording `connected: false` for the player was a point-in-time observation and is superseded.
- **Topic root** is `falcon/player/<HostName>/`. `MQTTPrefix` is unset on this fleet, so there is no additional prefix segment — but nothing may assume that, since the setting exists and is per host.
- **Per-host topics:** `fppd_status`, `port_status`, `warnings`, `version`, `branch`, `status`, `ready`, `playlist_details`, `playlist/{position,repeat,sectionPosition,media}/status`, `playlist/sequence/{status,secondsTotal}`, and `ha/sensor/<Name>/{config,state}` for Home Assistant discovery.
- **Cadence:** `fppd_status` roughly every 4 s; `port_status` on the remotes roughly every 1 s. Both are considerably faster than a REST poll, which is the argument for ingesting MQTT at all.
- **Every topic is published retained**, with live updates following on the same topic. A retained delivery therefore carries no valid observation time, per ADR-011 and the rule this project has now had to enforce four times.
- **`falcon/player/<host>/command/run` and `.../command/preset/triggered` are live command topics that FPP acts on**, and both are themselves retained. ShowMesh must never publish to this broker on any topic.
- **`falcon/control/*` topics** (`power/main`, `power/transmitter`, `power/pj-heat`, `projectors`, `content/brightness`, `content/up-next`, `content/day-of-week`) are the operator's own control surface. ShowMesh neither reads nor writes them.

Pointing FPP at a ShowMesh-owned broker instead would be a settings write to a live show host and is deliberately not done.

## Audio

- Audio interface(s) (model, channel count, driver): Model intentionally unselected. Any Linux-supported multichannel interface is a candidate if runtime discovery and commissioning prove independently addressable outputs for the configured program channel set plus one non-overlapping LTC channel from one clock domain. The stereo reference needs at least three outputs; a mono installation needs at least two. No model gates Track C.
- LTC output path (dedicated channel? Dante?): Dedicated output from the same clock domain as program. A particular physical or Dante route is selected only after its capabilities and clock relationship are verified.
- Program audio path (FM transmitter? speakers?): FM transmitter. A third-party listener application is also in use on the FPP host; its integration is being discussed with its developer and is deliberately not specified here.
- Dante devices and clock master, if applicable: Dante master is a BSS BLU-DAN

## Video / projection

- Projector model(s), resolution, refresh: Mixed models all 720 — all support PJLink (purchased for it); per-model PJLink class (`CLSS?`) still to be probed
- Transport plan per surface (NDI / HDMI+capture): Node to resolume should be NDI primary, but also support HDMI/DP out on the video nodes as well, if its NDI i’ll pull directly into resolume then its HDMI > convert to SDI or HDMI over cat6 > long cable > converter > projector
- Renderer reference profile: one logical surface per Dell Micro 7040-class x86 renderer, 40 fps, NDI output. FPP Connect uploads the sequence/FSEQ before playback; the renderer extracts its virtual-matrix channels locally.
- Surface-to-projector mapping: a single combined logical surface may feed a projector pair downstream in this installation. Other deployments may choose one surface per projector; this is configuration outside the projector-agnostic renderer model.
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
| 2026-08-11 | Live read-only probe for Step 5. Recorded the MQTT section above; confirmed per-board port element counts and the empty array on fpp-player; recorded that player and remote report structurally different status documents; found a fourth FPP (`fpp-ghost`, v9.2) present only as retained MQTT state | Nothing invalidated. RES-011 gains shape evidence only — every `ma` reads 0 with the display de-energized, which is not evidence that current telemetry works |
| 2026-08-10 | Confirmed K16A-B is the eFuse variant (per-string current monitoring); all projectors PJLink-capable; deployed smart receivers are pre-V5 (no telemetry, planned upgrade); Kulp boards support V5 receiver protocol; ESPHome Emporia Vue per-circuit power monitoring available | RES-011/RES-012 evidence updated to match |

# RES-019: PTP-Synchronized Multi-Node Audio Playback

[Research tracker](README.md) · [Audio Engine](../architecture/AUDIO-ENGINE.md) · [RES-007](RES-007-audio-node-architecture.md) · [ADR-007](../decisions/ADR-007-gstreamer-media-engine.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [Track C](../build/TRACK-C-audio-node.md)

Status: planned · Risk: **high** · Verification: **L1 source evidence** for upstream FPP master, linuxptp, GStreamer, PipeWire, kernel, and Raspberry Pi driver behavior; **L0** for every ShowMesh measurement, every proposed loop constant, and every hardware claim about the reference installation

Research date: 2026-08-28. The FPP facts below were read from `FalconChristmas/fpp` master at commit `d318b1d` (2026-08-28) and are changing daily; §12 lists every commit used. Nothing in this record is a frozen contract, and nothing here has been built.

## 1. Why this record exists

An installation may now declare more than one audio node with roles (ADR-045, `dev/multi-audio`). Two nodes that receive the same cue play the same file, and the current implementation aligns them only at start: each node's `Start` seeks its local file to the requested position and moves the shared pipeline to PLAYING when the command arrives (`internal/agent/audio/gstengine/methods.go`, `Start`). After that, playback follows the node's own audio interface clock. Two USB interfaces of the same model can differ by tens of ppm; 20 ppm is 72 ms per hour, and 50 ppm is 3 ms per minute. Two nodes that begin aligned drift apart across a cue and across a night, and nothing today measures it: `node.audio.clock.alignment` reports `not_collected` on every poll (`internal/coordinator/collector/nodeaudio/signals.go`).

The multi-node decision deliberately excluded cross-node sample sync and realigns nodes at cue boundaries, which are silent. That is sufficient for two independent zones. It is not sufficient for two nodes that cover overlapping listening areas, for an FM feed and a yard feed that a listener can hear together, or for any future AES67 output, which requires a PTP-referenced media clock by definition.

This record therefore researches a reusable node-level clock subsystem: one media clock, several possible providers, scheduled presentation against that clock, continuous sample-rate reconciliation of each audio interface to it, and per-output static latency compensation. AES67 must later consume the same clock and timeline instead of introducing a second synchronization system.

## 2. Four separate problems, not one "PTP sync"

| # | Problem | What solves it | What it does not solve |
|---|---|---|---|
| 1 | **Shared media time.** Nodes agree what time it is, to well under a millisecond. | PTP (IEEE 1588-2008) through linuxptp `ptp4l`, hardware timestamped where the NIC has a PHC. | Nothing about when media starts or how fast the DAC runs. |
| 2 | **Presentation alignment.** Nodes agree that media sample zero is presented at one future instant `T0` on the shared clock. | A scheduled-start command carrying `T0` in the PTP domain, with preroll complete before `T0`. | Drift after `T0`. |
| 3 | **Sample-rate alignment.** Each audio interface's crystal runs at its own rate; a nominal 48 kHz device emits 48 000 ± tens of samples per PTP second. | Continuous rate matching: a variable-ratio resampler (or PipeWire's rate-matching DLL) driven by the measured error between PTP-expected and actually-consumed samples. | Fixed offsets. |
| 4 | **Static output latency.** Different interfaces, drivers, buffer depths, converters, and downstream amplifiers present the same sample at different fixed delays even when perfectly rate-locked. | A per-output calibrated offset, measured acoustically or electrically, applied to the scheduled timeline. | Anything time-varying. |

PTP solves problem 1 and is the reference that makes 2, 3, and 4 solvable. Problem 3 is the one the current architecture forbids solving (§3.3), and problem 4 is the one nobody can measure yet (§8).

## 3. Current ShowMesh state (source verified, this repository)

### 3.1 Pipeline clock

`gstengine.buildPipeline` creates a `pipeline` element and never calls `UseClock` (`internal/agent/audio/gstengine/engine_cgo.go`). GStreamer's default selection then picks the clock provided by the sink, which for `alsasink` is the device's own ring-buffer audio clock. Every position, fade, and LTC anchor in the engine is therefore measured in audio-interface time, and two nodes have two unrelated timelines.

### 3.2 Start semantics

`session.start` prepares the current item and starts it from its bookmark or from zero when the command arrives (`api/openapi.yaml`, `audio:command`). There is no scheduled start time in the command, the engine, or `pkg/audio`. Cross-node alignment at start is therefore bounded by the difference in command delivery and pipeline preroll time between nodes, which is unmeasured.

### 3.3 Drift policy

AUDIO-ENGINE §4.2 forbids continuous clock discipline of program audio: align at start, measure, ignore below a threshold, correct at track boundaries, allow a discrete seek when drift is operationally significant, "avoid audible playback-rate manipulation". ADR-018's alternatives section rejected "correcting in software" on the grounds that the correction would be continuous rate adjustment or repeated seeks. Both texts were written against a free-running node with no shared clock and no measurement, and against the audible artefacts of MultiSync-style four-frame slews.

Problem 3 requires exactly the continuous rate correction those texts reject. The distinction that matters, and that the existing text does not draw, is between **a step or slew of tens of milliseconds**, which is audible, and **a ppm-scale ratio trim applied through a high-quality variable-ratio resampler**, which the AES67 and RAVENNA ecosystem, PipeWire, and zita-ajbridge apply continuously as a matter of course. [ADR-046](../decisions/ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md) (owner, 2026-08-28) draws that distinction: rate trim against a locked shared clock is permitted; slews, seeks as ordinary correction, and chasing the position feed are not. The owner's original evidence for the ban was FPP MultiSync driving audio on a remote FPP node in 2025, a position-feed chase with frame-scale slews that sounded like a skipping CD; that mechanism stays forbidden.

`audio.settings.driftIgnoreThresholdMs` defaults to 20 ms and is stored but consumed by nothing (`internal/coordinator/config/audiosettings.go`, Track C report).

### 3.4 Clock domain declaration

`audio.node.clockDomain` and `clockDomainProvenance` are operator-declared strings used to place program and LTC on one interface (ADR-018). They describe a hardware fact, not a synchronized clock, and nothing reads them as a timing source. The clock subsystem below leaves them unchanged.

### 3.5 MultiSync

`pkg/multisync` follows FPP's remote-sync semantics for the lighting timeline. FPP MultiSync has no PTP involvement (§4.1, item 10), so nothing in this record changes the show-timeline path. The show timeline remains what audio aligns to at cue start; PTP is the clock nodes share with each other, not a replacement for the FPP timeline.

## 4. Verified upstream facts

### 4.1 FPP master, `d318b1d`, 2026-08-28 (L1, source)

1. **PTP is owned by `AES67Manager`, not by a timing service.** `ptp4l` is fork/exec'd from `src/mediaoutput/AES67Manager.cpp` (`InitPTP`, lines 557 to 619). `grep -rl ptp4l src` returns only that class, a route doc string, and a boot-time cleanup of a legacy config path. There is no standalone PTP daemon in FPP.
2. **`ptp4l` runs only when AES67 is active.** `ApplyConfig` (lines 303 to 361) requires `MediaBackend == "pipewire"`, the file `<media>/config/pipewire-aes67-instances.json`, at least one `enabled` instance (send, receive, or both), and `ptpEnabled` (default true). With zero enabled instances it logs "No enabled instances" and returns before `InitPTP`. Every AES67 Apply calls `ShutdownPTP` first and restarts `ptp4l`. **A ShowMesh audio node cannot assume PTP exists on an FPP 10 host.** `www/settings.json` carries an `AES67PTPEnabled` setting with no consumer in `src` or `www`.
3. **Config written to `/tmp/fpp-ptp4l.conf`** (`WritePtpConf`, lines 469 to 516): `domainNumber <ptpDomain>` (default 0), `twoStepFlag 1`, `priority2 128`, `clockClass 248`, `logAnnounceInterval 1`, `logSyncInterval -3`, `logMinDelayReqInterval 0`, `announceReceiptTimeout 3`, `network_transport UDPv4`, `delay_mechanism E2E`, `time_stamping hardware|software`, optional `dscp_event`/`dscp_general 46`. Three start attempts: hardware+DSCP, software+DSCP, software. Interface comes from `-i <ptpInterface>` (default `eth0`).
4. **Roles** (`AES67Manager.h` lines 65 to 70): `auto` = `priority1 248`, not slave-only, so FPP still becomes grandmaster when alone but yields to professional gear at 128; `master` = `priority1 127`; `follower` = `slaveOnly 1`. The in-source comment explains the 248 choice: a tie at 128 is broken by clock identity (lowest MAC), "which is how an FPP box ends up grandmastering a Q-SYS or Yamaha domain it should have been following."
5. **`phc2sys` was removed on 2026-08-27** (`0c8eaad`). It previously ran `phc2sys -a -r -n <domain>` whenever `ptp4l` started. Commit message: "PTP's timescale is whatever the grandmaster distributes, and Dante/Brooklyn devices commonly distribute an arbitrary epoch rather than wall time, one measured at ~4411 seconds, i.e. time since that device booted. `phc2sys -a -r` faithfully slaved CLOCK_REALTIME to it and dragged the Pi's clock back to 1970." The retained comment (lines 597 to 614) says a site wanting PTP-disciplined system time should run `phc2sys` from systemd with a chosen offset.
6. **FPP reads PTP time directly.** `FppPtpClock` is a `GstSystemClock` subclass whose `get_internal_time` calls `clock_gettime` on the dynamic POSIX clock id of an open `/dev/ptpN` (lines 941, 973 to 1012, macro `FPP_FD_TO_CLOCKID`). The PHC index comes from `ETHTOOL_GET_TS_INFO` on `ptpInterface`. If there is no PHC, the clock id falls back to `CLOCK_REALTIME`, on the reasoning that software-timestamped `ptp4l` disciplines `CLOCK_REALTIME` itself. The rationale comment (lines 916 to 937) rejects `CLOCK_TAI` ("only correct while something is setting the kernel TAI offset") and `GstPtpClock` ("would run gst-ptp-helper as a second PTP client on the same domain alongside our ptp4l"). Inference: the selection does not know which timestamping mode `ptp4l` actually reached, so a NIC with a PHC where `ptp4l` fell back to software timestamping would read an undisciplined PHC.
7. **Lock and grandmaster state come from `pmc` over the `ptp4l` UDS socket**, `GET TIME_STATUS_NP` (`gmPresent`, `gmIdentity`, `master_offset`) and `GET PORT_DATA_SET` (`portState`), cached 1 s (`RefreshPtpCache`, lines 756 to 829). "Synced" means `gmPresent` with a non-empty `gmIdentity`, or own port state MASTER/GRAND_MASTER. No offset threshold is applied.
8. **RTP timestamps are derived from the PTP clock** when `ptpMediaClock` is true (default): `gst_pipeline_use_clock(pipeline, ptpClock)`, base time 0, start time NONE, `rtpL24pay timestamp-offset=0 perfect-rtptime=false`, so RTP ts = PTP ns × 48000 / 1e9 mod 2^32 (lines 1637 to 1670). `udpsink sync=false` remains the default; `sinkPacing` (default false) switches it to `sync=true ts-offset=40ms`. `GstPtpClock` is not used anywhere in current code; `docs/FPP_Audio_Architecture.md` and the `BuildSDP` note still say it is, and are stale.
9. **Sound card versus PHC.** The maintainer measured "56ppm, measured three independent ways agreeing to ~1ppm. That is 3.4ms per minute" on their hardware, ~80 ppm earlier on the same tester bench, and a contributor reported ~111 ppm elsewhere. Header comment `AES67Manager.h` lines 446 to 451: the sound card free-runs against the PHC and "no PTP setting can remove this". **These are hardware-specific numbers, not constants.**
10. **Drift correction, `driftResample`** (`d081180`, 2026-08-27, default off, compiled only when `<samplerate.h>` is present): a pad probe on `identity name=driftpoint` in F32LE before the S24BE conversion runs libsamplerate `SRC_SINC_FASTEST` with feedforward `target = 48000 / measured_card_rate`, feedback `KP = 0.002` on accumulated sample error, clamp ±300 ppm, slew 2 ppm per buffer, 5 s warm-up, re-anchor on a gap over 50 ms or a DISCONT, keeping the learned trim. An earlier `adaptiveResample` path through a `pitch` element is dead scaffolding: `speed` and `pitch` do not actuate on a live stream (`d2d3c82`, `5dec0c4`). `audiorate` was ruled out (`f120313`).
11. **PTP clock step handling** (`d318b1d`): the drift probe detects `now < anchor` (a backward PHC step, which happens when a follower first locks) and re-anchors its measurement window while keeping the trim. Forward steps are absorbed by the 50 ms gap rule. Nothing else in FPP reacts to a step; the pipeline clock is not re-based. The commit message says "Slave-mode behaviour is otherwise untested here, there is no second grandmaster on this network to test against."
12. **Status API.** `GET :32322/api/aes67/status` (fppd, proxied by PHP `GET /api/pipewire/aes67/status`) returns `ptp{synced, offsetNs, grandmasterId, portState, isGrandmaster, enabled, domain, role}` plus pipelines and discovered streams. It exposes **PTP state, never PTP time**, and only while AES67 is active. No MQTT publication, no shared memory, no file.
13. **PipeWire is not disciplined to PTP in FPP.** The graph follows the output card; AES67 receive runs on GStreamer's default clock and plays through `pipewiresink` as an `Audio/Source` behind a jitter buffer. Only the send path is PTP-driven.
14. **MultiSync has no PTP involvement**; timing is `GetTimeMS()` and packet based. The AES67 UI states the PTP role "is unrelated to FPP's Master/Remote player mode."
15. **Issue #2848 chronology** (2026-08-24 to 08-28): a contributor opened it over out-of-spec `ptp4l` intervals, the status API and SDP reporting the Pi's own identity regardless of BMCA, and missing DSCP (PR #2849 merged 08-24). An external tester with a Yamaha MRX7-D reproduced FPP staying MASTER at priority 128 against the Yamaha's 119, and the Yamaha muting when forced to follow FPP. The maintainer added roles (08-24), the PHC media clock and packet-sized buffering (08-25, explicitly rejecting `GstPtpClock`), found the fractional-quantum theory "dead" (08-26), removed `phc2sys` after the tester's 1970 report (08-27), fixed the send-rate collapse with `pipewiresrc min-buffers=16` (08-28), and shipped `driftResample` and `sinkPacing` default off (08-28), asking testers for `AES67 drift` log lines. Holdover is never mentioned in the issue or the code.

### 4.2 linuxptp (L1, man pages and source)

- Hardware timestamping: `ptp4l` opens `/dev/ptpN` `O_RDWR` and disciplines that PHC (`clock.c`, `phc_open`). Software or legacy timestamping: `clkid = CLOCK_REALTIME`, there is no PHC in the loop, and the announced timescale as grandmaster is "Arbitrary time scale mode, which is effectively UTC here" (`ptp4l.8`, TIME SCALE USAGE). With hardware timestamping a grandmaster announces the PTP (TAI) timescale and "it is up to phc2sys to maintain the correct offset between UTC and PTP times."
- `clientOnly` (formerly `slaveOnly`), `priority1` (default 128, lower wins), `domainNumber` (default 0), `step_threshold` (default 0.0: never step after the first update), `first_step_threshold` (default 20 µs), `clock_servo` `pi|linreg|ntpshm|refclock_sock|nullf`, `free_running`.
- Management: `pmc -u` over `uds_address` (default `/var/run/ptp4l`); a read-only socket `uds_ro_address` (`/var/run/ptp/ptp4lro` on master, mode 0666) "restricted to GET actions" exists for untrusted monitors. Each `pmc` client binds its own `pmc.<pid>` datagram socket and `ptp4l` replies per sender, so concurrent clients are supported by design. `TIME_STATUS_NP` gives `master_offset`, `gmPresent`, `gmIdentity`; `TIME_PROPERTIES_DATA_SET` and `GRANDMASTER_SETTINGS_NP` give `ptpTimescale`, `currentUtcOffset`, `timeTraceable`, `clockClass`.
- `phc2sys -a -r` copies the PHC to `CLOCK_REALTIME` following `ptp4l` port states; `-s`/`-c` name explicit source and sink; `-O` sets the TAI offset. It is unnecessary with software timestamping.
- Reading a PHC: kernel `Documentation/driver-api/ptp.rst` states that an open `/dev/ptpN` fd is usable as a POSIX clock id for `clock_gettime`. `kernel/time/posix-clock.c` requires `FMODE_WRITE` only for `settime` and `adjtime`; `gettime` needs read access alone. Multiple processes may hold the device open concurrently; a read does not disturb `ptp4l`'s discipline (inference from the kernel code path, not exercised here). Distributions typically create `/dev/ptpN` as `root:root 0600`, so an unprivileged agent needs a udev rule or group.

### 4.3 PTP timescale and AES67 (L1, RFC 7273, RFC 8173, secondary sources)

- The PTP epoch is 1970-01-01 00:00:00 TAI. `currentUtcOffset` is meaningful only "in PTP systems whose epoch is the PTP epoch" (RFC 8173). A grandmaster may announce `ptpTimescale = false` (ARB); followers must then make no assumption about the relation to any standard timescale.
- AES67 uses PTPv2 with the default profile mandatory and a media profile (Annex A) recommended: domain 0 default, `logSyncInterval` default -3, `logAnnounceInterval` default 1. SMPTE ST 2059-2 defaults to domain 127. RFC 7273 signals the reference as `a=ts-refclk:ptp=IEEE1588-2008:<gmid>:<domain>` and `a=mediaclk:direct=<offset>`, where the offset "indicates the RTP timestamp value at the epoch of the reference clock"; direct mode "SHOULD use a TAI timestamp reference clock".
- Dante devices use PTPv1 by default; AES67 mode enables PTPv2 on domain 0 and one Brooklyn/Broadway/HCX device bridges the two. That Dante-only networks commonly distribute an arbitrary epoch is FPP's field observation (§4.1 item 5), not something verified against an Audinate source; treat it as a well-supported hypothesis and design for it.

### 4.4 GStreamer (L1, docs and source at `main`)

- `gst_pipeline_use_clock` forces the pipeline clock; the default otherwise prefers the sink-provided clock for a non-live pipeline.
- `GstAudioBaseSink` `slave-method`: `skew` (default) steps the ring-buffer playout pointer when the moving-average skew between the pipeline clock and the device clock exceeds half of `drift-tolerance` (default 40 000 µs, so 20 ms steps); `resample` sets the pipeline clock as master of the device clock and remaps timestamps through the calibrated rate, but **`gstaudiobasesink.c` contains no sample-rate conversion**: drift still resolves into occasional dropped or inserted samples at the write pointer, and the observation-adding code is `#if 0`. `none` does nothing; `custom` installs a callback receiving both clock times and returning a requested skew. Continuous ppm correction inside GStreamer therefore needs `GstAudioResampler` with `gst_audio_resampler_update` driven by ShowMesh, or an external library, or PipeWire.
- `GstPtpClock` (since 1.6) is a slave-only, IPv4-multicast, software-timestamped PTPv2 client run through a privileged `gst-ptp-helper` bound to UDP 319/320 with `SO_REUSEADDR|SO_REUSEPORT`. It sends its own Delay_Req with its own clock identity and disciplines an in-process `GstSystemClock` by linear regression. It never uses a PHC. It can coexist with `ptp4l` on the ports (linuxptp also sets `SO_REUSEADDR`), but it is a second participant on the wire and a worse clock than hardware-timestamped `ptp4l`.
- `GstSystemClock` `clock-type` offers only REALTIME, MONOTONIC, TAI, and OTHER. It cannot take an arbitrary `clockid_t`. Reading a PHC from GStreamer requires a custom clock: a `GstSystemClock` subclass with `get_internal_time` (FPP's approach) or `gst_audio_clock_new` with a callback. Whether go-gst exposes either is **unverified**; the GIR-generated `pkg/gstaudio` and `pkg/gstnet` packages could not be read, and a cgo shim under ADR-042 is the fallback.
- `GstNetTimeProvider` exports any `GstClock` over UDP and `GstNetClientClock` follows it with RTT filtering and linear regression; precision on a LAN is undocumented and is a software-timestamp clock in any case.

### 4.5 PipeWire (L1, source at `master`, NEWS)

- The graph is driven by one driver node filling `spa_io_clock`; ALSA nodes name their clock `api.alsa.<p|c>-<card>` and, when following a driver with a different `clock.name`, rate-match through `spa_dll_update` feeding the adapter's resampler (`resample.quality` 0 to 14, default 4). Nodes sharing a `clock.name` skip the resampler.
- **`support.node.driver` can be clocked from a PHC.** Properties `clock.device` ("/dev/ptp0", opened `O_RDONLY`, `FD_TO_CLOCKID`) or `clock.interface` ("eth0", resolved by `ETHTOOL_GET_TS_INFO`), plus `clock.id` `monotonic|realtime|tai|monotonic-raw|boottime`, `resync.ms`, `sync.force-tracking`. The driver runs a monotonic timer and tracks the PHC with a DLL. Shipped since 0.3.66 (2023-02-16, "synchronize the graph to a PTP clock"), `clock.interface` since 0.3.82, PTP driver priority bump 1.0.1, RTP PTP management client 1.2, RTP PTP clocking 1.4.0 (2025-03-06).
- With that driver at a priority above the ALSA sink, the sink becomes a follower and is rate-matched to the PHC. `pipewiresink` from GStreamer inherits the graph clock. `module-rtp-sink` supports `sess.ts-refclk`, `sess.ts-direct`, and `aes67.driver-group`; `module-rtp-sap` queries `ptp4l` over `ptp.management-socket` (`PARENT_DATA_SET`) and writes `ts-refclk` only when synced to an external grandmaster, domain hard-coded 0. This is the AES67 path PipeWire ships; it is not exercised here.

### 4.6 Raspberry Pi hardware timestamping (L1, driver source and issue trackers)

| Board | NIC path | PHC | Notes |
|---|---|---|---|
| Pi 5 | RP1 GEM (`cdns,macb`) | yes (`macb_ptp.c`) | raspberrypi/linux #5904 reports "received SYNC without timestamp" on 6.1/6.6 kernels; `hwts_filter full` or `ptp_minor_version 0` reported as workarounds |
| CM4 | bcmgenet + BCM54210PE PHY | yes, PHY timestamping since Linux 6.0 (`bcm-phy-ptp.c`) | stock Pi OS enablement unverified |
| Pi 4B | bcmgenet + BCM54213PE | no | software timestamping only |
| Pi 3B+ | LAN7515 USB | no | USB adds scheduling jitter even to software timestamps |

x86 Intel NICs (i210, i225, i226, e1000e) commonly have a PHC; confirm with `ethtool -T <if>` per host. Every node in the reference installation must be checked this way before its achievable accuracy is claimed.

## 5. Proposed clock-provider architecture (recommendation, L0)

### 5.1 The media clock is a PTP-domain clock, never wall time

The subsystem exposes one **media clock** per node: a monotonic-within-lock nanosecond counter in the PTP domain's timescale, whatever that timescale is. It is read from the NIC PHC when `ptp4l` runs with hardware timestamping, or from the clock `ptp4l` is disciplining when it runs with software timestamping. ShowMesh never requires `CLOCK_REALTIME` to become PTP time and never runs `phc2sys -r` on its own authority. FPP learned this on real Dante hardware (§4.1 item 5); AES67 direct-mode RTP timestamps only need PTP nanoseconds modulo 2^32, so an arbitrary epoch is harmless to media and harmful only when copied into the system clock.

Consequences: the coordinator cannot convert a media-clock instant to a wall-clock instant without a per-node offset sample, so scheduling (§6) is expressed in media-clock time by a node that holds the clock, not in UTC by the coordinator. The audit log and telemetry keep timestamps in wall time as today; media-clock values are reported alongside, labelled by domain and grandmaster.

### 5.2 Provider interface

Exact names are not frozen. The provider exposes:

- `now()` in the media clock, with an error bound and a validity flag;
- status: `state` (`unsynchronized`, `acquiring`, `locked`, `holdover`, `failed`), `role` (`grandmaster`, `follower`, `passive`, `listening`), `source` (which provider), `ptpDomain`, `grandmasterIdentity`, `timescale` (`ptp`, `arb`, `unknown`), `offsetNs` (`master_offset`), `clockClass`, `timestamping` (`hardware`, `software`), `ownership` (who runs `ptp4l`), `lastStepAt` and step magnitude, observation age;
- events: locked, lost, grandmaster changed, clock stepped (with sign and magnitude), provider stopped.

### 5.3 Providers

| Provider | Owns `ptp4l` | Reads time from | When |
|---|---|---|---|
| ShowMesh-managed linuxptp | yes: writes config, supervises the process, chooses role | PHC via `clock_gettime(FD_TO_CLOCKID)`, or the disciplined system clock in software mode | dedicated ShowMesh audio node, no other PTP participant on the interface |
| External linuxptp | no: observes only | same, plus `pmc` on the read-only UDS socket | site runs `ptp4l` from systemd, or a distribution service already owns it |
| FPP AES67 PTP | no: observes only | same PHC; state from `GET /api/aes67/status` and the `ptp4l` UDS socket | ShowMesh agent on an FPP 10 host with an enabled AES67 instance; **absent when no AES67 instance is enabled** (§4.1 item 2) |
| External professional domain | no | same as above, following the site grandmaster (BSS, Q-SYS, Yamaha, Dante bridge) | production AES67 deployments; ShowMesh is always a follower here |
| Degraded non-PTP fallback | no `ptp4l` | `CLOCK_MONOTONIC` on the node, plus the existing cue-boundary realignment | PTP unavailable; reports `unsynchronized`; retains today's behavior |

One rule governs all of them: **exactly one component owns `ptp4l` on an interface.** Before the ShowMesh-managed provider starts, it must prove no `ptp4l` is bound on that interface and domain (process table plus UDS socket presence, the check FPP itself performs badly enough to have had a double-start bug, `AES67Manager.cpp` lines 626 to 629). If FPP or systemd owns it, ShowMesh observes and never restarts, reconfigures, or competes in BMCA. `GstPtpClock` is excluded for the reason FPP gave: it would be a second participant.

Role policy for the managed provider follows FPP's: `priority1 248` in auto so a professional grandmaster wins, `clientOnly` when the operator declares an external domain, explicit master only where ShowMesh is the only clock. Two ShowMesh nodes alone on a LAN elect one grandmaster among themselves by BMCA; which one does not matter for problems 2 to 4.

### 5.4 Relation to `audio.node.clockDomain`

Unchanged. That field states that program and LTC leave one hardware clock. The media clock is the reference the hardware clock is disciplined toward; LTC stays sample-locked to program because both come out of the same rate-corrected stream on the same interface. ADR-018's requirement is strengthened, not weakened.

## 6. Scheduled-epoch playback (recommendation, L0)

Replace start-on-arrival with:

```text
PREPARE <session> <item>            open, decode, preroll, report ready with preroll latency
START   <session> AT <T0>           T0 is a media-clock instant chosen by the coordinator
```

The coordinator picks `T0 = max(node ready times) + command delivery bound + margin`, all in media-clock time. Because the coordinator does not hold the media clock (§5.1), it obtains it from the node that holds the `program+ltc` role: that node reports `now()` with each readiness message and the coordinator offsets from that sample. A `T0` in the past when a node receives it is a refused start with a distinct fault, not a late start.

During playback each node computes:

```text
expected = media_now - T0 + static_offset(output)
actual   = presented sample count / nominal rate   (from the sink clock, not the decode frontier)
error    = expected - actual
```

Small errors feed the rate loop (§7). An error beyond a threshold to be measured (first guess: the existing 20 ms `driftIgnoreThresholdMs`, finally consumed) after a PTP step, restart, or device change is a discontinuity: a flushing seek to `expected`, as `Resume` already does through `seekTo`, reported as a `resync` event, never fed into the rate loop. Network command latency stops mattering as long as every node receives the schedule before `T0`.

Cue boundaries continue to realign, so a node whose provider is `unsynchronized` keeps today's behavior exactly.

## 7. Sample-rate correction (recommendation with two candidate mechanisms, L0)

### 7.1 Why the audio sink's own slaving is not enough

`alsasink slave-method=skew` steps 20 ms at a time and `resample` does not resample (§4.4). Neither is acceptable for program audio; the existing §4.2 text is right about that. FPP's `speed` and `pitch` attempts failed to actuate at all. What works in the field is a variable-ratio resampler driven by a slow DLL: zita-ajbridge (built for "sound cards which do not have a common word clock"), PipeWire's ALSA follower, and now FPP's libsamplerate probe.

### 7.2 Candidate A: PipeWire owns the graph clock

Run a `support.node.driver` with `clock.device=/dev/ptpN` at driver priority above the ALSA sink; the ShowMesh engine outputs through `pipewiresink` (the `SinkFactory` is already a parameter). PipeWire rate-matches the ALSA sink to the PHC through its DLL and resampler; ShowMesh touches no samples, which keeps the ADR-007 boundary intact. Costs: a PipeWire dependency on every audio node (FPP 10 hosts already have it; the current audio node is raw ALSA and the "PipeWire or raw ALSA" question in RES-007 is open), PipeWire's `resample.quality` and DLL constants are what they are, and the LTC channel goes through the same resampler (acceptable: LTC is a bi-phase audio signal and survives ppm resampling; needs bench confirmation against the Resolume reader, RES-001).

### 7.3 Candidate B: ShowMesh drives `GstAudioResampler` from a custom clock

Force the pipeline clock to a PHC-backed `GstClock` (FPP's `FppPtpClock` pattern, in a small cgo shim if go-gst cannot subclass), keep `alsasink` with `slave-method=none`, and insert a rate-trim stage before `interleave` whose ratio is updated by a DLL comparing PHC elapsed time with samples consumed by the sink. FPP's `driftResample` is a working reference implementation of the loop (feedforward measured card rate, `KP 0.002`, clamp ±300 ppm, slew 2 ppm per buffer, 5 s warm-up, re-anchor on step or gap). Costs: ShowMesh code inside the sample path, which ADR-007 forbids unless the trim stage is a stock element driven only by property updates; a custom clock in cgo; and the loop constants become ShowMesh's to tune.

### 7.4 Recommendation

Spike A first; it is the smaller change, and a positive result answers RES-007's PipeWire question at the same time. Spike B only if A fails to hold lock, produces audible artefacts, or cannot coexist with FPP's PipeWire graph on a shared host. Either way the correction is expressed as a ppm trim, applied gradually, clamped, and observable (§10); a step is never fed into it (§9).

Hypotheses requiring the spike, none of them measured:

- H1: a ±300 ppm clamp with ≤2 ppm per-buffer slew is inaudible on program material through a MOTU M4 and a Scarlett Solo.
- H2: two nodes rate-locked to one PHC hold each other within ±1 ms over 60 minutes without any seek.
- H3: LTC read by Resolume stays locked through continuous ppm trimming.
- H4: `pipewiresink` under a PTP node-driver adds no more than the currently measured LTC lead (the queue-depth work already done in Track C).

## 8. Static output latency (definition only, no values)

Two rate-locked outputs still differ by a fixed delay: USB isochronous depth, ALSA period and buffer, PipeWire quantum, DAC group delay, and whatever is downstream (an FM transmitter's processing chain versus a powered speaker). The model is a signed per-output offset in the `audio.node` object, in microseconds, with provenance:

```text
outputLatency: { valueUs, measuredAt, method, reference, confidence }
```

`method` is one of `unmeasured` (default; applies 0 and reports `unknown`), `loopback` (electrical: output fed to an input on a third device, cross-correlated), `acoustic` (microphone at a defined position, cross-correlated against the reference node), or `declared` (operator-entered from a datasheet, lowest confidence). The value is subtracted from that output's `T0` so the sample reaches the air at the intended instant.

What must eventually be measured, per output, per sample rate, after any driver or buffer change: the delay from a scheduled `T0` to the first sample at the physical connector, its run-to-run variance (a value that changes more than a few hundred microseconds between engine restarts is not static and must be re-measured at each start), and the downstream chain delay where the audience hears it. **No value is asserted in this record; the reference installation has no measurement rig yet, and a fabricated number would be worse than zero.**

## 9. Failure behavior (requirements, L0)

| Event | Provider report | Node behavior | Show effect |
|---|---|---|---|
| Startup before PTP lock | `acquiring` | scheduled starts refused until `locked` or until the operator-configured wait expires, then fall back to §6's unsynchronized path | cue starts on time, unsynchronized, flagged |
| Grandmaster change, BMCA election | `grandmasterIdentity` changes; usually a phase step | treat as a step (next row); do not touch the rate trim until re-anchored | continues, degraded until re-locked |
| PTP clock step (either direction) | `lastStepAt`, magnitude | freeze the rate loop, re-anchor its window keeping the learned trim (FPP `d318b1d`), then resync by seek only if the position error exceeds the threshold | inaudible for small steps; one seek for large ones |
| Temporary loss of sync | `holdover` with age | keep the last trim, continue on the disciplined clock's free run; no seeks; after a configured holdover limit report `unsynchronized` | continues, degraded |
| Interface or link loss | `failed` | same as holdover; media keeps playing from local files (ADR-017) | continues, degraded |
| Node restart mid-cue | provider restarts | restore joins at `expected = media_now - T0` from the persisted schedule if `locked`, else at bookmark as today | one node rejoins in sync if PTP is up |
| Audio device disappears | unchanged | fail silent (ADR-019); rate state discarded | that output silent, manual recovery |
| Device sample rate changes | unchanged | engine rebuild as today; rate state discarded and re-learned | brief silence on that node |
| `ptp4l` owner (FPP) stops or restarts it | `failed` then `acquiring` | as holdover; never start a competing `ptp4l` | continues |
| Provider disagrees with declared domain or grandmaster | `locked` with a mismatch flag | operator-visible warning; no automatic action | continues |

Nothing in the table stops a show. Silence comes only from device loss, which ADR-019 already decides. Degradation always keeps the media playing and flags the synchronization state.

## 10. Observability (requirements, L0)

Signals, one namespace per layer so the three failure classes can be told apart:

- `node.clock.ptp.*`: state, role, domain, grandmaster identity, timescale, offset ns, clock class, timestamping mode, owner, seconds since lock, last step.
- `node.audio.timeline.*`: scheduled `T0`, expected position, actual position, error ms, resync count, last resync reason.
- `node.audio.rate.*`: measured interface rate ppm against the media clock, current trim ppm, loop state (`warming`, `tracking`, `frozen`, `clamped`), resampler in use.
- `node.audio.output.latency.*`: configured offset, method, confidence, measured-at.

"PTP not synchronized" is `node.clock.ptp.state`; "PTP fine but this engine drifts" is a growing `timeline.error` with a `rate.loop` that is `clamped` or `frozen`; "clock and rate fine but this output is offset" is a stable non-zero acoustic error with `timeline.error` near zero, which only a measurement (§8) can show. `node.audio.clock.alignment` (program to LTC) stays a separate, still-unmeasured signal.

## 11. Future AES67

The reusable asset is the media clock and timeline, not the local playback engine:

```text
local:  media clock -> timeline (T0, expected) -> rate trim -> interface
AES67:  media clock -> timeline (T0, expected) -> 48 kHz PCM -> RTP/L24 -> RTP ts = PTP ns × 48000/1e9 mod 2^32
```

Requirements this record places on the clock subsystem for AES67's sake: the provider must expose the grandmaster identity and domain for `ts-refclk`, must know the timescale so `mediaclk:direct` can be advertised honestly, and must be a follower of a professional domain when one exists. Candidate A already contains the AES67 send path (PipeWire `module-rtp-sink` with `sess.ts-direct`). FPP's own AES67 interop is unresolved (RES-007 item 3 in the FPP 10 audio issue), so nothing here claims ShowMesh AES67 interoperates with anything until a bench with a protocol analyser and two vendor receivers says so.

## 12. FPP coexistence findings

1. **FPP PTP can be an observed provider, not a dependency.** On an FPP 10 host with an enabled AES67 instance, ShowMesh can read the same PHC that FPP's `ptp4l` disciplines and read state from `/api/aes67/status` or `pmc` on the UDS socket. It must not restart or reconfigure that `ptp4l`, and it must expect it to vanish on every AES67 Apply and whenever no instance is enabled.
2. **PTP is not available on an FPP 10 node by default.** Without an enabled AES67 instance under the PipeWire backend, no `ptp4l` runs (§4.1 item 2). An installation that wants PTP on an FPP host that does not send or receive AES67 needs either a ShowMesh-managed `ptp4l` (allowed only if FPP's is provably absent) or a systemd-managed one that both observe.
3. **Never two `ptp4l` on one interface.** The managed provider's pre-start check is mandatory, and an FPP AES67 Apply after ShowMesh started its own `ptp4l` is a conflict the provider must detect (FPP's start will fail or both will fight BMCA) and report, yielding by stopping its own instance.
4. FPP's `phc2sys` removal, PHC media clock, `GstPtpClock` rejection, and drift loop all match the direction this record recommends. Their constants are not ours; their `sinkPacing`, `sourceMinBuffers`, and `driftResample` switches are all default-off and untested in follower mode by their own account.

## 13. Staged plan

Every stage ends with recorded evidence at the stated level. Stages 0 to 2 do not touch program audio's rate; stage 3 is permitted by ADR-046 and gated on its own measurements.

| Stage | Outcome | Evidence needed | Level |
|---|---|---|---|
| 0. Measure | Two real nodes, one 30 to 60 minute cue, two-channel recording of both outputs, drift curve in ms and ppm; `ethtool -T` on every node | owner hardware test | L2 |
| 1. Clock provider | Provider interface, ShowMesh-managed and external linuxptp providers, FPP observed provider, `node.clock.ptp.*` telemetry, ownership check; no change to playback | container bench with two agents and `ptp4l` in software mode; unit tests | L2 |
| 2. Scheduled start | `PREPARE` / `START AT T0`, timeline telemetry, seek-only resync on discontinuity, unsynchronized fallback | container bench: two agents, one `T0`, start skew measured from sink clocks; then real nodes | L2, then L3 |
| 3. Rate lock | Candidate A spike (PipeWire PTP driver + `pipewiresink`), candidate B only on A's failure; H1 to H4 answered | container for lock behavior; real interfaces for audibility, LTC, and long-run hold | L2, then L3 |
| 4. Latency calibration | `outputLatency` representation, measurement procedure, operator entry, applied to `T0` | loopback or acoustic rig, owner test | L2 |
| 5. AES67 | `module-rtp-sink` or GStreamer send on the same clock; `ts-refclk` from the provider | protocol analyser plus two vendor receivers | L2 |

Stage 0 needs no code beyond a recording and can run at the next hardware session. Stages 1 and 2 are ordinary seams under the existing decisions and give the multi-node installation observable synchronization state before any rate correction exists.

## 14. Open questions

Requiring a spike (H1 to H4 in §7.4, plus):

- Q1: Does go-gst expose `gst_audio_clock_new` or a subclassable `GstClock`? If not, how small is the cgo shim under ADR-042?
- Q2: Can PipeWire's PTP node-driver and FPP's own PipeWire graph coexist on one host without ShowMesh's driver taking over FPP's output groups?
- Q3: What does `ptp4l` in software mode achieve between a Pi 3B+ or 4B and an x86 node on the installation's switch, and is that good enough for stage 2 alone?
- Q4: How does `Resume`'s flushing-seek resync behave when driven by a timeline error rather than an operator command; is the current `seekTo` path fast enough to be inaudible at cue boundaries?
- Q5: Which node samples the media clock for the coordinator when the `program+ltc` node is the one that is unsynchronized?

Requiring hardware: every number in §8, the drift curve in stage 0, the perceptual threshold RES-007 already asks for, and whether the Pi 5 timestamping defect in raspberrypi/linux #5904 affects the kernel the Pi node runs.

Requiring an owner ruling: the ADR question is closed by ADR-046. Still open: whether PipeWire becomes a required dependency of the audio node (RES-007's open question, decided by stage 3's result).

## 15. FPP-originated AES67 program audio as a preferred source (hypothesis, L0 for ShowMesh; L1 for the FPP facts)

Owner brainstorm, 2026-08-28. The idea: when FPP 10 sends its program audio as AES67, a ShowMesh audio node could receive that stream instead of playing its own copy of the media. FPP stays the authoritative show player, the stream already carries PTP-referenced RTP timestamps, and every ShowMesh node (audio now, render later) could align to the same presentation timeline FPP's media playback is on, instead of two players correcting relative position through MultiSync.

### 15.1 What this collides with

ADR-017 decides that audio nodes play local media, that "real-time audio streaming may exist later as a separate input/output capability" and "is not the synchronized show-audio architecture", and it rejected "streaming PCM from the coordinator or from FPP to audio nodes" because it puts the network in the real-time path and makes every dropout a network event. AUDIO-ENGINE §8 lists stream inputs and outputs as secondary and "never required for basic show playback". ADR-017 also requires FPP's audience audio output to be disabled or unused, and the AES67 sender is fed by FPP actively playing media through its PipeWire graph.

Making AES67 the *preferred* program source with local playback as fallback is therefore a narrowing of ADR-017, not an implementation detail. The owner adopted the model in §15.3 on 2026-08-28. A narrowing ADR against ADR-017 is owed and must land before any seam builds toward it; the failover research in §15.3 and questions Q6 to Q10 stay open regardless.

### 15.2 What the FPP source actually establishes

- **Inside FPP, media is the master and the sequence follows it.** `GStreamerOut.cpp` queries `pipewiresink`'s position, corrects it by the ALSA card's queued `delay` read from `/proc/asound/cardN/pcmMp/sub0/status` (the comment records lights running ahead of sound by 22 ms on an AM62x I2S cape, 56 ms on a Pi 5 I2S cape, and 160 ms on a BeagleBone USB dongle before that correction, matched to within 3 ms after it), then calls `CalculateNewChannelOutputDelay(mediaSeconds)` (`GStreamerOut.cpp` lines 2291 to 2324, 2787). `channeloutputthread.cpp` lines 448 to 520 servo the channel-output frame to `mediaPosition × RefreshRate`: checked every 20 frames, `LightDelay` slewed by at most 15 ms per frame, frames skipped when more than four behind, a jump past half a second behind. MultiSync remotes follow the frame numbers this servo produces (`MultiSync.cpp` line 2208, `SyncSyncedSequence` line 3439). So the sequence-to-media phase relationship is **a bounded servo, not a constant**: within roughly one frame of the corrected media position in steady state, with a per-platform residual FPP itself estimates from the card's queue depth. For a `PlaylistEntryBoth` item the sequence starts when media elapsed passes `mediaOffset` (`PlaylistEntryBoth.cpp` lines 138 to 146).
- **The AES67 send taps the PipeWire graph, not the decoder.** `pipewiresrc` registers a node named `aes67_<slug>_send` that the audio group's filter chain feeds; RTP timestamp = PTP time when the buffer is payloaded. The offset between FPP's `mediaSeconds` and the RTP timestamp of the samples carrying that media instant is the graph path from decoder to that node, which nothing in FPP reports. It is measurable (known-content media, compare packet timestamp to the position FPP publishes) and is the number the renderer mapping in §15.4 depends on.
- **The stream's sample rate is the sound card's, not PTP's, unless `driftResample` is on.** RTP timestamps are PTP-referenced, but the samples behind them come off the card clock (§4.1 items 9 and 10). With `driftResample` off (default), a receiver playing in direct-timestamp mode will see the stream's sample count and its timestamps disagree by the card's ppm, which is exactly the drift this record exists to remove, moved from ShowMesh's card to FPP's. `driftResample` is default off and untested in follower mode by FPP's own account. **A "PTP-referenced media timeline" from FPP is only as good as FPP's own rate lock.**
- **FPP's pixel output is not PTP scheduled** (§4.1 item 14). The channel-output thread runs on `GetTimeMS()` and the servo above; PTP in FPP is AES67-only today.
- **FPP's AES67 receive path is not a model for ours.** It runs on GStreamer's default clock behind a jitter buffer (§4.1 item 13). ShowMesh receiving would use the PipeWire direct-timestamp path (§4.5: `module-rtp-source` "assumes that a graph driver is used whose time is somehow synchronized to the sender's", "useful for when receivers shall play in sync with each other", "AES67 sessions use this mode") under the same PTP node-driver candidate A proposes for local playback. The clock subsystem in §5 is unchanged by this idea; it is the prerequisite for it.

### 15.3 Source redundancy, not two authorities (owner target model, ruled 2026-08-28)

The owner adopted the following as the audio node's target model on 2026-08-28. It narrows ADR-017 (§15.1) and the narrowing ADR is owed before a seam builds toward it; the vocabulary below reuses AUDIO-ENGINE §6 (program bus, sources mixed into program) and §8 (the three output adapter classes).

```text
Program source, one bus, priority order
  primary:  FPP AES67 stream for the current show item
  standby:  the local synchronized copy of the same asset

ShowMesh-owned sources, always local, mixed into the program bus
  background and resting music
  announcements
  alerts and failure messages
  test audio

Output backends, fed from the program bus
  local hardware interface        (PCM output)
  FM transmitter feed             (PCM output; a routing of the same bus)
  third-party listener system     (synchronized remote output, RES-016)
  AES67 send, future              (stream output, §11)
```

Rules that follow:

- **Primary source**: the FPP AES67 stream for the current show item, played in direct-timestamp mode against the node's PTP clock, with the per-output static offset (§8) applied.
- **Standby source**: the node's local copy of the same media, started at `expected = media_now - T0` from §6's schedule, where `T0` for an FPP-originated item is derived from the stream (first RTP timestamp of the item plus the measured decoder-to-packet offset) rather than chosen by the coordinator.
- **ShowMesh-owned sources** always come from the local engine and mix over whichever program source is active, so ducking, announcements, alerts, and test audio behave identically whether the program bus is carrying the stream or the local asset. A site-power loss with nodes on UPS is the motivating case: FPP is gone, the node announces the display is down, transitions, and shuts down without the upstream system.

What the failover research must settle before anything is built (do not guess these): stream health definition (packet gap, timestamp discontinuity, SAP withdrawal, grandmaster loss); media identity matching between the SAP session or FPP's playlist status and the local asset (content hash, not filename); the switch itself (gain crossfade at the matched position, and how long silence is tolerated before switching); return-to-preferred behavior (never mid-item, or only at a measured position match); and what the node does when the stream is present but its timestamps and sample count disagree beyond a threshold (that is FPP drifting, and the honest answer may be to prefer local).

### 15.4 Renderer timing from the audio timeline

A renderer holding the sequence locally could map frame `n` to PTP time as `T_media(n) = T0_stream + n / RefreshRate + mediaOffset + decoder_to_packet_offset`, using the AES67 timestamps as the absolute reference instead of MultiSync packet arrival. Whether that is better than what RES-002's MultiSync path gives depends on three measurements none of which exist: the decoder-to-packet offset and its stability across FPP restarts and media items; the servo error between FPP's own frame counter and its media position under load (the source suggests about one frame plus the platform residual); and the difference between the two on the reference hardware. The upside is bounded by FPP's own servo: ShowMesh renderers cannot be more aligned to FPP's pixels than FPP's pixels are to FPP's audio. The candidate benefit is removing MultiSync packet jitter and receipt-time dependence, not removing FPP's servo error.

### 15.5 Questions this adds

- Q6: Does an FPP 10 null-sink or AES67-only output group keep `mediaSeconds` and the MultiSync `secondsElapsed` identical to a real-card configuration? This is the "disabled versus muted" question already open for ADR-017, and it decides whether FPP can send AES67 while its audience output is unused.
- Q7: Decoder-to-packet offset of FPP's AES67 send: value, variance, and stability across restarts.
- Q8: With `driftResample` off, how far do FPP's stream timestamps and sample count diverge over an hour on the reference FPP host, and does turning it on hold?
- Q9: Does the PipeWire direct-timestamp receiver under a PTP node-driver present the stream within the same tolerance as two local players under §7 candidate A?
- Q10: What does the owner want the node to do when FPP is up, the stream is up, and the stream is measurably off its own timestamps: trust FPP or trust local?

## 16. Upstream opportunity: a PTP presentation epoch for MultiSync (not a dependency)

MultiSync distributes current position (`frameNumber`, `secondsElapsed`) and remotes servo toward it; packet arrival time is implicitly part of the timing. If ShowMesh proves absolute PTP-scheduled playback across its own nodes, the same model could apply upstream: the master publishes a sequence's presentation epoch in PTP time, every participant computes frame `n`'s presentation instant locally, and MultiSync packets carry state, identity, and recovery only. FPP today has PTP only inside `AES67Manager` and no PTP in MultiSync (§4.1 items 1 and 14), so this would be a new upstream feature. Recorded here so it is not lost; nothing in ShowMesh may depend on it.

## 17. The third-party synchronized listener output

[RES-016](RES-016-third-party-synchronized-audio-output.md) covers the third-party listener system. Its current understanding is that media is uploaded to the vendor's service and transcoded before synchronized playback, and its internal timing is a black box. Consequences for this record: an FPP AES67 stream cannot be assumed forwardable to it, so ShowMesh may need to start that output from an uploaded asset even while its wired outputs receive AES67; its acoustic presentation cannot be assumed to share the PTP media clock; the adapter should carry an operator-configurable presentation offset in the same `outputLatency` shape as §8 with `method: declared` and expose whatever health or timing the service reports. Its limitations must not weaken the model for local and AES67 outputs, which is why it is a §8 offset and not a change to §5 to §7.

## 18. Sources

FPP, `https://github.com/FalconChristmas/fpp`, master at `d318b1d`: `src/mediaoutput/AES67Manager.cpp`, `src/mediaoutput/AES67Manager.h`, `src/httpAPI.cpp`, `src/fppd.cpp`, `src/boot/FPPINIT_Audio.cpp`, `src/MultiSync.cpp`, `src/mediaoutput/GStreamerOut.cpp`, `src/channeloutput/channeloutputthread.cpp`, `src/playlist/PlaylistEntryBoth.cpp`, `www/api/controllers/pipewire.php`, `www/settings.json`, `www/aes67-config.php`, `docs/FPP_Audio_Architecture.md`; issue #2848 and PR #2849. Commits (`https://github.com/FalconChristmas/fpp/commit/<hash>`): `d318b1d` 2026-08-28 re-anchor drift control on backward step; `6516268`, `2e97737` 2026-08-28 send-rate collapse and buffer pool; `d081180` 2026-08-27 libsamplerate drift correction; `f120313`, `5dec0c4`, `d2d3c82`, `528b83d`, `125e1cb` 2026-08-27 drift actuator experiments; `0c8eaad` 2026-08-27 phc2sys removed; `cbd1151` 2026-08-26; `36d36f1`, `989edbd` 2026-08-25 PTP media clock; `585407e`, `a641f71`, `cc5e188` 2026-08-24 roles and status; `6e42854` 2026-08-24 pmc grandmaster query and DSCP (#2849). The February commit that set `udpsink sync=false` predates the clone window and is unverified.

linuxptp: `ptp4l.8`, `phc2sys.8`, `pmc.8` (linuxptp.nwtime.org and `richardcochran/linuxptp` master), `clock.c`, `phc.c`, `uds.c`, `udp.c`, `pmc.c`, `missing.h`. Kernel: `Documentation/driver-api/ptp.rst`, `kernel/time/posix-clock.c`, `tools/testing/selftests/ptp/testptp.c`, `drivers/net/ethernet/cadence/macb_ptp.c`, `drivers/net/ethernet/broadcom/genet/bcmgenet.c`, `drivers/net/phy/bcm-phy-ptp.c`, `drivers/net/usb/lan78xx.c`; raspberrypi/linux `rp1.dtsi`, issues #4151 and #5904.

GStreamer `main`: `gstpipeline`, `gstaudiobasesink.c/.h`, `gstptpclock.c`, `libs/gst/helpers/ptp/{main.rs,net.rs,clock.rs}`, `gstsystemclock.c`, `gstnetclientclock.c`, `gstnettimeprovider`, `gstaudioclock`, `gstaudioresampler` documentation. go-gst: repository README and package listing only; API not read.

PipeWire `master`: `spa/plugins/support/node-driver.c`, `spa/plugins/alsa/alsa-pcm.c`, `spa/include/spa/node/io.h`, `src/daemon/pipewire.conf.in`, `src/modules/module-rtp-sink.c`, `module-rtp-sap.c`, `module-rtp/stream.c`, `NEWS`; `pipewire-props(7)`, `pipewire.conf(5)`, `pw-metadata(1)`, module-rtp-sink/source/sap pages.

Standards and secondary: RFC 7273, RFC 8173, Meinberg "PTP timescale and ARB time", Audinate Dante Controller clock configuration pages, libsamplerate API, zita-resampler and zita-ajbridge documentation, soxr `soxr.h` and example 5.

# Track I: Clock, scheduled start, and the FPP program source

[Build plan](BUILD-PLAN.md) · [RES-019](../research/RES-019-ptp-synchronized-multi-node-audio.md) · [ADR-046](../decisions/ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md) · [ADR-047](../decisions/ADR-047-fpp-aes67-is-the-primary-program-source.md) · [ADR-045](../decisions/ADR-045-multi-node-audio-and-roles.md) · [Track C](TRACK-C-audio-node.md) · [Audio engine spec](../architecture/AUDIO-ENGINE.md)

Status: **PLANNED.** Opened 2026-08-28 from RES-019 and the two owner rulings of the same day. Nothing in this track is built. The research is L1 for upstream behavior and L0 for every ShowMesh measurement.

## What this track delivers

Audio nodes share one PTP-domain media clock, start media at a scheduled instant on that clock instead of on command arrival, take FPP 10's AES67 stream as the primary program source with the local synchronized asset as the standby, rate-lock each audio interface to the shared clock at ppm scale, and carry a calibrated static offset per output. AES67 send later reuses the same clock and timeline.

The reusable asset is the media clock and timeline, not any one playback path.

## Integration branch

Built on `dev/clock-sync`, the way multi-node audio is built on `dev/multi-audio` and FPP Connect was built on `dev/fpp-connect`: seams merge to the integration branch with the usual gates and `main` stays the stable representation. The branch lands on `main` through a fold pull request after the owner's hardware test of the seams it carries; nothing here is required for the show to run. `main` keeps moving during a hardware session and is merged into the integration branch, never the other way round by rebase.

## The ordering rule that will bite if ignored

ADR-047 decision 6, restated as a build rule: **I1 (clock provider) and I2 (scheduled start) merge before I3 (AES67 receive) starts, and I3 merges before I4 (rate lock) is required.** The standby source is only synchronized if the node already holds the media clock, the schedule, and the measured stream offset. A build that takes the stream first produces a node that goes silent or jumps on failover. An orchestrator that finds I1 or I2 unmerged when I3 is proposed stops and says so on the track's working issue; it does not reorder.

The second dependency is external to this track: I2 needs cue outputs to resolve to a target node (Track C's multi-node seam on `dev/multi-audio`), otherwise there is no second node to schedule. `dev/clock-sync` is cut from `main`; I1 does not need multi-audio, and I2 waits for `dev/multi-audio` to land on `main`, then `main` is merged into `dev/clock-sync`. A pushed integration branch is never rebased onto `main` ([HARDWARE-TEST-SESSION.md](../bench/HARDWARE-TEST-SESSION.md)); each lane merges `main` in before it starts and the fold to `main` is an ordinary pull request.

## Seams

| Seam | Outcome | Where it builds | Evidence level it can reach | Depends on |
|---|---|---|---|---|
| **I0. Measure** | Two-channel recording of the M4 node and the Pi node through one 30 to 60 minute cue; drift curve in ms and ppm; `ethtool -T` on every node; result recorded in RES-019 stage 0 | owner hardware, laptop dev stack | L2 | both nodes following one show (Track C multi-node bench) |
| **I1. Clock provider** | Provider interface; ShowMesh-managed linuxptp provider (config, supervision, role policy, ownership pre-check); external linuxptp provider; FPP-observed provider reading `/api/aes67/status` and the `ptp4l` UDS socket; PHC read through `clock_gettime` on `/dev/ptpN` with `CLOCK_REALTIME` fallback in software mode; `node.clock.ptp.*` telemetry; no playback change | VM, container bench with two agents and `ptp4l` in software mode | L2 | none |
| **I2. Scheduled start** | `session.prepare` reports ready plus preroll latency; `session.start` gains a media-clock start instant; the coordinator samples the media clock from the `program+ltc` node; `node.audio.timeline.*` telemetry; seek-only resync on discontinuity using `audio.settings.driftIgnoreThresholdMs` as the threshold; a node without a locked provider keeps today's start-on-arrival; API, `showmeshctl`, generated client, and UI at parity | VM, container bench: two agents, one start instant, start skew measured from sink clocks | L2, then L3 on the real nodes | I1; cue outputs resolved to a target node on `main` |
| **I3. AES67 receive as primary program source** | Node receives FPP's AES67 stream through PipeWire `module-rtp-source` in direct-timestamp mode under the PTP node-driver; program-source state (`primary`, `standby`, `none`) with reason; `audio.node` policy for stream preference; media identity by content hash from the stream's session and FPP's playlist status; failover and return-to-primary per the policy RES-019 §15.3 settles first; ShowMesh-owned sources mix over either | VM for the receive path against a containerized FPP 10 sender; laptop with real nodes for the switch | L2, then L3 | I1, I2, and the FPP "AES67-only output group keeps `secondsElapsed`" capture |
| **I4. Rate lock** | Candidate A: PipeWire `support.node.driver` clocked from `/dev/ptpN` with the ALSA sink as follower and `pipewiresink` as the engine's `SinkFactory`; clamp, slew, freeze-on-step, `node.audio.rate.*` telemetry; per-node enable. Candidate B (own `GstAudioResampler` loop, PHC-backed pipeline clock through a cgo shim) only if A fails to lock, is audible, or cannot coexist with FPP's graph | container for lock behavior; real interfaces for audibility, LTC, and long-run hold | L2, then L3 | I0 baseline, I2; ADR-046 permits it |
| **I5. Latency calibration** | `audio.node.outputLatency` with value, method, confidence, measured-at; measurement procedure; applied to the start instant | loopback or acoustic rig, owner | L2 | I2; a measurement rig |
| **I6. AES67 send** | `module-rtp-sink` or GStreamer L24 on the same clock; `ts-refclk` from the provider | protocol analyser plus two vendor receivers | L2 | I1, I4 |

## Bound by

ADR-007 (GStreamer owns samples; ShowMesh owns supervision and sync policy), ADR-017 as narrowed by ADR-047, ADR-018 (program and LTC on one interface; the trim applies to the whole interface), ADR-019 (device loss fails silent), ADR-042 (cgo only in the native agent, and only for the two authorized libraries plus whatever a superseding record adds), ADR-045, ADR-046, ADR-047, AUDIO-ENGINE §4 to §8 as amended 2026-08-28, and the command envelope in ARCHITECTURE §8.1.

Two hard rules from RES-019 §5.3 and §12 apply to every seam: exactly one component owns `ptp4l` on an interface, and ShowMesh never starts one where FPP's AES67 subsystem or a systemd unit already does; and `phc2sys` is never run on ShowMesh's authority, because the PTP domain's epoch is not wall time.

## Acceptance criteria for the assembled path

1. Two real nodes report `locked` against one grandmaster with the same domain and identity, and the coordinator shows it.
2. Two real nodes given one start instant begin the same asset within the tolerance I0 establishes as audible, measured from a two-channel recording, not from telemetry.
3. A node whose provider is not locked starts on arrival, says so, and realigns at the next cue boundary exactly as today.
4. A PTP clock step during playback produces at most one reported resync and no rate-loop excursion.
5. With I3 active, pulling the FPP host's network cable during a cue switches the node to standby within the policy's window, at the matched position, with the switch reported; restoring it does not switch back mid-item.
6. With I4 active, two real nodes hold each other within I0's tolerance over an hour without a seek, and the trim is visible in telemetry and inaudible to the owner.
7. LTC read by Resolume stays locked through I4's trim.
8. Existing single-node shows work unchanged at every seam.

## Out of scope

The renderer following the audio timeline (RES-019 §15.4, a later track once I3 exists), a PTP presentation epoch for MultiSync (RES-019 §16, upstream), the third-party listener system beyond its declared offset (RES-016), per-node `audio.settings`, and any change to the show timeline path in `pkg/multisync`.

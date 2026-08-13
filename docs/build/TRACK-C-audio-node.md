# Track C: Audio node

[Build plan](BUILD-PLAN.md) · [Audio engine spec](../architecture/AUDIO-ENGINE.md) · [RES-007](../research/RES-007-audio-node-architecture.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md)

Status: not started. Specified 2026-08-13. Interface purchased the same day.

## Goal

ShowMesh owns audience-facing audio: a node plays show audio and background music on its own clock, generates LTC on a discrete output in the same clock domain, and fails silent when the device goes away.

## Start here, before any engine work

**Confirm output 3 on the Behringer U-Phoria UMC204HD is independently addressable and not a mirror of outputs 1 and 2.**

On consumer interfaces a second output pair is sometimes a mirror of the main pair rather than a separately addressable one. If that is true here, LTC sums into program audio. That is the exact failure [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) exists to prevent, it is close to inaudible on a casual listen, and it corrupts timecode. It would be found during a show.

The test is a signal generator, a cable, and twenty minutes: send tone to output 3 alone under ALSA and confirm nothing appears on 1 and 2. **If it fails, the answer is a different interface, not a software workaround**, and finding that out in August is cheap while finding it out in October is not. Recorded in RES-007.

The unit is 2-in / 4-out, so on channel count it clears ADR-018's three-output minimum, and because program and LTC leave the same USB device they share one clock domain. ADR-018's specific prohibition, program on USB with LTC on Dante, is not triggered.

## What is decided, and what is entirely unproven

The architecture is settled across three ADRs and `AUDIO-ENGINE.md`. **All of it is design intent at L0**, and RES-007 is critical-risk with no evidence collected. This track is where that changes.

The three constraints that shape everything and must not be "improved" during implementation:

- **Nodes play complete local files against their own audio clock**, never a PCM or sample-position stream (ADR-017). Drift is measured and corrected **discretely at track boundaries**, never by continuous rate manipulation. Audio deliberately does not follow the MultiSync slew and jump model that Track B's renderer uses. Two subsystems in this project synchronise differently on purpose, and that divergence is the decision, not a bug.
- **Program and LTC share one clock domain** (ADR-018), LTC on a discrete output, never mixed into program.
- **Device loss fails silent** (ADR-019): stop the failed output, keep session state, mark critical, alert, continue other outputs. **Never auto-fall back to FPP audio.** Uncontrolled routing and gain into an FM transmitter is worse than silence. This is a deliberate, recorded exception to ADR-004's fallback rule.

FPP's own audio output goes unused. That is ADR-017 and it is why `Volume Set` was rejected as Step 7's first command: a control for FPP audio would be built to be deleted.

## Deliverables

**C0. The output-addressing check** above, and the RES-007 prototype it gates. The prototype is the minimum engine on the intended host and interface, recording program audio, LTC, and a visual reference **together**, so alignment is measurable after the fact rather than judged live by ear.

**C1. Audio pipelines under the same agent Track B builds.** Mixing, fades, ducking, interleave, and LTC generation are GStreamer, never a Go sample path (ADR-007). The agent supervises; it does not process audio.

**C2. Playback sessions.** Session state, transport, and the drift policy from `AUDIO-ENGINE.md`, with correction only at track boundaries.

**C3. LTC generation** on the discrete output, in the program clock domain.

**C4. Device-loss behaviour** per ADR-019, including how fast loss is detected and how long the unreported silent window is, which RES-007 lists as an open question and which is operator-visible.

**C5. Capability advertisement** for audio output and LTC, versioned per ADR-002, with the channel count and clock domain as attributes so [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md)'s placement constraint is checkable by the coordinator rather than by a human remembering it.

## Decisions this track must make

- **PipeWire or raw ALSA.** RES-007 lists this as an open question affecting alignment, and it is the kind of choice that is cheap now and expensive to reverse once the engine is built on it.
- **What the show audio actually is, and where it comes from.** ADR-017 says nodes play complete local files. Which files, delivered how, and validated against what, is unanswered. It is the same delivery and freshness problem Track B has for FSEQ, and the two should be solved once rather than twice.
- **What background and resting audio does between shows**, since ARCHITECTURE Phase 1 names deterministic fades into and out of shows as day-0-adjacent scope.

## Acceptance criteria

- Output 3 carries LTC and outputs 1 and 2 carry program, proven by measurement rather than by the interface's marketing.
- Program-to-LTC alignment is measured over a full-show duration and recorded in RES-007 with the host, interface, sample rate, and audio stack named.
- Drift against the FPP show timeline over a 30 to 60 minute show is measured, which is one of RES-007's two headline numbers and cannot be produced by a unit test.
- Pulling the interface mid-playback stops that output, keeps session state, marks critical, alerts, and **does not** route anything to FPP audio.
- A discrete boundary correction is audibly acceptable, judged on the real system.

**Bound by:** ADR-007, ADR-011, ADR-017, ADR-018, ADR-019, and `AUDIO-ENGINE.md`.

**Out of scope:** Dante, multi-node audio failover (deferred by ADR-019 until the roadmap's failover item lands, and operator-initiated before that), and any use of FPP's audio output.

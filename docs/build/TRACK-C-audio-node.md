# Track C: Audio node

[Build plan](BUILD-PLAN.md) · [Audio engine spec](../architecture/AUDIO-ENGINE.md) · [RES-007](../research/RES-007-audio-node-architecture.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md)

Status: not started. Specified 2026-08-13. **Interface selection reopened 2026-08-14** and deliberately removed from this track's critical path, per the section below.

## Goal

ShowMesh owns audience-facing audio: a node plays show audio and background music on its own clock, generates LTC on a discrete output in the same clock domain, and fails silent when the device goes away.

**This track is also Resolume's timecode source, which makes it more critical than "audio" suggests.** Resolume Arena accepts SMPTE only as audio LTC, so [Track D](TRACK-D-resolume.md) follows the show over a physical cable from a discrete output on this node into the Resolume machine.

## No specific interface gates this track

**Owner's decision, 2026-08-14, replacing the interface check that opened this track.** The U-Phoria UMC204HD ordered on 2026-08-13 turned out to be backordered, and the interface will be reselected against wider requirements than this project's. That is the occasion for the change rather than its reason. The reason is that gating a track on one consumer unit behaving as advertised was the wrong shape to begin with: **[ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) names a property, not a product.**

**Nothing in ADR-018 changes.** Program and LTC still leave one interface in one clock domain, LTC still lands on a discrete output that program never touches, and program on USB with LTC on Dante is still forbidden. What changes is where that property is checked.

**The engine is built against what the audio stack reports**, meaning channel count and output addressing are read from ALSA at run time and the pipeline is built for N outputs. No device model appears anywhere in the code, and no interface-specific workaround is written. Any multichannel interface with working Linux support is a candidate, which is also the posture a released product needs.

**The placement check moves into capability advertisement**, which is what C5 was always for and is now load-bearing rather than late. A node advertises its output channel count and clock domain as attributes, and a device that cannot supply a discrete output outside the program pair fails ADR-018 placement at configuration time, whatever the device is. That is the coordinator checking the constraint instead of a human remembering it.

**The twenty-minute tone test survives as commissioning, not as a gate.** On consumer interfaces a second output pair is sometimes a hardware mirror of the main pair rather than a separately addressable one, and that is only partly visible to software: a driver can report four outputs on a unit that routes 3/4 from 1/2 downstream of anything ALSA can see. If it is true of the interface in use, LTC sums into program audio, which is close to inaudible on a casual listen, corrupts timecode, and would be found during a show. So the test still exists, it runs against whichever interface is present, and **it blocks that interface rather than this track.** Send tone to the intended LTC output alone and confirm nothing appears on the program pair. Record the result and the device in RES-007.

**Track D is no longer waiting on this track's build**, which is the practical consequence. D0 needs LTC, not ShowMesh's LTC, so any generator on any working interface answers RES-001's question about what Resolume does when timecode disappears.

## What is decided, and what is entirely unproven

The architecture is settled across three ADRs and `AUDIO-ENGINE.md`. **All of it is design intent at L0**, and RES-007 is critical-risk with no evidence collected. This track is where that changes.

The three constraints that shape everything and must not be "improved" during implementation:

- **Nodes play complete local files against their own audio clock**, never a PCM or sample-position stream (ADR-017). Drift is measured and corrected **discretely at track boundaries**, never by continuous rate manipulation. Audio deliberately does not follow the MultiSync slew and jump model that Track B's renderer uses. Two subsystems in this project synchronise differently on purpose, and that divergence is the decision, not a bug.
- **Program and LTC share one clock domain** (ADR-018), LTC on a discrete output, never mixed into program.
- **Device loss fails silent** (ADR-019): stop the failed output, keep session state, mark critical, alert, continue other outputs. **Never auto-fall back to FPP audio.** Uncontrolled routing and gain into an FM transmitter is worse than silence. This is a deliberate, recorded exception to ADR-004's fallback rule.

FPP's own audio output goes unused. That is ADR-017 and it is why `Volume Set` was rejected as Step 7's first command: a control for FPP audio would be built to be deleted.

## Deliverables

**C0. The RES-007 prototype**, the minimum engine on the intended host, recording program audio, LTC, and a visual reference **together**, so alignment is measurable after the fact rather than judged live by ear. It runs against whichever multichannel interface is present, and the commissioning tone test above runs against that interface first. **This no longer waits for a specific unit to arrive**, which is the 2026-08-14 change.

**C1. Audio pipelines under the same agent Track B builds.** Mixing, fades, ducking, interleave, and LTC generation are GStreamer, never a Go sample path (ADR-007). The agent supervises; it does not process audio.

**C2. Playback sessions.** Session state, transport, and the drift policy from `AUDIO-ENGINE.md`, with correction only at track boundaries.

**C3. LTC generation** on the discrete output, in the program clock domain.

**C4. Device-loss behaviour** per ADR-019, including how fast loss is detected and how long the unreported silent window is, which RES-007 lists as an open question and which is operator-visible.

**C5. Capability advertisement** for audio output and LTC, versioned per ADR-002, with the channel count and clock domain as attributes so [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md)'s placement constraint is checkable by the coordinator rather than by a human remembering it. **This is now how the interface requirement is enforced at all**, per the 2026-08-14 decision above, so it is not the last thing built despite being the last thing listed. The attributes are read from the audio stack rather than configured, because a hand-entered channel count is a claim and the point of this deliverable is evidence.

## Decisions this track must make

- **PipeWire or raw ALSA.** RES-007 lists this as an open question affecting alignment, and it is the kind of choice that is cheap now and expensive to reverse once the engine is built on it.
- ~~What the show audio actually is, and where it comes from.~~ **Answered 2026-08-13: it belongs to [Track E](TRACK-E-show-authoring-and-assets.md)**, which owns one asset store and one sync service for every node type, so the delivery and freshness problem is solved once rather than twice as this bullet asked. [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md) decision 5 also settles the ADR-017 question directly: **playback is always from node-local storage**, and no playback path may reach the store.
- **What background and resting audio does between shows**, since ARCHITECTURE Phase 1 names deterministic fades into and out of shows as day-0-adjacent scope.

## Acceptance criteria

- **The LTC output carries LTC and the program pair carries program, on the interface actually in use**, proven by measurement rather than by that interface's marketing. Which output number that is depends on the device and is not fixed by this document.
- **A node whose interface cannot supply a discrete output outside the program pair is refused ADR-018 placement by the coordinator**, from advertised attributes, rather than being discovered by a listener during a show.
- Program-to-LTC alignment is measured over a full-show duration and recorded in RES-007 with the host, interface, sample rate, and audio stack named.
- Drift against the FPP show timeline over a 30 to 60 minute show is measured, which is one of RES-007's two headline numbers and cannot be produced by a unit test.
- Pulling the interface mid-playback stops that output, keeps session state, marks critical, alerts, and **does not** route anything to FPP audio.
- A discrete boundary correction is audibly acceptable, judged on the real system.

**Bound by:** ADR-007, ADR-011, ADR-017, ADR-018, ADR-019, and `AUDIO-ENGINE.md`.

**Out of scope:** Dante, multi-node audio failover (deferred by ADR-019 until the roadmap's failover item lands, and operator-initiated before that), and any use of FPP's audio output.

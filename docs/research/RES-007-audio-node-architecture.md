# RES-007: Audio-Node Architecture

[Architecture](../architecture/ARCHITECTURE.md#45-audio-engine) · [Audio Engine specification](../architecture/AUDIO-ENGINE.md) · [Tracker](README.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md) · [Resolume SMPTE research](RES-001-resolume-smpte-behavior.md)

Status: planned (bench) · Risk: critical · Verification: L0 — assumption

## Decision to make

Establish whether the audio architecture decided on 2026-08-10 is achievable on real hardware, and produce the numbers it depends on but does not contain.

Three ADRs settled the questions this record originally asked about clock ownership and playback model: ShowMesh owns audience-facing audio and nodes play local media against their own clock (ADR-017), program and LTC share one clock domain (ADR-018), and audio device loss fails silent with no automatic FPP fallback (ADR-019). Those decisions are architectural intent at L0. **Nothing below has been prototyped**, and this record is now the work queue that either confirms them or forces a superseding ADR.

## Required use cases

- Show audio synchronized to the FPP timeline.
- Background and ambient music outside scheduled shows.
- Deterministic crossfades into pre-show, live, intermission, and post-show states.
- Announcements ducked over show or background audio.
- Independent LTC output without consuming the stereo program pair, from the same clock as program.
- Multichannel USB interfaces, with optional Dante as an additional output.
- Metering, device health, underrun reporting, and local recovery.

## Questions

**Rendering (blocks ADR-007 applied to audio; AUDIO-ENGINE §7)**

- Can agent-supervised GStreamer pipelines deliver click-free ducking, reliable gapless playback, and multichannel interleave on the target Linux host?
- Can LTC be generated within the same pipeline and remain sample-aligned to program audio, or does it require an element or approach that does not exist? What is the actual alignment achieved, in samples?
- Does the pipeline hold alignment across track changes, seeks, gain transitions, and process restarts?

**Drift (blocks ADR-017's correction policy)**

- What drift does a free-running audio node accumulate against the FPP show timeline over a 30–60 minute show, on the intended hardware?
- What audio-to-lighting offset is perceptible to an audience, and therefore what threshold should the ignore band use?
- Is correction at track boundaries sufficient in practice, or does a show reach the significant-drift threshold mid-track?
- Is a discrete seek correction audibly acceptable when it does occur?

**Clock domains (blocks ADR-018)**

- What program-to-LTC alignment is achieved on the selected interface, and how does it behave over a full show?
- How many usable output channels from one clock does the candidate interface actually provide, and does the driver expose them as one device?
- What happens to alignment on device hot-plug, sample-rate change, and PipeWire versus raw ALSA paths?

**Failure behavior (blocks ADR-019)**

- How quickly is device loss detected, and how long is the unreported silent window?
- Does session and show state survive device loss, device return, and engine process restart?
- Does anything reach the FM transmitter from a path ShowMesh did not intend during any failure case?
- What must be verified on a standby node, and can that verification be maintained continuously rather than performed at failover time?

**Interaction with the FPP show timeline**

- [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) requires FPP's own audio output to be disabled or unused on the primary controller. "Disabled or unused" is ambiguous between muting the output device and not loading the media at all, and the difference may not be inert: FPP derives the `SecondsElapsed` field that MultiSync carries from its own media playback, and that field is the show timeline ShowMesh aligns audio *to*. Which interpretation preserves the timeline unchanged, and whether either alters it, is unverified and must be settled by capture rather than reasoning. Relates to [RES-002](RES-002-fpp-multisync-compatibility.md).

**Media and platform**

- Media distribution, hashing, and per-node availability verification: what is sufficient before a node may hold audio authority?
- Can useful ideas be reused from BackgroundMusicFPP without making it a hard dependency?
- What breaks first if the audio node is a Raspberry Pi-class device rather than the Dell Micro 7040?

## Acceptance criteria

- Transitions are click-free, repeatable, and measurable.
- LTC remains valid and aligned with program audio, with the achieved alignment recorded as a number rather than as an impression.
- Drift over a full-length show is measured and a threshold is set from that measurement.
- Device loss is detected within a recorded interval, session state survives it, and no unintended audio path activates.
- Coordinator loss does not interrupt current playback.
- Show start, correction, and recovery behavior are defined and reproducible.
- An overnight background-and-show cycle soak shows no leaks and no accumulating offset.

## Test method

Prototype the minimum engine on the intended Linux host and interface. Record program audio, LTC, and a visual reference together so alignment is measurable after the fact rather than judged live.

Exercise every lifecycle transition plus coordinator loss, FPP timing loss, device unplug and replug, sample-rate change, process restart, missing media, media hash mismatch, Dante interruption where applicable, standby-node failover, and power restoration.

Measure rather than observe: alignment in samples, drift in milliseconds against elapsed show time, detection latency in seconds, underrun counts.

## Evidence and findings

No evidence collected. This section is a work queue, not a conclusion.

### Interface selection is reopened, and is no longer a prerequisite (2026-08-14)

**Superseding the 2026-08-13 note that recorded the Behringer U-Phoria UMC204HD as purchased.** That order turned out to be a backorder, the unit is out of stock widely, and the owner will reselect against requirements beyond this project. Preserved below because the reasoning it contains still applies to whatever is chosen.

**The occasion was the backorder; the decision is structural.** [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) names a property, not a product, and gating the audio track on one consumer unit behaving as advertised was the wrong shape regardless of stock levels. **ADR-018 is unchanged and needs no supersession**: program and LTC still leave one interface in one clock domain, LTC still lands on a discrete output that program never touches, and program on USB with LTC on Dante is still forbidden.

**What changes is where the property is checked.** The engine is built against the channel count and output addressing the audio stack reports at run time, so any multichannel interface with working Linux support is a candidate and no device model appears in the code. The ADR-018 placement constraint is then enforced from advertised capability attributes, meaning a node whose interface cannot supply a discrete output outside the program pair is refused placement by the coordinator. Recorded in [Track C](../build/TRACK-C-audio-node.md), which owns the delivery.

**The mirror problem is real and stays on the test list, as a per-device commissioning check.** This record's own clock-domain question asks how many usable output channels from one clock an interface provides *and whether the driver exposes them as one device*. On consumer interfaces a second output pair is sometimes a **hardware mirror** of the main pair rather than an independently addressable one, and that is only partly visible to software: a driver can report four outputs on a unit that routes the second pair from the first downstream of anything ALSA can observe. Where it is true, LTC sums into program audio, which is close to inaudible on a casual listen, corrupts timecode, and would be discovered during a show.

So for **whichever interface is in use**: confirm under Linux that a signal sent to the intended LTC output alone appears there and **not** on the program pair. That is a signal generator, a cable, and twenty minutes. A failure blocks that interface rather than the track, and the answer is a different interface rather than a software workaround. Record the result **with the device named**, since the result does not generalise to another unit.

**Unverified and deliberately not asserted for any candidate:** Linux class-compliance behaviour, channel map under ALSA versus PipeWire, achievable sample rates, and whether a mode switch affects output addressing. These are test parameters, recorded as questions rather than guessed at, per this project's rule that an external system's behaviour is named only from that system's own output.

**One candidate already on hand, recorded rather than recommended.** The owner has a Behringer X Air mixer, which is a multichannel USB device and is sufficient to generate LTC for [RES-001](RES-001-resolume-smpte-behavior.md)'s bench. Nothing about its Linux behaviour, channel map, or suitability as the show's audio interface has been checked, and this note is not a selection.

## Decision, fallback, and revalidation

The architecture is decided (ADR-017, ADR-018, ADR-019) and unverified. If bench work shows the node path cannot meet the acceptance criteria — most plausibly if GStreamer cannot hold LTC alignment to program — the correct response is a superseding ADR, not moving sample generation into ShowMesh code, which [ADR-006](../decisions/ADR-006-go-implementation-language.md) and [ADR-007](../decisions/ADR-007-gstreamer-media-engine.md) forbid.

FPP-hosted stereo playback is no longer the automatic fallback; ADR-019 replaced it with fail-silent plus a documented operator procedure. FPP-hosted audio remains available as a deliberate operator choice and as the position to return to if ADR-017 is superseded.

Revalidate after audio engine, kernel, driver, interface, PipeWire, or timing-source changes.

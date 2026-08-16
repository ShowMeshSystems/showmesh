# Audio Engine Specification

[Documentation index](../README.md) · [Architecture specification](ARCHITECTURE.md) · [Observability specification](OBSERVABILITY.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md)

Status: Draft architecture baseline — design intent, not implemented and not verified  
Audience: Maintainers, audio-node contributors, show operators

Nothing in this document has been prototyped. The central viability questions — whether GStreamer delivers click-free ducking and sample-aligned LTC from one clock, and what drift a free-running audio node actually accumulates over a show — are open bench items in [RES-007](../research/RES-007-audio-node-architecture.md), which is critical-risk and currently at L0.

## 1. Purpose and scope

The Audio Engine is the playback and routing system for all audience-facing audio: show music, background and intermission music, announcements, and the LTC that drives Resolume.

It owns how audio is rendered and distributed. It does not own what the show is doing — FPP remains the scheduler and sequence authority ([ADR-001](../decisions/ADR-001-fpp-is-authoritative.md)). The Audio Engine acts on show state rather than producing it.

This document owns the audio subsystem's architecture. [RESTING-MODE](RESTING-MODE.md) owns when background and announcement sessions are created and faded around the operating-night lifecycle. Health presentation and alerting for audio resources belong to [OBSERVABILITY](OBSERVABILITY.md); how any of it is displayed belongs to [OPERATOR-UI](OPERATOR-UI.md).

## 2. Authority model

Authority is deliberately split three ways, and the split is the point of the design.

**FPP is authoritative for** scheduler state, playlist and show selection, sequence execution, and the sequence timeline.

**ShowMesh is authoritative for** audience-facing audio sessions, routing, output assignment, background audio, announcements, mixing and ducking, audio-node placement, and audio health. This is a change from FPP's default arrangement, where the primary FPP controller plays program audio itself; see [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md).

**The active audio node is authoritative for** its local media playback clock, PCM rendering, and local device state.

Show synchronization and audio clock synchronization are therefore separate problems, solved separately. That separation is what makes it possible for the coordinator to stay out of the timing path ([ADR-008](../decisions/ADR-008-mqtt-control-plane.md)) while audio still starts in the right place.

## 3. Playback model

Audio is represented as logical **playback sessions**. A session carries media identity, source type, start time, current position, playback state, priority, gain, loop behavior, and output targets.

Initial source types are `show`, `background`, `announcement`, and `manual`. Higher-priority sessions may duck, pause, replace, or mix with lower-priority ones according to policy. Indicative default priorities are background 10, show 50, announcement 100; these are starting values for tuning, not normative constants.

## 4. Timing and synchronization

### 4.1 Nodes play local media, not a sample stream

Each eligible audio node holds local copies of show media and plays them locally. ShowMesh supplies authoritative playback state — media identity, start and stop, desired position, show start time, and current authoritative show position — and the node starts or seeks its matching local file and then plays it on its own stable audio clock.

The sequence is:

1. FPP starts the sequence or show.
2. ShowMesh receives current media and timeline state.
3. ShowMesh creates or updates the authoritative audio session.
4. The audio node starts the matching local asset at the requested position.
5. The node continues playback against its own audio clock.
6. ShowMesh compares node position against show position as telemetry.

Continuous PCM streaming would put the network and the coordinator inside the real-time audio path, which the standing constraints forbid, and would import MultiSync-rate correction artifacts into program audio. Real-time streaming may exist later as a separate capability; it is not the synchronized show-audio architecture. See [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md).

### 4.2 Drift policy

The Audio Engine must not continuously discipline its audio clock against show timing. Policy:

- align accurately at playback start;
- measure drift continuously and report it;
- ignore drift below a configurable threshold;
- prefer correction at track boundaries;
- allow a discrete seek when drift becomes operationally significant;
- avoid audible playback-rate manipulation;
- never chase small timing differences continuously.

This is a deliberate divergence from the FPP remote sync semantics that `pkg/multisync` implements for the lighting timeline. Those semantics slew by up to four frames and jump beyond half a second, which is correct for pixels and wrong for program audio, where rate manipulation is audible and a seek is a defect the audience hears. The two models are not required to match, and the divergence must not be "fixed" by making audio behave like a MultiSync remote.

ARCHITECTURE §5.1 requires nodes to derive presentation position from an authoritative show timeline rather than free-running indefinitely from receipt time. An audio node satisfies that by aligning to the show timeline at start and at every correction point, and by remaining continuously measured against it. It is not free-running: it is loosely coupled, with the coupling interval set by policy rather than by the frame rate.

The tolerable drift threshold is unknown. It depends on what the audience can perceive between audio and lighting, which is a bench and field question in [RES-007](../research/RES-007-audio-node-architecture.md), not something to assert here.

## 5. Clock domains

Three clocks are explicitly distinguished and must not be assumed identical:

1. the show timeline clock;
2. the audio playback and device clock;
3. the network audio clock, where one exists.

Program audio and LTC must share a clock domain wherever their timing relationship matters, which for a show driving Resolume from LTC is always ([ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md)). The preferred deployment puts program and LTC on one hardware interface and therefore one hardware clock.

The arrangement to avoid is program audio on a USB interface and LTC on a Dante interface, or any other pairing that places two signals whose phase relationship matters on independent hardware clocks. If both eventually run over Dante they belong in the same Dante clock domain.

## 6. Buses and routing

Logical buses are defined independently of physical outputs, and channel counts are never hard-coded.

**Program bus** — the stereo audience-facing mix, containing show music, background audio, announcements, and any other audience-facing source.

**LTC bus** — mono timecode. Never mixed into program audio, on its own discrete physical output.

The minimum physical output requirement for an initial deployment is therefore channels 1–2 for stereo program and channel 3 for LTC, with a fourth channel reserved where practical for future routing or monitoring.

Future optional buses the architecture must permit without redesign: independent local speaker zones, an independent FM mix, monitor and cue outputs, alternate language or program feeds, additional timecode, and Dante destinations.

One bus may feed several outputs simultaneously rather than requiring duplicate mixes. An FM transmitter needs no separate logical mix unless it later needs different processing or content, and announcements need no dedicated channels — they are sources mixed into program.

## 7. Rendering

Mixing, gain, fades, ducking, gapless playback, multichannel interleave, and LTC generation are performed by agent-supervised GStreamer pipelines, per [ADR-007](../decisions/ADR-007-gstreamer-media-engine.md). ShowMesh code owns session state, sync policy, routing decisions, supervision, and health — never sample generation. This is the same boundary the renderer observes, applied to audio.

Whether GStreamer delivers click-free ducking, reliable gapless playback, and LTC sample-aligned to program on a shared clock is unverified and is the first bench item in [RES-007](../research/RES-007-audio-node-architecture.md). A negative result is an architectural finding requiring a superseding ADR, not something to work around by moving sample generation into Go.

## 8. Output adapters

Outputs are adapters over logical session state. Each adapter exposes approximately: initialize, start, stop, seek where applicable, set gain, mute and unmute, health, current position where applicable, latency, and capability metadata. **An adapter must declare what it does not support rather than pretending to support it.**

Three adapter classes behave differently and must not be flattened into one:

**PCM outputs** receive rendered audio — USB interfaces, ALSA and PipeWire devices, analog speakers, FM transmitter feeds, HDMI, Dante interfaces.

**Synchronized remote outputs** independently reproduce media from media assets plus playback state, receiving logical playback information rather than PCM. Mobile or web listener applications are of this class.

**Stream outputs** carry real-time encoded or PCM audio — RTP, WebRTC, Icecast, NDI audio, AES67-style transports. These are secondary and must never be required for basic show playback.

Adapter capability metadata is honest about asymmetry rather than lowest-common-denominator. A synchronized remote output that cannot reproduce the local mix says so, and ShowMesh surfaces the limitation instead of hiding it:

```yaml
name: example-remote-listener
capabilities:
  synchronized_media: true
  pcm_stream: false
  mixing: unknown
  announcements: unknown
```

`unknown` is a legitimate value. An adapter whose behavior has not been established says so, and ShowMesh must not resolve that into an assumption on the adapter's behalf.

### 8.1 Output adapters are not control providers

An output adapter carries audio and sits in the media path. A control provider ([ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md)) is management-plane only and explicitly never in the timing path. They share a discipline — declare capabilities honestly, never fake support — and nothing else, and must not be unified.

The distinction is visible in the reference installation: the FM transmitter and the BSS BLU-DAN Dante master are *controlled devices* addressed by providers, while the interface output feeding the transmitter and the Dante transmit path are *output adapters*. The same physical signal chain crosses both models, on purpose.

## 9. Mixing and priority

Minimum required behavior: show audio can replace or duck background audio; announcements can duck show or background; gain transitions use configurable fades; background playback can optionally resume from its previous position; and output adapters may implement reduced functionality where they cannot reproduce the full local mix.

```text
background playing
       |  show begins
       v
fade/duck background, play show audio
       |  announcement
       v
duck program, mix announcement
       |
       v
restore program
```

## 10. Media management

Each eligible audio node maintains local copies of required media. ShowMesh tracks media ID, path, hash, size, duration, version, and per-node availability.

Before assigning audio authority to a node, ShowMesh verifies that the required assets are present and match. Media synchronization is a separate concern from playback synchronization and must not be conflated with it: a node with stale media is not a node that has drifted, and the two failures need different responses.

## 11. Failure behavior

### 11.1 Output device loss

Loss of an audio device fails silent and reports critical degradation. The engine stops sending to the failed output, keeps session state alive, marks the output critical, notifies the coordinator, and continues unaffected outputs.

ShowMesh must never automatically hand audio back to FPP's own output. The reasoning and the consequences for [ADR-004](../decisions/ADR-004-layered-commands-and-fallback.md) are in [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md). Clean silence on a failed output is preferable to an uncontrolled path into an FM transmitter.

### 11.2 Individual output failure

Other outputs continue, the session remains active, ShowMesh reports degraded audio state, and the failed output can be restarted or recovered independently.

### 11.3 Node failure

If the active audio node fails, ShowMesh detects the loss, identifies eligible standby nodes, verifies required media and output capabilities and physical routing and health, and a replacement node may assume audio authority, starting at the current authoritative show position.

Recovery prioritizes safe and deterministic behavior over seamless sample-accurate continuity. Audio will be interrupted; the goal is that it resumes correctly and audibly once, not that the seam is inaudible.

Scope note: ARCHITECTURE §12 defers automatic failover during a live set until evidence supports it, and reassignable capability workloads are Phase 3. Until then this is an operator-initiated procedure with ShowMesh performing the eligibility verification, not an automatic one. Making it automatic requires the deferred roadmap item, the verification gates above, and RES-009 failure-injection evidence.

## 12. Platform

Linux is the reference and supported platform for the audio node: FPP ecosystem compatibility, predictable headless operation, the existing agent deployment model, GStreamer, PipeWire and ALSA, straightforward service supervision, and no dependence on desktop-session behavior.

Windows audio nodes may be supported later where a specific hardware or software dependency requires it. Windows is a platform exception, not the baseline.

Dante must not dictate the audio node's operating system. Where a Dante implementation requires Windows, the Dante *output capability* may be placed on a separate Windows node rather than moving the authoritative Audio Engine onto Windows.

That bridge has a cost the spec it came from did not state: carrying program audio from a Linux audio node to a Windows Dante node is real-time audio transport between ShowMesh nodes, which §4.1 excludes from the show-audio path and §16 defers, and it introduces the second independent clock domain that §5 exists to avoid. The Windows Dante bridge is therefore not a free deployment option. It is a deferred configuration requiring its own transport decision and its own clock-domain analysis, and it is not part of the initial scope. A Linux-native Dante path, if one becomes viable, removes the problem rather than solving it.

## 13. Capabilities

Audio capabilities are advertised independently, so that placement follows what a node can actually do:

```text
audio.engine
audio.output.local
audio.output.fm
audio.output.ltc
audio.output.dante
```

A node provides any subset. One node may run the engine with local, FM, and LTC outputs while another provides only Dante. Additional `audio.output.*` identifiers are added when an output adapter exists to advertise them.

ShowMesh determines placement from hardware, required clock relationships, media availability, physical routing, node health, and output dependencies. Clock relationships are a placement input, not an afterthought: ADR-018 means `audio.output.ltc` and the program output must land on the same interface, which can force both onto one node regardless of what other nodes could otherwise provide.

These identifiers replace the earlier audio entries in the ARCHITECTURE §6 vocabulary.

## 14. Control surface

Representative operations: `play(media, source, position, outputs)`, `stop(session)`, `seek(session, position)`, `announce(media, priority, duck)`, `set_gain(output, gain)`, `mute(output)`.

Representative session state:

```json
{
  "session": "show-1234",
  "source": "show",
  "media": "songs/song01.flac",
  "state": "playing",
  "position": 42.183,
  "reference_position": 42.179,
  "drift_ms": 4
}
```

These are illustrative shapes, not the wire contract. The actual command envelope follows ARCHITECTURE §8.1 and the ADR-008 topic conventions, and any operator-facing exposure follows the public API contract in [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md).

## 15. Telemetry

The engine reports current media, playback position, reference show position, drift, active clock source and domain, audio device and device health, output state, output latency, underruns, media availability, active sources, mix state, effective output gain, LTC state, and failover eligibility.

This feeds the desired-versus-observed model directly. Two rules from [ADR-011](../decisions/ADR-011-context-aware-observability.md) apply with particular force here: stale telemetry is `unknown` rather than healthy, and a session reported as `playing` is a claim about the engine's state, not proof that an audience heard anything. Evidence that audio reached an output is a different signal from evidence that a session exists.

## 16. Initial scope

Version 1: a Linux audio node; local-file playback; FPP show-state synchronization; a stable independent playback clock; the stereo program bus and mono LTC bus; physical multichannel output; FM transmitter routing; background audio; announcements; ducking and fades; playback telemetry; the output adapter framework; media synchronization between nodes; and safe audio-device-loss handling.

Deferred: Dante as a required transport, synchronized remote outputs, real-time PCM streaming between ShowMesh nodes (including the Windows Dante bridge in §12), sample-transparent node failover, multi-zone audio, and dynamic clock-rate correction.

## 17. Open questions

- **GStreamer viability** for click-free ducking, gapless playback, and LTC sample-aligned to program on one clock. [RES-007](../research/RES-007-audio-node-architecture.md), first bench item.
- **Tolerable drift**, both the perceptual threshold against lighting and what a free-running node actually accumulates over a show. RES-007.
- **Audio interface selection.** Channel count and model are unpurchased, and ADR-018's shared-clock requirement is a purchasing constraint, not just a configuration one.
- **LTC frame rate configuration** and its relationship to the Resolume input configuration ([RES-001](../research/RES-001-resolume-smpte-behavior.md)).
- **Announcement policy** — whether announcements ever interrupt show audio or only duck it, which is a show-design decision with a technical consequence.

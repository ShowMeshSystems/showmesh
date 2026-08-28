# ADR-017: ShowMesh Owns Audience-Facing Audio, and Audio Nodes Play Local Media

Status: Accepted  
Date: 2026-08-10  
Amended: 2026-08-28, [ADR-046](ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md) narrows "playback-rate manipulation is avoided" to slews and seeks; a bounded ppm-scale rate trim against a locked shared PTP clock is permitted. Following a position feed remains rejected.

## Context

ARCHITECTURE §4.5 already states that audio is a first-class node capability rather than a mandatory responsibility of the primary FPP controller, but it never says who is *authoritative* for audio output, and [RES-007](../research/RES-007-audio-node-architecture.md) — critical-risk, L0 — lists clock ownership as its first open question. Implementation cannot start against an undecided authority boundary.

Two questions have to be answered together, because the answer to each constrains the other: who owns audience-facing audio, and does a node follow a sample stream or play its own copy of the media.

The reference installation makes the stakes concrete. Program audio drives an FM transmitter, LTC drives Resolume, and background music must run outside scheduled shows. FPP's own audio output is stereo from the primary controller, which cannot serve a separate LTC channel from the same clock and has no model for background music, ducking, or announcements.

[ADR-001](ADR-001-fpp-is-authoritative.md) makes FPP the authoritative scheduler. That decision is about scheduling, playlist order, and sequence execution. It has never covered which device converts show state into sound.

## Decision

**ShowMesh is authoritative for audience-facing audio.** It owns audio sessions, routing, output assignment, background audio, announcements, mixing and ducking, audio-node placement, and audio health. FPP remains authoritative for scheduler state, playlist and show selection, sequence execution, and the sequence timeline, exactly as ADR-001 requires. FPP tells ShowMesh what the show is doing; the Audio Engine decides how it is rendered and distributed.

**Audio nodes play complete local media files against their own audio clock.** ShowMesh supplies media identity, start and stop, desired position, show start time, and authoritative show position. The node starts or seeks its matching local file and plays it locally. It does not consume a continuous PCM or sample-position feed for normal show playback.

**The active audio node is authoritative for its own playback clock, PCM rendering, and local device state.** Show synchronization and audio clock synchronization are separate problems.

**Drift is measured, not chased.** Alignment is accurate at start; drift below a configured threshold is ignored; correction is preferred at track boundaries and applied as a discrete seek when operationally significant; playback-rate manipulation is avoided. The Audio Engine does not continuously discipline its clock with MultiSync-style corrections. (ADR-046: a ppm-scale rate trim of the whole interface against a locked shared PTP clock is not such a correction; slews and seeks still are.)

Real-time audio streaming may exist later as a separate input/output capability. It is not the synchronized show-audio architecture.

## Consequences

- The coordinator and the network stay out of the real-time audio path, preserving the standing constraint that they are never in the timing or media path, and audio survives coordinator loss and broker loss for the duration of media already playing.
- Media distribution becomes a precondition for audio authority. A node without verified local assets cannot hold the audio role, so media synchronization, hashing, and per-node availability tracking become required infrastructure rather than a convenience. ARCHITECTURE §4.3's media cache carries real weight here.
- **Audio deliberately diverges from the FPP remote sync model.** `pkg/multisync` implements slew-and-jump semantics that are correct for pixels and audible in program audio. Audio must not be "corrected" to match it. This divergence is a design decision, not an inconsistency to be tidied up.
- Drift becomes a first-class telemetry signal with an operational threshold, and that threshold is unknown until bench and field measurement. RES-007 must produce it.
- FPP's own audio output must be disabled or unused on the primary controller in a ShowMesh deployment, otherwise two systems produce audience audio. This is a deployment requirement and a documentation obligation, and it is the constraint any third-party audio integration on the FPP host has to be reconciled against.
- ShowMesh now owns something an audience hears directly. Failures here are not degraded observability, they are silence or noise in front of people, which is why [ADR-019](ADR-019-audio-device-loss-fails-silent.md) governs failure behavior separately.
- Nothing in this decision moves sample generation into ShowMesh code. Rendering stays in GStreamer per [ADR-007](ADR-007-gstreamer-media-engine.md).

## Alternatives considered

**Leaving audio with FPP** was rejected because it cannot deliver LTC on a discrete channel from the same clock as program audio, has no background-music, ducking, or announcement model, and ties audio output to the pixel controller's physical location. It remains the conservative position, and if RES-007's bench work shows the node path cannot meet the acceptance criteria, this decision must be superseded rather than worked around.

**Streaming PCM from the coordinator or from FPP to audio nodes** was rejected because it puts the network and the coordinator inside the real-time audio path, makes every audio dropout a network event, imports MultiSync-rate correction artifacts into program audio, and removes the failure isolation that local playback provides. It also contradicts the standing constraint that a running show survives coordinator loss.

**Following a continuous time-position feed with rate correction**, the way an FPP remote follows MultiSync, was rejected for program audio specifically: continuous rate manipulation is audible, and continuous position chasing produces repeated small seeks. The correct behavior for pixels is the wrong behavior for sound. This rejection is about chasing a position feed with frame-scale slews; ADR-046 distinguishes it from rate-locking to a shared clock.

## Related research

[Audio-node architecture](../research/RES-007-audio-node-architecture.md) · [Resolume SMPTE behavior](../research/RES-001-resolume-smpte-behavior.md) · [Failure-mode testing](../research/RES-009-failure-mode-testing.md)

RES-007 must establish the drift budget and the GStreamer viability question before this decision reaches integrated verification. RES-009 must cover coordinator loss during playback, media missing at assignment time, and node loss mid-show.

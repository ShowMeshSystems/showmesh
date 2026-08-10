# ADR-018: Program Audio and LTC Share One Clock Domain

Status: Accepted  
Date: 2026-08-10

## Context

Resolume takes timecode as audio LTC on a per-clip basis ([RES-001](../research/RES-001-resolume-smpte-behavior.md)), so the projection timeline is driven by a signal that must stay phase-related to program audio. If program and LTC are generated from independent hardware clocks, they drift apart at the difference between two crystals — small, constant, and cumulative over a show, with no error reported by anything, because each device is doing exactly what it was told.

The reference installation makes this a live risk rather than a theoretical one. It records a likely Focusrite-class USB interface *or* a Dante Virtual Soundcard, with the LTC path as "its own output on a physical or its own output on dante," and a BSS BLU-DAN as Dante master. Those options can be combined into exactly the arrangement that fails quietly, and the audio interface has not been purchased yet, so the decision is still free.

## Decision

Program audio and LTC must be generated from, and leave through, one clock domain wherever their timing relationship matters — which, for a show driving Resolume from LTC, is always.

The preferred deployment puts program audio and LTC on one hardware interface and therefore one hardware clock:

- channels 1–2: stereo program;
- channel 3: LTC, on a discrete physical output, never mixed into audience-facing program audio;
- a fourth channel reserved where practical for future routing or monitoring.

If both signals eventually run over Dante they must be in the same Dante clock domain. The arrangement explicitly excluded is program audio on one interface and LTC on an independent one — a USB interface for program with LTC on Dante, or the reverse.

ShowMesh models three distinct clocks and never assumes they are the same: the show timeline clock, the audio playback and device clock, and the network audio clock where one exists.

## Consequences

- **This is a hardware purchasing constraint, not only a configuration one.** The audio interface must provide at least three usable output channels from one clock. Choosing a stereo interface and adding LTC elsewhere later would violate this decision after the money is spent.
- Clock relationship becomes an input to capability placement. `audio.output.ltc` and the program output must land on the same interface, which can pin both to one node even when other nodes could otherwise host one of them. Placement logic must treat this as a hard constraint rather than a preference.
- Dante cannot be a required transport for basic Audio Engine operation, because making it required would force the LTC path onto the network clock. Dante remains an additional output.
- The Windows Dante bridge sketched for deployments where Dante needs Windows is constrained by this decision: bridging program audio to a separate Dante node introduces the second independent clock domain this ADR exists to prevent, and is therefore deferred rather than available (AUDIO-ENGINE §12).
- Drift between program and LTC becomes a measurable readiness check rather than something noticed when projection looks wrong. It belongs in the readiness evidence of OBSERVABILITY §10.
- If a future deployment genuinely needs program and LTC on separate clocks, this requires a superseding ADR and a demonstrated synchronization mechanism, not a configuration option.

## Alternatives considered

**Allowing independent clocks and correcting in software** was rejected because the correction would have to be continuous rate adjustment or repeated seeks on one of the two signals, and both are the failure modes [ADR-017](ADR-017-showmesh-owns-audience-audio.md) rules out for program audio. It also converts a hardware guarantee into a running software obligation with no failure signal when it stops working.

**Deriving LTC from the show timeline independently of the audio device** was rejected for the same reason: the resulting LTC is phase-related to the coordinator's idea of the timeline rather than to the sound the audience hears, which is the relationship that actually matters for projection.

**Mixing LTC into a program channel** and splitting it downstream was rejected outright. It risks timecode reaching the FM transmitter and the audience, and it consumes a program channel for a signal that must never be audible.

**Deferring the decision until the interface is purchased** was rejected because the decision is what should determine the purchase.

## Related research

[Audio-node architecture](../research/RES-007-audio-node-architecture.md) · [Resolume SMPTE behavior](../research/RES-001-resolume-smpte-behavior.md) · [Reference installation](../reference-installation.md)

RES-007 must measure achieved program-to-LTC alignment on the selected interface and over a full-length show. RES-001 must confirm Resolume's behavior on LTC loss and reacquisition, which determines how bad a clock fault looks from the audience side.

# RES-007: Audio-Node Architecture

[Architecture](../architecture/ARCHITECTURE.md#45-audio-engine) · [Tracker](README.md) · [Resolume SMPTE research](RES-001-resolume-smpte-behavior.md)

Status: unresearched · Risk: critical · Verification: L0

## Decision to make

Define clock ownership, playback, mixing, routing, LTC generation, background-music transitions, and local fallback for an audio-capable media node.

## Required use cases

- Show audio synchronized to the FPP timeline.
- Background and ambient music outside scheduled shows.
- Deterministic crossfades into pre-show, live, intermission, and post-show states.
- Independent LTC output without consuming the main stereo program pair.
- Multichannel USB interfaces and optional Dante routing.
- Metering, device health, underrun reporting, and local recovery.

## Questions

- Which component owns the audio sample clock and authoritative playback position?
- Does the node play complete show media or follow a time-position stream?
- Which engine provides gapless playback, fades, routing, device hot-plug, and recovery?
- How are program audio and LTC kept phase-related?
- What state remains available if the coordinator, FPP link, or audio device disappears?
- Can useful ideas be reused from BackgroundMusicFPP without making it a hard dependency?

## Acceptance criteria

Transitions are click-free, repeatable, and measurable; LTC remains valid and aligned with program audio; device loss is detected promptly; coordinator loss does not interrupt current playback; show start and recovery behavior are defined; and an overnight background/show cycle soak has no leaks or accumulating offset.

## Test method

Prototype the minimum engine on the intended Linux host and interface. Record program audio, LTC, and a visual reference. Exercise every lifecycle transition plus coordinator loss, timing loss, device unplug/replug, process restart, missing media, Dante interruption where applicable, and power restoration.

## Evidence and findings

No evidence collected.

## Decision, fallback, and revalidation

Decision pending. FPP-hosted stereo playback remains the conservative fallback until the node path reaches integrated verification. Revalidate after audio engine, kernel, driver, interface, or timing-source changes.
